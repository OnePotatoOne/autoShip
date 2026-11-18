// Package relay 实现开发 Agent 在 tailnet 地址上暴露的模型受限 HTTPS 中继服务（设计文档 7.5 节），支持 Server Ed25519 请求头验签与防重放、按角色映射本地模型、强制截断 max_tokens、令牌桶限速、流式 SSE 透传以及客服 intake 独立高优并发槽。
package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"autoship/internal/devagent/config"
	"autoship/pkg/crypto"
	"autoship/pkg/keychain"
)

const (
	// HeaderServerTimestamp 是 Server 请求模型中继接口时携带的秒级时间戳请求头。
	HeaderServerTimestamp = "X-AutoShip-Server-Timestamp"
	// HeaderServerNonce 是 Server 请求模型中继接口时携带的防重放随机数请求头。
	HeaderServerNonce = "X-AutoShip-Server-Nonce"
	// HeaderServerSignature 是 Server 使用自身私钥计算出的 Ed25519 签名请求头。
	HeaderServerSignature = "X-AutoShip-Server-Signature"
)

// nonceReplayCache 在签名有效时间窗（5 分钟）内缓存已使用的 Nonce，阻止重放请求。
type nonceReplayCache struct {
	mu    sync.Mutex
	items map[string]int64 // nonce -> expiresUnix
}

// checkAndMark 原子检查 nonce 是否尚未被消费；若已消费返回 false，否则记录过期时间并返回 true。
func (n *nonceReplayCache) checkAndMark(nonce string, now time.Time, ttl time.Duration) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	nowSec := now.Unix()
	if len(n.items) > 4096 {
		for k, exp := range n.items {
			if exp < nowSec {
				delete(n.items, k)
			}
		}
	}
	if exp, exists := n.items[nonce]; exists && exp >= nowSec {
		return false
	}
	n.items[nonce] = now.Add(ttl).Unix()
	return true
}

// Server 封装开发 Agent 在 tailnet 上运行的受限 HTTPS 模型中继服务器（兼容 `/v1/chat/completions` 与 `/relay/v1/health`）。
type Server struct {
	agentCfg          *config.AgentConfig
	secrets           keychain.Store
	limiter           *rate.Limiter
	intakeSem         chan struct{}
	bgSem             chan struct{}
	activeIntake      atomic.Int32
	activeBackground  atomic.Int32
	nonces            *nonceReplayCache
	httpClient        *http.Client
	httpServer        *http.Server
	listener          net.Listener
}

// NewServer 根据开发 Agent 全局配置与钥匙串存储构造受限模型中继服务实例。
// 参数 agentCfg 为全局配置，secrets 为钥匙串存取接口；返回初始化完成的 *Server 及配置校验错误。
func NewServer(agentCfg *config.AgentConfig, secrets keychain.Store) (*Server, error) {
	if agentCfg == nil {
		return nil, errors.New("agentCfg 不能为空")
	}
	agentCfg.ApplyDefaults()
	if secrets == nil {
		secrets = keychain.NewDefaultStore()
	}

	rpm := agentCfg.Relay.RequestsPerMinute
	if rpm <= 0 {
		rpm = 60
	}
	burst := agentCfg.Relay.Burst
	if burst <= 0 {
		burst = 15
	}
	limiter := rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), burst)

	s := &Server{
		agentCfg:   agentCfg,
		secrets:    secrets,
		limiter:    limiter,
		intakeSem:  make(chan struct{}, agentCfg.Relay.IntakeConcurrency),
		bgSem:      make(chan struct{}, agentCfg.Relay.BackgroundConcurrency),
		nonces:     &nonceReplayCache{items: make(map[string]int64)},
		httpClient: &http.Client{Timeout: 3 * time.Minute},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/relay/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)

	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}
	return s, nil
}

// Start 加载 TLS 证书并在配置的 tailnet 监听地址上异步启动受限 HTTPS 中继服务（禁止明文 HTTP）。
// 无入参；返回实际绑定的监听地址（如 `100.64.0.20:8445`）及证书/绑定错误。
func (s *Server) Start() (string, error) {
	pair, err := tls.LoadX509KeyPair(s.agentCfg.Relay.CertFile, s.agentCfg.Relay.KeyFile)
	if err != nil {
		return "", fmt.Errorf("加载中继服务 TLS 证书失败: %w", err)
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{pair},
	}
	ln, err := tls.Listen("tcp", s.agentCfg.Relay.ListenAddr, tlsCfg)
	if err != nil {
		return "", fmt.Errorf("监听模型中继 HTTPS 地址 %s 失败: %w", s.agentCfg.Relay.ListenAddr, err)
	}
	s.listener = ln
	go func() {
		_ = s.httpServer.Serve(ln)
	}()
	return ln.Addr().String(), nil
}

