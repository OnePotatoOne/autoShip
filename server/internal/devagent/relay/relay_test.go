package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"autoship/internal/devagent/config"
	"autoship/pkg/crypto"
	"autoship/pkg/keychain"
	"autoship/pkg/pki"
)

// TestRelaySignatureSSERoleCapAndIntakePriority 全面验证模型中继的 HTTPS 监听、Server Ed25519 验签防重放、角色映射与 max_tokens 封顶、SSE 流式透传以及 intake 独立高优并发槽不被后台打满阻塞。
func TestRelaySignatureSSERoleCapAndIntakePriority(t *testing.T) {
	serverPub, serverPriv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var lastModel string
	var lastMaxTokens int
	var lastAuth string
	blockTriage := make(chan struct{})

	// 模拟本机 workbuddy2api-hub-local 上游服务
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-v4.1-flash"}]}`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var reqObj struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
		}
		_ = json.Unmarshal(raw, &reqObj)

		mu.Lock()
		lastModel = reqObj.Model
		lastMaxTokens = reqObj.MaxTokens
		lastAuth = r.Header.Get("Authorization")
		mu.Unlock()

		// 当请求来自后台 triage 慢任务时故意阻塞，测试客服 intake 独立并发槽是否完全不受影响
		if bytes.Contains(raw, []byte("SLOW_TRIAGE")) {
			<-blockTriage
		}

		if reqObj.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"你好\"}}]}\n\n")
			if fl != nil {
				fl.Flush()
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			if fl != nil {
				fl.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"triage done"}}]}`))
	}))
	defer upstream.Close()

	pkiDir := filepath.Join(t.TempDir(), "pki")
	ca, err := pki.EnsureCA(pkiDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ca.EnsureServerCert(pkiDir, "agent", "relay", []string{"127.0.0.1"}, nil); err != nil {
		t.Fatal(err)
	}

	secrets := keychain.NewMemoryStore()
	_ = secrets.Set("autoship.devagent", "llm.local-hub", "sk-mac-local-secret-999")

	cfg := config.Default()
	cfg.ServerPublicKeyB64 = serverPub
	cfg.Relay.ListenAddr = "127.0.0.1:0"
	cfg.Relay.CertFile = filepath.Join(pkiDir, "agent.crt")
	cfg.Relay.KeyFile = filepath.Join(pkiDir, "agent.key")
	cfg.Relay.MaxTokensLimit = 1024
	cfg.Relay.IntakeConcurrency = 2
	cfg.Relay.BackgroundConcurrency = 1
	cfg.Models.Profiles[0].BaseURL = upstream.URL + "/v1"

	srv, err := NewServer(cfg, secrets)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("启动 Relay HTTPS 失败: %v", err)
	}
	defer srv.Shutdown(context.Background())

	client, err := pki.NewCAPinnedHTTPClient(ca.CAFingerprint, "127.0.0.1", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	baseHTTPS := "https://" + addr

	// 1. 未签名请求必须返回 401
	unauthResp, err := client.Get(baseHTTPS + "/relay/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = unauthResp.Body.Close()
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未签名请求预期 401，实际 %d", unauthResp.StatusCode)
	}

	// 2. 合法签名探活 /relay/v1/health 返回 200，且同 Nonce 重放返回 401
	healthReq := mustSignServerRequest(t, serverPriv, http.MethodGet, baseHTTPS+"/relay/v1/health", "nonce-health-1", nil)
	healthResp, err := client.Do(healthReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("合法签名探活预期 200，实际 %d", healthResp.StatusCode)
	}

	replayReq := mustSignServerRequest(t, serverPriv, http.MethodGet, baseHTTPS+"/relay/v1/health", "nonce-health-1", nil)
	replayReq.Header.Set(HeaderServerTimestamp, healthReq.Header.Get(HeaderServerTimestamp))
	replayReq.Header.Set(HeaderServerSignature, healthReq.Header.Get(HeaderServerSignature))
	replayResp, err := client.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = replayResp.Body.Close()
	if replayResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("重复 Nonce 重放请求预期 401，实际 %d", replayResp.StatusCode)
	}

	// 3. 未登记角色请求必须返回 403（证明不是通用开放代理）
	forbidBody := []byte(`{"model":"gpt-4o-arbitrary","messages":[{"role":"user","content":"hi"}]}`)
	forbidReq := mustSignServerRequest(t, serverPriv, http.MethodPost, baseHTTPS+"/v1/chat/completions", "nonce-forbid", forbidBody)
	forbidResp, err := client.Do(forbidReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = forbidResp.Body.Close()
	if forbidResp.StatusCode != http.StatusForbidden {
		t.Fatalf("未授权模型角色预期 403，实际 %d", forbidResp.StatusCode)
	}

	// 4. 占满后台低优并发槽（BackgroundConcurrency=1），验证第二个后台请求被 429 拒绝的同时，客服 intake 流式 SSE 请求依然秒级畅通！
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		slowBody := []byte(`{"model":"triage","max_tokens":99999,"messages":[{"role":"user","content":"SLOW_TRIAGE"}]}`)
		req := mustSignServerRequest(t, serverPriv, http.MethodPost, baseHTTPS+"/v1/chat/completions", "nonce-bg-1", slowBody)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	time.Sleep(80 * time.Millisecond)

	// 第二个 triage 因后台槽位已满返回 429
	bg2Body := []byte(`{"model":"triage","messages":[{"role":"user","content":"second triage"}]}`)
	bg2Req := mustSignServerRequest(t, serverPriv, http.MethodPost, baseHTTPS+"/v1/chat/completions", "nonce-bg-2", bg2Body)
	bg2Resp, err := client.Do(bg2Req)
	if err != nil {
		t.Fatal(err)
	}
	_ = bg2Resp.Body.Close()
	if bg2Resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("后台槽占满时第二个 triage 预期 429，实际 %d", bg2Resp.StatusCode)
	}

	// 与此同时，客服 intake 流式 SSE 请求走独立高优槽立即成功，且 max_tokens 被截断为 1024、自动注入钥匙串 API Key
	intakeBody := []byte(`{"model":"intake","max_tokens":8192,"stream":true,"messages":[{"role":"user","content":"帮我报个障"}]}`)
	intakeReq := mustSignServerRequest(t, serverPriv, http.MethodPost, baseHTTPS+"/v1/chat/completions", "nonce-intake-1", intakeBody)
	intakeResp, err := client.Do(intakeReq)
	if err != nil {
		t.Fatal(err)
	}
	sseData, _ := io.ReadAll(intakeResp.Body)
	_ = intakeResp.Body.Close()
	if intakeResp.StatusCode != http.StatusOK || !strings.Contains(string(sseData), "你好") || !strings.Contains(string(sseData), "[DONE]") {
		t.Fatalf("intake 流式 SSE 转发异常: status=%d body=%q", intakeResp.StatusCode, string(sseData))
	}

	mu.Lock()
	gotModel, gotMax, gotAuth := lastModel, lastMaxTokens, lastAuth
	mu.Unlock()
	if gotModel != "deepseek-v4.1-flash" || gotMax != 1024 || gotAuth != "Bearer sk-mac-local-secret-999" {
		t.Fatalf("角色重写/max_tokens 封顶/密钥注入不符合预期: model=%s max=%d auth=%s", gotModel, gotMax, gotAuth)
	}

	close(blockTriage)
	wg.Wait()
}

// mustSignServerRequest 构造携带 Server Ed25519 签名请求头的测试 HTTP 请求。
func mustSignServerRequest(t *testing.T, serverPriv, method, fullURL, nonce string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, fullURL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	ts := fmt.Sprintf("%d", time.Now().Unix())
	sig, err := crypto.SignHTTPRequestEd25519(serverPriv, method, req.URL.RequestURI(), ts, nonce, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(HeaderServerTimestamp, ts)
	req.Header.Set(HeaderServerNonce, nonce)
	req.Header.Set(HeaderServerSignature, sig)
	return req
}
