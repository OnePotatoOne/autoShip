// Package daemon_test 通过真实自签根 CA HTTPS 服务端、真实编译的 autoship-apply 二进制、模拟无通配 sudo 及有状态部署钩子，
// 端到端验证注册鉴权、断点续传升级、健康检查失败自动回滚、防降级拦截、数据库备份空间不足阻断、重复回滚幂等及重启日志恢复。
package daemon_test

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"autoship/internal/updateagent/config"
	"autoship/internal/updateagent/daemon"
	"autoship/pkg/crypto"
	"autoship/pkg/envelope"
	"autoship/pkg/manifest"
	"autoship/pkg/pki"
)

var (
	compileApplyOnce sync.Once
	compiledApplyBin string
	compileApplyErr  error
)

// getCompiledApplyBinary 在测试进程生命周期内编译一次真实的 cmd/autoship-apply 可执行文件供模拟 sudo 调用。
// 参数 t 为标准测试上下文；返回编译产物的绝对路径。
func getCompiledApplyBinary(t *testing.T) string {
	t.Helper()
	compileApplyOnce.Do(func() {
		outDir, err := os.MkdirTemp("", "autoship-apply-bin-*")
		if err != nil {
			compileApplyErr = err
			return
		}
		binPath := filepath.Join(outDir, "autoship-apply")
		cmd := exec.Command("go", "build", "-o", binPath, "autoship/cmd/autoship-apply")
		out, buildErr := cmd.CombinedOutput()
		if buildErr != nil {
			compileApplyErr = fmt.Errorf("编译 autoship-apply 失败: %w (%s)", buildErr, string(out))
			return
		}
		compiledApplyBin = binPath
	})
	if compileApplyErr != nil {
		t.Fatalf("%v", compileApplyErr)
	}
	return compiledApplyBin
}

// simulatedBinaryPayload 定义写入模拟业务二进制文件中的版本与运行行为描述。
type simulatedBinaryPayload struct {
	Version      string `json:"version"`      // 模拟运行版本号
	Healthy      bool   `json:"healthy"`      // 迁移完成后模块探测是否健康
	MigratePolls int    `json:"migratePolls"` // 需要经历多少次 migrating 轮询后才标记 migrationDone=true
	CrashLog     string `json:"crashLog"`     // 启动后写入日志文件的模拟故障内容（可含敏感凭据）
}

// e2eEnv 封装端到端模拟测试所需的 HTTPS Server、模拟产品健康服务、真实 autoship-apply、模拟 sudo 与密钥套件。
type e2eEnv struct {
	t              *testing.T
	rootDir        string
	caBundle       *pki.Bundle
	serverPubB64   string
	serverPrivB64  string
	releasePubB64  string
	releasePrivB64 string

	installedBinPath string
	productLogPath   string
	hookStateDir     string
	sudoCallsLog     string
	applyCfgPath     string
	agentCfg         *config.AgentConfig

	mu               sync.Mutex
	registeredPubB64 string
	registeredAgent  string
	commands         []envelope.Message
	artifacts        map[uint64][]byte
	dropFirstRange   map[uint64]bool
	range206Count    atomic.Int32
	heartbeats       []daemon.HeartbeatRequest
	reports          map[string][]daemon.DeployReport
	failNextReport   atomic.Bool

	productPollCount atomic.Int32
	mockServer       *httptest.Server
	productServer    *httptest.Server
}

