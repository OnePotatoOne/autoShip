package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"autoship/internal/devagent/config"
	"autoship/pkg/keychain"
)

// ErrBudgetExhausted 表示当前开发任务已触达配置的 Token、对话轮数或执行时长预算上限。
var ErrBudgetExhausted = errors.New("开发任务预算已耗尽")

// ChatMessage 表示符合 OpenAI Chat Completions 协议的单条对话消息（支持纯文本、多模态图片与工具调用）。
type ChatMessage struct {
	Role       string     `json:"role"`                 // system | user | assistant | tool
	Content    any        `json:"content,omitempty"`    // string 或 []ContentPart（多模态看图时使用）
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"` // assistant 角色发起的工具调用列表
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool 角色回传结果对应的调用 ID
	Name       string     `json:"name,omitempty"`       // 可选工具名称
}

// ContentPart 描述多模态消息中的文本片段或 Base64 / HTTPS 图片片段。
type ContentPart struct {
	Type     string        `json:"type"`               // "text" 或 "image_url"
	Text     string        `json:"text,omitempty"`     // 当 Type="text" 时的文本内容
	ImageURL *ImageURLSpec `json:"image_url,omitempty"` // 当 Type="image_url" 时的图片地址结构
}

// ImageURLSpec 包装 OpenAI Vision 协议中的 `image_url.url` 字段。
type ImageURLSpec struct {
	URL string `json:"url"` // 形如 data:image/png;base64,...
}

// ToolDefinition 定义暴露给大模型的单个函数工具 JSON Schema 描述。
type ToolDefinition struct {
	Type     string           `json:"type"`     // 固定为 "function"
	Function ToolFunctionSpec `json:"function"` // 函数名称、说明与参数 Schema
}

// ToolFunctionSpec 描述工具函数的签名元数据。
type ToolFunctionSpec struct {
	Name        string         `json:"name"`        // 工具名称
	Description string         `json:"description"` // 工具用途说明
	Parameters  map[string]any `json:"parameters"`  // 标准 JSON Schema 对象
}

// ToolCall 表示模型单轮回复中请求执行的一个工具调用。
type ToolCall struct {
	ID       string           `json:"id"`       // 调用唯一 ID
	Type     string           `json:"type"`     // 固定为 "function"
	Function ToolCallFunction `json:"function"` // 函数名与 JSON 参数串
}

// ToolCallFunction 包含模型输出的工具函数名及序列化参数字符串。
type ToolCallFunction struct {
	Name      string `json:"name"`      // 工具函数名
	Arguments string `json:"arguments"` // JSON 编码的参数文本
}

// ChatCompletionResponse 表示单轮模型推理返回的助手消息、Token 用量及实际命中的模型通道。
type ChatCompletionResponse struct {
	Message          ChatMessage `json:"message"`          // 模型返回的 assistant 消息
	PromptTokens     int         `json:"promptTokens"`     // 输入 Token 数
	CompletionTokens int         `json:"completionTokens"` // 输出 Token 数
	TotalTokens      int         `json:"totalTokens"`      // 本轮总计 Token 数
	ProfileUsed      string      `json:"profileUsed"`      // 实际成功响应的 Profile 名称
	ModelUsed        string      `json:"modelUsed"`        // 实际响应的模型名
}

// VisionProbeResult 记录阶段 0 验证或启动探测时当前模型通道是否支持多模态看图的结构化结论（第 12 节）。
type VisionProbeResult struct {
	Supported   bool   `json:"supported"`   // 模型接口是否正常接收并解析了图片输入
	ProfileUsed string `json:"profileUsed"` // 探测命中的 Profile 名
	ModelUsed   string `json:"modelUsed"`   // 探测使用的模型名
	LatencyMs   int64  `json:"latencyMs"`   // 探测往返耗时毫秒数
	Detail      string `json:"detail"`      // 模型回复摘录或不支持的错误说明
}

// BudgetTracker 并发安全地统计并强控单个开发任务的累计 Token、模型调用轮数与运行时长（8.4 节）。
type BudgetTracker struct {
	mu          sync.Mutex
	budget      config.TaskBudget
	startedAt   time.Time
	totalTokens int
	turns       int
}

// NewBudgetTracker 根据给定任务预算上限初始化资源追踪器。
// 参数 budget 为 Token/时长/轮数上限配置；返回开始计时的 *BudgetTracker。
func NewBudgetTracker(budget config.TaskBudget) *BudgetTracker {
	if budget.MaxTokens <= 0 {
		budget.MaxTokens = 500000
	}
	if budget.MaxDurationMinutes <= 0 {
		budget.MaxDurationMinutes = 60
	}
	if budget.MaxTurns <= 0 {
		budget.MaxTurns = 80
	}
	return &BudgetTracker{
		budget:    budget,
		startedAt: time.Now(),
	}
}

