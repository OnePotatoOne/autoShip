// Package workspace 管理 Mac 开发 Agent 的专用仓库克隆、独立 Git Worktree、锁文件 SHA-256 依赖缓存、按产品前缀隔离的 MySQL 测试库/预览库、动态端口池、自有进程 PID 登记表以及 caffeinate 防休眠守护（设计文档 8.1 & 8.2 节）。
package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"autoship/internal/devagent/config"
	"autoship/pkg/envelope"
)

// ErrUnregisteredPID 表示调用方试图终止一个未在本 Agent 进程登记表中注册的外部进程（严格禁止误杀用户个人进程）。
var ErrUnregisteredPID = errors.New("拒绝终止未由本 Agent 登记的外部进程 PID")

// ErrPortPoolExhausted 表示配置的动态端口池区间内已无可用空闲端口。
var ErrPortPoolExhausted = errors.New("动态端口池可用端口不足")

var (
	slugUnsafeRe   = regexp.MustCompile(`[^a-zA-Z0-9-]+`)
	dbSafeIdentRe  = regexp.MustCompile(`^[a-zA-Z0-9_]{1,60}$`)
	trackedLockSet = []string{"go.sum", "go.mod", "package-lock.json", "pnpm-lock.yaml", "yarn.lock"}
)

// WorktreeSession 描述已为单个开发任务创建完毕的隔离工作树与专属路径集合。
type WorktreeSession struct {
	ProductCode string // 产品唯一编码
	WorkID      string // 开发任务唯一标识
	BranchName  string // 任务分支名（形如 autoship/<workId>-<slug>）
	BaseCommit  string // 拉出分支时的基准提交哈希
	RepoDir     string // 产品专用克隆目录（~/autoship/repos/<product>）
	WorktreeDir string // 任务独立 worktree 目录（~/autoship/work/<product>/<workId>）
	TempDir     string // 任务专属临时目录
}

// CachePrepareResult 记录跨任务依赖缓存命中状态、锁文件摘要及注入给子进程的缓存环境变量。
type CachePrepareResult struct {
	CacheHit  bool              `json:"cacheHit"`  // 锁文件哈希是否命中已有就绪缓存
	Ecosystem string            `json:"ecosystem"` // 识别出的技术栈生态（go / node / mixed / none）
	LockHash  string            `json:"lockHash"`  // 锁文件内容组合的 64 位 SHA-256 摘要
	CacheDir  string            `json:"cacheDir"`  // 本产品依赖缓存根目录
	LockFiles []string          `json:"lockFiles"` // 参与计算哈希的相对锁文件列表
	EnvVars   map[string]string `json:"envVars"`   // 需注入构建/测试环境的 GOMODCACHE / GOCACHE / npm_config_cache 等变量
}

// InstallRunner 定义当锁文件缓存未命中时实际执行依赖安装命令的回调函数签名。
type InstallRunner func(ctx context.Context, worktreeDir string, env map[string]string, installCmd string) error

// Manager 统筹管理本地仓库克隆、任务 Worktree 生命周期与跨任务依赖缓存目录。
type Manager struct {
	rootDir string // autoShip 隔离工作根目录（如 ~/autoship，绝不触碰用户个人代码目录）
	mu      sync.Mutex
}

// NewManager 创建绑定到指定隔离根目录（如 ~/autoship）的工作区管理器。
// 参数 rootDir 为隔离根目录路径；返回初始化后的 *Manager 及目录创建错误。
func NewManager(rootDir string) (*Manager, error) {
	cleaned := config.ExpandPath(rootDir)
	if cleaned == "" {
		return nil, errors.New("workspace rootDir 不能为空")
	}
	for _, sub := range []string{"repos", "work", "cache", "tmp"} {
		if err := os.MkdirAll(filepath.Join(cleaned, sub), 0755); err != nil {
			return nil, fmt.Errorf("创建工作区子目录 %s 失败: %w", sub, err)
		}
	}
	return &Manager{rootDir: cleaned}, nil
}

// ProductRepoDir 返回指定产品专用克隆仓库的路径（`<rootDir>/repos/<productCode>`）。
// 参数 productCode 为产品标识；返回绝对路径字符串。
func (m *Manager) ProductRepoDir(productCode string) string {
	return filepath.Join(m.rootDir, "repos", productCode)
}