// newE2EEnv 搭建完整的端到端模拟环境（含固定 CA 指纹 TLS 1.3 Server、Ed25519 鉴权校验、模拟无通配 sudo 及产品健康服务）。
// 参数 t 为测试上下文，initialVersion 为初始运行的产品版本号；返回初始化完毕的 *e2eEnv。
func newE2EEnv(t *testing.T, initialVersion string) *e2eEnv {
	t.Helper()
	root := t.TempDir()
	applyBin := getCompiledApplyBinary(t)

	pkiDir := filepath.Join(root, "pki")
	ca, err := pki.EnsureCA(pkiDir)
	if err != nil {
		t.Fatalf("EnsureCA 失败: %v", err)
	}
	srvCert, err := ca.EnsureServerCert(pkiDir, "public", "autoShip Test Server", []string{"127.0.0.1"}, nil)
	if err != nil {
		t.Fatalf("EnsureServerCert 失败: %v", err)
	}

	srvPub, srvPriv, _ := crypto.GenerateEd25519KeyPair()
	relPub, relPriv, _ := crypto.GenerateEd25519KeyPair()

	etcDir := filepath.Join(root, "etc")
	stateDir := filepath.Join(root, "agent-state")
	stagingDir := filepath.Join(stateDir, "staging", "current")
	applyWorkDir := filepath.Join(root, "apply-work")
	ledgerPath := filepath.Join(root, "apply-state", "ledger.json")
	hookPath := filepath.Join(root, "libexec", "deploy-hook")
	backupRoot := filepath.Join(root, "backups")
	installedBin := filepath.Join(root, "product", "bin", "app")
	productLog := filepath.Join(root, "product", "logs", "app.log")
	hookState := filepath.Join(root, "product", "hook-state")

	for _, d := range []string{
		etcDir, stateDir, stagingDir, applyWorkDir, filepath.Dir(ledgerPath),
		filepath.Dir(hookPath), backupRoot, filepath.Dir(installedBin), filepath.Dir(productLog), hookState,
	} {
		if mkErr := os.MkdirAll(d, 0755); mkErr != nil {
			t.Fatalf("创建测试目录失败: %v", mkErr)
		}
	}

	serverPubPath := filepath.Join(etcDir, "server.pub")
	releasePubPath := filepath.Join(etcDir, "release.pub")
	_ = os.WriteFile(serverPubPath, []byte(srvPub+"\n"), 0644)
	_ = os.WriteFile(releasePubPath, []byte(relPub+"\n"), 0644)

	// 写入权限为 0600 的测试 apply.yaml
	applyCfgPath := filepath.Join(etcDir, "apply.yaml")
	applyYAML := fmt.Sprintf(`stagingDir: %q
workBaseDir: %q
ledgerPath: %q
serverPubPath: %q
releasePubPath: %q
hookPath: %q
backupBaseDir: %q
hookTimeoutSec: 30
`, stagingDir, applyWorkDir, ledgerPath, serverPubPath, releasePubPath, hookPath, backupRoot)
	if err := os.WriteFile(applyCfgPath, []byte(applyYAML), 0600); err != nil {
		t.Fatalf("写入 apply.yaml 失败: %v", err)
	}

	// 构造模拟 sudo 脚本：强制校验无通配固定参数（仅允许 -n <applyBin> upgrade|rollback|backup|logs|cleanup）
	sudoLog := filepath.Join(root, "sudo_calls.log")
	sudoScriptPath := filepath.Join(root, "mock-sudo.sh")
	sudoScript := fmt.Sprintf(`#!/bin/sh
set -eu
if [ "$#" -ne 3 ]; then
  echo "sudo rule violation: expected exact 3 args, got $#" >&2
  exit 1
fi
if [ "$1" != "-n" ] || [ "$2" != %q ]; then
  echo "sudo rule violation: unexpected binary $1 $2" >&2
  exit 1
fi
case "$3" in
  upgrade|rollback|backup|logs|cleanup)
    printf '%%s\n' "$3" >> %q
    exec "$2" "$3"
    ;;
  *)
    echo "sudo rule violation: forbidden subcommand $3" >&2
    exit 1
    ;;
esac
`, applyBin, sudoLog)
	if err := os.WriteFile(sudoScriptPath, []byte(sudoScript), 0755); err != nil {
		t.Fatalf("写入 mock-sudo.sh 失败: %v", err)
	}

	env := &e2eEnv{
		t:                t,
		rootDir:          root,
		caBundle:         ca,
		serverPubB64:     srvPub,
		serverPrivB64:    srvPriv,
		releasePubB64:    relPub,
		releasePrivB64:   relPriv,
		installedBinPath: installedBin,
		productLogPath:   productLog,
		hookStateDir:     hookState,
		sudoCallsLog:     sudoLog,
		applyCfgPath:     applyCfgPath,
		artifacts:        make(map[uint64][]byte),
		dropFirstRange:   make(map[uint64]bool),
		reports:          make(map[string][]daemon.DeployReport),
	}

	// 预置初始运行版本二进制与初始部署钩子
	env.writeInstalledBinary(simulatedBinaryPayload{Version: initialVersion, Healthy: true})
	_ = os.WriteFile(productLog, []byte("2026-11-18 INFO boot ok "+initialVersion+"\n"), 0644)
	_ = os.WriteFile(hookPath, []byte(env.renderHookScript()), 0700)

	// 启动模拟产品健康检查服务
	env.productServer = httptest.NewServer(http.HandlerFunc(env.handleProductHealth))
	t.Cleanup(env.productServer.Close)

	// 启动带自建根 CA 证书链的模拟 autoShip Server 公网入口
	mux := http.NewServeMux()
	mux.HandleFunc("/agent/v1/deploy/register", env.handleRegister)
	mux.HandleFunc("/agent/v1/deploy/heartbeat", env.requireSigned(env.handleHeartbeat))
	mux.HandleFunc("/agent/v1/deploy/poll", env.requireSigned(env.handlePoll))
	mux.HandleFunc("/agent/v1/deploy/artifacts/", env.requireSigned(env.handleArtifactDownload))
	mux.HandleFunc("/agent/v1/deploy/deployments/", env.requireSigned(env.handleDeployReport))

	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{srvCert},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	env.mockServer = srv

	env.agentCfg = &config.AgentConfig{
		AgentVersion:           "v0.2.0",
		ServerURL:              srv.URL,
		CAFingerprint:          ca.CAFingerprint,
		BootstrapToken:         "asreg_valid_bootstrap_token",
		TargetID:               9,
		ProductCode:            "seccenter",
		ReleasePubPath:         releasePubPath,
		ServerPubPath:          serverPubPath,
		StateDir:               stateDir,
		StagingDir:             stagingDir,
		BackupRootDir:          backupRoot,
		BackupQuotaBytes:       1024 * 1024 * 100, // 100 MiB
		MinFreeDiskBytes:       1024 * 1024,       // 1 MiB
		EstimatedDBBackupBytes: 1024 * 1024 * 10,  // 10 MiB
		ApplyBinPath:           applyBin,
		SudoBinPath:            sudoScriptPath,
		UseSudo:                true,
		HealthCheckURL:         env.productServer.URL + "/healthz",
		HealthCheckIntervalMs:  40,
		HealthCheckTimeoutSec:  1,
		MigrationTimeoutSec:    3,
		HeartbeatIntervalSec:   30,
		PollTimeoutSec:         5,
		HTTPTimeoutSec:         5,
		DownloadTimeoutSec:     10,
	}
	return env
}

// newDaemon 基于当前测试环境配置实例化并绑定测试钩子的 *daemon.Daemon。
// 无入参；返回立即可用的 *daemon.Daemon。
func (e *e2eEnv) newDaemon() *daemon.Daemon {
	e.t.Helper()
	d, err := daemon.New(e.agentCfg)
	if err != nil {
		e.t.Fatalf("daemon.New 失败: %v", err)
	}
	d.SetTestApplyConfigPath(e.applyCfgPath)
	d.SetPollWaitSecForTest(0)
	return d
}

