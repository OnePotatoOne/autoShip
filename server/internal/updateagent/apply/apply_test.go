// Package apply_test 验证 autoship-apply 的无通配子命令约束、TOCTOU 防护、双公钥验签、环境变量清空、账本持久化及幂等回滚。
package apply_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"autoship/internal/updateagent/apply"
	"autoship/internal/updateagent/config"
	"autoship/pkg/crypto"
	"autoship/pkg/envelope"
	"autoship/pkg/manifest"
)

// applyTestHarness 封装单元测试所需的密钥对、临时目录与 autoship-apply 执行器。
type applyTestHarness struct {
	rootDir        string              // 测试临时根目录
	cfg            *config.ApplyConfig // 测试专用 ApplyConfig
	serverPubB64   string              // 模拟 Server 验签公钥
	serverPrivB64  string              // 模拟 Server 票据签名私钥
	releasePubB64  string              // 模拟发布验签公钥
	releasePrivB64 string              // 模拟发布清单签名私钥
	installedBin   string              // 模拟产品钩子安装业务二进制的目标路径
	hookStateDir   string              // 模拟产品钩子保存备份与环境变量记录的目录
	executor       *apply.Executor     // 被测执行器
	fixedNow       time.Time           // 固定测试时钟
}

// newApplyTestHarness 初始化隔离的 autoship-apply 测试夹具与密钥文件。
// 参数 t 为标准测试上下文；返回构造完毕的 *applyTestHarness。
func newApplyTestHarness(t *testing.T) *applyTestHarness {
	t.Helper()
	root := t.TempDir()
	srvPub, srvPriv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatalf("生成 Server 密钥对失败: %v", err)
	}
	relPub, relPriv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		t.Fatalf("生成 Release 密钥对失败: %v", err)
	}

	etcDir := filepath.Join(root, "etc")
	stagingDir := filepath.Join(root, "staging", "current")
	workDir := filepath.Join(root, "apply-state", "work")
	ledgerPath := filepath.Join(root, "apply-state", "ledger.json")
	hookPath := filepath.Join(root, "libexec", "deploy-hook")
	backupDir := filepath.Join(root, "backups")
	installedBin := filepath.Join(root, "product", "bin", "app")
	hookStateDir := filepath.Join(root, "product", "state")

	for _, d := range []string{etcDir, stagingDir, workDir, filepath.Dir(hookPath), backupDir, filepath.Dir(installedBin), hookStateDir} {
		if mkErr := os.MkdirAll(d, 0755); mkErr != nil {
			t.Fatalf("创建目录失败: %v", mkErr)
		}
	}
	serverPubPath := filepath.Join(etcDir, "server.pub")
	releasePubPath := filepath.Join(etcDir, "release.pub")
	if err := os.WriteFile(serverPubPath, []byte(srvPub+"\n"), 0644); err != nil {
		t.Fatalf("写入 server.pub 失败: %v", err)
	}
	if err := os.WriteFile(releasePubPath, []byte(relPub+"\n"), 0644); err != nil {
		t.Fatalf("写入 release.pub 失败: %v", err)
	}

	cfg := &config.ApplyConfig{
		StagingDir:     stagingDir,
		WorkBaseDir:    workDir,
		LedgerPath:     ledgerPath,
		ServerPubPath:  serverPubPath,
		ReleasePubPath: releasePubPath,
		HookPath:       hookPath,
		BackupBaseDir:  backupDir,
		HookTimeoutSec: 30,
	}
	fixedNow := time.Unix(1795000000, 0)
	execEngine := apply.NewExecutor(cfg)
	execEngine.SetNowFunc(func() time.Time { return fixedNow })

	return &applyTestHarness{
		rootDir:        root,
		cfg:            cfg,
		serverPubB64:   srvPub,
		serverPrivB64:  srvPriv,
		releasePubB64:  relPub,
		releasePrivB64: relPriv,
		installedBin:   installedBin,
		hookStateDir:   hookStateDir,
		executor:       execEngine,
		fixedNow:       fixedNow,
	}
}