// TaskWorktreeDir 返回指定产品与开发任务的独立 worktree 路径（`<rootDir>/work/<productCode>/<workID>`）。
// 参数 productCode 为产品标识，workID 为任务标识；返回绝对路径字符串。
func (m *Manager) TaskWorktreeDir(productCode, workID string) string {
	return filepath.Join(m.rootDir, "work", productCode, workID)
}

// EnsureProductRepo 确保产品专用克隆 `~/autoship/repos/<product>` 已就绪并与远端同步（若已存在则执行 git fetch --all --prune）。
// 参数 ctx 为上下文，prod 为产品档案（支持 DeployKeyPath 专用推送/拉取密钥）；返回仓库本地路径及错误。
func (m *Manager) EnsureProductRepo(ctx context.Context, prod config.ProductProfile) (string, error) {
	if err := envelope.ValidateSafeID(prod.ProductCode); err != nil {
		return "", fmt.Errorf("产品编码不合法: %w", err)
	}
	if strings.TrimSpace(prod.RepoURL) == "" {
		return "", fmt.Errorf("产品 %s 缺少 RepoURL", prod.ProductCode)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	repoDir := m.ProductRepoDir(prod.ProductCode)
	gitEnv := buildGitEnv(prod.DeployKeyPath)

	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		// 仓库已存在，拉取最新远端引用并清理陈旧 worktree 元数据
		if _, fetchErr := runGit(ctx, repoDir, gitEnv, "fetch", "--all", "--prune"); fetchErr != nil {
			return "", fmt.Errorf("更新产品仓库 %s 失败: %w", prod.ProductCode, fetchErr)
		}
		_, _ = runGit(ctx, repoDir, gitEnv, "worktree", "prune")
		return repoDir, nil
	}

	if err := os.MkdirAll(filepath.Dir(repoDir), 0755); err != nil {
		return "", err
	}
	if _, err := runGit(ctx, "", gitEnv, "clone", prod.RepoURL, repoDir); err != nil {
		return "", fmt.Errorf("克隆产品仓库 %s 到专用目录失败: %w", prod.ProductCode, err)
	}
	return repoDir, nil
}

// CreateTaskWorktree 基于产品专用克隆创建独立任务 worktree `~/autoship/work/<product>/<workId>` 及分支 `autoship/<workId>-<slug>`。
// 参数 ctx 为上下文，prod 为产品档案，workID 为任务 ID，slug 为简述英文短语，baseRefOverride 为可选指定起点 commit/ref（留空取默认主分支）；返回 *WorktreeSession 及错误。
func (m *Manager) CreateTaskWorktree(ctx context.Context, prod config.ProductProfile, workID, slug, baseRefOverride string) (*WorktreeSession, error) {
	if err := envelope.ValidateSafeID(prod.ProductCode); err != nil {
		return nil, err
	}
	if err := envelope.ValidateSafeID(workID); err != nil {
		return nil, fmt.Errorf("workID 非法: %w", err)
	}
	repoDir, err := m.EnsureProductRepo(ctx, prod)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	gitEnv := buildGitEnv(prod.DeployKeyPath)
	defaultBranch := strings.TrimSpace(prod.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	baseRef := strings.TrimSpace(baseRefOverride)
	if baseRef == "" {
		// 优先使用远端最新主分支 origin/<defaultBranch>，本地纯仓库测试场景回退到 <defaultBranch> 或 HEAD
		if _, err := runGit(ctx, repoDir, gitEnv, "rev-parse", "--verify", "origin/"+defaultBranch); err == nil {
			baseRef = "origin/" + defaultBranch
		} else if _, err := runGit(ctx, repoDir, gitEnv, "rev-parse", "--verify", defaultBranch); err == nil {
			baseRef = defaultBranch
		} else {
			baseRef = "HEAD"
		}
	}

	baseSHA, err := runGit(ctx, repoDir, gitEnv, "rev-parse", baseRef)
	if err != nil {
		return nil, fmt.Errorf("解析基准分支/提交 %s 失败: %w", baseRef, err)
	}

	branchName := BuildTaskBranchName(workID, slug)
	worktreeDir := m.TaskWorktreeDir(prod.ProductCode, workID)
	// 若此前同 workID 残留旧目录，先安全摘除旧 worktree
	if _, statErr := os.Stat(worktreeDir); statErr == nil {
		_, _ = runGit(ctx, repoDir, gitEnv, "worktree", "remove", "--force", worktreeDir)
		_ = os.RemoveAll(worktreeDir)
	}
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0755); err != nil {
		return nil, err
	}

	if _, err := runGit(ctx, repoDir, gitEnv, "worktree", "add", "-B", branchName, worktreeDir, strings.TrimSpace(baseSHA)); err != nil {
		return nil, fmt.Errorf("创建 git worktree 失败: %w", err)
	}
	// 为独立 worktree 设置本地提交者身份（仅写仓库局部配置，绝不修改用户全局 ~/.gitconfig）
	_, _ = runGit(ctx, worktreeDir, gitEnv, "config", "user.name", "autoShip Dev Agent")
	_, _ = runGit(ctx, worktreeDir, gitEnv, "config", "user.email", "dev-agent@autoship.local")

	tempDir := filepath.Join(m.rootDir, "tmp", prod.ProductCode+"-"+workID)
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return nil, err
	}

	return &WorktreeSession{
		ProductCode: prod.ProductCode,
		WorkID:      workID,
		BranchName:  branchName,
		BaseCommit:  strings.TrimSpace(baseSHA),
		RepoDir:     repoDir,
		WorktreeDir: worktreeDir,
		TempDir:     tempDir,
	}, nil
}

