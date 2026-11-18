// Package service 实现统一 OpenAI Chat Completions 兼容调用客户端（支持经开发 Agent tailnet 受限中继与直连配置无缝切换、Server Ed25519 请求签名、定时探活及离线恢复回调）。
package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"autoship/internal/config"
	agentSvc "autoship/internal/modules/agent/service"
	"autoship/internal/modules/llm/model"
	"autoship/internal/modules/llm/repository"
	"autoship/pkg/crypto"
	"autoship/pkg/pki"
)

// ChatMessage 表示标准 OpenAI Chat Completions 消息项。
type ChatMessage struct {
	Role       string     `json:"role"`                 // system | user | assistant | tool
	Content    string     `json:"content"`              // 文本内容
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // assistant 发起的工具调用列表
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall 表示模型生成的单个无副作用函数调用指令。
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolDefinition 描述提供给客服 AI 使用的单个无副作用 JSON Schema 工具定义。
type ToolDefinition struct {
	Type     string         `json:"type"`
	Function map[string]any `json:"function"`
}

// CompletionRequest 封装一次客服或分拣模型请求参数。
type CompletionRequest struct {
	ProductID      uint64           // 关联产品 ID
	ConversationID uint64           // 关联会话 ID
	RoleName       string           // 逻辑角色：intake | triage
	Messages       []ChatMessage    // 对话上下文
	Tools          []ToolDefinition // 可选工具定义列表
	OnDelta        func(text string) // 流式文字增量回调（可为 nil）
}

// CompletionResult 封装模型回复文本、工具调用及消耗 Token。
type CompletionResult struct {
	Content          string     // 完整回复文本
	ToolCalls        []ToolCall // 模型请求执行的工具列表
	PromptTokens     int        // 输入 Token
	CompletionTokens int        // 输出 Token
}

// Service 统一管理模型中继/直连请求、健康状态缓存与恢复通知。
type Service struct {
	repo            *repository.Repository
	cfg             *config.ServerConfig
	encKey          []byte
	ca              *pki.Bundle
	agents          *agentSvc.Service
	directClient    *http.Client
	mu              sync.RWMutex
	healthy         bool
	lastCheckedAt   *time.Time
	onRecoveredFunc func()
}

// New 创建统一模型调用服务。
// 参数 repo 为用量仓储，cfg 为全局配置，encKey 为密钥，ca 为根 CA（用于固定指纹访问开发 Agent 中继），agents 为 Agent 服务；返回 *Service。
func New(repo *repository.Repository, cfg *config.ServerConfig, encKey []byte, ca *pki.Bundle, agents *agentSvc.Service) *Service {
	return &Service{
		repo:   repo,
		cfg:    cfg,
		encKey: encKey,
		ca:     ca,
		agents: agents,
		directClient: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

// SetOnRecovered 注册当模型通道从离线恢复为可用时的回调函数（用于按 FIFO 处理“待 AI 跟进”会话与排队分拣）。
// 参数 fn 为恢复处理函数；无返回值。
func (s *Service) SetOnRecovered(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRecoveredFunc = fn
}

// HealthSnapshot 返回当前模型通道模式、可用性与最近探活时间（供总览页展示与新建会话判断是否降级为表单模式）。
// 无入参；返回 mode、healthy、relayOnline、lastCheckedAt。
func (s *Service) HealthSnapshot() (mode string, healthy bool, relayOnline bool, lastCheckedAt *time.Time) {
	mode = s.cfg.LLM.Mode
	_, relayOnline = s.agents.ActiveRelayEndpoint()
	s.mu.RLock()
	healthy = s.healthy
	lastCheckedAt = s.lastCheckedAt
	s.mu.RUnlock()

	if mode == "relay" && !relayOnline {
		healthy = false
	}
	if mode == "direct" && strings.TrimSpace(s.cfg.LLM.DirectBaseURL) != "" && lastCheckedAt == nil {
		healthy = true
	}
	return mode, healthy, relayOnline, lastCheckedAt
}

// IsAvailable 快速判断当前模型通道是否可用；若尚未探活则立即执行一次轻量探活。
// 参数 ctx 为请求上下文；可用返回 true，离线返回 false。
func (s *Service) IsAvailable(ctx context.Context) bool {
	_, healthy, _, checked := s.HealthSnapshot()
	if checked == nil {
		return s.ProbeOnce(ctx)
	}
	return healthy
}

// StartProbeLoop 启动后台定时探活协程（每 20 秒探测一次模型中继或直连接口）。
// 参数 stopCh 为退出信号通道；无返回值。
func (s *Service) StartProbeLoop(stopCh <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.ProbeOnce(ctx)
		cancel()

		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				pctx, pcancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = s.ProbeOnce(pctx)
				pcancel()
			}
		}
	}()
}