// buildSignedBundle 构造包含模拟业务二进制与遵循第 6 节契约部署钩子的签名 tar.gz 制品包并写入暂存目录。
// 参数 t 为测试上下文，version 为语义化版本号；返回整包字节大小与 SHA-256 摘要。
func (h *applyTestHarness) buildSignedBundle(t *testing.T, version string) (int64, string) {
	t.Helper()
	srcDir := t.TempDir()
	binFile := filepath.Join(srcDir, "app")
	if err := os.WriteFile(binFile, []byte("binary-"+version), 0755); err != nil {
		t.Fatalf("创建模拟二进制失败: %v", err)
	}

	hookScript := fmt.Sprintf(`#!/bin/sh
set -eu
STATE_DIR=%q
INSTALL_BIN=%q
env > "${STATE_DIR}/last_env.txt"
sub="${1:-}"
case "$sub" in
  install)
    cand="$2"
    dep="$4"
    if [ ! -f "${STATE_DIR}/${dep}.meta" ]; then
      if [ -f "$INSTALL_BIN" ]; then
        cp "$INSTALL_BIN" "${STATE_DIR}/${dep}.prev"
        echo "has_prev=1" > "${STATE_DIR}/${dep}.meta"
      else
        echo "has_prev=0" > "${STATE_DIR}/${dep}.meta"
      fi
    fi
    cp "$cand" "$INSTALL_BIN"
    ;;
  rollback)
    dep="$3"
    if [ ! -f "${STATE_DIR}/${dep}.meta" ]; then
      echo "missing backup metadata for $dep" >&2
      exit 12
    fi
    if grep -q "has_prev=1" "${STATE_DIR}/${dep}.meta"; then
      cp "${STATE_DIR}/${dep}.prev" "$INSTALL_BIN"
    fi
    ;;
  backup-db)
    dep="$3"
    dir="$5"
    echo "hook chatter on stdout that must not break json"
    printf 'CREATE TABLE t(id INT); -- %%s\n' "$dep" > "${dir}/dump.sql"
    ;;
  logs)
    printf '2026-11-18 INFO service healthy lines=%%s\n' "$2"
    ;;
  cleanup)
    touch "${STATE_DIR}/cleaned"
    ;;
  *)
    echo "unknown hook subcmd: $sub" >&2
    exit 2
    ;;
esac
`, h.hookStateDir, h.installedBin)

	hookFile := filepath.Join(srcDir, "deploy-hook")
	if err := os.WriteFile(hookFile, []byte(hookScript), 0755); err != nil {
		t.Fatalf("创建模拟钩子脚本失败: %v", err)
	}

	m := &manifest.ReleaseManifest{
		ProductCode:     "seccenter",
		Version:         version,
		Tag:             version,
		CommitSHA:       "abcdef1234567890",
		Changelog:       "upgrade to " + version,
		MigrationRisk:   manifest.RiskDataRewrite,
		MinAgentVersion: "v0.1.0",
		CreatedAt:       h.fixedNow.Unix(),
	}
	dstBundle := filepath.Join(h.cfg.StagingDir, apply.StagingBundleFileName)
	sz, sha, err := manifest.PackBundle(dstBundle, m, binFile, hookFile, h.releasePrivB64)
	if err != nil {
		t.Fatalf("打包签名制品失败: %v", err)
	}
	return sz, sha
}

// writeSignedTicket 在暂存目录生成由模拟 Server 私钥签名的 ticket.json。
// 参数 t 为测试上下文，ticketID/action/deploymentID/version 为票据关键字段；无返回值。
func (h *applyTestHarness) writeSignedTicket(t *testing.T, ticketID, action, deploymentID, version string) {
	t.Helper()
	tk := envelope.ActionTicket{
		TicketID:     ticketID,
		Action:       action,
		ProductCode:  "seccenter",
		TargetID:     1,
		DeploymentID: deploymentID,
		ReleaseID:    100,
		Version:      version,
		BackupDB:     true,
		IssuedAt:     h.fixedNow.Unix() - 5,
		ExpiresAt:    h.fixedNow.Unix() + 600,
	}
	if err := tk.Sign(h.serverPrivB64); err != nil {
		t.Fatalf("签名动作票据失败: %v", err)
	}
	raw, _ := json.Marshal(tk)
	if err := os.WriteFile(filepath.Join(h.cfg.StagingDir, apply.StagingTicketFileName), raw, 0644); err != nil {
		t.Fatalf("写入 ticket.json 失败: %v", err)
	}
}