// CheckBeforeTurn 在发起下一轮大模型请求前检查时长、轮数或累计 Token 是否已经触顶。
// 无入参；未超预算返回 nil，触顶返回包裹原因的 ErrBudgetExhausted。
func (b *BudgetTracker) CheckBeforeTurn() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	maxDur := time.Duration(b.budget.MaxDurationMinutes) * time.Minute
	if time.Since(b.startedAt) >= maxDur {
		return fmt.Errorf("%w: 执行耗时已达上限 %d 分钟", ErrBudgetExhausted, b.budget.MaxDurationMinutes)
	}
	if b.turns >= b.budget.MaxTurns {
		return fmt.Errorf("%w: 模型对话轮数已达上限 %d 轮", ErrBudgetExhausted, b.budget.MaxTurns)
	}
	if b.totalTokens >= b.budget.MaxTokens {
		return fmt.Errorf("%w: 累计 Token (%d) 已达上限 %d", ErrBudgetExhausted, b.totalTokens, b.budget.MaxTokens)
	}
	return nil
}

// RecordTurn 在单轮模型响应返回后累加轮数与 Token 消耗。
// 参数 tokens 为本轮消耗的总 Token 数；无返回值。
func (b *BudgetTracker) RecordTurn(tokens int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.turns++
	if tokens > 0 {
		b.totalTokens += tokens
	}
}

// Snapshot 返回当前累计消耗的 Token 总数、已用轮数与已运行耗时。
// 无入参；返回 (totalTokens, turns, elapsed)。
func (b *BudgetTracker) Snapshot() (int, int, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalTokens, b.turns, time.Since(b.startedAt)
}

// FailoverChatClient 实现支持多 Profile 顺序故障转移、指数退避重试、钥匙串取密钥及客服高优避让钩子的原生 OpenAI Chat Completions 客户端（8.1 & 8.4 节）。
type FailoverChatClient struct {
	keychainSvc    string
	secrets        keychain.Store
	retryBaseDelay time.Duration
	beforeCallHook func(ctx context.Context) error
}

// NewFailoverChatClient 创建多模型通道故障转移客户端。
// 参数 keychainService 为钥匙串服务命名空间，secrets 为钥匙串存取接口，beforeCallHook 为可选避让钩子（如等待客服 intake 槽空闲）；返回 *FailoverChatClient。
func NewFailoverChatClient(keychainService string, secrets keychain.Store, beforeCallHook func(ctx context.Context) error) *FailoverChatClient {
	if strings.TrimSpace(keychainService) == "" {
		keychainService = "autoship.devagent"
	}
	return &FailoverChatClient{
		keychainSvc:    keychainService,
		secrets:        secrets,
		retryBaseDelay: 150 * time.Millisecond,
		beforeCallHook: beforeCallHook,
	}
}

// SetRetryBaseDelay 允许单元测试调整退避重试初始等待时长以加速故障转移用例执行。
// 参数 d 为基础退避时长；无返回值。
func (c *FailoverChatClient) SetRetryBaseDelay(d time.Duration) {
	if d > 0 {
		c.retryBaseDelay = d
	}
}

// Complete 按传入的 profiles 优先级顺序调用 `/chat/completions`，单通道重试仍失败时自动故障转移到下一个 Profile。
// 参数 ctx 为上下文，profiles 为候选通道列表，messages 为对话上下文，tools 为工具定义列表，budget 为可选预算追踪器；返回 *ChatCompletionResponse 及错误。
func (c *FailoverChatClient) Complete(
	ctx context.Context,
	profiles []config.ModelProfile,
	messages []ChatMessage,
	tools []ToolDefinition,
	budget *BudgetTracker,
) (*ChatCompletionResponse, error) {
	if len(profiles) == 0 {
		return nil, errors.New("未提供任何可用的模型 Profile")
	}
	if budget != nil {
		if err := budget.CheckBeforeTurn(); err != nil {
			return nil, err
		}
	}
	if c.beforeCallHook != nil {
		if err := c.beforeCallHook(ctx); err != nil {
			return nil, err
		}
	}

	var errs []string
	for _, prof := range profiles {
		maxAttempts := prof.MaxRetries + 1
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		for attempt := 0; attempt < maxAttempts; attempt++ {
			if attempt > 0 {
				delay := c.retryBaseDelay * time.Duration(1<<(attempt-1))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
			}
			resp, err := c.callSingleProfile(ctx, prof, messages, tools)
			if err == nil {
				if budget != nil {
					budget.RecordTurn(resp.TotalTokens)
				}
				return resp, nil
			}
			errs = append(errs, fmt.Sprintf("[%s#%d] %v", prof.Name, attempt+1, err))
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
		}
	}
	return nil, fmt.Errorf("全部模型 Profile 调用均失败: %s", strings.Join(errs, " | "))
}

