// Package service 实现版本制品包本地磁盘分片续传合并、SHA-256 完整性校验、HTTP Range 断点下载以及保留最近 N 个版本的自动清理策略（5.1 与 8.10 节）。
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"autoship/internal/config"
	"autoship/pkg/envelope"
)

// uploadSession 保存在内存中尚未合并完成的单次分片上传会话元数据。
type uploadSession struct {
	UploadID     string
	ReleaseID    uint64
	ProductCode  string
	Version      string
	TotalSize    int64
	ExpectedHash string
	ManifestJSON string
	TempDir      string
}

// Service 管理制品包分片接收、落盘存储、Range 下载与历史版本淘汰。
type Service struct {
	cfg     *config.ServerConfig
	mu      sync.Mutex
	uploads map[string]*uploadSession
}

// New 创建制品存储服务实例。
// 参数 cfg 为全局配置指针；返回 *Service。
func New(cfg *config.ServerConfig) *Service {
	return &Service{
		cfg:     cfg,
		uploads: make(map[string]*uploadSession),
	}
}

// InitUpload 开启一次制品包分片续传会话并返回 uploadId。
// 参数 releaseID 为版本主键，productCode/version 为安全标识，totalSize/expectedSHA256 为整包大小和摘要，manifestJSON 为已签名清单原文；返回 uploadId 与错误。
func (s *Service) InitUpload(releaseID uint64, productCode, version string, totalSize int64, expectedSHA256, manifestJSON string) (string, error) {
	if err := envelope.ValidateSafeID(productCode); err != nil {
		return "", err
	}
	if totalSize <= 0 {
		return "", errors.New("制品包总大小必须大于 0")
	}
	uid := "up_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	tmpDir := filepath.Join(s.cfg.ArtifactDir(), "_tmp", uid)
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return "", fmt.Errorf("创建分片临时目录失败: %w", err)
	}
	s.mu.Lock()
	s.uploads[uid] = &uploadSession{
		UploadID:     uid,
		ReleaseID:    releaseID,
		ProductCode:  productCode,
		Version:      version,
		TotalSize:    totalSize,
		ExpectedHash: strings.ToLower(strings.TrimSpace(expectedSHA256)),
		ManifestJSON: manifestJSON,
		TempDir:      tmpDir,
	}
	s.mu.Unlock()
	return uid, nil
}

// WriteChunk 保存指定分片序号的原始字节数据（重传同一分片幂等覆盖）。
// 参数 releaseID 为版本 ID，uploadID 为上传会话 ID，index 为从 0 开始的分片号，data 为分片字节；返回当前分片写入字节数与错误。
func (s *Service) WriteChunk(releaseID uint64, uploadID string, index int, data []byte) (int64, error) {
	if index < 0 || index > 100000 {
		return 0, errors.New("分片序号超出合法范围")
	}
	s.mu.Lock()
	sess, ok := s.uploads[uploadID]
	s.mu.Unlock()
	if !ok || sess.ReleaseID != releaseID {
		return 0, errors.New("上传会话不存在或已失效")
	}
	partPath := filepath.Join(sess.TempDir, fmt.Sprintf("%06d.part", index))
	if err := os.WriteFile(partPath, data, 0600); err != nil {
		return 0, fmt.Errorf("保存分片 %d 失败: %w", index, err)
	}
	return int64(len(data)), nil
}