// RemoveTaskWorktree 安全销毁指定任务的 git worktree 与任务专属临时目录。
// 参数 ctx 为上下文，productCode 为产品编码，workID 为任务 ID；成功或原本不存在均返回 nil。
func (m *Manager) RemoveTaskWorktree(ctx context.Context, productCode, workID string) error {
	if err := envelope.ValidateSafeID(productCode); err != nil {
		return err
	}
	if err := envelope.ValidateSafeID(workID); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	repoDir := m.ProductRepoDir(productCode)
	worktreeDir := m.TaskWorktreeDir(productCode, workID)
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); err == nil {
		_, _ = runGit(ctx, repoDir, nil, "worktree", "remove", "--force", worktreeDir)
		_, _ = runGit(ctx, repoDir, nil, "worktree", "prune")
	}
	_ = os.RemoveAll(worktreeDir)
	_ = os.RemoveAll(filepath.Join(m.rootDir, "tmp", productCode+"-"+workID))
	return nil
}

// BuildTaskBranchName 生成符合规范 `autoship/<workId>-<slug>` 的任务开发分支名。
// 参数 workID 为任务 ID，slug 为任务简述；返回清理非法字符并截断长度后的分支名称。
func BuildTaskBranchName(workID, slug string) string {
	cleanSlug := strings.ToLower(strings.TrimSpace(slug))
	cleanSlug = slugUnsafeRe.ReplaceAllString(cleanSlug, "-")
	cleanSlug = strings.Trim(cleanSlug, "-")
	if cleanSlug == "" {
		cleanSlug = "task"
	}
	if len(cleanSlug) > 32 {
		cleanSlug = strings.Trim(cleanSlug[:32], "-")
	}
	return fmt.Sprintf("autoship/%s-%s", workID, cleanSlug)
}

// ComputeLockfileHash 扫描 worktree 根目录及一级子目录中的锁文件（go.sum / package-lock.json 等）计算确定性 SHA-256 哈希。
// 参数 worktreeDir 为当前任务工作树路径；返回生态标识、64 位十六进制哈希、匹配到的相对锁文件列表及读取错误。
func ComputeLockfileHash(worktreeDir string) (ecosystem string, hashHex string, matched []string, err error) {
	var relCandidates []string
	// 检查根目录与常见前后端一级子目录（如 server/go.sum、web/package-lock.json）
	prefixes := []string{"", "server", "backend", "web", "frontend"}
	for _, pfx := range prefixes {
		for _, lf := range trackedLockSet {
			rel := lf
			if pfx != "" {
				rel = filepath.ToSlash(filepath.Join(pfx, lf))
			}
			full := filepath.Join(worktreeDir, filepath.FromSlash(rel))
			if st, statErr := os.Stat(full); statErr == nil && !st.IsDir() {
				relCandidates = append(relCandidates, rel)
			}
		}
	}
	sort.Strings(relCandidates)
	if len(relCandidates) == 0 {
		return "none", "", nil, nil
	}

	hasGo := false
	hasNode := false
	hasher := sha256.New()
	for _, rel := range relCandidates {
		data, readErr := os.ReadFile(filepath.Join(worktreeDir, filepath.FromSlash(rel)))
		if readErr != nil {
			return "", "", nil, fmt.Errorf("读取锁文件 %s 失败: %w", rel, readErr)
		}
		_, _ = hasher.Write([]byte(rel + "\n"))
		_, _ = hasher.Write(data)
		_, _ = hasher.Write([]byte("\n---\n"))
		if strings.HasSuffix(rel, "go.sum") || strings.HasSuffix(rel, "go.mod") {
			hasGo = true
		} else {
			hasNode = true
		}
	}

	switch {
	case hasGo && hasNode:
		ecosystem = "mixed"
	case hasGo:
		ecosystem = "go"
	default:
		ecosystem = "node"
	}
	return ecosystem, hex.EncodeToString(hasher.Sum(nil)), relCandidates, nil
}

