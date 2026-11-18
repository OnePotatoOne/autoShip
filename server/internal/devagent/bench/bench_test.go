package bench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/engine"
	"autoship/pkg/keychain"
)

// TestRunPhase0BenchmarkEndToEnd 验证阶段 0 验证基准器：沙箱自检 + 看图探测 + 回到前置历史 commit 隔离运行自研引擎并通过独立测试裁判，且原始仓库工作区零改动。
func TestRunPhase0BenchmarkEndToEnd(t *testing.T) {
	ctx := context.Background()
	origRepo := filepath.Join(t.TempDir(), "product-repo")
	if err := os.MkdirAll(origRepo, 0755); err != nil {
		t.Fatal(err)
	}

	// 提交 1（pre-commit）：包含待实现功能占位与失败单元测试
	mustWriteBenchFile(t, filepath.Join(origRepo, "go.mod"), "module benchapp\n\ngo 1.27\n")
	mustWriteBenchFile(t, filepath.Join(origRepo, "greet.go"), "// Package benchapp 示例包。\npackage benchapp\n\n// Greet 返回问候语。\nfunc Greet(name string) string { return \"\" }\n")
	mustWriteBenchFile(t, filepath.Join(origRepo, "greet_test.go"), "package benchapp\n\nimport \"testing\"\n\n// TestGreet 验证问候输出。\nfunc TestGreet(t *testing.T) {\n\tif Greet(\"AutoShip\") != \"Hello, AutoShip\" {\n\t\tt.Fatal(\"mismatch\")\n\t}\n}\n")
	mustBenchGit(t, origRepo, "init", "-b", "main")
	mustBenchGit(t, origRepo, "config", "user.name", "Author")
	mustBenchGit(t, origRepo, "config", "user.email", "author@example.com")
	mustBenchGit(t, origRepo, "add", ".")
	mustBenchGit(t, origRepo, "commit", "-m", "pre-feature commit")
	preCommitSHA := mustBenchGit(t, origRepo, "rev-parse", "HEAD")

	// 提交 2（当前仓库 HEAD）：模拟仓库后续已演进，验证 bench 回退到 preCommitSHA 建 worktree 而不污染 origRepo HEAD！
	mustWriteBenchFile(t, filepath.Join(origRepo, "README.md"), "# Latest Main\n")
	mustBenchGit(t, origRepo, "add", ".")
	mustBenchGit(t, origRepo, "commit", "-m", "later commit on main")
	origMainHead := mustBenchGit(t, origRepo, "rev-parse", "HEAD")

	var mu sync.Mutex
	coderTurn := 0

	mockHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody := make(map[string]any)
		_ = json.NewDecoder(r.Body).Decode(&rawBody)
		rawBytes, _ := json.Marshal(rawBody)
		bodyStr := string(rawBytes)

		w.Header().Set("Content-Type", "application/json")
		// 看图探测请求
		if strings.Contains(bodyStr, "image_url") {
			_, _ = w.Write([]byte(`{"model":"deepseek-v4.1-flash","choices":[{"message":{"role":"assistant","content":"OK"}}],"usage":{"total_tokens":20}}`))
			return
		}
		if strings.Contains(bodyStr, "规格架构师") {
			_, _ = w.Write([]byte(`{"model":"deepseek-v4.1-flash","choices":[{"message":{"role":"assistant","content":"{\"criteria\":[{\"id\":\"AC-1\",\"description\":\"Greet returns Hello, <name>\"}],\"technicalPlan\":\"update Greet\"}"}}],"usage":{"total_tokens":50}}`))
			return
		}
		if strings.Contains(bodyStr, "安全与质量代码评审员") {
			_, _ = w.Write([]byte(`{"model":"deepseek-v4.1-flash","choices":[{"message":{"role":"assistant","content":"{\"approved\":true,\"summary\":\"clean\"}"}}],"usage":{"total_tokens":40}}`))
			return
		}

		mu.Lock()
		coderTurn++
		turn := coderTurn
		mu.Unlock()

		switch turn {
		case 1:
			fixedCode := "// Package benchapp 示例包。\npackage benchapp\n\n// Greet 返回带名字的标准问候字符串。\n// 参数 name 为称呼对象；返回拼接后的问候文本，通过 Greet(name) 调用。\nfunc Greet(name string) string {\n\treturn \"Hello, \" + name\n}\n"
			wArgs, _ := json.Marshal(map[string]string{"path": "greet.go", "content": fixedCode})
			resp := map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "tool_calls": []engine.ToolCall{
						{ID: "t1", Type: "function", Function: engine.ToolCallFunction{Name: "write_file", Arguments: string(wArgs)}},
					}}},
				},
				"usage": map[string]int{"total_tokens": 80},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case 2:
			resp := map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "tool_calls": []engine.ToolCall{
						{ID: "t2", Type: "function", Function: engine.ToolCallFunction{Name: "run_command", Arguments: `{"command":"go test -v ./..."}`}},
					}}},
				},
				"usage": map[string]int{"total_tokens": 60},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			resp := map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "tool_calls": []engine.ToolCall{
						{ID: "t3", Type: "function", Function: engine.ToolCallFunction{Name: "bind_evidence", Arguments: `{"criterionId":"AC-1","runId":"run-1","testName":"TestGreet"}`}},
						{ID: "t4", Type: "function", Function: engine.ToolCallFunction{Name: "finish_coding", Arguments: `{"summary":"done"}`}},
					}}},
				},
				"usage": map[string]int{"total_tokens": 60},
			}
			_ = json.NewEncoder(w).Encode(resp)
		}
	}))
	defer mockHub.Close()

	report, err := RunPhase0Benchmark(ctx, RunOptions{
		RepoPath:    origRepo,
		ProductCode: "benchapp",
		BaseURL:     mockHub.URL,
		ModelName:   "deepseek-v4.1-flash",
		SandboxMode: "off",
		Budget:      config.TaskBudget{MaxTokens: 50000, MaxDurationMinutes: 10, MaxTurns: 20},
		Cases: []CaseSpec{
			{
				CaseID:          "hist-feat-1",
				PreCommit:       preCommitSHA,
				WorkType:        engine.WorkTypeFeature,
				TaskDescription: "实现 Greet(name) 返回 'Hello, ' + name 并通过测试",
				TestCommand:     "go test ./...",
			},
		},
	}, keychain.NewMemoryStore())
	if err != nil {
		t.Fatalf("RunPhase0Benchmark 执行失败: %v", err)
	}
	if !report.AllCasesPass || report.PassedCount != 1 || !report.VisionProbe.Supported {
		t.Fatalf("阶段 0 基准测试预期全部通过，实际报告: %+v", report)
	}

	// 验证输出报告文件生成正常，且用户原始仓库当前分支 HEAD 未被触碰
	outReport := filepath.Join(t.TempDir(), "phase0-report.json")
	if err := SaveReportFiles(outReport, report); err != nil {
		t.Fatalf("保存阶段 0 报告失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(outReport), "phase0-report.md")); err != nil {
		t.Fatalf("未同步生成 Markdown 阶段 0 报告: %v", err)
	}
	afterMainHead := mustBenchGit(t, origRepo, "rev-parse", "HEAD")
	if afterMainHead != origMainHead {
		t.Fatalf("原始仓库 HEAD 被意外改动: %s != %s", afterMainHead, origMainHead)
	}
}

// mustWriteBenchFile 写入基准测试临时文件。
func mustWriteBenchFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// mustBenchGit 执行测试 git 命令。
func mustBenchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}
