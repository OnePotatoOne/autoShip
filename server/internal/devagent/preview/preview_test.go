package preview

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/workspace"
	"autoship/pkg/pki"
)

// TestPreviewLocalTLSProxyAndIdleReaper 验证内置本地 TLS 反向代理 HTTPS 直连转发以及空闲超时自动停止回收。
func TestPreviewLocalTLSProxyAndIdleReaper(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 使用 pkg/pki 生成自建根 CA 与绑定 127.0.0.1 的预览服务端叶证书
	caBundle, err := pki.EnsureCA(filepath.Join(tmpDir, "pki"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caBundle.EnsureServerCert(filepath.Join(tmpDir, "pki"), "agent", "mac-dev-agent", []string{"127.0.0.1"}, nil); err != nil {
		t.Fatal(err)
	}
	certFile := filepath.Join(tmpDir, "pki", "agent.crt")
	keyFile := filepath.Join(tmpDir, "pki", "agent.key")

	// 编写一个最小 Go 后端脚本编译为可执行程序，在 PORT 环境变量指定端口返回明文 HTTP 响应
	wt := filepath.Join(tmpDir, "wt")
	if err := os.MkdirAll(wt, 0755); err != nil {
		t.Fatal(err)
	}
	appSrc := `package main
import (
	"fmt"
	"net/http"
	"os"
)
func main() {
	port := os.Getenv("PORT")
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "preview-ok:%s", os.Getenv("AUTOSHIP_PREVIEW_USER"))
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, nil)
}
`
	if err := os.WriteFile(filepath.Join(wt, "main.go"), []byte(appSrc), 0644); err != nil {
		t.Fatal(err)
	}

	ports, err := workspace.NewPortAllocator(29200, 29230)
	if err != nil {
		t.Fatal(err)
	}
	procs := workspace.NewProcessRegistry()
	inhibitor := workspace.NewSleepInhibitor(procs)

	agentCfg := config.Default()
	agentCfg.RootDir = tmpDir
	agentCfg.TailnetIP = "127.0.0.1"
	mgr := NewManager(agentCfg, ports, procs, inhibitor, nil)
	mgr.SetIdleTimeout(100 * time.Millisecond)

	prod := config.ProductProfile{
		ProductCode: "demo",
		Sandbox:     config.SandboxPolicyConfig{Mode: "off"},
		Preview: config.PreviewRecipeConfig{
			StartCmd:            "go run main.go",
			SupportsExternalTLS: false,
			PortEnvVar:          "PORT",
		},
	}

	sess, err := mgr.Start(ctx, StartRequest{
		WorkID:      "w-prev-1",
		Product:     prod,
		WorktreeDir: wt,
		TempDir:     tmpDir,
		CertFile:    certFile,
		KeyFile:     keyFile,
	})
	if err != nil {
		t.Fatalf("启动预览失败: %v", err)
	}
	defer mgr.StopAll(ctx)

	if !sess.UsesLocalTLSProxy || sess.ExternalPort == sess.BackendPort {
		t.Fatalf("期望启用双端口本地 TLS 反向代理，实际: %+v", sess)
	}

	// 使用固定 CA 指纹客户端访问预览 HTTPS 地址，轮询等待 go run 启动完成
	httpsClient, err := pki.NewCAPinnedHTTPClient(caBundle.CAFingerprint, "127.0.0.1", 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var bodyStr string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		resp, reqErr := httpsClient.Get(sess.ExternalURL + "/healthz")
		if reqErr == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				bodyStr = string(b)
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	if !strings.HasPrefix(bodyStr, "preview-ok:preview_") {
		t.Fatalf("经内置 TLS 代理访问候选预览返回内容异常: %q", bodyStr)
	}

	// 验证空闲超过阈值后 ReapIdle 自动停止并回收
	reaped := mgr.ReapIdle(ctx, time.Now().Add(2*time.Second))
	if len(reaped) != 1 || reaped[0] != "w-prev-1" {
		t.Fatalf("期望空闲回收 w-prev-1，实际: %v", reaped)
	}
}
