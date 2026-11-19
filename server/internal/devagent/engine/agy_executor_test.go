package engine_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/engine"
)

// initGitRepoForAgyTest 初始化一个包含基准提交的临时 Git 仓库供 AgyExecutor 单元测试使用。
// 参数 t 为测试上下文；返回仓库目录与初始基准提交 SHA。
func initGitRepoForAgyTest(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=tester",
			"GIT_AUTHOR_EMAIL=tester@autoship.local",
			"GIT_COMMITTER_NAME=tester",
			"GIT_COMMITTER_EMAIL=tester@autoship.local",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v 失败: %v (%s)", args, err, string(out))
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main")
	initialGo := "// Package demo 提供示例问候能力。\npackage demo\n\n// Greet 返回默认问候语；无入参；返回问候文本，通过 Greet() 调用。\nfunc Greet() string {\n\treturn \"hello\"\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.go"), []byte(initialGo), 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "-A")
	runGit("commit", "-m", "initial commit")
	baseSHA := runGit("rev-parse", "HEAD")
	return dir, baseSHA
}

// TestAgyExecutorPlanConfirmAndFullCodingLoop 验证 AgyExecutor 先停在方案确认门，人工确认后完成编码、门禁与独立评审并产出验收包。
// 参数 t 为 Go 标准测试上下文；无返回值。
func TestAgyExecutorPlanConfirmAndFullCodingLoop(t *testing.T) {
	workDir, baseSHA := initGitRepoForAgyTest(t)

	cfg := config.Default()
	cfg.Engine.Driver = "agy"
	cfg.ApplyDefaults()

	execEngine := engine.NewAgyExecutor(cfg)
	execEngine.SetRunner(func(_ context.Context, inv engine.AgyInvocation) (engine.AgyRunResult, error) {
		if inv.Mode == "plan" && strings.Contains(inv.Prompt, "规格架构师") {
			return engine.AgyRunResult{
				Output: `{"criteria":[{"id":"AC-1","description":"新增 Farewell 函数并通过测试","verifyHint":"go test ./..."}],"plannedModules":["core"],"plannedPublicFiles":[],"schemaChangePlanned":false,"technicalPlan":"在 demo.go 中新增带完整中文注释的 Farewell 函数并补充单测"}`,
			}, nil
		}
		if inv.Mode == "auto" {
			updatedGo := "// Package demo 提供示例问候与告别能力。\npackage demo\n\n// Greet 返回默认问候语；无入参；返回问候文本，通过 Greet() 调用。\nfunc Greet() string {\n\treturn \"hello\"\n}\n\n// Farewell 返回标准告别语；无入参；返回告别字符串，通过 Farewell() 调用。\nfunc Farewell() string {\n\treturn \"bye\"\n}\n"
			testGo := "// Package demo_test 验证问候与告别函数行为。\npackage demo_test\n\nimport (\n\t\"testing\"\n\n\t\"example.com/demo\"\n)\n\n// TestFarewell 验证 Farewell 返回预期告别语；参数 t 为测试上下文；无返回值。\nfunc TestFarewell(t *testing.T) {\n\tif demo.Farewell() != \"bye\" {\n\t\tt.Fatalf(\"unexpected: %s\", demo.Farewell())\n\t}\n}\n"
			_ = os.WriteFile(filepath.Join(inv.WorkDir, "demo.go"), []byte(updatedGo), 0644)
			_ = os.WriteFile(filepath.Join(inv.WorkDir, "demo_test.go"), []byte(testGo), 0644)
			return engine.AgyRunResult{
				Output: "已完成代码修改与单测。\n" + `{"status":"completed","changeSummary":"新增 Farewell 函数及单元测试"}`,
			}, nil
		}
		// 独立评审会话返回通过
		return engine.AgyRunResult{
			Output: `{"approved":true,"summary":"代码与注释规范合规，无安全问题","suspiciousFindings":[],"qualityIssues":[],"bugRootCauseVerified":true}`,
		}, nil
	})

	req := &engine.TaskRequest{
		WorkID:      "work-101",
		WorktreeDir: workDir,
		BaseRef:     baseSHA,
		WorkType:    engine.WorkTypeFeature,
		TargetModule: "core",
		RequirementRaw: "请新增 Farewell() 函数返回 bye，验收标准：Given 调用 Farewell When 执行 go test ./... Then 全部通过。",
		RequirePlanConfirmation: true,
		Product: config.ProductProfile{
			ProductCode: "demo",
			Name:        "示例项目",
			TestCmd:     config.CommandSpec{Cmd: "go test ./...", TimeoutSeconds: 60},
			Modules:     map[string][]string{"core": {"demo.go", "demo_test.go"}},
		},
	}

	// 第 1 步：尚未人工确认方案，必须停在 OutcomeWaitingPlanConfirm
	out1, err := execEngine.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("首次执行生成方案失败: %v", err)
	}
	if out1.Status != engine.OutcomeWaitingPlanConfirm || out1.Plan == nil {
		t.Fatalf("期望停在 waiting_plan_confirmation，实际: %+v", out1)
	}

	// 第 2 步：人工确认方案后恢复执行，完成编码、真实 go test、内置门禁与独立评审
	req.PlanConfirmed = true
	req.ApprovedPlan = out1.Plan
	out2, err := execEngine.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("确认方案后执行编码失败: %v", err)
	}
	if out2.Status != engine.OutcomeReadyForAcceptance || out2.AcceptancePackage == nil {
		t.Fatalf("期望进入 ready_for_acceptance，实际: %+v", out2)
	}
	if out2.AcceptancePackage.CommitSHA == "" || out2.AcceptancePackage.CommitSHA == baseSHA {
		t.Fatalf("期望生成新的任务分支候选 Commit，实际: %s", out2.AcceptancePackage.CommitSHA)
	}
}
