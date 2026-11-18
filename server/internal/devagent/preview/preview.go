// Package preview 管理 Mac 开发 Agent 上的候选版本预览实例生命周期（设计文档 7.6 & 8.9 节），支持产品原生加载外部证书或内置本地 TLS 反向代理转发、随机专用测试账号生成、caffeinate 防休眠联动及 24 小时空闲自动回收。
package preview

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/sandbox"
	"autoship/internal/devagent/workspace"
	"autoship/pkg/crypto"
)

// Session 描述单个正在运行的候选预览实例元数据与状态。
type Session struct {
	SessionID        string    `json:"sessionId"`        // 预览会话唯一标识（通常对应 preview-<workId>）
	WorkID           string    `json:"workId"`           // 关联开发任务 ID
	ProductCode      string    `json:"productCode"`      // 产品编码
	ExternalURL      string    `json:"externalUrl"`      // 供管理员经 tailnet 直接访问的 HTTPS 地址
	ExternalPort     int       `json:"externalPort"`     // 对外 HTTPS 监听端口
	BackendPort      int       `json:"backendPort"`      // 候选服务实际监听端口
	UsesLocalTLSProxy bool     `json:"usesLocalTlsProxy"` // 是否启用了 Agent 内置本地 TLS 反向代理
	DBName           string    `json:"dbName"`           // 动态创建的预览库名称（未启用时为空）
	TestUsername     string    `json:"testUsername"`     // 预览专用随机测试账号名
	TestPassword     string    `json:"testPassword"`     // 预览专用随机测试密码
	PID              int       `json:"pid"`              // 登记在 ProcessRegistry 中的候选进程 PID
	StartedAt        time.Time `json:"startedAt"`        // 启动时间
	lastActiveUnix   atomic.Int64
	proxyServer      *http.Server
	proxyListener    net.Listener
}

// Touch 刷新该预览实例的最后活跃访问时间戳（每次经内置 TLS 代理转发请求时自动调用）。
// 参数 now 为当前时刻；无返回值。
func (s *Session) Touch(now time.Time) {
	s.lastActiveUnix.Store(now.UnixNano())
}

// LastActiveAt 返回该预览实例最后一次被访问或启动的时间。
// 无入参；返回 time.Time。
func (s *Session) LastActiveAt() time.Time {
	ns := s.lastActiveUnix.Load()
	if ns <= 0 {
		return s.StartedAt
	}
	return time.Unix(0, ns)
}

// StartRequest 定义启动一个预览实例所需的完整输入参数。
type StartRequest struct {
	WorkID      string                // 开发任务 ID
	Product     config.ProductProfile // 产品档案（含预览配方与权限档案）
	WorktreeDir string                // 候选代码所在 worktree 目录
	TempDir     string                // 任务专属临时目录
	CertFile    string                // autoShip CA 签发的叶证书路径（SAN 含 Mac tailnet IP）
	KeyFile     string                // 对应证书私钥路径
}

// Manager 统筹管理本机全部活跃预览实例、内置 TLS 反向代理及 24 小时空闲回收协程。
type Manager struct {
	agentCfg    *config.AgentConfig
	ports       *workspace.PortAllocator
	procs       *workspace.ProcessRegistry
	inhibitor   *workspace.SleepInhibitor
	dbManager   *workspace.DBManager
	idleTimeout time.Duration

	mu       sync.RWMutex
	sessions map[string]*Session // workID -> *Session
}