// ProbeOnce 执行单次模型通道探活，若状态从不可用切换为可用则触发恢复回调。
// 参数 ctx 为超时上下文；返回是否可用布尔值。
func (s *Service) ProbeOnce(ctx context.Context) bool {
	ok := s.doProbe(ctx)
	now := time.Now()
	s.mu.Lock()
	wasHealthy := s.healthy
	s.healthy = ok
	s.lastCheckedAt = &now
	cb := s.onRecoveredFunc
	s.mu.Unlock()

	if !wasHealthy && ok && cb != nil {
		go cb()
	}
	return ok
}

// Complete 发送一次 Chat Completions 请求（支持 SSE 流式分片解析或标准 JSON 响应），并自动登记 Token 消耗。
// 参数 ctx 为请求上下文，req 为对话输入与流式回调；返回 *CompletionResult 及错误。
func (s *Service) Complete(ctx context.Context, req CompletionRequest) (*CompletionResult, error) {
	if req.ConversationID > 0 && s.cfg.LLM.MaxSessionTokens > 0 {
		used, _ := s.repo.SumConversationTokens(req.ConversationID)
		if used >= s.cfg.LLM.MaxSessionTokens {
			return nil, errors.New("当前澄清会话已达到最大 Token 预算上限")
		}
	}

	baseURL, httpClient, modelID, isRelay, err := s.resolveEndpoint(req.RoleName)
	if err != nil {
		return nil, err
	}

	maxTok := s.cfg.LLM.MaxRequestTokens
	if maxTok <= 0 {
		maxTok = 4096
	}
	streamMode := req.OnDelta != nil
	bodyMap := map[string]any{
		"model":      modelID,
		"messages":   req.Messages,
		"max_tokens": maxTok,
		"stream":     streamMode,
	}
	if len(req.Tools) > 0 {
		bodyMap["tools"] = req.Tools
	}
	rawBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
	}

	pathPart := "/v1/chat/completions"
	if isRelay {
		pathPart = "/relay/v1/chat/completions"
	}
	fullURL := strings.TrimRight(baseURL, "/") + pathPart
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")

	if isRelay {
		if err := s.signServerRelayRequest(httpReq, pathPart, rawBody); err != nil {
			return nil, err
		}
		httpReq.Header.Set("X-AutoShip-Role", req.RoleName)
	} else if s.cfg.LLM.DirectAPIKeyEnc != "" {
		if apiKey, decErr := crypto.DecryptField(s.cfg.LLM.DirectAPIKeyEnc, s.encKey); decErr == nil && apiKey != "" {
			httpReq.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}

	start := time.Now()
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		s.recordUsage(req, modelID, 0, 0, time.Since(start), false)
		return nil, fmt.Errorf("请求模型接口失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		s.recordUsage(req, modelID, 0, 0, time.Since(start), false)
		return nil, fmt.Errorf("模型接口返回错误 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBytes)))
	}

	var res *CompletionResult
	if streamMode && strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		res, err = parseSSECompletion(resp.Body, req.OnDelta)
	} else {
		res, err = parseJSONCompletion(resp.Body, req.OnDelta)
	}
	if err != nil {
		s.recordUsage(req, modelID, 0, 0, time.Since(start), false)
		return nil, err
	}
	if res.PromptTokens == 0 && res.CompletionTokens == 0 {
		// 若上游未在流式响应返回 usage，按字符近似估算以便统计和预算保护
		res.PromptTokens = max(1, len(rawBody)/4)
		res.CompletionTokens = max(1, len(res.Content)/4)
	}
	s.recordUsage(req, modelID, res.PromptTokens, res.CompletionTokens, time.Since(start), true)
	return res, nil
}

