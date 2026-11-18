// Package config 验证更新 Agent 与 autoship-apply 配置加载、默认值填充、HTTPS 强制约束及 root 权限校验。
package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadAgentConfigAndValidation 验证 AgentConfig 的 YAML 加载、CA 指纹规范化及明文 HTTP 拦截。
// 参数 t 为标准测试上下文；无返回值，通过 go test 运行。
func TestLoadAgentConfigAndValidation(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "agent.yaml")
	fp := strings.Repeat("ab:", 31) + "cd"

	validYAML := `
serverURL: "https://203.0.113.10:8443/"
caFingerprint: "sha256:` + fp + `"
bootstrapToken: "tok-once-123"
productCode: "seccenter"
targetId: 7
healthCheckURL: "http://127.0.0.1:18080/healthz"
`
	if err := os.WriteFile(cfgPath, []byte(validYAML), 0600); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}
	cfg, err := LoadAgentConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadAgentConfig 意外失败: %v", err)
	}
	if cfg.ServerURL != "https://203.0.113.10:8443" {
		t.Fatalf("ServerURL 尾部斜杠未清理: %q", cfg.ServerURL)
	}
	if len(cfg.CAFingerprint) != 64 {
		t.Fatalf("CAFingerprint 未规范化为 64 字符: %q", cfg.CAFingerprint)
	}
	if !cfg.UseSudo || cfg.HeartbeatIntervalSec != 30 {
		t.Fatalf("默认值补齐异常: UseSudo=%v, Heartbeat=%d", cfg.UseSudo, cfg.HeartbeatIntervalSec)
	}

	// 验证明文 HTTP 地址必须被拒绝
	httpCfg := *cfg
	httpCfg.ServerURL = "http://203.0.113.10:8443"
	if err := httpCfg.Validate(); err == nil {
		t.Fatalf("明文 HTTP serverURL 未被拦截")
	}
}

// TestLoadApplyConfigSecurity 验证 ApplyConfig 在 root 模式下忽略环境变量、拒绝过宽权限和非 root 属主文件。
// 参数 t 为标准测试上下文；无返回值，通过 go test 运行。
func TestLoadApplyConfigSecurity(t *testing.T) {
	dir := t.TempDir()
	overridePath := filepath.Join(dir, "apply-override.yaml")
	customWork := filepath.Join(dir, "work")
	content := "workBaseDir: " + customWork + "\n"
	if err := os.WriteFile(overridePath, []byte(content), 0600); err != nil {
		t.Fatalf("写入覆盖配置失败: %v", err)
	}

	// 非 root 测试态（euid=1000）允许读取测试覆盖路径
	cfg, err := loadApplyConfigWithEUID(1000, filepath.Join(dir, "nonexistent.yaml"), overridePath)
	if err != nil {
		t.Fatalf("非 root 测试态加载配置失败: %v", err)
	}
	if cfg.WorkBaseDir != customWork {
		t.Fatalf("期望 WorkBaseDir=%s，实际=%s", customWork, cfg.WorkBaseDir)
	}

	// root 态（euid=0）必须完全忽略环境变量覆盖路径，回退内置默认配置
	rootCfg, err := loadApplyConfigWithEUID(0, filepath.Join(dir, "nonexistent-prod.yaml"), overridePath)
	if err != nil {
		t.Fatalf("root 态回退默认配置失败: %v", err)
	}
	if rootCfg.WorkBaseDir != "/var/lib/autoship-apply/work" {
		t.Fatalf("root 态未忽略环境变量覆盖，实际 WorkBaseDir=%s", rootCfg.WorkBaseDir)
	}

	// 权限过宽（0666）的配置文件必须被拒绝
	worldWritable := filepath.Join(dir, "writable.yaml")
	if err := os.WriteFile(worldWritable, []byte(content), 0666); err != nil {
		t.Fatalf("创建测试文件失败: %v", err)
	}
	_ = os.Chmod(worldWritable, 0666)
	if _, err := loadApplyConfigWithEUID(1000, worldWritable, ""); err == nil {
		t.Fatalf("组/全局可写的 apply 配置文件未被拦截")
	}

	// workBaseDir 嵌套在 stagingDir 内时必须被拒绝
	bad := DefaultApplyConfig()
	bad.StagingDir = filepath.Join(dir, "staging")
	bad.WorkBaseDir = filepath.Join(dir, "staging", "work")
	if err := bad.Validate(); err == nil {
		t.Fatalf("workBaseDir 位于 stagingDir 内部时未被拦截")
	}
}

// TestExampleConfigAndDeployScripts 校验仓库提供的 update-agent.example.yaml 可直接通过配置加载校验，且安装/钩子脚本无语法与通配 sudo 违规。
// 参数 t 为标准测试上下文；无返回值，通过 go test 运行。
func TestExampleConfigAndDeployScripts(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	exampleYAML := filepath.Join(repoRoot, "configs", "update-agent.example.yaml")
	cfg, err := LoadAgentConfig(exampleYAML)
	if err != nil {
		t.Fatalf("加载 configs/update-agent.example.yaml 失败: %v", err)
	}
	if cfg.ProductCode != "seccenter" || !cfg.UseSudo {
		t.Fatalf("示例配置解析结果不符合预期: %+v", cfg)
	}

	installScript := filepath.Join(repoRoot, "deploy", "install-update-agent.sh")
	hookScript := filepath.Join(repoRoot, "deploy", "hook-example.sh")
	for _, script := range []string{installScript, hookScript} {
		cmd := exec.Command("bash", "-n", script)
		if out, synErr := cmd.CombinedOutput(); synErr != nil {
			t.Fatalf("脚本 %s 存在 bash 语法错误: %v (%s)", script, synErr, string(out))
		}
	}

	// 确保安装脚本中的 sudoers 规则绝不包含通配符 *
	installContent, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatalf("读取安装脚本失败: %v", err)
	}
	for _, line := range strings.Split(string(installContent), "\n") {
		if strings.Contains(line, "NOPASSWD:") && strings.Contains(line, "*") {
			t.Fatalf("检测到 sudoers 规则包含禁止的通配符 *: %s", line)
		}
	}
}

