// Package daemon 中的 client.go 封装固定 CA 指纹的 HTTPS 客户端、Ed25519 请求签名注入及断点续传制品下载。
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"autoship/internal/updateagent/config"
	"autoship/pkg/crypto"
	"autoship/pkg/envelope"
	"autoship/pkg/manifest"
	"autoship/pkg/pki"
)

// apiEnvelope 用于兼容解析 Server 统一响应信封 {"code":0,"message":"success","data":...}。
type apiEnvelope struct {
	Code    *int            `json:"code"`    // 业务状态码指针（nil 表示响应未包裹外层 Envelope）
	Message string          `json:"message"` // 服务端提示文案
	Data    json.RawMessage `json:"data"`    // 内层业务数据原始 JSON
}

// ServerClient 负责 autoship-agent 与 autoShip Server 公网 HTTPS 接口的全部加密与签名通信。
type ServerClient struct {
	baseURL    string       // 去除尾部斜杠的 Server HTTPS 根地址
	apiClient  *http.Client // 常规注册、心跳与进度上报 HTTPS 客户端
	pollClient *http.Client // 30 秒长轮询专用 HTTPS 客户端
	dlClient   *http.Client // 大体积制品包断点续传下载专用 HTTPS 客户端
	nowFn      func() time.Time
}

// NewServerClient 基于更新 Agent 配置创建固定根 CA 指纹的 HTTPS 通信客户端集合。
// 参数 cfg 为已校验的 *config.AgentConfig；返回 *ServerClient 及证书指纹初始化错误，供 Daemon 初始化调用。
func NewServerClient(cfg *config.AgentConfig) (*ServerClient, error) {
	parsedURL, err := url.Parse(cfg.ServerURL)
	if err != nil || !strings.EqualFold(parsedURL.Scheme, "https") || parsedURL.Host == "" {
		return nil, fmt.Errorf("无效的 HTTPS serverURL: %q", cfg.ServerURL)
	}
	apiCli, err := pki.NewCAPinnedHTTPClient(cfg.CAFingerprint, parsedURL.Host, time.Duration(cfg.HTTPTimeoutSec)*time.Second)
	if err != nil {
		return nil, err
	}
	pollCli, err := pki.NewCAPinnedHTTPClient(cfg.CAFingerprint, parsedURL.Host, time.Duration(cfg.PollTimeoutSec)*time.Second)
	if err != nil {
		return nil, err
	}
	dlCli, err := pki.NewCAPinnedHTTPClient(cfg.CAFingerprint, parsedURL.Host, time.Duration(cfg.DownloadTimeoutSec)*time.Second)
	if err != nil {
		return nil, err
	}
	return &ServerClient{
		baseURL:    strings.TrimRight(cfg.ServerURL, "/"),
		apiClient:  apiCli,
		pollClient: pollCli,
		dlClient:   dlCli,
		nowFn:      time.Now,
	}, nil
}

// Register 在首次启动时使用一次性令牌向 POST /agent/v1/deploy/register 登记本地生成的 Ed25519 公钥。
// 参数 ctx 为请求上下文，reqPayload 为注册令牌与公钥参数；返回解析后的 *RegisterResponse 及错误。
func (c *ServerClient) Register(ctx context.Context, reqPayload RegisterRequest) (*RegisterResponse, error) {
	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/agent/v1/deploy/register", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	var out RegisterResponse
	if err := c.doJSON(c.apiClient, req, &out); err != nil {
		return nil, fmt.Errorf("调用更新 Agent 注册接口失败: %w", err)
	}
	if out.AgentID.String() == "" {
		return nil, errors.New("注册接口未返回有效的 agentId")
	}
	return &out, nil
}

// SendHeartbeat 使用 Ed25519 签名向 POST /agent/v1/deploy/heartbeat 上报心跳与磁盘/备份占用。
// 参数 ctx 为请求上下文，id 为本地身份凭据，hb 为心跳载荷；成功返回 nil，失败返回错误。
func (c *ServerClient) SendHeartbeat(ctx context.Context, id *Identity, hb HeartbeatRequest) error {
	bodyBytes, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	req, err := c.newSignedRequest(ctx, id, http.MethodPost, "/agent/v1/deploy/heartbeat", bodyBytes)
	if err != nil {
		return err
	}
	return c.doJSON(c.apiClient, req, nil)
}

