package daemon

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/engine"
	"autoship/internal/devagent/preview"
	"autoship/internal/devagent/publisher"
	"autoship/internal/devagent/sandbox"
	"autoship/internal/devagent/workspace"
	"autoship/pkg/crypto"
	"autoship/pkg/envelope"
	"autoship/pkg/keychain"
	"autoship/pkg/pki"
)

// DefaultHeartbeatInterval 是设计文档 7.2 节规定的开发 Agent WSS 心跳发送间隔（15 秒）。
const DefaultHeartbeatInterval = 15 * time.Second

// Daemon 管理 Mac 开发 Agent 与 autoShip Server 之间的单条 WSS 控制流、离线发件箱续传、心跳及开发/发布任务调度。
type Daemon struct {
	cfg               *config.AgentConfig
	secrets           keychain.Store
	outbox            *Outbox
	stateStore        *StateStore
	wsManager         *workspace.Manager
	inhibitor         *workspace.SleepInhibitor
	devEngine         engine.Executor
	previewManager    *preview.Manager
	publishPipeline   *publisher.Pipeline
	taskSem           chan struct{}
	heartbeatInterval time.Duration
	reconnectBaseWait time.Duration
	tlsConfigOverride *tls.Config
	cfgFilePath       string

	mu             sync.Mutex
	seenCmdIDs     map[string]bool
	seenCmdFile    string
	lastServerSeq  atomic.Uint64
	lastServerAck  atomic.Uint64
	activeConn     *websocket.Conn
	connWriteMu    sync.Mutex
	productsByCode map[string]config.ProductProfile
}

// NewDaemon 组装完整的开发 Agent 守护实例（初始化工作区、JSONL 发件箱、可选状态库及幂等去重表）。
// 参数 cfg 为全局配置，secrets 为钥匙串接口，eng 为开发引擎执行器，ws/inhibitor/pv/pub 为各子模块管理器；返回 *Daemon 及初始化错误。
func NewDaemon(
	cfg *config.AgentConfig,
	secrets keychain.Store,
	eng engine.Executor,
	ws *workspace.Manager,
	inhibitor *workspace.SleepInhibitor,
	pv *preview.Manager,
	pub *publisher.Pipeline,
) (*Daemon, error) {
	if cfg == nil {
		return nil, errors.New("AgentConfig 不能为空")
	}
	cfg.ApplyDefaults()
	if secrets == nil {
		secrets = keychain.NewDefaultStore()
	}
	if err := os.MkdirAll(cfg.StateDir(), 0700); err != nil {
		return nil, err
	}

	ob, err := OpenOutbox(filepath.Join(cfg.StateDir(), "outbox.jsonl"))
	if err != nil {
		return nil, err
	}
	stStore, err := OpenStateStore(cfg.StateDB, cfg.KeychainService, secrets)
	if err != nil {
		return nil, err
	}

	seenPath := filepath.Join(cfg.StateDir(), "seen_commands.json")
	seenMap := loadSeenCommands(seenPath)

	prodMap := make(map[string]config.ProductProfile, len(cfg.Products))
	for _, p := range cfg.Products {
		prodMap[p.ProductCode] = p
	}

	return &Daemon{
		cfg:               cfg,
		secrets:           secrets,
		outbox:            ob,
		stateStore:        stStore,
		wsManager:         ws,
		inhibitor:         inhibitor,
		devEngine:         eng,
		previewManager:    pv,
		publishPipeline:   pub,
		taskSem:           make(chan struct{}, cfg.MaxParallelTasks),
		heartbeatInterval: DefaultHeartbeatInterval,
		reconnectBaseWait: 1 * time.Second,
		seenCmdIDs:        seenMap,
		seenCmdFile:       seenPath,
		productsByCode:    prodMap,
	}, nil
}