// RecordExternalUsage 记录开发 Agent 同步回来的任务级模型 Token 消耗。
// 参数 item 为用量记录指针；无返回值。
func (s *Service) RecordExternalUsage(item *model.LLMUsage) {
	_ = s.repo.Record(item)
}

// resolveEndpoint 根据全局配置选择中继或直连模型基地址、带固定 CA 指纹的 HTTP 客户端及请求模型名；参数 role 为逻辑角色；返回地址、客户端、模型名、是否中继及错误。
func (s *Service) resolveEndpoint(role string) (baseURL string, client *http.Client, modelID string, isRelay bool, err error) {
	modelID = s.cfg.LLM.IntakeModel
	if role == "triage" && s.cfg.LLM.TriageModel != "" {
		modelID = s.cfg.LLM.TriageModel
	}
	if modelID == "" {
		modelID = role
	}

	if s.cfg.LLM.Mode == "direct" {
		base := strings.TrimRight(strings.TrimSpace(s.cfg.LLM.DirectBaseURL), "/")
		if base == "" {
			return "", nil, "", false, errors.New("直连模式尚未配置 DirectBaseURL")
		}
		base = strings.TrimSuffix(base, "/v1")
		return base, s.directClient, modelID, false, nil
	}

	relayAddr, ok := s.agents.ActiveRelayEndpoint()
	if !ok {
		return "", nil, "", true, errors.New("开发 Agent 当前离线或未开放模型中继接口")
	}
	if s.ca == nil {
		return relayAddr, s.directClient, role, true, nil
	}
	host := strings.TrimPrefix(relayAddr, "https://")
	pinnedClient, err := pki.NewCAPinnedHTTPClient(s.ca.CAFingerprint, host, 90*time.Second)
	if err != nil {
		return "", nil, "", true, err
	}
	return relayAddr, pinnedClient, role, true, nil
}