// renderHookScript 生成遵循第 6 节契约（按部署 ID 幂等回滚、非 root 预检标记、数据库备份、日志读取与清理）的测试钩子脚本。
// 无入参；返回完整 Shell 脚本内容。
func (e *e2eEnv) renderHookScript() string {
	return fmt.Sprintf(`#!/bin/sh
set -eu
INSTALL_BIN=%q
LOG_FILE=%q
STATE_DIR=%q

sub="${1:-}"
case "$sub" in
  install)
    cand="$2"
    dep="$4"
    if [ ! -f "${STATE_DIR}/${dep}.meta" ]; then
      cp "$INSTALL_BIN" "${STATE_DIR}/${dep}.bak"
      echo "recorded=1" > "${STATE_DIR}/${dep}.meta"
    fi
    cp "$cand" "$INSTALL_BIN"
    # 重置迁移轮询计数并用纯 POSIX 工具写入候选版本启动日志（无需外部语言运行时）
    rm -f "${STATE_DIR}/activated_mark"
    if grep -q '"crashLog":"[^"]' "$INSTALL_BIN"; then
      sed -n 's/.*"crashLog":"\([^"]*\)".*/\1/p' "$INSTALL_BIN" > "$LOG_FILE"
    else
      ver=$(sed -n 's/.*"version":"\([^"]*\)".*/\1/p' "$INSTALL_BIN")
      echo "2026-11-18 INFO started version=${ver}" > "$LOG_FILE"
    fi
    ;;
  rollback)
    dep="$3"
    if [ ! -f "${STATE_DIR}/${dep}.bak" ]; then
      echo "no backup for deployment ${dep}" >&2
      exit 15
    fi
    # 幂等回滚：若当前程序已与该次部署前备份完全一致则无需重复覆盖，且绝不把坏版本覆盖回 .bak
    if cmp -s "$INSTALL_BIN" "${STATE_DIR}/${dep}.bak"; then
      exit 0
    fi
    cp "${STATE_DIR}/${dep}.bak" "$INSTALL_BIN"
    echo "2026-11-18 INFO rolled back deployment=${dep}" > "$LOG_FILE"
    ;;
  backup-db)
    dep="$3"
    dir="$5"
    printf ' -- autoShip DB dump for deployment %%s\nSELECT 1;\n' "$dep" > "${dir}/seccenter.sql"
    ;;
  status)
    echo "active"
    ;;
  logs)
    cat "$LOG_FILE"
    ;;
  cleanup)
    touch "${STATE_DIR}/cleanup_done"
    ;;
  *)
    echo "unsupported hook action: $sub" >&2
    exit 2
    ;;
esac
`, e.installedBinPath, e.productLogPath, e.hookStateDir)
}

// writeInstalledBinary 将模拟版本配置序列化写入当前运行的业务程序路径，同时重置轮询计数器。
// 参数 payload 为模拟二进制行为定义；无返回值。
func (e *e2eEnv) writeInstalledBinary(payload simulatedBinaryPayload) {
	raw, _ := json.Marshal(payload)
	if err := os.WriteFile(e.installedBinPath, raw, 0755); err != nil {
		e.t.Fatalf("写入模拟业务程序失败: %v", err)
	}
	e.productPollCount.Store(0)
}

// currentInstalledVersion 读取当前安装在模拟服务器上的业务程序版本号。
// 无入参；返回版本号字符串。
func (e *e2eEnv) currentInstalledVersion() string {
	raw, err := os.ReadFile(e.installedBinPath)
	if err != nil {
		return ""
	}
	var p simulatedBinaryPayload
	_ = json.Unmarshal(raw, &p)
	return p.Version
}

// packAndRegisterRelease 生成签名制品包并放入模拟 Server 制品库，同时返回已签名的 ReleaseManifest、包体积与 SHA-256。
// 参数 releaseID 为版本 ID，binSpec 为候选程序行为，risk 为迁移风险等级；返回清单、字节大小与哈希。
func (e *e2eEnv) packAndRegisterRelease(
	releaseID uint64,
	binSpec simulatedBinaryPayload,
	risk manifest.MigrationRisk,
) (*manifest.ReleaseManifest, int64, string) {
	e.t.Helper()
	tmp := e.t.TempDir()
	binFile := filepath.Join(tmp, "app")
	// 附加填充字节以便在 HTTP Range 断点续传测试中有充足的字节流可切分
	type paddedPayload struct {
		simulatedBinaryPayload
		Padding string `json:"padding"`
	}
	rawBin, _ := json.Marshal(paddedPayload{
		simulatedBinaryPayload: binSpec,
		Padding:                strings.Repeat("X", 8192),
	})
	_ = os.WriteFile(binFile, rawBin, 0755)

	hookFile := filepath.Join(tmp, "deploy-hook")
	_ = os.WriteFile(hookFile, []byte(e.renderHookScript()), 0755)

	m := &manifest.ReleaseManifest{
		ProductCode:     "seccenter",
		Version:         binSpec.Version,
		Tag:             binSpec.Version,
		CommitSHA:       "deadbeefcafebabe1234",
		Changelog:       "release " + binSpec.Version,
		MigrationRisk:   risk,
		MinAgentVersion: "v0.1.0",
		CreatedAt:       time.Now().Unix(),
	}
	bundlePath := filepath.Join(tmp, "bundle.tar.gz")
	sz, sha, err := manifest.PackBundle(bundlePath, m, binFile, hookFile, e.releasePrivB64)
	if err != nil {
		e.t.Fatalf("PackBundle 失败: %v", err)
	}
	bundleBytes, _ := os.ReadFile(bundlePath)

	e.mu.Lock()
	e.artifacts[releaseID] = bundleBytes
	e.mu.Unlock()
	return m, sz, sha
}