// SetTimingsAndTLS 允许单元测试调整心跳周期、重连初始退避时间及自定义 TLS 配置。
// 参数 heartbeat 为心跳间隔，reconnectBase 为初始重连等待，tlsCfg 为可选 TLS 配置；无返回值。
func (d *Daemon) SetTimingsAndTLS(heartbeat, reconnectBase time.Duration, tlsCfg *tls.Config) {
	if heartbeat > 0 {
		d.heartbeatInterval = heartbeat
	}
	if reconnectBase > 0 {
		d.reconnectBaseWait = reconnectBase
	}
	if tlsCfg != nil {
		d.tlsConfigOverride = tlsCfg
	}
}

// SetConfigPath 记录当前 YAML 配置文件磁盘路径，以便首次令牌注册成功后回写数字 AgentID 并清除一次性令牌。
// 参数 path 为配置文件路径；无返回值。
func (d *Daemon) SetConfigPath(path string) {
	d.cfgFilePath = config.ExpandPath(path)
}

// EnsureAgentIdentity 确保钥匙串中已生成开发 Agent 自身的 Ed25519 身份密钥对；首次不存在时自动创建并返回公钥。
// 无入参；返回 Base64 公钥、私钥及钥匙串错误。
func (d *Daemon) EnsureAgentIdentity() (pubB64 string, privB64 string, err error) {
	priv, getErr := d.secrets.Get(d.cfg.KeychainService, publisher.AgentIdentityAccount)
	if getErr == nil && strings.TrimSpace(priv) != "" {
		if savedPub, pErr := d.secrets.Get(d.cfg.KeychainService, "agent_ed25519_pub"); pErr == nil && savedPub != "" {
			return savedPub, priv, nil
		}
		if parsedPriv, parseErr := crypto.ParseEd25519PrivateKey(priv); parseErr == nil && len(parsedPriv) == ed25519.PrivateKeySize {
			derivedPub := base64.StdEncoding.EncodeToString([]byte(parsedPriv[ed25519.SeedSize:]))
			_ = d.secrets.Set(d.cfg.KeychainService, "agent_ed25519_pub", derivedPub)
			return derivedPub, priv, nil
		}
	}
	pub, newPriv, genErr := crypto.GenerateEd25519KeyPair()
	if genErr != nil {
		return "", "", genErr
	}
	if err := d.secrets.Set(d.cfg.KeychainService, publisher.AgentIdentityAccount, newPriv); err != nil {
		return "", "", err
	}
	_ = d.secrets.Set(d.cfg.KeychainService, "agent_ed25519_pub", pub)
	return pub, newPriv, nil
}

