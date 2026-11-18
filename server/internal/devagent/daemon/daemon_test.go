package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"autoship/internal/database"
	"autoship/internal/devagent/config"
	"autoship/pkg/envelope"
	"autoship/pkg/keychain"
	"autoship/pkg/pki"
)

// TestLocalStateModelsCommentsMetadata 验证本地状态库 autoship_agent 的所有 GORM 表模型 100% 具备中文表注释与字段注释。
func TestLocalStateModelsCommentsMetadata(t *testing.T) {
	if err := database.ValidateModelsMetadata(LocalStateModels()); err != nil {
		t.Fatalf("autoship_agent 本地状态表注释审计不通过: %v", err)
	}
}

// TestDaemonWSSHeartbeatReconnectOutboxReplayAndIdempotency 验证 WSS 固定 CA 指纹连接、心跳、离线 JSONL 发件箱断线重连续传与重复指令幂等去重。
func TestDaemonWSSHeartbeatReconnectOutboxReplayAndIdempotency(t *testing.T) {
	tmp := t.TempDir()
	pkiDir := filepath.Join(tmp, "pki")
	ca, err := pki.EnsureCA(pkiDir)
	if err != nil {
		t.Fatal(err)
	}
	srvCert, err := ca.EnsureServerCert(pkiDir, "tailnet", "autoship-server", []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatal(err)
	}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var (
		mu             sync.Mutex
		connCount      atomic.Int32
		receivedEvents []envelope.Message
		heartbeats     atomic.Int32
		policyUpdates  atomic.Int32
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/dev/ws", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-AutoShip-Signature") == "" {
			http.Error(w, "missing signature", http.StatusUnauthorized)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		curConn := connCount.Add(1)

		// 首次连接在收到首条重传事件后故意踢断连接，测试守护进程指数退避重连与未 Ack 事件续传！
		if curConn == 2 {
			// 第二次连接下发两条相同 ID 的指令，验证客户端幂等只执行一次
			dupCmd, _ := envelope.NewMessage("cmd-dup-001", "cmd.sandbox.quick_action", 1, 0, map[string]string{
				"productCode": "demo",
				"action":      "switch_off",
			})
			_ = ws.WriteJSON(dupCmd)
			_ = ws.WriteJSON(dupCmd)
		}

		for {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				_ = ws.Close()
				return
			}
			var m envelope.Message
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			if m.Type == "evt.heartbeat" {
				heartbeats.Add(1)
				continue
			}
			if m.Type == "evt.ack" {
				continue
			}

			mu.Lock()
			receivedEvents = append(receivedEvents, m)
			mu.Unlock()
			if m.Type == "evt.sandbox.policy_updated" {
				policyUpdates.Add(1)
			}

			if curConn == 1 {
				// 第一次连接故意不发 Ack 直接断开，迫使客户端重连重传该事件
				_ = ws.Close()
				return
			}
			// 第二次连接正常返回 Ack 确认号
			ackFrame, _ := envelope.NewMessage("srv-ack", "evt.ack", 0, m.Seq, nil)
			_ = ws.WriteJSON(ackFrame)
		}
	})

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{srvCert},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	cfg := config.Default()
	cfg.RootDir = filepath.Join(tmp, "agent")
	cfg.ServerURL = "https://127.0.0.1:" + portStr
	cfg.CAFingerprint = ca.CAFingerprint
	cfg.Products = []config.ProductProfile{{ProductCode: "demo"}}

	d, err := NewDaemon(cfg, keychain.NewMemoryStore(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	d.SetTimingsAndTLS(50*time.Millisecond, 40*time.Millisecond, nil)

	// 在连接启动前先离线写入一条事件到 JSONL 发件箱
	offlineMsg, err := d.EmitEvent("evt.offline.queued", map[string]string{"hello": "world"})
	if err != nil || offlineMsg.Seq != 1 || d.Outbox().PendingCount() != 1 {
		t.Fatalf("离线事件写入发件箱异常: msg=%+v err=%v", offlineMsg, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = d.Run(ctx)
		close(done)
	}()

	// 等待经历“连接 1 断线 -> 退避重连连接 2 -> 续传离线事件被 Ack 清空 -> 幂等执行重复指令”
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if connCount.Load() >= 2 && d.Outbox().PendingCount() == 0 && policyUpdates.Load() >= 1 && heartbeats.Load() >= 1 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	cancel()
	<-done

	if connCount.Load() < 2 {
		t.Fatalf("期望经历断线重连至少建立 2 次 WSS 连接，实际 %d", connCount.Load())
	}
	if d.Outbox().PendingCount() != 0 {
		t.Fatalf("收到 Server Ack 后发件箱挂起条数应清零，实际 %d", d.Outbox().PendingCount())
	}
	if policyUpdates.Load() != 1 {
		t.Fatalf("重复投递同一 ID 的指令应当幂等只执行 1 次，实际执行了 %d 次", policyUpdates.Load())
	}
	if heartbeats.Load() < 1 {
		t.Fatal("未收到任何 WSS 心跳帧")
	}
}