// PollCommands 使用 Ed25519 签名发起长轮询请求 GET /agent/v1/deploy/poll?ack=<seq> 拉取待执行指令。
// 参数 ctx 为上下文，id 为本地身份凭据，ackSeq 为已确认最大序号，waitSecOverride 为可选等待秒数覆盖（小于 0 时不附带 waitSec 参数，使用服务端默认 30s）；
// 返回统一消息信封切片及网络或鉴权错误。
func (c *ServerClient) PollCommands(ctx context.Context, id *Identity, ackSeq uint64, waitSecOverride int) ([]envelope.Message, error) {
	pathWithQuery := fmt.Sprintf("/agent/v1/deploy/poll?ack=%d", ackSeq)
	if waitSecOverride >= 0 {
		pathWithQuery = fmt.Sprintf("%s&waitSec=%d", pathWithQuery, waitSecOverride)
	}
	req, err := c.newSignedRequest(ctx, id, http.MethodGet, pathWithQuery, nil)
	if err != nil {
		return nil, err
	}

	rawResp, err := c.doRaw(c.pollClient, req)
	if err != nil {
		return nil, err
	}
	var parsed PollResponse
	if err := decodeAPIResponse(rawResp, &parsed); err != nil {
		// 兼容直接返回 []envelope.Message 数组的简化实现
		var directSlice []envelope.Message
		if sliceErr := decodeAPIResponse(rawResp, &directSlice); sliceErr == nil {
			return directSlice, nil
		}
		return nil, fmt.Errorf("解析长轮询指令响应失败: %w", err)
	}
	return parsed.Commands, nil
}

// ReportDeployment 使用 Ed25519 签名向 POST /agent/v1/deploy/deployments/<deploymentId>/report 上报步骤进度与终态诊断。
// 参数 ctx 为上下文，id 为身份凭据，deploymentID 为部署记录唯一标识，report 为上报数据；成功返回 nil，失败返回错误。
func (c *ServerClient) ReportDeployment(ctx context.Context, id *Identity, deploymentID string, report DeployReport) error {
	if err := envelope.ValidateSafeID(deploymentID); err != nil {
		return fmt.Errorf("上报部署 ID 非法: %w", err)
	}
	bodyBytes, err := json.Marshal(report)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/agent/v1/deploy/deployments/%s/report", url.PathEscape(deploymentID))
	req, err := c.newSignedRequest(ctx, id, http.MethodPost, path, bodyBytes)
	if err != nil {
		return err
	}
	return c.doJSON(c.apiClient, req, nil)
}

// DownloadArtifactWithResume 通过 HTTP Range 断点续传下载 /agent/v1/deploy/artifacts/<releaseId>/download 到目标路径，并核对大小与 SHA-256（9.4 第 3 步）。
// 参数 ctx 为上下文，id 为身份凭据，releaseID 为版本 ID，destPath 为最终文件路径，expectedSize/expectedSHA256 为可选预期大小与哈希；
// 成功返回实际字节大小、64 位小写 SHA-256 及 nil，失败返回错误（保留 .part 文件供后续重试续传）。
func (c *ServerClient) DownloadArtifactWithResume(
	ctx context.Context,
	id *Identity,
	releaseID uint64,
	destPath string,
	expectedSize int64,
	expectedSHA256 string,
) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return 0, "", fmt.Errorf("创建下载暂存目录失败: %w", err)
	}
	partPath := destPath + ".part"
	dlPath := fmt.Sprintf("/agent/v1/deploy/artifacts/%d/download", releaseID)

	const maxAttempts = 4
	var lastErr error

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return 0, "", ctx.Err()
		}
		var offset int64
		if info, statErr := os.Stat(partPath); statErr == nil && info.Mode().IsRegular() {
			offset = info.Size()
			// 若已有分片已达到预期大小且哈希匹配，直接完成重命名无需重复请求网络
			if expectedSize > 0 && offset == expectedSize {
				if sz, sum, hErr := manifest.HashFile(partPath); hErr == nil && (expectedSHA256 == "" || strings.EqualFold(sum, expectedSHA256)) {
					if renErr := os.Rename(partPath, destPath); renErr != nil {
						return 0, "", renErr
					}
					return sz, sum, nil
				}
				// 大小相等但哈希不符，说明历史分片损坏，清空从零重下
				_ = os.Remove(partPath)
				offset = 0
			} else if expectedSize > 0 && offset > expectedSize {
				_ = os.Remove(partPath)
				offset = 0
			}
		}

		req, err := c.newSignedRequest(ctx, id, http.MethodGet, dlPath, nil)
		if err != nil {
			return 0, "", err
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}

		resp, err := c.dlClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("第 %d 次下载请求失败: %w", attempt, err)
			continue
		}

		writeErr := writeDownloadStream(partPath, offset, resp)
		_ = resp.Body.Close()
		if writeErr != nil {
			lastErr = fmt.Errorf("第 %d 次接收制品流中断: %w", attempt, writeErr)
			continue
		}

		actualSize, actualSHA, hashErr := manifest.HashFile(partPath)
		if hashErr != nil {
			return 0, "", hashErr
		}
		if expectedSize > 0 && actualSize < expectedSize {
			// 流提前结束但未返回完整个体，下一轮基于新 offset 发起 Range 续传
			lastErr = fmt.Errorf("制品下载尚未完整 (%d/%d 字节)", actualSize, expectedSize)
			continue
		}
		if expectedSize > 0 && actualSize != expectedSize {
			_ = os.Remove(partPath)
			return 0, "", fmt.Errorf("下载制品大小不匹配: 期望 %d 字节，实际 %d 字节", expectedSize, actualSize)
		}
		if expectedSHA256 != "" && !strings.EqualFold(actualSHA, expectedSHA256) {
			_ = os.Remove(partPath)
			return 0, "", fmt.Errorf("下载制品 SHA-256 校验失败: 期望 %s，实际 %s", expectedSHA256, actualSHA)
		}

		if err := os.Chmod(partPath, 0644); err != nil {
			return 0, "", err
		}
		if err := os.Rename(partPath, destPath); err != nil {
			return 0, "", fmt.Errorf("重命名下载完成的制品包失败: %w", err)
		}
		return actualSize, actualSHA, nil
	}
	if lastErr == nil {
		lastErr = errors.New("制品断点续传重试耗尽")
	}
	return 0, "", lastErr
}