// Shutdown 优雅关闭模型中继 HTTPS 监听器与连接。
// 参数 ctx 为关闭超时上下文；返回关闭错误。
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

// Handler 返回中继服务的顶层 http.Handler（便于单元测试或复用监听器）。
// 无入参；返回 http.Handler。
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// WaitUntilIntakeIdle 供后台开发引擎作为避让钩子挂载：当客服对话（intake）存在活跃并发请求时短暂礼让，保障在线用户实时体验（7.5 节）。
// 参数 ctx 为请求上下文；正常或礼让超时后返回 nil，上下文取消返回 ctx.Err()。
func (s *Server) WaitUntilIntakeIdle(ctx context.Context) error {
	deadline := time.Now().Add(3 * time.Second)
	for s.activeIntake.Load() > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(40 * time.Millisecond):
		}
	}
	return nil
}

// handleHealth 处理 `/relay/v1/health` 探活请求：验签后探测本机模型网关连通性并返回并发槽水位（7.5 节：探活失败触发 Server 留言模式）。
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "仅支持 GET")
		return
	}
	if _, err := s.verifyServerRequest(r); err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}

	reachable := s.probeLocalUpstream(r.Context())
	statusCode := http.StatusOK
	statusStr := "ok"
	if !reachable {
		statusCode = http.StatusServiceUnavailable
		statusStr = "upstream_unreachable"
	}

	roles := make([]string, 0, len(s.agentCfg.Relay.RoleModels))
	for role := range s.agentCfg.Relay.RoleModels {
		roles = append(roles, role)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":            statusStr,
		"upstreamReachable": reachable,
		"roles":             roles,
		"activeIntake":      s.activeIntake.Load(),
		"activeBackground":  s.activeBackground.Load(),
	})
}

// handleChatCompletions 处理 `/v1/chat/completions` 请求：验签防重放 → 速率限制 → 角色白名单映射与 max_tokens 封顶 → 高低优并发槽调度 → 流式 SSE / JSON 原样转发。
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	bodyBytes, err := s.verifyServerRequest(r)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if !s.limiter.Allow() {
		writeJSONError(w, http.StatusTooManyRequests, "模型中继整体请求速率超限")
		return
	}

	var payload map[string]any
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "请求体 JSON 格式非法")
		return
	}

	roleKey, _ := payload["model"].(string)
	roleKey = strings.TrimSpace(roleKey)
	targetCfg, allowed := s.agentCfg.Relay.RoleModels[roleKey]
	if !allowed {
		// 严格不是通用代理：拒绝任何未在本地声明的角色或任意模型名
		writeJSONError(w, http.StatusForbidden, fmt.Sprintf("未授权的中继模型角色 %q（仅允许已登记角色如 intake/triage）", roleKey))
		return
	}
	prof, ok := s.agentCfg.FindProfile(targetCfg.ProfileName)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("本地未找到角色 %s 关联的 Profile %s", roleKey, targetCfg.ProfileName))
		return
	}

	// 将逻辑角色名重写为本机实际模型名，并强制限制单请求 max_tokens 上限
	actualModel := targetCfg.ModelName
	if strings.TrimSpace(actualModel) == "" {
		actualModel = prof.Model
	}
	payload["model"] = actualModel
	maxLimit := s.agentCfg.Relay.MaxTokensLimit
	if curMax, ok := asInt(payload["max_tokens"]); !ok || curMax <= 0 || curMax > maxLimit {
		payload["max_tokens"] = maxLimit
	}
	isStream, _ := payload["stream"].(bool)

	// 按角色分配独立并发槽：intake 走专属高优槽，其余角色走后台低优槽（绝不挤占客服槽位）
	releaseSlot, slotAcquired := s.acquireRoleSlot(r.Context(), roleKey)
	if !slotAcquired {
		writeJSONError(w, http.StatusTooManyRequests, fmt.Sprintf("角色 %s 当前并发槽已满", roleKey))
		return
	}
	defer releaseSlot()

	rewrittenBody, err := json.Marshal(payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "序列化转发请求失败")
		return
	}

	upstreamURL := strings.TrimRight(strings.TrimSpace(prof.BaseURL), "/") + "/chat/completions"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstreamURL, bytes.NewReader(rewrittenBody))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	if isStream {
		upReq.Header.Set("Accept", "text/event-stream")
	}
	if apiKey := s.resolveProfileKey(prof); apiKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	upResp, err := s.httpClient.Do(upReq)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "访问本机模型网关失败: "+err.Error())
		return
	}
	defer upResp.Body.Close()

	if ct := upResp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	if isStream && upResp.StatusCode == http.StatusOK {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
	}
	w.WriteHeader(upResp.StatusCode)

	flusher, canFlush := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := upResp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if isStream && canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
}