// NewManager 创建预览实例管理器。
// 参数 agentCfg 为全局配置，ports 为动态端口池，procs 为自有进程登记表，inhibitor 为防休眠控制器，dbManager 为可选数据库管理器；返回 *Manager。
func NewManager(
	agentCfg *config.AgentConfig,
	ports *workspace.PortAllocator,
	procs *workspace.ProcessRegistry,
	inhibitor *workspace.SleepInhibitor,
	dbManager *workspace.DBManager,
) *Manager {
	if agentCfg == nil {
		agentCfg = config.Default()
	}
	idleHours := agentCfg.Preview.IdleTimeoutHours
	if idleHours <= 0 {
		idleHours = 24
	}
	return &Manager{
		agentCfg:    agentCfg,
		ports:       ports,
		procs:       procs,
		inhibitor:   inhibitor,
		dbManager:   dbManager,
		idleTimeout: time.Duration(idleHours) * time.Hour,
		sessions:    make(map[string]*Session),
	}
}

// SetIdleTimeout 允许单元测试将默认 24 小时空闲回收阈值调整为毫秒级以验证自动回收逻辑。
// 参数 d 为空闲超时时间；无返回值。
func (m *Manager) SetIdleTimeout(d time.Duration) {
	if d > 0 {
		m.idleTimeout = d
	}
}