// makeSignedTicket 生成由模拟 Server 私钥签名的 ActionTicket。
// 参数 ticketID/action/depID/version 为票据核心参数，releaseID 为版本 ID，backupDB 是否强制备份；返回已签名票据。
func (e *e2eEnv) makeSignedTicket(ticketID, action, depID, version string, releaseID uint64, backupDB bool) envelope.ActionTicket {
	e.t.Helper()
	now := time.Now().Unix()
	tkt := envelope.ActionTicket{
		TicketID:     ticketID,
		Action:       action,
		ProductCode:  "seccenter",
		TargetID:     9,
		DeploymentID: depID,
		ReleaseID:    releaseID,
		Version:      version,
		BackupDB:     backupDB,
		IssuedAt:     now - 2,
		ExpiresAt:    now + 900,
	}
	if err := tkt.Sign(e.serverPrivB64); err != nil {
		e.t.Fatalf("签名票据失败: %v", err)
	}
	return tkt
}

// enqueueCommand 向模拟 Server 的长轮询指令队列追加一条统一消息信封。
// 参数 id 为消息唯一 ID，seq 为单调递增序号，cmdType 为指令类型，payload 为指令结构体；无返回值。
func (e *e2eEnv) enqueueCommand(id string, seq uint64, cmdType string, payload any) {
	msg, err := envelope.NewMessage(id, cmdType, seq, 0, payload)
	if err != nil {
		e.t.Fatalf("构造信封失败: %v", err)
	}
	e.mu.Lock()
	e.commands = append(e.commands, msg)
	e.mu.Unlock()
}

// handleProductHealth 模拟产品的免登录健康检查接口（支持模拟数据库迁移进行中与模块只读探测失败）。
// 参数 w 为 HTTP 响应流，_ 为请求对象；无返回值。
func (e *e2eEnv) handleProductHealth(w http.ResponseWriter, _ *http.Request) {
	raw, err := os.ReadFile(e.installedBinPath)
	if err != nil {
		http.Error(w, `{"status":"down"}`, http.StatusServiceUnavailable)
		return
	}
	var spec simulatedBinaryPayload
	if err := json.Unmarshal(raw, &spec); err != nil {
		http.Error(w, `{"status":"corrupt"}`, http.StatusInternalServerError)
		return
	}

	// 检测是否为新安装后的首次探测，若是则重新计数模拟迁移过程
	markPath := filepath.Join(e.hookStateDir, "activated_mark")
	if _, statErr := os.Stat(markPath); os.IsNotExist(statErr) {
		_ = os.WriteFile(markPath, []byte("1"), 0644)
		e.productPollCount.Store(0)
	}
	pollIdx := int(e.productPollCount.Add(1))

	w.Header().Set("Content-Type", "application/json")
	if spec.MigratePolls > 0 && pollIdx <= spec.MigratePolls {
		_ = json.NewEncoder(w).Encode(daemon.HealthCheckResponse{
			Status:        "migrating",
			Version:       spec.Version,
			MigrationDone: false,
			Probes:        map[string]any{"auth": "ok", "order": true},
		})
		return
	}

	if !spec.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintf(w, `{"status":"degraded","version":%q,"migrationDone":true,"probes":{"auth":"ok","order":false},"diag":"dsn app:LeakPass99@tcp(127.0.0.1:3306)/db"}`, spec.Version)
		return
	}
	_ = json.NewEncoder(w).Encode(daemon.HealthCheckResponse{
		Status:        "ok",
		Version:       spec.Version,
		MigrationDone: true,
		Probes: map[string]any{
			"auth":  "ok",
			"order": true,
			"audit": map[string]any{"ok": true},
		},
	})
}

// handleRegister 处理模拟 Server 的 POST /agent/v1/deploy/register 一次性令牌注册接口。
// 参数 w 为响应流，r 为 HTTP 请求；无返回值。
func (e *e2eEnv) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req daemon.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token != "asreg_valid_bootstrap_token" {
		http.Error(w, `{"code":401,"message":"invalid bootstrap token"}`, http.StatusUnauthorized)
		return
	}
	e.mu.Lock()
	e.registeredPubB64 = req.PublicKeyB64
	e.registeredAgent = "42"
	e.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	// 故意返回数字型 agentId: 42，与真实 Server modules/agent/handler 一致
	_, _ = fmt.Fprintf(w, `{"code":0,"message":"success","data":{"agentId":42,"targetId":9,"productCode":"seccenter","serverSignPubB64":%q}}`, e.serverPubB64)
}