// BootstrapRegisterIfNeeded 当配置了首次注册令牌时，在本地生成 Ed25519 身份与 CSR 证书请求向 Server 完成登记并落盘签回的叶证书（7.3 & 7.4 节）。
// 参数 ctx 为上下文；无需注册或注册成功返回 nil，失败返回错误。
func (d *Daemon) BootstrapRegisterIfNeeded(ctx context.Context) error {
	token := strings.TrimSpace(d.cfg.BootstrapToken)
	if token == "" {
		return nil
	}
	pubB64, _, err := d.EnsureAgentIdentity()
	if err != nil {
		return err
	}
	csrPEM, keyPEM, err := pki.GenerateCSR(d.cfg.AgentID, []string{d.cfg.TailnetIP, "127.0.0.1"})
	if err != nil {
		return err
	}

	reqBody, _ := json.Marshal(map[string]any{
		"agentId":         d.cfg.AgentID,
		"kind":            "dev",
		"token":           token,
		"bootstrapToken":  token,
		"publicKeyB64":    pubB64,
		"agentVersion":    "v0.2.0",
		"tailnetIP":       d.cfg.TailnetIP,
		"tailnetIp":       d.cfg.TailnetIP,
		"relayListenAddr": d.cfg.Relay.ListenAddr,
		"csrPEM":          string(csrPEM),
		"csrPem":          string(csrPEM),
	})
	client, err := d.newServerHTTPClient()
	if err != nil {
		return err
	}
	baseURL := strings.TrimRight(d.cfg.ServerURL, "/")
	resp, err := postRegisterJSON(ctx, client, baseURL+"/agent/v1/dev/register", reqBody)
	if err != nil {
		return fmt.Errorf("请求 Server 注册开发 Agent 失败: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		resp, err = postRegisterJSON(ctx, client, baseURL+"/agent/v1/register", reqBody)
		if err != nil {
			return fmt.Errorf("请求回退注册接口失败: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Server 拒绝注册令牌: HTTP %d", resp.StatusCode)
	}

	var rawMap map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rawMap); err == nil {
		dataMap := rawMap
		if inner, ok := rawMap["data"].(map[string]any); ok {
			dataMap = inner
		}
		certPEM := pickStrField(dataMap, "certChainPEM", "certChainPem")
		if certPEM != "" {
			_ = os.MkdirAll(filepath.Dir(d.cfg.Relay.CertFile), 0755)
			_ = os.WriteFile(d.cfg.Relay.CertFile, []byte(certPEM), 0644)
			_ = os.WriteFile(d.cfg.Relay.KeyFile, keyPEM, 0600)
		}
		srvPub := pickStrField(dataMap, "serverSignPubB64", "serverPublicKeyB64")
		if srvPub != "" {
			d.cfg.ServerPublicKeyB64 = srvPub
		}
		if numID, ok := dataMap["agentId"].(float64); ok && numID > 0 {
			d.cfg.AgentID = fmt.Sprintf("%.0f", numID)
		} else if strID, ok := dataMap["agentId"].(string); ok && strings.TrimSpace(strID) != "" {
			d.cfg.AgentID = strings.TrimSpace(strID)
		}
		d.cfg.BootstrapToken = ""
		if d.cfgFilePath != "" {
			_ = config.Save(d.cfgFilePath, d.cfg)
		}
	}
	return nil
}

// postRegisterJSON 发送单次 JSON POST 注册请求并返回 HTTP 响应。
func postRegisterJSON(ctx context.Context, client *http.Client, targetURL string, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return client.Do(httpReq)
}

// pickStrField 依次按候选键名读取非空字符串值。
func pickStrField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// EmitEvent 将事件持久化写入本地 JSONL 发件箱；若当前 WSS 在线则立即推送，离线则自动积压待重连后续传。
// 参数 eventType 为事件名，payload 为任意结构体；返回入箱后的 envelope.Message 及磁盘错误。
func (d *Daemon) EmitEvent(eventType string, payload any) (envelope.Message, error) {
	msg, err := d.outbox.Enqueue(eventType, d.lastServerSeq.Load(), payload)
	if err != nil {
		return envelope.Message{}, err
	}
	_ = d.trySendMessage(msg)
	return msg, nil
}

// Outbox 返回守护进程绑定的发件箱指针（便于测试核验离线缓存与 Ack 压缩）。
// 无入参；返回 *Outbox。
func (d *Daemon) Outbox() *Outbox {
	return d.outbox
}

// Run 启动常驻 WSS 连接主循环：掉线自动按指数退避重连，重连成功后先续传离线积压发件箱再启动 15 秒心跳与指令监听，直到 ctx 结束。
// 参数 ctx 为守护生命周期上下文；正常取消退出返回 nil。
func (d *Daemon) Run(ctx context.Context) error {
	_, _, err := d.EnsureAgentIdentity()
	if err != nil {
		return err
	}

	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		connErr := d.runSingleWSSSession(ctx)
		if ctx.Err() != nil {
			return nil
		}

		// 断线指数退避重连（1×, 2×, 4× ... 封顶 60s）
		shift := attempt
		if shift > 6 {
			shift = 6
		}
		backoff := d.reconnectBaseWait * time.Duration(1<<shift)
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
		attempt++
		_ = connErr

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		// 一旦某次会话存活过久后断开，下次退避会在新会话握手成功时重置
	}
}