// Start 按产品预览配方分配端口、准备演示数据库、拉起候选进程，并在产品不支持外部证书时启动内置本地 TLS 反向代理（7.6 节）。
// 参数 ctx 为上下文，req 为预览启动请求；返回就绪的 *Session 及错误。
func (m *Manager) Start(ctx context.Context, req StartRequest) (*Session, error) {
	if strings.TrimSpace(req.WorkID) == "" {
		return nil, errors.New("预览 WorkID 不能为空")
	}
	req.Product.ApplyDefaults()
	recipe := req.Product.Preview
	if strings.TrimSpace(recipe.StartCmd) == "" {
		return nil, fmt.Errorf("产品 %s 未配置 preview.startCmd", req.Product.ProductCode)
	}

	// 若同任务已有旧预览，先安全清理释放资源
	_ = m.Stop(ctx, req.WorkID)

	sessionID := "preview-" + req.WorkID
	needPorts := 1
	if !recipe.SupportsExternalTLS {
		// 不支持直接加载外部证书时：分配 1 个对外 tailnet HTTPS 端口 + 1 个本机回环 HTTP 后端端口
		needPorts = 2
	}
	allocated, err := m.ports.Allocate(sessionID, needPorts)
	if err != nil {
		return nil, fmt.Errorf("为预览实例分配端口失败: %w", err)
	}

	extPort := allocated[0]
	backendPort := allocated[0]
	if !recipe.SupportsExternalTLS {
		backendPort = allocated[1]
	}

	cleanupOnErr := func(dbName string) {
		m.ports.Release(sessionID)
		if dbName != "" && m.dbManager != nil {
			_ = m.dbManager.DropDatabase(context.Background(), recipe.DBPrefix, dbName)
		}
	}

	var previewDB, previewDSN string
	if m.dbManager != nil {
		dbName, dsn, dbErr := m.dbManager.CreateDatabase(ctx, recipe.DBPrefix, "preview", req.WorkID, req.Product.DBTestUser, req.Product.DBTestPassword)
		if dbErr != nil {
			cleanupOnErr("")
			return nil, fmt.Errorf("创建预览数据库失败: %w", dbErr)
		}
		previewDB = dbName
		previewDSN = dsn
	}

	randSuffix, _ := crypto.GenerateRandomHex(4)
	randPass, _ := crypto.GenerateRandomHex(10)
	testUser := "preview_" + randSuffix

	certPath := strings.TrimSpace(req.CertFile)
	if certPath == "" {
		certPath = m.agentCfg.Relay.CertFile
	}
	keyPath := strings.TrimSpace(req.KeyFile)
	if keyPath == "" {
		keyPath = m.agentCfg.Relay.KeyFile
	}

	tailnetHost := strings.TrimSpace(m.agentCfg.TailnetIP)
	if tailnetHost == "" {
		tailnetHost = "127.0.0.1"
	}

	extraEnv := map[string]string{
		recipe.PortEnvVar:          fmt.Sprintf("%d", backendPort),
		"AUTOSHIP_PREVIEW_USER":    testUser,
		"AUTOSHIP_PREVIEW_PASS":    randPass,
		"AUTOSHIP_PREVIEW_HOST":    tailnetHost,
		"AUTOSHIP_PREVIEW_EXTPORT": fmt.Sprintf("%d", extPort),
	}
	if previewDSN != "" {
		extraEnv[recipe.DBDSNEnvVar] = previewDSN
	}
	if recipe.SupportsExternalTLS {
		if recipe.CertEnvVar != "" {
			extraEnv[recipe.CertEnvVar] = certPath
		}
		if recipe.KeyEnvVar != "" {
			extraEnv[recipe.KeyEnvVar] = keyPath
		}
	}

	sbSpec := sandbox.BuildSpecFromProduct(m.agentCfg, req.Product, req.WorkID, req.WorktreeDir, req.TempDir)
	// 若配置了生成演示数据的 seedCmd（绝不使用生产数据），在启动主服务前先执行
	if strings.TrimSpace(recipe.SeedCmd) != "" {
		seedCmd, _, wrapErr := sandbox.WrapCommand(ctx, sbSpec, "/bin/sh", "-c", recipe.SeedCmd)
		if wrapErr != nil {
			cleanupOnErr(previewDB)
			return nil, wrapErr
		}
		seedCmd.Dir = req.WorktreeDir
		seedCmd.Env = mergeProcessEnv(seedCmd.Env, extraEnv)
		if out, seedErr := seedCmd.CombinedOutput(); seedErr != nil {
			cleanupOnErr(previewDB)
			return nil, fmt.Errorf("执行预览演示数据生成命令失败: %w (%s)", seedErr, strings.TrimSpace(string(out)))
		}
	}

	// 启动常驻候选预览进程（使用独立 Background 上下文使其在 Start 返回后继续存活）
	procCmd, _, err := sandbox.WrapCommand(context.Background(), sbSpec, "/bin/sh", "-c", recipe.StartCmd)
	if err != nil {
		cleanupOnErr(previewDB)
		return nil, err
	}
	procCmd.Dir = req.WorktreeDir
	procCmd.Env = mergeProcessEnv(procCmd.Env, extraEnv)
	if err := procCmd.Start(); err != nil {
		cleanupOnErr(previewDB)
		return nil, fmt.Errorf("启动候选预览服务失败: %w", err)
	}
	pid := procCmd.Process.Pid
	_ = m.procs.Register(pid, sessionID, recipe.StartCmd)
	go func(c *exec.Cmd, p int) {
		_ = c.Wait()
	}(procCmd, pid)

	now := time.Now()
	sess := &Session{
		SessionID:         sessionID,
		WorkID:            req.WorkID,
		ProductCode:       req.Product.ProductCode,
		ExternalURL:       fmt.Sprintf("https://%s:%d", tailnetHost, extPort),
		ExternalPort:      extPort,
		BackendPort:       backendPort,
		UsesLocalTLSProxy: !recipe.SupportsExternalTLS,
		DBName:            previewDB,
		TestUsername:      testUser,
		TestPassword:      randPass,
		PID:               pid,
		StartedAt:         now,
	}
	sess.Touch(now)

	// 若产品不支持直接加载外部 TLS 证书，由开发 Agent 在本机启动一层 HTTPS -> HTTP 反向代理转发
	if !recipe.SupportsExternalTLS {
		if err := m.startLocalTLSReverseProxy(sess, tailnetHost, extPort, backendPort, certPath, keyPath); err != nil {
			_ = m.procs.KillByOwner(sessionID)
			cleanupOnErr(previewDB)
			return nil, err
		}
	}

	if m.inhibitor != nil {
		_ = m.inhibitor.Acquire(sessionID)
	}

	m.mu.Lock()
	m.sessions[req.WorkID] = sess
	m.mu.Unlock()
	return sess, nil
}

