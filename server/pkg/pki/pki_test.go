// Package pki_test 验证自建根 CA、服务端双证书签发、开发 Agent CSR 签发与固定 CA 指纹 HTTPS 握手。
package pki_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"autoship/pkg/pki"
)

// TestCAPinnedTLSHandshakeAndCSR 验证仅凭根 CA 指纹即可验证服务端 IP 证书，且错误指纹会被拒绝；同时验证 CSR 签发链路。
func TestCAPinnedTLSHandshakeAndCSR(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.EnsureCA(dir)
	if err != nil {
		t.Fatalf("EnsureCA 失败: %v", err)
	}
	certPair, err := ca.EnsureServerCert(dir, "public", "autoShip Public", []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("EnsureServerCert 失败: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("pinned-ok"))
	}))
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certPair},
	}
	srv.StartTLS()
	defer srv.Close()

	client, err := pki.NewCAPinnedHTTPClient(ca.CAFingerprint, "127.0.0.1", 5*time.Second)
	if err != nil {
		t.Fatalf("NewCAPinnedHTTPClient 失败: %v", err)
	}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("固定 CA 指纹 HTTPS 请求失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}

	// 错误的 CA 指纹必须拒绝握手
	badClient, err := pki.NewCAPinnedHTTPClient("0000000000000000000000000000000000000000000000000000000000000000", "127.0.0.1", 5*time.Second)
	if err != nil {
		t.Fatalf("构造错误指纹客户端失败: %v", err)
	}
	if _, err := badClient.Get(srv.URL); err == nil {
		t.Fatalf("错误 CA 指纹居然握手成功")
	}

	// 验证开发 Agent CSR 签发（私钥不出本地）
	csrPEM, keyPEM, err := pki.GenerateCSR("dev-agent-mac", []string{"100.64.0.10", "127.0.0.1"})
	if err != nil {
		t.Fatalf("GenerateCSR 失败: %v", err)
	}
	signedChain, err := ca.SignCSR(csrPEM, []string{"100.64.0.10", "127.0.0.1"}, 365)
	if err != nil || len(signedChain) == 0 || len(keyPEM) == 0 {
		t.Fatalf("SignCSR 失败: %v", err)
	}
	if _, err := tls.X509KeyPair(signedChain, keyPEM); err != nil {
		t.Fatalf("签发回传的证书链与本地私钥不匹配: %v", err)
	}
}