// runSingleWSSSession 建立单次 WSS 长连接，续传未确认发件箱，维持 15s 心跳并循环接收 Server 指令。
func (d *Daemon) runSingleWSSSession(ctx context.Context) error {
	wsURL, pathWithQuery, err := buildWSSURL(d.cfg.ServerURL, d.cfg.ServerWSPath)
	if err != nil {
		return err
	}
	tlsCfg, err := d.resolveTLSConfig()
	if err != nil {
		return err
	}

	headers := http.Header{}
	if priv, keyErr := d.secrets.Get(d.cfg.KeychainService, publisher.AgentIdentityAccount); keyErr == nil && priv != "" {
		ts := fmt.Sprintf("%d", time.Now().Unix())
		nonce, _ := crypto.GenerateRandomHex(12)
		if sig, sigErr := crypto.SignHTTPRequestEd25519(priv, http.MethodGet, pathWithQuery, ts, nonce, nil); sigErr == nil {
			headers.Set("X-AutoShip-Agent-ID", d.cfg.AgentID)
			headers.Set("X-AutoShip-Timestamp", ts)
			headers.Set("X-AutoShip-Nonce", nonce)
			headers.Set("X-AutoShip-Signature", sig)
		}
	}

	dialer := websocket.Dialer{
		TLSClientConfig:  tlsCfg,
		HandshakeTimeout: 15 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		return err
	}

	d.mu.Lock()
	d.activeConn = conn
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.activeConn == conn {
			d.activeConn = nil
		}
		d.mu.Unlock()
		_ = conn.Close()
	}()

	// 1. 重连成功后按序续传所有 Server 尚未确认的发件箱事件
	for _, pendingMsg := range d.outbox.PendingAfter(d.lastServerAck.Load()) {
		if err := d.writeConnJSON(conn, pendingMsg); err != nil {
			return err
		}
	}

	// 2. 启动 15 秒周期心跳协程（心跳直接走实时帧，不塞满持久化发件箱）
	sessCtx, cancelSess := context.WithCancel(ctx)
	defer cancelSess()
	go func() {
		// 连接建立立即发一帧上线心跳
		_ = d.sendHeartbeatFrame(conn)
		ticker := time.NewTicker(d.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-ticker.C:
				if err := d.sendHeartbeatFrame(conn); err != nil {
					cancelSess()
					_ = conn.Close()
					return
				}
			}
		}
	}()

	// 当外部 ctx 取消时主动关闭底层连接以唤醒阻塞的 ReadMessage
	go func() {
		<-sessCtx.Done()
		_ = conn.Close()
	}()

	// 3. 循环读取 Server 下发信封消息
	for {
		_, raw, readErr := conn.ReadMessage()
		if readErr != nil {
			return readErr
		}
		var envMsg envelope.Message
		if err := json.Unmarshal(raw, &envMsg); err != nil {
			continue
		}
		if envMsg.Ack > 0 {
			d.lastServerAck.Store(envMsg.Ack)
			_ = d.outbox.AckUpTo(envMsg.Ack)
		}
		if envMsg.Seq > d.lastServerSeq.Load() {
			d.lastServerSeq.Store(envMsg.Seq)
		}
		if envMsg.Type == "evt.ack" || envMsg.Type == "evt.pong" {
			continue
		}
		d.handleInboundCommand(ctx, conn, envMsg)
	}
}

// handleInboundCommand 对收到的指令按 Message.ID 做幂等判重，立即回传确认帧并异步分发执行。
func (d *Daemon) handleInboundCommand(ctx context.Context, conn *websocket.Conn, cmd envelope.Message) {
	if strings.TrimSpace(cmd.ID) == "" {
		return
	}
	if d.markCommandIfNew(cmd.ID, cmd.Type, cmd.Seq) {
		go d.executeCommandPayload(ctx, cmd)
	}
	// 无论首次执行还是重复投递，均回传带最新 Ack 的确认帧让 Server 更新队列状态
	ackMsg, err := envelope.NewMessage(uuid.NewString(), "evt.ack", 0, d.lastServerSeq.Load(), map[string]string{
		"ackedCommandId": cmd.ID,
	})
	if err == nil {
		_ = d.writeConnJSON(conn, ackMsg)
	}
}

