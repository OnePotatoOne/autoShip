// Package daemon 中的 journal.go 管理本地身份文件（identity.json）、操作状态日志（journal.json）、备份占用索引及防并发部署文件锁（deploy.lock）。
package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"

	"autoship/internal/updateagent/config"
)

// ErrDeployLocked 表示本机已有另一个部署、回滚或数据库备份流程正在执行，拒绝并发操作。
var ErrDeployLocked = errors.New("当前已有其他部署或回滚任务正在运行（文件锁占用）")

// StateStore 线程安全地管理 autoship-agent 的本地持久化文件与跨进程部署排他锁。
type StateStore struct {
	cfg      *config.AgentConfig // Agent 配置对象
	mu       sync.Mutex          // 保护 journal 与备份索引并发读写
	inFlight atomic.Bool         // 进程内快速并发部署互斥标志
}

// NewStateStore 创建并初始化本地状态存储目录结构。
// 参数 cfg 为已校验的 *config.AgentConfig；返回 *StateStore 实例及目录创建错误。
func NewStateStore(cfg *config.AgentConfig) (*StateStore, error) {
	if err := os.MkdirAll(cfg.StateDir, 0755); err != nil {
		return nil, fmt.Errorf("创建状态目录 %s 失败: %w", cfg.StateDir, err)
	}
	if err := os.MkdirAll(cfg.StagingDir, 0755); err != nil {
		return nil, fmt.Errorf("创建暂存目录 %s 失败: %w", cfg.StagingDir, err)
	}
	return &StateStore{cfg: cfg}, nil
}

// LoadIdentity 读取本地持久化的 Agent Ed25519 身份文件；若尚未注册（文件不存在）返回 (nil, nil)。
// 无入参；返回 *Identity 及读取/解析错误，供启动时判断是否需要执行首次注册。
func (s *StateStore) LoadIdentity() (*Identity, error) {
	path := s.cfg.IdentityFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取身份文件失败: %w", err)
	}
	var id Identity
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, fmt.Errorf("解析身份文件 JSON 失败: %w", err)
	}
	if id.AgentID == "" || id.PrivateKeyB64 == "" {
		return nil, nil
	}
	return &id, nil
}

// SaveIdentity 将首次注册获得的 Agent 身份及 Ed25519 私钥以 0600 权限原子落盘。
// 参数 id 为待保存的身份结构指针；成功返回 nil，失败返回 IO 错误。
func (s *StateStore) SaveIdentity(id *Identity) error {
	return writeJSONAtomic(s.cfg.IdentityFilePath(), id, 0600)
}

// LoadJournal 加载本地 journal.json 操作日志（文件不存在时自动初始化空结构）。
// 无入参；返回 *Journal 副本及解析错误，通过 store.LoadJournal() 调用。
func (s *StateStore) LoadJournal() (*Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadJournalUnlocked()
}

// UpdateJournal 在互斥锁保护下读取、修改并原子持久化 journal.json。
// 参数 mutator 为原地修改 *Journal 的闭包函数；成功返回 nil，失败返回错误。
func (s *StateStore) UpdateJournal(mutator func(j *Journal)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.loadJournalUnlocked()
	if err != nil {
		return err
	}
	mutator(j)
	return writeJSONAtomic(s.cfg.JournalFilePath(), j, 0600)
}

// loadJournalUnlocked 在已持有互斥锁的前提下从磁盘加载或初始化 Journal 对象。
// 无入参；返回 *Journal 及错误。
func (s *StateStore) loadJournalUnlocked() (*Journal, error) {
	path := s.cfg.JournalFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Journal{
				ProcessedMessages: make(map[string]string),
				Deployments:       make(map[string]JournalRecord),
			}, nil
		}
		return nil, fmt.Errorf("读取 journal.json 失败: %w", err)
	}
	var j Journal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("解析 journal.json 失败: %w", err)
	}
	if j.ProcessedMessages == nil {
		j.ProcessedMessages = make(map[string]string)
	}
	if j.Deployments == nil {
		j.Deployments = make(map[string]JournalRecord)
	}
	return &j, nil
}