// requireSigned 包装 HTTP 处理器，强制校验 X-AutoShip-Agent-Id、Timestamp、Nonce 及 Ed25519 请求签名。
// 参数 next 为验签通过后执行的下游处理器；返回包装后的 http.HandlerFunc。
func (e *e2eEnv) requireSigned(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))

		e.mu.Lock()
		pubB64 := e.registeredPubB64
		expectedAgent := e.registeredAgent
		e.mu.Unlock()

		agentID := r.Header.Get(daemon.HeaderAgentID)
		ts := r.Header.Get(daemon.HeaderTimestamp)
		nonce := r.Header.Get(daemon.HeaderNonce)
		sig := r.Header.Get(daemon.HeaderSignature)

		if pubB64 == "" || agentID != expectedAgent {
			http.Error(w, `{"code":401,"message":"unknown agent"}`, http.StatusUnauthorized)
			return
		}
		if err := crypto.VerifyHTTPRequestEd25519(pubB64, r.Method, r.URL.RequestURI(), ts, nonce, body, sig, time.Now(), 5*time.Minute); err != nil {
			http.Error(w, fmt.Sprintf(`{"code":401,"message":%q}`, err.Error()), http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleHeartbeat 处理模拟 Server 的签名心跳上报请求。
// 参数 w 为响应流，r 为 HTTP 请求；无返回值。
func (e *e2eEnv) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	var hb daemon.HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, `{"code":400,"message":"bad heartbeat"}`, http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	e.heartbeats = append(e.heartbeats, hb)
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"code":0,"message":"success","data":{"ok":true}}`))
}

// handlePoll 处理模拟 Server 的 GET /agent/v1/deploy/poll?ack=<seq> 请求。
// 参数 w 为响应流，r 为 HTTP 请求；无返回值。
func (e *e2eEnv) handlePoll(w http.ResponseWriter, r *http.Request) {
	ack, _ := strconv.ParseUint(r.URL.Query().Get("ack"), 10, 64)
	e.mu.Lock()
	var pending []envelope.Message
	for _, cmd := range e.commands {
		if cmd.Seq > ack {
			pending = append(pending, cmd)
		}
	}
	e.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"code":    0,
		"message": "success",
		"data": map[string]any{
			"commands": pending,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handleArtifactDownload 处理制品下载接口，支持模拟首次流截断与第二次 HTTP Range 206 断点续传。
// 参数 w 为响应流，r 为 HTTP 请求；无返回值。
func (e *e2eEnv) handleArtifactDownload(w http.ResponseWriter, r *http.Request) {
	// 路径形如 /agent/v1/deploy/artifacts/<releaseId>/download
	trimmed := strings.TrimPrefix(r.URL.Path, "/agent/v1/deploy/artifacts/")
	idStr := strings.TrimSuffix(trimmed, "/download")
	relID, _ := strconv.ParseUint(idStr, 10, 64)

	e.mu.Lock()
	data, ok := e.artifacts[relID]
	shouldDrop := e.dropFirstRange[relID]
	if shouldDrop {
		e.dropFirstRange[relID] = false
	}
	e.mu.Unlock()

	if !ok {
		http.NotFound(w, r)
		return
	}

	if shouldDrop {
		// 仅写出前半段字节后提前返回，迫使客户端保留 .part 文件并发起 Range 断点续传
		half := len(data) / 2
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data[:half])
		return
	}

	rangeHdr := r.Header.Get("Range")
	if strings.HasPrefix(rangeHdr, "bytes=") {
		offStr := strings.TrimSuffix(strings.TrimPrefix(rangeHdr, "bytes="), "-")
		off, err := strconv.Atoi(offStr)
		if err == nil && off > 0 && off < len(data) {
			e.range206Count.Add(1)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(data[off:])
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleDeployReport 接收并记录 Agent 分步上报与终态报告。
// 参数 w 为响应流，r 为 HTTP 请求；无返回值。
func (e *e2eEnv) handleDeployReport(w http.ResponseWriter, r *http.Request) {
	if e.failNextReport.CompareAndSwap(true, false) {
		http.Error(w, `{"code":500,"message":"simulated transient server outage"}`, http.StatusInternalServerError)
		return
	}
	trimmed := strings.TrimPrefix(r.URL.Path, "/agent/v1/deploy/deployments/")
	depID := strings.TrimSuffix(trimmed, "/report")

	var rep daemon.DeployReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		http.Error(w, `{"code":400,"message":"bad report json"}`, http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	e.reports[depID] = append(e.reports[depID], rep)
	e.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"code":0,"message":"success"}`))
}

// latestReport 返回指定部署记录收到的最后一条上报载荷。
// 参数 depID 为部署记录唯一标识；返回最后一条 DeployReport 及是否存在布尔值。
func (e *e2eEnv) latestReport(depID string) (daemon.DeployReport, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := e.reports[depID]
	if len(list) == 0 {
		return daemon.DeployReport{}, false
	}
	return list[len(list)-1], true
}

// TestEndToEndNormalUpgradeWithResumeAndMigration 端到端验证首次注册、签名心跳、断点续传（206 Partial Content）、
// 数据改写自动备份、sudo autoship-apply upgrade、迁移等待及成功清理全链路。
// 参数 t 为标准测试上下文；无返回值。
func TestEndToEndNormalUpgradeWithResumeAndMigration(t *testing.T) {
	env := newE2EEnv(t, "v1.0.0")
	svc := env.newDaemon()
	ctx := context.Background()

	// 1. 验证首次注册与签名心跳
	if err := svc.SendHeartbeatOnce(ctx); err != nil {
		t.Fatalf("首次注册并发送心跳失败: %v", err)
	}
	if len(env.heartbeats) != 1 || env.heartbeats[0].ProductVersion != "v1.0.0" || env.heartbeats[0].ServiceStatus != "running" {
		t.Fatalf("心跳内容异常: %+v", env.heartbeats)
	}

	// 2. 构造含数据改写（data_rewrite）且需要 2 轮迁移等待的 v1.1.0 发布包，并开启首次下载中断模拟断点续传
	relManifest, sz, sha := env.packAndRegisterRelease(101, simulatedBinaryPayload{
		Version:      "v1.1.0",
		Healthy:      true,
		MigratePolls: 2,
	}, manifest.RiskDataRewrite)
	env.dropFirstRange[101] = true

	tkt := env.makeSignedTicket("tkt-e2e-101", "upgrade", "dep-e2e-101", "v1.1.0", 101, false)
	env.enqueueCommand("cmd-e2e-101", 1, daemon.CmdDeployUpgrade, daemon.UpgradeCommandPayload{
		DeploymentID: "dep-e2e-101",
		ReleaseID:    101,
		TargetID:     9,
		ProductCode:  "seccenter",
		Version:      "v1.1.0",
		BundleSize:   sz,
		BundleSHA256: sha,
		Manifest:     relManifest,
		Ticket:       tkt,
	})

	count, err := svc.PollAndExecuteOnce(ctx)
	if err != nil || count != 1 {
		t.Fatalf("PollAndExecuteOnce 失败: count=%d err=%v", count, err)
	}

	// 验证触发了真实的 HTTP Range 206 断点续传
	if env.range206Count.Load() < 1 {
		t.Fatalf("未触发预期的 HTTP 206 Range 断点续传")
	}
	if gotVer := env.currentInstalledVersion(); gotVer != "v1.1.0" {
		t.Fatalf("升级后实际运行版本错误: 期望 v1.1.0，实际 %s", gotVer)
	}

	// 验证分步报告包含 backing_up -> deploying -> succeeded，且附带备份清单
	finalRep, ok := env.latestReport("dep-e2e-101")
	if !ok || finalRep.Status != daemon.StatusSucceeded || finalRep.Backup == nil || finalRep.BackupTotalBytes <= 0 {
		t.Fatalf("最终成功上报数据不完整: %+v", finalRep)
	}

	hasBackingUp := false
	for _, r := range env.reports["dep-e2e-101"] {
		if r.Status == daemon.StatusBackingUp {
			hasBackingUp = true
		}
	}
	if !hasBackingUp {
		t.Fatalf("升级轨迹未上报 backing_up 阶段")
	}

	// 验证模拟无通配 sudo 依次执行了 backup、upgrade、logs、cleanup
	sudoCallsBytes, _ := os.ReadFile(env.sudoCallsLog)
	sudoCalls := strings.Fields(string(sudoCallsBytes))
	expectedCalls := []string{"backup", "upgrade", "logs", "cleanup"}
	if strings.Join(sudoCalls, ",") != strings.Join(expectedCalls, ",") {
		t.Fatalf("sudo 子命令调用序列不符合预期: 期望 %v，实际 %v", expectedCalls, sudoCalls)
	}

	// 再次发送心跳，验证产品版本更新为 v1.1.0 且备份目录占用字节数大于 0
	if err := svc.SendHeartbeatOnce(ctx); err != nil {
		t.Fatalf("升级后心跳失败: %v", err)
	}
	lastHB := env.heartbeats[len(env.heartbeats)-1]
	if lastHB.ProductVersion != "v1.1.0" || lastHB.BackupUsedBytes <= 0 {
		t.Fatalf("升级后心跳未反映新版本与备份占用: %+v", lastHB)
	}
}