// executeCommandPayload 按指令类型路由到沙箱热更新、预览回收、开发引擎循环或自动发布流水线。
func (d *Daemon) executeCommandPayload(ctx context.Context, cmd envelope.Message) {
	switch cmd.Type {
	case "cmd.sandbox.quick_action":
		var p struct {
			ProductCode string `json:"productCode"`
			Action      string `json:"action"`
			TargetPath  string `json:"targetPath"`
		}
		if json.Unmarshal(cmd.Payload, &p) == nil {
			d.mu.Lock()
			prod, ok := d.productsByCode[p.ProductCode]
			if ok {
				_ = sandbox.ApplyQuickAction(&prod.Sandbox, p.Action, p.TargetPath)
				d.productsByCode[p.ProductCode] = prod
			}
			d.mu.Unlock()
			_, _ = d.EmitEvent("evt.sandbox.policy_updated", p)
		}

	case "cmd.preview.stop":
		var p struct {
			WorkID string `json:"workId"`
		}
		if json.Unmarshal(cmd.Payload, &p) == nil && d.previewManager != nil {
			_ = d.previewManager.Stop(ctx, p.WorkID)
			_, _ = d.EmitEvent("evt.preview.stopped", p)
		}

	case "cmd.engine.config":
		var p struct {
			AgyModel  string `json:"agyModel"`
			AgyEffort string `json:"agyEffort"`
		}
		if json.Unmarshal(cmd.Payload, &p) == nil {
			d.mu.Lock()
			d.cfg.Engine.AgyModel = strings.TrimSpace(p.AgyModel)
			d.cfg.Engine.AgyEffort = strings.TrimSpace(p.AgyEffort)
			if d.cfgFilePath != "" {
				_ = config.Save(d.cfgFilePath, d.cfg)
			}
			d.mu.Unlock()
		}

	case "cmd.work.start", "cmd.work.resume":
		d.runDevWorkCommand(ctx, cmd.Payload)

	case "cmd.work.plan_decision":
		var p struct {
			WorkItemID uint64 `json:"workItemId"`
			Approved   bool   `json:"approved"`
			Comment    string `json:"comment"`
		}
		if json.Unmarshal(cmd.Payload, &p) == nil && p.WorkItemID > 0 {
			if snap, ok := d.loadTaskSnapshot(p.WorkItemID); ok {
				if p.Approved {
					snap.TaskReq.PlanConfirmed = true
					snap.TaskReq.ApprovedPlan = snap.LastPlan
				} else {
					snap.TaskReq.PlanConfirmed = false
					snap.TaskReq.ApprovedPlan = nil
				}
				if strings.TrimSpace(p.Comment) != "" {
					snap.TaskReq.OwnerReplies = append(snap.TaskReq.OwnerReplies, p.Comment)
				}
				d.executeResolvedWorkTask(ctx, snap.TaskReq, snap.WorkItemID, snap.ProductID)
			}
		}

	case "cmd.work.answer":
		var p struct {
			WorkItemID uint64 `json:"workItemId"`
			Answer     string `json:"answer"`
		}
		if json.Unmarshal(cmd.Payload, &p) == nil && p.WorkItemID > 0 {
			if snap, ok := d.loadTaskSnapshot(p.WorkItemID); ok {
				if strings.TrimSpace(p.Answer) != "" {
					snap.TaskReq.OwnerReplies = append(snap.TaskReq.OwnerReplies, p.Answer)
				}
				d.executeResolvedWorkTask(ctx, snap.TaskReq, snap.WorkItemID, snap.ProductID)
			}
		}

	case "cmd.work.review_decision":
		var p struct {
			WorkItemID uint64 `json:"workItemId"`
			Action     string `json:"action"`
			Comment    string `json:"comment"`
		}
		if json.Unmarshal(cmd.Payload, &p) == nil && p.WorkItemID > 0 {
			if p.Action == "abandon" && d.previewManager != nil {
				_ = d.previewManager.Stop(ctx, fmt.Sprintf("work-%d", p.WorkItemID))
				return
			}
			if p.Action == "reject" {
				if snap, ok := d.loadTaskSnapshot(p.WorkItemID); ok {
					snap.TaskReq.PlanConfirmed = true
					snap.TaskReq.ApprovedPlan = snap.LastPlan
					if strings.TrimSpace(p.Comment) != "" {
						snap.TaskReq.OwnerReplies = append(snap.TaskReq.OwnerReplies, "验收打回修改意见："+p.Comment)
					}
					d.executeResolvedWorkTask(ctx, snap.TaskReq, snap.WorkItemID, snap.ProductID)
				}
			}
		}

	case "cmd.release.publish":
		d.runReleasePublishCommand(ctx, cmd.Payload)
	}
}