// CompleteUpload 按序合并全部分片，校验整包总字节数与 SHA-256 哈希，原子移动到正式制品目录并清理历史多余版本。
// 参数 releaseID 为版本 ID，uploadID 为会话 ID；返回相对存储路径、实际大小、SHA-256、manifestJSON 及错误。
func (s *Service) CompleteUpload(releaseID uint64, uploadID string) (relPath string, size int64, sha256Hex string, manifestJSON string, err error) {
	s.mu.Lock()
	sess, ok := s.uploads[uploadID]
	if ok {
		delete(s.uploads, uploadID)
	}
	s.mu.Unlock()
	if !ok || sess.ReleaseID != releaseID {
		return "", 0, "", "", errors.New("上传会话不存在或已完成")
	}
	defer os.RemoveAll(sess.TempDir)

	entries, err := os.ReadDir(sess.TempDir)
	if err != nil || len(entries) == 0 {
		return "", 0, "", "", errors.New("未找到任何已上传的分片")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".part") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	relPath = filepath.Join(sess.ProductCode, fmt.Sprintf("release-%d-%s.tar.gz", releaseID, sess.Version))
	finalAbs := filepath.Join(s.cfg.ArtifactDir(), relPath)
	if err := os.MkdirAll(filepath.Dir(finalAbs), 0755); err != nil {
		return "", 0, "", "", err
	}
	stagingFile := finalAbs + ".assembling"
	out, err := os.OpenFile(stagingFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return "", 0, "", "", err
	}

	hasher := sha256.New()
	var written int64
	for _, name := range names {
		pf, openErr := os.Open(filepath.Join(sess.TempDir, name))
		if openErr != nil {
			_ = out.Close()
			_ = os.Remove(stagingFile)
			return "", 0, "", "", openErr
		}
		n, copyErr := io.Copy(io.MultiWriter(out, hasher), pf)
		_ = pf.Close()
		if copyErr != nil {
			_ = out.Close()
			_ = os.Remove(stagingFile)
			return "", 0, "", "", copyErr
		}
		written += n
	}
	_ = out.Close()

	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if written != sess.TotalSize {
		_ = os.Remove(stagingFile)
		return "", 0, "", "", fmt.Errorf("合并后制品大小不匹配: 期望 %d，实际 %d", sess.TotalSize, written)
	}
	if sess.ExpectedHash != "" && actualHash != sess.ExpectedHash {
		_ = os.Remove(stagingFile)
		return "", 0, "", "", fmt.Errorf("合并后制品 SHA-256 不匹配: 期望 %s，实际 %s", sess.ExpectedHash, actualHash)
	}
	if err := os.Rename(stagingFile, finalAbs); err != nil {
		_ = os.Remove(stagingFile)
		return "", 0, "", "", err
	}

	s.pruneOldArtifacts(sess.ProductCode)
	return relPath, written, actualHash, sess.ManifestJSON, nil
}

// AbsolutePath 将制品相对存储路径转换为磁盘绝对路径。
// 参数 relPath 为相对路径；返回绝对路径。
func (s *Service) AbsolutePath(relPath string) string {
	return filepath.Join(s.cfg.ArtifactDir(), relPath)
}

// ServeFileWithRange 使用标准库 http.ServeContent 对更新 Agent 提供支持 HTTP `Range: bytes=start-end` 的断点续传下载服务（9.4 第 3 步）。
// 参数 w/r 为 HTTP 响应与请求，relPath 为制品包相对路径，downloadName 为响应文件名；失败时返回错误供控制器输出 JSON。
func (s *Service) ServeFileWithRange(w http.ResponseWriter, r *http.Request, relPath, downloadName string) error {
	abs := s.AbsolutePath(relPath)
	f, err := os.Open(abs)
	if err != nil {
		return errors.New("制品包文件不存在或已被淘汰清理")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(downloadName)))
	http.ServeContent(w, r, downloadName, info.ModTime(), f)
	return nil
}

// pruneOldArtifacts 按配置的 ArtifactKeepCount 保留某产品最近 N 个版本制品包，超出部分自动从本地磁盘删除；参数 productCode 为产品编码，由 CompleteUpload 内部调用。
func (s *Service) pruneOldArtifacts(productCode string) {
	keep := s.cfg.ArtifactKeepCount
	if keep <= 0 {
		keep = 10
	}
	prodDir := filepath.Join(s.cfg.ArtifactDir(), productCode)
	entries, err := os.ReadDir(prodDir)
	if err != nil {
		return
	}
	type fileWithID struct {
		path string
		id   uint64
	}
	var files []fileWithID
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		// 文件名形如 release-<id>-vX.Y.Z.tar.gz
		trimmed := strings.TrimPrefix(e.Name(), "release-")
		dashIdx := strings.Index(trimmed, "-")
		var id uint64
		if dashIdx > 0 {
			id, _ = strconv.ParseUint(trimmed[:dashIdx], 10, 64)
		}
		files = append(files, fileWithID{path: filepath.Join(prodDir, e.Name()), id: id})
	}
	if len(files) <= keep {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].id > files[j].id })
	for _, old := range files[keep:] {
		_ = os.Remove(old.path)
	}
}