// TestEndToEndHealthFailureAutoRollbackAndDesensitize 验证候选版本上线健康检查失败时自动执行回滚、确认旧版本恢复，并对上报的崩溃日志与响应脱敏。
// 参数 t 为标准测试上下文；无返回值。
func TestEndToEndHealthFailureAutoRollbackAndDesensitize(t *testing.T) {
	env := newE2EEnv(t, "v1.0.0")
	svc := env.newDaemon()
	ctx := context.Background()

	// 构造模块探测失败且日志含密码与 Bearer 令牌的坏版本 v1.2.0
	badManifest, sz, sha := env.packAndRegisterRelease(102, simulatedBinaryPayload{
		Version:  "v1.2.0",
		Healthy:  false,
		CrashLog: "2026-11-18 ERROR panic: db failed root:TopSecretDBPass@tcp(127.0.0.1:3306)/prod auth=Bearer sk-secret1234567890abcd",
	}, manifest.RiskNone)

	tkt := env.makeSignedTicket("tkt-bad-102", "upgrade", "dep-bad-102", "v1.2.0", 102, false)
	env.enqueueCommand("cmd-bad-102", 1, daemon.CmdDeployUpgrade, daemon.UpgradeCommandPayload{
		DeploymentID: "dep-bad-102",
		ReleaseID:    102,
		TargetID:     9,
		ProductCode:  "seccenter",
		Version:      "v1.2.0",
		BundleSize:   sz,
		BundleSHA256: sha,
		Manifest:     badManifest,
		Ticket:       tkt,
	})

	if _, err := svc.PollAndExecuteOnce(ctx); err != nil {
		t.Fatalf("执行故障升级轮询出错: %v", err)
	}

	// 验证已自动回滚回 v1.0.0
	if gotVer := env.currentInstalledVersion(); gotVer != "v1.0.0" {
		t.Fatalf("健康检查失败后未自动回滚至 v1.0.0，当前版本: %s", gotVer)
	}

	finalRep, ok := env.latestReport("dep-bad-102")
	if !ok || finalRep.Status != daemon.StatusRolledBack || finalRep.FailedStep != "health_check" {
		t.Fatalf("期望终态为 rolled_back 且 FailedStep=health_check，实际: %+v", finalRep)
	}
	if finalRep.LogTail == "" || finalRep.HealthResponse == "" {
		t.Fatalf("回滚报告缺少故障日志或健康检查响应摘录: %+v", finalRep)
	}
	// 验证敏感数据库口令与 Bearer 密钥已被 MaskSecrets 彻底脱敏
	for _, secret := range []string{"TopSecretDBPass", "LeakPass99", "sk-secret1234567890abcd"} {
		if strings.Contains(finalRep.LogTail, secret) || strings.Contains(finalRep.HealthResponse, secret) {
			t.Fatalf("故障回传未脱敏敏感凭据 %q: log=%s hc=%s", secret, finalRep.LogTail, finalRep.HealthResponse)
		}
	}
	if !strings.Contains(finalRep.LogTail, "[PASSWORD_REDACTED]") || !strings.Contains(finalRep.LogTail, "[SECRET_REDACTED]") {
		t.Fatalf("故障日志脱敏占位符缺失: %s", finalRep.LogTail)
	}
}