// writeDownloadStream 根据 HTTP 状态码（200 全量或 206 断点续传）把响应体安全写入 .part 临时文件。
// 参数 partPath 为临时文件路径，offset 为发起请求时的本地字节偏移，resp 为 HTTP 响应；返回写入错误。
func writeDownloadStream(partPath string, offset int64, resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		f, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, resp.Body)
		syncErr := f.Sync()
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr

	case http.StatusOK:
		// 服务端不支持 Range 或从头返回全量数据，截断重写
		f, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(f, resp.Body)
		syncErr := f.Sync()
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		return closeErr

	case http.StatusRequestedRangeNotSatisfiable:
		if offset > 0 {
			_ = os.Remove(partPath)
		}
		return errors.New("HTTP 416 Range 越界，已重置本地分片")

	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("下载制品返回异常状态码 %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}

// newSignedRequest 构造附带 X-AutoShip-Agent-Id、Timestamp、Nonce 与 Ed25519 签名请求头的 HTTPS 请求。
// 参数 ctx 为上下文，id 为本地 Agent 身份，method 为大写 HTTP 方法，pathWithQuery 为以 "/" 开头的路径及查询串，body 为请求体字节；返回签名完毕的 *http.Request 及错误。
func (c *ServerClient) newSignedRequest(ctx context.Context, id *Identity, method, pathWithQuery string, body []byte) (*http.Request, error) {
	if id == nil || strings.TrimSpace(id.AgentID) == "" || strings.TrimSpace(id.PrivateKeyB64) == "" {
		return nil, errors.New("本地 Agent 身份尚未初始化，无法签名请求")
	}
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+pathWithQuery, bodyReader)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	ts := strconv.FormatInt(c.nowFn().Unix(), 10)
	nonce, err := crypto.GenerateRandomHex(16)
	if err != nil {
		return nil, err
	}
	canonicalURI := req.URL.RequestURI()
	sigB64, err := crypto.SignHTTPRequestEd25519(id.PrivateKeyB64, method, canonicalURI, ts, nonce, body)
	if err != nil {
		return nil, fmt.Errorf("签名 Agent HTTP 请求失败: %w", err)
	}

	req.Header.Set(HeaderAgentID, id.AgentID)
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, sigB64)
	return req, nil
}

// doRaw 发送 HTTP 请求并校验 2xx 状态码，返回完整响应体字节。
// 参数 cli 为底层 HTTP 客户端，req 为已构造请求；成功返回响应字节切片，非 2xx 或网络错误时返回错误。
func (c *ServerClient) doRaw(cli *http.Client, req *http.Request) ([]byte, error) {
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var env apiEnvelope
		if json.Unmarshal(raw, &env) == nil && strings.TrimSpace(env.Message) != "" {
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, env.Message)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, nil
}

// doJSON 发送 HTTP 请求并将响应解码到目标结构体指针（自动兼容 response.Envelope 包装与裸 JSON）。
// 参数 cli 为 HTTP 客户端，req 为请求对象，out 为目标反序列化指针（可为 nil）；成功返回 nil，失败返回错误。
func (c *ServerClient) doJSON(cli *http.Client, req *http.Request, out any) error {
	raw, err := c.doRaw(cli, req)
	if err != nil {
		return err
	}
	return decodeAPIResponse(raw, out)
}

// decodeAPIResponse 解码 Server 返回的 JSON 字节（同时支持 {"code":0,"data":...} 统一信封与直接 JSON 载荷）。
// 参数 raw 为响应体字节，out 为目标指针（nil 时仅校验业务状态码）；成功返回 nil，业务码非零或格式错误时返回错误。
func decodeAPIResponse(raw []byte, out any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		if out == nil {
			return nil
		}
		return errors.New("服务端返回空响应体")
	}

	var env apiEnvelope
	if err := json.Unmarshal(trimmed, &env); err == nil && env.Code != nil {
		if *env.Code != 0 {
			msg := strings.TrimSpace(env.Message)
			if msg == "" {
				msg = fmt.Sprintf("服务端业务错误码 %d", *env.Code)
			}
			return errors.New(msg)
		}
		if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
			return nil
		}
		return json.Unmarshal(env.Data, out)
	}

	if out == nil {
		return nil
	}
	return json.Unmarshal(trimmed, out)
}