// TestValidateArgsStrictNoWildcard 验证 autoship-apply 拒绝任何多参数、选项标志或未知子命令。
// 参数 t 为标准测试上下文；无返回值。
func TestValidateArgsStrictNoWildcard(t *testing.T) {
	for _, valid := range []string{"upgrade", "rollback", "backup", "logs", "cleanup"} {
		got, err := apply.ValidateArgs([]string{valid})
		if err != nil || got != valid {
			t.Fatalf("合法固定子命令 %s 校验失败: %v", valid, err)
		}
	}
	invalidCases := [][]string{
		{},
		{"upgrade", "--force"},
		{"rollback", "--deployment", "dep-1"},
		{"--help"},
		{"exec"},
		{"upgrade;id"},
	}
	for _, tc := range invalidCases {
		if _, err := apply.ValidateArgs(tc); err == nil {
			t.Fatalf("非法参数列表 %v 未被拒绝", tc)
		}
	}
}

// TestUpgradeBackupRollbackLogsCleanupAndCleanEnv 端到端验证 upgrade、环境变量清空、账本记录、backup JSON 输出、幂等 rollback、logs 与 cleanup。
// 参数 t 为标准测试上下文；无返回值。
func TestUpgradeBackupRollbackLogsCleanupAndCleanEnv(t *testing.T) {
	h := newApplyTestHarness(t)
	if err := os.WriteFile(h.installedBin, []byte("binary-v1.0.0"), 0755); err != nil {
		t.Fatalf("初始化旧版程序失败: %v", err)
	}
	// 设置敏感环境变量以验证调用部署钩子前必须被彻底清空
	t.Setenv("AUTOSHIP_SENSITIVE_LEAK_TEST", "top-secret-should-not-leak")

	// 1. 执行 v1.1.0 升级前备份与升级
	h.buildSignedBundle(t, "v1.1.0")
	h.writeSignedTicket(t, "tkt-up-1", apply.ActionUpgrade, "dep-101", "v1.1.0")

	var backupOut, backupErr bytes.Buffer
	if err := h.executor.Run(context.Background(), []string{"backup"}, &backupOut, &backupErr); err != nil {
		t.Fatalf("首次使用升级票据执行 backup 失败: %v (stderr=%s)", err, backupErr.String())
	}
	var bRes apply.BackupResult
	if err := json.Unmarshal(backupOut.Bytes(), &bRes); err != nil {
		t.Fatalf("解析 backup 输出 JSON 失败（说明钩子回显污染了 stdout）: %v, raw=%q", err, backupOut.String())
	}
	if bRes.TotalBytes <= 0 || len(bRes.Files) != 1 || bRes.Files[0].Name != "dump.sql" || len(bRes.SummarySHA256) != 64 {
		t.Fatalf("备份清单数据异常: %+v", bRes)
	}
	bDirInfo, err := os.Stat(bRes.BackupDir)
	if err != nil || bDirInfo.Mode().Perm() != 0700 {
		t.Fatalf("备份专用目录权限必须为 0700，实际: %v", bDirInfo)
	}

	var upOut, upErr bytes.Buffer
	if err := h.executor.Run(context.Background(), []string{"upgrade"}, &upOut, &upErr); err != nil {
		t.Fatalf("upgrade 执行失败: %v (stderr=%s)", err, upErr.String())
	}
	installedData, _ := os.ReadFile(h.installedBin)
	if string(installedData) != "binary-v1.1.0" {
		t.Fatalf("升级后程序内容错误: %q", string(installedData))
	}
	hookInfo, err := os.Stat(h.cfg.HookPath)
	if err != nil || hookInfo.Mode().Perm() != 0700 {
		t.Fatalf("部署钩子权限必须为 0700，实际: %v", hookInfo)
	}

	// 验证部署钩子收到的环境变量不包含父进程泄露变量，仅保留安全 PATH
	envRaw, err := os.ReadFile(filepath.Join(h.hookStateDir, "last_env.txt"))
	if err != nil {
		t.Fatalf("读取钩子环境快照失败: %v", err)
	}
	if strings.Contains(string(envRaw), "AUTOSHIP_SENSITIVE_LEAK_TEST") {
		t.Fatalf("部署钩子泄露了父进程敏感环境变量: %s", string(envRaw))
	}
	if !strings.Contains(string(envRaw), "PATH="+apply.SafeMinimalPath) {
		t.Fatalf("部署钩子未收到预期最小安全 PATH: %s", string(envRaw))
	}

	// 验证账本 ledger.json 记录了 deployment_id
	ledgerRaw, err := os.ReadFile(h.cfg.LedgerPath)
	if err != nil {
		t.Fatalf("读取 ledger.json 失败: %v", err)
	}
	var ledger apply.Ledger
	if err := json.Unmarshal(ledgerRaw, &ledger); err != nil {
		t.Fatalf("解析 ledger.json 失败: %v", err)
	}
	entry, ok := ledger.Deployments["dep-101"]
	if !ok || entry.Version != "v1.1.0" || entry.Status != "installed" || len(entry.BinarySHA256) != 64 {
		t.Fatalf("账本记录不完整: %+v", entry)
	}

	// 2. 验证 logs 与 cleanup
	var logsOut bytes.Buffer
	if err := h.executor.Run(context.Background(), []string{"logs"}, &logsOut, nil); err != nil {
		t.Fatalf("logs 执行失败: %v", err)
	}
	if !strings.Contains(logsOut.String(), "lines=200") {
		t.Fatalf("logs 未传递固定 200 行参数: %q", logsOut.String())
	}
	if err := h.executor.Run(context.Background(), []string{"cleanup"}, nil, nil); err != nil {
		t.Fatalf("cleanup 执行失败: %v", err)
	}

	// 3. 验证按 deployment_id 重复回滚幂等（连续执行两次 rollback 均保持 v1.0.0）
	h.writeSignedTicket(t, "tkt-rb-1", apply.ActionRollback, "dep-101", "v1.1.0")
	if err := h.executor.Run(context.Background(), []string{"rollback"}, nil, nil); err != nil {
		t.Fatalf("第一次 rollback 失败: %v", err)
	}
	afterRB1, _ := os.ReadFile(h.installedBin)
	if string(afterRB1) != "binary-v1.0.0" {
		t.Fatalf("第一次回滚后版本错误: %q", string(afterRB1))
	}

	h.writeSignedTicket(t, "tkt-rb-2", apply.ActionRollback, "dep-101", "v1.1.0")
	if err := h.executor.Run(context.Background(), []string{"rollback"}, nil, nil); err != nil {
		t.Fatalf("第二次重复幂等 rollback 失败: %v", err)
	}
	afterRB2, _ := os.ReadFile(h.installedBin)
	if string(afterRB2) != "binary-v1.0.0" {
		t.Fatalf("第二次幂等回滚破坏了旧版本: %q", string(afterRB2))
	}
}

