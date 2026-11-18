package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"autoship/internal/devagent/config"
)

// TestBuildSBPLModes 验证 off / protected / custom 三档权限档案生成的 SBPL 规则完整性与自定义路径优先级。
func TestBuildSBPLModes(t *testing.T) {
	sbplOff, err := BuildSBPL(ProfileSpec{Mode: ModeOff})
	if err != nil || sbplOff != "" {
		t.Fatalf("期望 ModeOff 返回空 SBPL，实际 sbpl=%q err=%v", sbplOff, err)
	}

	spec := ProfileSpec{
		ProductCode:            "seccenter",
		WorkID:                 "work-101",
		Mode:                   ModeProtected,
		HomeDir:                "/Users/tester",
		AutoshipRootDir:        "/Users/tester/autoship",
		CurrentRepoDir:         "/Users/tester/autoship/repos/seccenter",
		WorktreeDir:            "/Users/tester/autoship/work/seccenter/work-101",
		DepCacheDir:            "/Users/tester/autoship/cache/seccenter",
		TempDir:                "/tmp/autoship-work-101",
		AgentStateDir:          "/Users/tester/autoship/state",
		ProductAllowWritePaths: []string{"/Users/tester/autoship/shared-assets"},
	}
	sbplProt, err := BuildSBPL(spec)
	if err != nil {
		t.Fatalf("生成 protected SBPL 失败: %v", err)
	}
	mustContain := []string{
		"(version 1)",
		"(allow network*)",
		"(deny file-write*)",
		`/Users/tester/.ssh`,
		`/Users/tester/Library/Keychains`,
		`/Users/tester/autoship/repos`,
		`/Users/tester/autoship/work`,
		`/Users/tester/autoship/state`,
		`/Users/tester/autoship/work/seccenter/work-101`,
		`/Users/tester/autoship/repos/seccenter/.git`,
		`/Users/tester/autoship/cache/seccenter`,
	}
	for _, token := range mustContain {
		if !strings.Contains(sbplProt, token) {
			t.Fatalf("protected SBPL 缺少预期片段 %q:\n%s", token, sbplProt)
		}
	}

	// 验证 custom 档位与一键放行路径追加
	policy := &config.SandboxPolicyConfig{Mode: "protected"}
	if err := ApplyQuickAction(policy, "allow_path", "/opt/homebrew/var/cache"); err != nil {
		t.Fatalf("ApplyQuickAction allow_path 失败: %v", err)
	}
	if policy.Mode != "custom" || len(policy.CustomAllowWrite) != 1 {
		t.Fatalf("期望自动切换为 custom 并加入允许路径，实际: %+v", policy)
	}
	spec.Mode = Mode(policy.Mode)
	spec.CustomAllowReadPaths = policy.CustomAllowRead
	spec.CustomAllowWritePaths = policy.CustomAllowWrite
	sbplCustom, err := BuildSBPL(spec)
	if err != nil {
		t.Fatalf("生成 custom SBPL 失败: %v", err)
	}
	if !strings.Contains(sbplCustom, "/opt/homebrew/var/cache") {
		t.Fatalf("custom SBPL 未包含新放行路径:\n%s", sbplCustom)
	}

	if err := ApplyQuickAction(policy, "switch_off", ""); err != nil {
		t.Fatalf("ApplyQuickAction switch_off 失败: %v", err)
	}
	if policy.Mode != "off" {
		t.Fatalf("期望降级后 mode=off，实际 %s", policy.Mode)
	}
}

// TestParseDenialOutput 验证多种子进程沙箱拦截报错文本都能准确提取路径并附带一键处置动作。
func TestParseDenialOutput(t *testing.T) {
	sample := "cat: /Users/user1/.ssh/id_ed25519: Operation not permitted\nEPERM: operation not permitted, open '/opt/secret/token.json'"
	evt := ParseDenialOutput("demo", "w-1", ModeProtected, "cat ~/.ssh/id_ed25519", sample)
	if evt == nil {
		t.Fatal("期望识别出拦截事件，实际返回 nil")
	}
	if len(evt.BlockedPaths) != 2 {
		t.Fatalf("期望提取出 2 条被拦截路径，实际: %+v", evt.BlockedPaths)
	}
	if evt.BlockedPaths[0] != "/Users/user1/.ssh/id_ed25519" || evt.BlockedPaths[1] != "/opt/secret/token.json" {
		t.Fatalf("提取路径不匹配: %+v", evt.BlockedPaths)
	}
	if len(evt.SuggestedActions) != 3 {
		t.Fatalf("期望包含 2 个 allow_path + 1 个 switch_off 快捷动作，实际: %+v", evt.SuggestedActions)
	}
}

// TestDarwinRealSandboxSelfCheckAndCustomAllow 在 macOS 宿主上真实调用 /usr/bin/sandbox-exec 验证读写隔离与一键放行闭环。
func TestDarwinRealSandboxSelfCheckAndCustomAllow(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("仅在 darwin 平台运行真实 sandbox-exec 子进程测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	res, err := SelfCheck(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("沙箱 SelfCheck 执行出错: %v", err)
	}
	if !res.Available {
		t.Fatalf("期望 macOS 沙箱自检可用，实际结果: %+v", res)
	}

	// 进一步测试：原本被拦的外部目录在执行一键放行后立即恢复可写
	root := t.TempDir()
	extraTargetDir := filepath.Join(root, "extra-tool-cache")
	if err := os.MkdirAll(extraTargetDir, 0755); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "worktree")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(extraTargetDir, "marker.txt")

	spec := ProfileSpec{
		ProductCode: "demo",
		WorkID:      "w-2",
		Mode:        ModeProtected,
		WorktreeDir: worktree,
		TempDir:     worktree,
	}
	out1, err1 := runWrapped(ctx, spec, "/usr/bin/touch", targetFile)
	if err1 == nil {
		t.Fatal("保护档下写入未授权外部目录应当失败")
	}
	evt := ParseDenialOutput("demo", "w-2", ModeProtected, "touch "+targetFile, out1)
	if evt == nil || len(evt.BlockedPaths) == 0 {
		t.Fatalf("未解析出被拦截外部路径，输出: %s", out1)
	}

	// 模拟后台管理员点击“一键放行该目录”
	spec.Mode = ModeCustom
	spec.CustomAllowWritePaths = []string{extraTargetDir}
	spec.CustomAllowReadPaths = []string{extraTargetDir}
	if out2, err2 := runWrapped(ctx, spec, "/usr/bin/touch", targetFile); err2 != nil {
		t.Fatalf("一键放行后再次写入应当成功，但失败: %v (output=%s)", err2, out2)
	}
}