// runDevWorkCommand 解析直接或 Server 格式的开工指令并进入并发受控的任务执行流。
func (d *Daemon) runDevWorkCommand(ctx context.Context, rawPayload json.RawMessage) {
	taskReq, workItemID, productID, err := d.decodeInboundWorkRequest(rawPayload)
	if err != nil {
		_, _ = d.EmitEvent("evt.work.failed", map[string]any{"workItemId": workItemID, "error": "解析任务指令失败: " + err.Error()})
		return
	}
	d.executeResolvedWorkTask(ctx, taskReq, workItemID, productID)
}

// executeResolvedWorkTask 获取并行开发槽位与防休眠锁，准备独立 worktree 并调用编码引擎（builtin 或 agy）执行任务。
func (d *Daemon) executeResolvedWorkTask(ctx context.Context, taskReq engine.TaskRequest, workItemID, productID uint64) {
	select {
	case d.taskSem <- struct{}{}:
		defer func() { <-d.taskSem }()
	case <-ctx.Done():
		return
	}

	holder := "work:" + taskReq.WorkID
	if d.inhibitor != nil {
		_ = d.inhibitor.Acquire(holder)
		defer d.inhibitor.Release(holder)
	}

	// 若未预先指定 worktree，则由 workspace.Manager 创建独立仓库克隆与分支 worktree
	if strings.TrimSpace(taskReq.WorktreeDir) == "" && d.wsManager != nil {
		wtSess, err := d.wsManager.CreateTaskWorktree(ctx, taskReq.Product, taskReq.WorkID, taskReq.TargetModule, taskReq.BaseRef)
		if err != nil {
			_, _ = d.EmitEvent("evt.work.failed", map[string]any{"workId": taskReq.WorkID, "workItemId": workItemID, "error": err.Error()})
			return
		}
		taskReq.WorktreeDir = wtSess.WorktreeDir
		taskReq.BaseRef = wtSess.BaseCommit
		taskReq.TempDir = wtSess.TempDir
	}
	if strings.TrimSpace(taskReq.TranscriptPath) == "" {
		taskReq.TranscriptPath = filepath.Join(d.cfg.TranscriptsDir(), taskReq.WorkID+".jsonl")
	}
	taskReq.OnActivity = func(evtType, message string, meta any) {
		_, _ = d.EmitEvent(evtType, map[string]any{
			"workId":     taskReq.WorkID,
			"workItemId": workItemID,
			"summary":    message,
			"message":    message,
			"meta":       meta,
		})
	}
	taskReq.OnSandboxIntercept = func(evt *sandbox.InterceptEvent) {
		_, _ = d.EmitEvent("evt.sandbox.intercepted", evt)
		if workItemID > 0 && evt != nil && len(evt.BlockedPaths) > 0 {
			_, _ = d.EmitEvent("evt.work.sandbox_blocked", map[string]any{
				"workItemId":  workItemID,
				"productId":   productID,
				"blockedPath": evt.BlockedPaths[0],
			})
		}
	}

	if d.devEngine == nil {
		return
	}
	outcome, err := d.devEngine.Execute(ctx, &taskReq)
	if err != nil {
		_, _ = d.EmitEvent("evt.work.failed", map[string]any{"workId": taskReq.WorkID, "workItemId": workItemID, "error": err.Error()})
		return
	}

	d.saveTaskSnapshot(persistedTaskSnapshot{
		WorkItemID: workItemID,
		ProductID:  productID,
		TaskReq:    taskReq,
		LastPlan:   outcome.Plan,
	})

	// 若进入待验收且配置了预览配方，启动本地预览实例并将地址与随机测试账号填入验收包
	var pvURL, pvUser, pvPass string
	if outcome.Status == engine.OutcomeReadyForAcceptance && outcome.AcceptancePackage != nil &&
		d.previewManager != nil && strings.TrimSpace(taskReq.Product.Preview.StartCmd) != "" {
		if pvSess, pvErr := d.previewManager.Start(ctx, preview.StartRequest{
			WorkID:      taskReq.WorkID,
			Product:     taskReq.Product,
			WorktreeDir: taskReq.WorktreeDir,
			TempDir:     taskReq.TempDir,
		}); pvErr == nil {
			pvURL = pvSess.ExternalURL
			pvUser = pvSess.TestUsername
			pvPass = pvSess.TestPassword
			outcome.AcceptancePackage.PreviewURL = pvSess.ExternalURL
			outcome.AcceptancePackage.PreviewTestAccount = pvSess.TestUsername + " / " + pvSess.TestPassword
		}
	}

	commitSHA := outcome.WIPCommitSHA
	if outcome.AcceptancePackage != nil && outcome.AcceptancePackage.CommitSHA != "" {
		commitSHA = outcome.AcceptancePackage.CommitSHA
	}
	_ = d.stateStore.UpsertWorkState(AgentWorkStateRecord{
		WorkID:         taskReq.WorkID,
		ProductCode:    taskReq.Product.ProductCode,
		Status:         string(outcome.Status),
		WorktreeDir:    taskReq.WorktreeDir,
		HeadCommitSHA:  commitSHA,
		TranscriptPath: taskReq.TranscriptPath,
		TotalTokens:    outcome.TotalTokens,
	})

	// 若进入等待人工答复（ask_owner）且配置了飞书 Webhook，主动推送提醒卡片（8.7 节）
	if outcome.Status == engine.OutcomeAskOwner && strings.TrimSpace(d.cfg.FeishuWebhookURL) != "" {
		_ = sendFeishuTextWebhook(ctx, d.cfg.FeishuWebhookURL,
			fmt.Sprintf("[autoShip ask_owner] 任务 %s (%s) 请求人工确认：\n%s",
				taskReq.WorkID, taskReq.Product.ProductCode, outcome.AskOwnerQuestion))
	}

	d.emitServerCompatibleOutcome(workItemID, outcome, pvURL, pvUser, pvPass)
	_, _ = d.EmitEvent("evt.work.outcome", map[string]any{
		"workId":     taskReq.WorkID,
		"workItemId": workItemID,
		"outcome":    outcome,
	})
}