// TestSecurityRejections 验证伪造票据、动作错配、符号链接攻击与制品版本偷换均被拦截。
// 参数 t 为标准测试上下文；无返回值。
func TestSecurityRejections(t *testing.T) {
	h := newApplyTestHarness(t)
	h.buildSignedBundle(t, "v1.2.0")

	// 1. backup 票据绝不能用于执行 upgrade
	h.writeSignedTicket(t, "tkt-wrong-action", apply.ActionBackup, "dep-201", "v1.2.0")
	if err := h.executor.Run(context.Background(), []string{"upgrade"}, nil, nil); err == nil {
		t.Fatalf("backup 票据居然通过了 upgrade 校验")
	}

	// 2. 票据声明升级 v1.3.0，但暂存区放了旧版 v1.2.0 合法签名包 -> 必须拦截版本不一致
	h.writeSignedTicket(t, "tkt-ver-mismatch", apply.ActionUpgrade, "dep-202", "v1.3.0")
	if err := h.executor.Run(context.Background(), []string{"upgrade"}, nil, nil); err == nil {
		t.Fatalf("制品包版本与票据目标版本不一致时未被拦截")
	}

	// 3. 暂存区 ticket.json 被替换为符号链接 -> 必须拒绝复制（防符号链接穿越）
	realTicket := filepath.Join(h.cfg.StagingDir, apply.StagingTicketFileName)
	movedTicket := filepath.Join(h.rootDir, "outside-ticket.json")
	_ = os.Rename(realTicket, movedTicket)
	if err := os.Symlink(movedTicket, realTicket); err != nil {
		t.Fatalf("创建测试符号链接失败: %v", err)
	}
	if err := h.executor.Run(context.Background(), []string{"upgrade"}, nil, nil); err == nil {
		t.Fatalf("符号链接 ticket.json 未被拒绝")
	}
}