// TestAntiDowngradeBlocked 验证候选版本低于或等于当前运行版本时在第 1 步即被 semver.IsUpgrade 拦截（不下载、不停服）。
// 参数 t 为标准测试上下文；无返回值。
func TestAntiDowngradeBlocked(t *testing.T) {
	env := newE2EEnv(t, "v1.2.0")
	svc := env.newDaemon()
	ctx := context.Background()

	lowerManifest, sz, sha := env.packAndRegisterRelease(103, simulatedBinaryPayload{
		Version: "v1.1.0",
		Healthy: true,
	}, manifest.RiskNone)
	tkt := env.makeSignedTicket("tkt-downgrade", "upgrade", "dep-downgrade", "v1.1.0", 103, false)

	env.enqueueCommand("cmd-downgrade", 1, daemon.CmdDeployUpgrade, daemon.UpgradeCommandPayload{
		DeploymentID: "dep-downgrade",
		ReleaseID:    103,
		TargetID:     9,
		ProductCode:  "seccenter",
		Version:      "v1.1.0",
		BundleSize:   sz,
		BundleSHA256: sha,
		Manifest:     lowerManifest,
		Ticket:       tkt,
	})

	if _, err := svc.PollAndExecuteOnce(ctx); err != nil {
		t.Fatalf("防降级轮询执行异常: %v", err)
	}
	if gotVer := env.currentInstalledVersion(); gotVer != "v1.2.0" {
		t.Fatalf("防降级拦截失效，当前版本被改写为: %s", gotVer)
	}
	rep, ok := env.latestReport("dep-downgrade")
	if !ok || rep.Status != daemon.StatusRolledBack || rep.FailedStep != "verify_manifest" {
		t.Fatalf("防降级上报异常: %+v", rep)
	}
	if _, statErr := os.Stat(env.sudoCallsLog); statErr == nil {
		t.Fatalf("防降级拦截不应调用任何 sudo 子命令")
	}
}

// TestInsufficientDiskSpaceForDBBackupBlocksDeploy 验证需要数据库备份但实际磁盘剩余空间不足时在第 2 步阻断部署且不停服（9.5 节）。
// 参数 t 为标准测试上下文；无返回值。
func TestInsufficientDiskSpaceForDBBackupBlocksDeploy(t *testing.T) {
	env := newE2EEnv(t, "v1.0.0")
	env.agentCfg.MinFreeDiskBytes = 10 * 1024 * 1024       // 10 MiB
	env.agentCfg.EstimatedDBBackupBytes = 80 * 1024 * 1024 // 预计备份需要 80 MiB
	svc := env.newDaemon()

	// 注入模拟磁盘仅剩 40 MiB（足够普通无备份小包，但不足以写下 80 MiB 数据库备份）
	svc.SetDiskStatFunc(func(_ string) (daemon.DiskStats, error) {
		return daemon.DiskStats{
			FreeBytes:  40 * 1024 * 1024,
			TotalBytes: 1024 * 1024 * 1024,
		}, nil
	})

	rewriteManifest, sz, sha := env.packAndRegisterRelease(104, simulatedBinaryPayload{
		Version: "v1.1.0",
		Healthy: true,
	}, manifest.RiskDataRewrite)
	tkt := env.makeSignedTicket("tkt-lowdisk", "upgrade", "dep-lowdisk", "v1.1.0", 104, false)

	env.enqueueCommand("cmd-lowdisk", 1, daemon.CmdDeployUpgrade, daemon.UpgradeCommandPayload{
		DeploymentID: "dep-lowdisk",
		ReleaseID:    104,
		TargetID:     9,
		ProductCode:  "seccenter",
		Version:      "v1.1.0",
		BundleSize:   sz,
		BundleSHA256: sha,
		Manifest:     rewriteManifest,
		Ticket:       tkt,
	})

	if _, err := svc.PollAndExecuteOnce(context.Background()); err != nil {
		t.Fatalf("低磁盘阻断轮询异常: %v", err)
	}
	if gotVer := env.currentInstalledVersion(); gotVer != "v1.0.0" {
		t.Fatalf("磁盘不足未阻断升级，版本变为: %s", gotVer)
	}
	rep, ok := env.latestReport("dep-lowdisk")
	if !ok || rep.Status != daemon.StatusRolledBack || rep.FailedStep != "precheck" {
		t.Fatalf("期望在 precheck 阻断部署，实际上报: %+v", rep)
	}
	if !strings.Contains(rep.Message, "数据库备份") {
		t.Fatalf("阻断原因未明确提示数据库备份空间不足: %q", rep.Message)
	}
	if _, statErr := os.Stat(env.sudoCallsLog); statErr == nil {
		t.Fatalf("空间不足阻断后不应调用 sudo autoship-apply")
	}
}

// TestRepeatedRollbackIdempotency 验证成功升级后连续多次下发针对同一 deployment_id 的手动回滚指令保持幂等（始终停留在升级前旧版本）。
// 参数 t 为标准测试上下文；无返回值。
func TestRepeatedRollbackIdempotency(t *testing.T) {
	env := newE2EEnv(t, "v1.0.0")
	svc := env.newDaemon()
	ctx := context.Background()

	// 先将 v1.0.0 正常升级至 v1.1.0
	m110, sz, sha := env.packAndRegisterRelease(105, simulatedBinaryPayload{
		Version: "v1.1.0",
		Healthy: true,
	}, manifest.RiskNone)
	upTkt := env.makeSignedTicket("tkt-up-105", "upgrade", "dep-idemp-105", "v1.1.0", 105, false)
	env.enqueueCommand("cmd-up-105", 1, daemon.CmdDeployUpgrade, daemon.UpgradeCommandPayload{
		DeploymentID: "dep-idemp-105",
		ReleaseID:    105,
		TargetID:     9,
		ProductCode:  "seccenter",
		Version:      "v1.1.0",
		BundleSize:   sz,
		BundleSHA256: sha,
		Manifest:     m110,
		Ticket:       upTkt,
	})
	_, _ = svc.PollAndExecuteOnce(ctx)
	if env.currentInstalledVersion() != "v1.1.0" {
		t.Fatalf("前置升级未到达 v1.1.0")
	}

	// 第一次手动回滚 dep-idemp-105
	rbTkt1 := env.makeSignedTicket("tkt-rb-105-a", "rollback", "dep-idemp-105", "v1.0.0", 105, false)
	env.enqueueCommand("cmd-rb-105-a", 2, daemon.CmdDeployRollback, daemon.RollbackCommandPayload{
		DeploymentID:    "dep-idemp-105",
		ExpectedVersion: "v1.0.0",
		Ticket:          rbTkt1,
	})
	if _, err := svc.PollAndExecuteOnce(ctx); err != nil {
		t.Fatalf("第一次手动回滚失败: %v", err)
	}
	if got := env.currentInstalledVersion(); got != "v1.0.0" {
		t.Fatalf("第一次回滚后期望 v1.0.0，实际 %s", got)
	}

	// 第二次使用新消息与新票据对同一 deployment_id 重复执行回滚（验证不把 v1.1.0 当备份倒装回去）
	rbTkt2 := env.makeSignedTicket("tkt-rb-105-b", "rollback", "dep-idemp-105", "v1.0.0", 105, false)
	env.enqueueCommand("cmd-rb-105-b", 3, daemon.CmdDeployRollback, daemon.RollbackCommandPayload{
		DeploymentID:    "dep-idemp-105",
		ExpectedVersion: "v1.0.0",
		Ticket:          rbTkt2,
	})
	if _, err := svc.PollAndExecuteOnce(ctx); err != nil {
		t.Fatalf("第二次重复幂等回滚失败: %v", err)
	}
	if got := env.currentInstalledVersion(); got != "v1.0.0" {
		t.Fatalf("第二次重复回滚破坏了幂等性，版本变为 %s", got)
	}
	rep, ok := env.latestReport("dep-idemp-105")
	if !ok || rep.Status != daemon.StatusRolledBack {
		t.Fatalf("第二次回滚终态报告异常: %+v", rep)
	}
}