// runReleasePublishCommand 执行验收通过后的自动发布指令并将结果或冲突打回事件写入发件箱。
func (d *Daemon) runReleasePublishCommand(ctx context.Context, rawPayload json.RawMessage) {
	if d.publishPipeline == nil {
		return
	}
	var pubReq publisher.PublishRequest
	if err := json.Unmarshal(rawPayload, &pubReq); err != nil {
		return
	}
	res, err := d.publishPipeline.Publish(ctx, &pubReq)
	if err != nil {
		evtName := "evt.release.failed"
		if errors.Is(err, publisher.ErrRebaseConflictNeedReaccept) {
			evtName = "evt.release.conflict_reaccept"
		}
		_, _ = d.EmitEvent(evtName, map[string]any{
			"releaseId": pubReq.ReleaseID,
			"workId":    pubReq.WorkID,
			"error":     err.Error(),
		})
		return
	}
	_, _ = d.EmitEvent("evt.release.published", res)
}

// sendHeartbeatFrame 发送单帧 15 秒存活心跳。
func (d *Daemon) sendHeartbeatFrame(conn *websocket.Conn) error {
	hb, err := envelope.NewMessage(uuid.NewString(), "evt.heartbeat", 0, d.lastServerSeq.Load(), map[string]any{
		"agentId":       d.cfg.AgentID,
		"pendingOutbox": d.outbox.PendingCount(),
		"ts":            time.Now().Unix(),
	})
	if err != nil {
		return err
	}
	return d.writeConnJSON(conn, hb)
}