// AcquireDeployLock 同时获取进程内互斥标志与基于 syscall.Flock 的非阻塞跨进程文件锁（9.4 第 2 步防并发部署）。
// 无入参；加锁成功返回幂等释放闭包与 nil，若已有并发任务运行则返回 ErrDeployLocked。
func (s *StateStore) AcquireDeployLock() (func(), error) {
	if !s.inFlight.CompareAndSwap(false, true) {
		return nil, ErrDeployLocked
	}

	lockPath := s.cfg.DeployLockFilePath()
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		s.inFlight.Store(false)
		return nil, fmt.Errorf("创建锁文件目录失败: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.inFlight.Store(false)
		return nil, fmt.Errorf("打开部署锁文件失败: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		s.inFlight.Store(false)
		return nil, ErrDeployLocked
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
			_ = f.Close()
			s.inFlight.Store(false)
		})
	}
	return release, nil
}

// RecordBackupSize 将 autoship-apply backup 返回的单次部署数据库备份总字节数记录到本地索引（解决 0700 目录对非特权账号不可遍历时的容量统计）。
// 参数 deploymentID 为部署记录 ID，totalBytes 为该次备份字节数；成功返回 nil，失败返回错误。
func (s *StateStore) RecordBackupSize(deploymentID string, totalBytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.loadBackupIndexUnlocked()
	idx[deploymentID] = totalBytes
	return writeJSONAtomic(s.cfg.BackupIndexFilePath(), idx, 0600)
}

// LatestBackupBytes 返回最近一次或最大单次历史数据库备份字节数，用于下次备份前的动态磁盘预估（9.5 节）。
// 无入参；返回字节数（无历史记录时返回 0）。
func (s *StateStore) LatestBackupBytes() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := s.loadBackupIndexUnlocked()
	var maxVal int64
	for _, sz := range idx {
		if sz > maxVal {
			maxVal = sz
		}
	}
	if maxVal <= 0 {
		return 0
	}
	return uint64(maxVal)
}

// ComputeBackupUsedBytes 统计指定产品数据库备份专用目录（/var/backups/autoship/<productCode>）当前占用的总字节数。
// 对可读目录直接遍历求和；对因 root 0700 权限无法读取内容的子目录回退使用本地备份索引（若子目录已被管理员手动删除则自动剔除计数）。
// 参数 productCode 为产品唯一编码；返回已占用总字节数（int64）。
func (s *StateStore) ComputeBackupUsedBytes(productCode string) int64 {
	if productCode == "" {
		return 0
	}
	prodBackupRoot := filepath.Join(s.cfg.BackupRootDir, productCode)
	entries, err := os.ReadDir(prodBackupRoot)
	if err != nil {
		return 0
	}

	s.mu.Lock()
	idx := s.loadBackupIndexUnlocked()
	s.mu.Unlock()

	var total int64
	for _, entry := range entries {
		subPath := filepath.Join(prodBackupRoot, entry.Name())
		if !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil {
				total += info.Size()
			}
			continue
		}
		var subBytes int64
		permDenied := false
		_ = filepath.WalkDir(subPath, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if errors.Is(walkErr, os.ErrPermission) {
					permDenied = true
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() && d.Type().IsRegular() {
				if fi, fiErr := d.Info(); fiErr == nil {
					subBytes += fi.Size()
				}
			}
			return nil
		})
		if permDenied {
			if recorded, ok := idx[entry.Name()]; ok && recorded > 0 {
				total += recorded
			}
		} else {
			total += subBytes
		}
	}
	return total
}

// loadBackupIndexUnlocked 读取本地备份大小索引表（内部辅助函数，调用前需持锁）。
// 无入参；返回 deploymentId -> totalBytes 映射。
func (s *StateStore) loadBackupIndexUnlocked() map[string]int64 {
	out := make(map[string]int64)
	raw, err := os.ReadFile(s.cfg.BackupIndexFilePath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// writeJSONAtomic 将任意结构体序列化为格式化 UTF-8 JSON 并通过临时文件同步重命名原子写入目标路径。
// 参数 path 为目标文件路径，val 为待序列化对象，perm 为文件权限；成功返回 nil，失败返回错误。
func writeJSONAtomic(path string, val any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}