// verifyServerRequest 读取请求体并校验 Server 携带的 `X-AutoShip-Server-Timestamp`、`X-AutoShip-Server-Nonce`、`X-AutoShip-Server-Signature` 头及防重放缓存。
func (s *Server) verifyServerRequest(r *http.Request) ([]byte, error) {
	pubKey := strings.TrimSpace(s.agentCfg.ServerPublicKeyB64)
	if pubKey == "" {
		return nil, errors.New("开发 Agent 未配置 ServerPublicKeyB64，拒绝中继请求")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		return nil, errors.New("读取请求体失败")
	}
	ts := r.Header.Get(HeaderServerTimestamp)
	nonce := r.Header.Get(HeaderServerNonce)
	sig := r.Header.Get(HeaderServerSignature)

	now := time.Now()
	if err := crypto.VerifyHTTPRequestEd25519(pubKey, r.Method, r.URL.RequestURI(), ts, nonce, body, sig, now, 5*time.Minute); err != nil {
		return nil, fmt.Errorf("Server 请求验签不通过: %w", err)
	}
	if !s.nonces.checkAndMark(nonce, now, 6*time.Minute) {
		return nil, errors.New("检测到重复的 X-AutoShip-Server-Nonce 重放请求")
	}
	return body, nil
}

// acquireRoleSlot 为 intake 客服对话申请独立高优并发槽，或为后台角色申请低优并发槽。
func (s *Server) acquireRoleSlot(ctx context.Context, role string) (func(), bool) {
	if role == "intake" {
		select {
		case s.intakeSem <- struct{}{}:
			s.activeIntake.Add(1)
			return func() {
				s.activeIntake.Add(-1)
				<-s.intakeSem
			}, true
		case <-ctx.Done():
			return nil, false
		case <-time.After(10 * time.Second):
			return nil, false
		}
	}

	// 非 intake 的后台分拣请求使用独立小并发池且短等待超时，确保绝不阻塞客服对话
	select {
	case s.bgSem <- struct{}{}:
		s.activeBackground.Add(1)
		return func() {
			s.activeBackground.Add(-1)
			<-s.bgSem
		}, true
	case <-ctx.Done():
		return nil, false
	case <-time.After(300 * time.Millisecond):
		return nil, false
	}
}

// probeLocalUpstream 快速探测首个本机模型网关地址的连通性。
func (s *Server) probeLocalUpstream(ctx context.Context) bool {
	if len(s.agentCfg.Models.Profiles) == 0 {
		return false
	}
	base := strings.TrimRight(s.agentCfg.Models.Profiles[0].BaseURL, "/")
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return false
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	// 只要网关 HTTP 层存活可达（即使 /models 需要鉴权返回 401/404）即证明本机模型进程在线
	return resp.StatusCode < 500
}

// resolveProfileKey 从钥匙串或配置提取本地模型 API Key（密钥永远留在 Mac 本机）。
func (s *Server) resolveProfileKey(prof config.ModelProfile) string {
	if s.secrets != nil && prof.KeychainAccount != "" {
		if v, err := s.secrets.Get(s.agentCfg.KeychainService, prof.KeychainAccount); err == nil && v != "" {
			return v
		}
	}
	return prof.APIKey
}

// asInt 将 JSON 解码后的 float64 / int 安全转换为整数。
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

// writeJSONError 输出统一 JSON 错误响应。
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