// PrepareDependencyCache 基于锁文件 SHA-256 跨任务复用依赖缓存（锁文件未变则跳过重复安装，Node 项目自动链接缓存 node_modules）。
// 参数 ctx 为上下文，productCode 为产品编码，worktreeDir 为任务目录，installCmd 为安装依赖命令，runner 为未命中时的执行器；返回 *CachePrepareResult 及错误。
func (m *Manager) PrepareDependencyCache(ctx context.Context, productCode, worktreeDir, installCmd string, runner InstallRunner) (*CachePrepareResult, error) {
	if err := envelope.ValidateSafeID(productCode); err != nil {
		return nil, err
	}
	prodCacheRoot := filepath.Join(m.rootDir, "cache", productCode)
	goModCache := filepath.Join(prodCacheRoot, "gomod")
	goBuildCache := filepath.Join(prodCacheRoot, "gobuild")
	npmCache := filepath.Join(prodCacheRoot, "npm")
	for _, d := range []string{goModCache, goBuildCache, npmCache} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return nil, err
		}
	}

	envVars := map[string]string{
		"GOMODCACHE":       goModCache,
		"GOCACHE":          goBuildCache,
		"npm_config_cache": npmCache,
	}

	eco, lockHash, lockFiles, err := ComputeLockfileHash(worktreeDir)
	if err != nil {
		return nil, err
	}
	res := &CachePrepareResult{
		Ecosystem: eco,
		LockHash:  lockHash,
		CacheDir:  prodCacheRoot,
		LockFiles: lockFiles,
		EnvVars:   envVars,
	}
	if lockHash == "" {
		// 仓库中无任何锁文件，若声明了安装命令则按常规执行一次
		if strings.TrimSpace(installCmd) != "" && runner != nil {
			if err := runner(ctx, worktreeDir, envVars, installCmd); err != nil {
				return nil, err
			}
		}
		return res, nil
	}

	snapDir := filepath.Join(prodCacheRoot, "snapshots", lockHash)
	readyMarker := filepath.Join(snapDir, ".ready")
	if _, err := os.Stat(readyMarker); err == nil {
		// 命中锁文件 SHA-256 缓存：恢复各 package-lock.json 所在目录的 node_modules 符号链接后直接返回
		if err := restoreNodeModulesLinks(snapDir, worktreeDir, lockFiles); err == nil {
			res.CacheHit = true
			return res, nil
		}
	}

	// 缓存未命中：执行依赖安装命令并在成功后归档快照与标记
	if strings.TrimSpace(installCmd) != "" && runner != nil {
		if err := runner(ctx, worktreeDir, envVars, installCmd); err != nil {
			return nil, fmt.Errorf("执行依赖安装命令失败: %w", err)
		}
	}
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, err
	}
	if err := archiveNodeModulesSnapshots(snapDir, worktreeDir, lockFiles); err != nil {
		return nil, err
	}
	if err := os.WriteFile(readyMarker, []byte(time.Now().UTC().Format(time.RFC3339)), 0644); err != nil {
		return nil, err
	}
	return res, nil
}

// SQLExecutor 抽象执行建库/授权/删库 DDL 语句的底层接口，便于注入真实 *sql.DB 或单元测试内存桩。
type SQLExecutor interface {
	// ExecContext 在给定上下文中执行单条 SQL DDL 语句。
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	// Close 关闭底层数据库连接池。
	Close() error
}

// DBManager 负责按产品前缀动态创建与销毁本地 MySQL 测试库与预览库（8.2 节）。
type DBManager struct {
	exec SQLExecutor
	host string
	port int
}