// trySendMessage 在当前 WSS 在线时直接写出消息帧。
func (d *Daemon) trySendMessage(msg envelope.Message) error {
	d.mu.Lock()
	conn := d.activeConn
	d.mu.Unlock()
	if conn == nil {
		return errors.New("offline")
	}
	return d.writeConnJSON(conn, msg)
}

// writeConnJSON 串行加锁向 WebSocket 连接发送一条 JSON 消息（避免 gorilla/websocket 并发写 panic）。
func (d *Daemon) writeConnJSON(conn *websocket.Conn, msg envelope.Message) error {
	d.connWriteMu.Lock()
	defer d.connWriteMu.Unlock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteJSON(msg)
}

// markCommandIfNew 检查指令 ID 是否首次到达，首次到达时写入内存、磁盘与可选状态库并返回 true。
func (d *Daemon) markCommandIfNew(cmdID, cmdType string, seq uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seenCmdIDs[cmdID] || d.stateStore.HasProcessedCommand(cmdID) {
		return false
	}
	d.seenCmdIDs[cmdID] = true
	saveSeenCommands(d.seenCmdFile, d.seenCmdIDs)
	_ = d.stateStore.RecordCommand(cmdID, cmdType, seq)
	return true
}

// resolveTLSConfig 构造固定 autoShip CA 指纹的 WSS 客户端 TLS 配置（支持测试注入覆盖）。
func (d *Daemon) resolveTLSConfig() (*tls.Config, error) {
	if d.tlsConfigOverride != nil {
		return d.tlsConfigOverride, nil
	}
	u, err := url.Parse(d.cfg.ServerURL)
	if err != nil {
		return nil, err
	}
	return pki.NewCAPinnedTLSConfig(d.cfg.CAFingerprint, u.Hostname())
}

// newServerHTTPClient 构造用于首次注册的固定 CA 指纹 HTTPS 客户端。
func (d *Daemon) newServerHTTPClient() (*http.Client, error) {
	tlsCfg, err := d.resolveTLSConfig()
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   20 * time.Second,
	}, nil
}

// buildWSSURL 将 `https://host:port` 转换为 `wss://host:port/agent/v1/dev/ws` 并返回签名所需的请求路径。
func buildWSSURL(serverURL, wsPath string) (fullWSS string, pathOnly string, err error) {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil {
		return "", "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		u.Scheme = "wss"
	case "wss":
	default:
		return "", "", fmt.Errorf("WSS 守护进程禁止非加密协议: %s", u.Scheme)
	}
	if !strings.HasPrefix(wsPath, "/") {
		wsPath = "/" + wsPath
	}
	u.Path = wsPath
	return u.String(), u.RequestURI(), nil
}

// sendFeishuTextWebhook 向飞书自定义机器人 Webhook 推送纯文本通知消息。
func sendFeishuTextWebhook(ctx context.Context, webhookURL, content string) error {
	body, _ := json.Marshal(map[string]any{
		"msg_type": "text",
		"content":  map[string]string{"text": content},
	})
	reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// loadSeenCommands 从本地 JSON 文件加载已处理的指令 ID 集合。
func loadSeenCommands(path string) map[string]bool {
	out := make(map[string]bool)
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		for _, id := range list {
			out[id] = true
		}
	}
	return out
}

// saveSeenCommands 将最近已处理指令 ID 持久化到本地磁盘。
func saveSeenCommands(path string, m map[string]bool) {
	list := make([]string, 0, len(m))
	for k := range m {
		list = append(list, k)
	}
	if raw, err := json.Marshal(list); err == nil {
		_ = os.WriteFile(path, raw, 0600)
	}
}
