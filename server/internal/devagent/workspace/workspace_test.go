package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"autoship/internal/devagent/config"
)

// mockSQLExec 记录 DBManager 发出的全部 SQL 语句，用于无外部 MySQL 依赖的确定性单元测试。
type mockSQLExec struct {
	queries []string
}

// ExecContext 记录收到的 SQL 文本并返回成功空结果。
func (m *mockSQLExec) ExecContext(_ context.Context, query string, _ ...any) (sql.Result, error) {
	m.queries = append(m.queries, query)
	return nil, nil
}

// Close 实现 SQLExecutor 关闭接口。
func (m *mockSQLExec) Close() error { return nil }

// TestWorktreeIsolationAndLockfileCache 验证专用克隆、独立 worktree 创建清理（不碰原始仓库工作树）及锁文件 SHA-256 跨任务缓存命中。
func TestWorktreeIsolationAndLockfileCache(t *testing.T) {
	ctx := context.Background()
	userOrigRepo := filepath.Join(t.TempDir(), "user-personal-repo")
	if err := os.MkdirAll(userOrigRepo, 0755); err != nil {
		t.Fatal(err)
	}
	// 初始化模拟远端/用户原仓库，写入 go.mod / go.sum / package-lock.json
	mustGit(t, userOrigRepo, "init", "-b", "main")
	mustGit(t, userOrigRepo, "config", "user.name", "Owner")
	mustGit(t, userOrigRepo, "config", "user.email", "owner@example.com")
	if err := os.WriteFile(filepath.Join(userOrigRepo, "go.mod"), []byte("module demo\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userOrigRepo, "go.sum"), []byte("example.com/pkg v1.0.0 h1:abc=\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userOrigRepo, "package-lock.json"), []byte(`{"name":"demo","lockfileVersion":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, userOrigRepo, "add", ".")
	mustGit(t, userOrigRepo, "commit", "-m", "initial")

	autoshipRoot := filepath.Join(t.TempDir(), "autoship-root")
	mgr, err := NewManager(autoshipRoot)
	if err != nil {
		t.Fatalf("创建 workspace Manager 失败: %v", err)
	}

	prod := config.ProductProfile{
		ProductCode:   "demo-prod",
		RepoURL:       userOrigRepo,
		DefaultBranch: "main",
	}
	sess1, err := mgr.CreateTaskWorktree(ctx, prod, "w-001", "Fix Login Bug", "")
	if err != nil {
		t.Fatalf("创建首个任务 worktree 失败: %v", err)
	}
	if sess1.BranchName != "autoship/w-001-fix-login-bug" {
		t.Fatalf("任务分支命名不符合预期: %s", sess1.BranchName)
	}
	if strings.HasPrefix(sess1.WorktreeDir, userOrigRepo) {
		t.Fatalf("worktree 严禁落在用户原始目录内: %s", sess1.WorktreeDir)
	}

	// 首个任务准备依赖：预期缓存未命中并执行一次安装回调（模拟生成 node_modules）
	installCount := 0
	runner := func(_ context.Context, wt string, _ map[string]string, _ string) error {
		installCount++
		nmFile := filepath.Join(wt, "node_modules", "dep-a", "index.js")
		if err := os.MkdirAll(filepath.Dir(nmFile), 0755); err != nil {
			return err
		}
		return os.WriteFile(nmFile, []byte("module.exports = 1;"), 0644)
	}
	c1, err := mgr.PrepareDependencyCache(ctx, prod.ProductCode, sess1.WorktreeDir, "npm ci", runner)
	if err != nil {
		t.Fatalf("首次准备依赖缓存失败: %v", err)
	}
	if c1.CacheHit || installCount != 1 {
		t.Fatalf("首次应当未命中缓存且执行 1 次安装，实际 hit=%v count=%d", c1.CacheHit, installCount)
	}

	// 第二个任务锁文件完全相同：预期直接命中 SHA-256 缓存，不再执行安装命令且自动挂载 node_modules
	sess2, err := mgr.CreateTaskWorktree(ctx, prod, "w-002", "Add Export", "")
	if err != nil {
		t.Fatalf("创建第二个任务 worktree 失败: %v", err)
	}
	c2, err := mgr.PrepareDependencyCache(ctx, prod.ProductCode, sess2.WorktreeDir, "npm ci", runner)
	if err != nil {
		t.Fatalf("第二次准备依赖缓存失败: %v", err)
	}
	if !c2.CacheHit || installCount != 1 {
		t.Fatalf("相同锁文件应当命中缓存并跳过重装，实际 hit=%v count=%d", c2.CacheHit, installCount)
	}
	if _, err := os.Stat(filepath.Join(sess2.WorktreeDir, "node_modules", "dep-a", "index.js")); err != nil {
		t.Fatalf("缓存恢复后的 node_modules 缺失: %v", err)
	}

	// 销毁 worktree 后验证目录已清理且用户原目录完好无损
	if err := mgr.RemoveTaskWorktree(ctx, prod.ProductCode, "w-001"); err != nil {
		t.Fatalf("清理 worktree 失败: %v", err)
	}
	if _, err := os.Stat(sess1.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("期望 worktree 目录已被移除: %s", sess1.WorktreeDir)
	}
}

// TestDBManagerPrefixSafety 验证测试库/预览库按产品前缀建库，且严格拒绝删除其他产品或系统状态库。
func TestDBManagerPrefixSafety(t *testing.T) {
	ctx := context.Background()
	sqlMock := &mockSQLExec{}
	dbm := NewDBManagerWithExecutor(sqlMock, "127.0.0.1", 3306)

	dbName, dsn, err := dbm.CreateDatabase(ctx, "seccenter", "test", "w-100", "sec_test", "pass123")
	if err != nil {
		t.Fatalf("CreateDatabase 失败: %v", err)
	}
	if dbName != "seccenter_test_w_100" || !strings.Contains(dsn, "/seccenter_test_w_100?") {
		t.Fatalf("生成的库名或 DSN 不符合预期: db=%s dsn=%s", dbName, dsn)
	}
	if err := dbm.DropDatabase(ctx, "seccenter", "autoship_agent"); err == nil {
		t.Fatal("期望拒绝删除 Agent 状态库 autoship_agent，但返回了 nil")
	}
	if err := dbm.DropDatabase(ctx, "seccenter", "otherprod_test_w_100"); err == nil {
		t.Fatal("期望拒绝删除跨产品数据库，但返回了 nil")
	}
	if err := dbm.DropDatabase(ctx, "seccenter", dbName); err != nil {
		t.Fatalf("删除合法测试库应当成功: %v", err)
	}
}

// TestPortPoolProcessRegistryAndCaffeinate 验证端口分配回收、进程登记表绝不误杀外部进程及 caffeinate 引用计数启停。
func TestPortPoolProcessRegistryAndCaffeinate(t *testing.T) {
	pool, err := NewPortAllocator(29100, 29120)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := pool.Allocate("preview-1", 2)
	if err != nil || len(ports) != 2 || ports[0] == ports[1] {
		t.Fatalf("端口分配异常: ports=%v err=%v", ports, err)
	}
	pool.Release("preview-1")

	reg := NewProcessRegistry()
	// 未登记的外部 PID（例如父进程 PID）绝对不允许被 KillPID 终止
	if err := reg.KillPID(os.Getppid()); !errors.Is(err, ErrUnregisteredPID) {
		t.Fatalf("未登记 PID 必须返回 ErrUnregisteredPID，实际: %v", err)
	}

	// 启动一个本 Agent 受管睡眠子进程并登记，验证能够安全清理
	sleepCmd := exec.Command("/bin/sleep", "30")
	if err := sleepCmd.Start(); err != nil {
		t.Fatalf("启动测试子进程失败: %v", err)
	}
	pid := sleepCmd.Process.Pid
	if err := reg.Register(pid, "task-9", "/bin/sleep 30"); err != nil {
		t.Fatal(err)
	}
	go func() { _ = sleepCmd.Wait() }()

	killed := reg.KillByOwner("task-9")
	if len(killed) != 1 || killed[0] != pid {
		t.Fatalf("期望清理已登记进程 %d，实际 %v", pid, killed)
	}

	// 验证 darwin 上 caffeinate -i 防休眠引用计数生命周期
	inhibitor := NewSleepInhibitor(reg)
	if err := inhibitor.Acquire("work:1"); err != nil {
		t.Fatalf("Acquire 防休眠失败: %v", err)
	}
	if err := inhibitor.Acquire("preview:1"); err != nil {
		t.Fatalf("二次 Acquire 防休眠失败: %v", err)
	}
	if runtime.GOOS == "darwin" && inhibitor.ActivePID() <= 0 {
		t.Fatal("darwin 环境下 Acquire 后应当拉起 caffeinate 进程")
	}
	inhibitor.Release("work:1")
	if runtime.GOOS == "darwin" && inhibitor.ActivePID() <= 0 {
		t.Fatal("仍有一个活跃预览持有防休眠锁时不应提前关闭 caffeinate")
	}
	inhibitor.Release("preview:1")
	if inhibitor.ActivePID() != 0 {
		t.Fatalf("所有持有者释放后 caffeinate PID 应归零，实际 %d", inhibitor.ActivePID())
	}
}

// mustGit 在测试目录执行 git 命令，失败时直接终止当前测试。
func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v 失败: %v (%s)", args, err, string(out))
	}
}