// ProbeVisionCapability 向首个可用模型通道发送一张内存生成的 4x4 纯色 PNG 图片，实测该模型（如 deepseek-v4.1-flash）是否支持多模态看图（阶段 0 核心指标，第 12 节）。
// 参数 ctx 为上下文，profiles 为待探测的模型通道列表；返回结构化 *VisionProbeResult（不支持看图时返回 Supported=false 且不抛致命异常）。
func (c *FailoverChatClient) ProbeVisionCapability(ctx context.Context, profiles []config.ModelProfile) *VisionProbeResult {
	start := time.Now()
	pngDataURL, err := buildTinyProbePNGDataURL()
	if err != nil {
		return &VisionProbeResult{Supported: false, Detail: err.Error()}
	}

	msgs := []ChatMessage{
		{
			Role: "user",
			Content: []ContentPart{
				{Type: "text", Text: "请用一句话简述这张图片的颜色，若收到图片请回复 OK。"},
				{Type: "image_url", ImageURL: &ImageURLSpec{URL: pngDataURL}},
			},
		},
	}
	resp, callErr := c.Complete(ctx, profiles, msgs, nil, nil)
	latency := time.Since(start).Milliseconds()
	if callErr != nil {
		return &VisionProbeResult{
			Supported: false,
			LatencyMs: latency,
			Detail:    fmt.Sprintf("多模态图片请求不被上游支持或调用失败: %v", callErr),
		}
	}
	text, _ := resp.Message.Content.(string)
	return &VisionProbeResult{
		Supported:   true,
		ProfileUsed: resp.ProfileUsed,
		ModelUsed:   resp.ModelUsed,
		LatencyMs:   latency,
		Detail:      strings.TrimSpace(text),
	}
}

// callSingleProfile 向单个 OpenAI 兼容端点发送 POST `/chat/completions` 请求并解析响应。
func (c *FailoverChatClient) callSingleProfile(
	ctx context.Context,
	prof config.ModelProfile,
	messages []ChatMessage,
	tools []ToolDefinition,
) (*ChatCompletionResponse, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(prof.BaseURL), "/") + "/chat/completions"
	reqPayload := map[string]any{
		"model":       prof.Model,
		"messages":    messages,
		"temperature": 0.1,
	}
	if len(tools) > 0 {
		reqPayload["tools"] = tools
	}
	rawBody, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	timeout := time.Duration(prof.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey := c.resolveAPIKey(prof); apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}

	client := &http.Client{Timeout: timeout}
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(httpResp.Body, 8*1024*1024))
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, truncateStr(string(respBytes), 300))
	}

	var parsed struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role      string     `json:"role"`
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return nil, fmt.Errorf("解析 OpenAI 响应 JSON 失败: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return nil, errors.New("模型响应 choices 为空")
	}

	choice := parsed.Choices[0].Message
	totalTok := parsed.Usage.TotalTokens
	if totalTok == 0 {
		totalTok = parsed.Usage.PromptTokens + parsed.Usage.CompletionTokens
	}
	if totalTok == 0 {
		// 若本地兼容网关未返回 usage 字段，按字符数保守估算以免预算控制失效
		totalTok = (len(rawBody) + len(respBytes)) / 4
	}
	modelUsed := parsed.Model
	if modelUsed == "" {
		modelUsed = prof.Model
	}

	return &ChatCompletionResponse{
		Message: ChatMessage{
			Role:      "assistant",
			Content:   choice.Content,
			ToolCalls: choice.ToolCalls,
		},
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
		TotalTokens:      totalTok,
		ProfileUsed:      prof.Name,
		ModelUsed:        modelUsed,
	}, nil
}

// resolveAPIKey 优先从 macOS 钥匙串读取模型 API Key，未命中时回退至内存配置字段。
func (c *FailoverChatClient) resolveAPIKey(prof config.ModelProfile) string {
	if c.secrets != nil && strings.TrimSpace(prof.KeychainAccount) != "" {
		if secret, err := c.secrets.Get(c.keychainSvc, prof.KeychainAccount); err == nil && strings.TrimSpace(secret) != "" {
			return strings.TrimSpace(secret)
		}
	}
	return strings.TrimSpace(prof.APIKey)
}

// buildTinyProbePNGDataURL 在内存中生成一张合法的 4x4 蓝色 PNG 图像并编码为 Data URL。
func buildTinyProbePNGDataURL() (string, error) {
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	blue := color.RGBA{R: 30, G: 120, B: 220, A: 255}
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, blue)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// truncateStr 按字节安全截断超长日志或错误片段。
func truncateStr(s string, maxLen int) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) <= maxLen {
		return trimmed
	}
	return trimmed[:maxLen] + "..."
}