// NewMySQLDBManager 连接本机 MySQL 管理实例并返回 *DBManager。
// 参数 host/port 为数据库地址端口，adminUser/adminPass 为管理账号口令；返回 *DBManager 及连接错误。
func NewMySQLDBManager(host string, port int, adminUser, adminPass string) (*DBManager, error) {
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	if port <= 0 {
		port = 3306
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s",
		adminUser, adminPass, host, port)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开 MySQL 管理连接失败: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("连通本地 MySQL 失败: %w", err)
	}
	return NewDBManagerWithExecutor(db, host, port), nil
}

// NewDBManagerWithExecutor 使用自定义 SQLExecutor 构造 *DBManager（供单元测试注入 SQL 记录器）。
// 参数 exec 为 SQL 执行器，host/port 为用于拼装业务 DSN 的主机与端口；返回 *DBManager。
func NewDBManagerWithExecutor(exec SQLExecutor, host string, port int) *DBManager {
	if host == "" {
		host = "127.0.0.1"
	}
	if port <= 0 {
		port = 3306
	}
	return &DBManager{exec: exec, host: host, port: port}
}

// CreateDatabase 按 `<prefix>_<kind>_<workId>` 命名规范动态创建隔离的 UTF8MB4 数据库，并按产品前缀授权专用账号。
// 参数 ctx 为上下文，productPrefix 为产品数据库前缀，kind 为 "test" 或 "preview"，workID 为任务标识，productUser/productPass 为该产品专用数据库账号；返回创建的库名、连接 DSN 及错误。
func (d *DBManager) CreateDatabase(ctx context.Context, productPrefix, kind, workID, productUser, productPass string) (string, string, error) {
	if d == nil || d.exec == nil {
		return "", "", errors.New("DBManager 未初始化")
	}
	cleanPrefix := sanitizeDBToken(productPrefix)
	if !dbSafeIdentRe.MatchString(cleanPrefix) {
		return "", "", fmt.Errorf("产品数据库前缀非法: %q", productPrefix)
	}
	if kind != "test" && kind != "preview" {
		return "", "", fmt.Errorf("数据库用途类型必须为 test 或 preview，实际 %q", kind)
	}
	cleanWork := sanitizeDBToken(workID)
	if cleanWork == "" {
		return "", "", errors.New("workID 转换后的数据库后缀为空")
	}
	dbName := fmt.Sprintf("%s_%s_%s", cleanPrefix, kind, cleanWork)
	if len(dbName) > 60 {
		dbName = dbName[:60]
	}

	createSQL := fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", dbName)
	if _, err := d.exec.ExecContext(ctx, createSQL); err != nil {
		return "", "", fmt.Errorf("创建数据库 %s 失败: %w", dbName, err)
	}

	if strings.TrimSpace(productUser) != "" {
		if !dbSafeIdentRe.MatchString(productUser) {
			return "", "", fmt.Errorf("产品数据库用户名非法: %q", productUser)
		}
		// 严格只授权 `<cleanPrefix>_%` 前缀下的测试库与预览库，绝不授权 autoship_agent 状态库或其他产品库
		grantSQL := fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s\\_%%`.* TO '%s'@'%%'", cleanPrefix, productUser)
		_, _ = d.exec.ExecContext(ctx, grantSQL)
	}

	dsnUser := productUser
	if dsnUser == "" {
		dsnUser = "root"
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		dsnUser, productPass, d.host, d.port, dbName)
	return dbName, dsn, nil
}

// DropDatabase 安全删除由本产品动态创建的测试库或预览库（强制校验 `<prefix>_test_` 或 `<prefix>_preview_` 前缀，杜绝误删其他库）。
// 参数 ctx 为上下文，productPrefix 为产品前缀，dbName 为待销毁数据库名；校验通过并删除后返回 nil，否则返回错误。
func (d *DBManager) DropDatabase(ctx context.Context, productPrefix, dbName string) error {
	if d == nil || d.exec == nil {
		return errors.New("DBManager 未初始化")
	}
	cleanPrefix := sanitizeDBToken(productPrefix)
	if !dbSafeIdentRe.MatchString(dbName) {
		return fmt.Errorf("拒绝删除名称非法的数据库: %q", dbName)
	}
	allowedTest := cleanPrefix + "_test_"
	allowedPreview := cleanPrefix + "_preview_"
	if !strings.HasPrefix(dbName, allowedTest) && !strings.HasPrefix(dbName, allowedPreview) {
		return fmt.Errorf("拒绝删除非本产品测试/预览前缀的数据库: %s (允许前缀: %s / %s)", dbName, allowedTest, allowedPreview)
	}
	dropSQL := fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)
	if _, err := d.exec.ExecContext(ctx, dropSQL); err != nil {
		return fmt.Errorf("删除数据库 %s 失败: %w", dbName, err)
	}
	return nil
}

// Close 关闭 DBManager 持有的底层管理数据库连接。
// 无入参；返回关闭错误。
func (d *DBManager) Close() error {
	if d == nil || d.exec == nil {
		return nil
	}
	return d.exec.Close()
}

// PortAllocator 管理本地测试与预览实例使用的动态 TCP 端口池。
type PortAllocator struct {
	mu        sync.Mutex
	startPort int
	endPort   int
	inUse     map[int]string // port -> ownerID
}

// NewPortAllocator 创建管理 `[startPort, endPort]` 闭区间的动态端口分配器。
// 参数 startPort 与 endPort 为起止端口；返回 *PortAllocator 及区间校验错误。
func NewPortAllocator(startPort, endPort int) (*PortAllocator, error) {
	if startPort < 1024 || endPort > 65535 || endPort < startPort {
		return nil, fmt.Errorf("无效的端口池区间: %d-%d", startPort, endPort)
	}
	return &PortAllocator{
		startPort: startPort,
		endPort:   endPort,
		inUse:     make(map[int]string),
	}, nil
}

// Allocate 为指定持有者（如任务或预览会话）分配 count 个当前未被占用且操作系统实际可绑定的空闲 TCP 端口。
// 参数 ownerID 为持有者标识，count 为所需端口数（<=0 时按 1 处理）；返回分配到的端口切片及错误。
func (p *PortAllocator) Allocate(ownerID string, count int) ([]int, error) {
	if strings.TrimSpace(ownerID) == "" {
		return nil, errors.New("ownerID 不能为空")
	}
	if count <= 0 {
		count = 1
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var picked []int
	for port := p.startPort; port <= p.endPort && len(picked) < count; port++ {
		if _, taken := p.inUse[port]; taken {
			continue
		}
		if !isTCPPortAvailable(port) {
			continue
		}
		picked = append(picked, port)
	}
	if len(picked) < count {
		return nil, ErrPortPoolExhausted
	}
	for _, port := range picked {
		p.inUse[port] = ownerID
	}
	return picked, nil
}

// Release 释放归属于指定 ownerID 的全部已分配端口。
// 参数 ownerID 为持有者标识；无返回值。
func (p *PortAllocator) Release(ownerID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for port, holder := range p.inUse {
		if holder == ownerID {
			delete(p.inUse, port)
		}
	}
}

// RegisteredProcess 记录由本开发 Agent 亲自启动并登记受管的单个子进程元数据。
type RegisteredProcess struct {
	PID       int       `json:"pid"`       // 操作系统进程 PID
	OwnerID   string    `json:"ownerId"`   // 归属任务 ID 或预览会话 ID
	Command   string    `json:"command"`   // 启动命令描述
	StartedAt time.Time `json:"startedAt"` // 登记时间
}

// ProcessRegistry 维护本 Agent 启动的受管子进程 PID 登记表，强制落实“只清理自己登记过 PID 的进程”（8.2 & 8.7 节）。
type ProcessRegistry struct {
	mu    sync.RWMutex
	procs map[int]RegisteredProcess
}

// NewProcessRegistry 创建一个空的受管进程 PID 登记表。
// 无入参；返回 *ProcessRegistry。
func NewProcessRegistry() *ProcessRegistry {
	return &ProcessRegistry{procs: make(map[int]RegisteredProcess)}
}

// Register 将本 Agent 刚启动的子进程 PID 写入受管登记表。
// 参数 pid 为大于 0 的进程号，ownerID 为归属标识，command 为命令摘要；成功返回 nil，pid 非法返回错误。
func (r *ProcessRegistry) Register(pid int, ownerID, command string) error {
	if pid <= 0 {
		return fmt.Errorf("无效的进程 PID: %d", pid)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.procs[pid] = RegisteredProcess{
		PID:       pid,
		OwnerID:   ownerID,
		Command:   command,
		StartedAt: time.Now(),
	}
	return nil
}

// Unregister 从登记表中移除已自然退出的子进程 PID。
// 参数 pid 为进程号；无返回值。
func (r *ProcessRegistry) Unregister(pid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.procs, pid)
}

// IsRegistered 检查给定 PID 是否属于本 Agent 明确登记过的自有子进程。
// 参数 pid 为待核查进程号；已登记返回 true，未登记返回 false。
func (r *ProcessRegistry) IsRegistered(pid int) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.procs[pid]
	return ok
}

// List 返回当前仍在登记表中的全部受管进程快照切片。
// 无入参；返回 []RegisteredProcess。
func (r *ProcessRegistry) List() []RegisteredProcess {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RegisteredProcess, 0, len(r.procs))
	for _, item := range r.procs {
		out = append(out, item)
	}
	return out
}

// KillPID 在严格确认 pid 已登记在册的前提下终止该子进程（若未登记立即返回 ErrUnregisteredPID，绝不触碰外部进程）。
// 参数 pid 为目标进程号；成功终止并注销返回 nil，未登记返回 ErrUnregisteredPID。
func (r *ProcessRegistry) KillPID(pid int) error {
	r.mu.Lock()
	if _, ok := r.procs[pid]; !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: pid=%d", ErrUnregisteredPID, pid)
	}
	delete(r.procs, pid)
	r.mu.Unlock()

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	// 先发送 SIGTERM 允许优雅退出，短暂等待后兜底发送 SIGKILL
	_ = proc.Signal(syscall.SIGTERM)
	time.Sleep(30 * time.Millisecond)
	_ = proc.Signal(syscall.SIGKILL)
	return nil
}

// KillByOwner 终止并注销归属于指定 ownerID（如某开发任务或预览实例）的所有已登记子进程。
// 参数 ownerID 为归属标识；返回本次被清理的 PID 列表。
func (r *ProcessRegistry) KillByOwner(ownerID string) []int {
	r.mu.RLock()
	var targets []int
	for pid, info := range r.procs {
		if info.OwnerID == ownerID {
			targets = append(targets, pid)
		}
	}
	r.mu.RUnlock()

	var killed []int
	for _, pid := range targets {
		if err := r.KillPID(pid); err == nil {
			killed = append(killed, pid)
		}
	}
	return killed
}

// SleepInhibitor 基于引用计数管理 macOS `caffeinate -i` 防系统休眠进程（有活跃任务或活跃预览时保持运行，归零自动退出，8.1 节）。
type SleepInhibitor struct {
	mu       sync.Mutex
	holders  map[string]int
	registry *ProcessRegistry
	cmd      *exec.Cmd
	pid      int
	binPath  string
	binArgs  []string
}

// NewSleepInhibitor 创建与指定进程登记表联动的防休眠控制器（darwin 默认调用 `/usr/bin/caffeinate -i`）。
// 参数 registry 为进程 PID 登记表；返回 *SleepInhibitor。
func NewSleepInhibitor(registry *ProcessRegistry) *SleepInhibitor {
	bin := ""
	var args []string
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/usr/bin/caffeinate"); err == nil {
			bin = "/usr/bin/caffeinate"
			args = []string{"-i", "-w", fmt.Sprintf("%d", os.Getpid())}
		}
	}
	return &SleepInhibitor{
		holders:  make(map[string]int),
		registry: registry,
		binPath:  bin,
		binArgs:  args,
	}
}

// Acquire 为活跃任务或预览会话申请防休眠锁；当首个持有者进入时自动拉起 `caffeinate -i` 并登记 PID。
// 参数 holderID 为持有者标识（如 "work:w-1" 或 "preview:p-1"）；返回启动错误（非 darwin 环境无操作返回 nil）。
func (s *SleepInhibitor) Acquire(holderID string) error {
	if strings.TrimSpace(holderID) == "" {
		return errors.New("holderID 不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	s.holders[holderID]++
	if s.cmd != nil || s.binPath == "" {
		return nil
	}

	cmd := exec.Command(s.binPath, s.binArgs...)
	if err := cmd.Start(); err != nil {
		delete(s.holders, holderID)
		return fmt.Errorf("启动防休眠进程 caffeinate 失败: %w", err)
	}
	s.cmd = cmd
	s.pid = cmd.Process.Pid
	if s.registry != nil {
		_ = s.registry.Register(s.pid, "system:caffeinate", s.binPath+" "+strings.Join(s.binArgs, " "))
	}
	go func(c *exec.Cmd) {
		_ = c.Wait()
	}(cmd)
	return nil
}

// Release 释放指定持有者的防休眠锁；当全部活跃任务与预览归零时立即终止 `caffeinate` 子进程并注销 PID。
// 参数 holderID 为持有者标识；无返回值。
func (s *SleepInhibitor) Release(holderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cnt, ok := s.holders[holderID]
	if !ok {
		return
	}
	if cnt <= 1 {
		delete(s.holders, holderID)
	} else {
		s.holders[holderID] = cnt - 1
	}
	if len(s.holders) > 0 || s.cmd == nil {
		return
	}
	pid := s.pid
	s.cmd = nil
	s.pid = 0
	if s.registry != nil && pid > 0 {
		_ = s.registry.KillPID(pid)
	} else if pid > 0 {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	}
}

// ActivePID 返回当前正在运行的 caffeinate 子进程 PID（未运行时返回 0）。
// 无入参；返回进程号整数。
func (s *SleepInhibitor) ActivePID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

// buildGitEnv 为配置了仓库专用 SSH Deploy Key 的产品构造 GIT_SSH_COMMAND 环境变量。
func buildGitEnv(deployKeyPath string) []string {
	cleaned := config.ExpandPath(deployKeyPath)
	if cleaned == "" {
		return nil
	}
	sshCmd := fmt.Sprintf("ssh -i %q -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new", cleaned)
	env := os.Environ()
	env = append(env, "GIT_SSH_COMMAND="+sshCmd)
	return env
}

// runGit 在指定目录下执行 git 子命令并返回去空白的标准输出。
func runGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s 失败: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// archiveNodeModulesSnapshots 在锁文件首次安装完成后将生成的 node_modules 复制归档到共享快照目录。
func archiveNodeModulesSnapshots(snapDir, worktreeDir string, lockFiles []string) error {
	for _, lf := range lockFiles {
		if !strings.HasSuffix(lf, "package-lock.json") && !strings.HasSuffix(lf, "pnpm-lock.yaml") && !strings.HasSuffix(lf, "yarn.lock") {
			continue
		}
		relParent := filepath.Dir(filepath.FromSlash(lf))
		srcNM := filepath.Join(worktreeDir, relParent, "node_modules")
		st, err := os.Stat(srcNM)
		if err != nil || !st.IsDir() {
			continue
		}
		dstNM := filepath.Join(snapDir, relParent, "node_modules")
		if err := copyDirRecursive(srcNM, dstNM); err != nil {
			return err
		}
	}
	return nil
}

// restoreNodeModulesLinks 在锁文件 SHA-256 命中缓存时，将快照中的 node_modules 符号链接回新任务的 worktree。
func restoreNodeModulesLinks(snapDir, worktreeDir string, lockFiles []string) error {
	for _, lf := range lockFiles {
		if !strings.HasSuffix(lf, "package-lock.json") && !strings.HasSuffix(lf, "pnpm-lock.yaml") && !strings.HasSuffix(lf, "yarn.lock") {
			continue
		}
		relParent := filepath.Dir(filepath.FromSlash(lf))
		cachedNM := filepath.Join(snapDir, relParent, "node_modules")
		st, err := os.Stat(cachedNM)
		if err != nil || !st.IsDir() {
			return fmt.Errorf("缓存缺少 node_modules: %s", cachedNM)
		}
		targetNM := filepath.Join(worktreeDir, relParent, "node_modules")
		_ = os.RemoveAll(targetNM)
		if err := os.Symlink(cachedNM, targetNM); err != nil {
			return err
		}
	}
	return nil
}

// copyDirRecursive 递归复制目录及普通文件/符号链接。
func copyDirRecursive(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.RemoveAll(target)
			return os.Symlink(linkTarget, target)
		}
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

// sanitizeDBToken 将产品编码或任务 ID 转换为仅含小写字母、数字与下划线的安全数据库标识符片段。
func sanitizeDBToken(raw string) string {
	lower := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else if r == '_' || r == '-' || r == '.' {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

// isTCPPortAvailable 尝试在回环地址绑定给定端口以确认宿主当前无人占用该端口。
func isTCPPortAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