// doProbe 对当前配置的直连 `/v1/models` 或开发 Agent 受限中继 `/relay/v1/health` 执行实际网络探活；参数 ctx 为超时上下文；连通正常返回 true，否则返回 false。
func (s *Service) doProbe(ctx context.Context) bool {
	if s.cfg.LLM.Mode == "direct" {
		base := strings.TrimRight(strings.TrimSpace(s.cfg.LLM.DirectBaseURL), "/")
		if base == "" {
			return false
		}
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
		if err != nil {
			return false
		}
		if s.cfg.LLM.DirectAPIKeyEnc != "" {
			if apiKey, decErr := crypto.DecryptField(s.cfg.LLM.DirectAPIKeyEnc, s.encKey); decErr == nil && apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+apiKey)
			}
		}
		resp, err := s.directClient.Do(req)
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode < 500
	}

	relayAddr, ok := s.agents.ActiveRelayEndpoint()
	if !ok {
		return false
	}
	pathPart := "/relay/v1/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, relayAddr+pathPart, nil)
	if err != nil {
		return false
	}
	if err := s.signServerRelayRequest(req, pathPart, nil); err != nil {
		return false
	}
	client := s.directClient
	if s.ca != nil {
		if c, cErr := pki.NewCAPinnedHTTPClient(s.ca.CAFingerprint, strings.TrimPrefix(relayAddr, "https://"), 5*time.Second); cErr == nil {
			client = c
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// signServerRelayRequest 使用 Server 的 Ed25519 私钥为发往开发 Agent 模型中继的 HTTPS 请求附加防重放签名头；参数 req 为请求对象，pathWithQuery 为路径，body 为请求体；返回签名错误。
func (s *Service) signServerRelayRequest(req *http.Request, pathWithQuery string, body []byte) error {
	priv, err := crypto.DecryptField(s.cfg.ServerSignPrivEnc, s.encKey)
	if err != nil || priv == "" {
		return errors.New("缺少 Server 中继签名私钥")
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce, err := crypto.GenerateRandomHex(12)
	if err != nil {
		return err
	}
	sig, err := crypto.SignHTTPRequestEd25519(priv, req.Method, pathWithQuery, ts, nonce, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-AutoShip-Server-Timestamp", ts)
	req.Header.Set("X-AutoShip-Server-Nonce", nonce)
	req.Header.Set("X-AutoShip-Server-Signature", sig)
	return nil
}

// recordUsage 将单次客服或分拣模型请求的输入输出 Token 与延迟耗时写入 llm_usage 表；参数 req 为请求上下文，modelName 为模型标识，promptTok/compTok/latency/ok 为计量指标。
func (s *Service) recordUsage(req CompletionRequest, modelName string, promptTok, compTok int, latency time.Duration, ok bool) {
	_ = s.repo.Record(&model.LLMUsage{
		ProductID:        req.ProductID,
		ConversationID:   req.ConversationID,
		RoleName:         req.RoleName,
		ModelName:        modelName,
		PromptTokens:     promptTok,
		CompletionTokens: compTok,
		TotalTokens:      promptTok + compTok,
		LatencyMs:        latency.Milliseconds(),
		Succeeded:        ok,
	})
}

// parseJSONCompletion 解析非流式 OpenAI Chat Completions JSON 响应并提取内容、工具调用及 Token 用量；参数 r 为响应流，onDelta 为可选回调；返回 *CompletionResult 与错误。
func parseJSONCompletion(r io.Reader, onDelta func(string)) (*CompletionResult, error) {
	var parsed struct {
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(r).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("解析模型 JSON 响应失败: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return &CompletionResult{}, nil
	}
	content := parsed.Choices[0].Message.Content
	if onDelta != nil && content != "" {
		onDelta(content)
	}
	return &CompletionResult{
		Content:          content,
		ToolCalls:        parsed.Choices[0].Message.ToolCalls,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
	}, nil
}

// parseSSECompletion 逐行解析 OpenAI Chat Completions 的 `data:` SSE 流式分片，实时回调增量文本并拼合工具调用参数；参数 r 为响应体流，onDelta 为实时增量回调；返回 *CompletionResult 与错误。
func parseSSECompletion(r io.Reader, onDelta func(string)) (*CompletionResult, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	var contentBuf strings.Builder
	toolMap := make(map[int]*ToolCall)
	var promptTok, compTok int

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if chunk.Usage.PromptTokens > 0 {
			promptTok = chunk.Usage.PromptTokens
			compTok = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			contentBuf.WriteString(delta.Content)
			if onDelta != nil {
				onDelta(delta.Content)
			}
		}
		for _, tc := range delta.ToolCalls {
			entry, exists := toolMap[tc.Index]
			if !exists {
				entry = &ToolCall{ID: tc.ID, Type: "function"}
				toolMap[tc.Index] = entry
			}
			if tc.ID != "" {
				entry.ID = tc.ID
			}
			if tc.Function.Name != "" {
				entry.Function.Name += tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				entry.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	var calls []ToolCall
	for idx := 0; idx < len(toolMap); idx++ {
		if c, ok := toolMap[idx]; ok {
			calls = append(calls, *c)
		}
	}
	return &CompletionResult{
		Content:          contentBuf.String(),
		ToolCalls:        calls,
		PromptTokens:     promptTok,
		CompletionTokens: compTok,
	}, scanner.Err()
}