// Stop 停止并彻底回收指定开发任务关联的预览实例（关闭反向代理、终止已登记 PID、释放端口、删除预览库并释放防休眠锁）。
// 参数 ctx 为上下文，workID 为任务标识；不存在时安全返回 nil。
func (m *Manager) Stop(ctx context.Context, workID string) error {
	m.mu.Lock()
	sess, ok := m.sessions[workID]
	if ok {
		delete(m.sessions, workID)
	}
	m.mu.Unlock()
	if !ok || sess == nil {
		return nil
	}

	if sess.proxyServer != nil {
		shutCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_ = sess.proxyServer.Shutdown(shutCtx)
		cancel()
	}
	if sess.proxyListener != nil {
		_ = sess.proxyListener.Close()
	}
	if m.procs != nil {
		_ = m.procs.KillByOwner(sess.SessionID)
	}
	if m.ports != nil {
		m.ports.Release(sess.SessionID)
	}
	if sess.DBName != "" && m.dbManager != nil {
		prefix := strings.ReplaceAll(sess.ProductCode, "-", "_")
		_ = m.dbManager.DropDatabase(ctx, prefix, sess.DBName)
	}
	if m.inhibitor != nil {
		m.inhibitor.Release(sess.SessionID)
	}
	return nil
}

// ReapIdle 扫描当前所有预览会话，自动停止空闲时长超过配置阈值（默认 24 小时）的实例（7.6 节）。
// 参数 ctx 为上下文，now 为校对当前时刻；返回本次被自动回收的 workID 列表。
func (m *Manager) ReapIdle(ctx context.Context, now time.Time) []string {
	m.mu.RLock()
	var expired []string
	for workID, sess := range m.sessions {
		if now.Sub(sess.LastActiveAt()) >= m.idleTimeout {
			expired = append(expired, workID)
		}
	}
	m.mu.RUnlock()

	for _, wid := range expired {
		_ = m.Stop(ctx, wid)
	}
	return expired
}

// StartIdleReaperLoop 在后台定期执行空闲预览实例自动回收，随 ctx 取消自动退出。
// 参数 ctx 为守护生命周期上下文，interval 为巡检间隔（默认 5 分钟）；无返回值。
func (m *Manager) StartIdleReaperLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				m.ReapIdle(ctx, now)
			}
		}
	}()
}

// StopAll 在开发 Agent 进程退出时安全清理全部活跃预览实例。
// 参数 ctx 为上下文；无返回值。
func (m *Manager) StopAll(ctx context.Context) {
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for wid := range m.sessions {
		ids = append(ids, wid)
	}
	m.mu.RUnlock()
	for _, wid := range ids {
		_ = m.Stop(ctx, wid)
	}
}

// startLocalTLSReverseProxy 在 `<tailnetHost>:<extPort>` 上使用 autoShip CA 签发的证书启动 HTTPS 反向代理转发到 `http://127.0.0.1:<backendPort>`。
func (m *Manager) startLocalTLSReverseProxy(sess *Session, tailnetHost string, extPort, backendPort int, certFile, keyFile string) error {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("加载预览 TLS 证书 (%s) 失败: %w", certFile, err)
	}
	targetURL, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", backendPort))
	if err != nil {
		return err
	}
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess.Touch(time.Now())
		proxy.ServeHTTP(w, r)
	})

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
	}
	listenAddr := net.JoinHostPort(tailnetHost, fmt.Sprintf("%d", extPort))
	ln, err := tls.Listen("tcp", listenAddr, tlsCfg)
	if err != nil {
		return fmt.Errorf("绑定预览 TLS 监听地址 %s 失败: %w", listenAddr, err)
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 15 * time.Second,
	}
	sess.proxyListener = ln
	sess.proxyServer = srv
	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

// mergeProcessEnv 将基础环境切片与附加键值表合并。
func mergeProcessEnv(base []string, extra map[string]string) []string {
	if len(base) == 0 {
		base = os.Environ()
	}
	out := append([]string{}, base...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}