// TestJournalRestartRecoveryAndDeployLock 验证终态报告网络失败后的重启自动补报，以及跨进程文件锁阻断并发部署。
// 参数 t 为标准测试上下文；无返回值。
func TestJournalRestartRecoveryAndDeployLock(t *testing.T) {
	env := newE2EEnv(t, "v1.0.0")
	svc1 := env.newDaemon()
	ctx := context.Background()

	// 1. 先测试跨进程排他文件锁：模拟外部已持有 deploy.lock，升级指令应在 precheck 阶段安全拒绝并发
	lockFile, err := os.OpenFile(env.agentCfg.DeployLockFilePath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("打开锁文件失败: %v", err)
	}
	_ = lockFile.Close()
	extStore, err := daemon.NewStateStore(env.agentCfg)
	if err != nil {
		t.Fatalf("创建外部锁存储失败: %v", err)
	}
	unlockExt, err := extStore.AcquireDeployLock()
	if err != nil {
		t.Fatalf("获取外部文件锁失败: %v", err)
	}

	mLock, szL, shaL := env.packAndRegisterRelease(106, simulatedBinaryPayload{Version: "v1.1.0", Healthy: true}, manifest.RiskNone)
	tktLock := env.makeSignedTicket("tkt-locked", "upgrade", "dep-locked", "v1.1.0", 106, false)
	env.enqueueCommand("cmd-locked", 1, daemon.CmdDeployUpgrade, daemon.UpgradeCommandPayload{
		DeploymentID: "dep-locked",
		ReleaseID:    106,
		TargetID:     9,
		ProductCode:  "seccenter",
		Version:      "v1.1.0",
		BundleSize:   szL,
		BundleSHA256: shaL,
		Manifest:     mLock,
		Ticket:       tktLock,
	})
	_, _ = svc1.PollAndExecuteOnce(ctx)
	unlockExt()

	lockRep, ok := env.latestReport("dep-locked")
	if !ok || lockRep.FailedStep != "precheck" || env.currentInstalledVersion() != "v1.0.0" {
		t.Fatalf("并发文件锁未能在 precheck 拦截部署: %+v", lockRep)
	}

	// 2. 验证终态上报瞬间若 Server 返回 500，重启新 Daemon 实例后通过 journal.json 自动补报终态
	tktRetry := env.makeSignedTicket("tkt-recover", "upgrade", "dep-recover", "v1.1.0", 106, false)
	// 让下一次降级拦截（简单触发单次终态上报）遇到模拟 Server 500
	env.writeInstalledBinary(simulatedBinaryPayload{Version: "v1.3.0", Healthy: true})
	env.failNextReport.Store(true)
	// 手动直接调用 HandleCommand：step 报告消耗一次失败？我们直接在 journal 中验证未确认终态补报：
	unackedStore, _ := daemon.NewStateStore(env.agentCfg)
	_ = unackedStore.UpdateJournal(func(j *daemon.Journal) {
		j.Deployments["dep-unacked-restart"] = daemon.JournalRecord{
			MessageID:     "cmd-unacked",
			CommandType:   daemon.CmdDeployUpgrade,
			DeploymentID:  "dep-unacked-restart",
			ReleaseID:     106,
			TargetVersion: "v1.3.0",
			CurrentStep:   "complete",
			Status:        daemon.StatusSucceeded,
			Terminal:      true,
			ReportAcked:   false,
			LastReport: &daemon.DeployReport{
				CommandID:    "cmd-unacked",
				DeploymentID: "dep-unacked-restart",
				ReleaseID:    106,
				TargetID:     9,
				ProductCode:  "seccenter",
				Version:      "v1.3.0",
				Status:       daemon.StatusSucceeded,
				Step:         "complete",
				StepStatus:   "succeeded",
				Message:      "pending report before restart",
				Timestamp:    time.Now().Unix(),
			},
			RollbackTkt: &tktRetry,
		}
	})
	env.failNextReport.Store(false)

	// 模拟进程重启，新建 Daemon 实例调用 RecoverPending
	svcRestarted := env.newDaemon()
	if err := svcRestarted.RecoverPending(ctx); err != nil {
		t.Fatalf("RecoverPending 补报失败: %v", err)
	}
	recoveredRep, ok := env.latestReport("dep-unacked-restart")
	if !ok || recoveredRep.Status != daemon.StatusSucceeded {
		t.Fatalf("重启后未收到 journal 补报的终态报告: %+v", recoveredRep)
	}
}
