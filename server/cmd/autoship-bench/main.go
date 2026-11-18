// Command autoship-bench 是 autoShip 阶段 0 验证基准命令行工具（对应设计文档第 12 节），支持对本地 Git 仓库指定的历史前置提交列表运行自研开发引擎最小循环、macOS 权限档案沙箱可用性自检与模型多模态看图能力探测，并输出结构化 JSON 与 Markdown 验证报告。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"autoship/internal/devagent/bench"
	"autoship/internal/devagent/config"
	"autoship/internal/devagent/engine"
	"autoship/pkg/keychain"
)

// benchVersion 标识阶段 0 基准验证工具版本号。
var benchVersion = "v0.3.0"

// main 解析命令行标志或批量用例文件，执行阶段 0 验证并打印与落盘评估报告。
// 无入参；无返回值（若基准任务存在未通过项或发生致命错误则以非零退出码退出）。
func main() {
	repoPath := flag.String("repo", "", "本地目标产品 Git 仓库路径（在独立临时 worktree 中回放，不修改原目录）")
	productCode := flag.String("product", "bench-product", "产品标识编码")
	commitsCSV := flag.String("commits", "", "逗号分隔的历史前置 Commit SHA 列表（如 sha1,sha2,sha3）")
	taskDesc := flag.String("task", "", "任务描述（多个 commit 可用 '||' 分隔各自描述，或统一共用同一描述）")
	workType := flag.String("type", string(engine.WorkTypeFeature), "任务类型: bug | feature | improvement")
	targetModule := flag.String("module", "", "任务所属模块名")
	testCmd := flag.String("test-cmd", "go test ./...", "担任最终裁判的产品测试命令")
	buildCmd := flag.String("build-cmd", "", "可选产品构建命令")
	casesFile := flag.String("cases-file", "", "可选 JSON 或 YAML 格式的批量历史任务定义文件（优先于 -commits）")
	baseURL := flag.String("base-url", "http://127.0.0.1:8788/v1", "OpenAI 兼容模型接口基地址")
	modelName := flag.String("model", "deepseek-v4.1-flash", "待验证的大模型名称")
	apiKey := flag.String("api-key", "", "可选模型 API Key（为空时自动尝试读取 macOS 钥匙串 llm.local-hub）")
	sandboxMode := flag.String("sandbox", "", "沙箱档位: protected | off（留空时根据沙箱自检自动选择）")
	maxTokens := flag.Int("max-tokens", 500000, "单任务最大累计 Token 预算")
	maxTurns := flag.Int("max-turns", 80, "单任务最大会话轮数预算")
	timeoutMin := flag.Int("timeout-min", 60, "单任务最大耗时分钟数")
	outPath := flag.String("out", "", "可选输出报告文件路径（自动同时输出 .json 与 .md）")
	probeOnly := flag.Bool("probe-only", false, "仅执行 macOS 沙箱自检与模型多模态看图探测，不跑仓库编码任务")
	showVersion := flag.Bool("version", false, "打印版本号并退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("autoship-bench %s\n", benchVersion)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var cases []bench.CaseSpec
	if !*probeOnly {
		parsedCases, err := resolveBenchCases(*casesFile, *commitsCSV, *taskDesc, *workType, *targetModule, *testCmd, *buildCmd)
		if err != nil {
			log.Fatalf("[autoship-bench] 参数错误: %v", err)
		}
		if strings.TrimSpace(*repoPath) == "" {
			log.Fatal("[autoship-bench] 非 -probe-only 模式下必须通过 -repo 指定本地 Git 仓库路径")
		}
		cases = parsedCases
	}

	opts := bench.RunOptions{
		RepoPath:    *repoPath,
		ProductCode: *productCode,
		BaseURL:     *baseURL,
		ModelName:   *modelName,
		APIKey:      *apiKey,
		SandboxMode: *sandboxMode,
		Budget: config.TaskBudget{
			MaxTokens:          *maxTokens,
			MaxDurationMinutes: *timeoutMin,
			MaxTurns:           *maxTurns,
		},
		Cases: cases,
	}

	report, err := bench.RunPhase0Benchmark(ctx, opts, keychain.NewDefaultStore())
	if err != nil {
		log.Fatalf("[autoship-bench] 运行基准验证失败: %v", err)
	}

	fmt.Println(report.FormatMarkdown())
	if strings.TrimSpace(*outPath) != "" {
		if err := bench.SaveReportFiles(*outPath, report); err != nil {
			log.Fatalf("[autoship-bench] 写入报告文件失败: %v", err)
		}
	}

	if !report.AllCasesPass {
		os.Exit(2)
	}
}

// resolveBenchCases 从 JSON/YAML 用例文件或命令行 `-commits` + `-task` 参数构造阶段 0 待执行用例列表。
// 参数 casesFile 为用例文件路径，commitsCSV/taskRaw/workType/module/testCmd/buildCmd 为命令行标志；返回 []bench.CaseSpec 及校验错误。
func resolveBenchCases(
	casesFile, commitsCSV, taskRaw, workType, module, testCmd, buildCmd string,
) ([]bench.CaseSpec, error) {
	if strings.TrimSpace(casesFile) != "" {
		cleanFile := config.ExpandPath(casesFile)
		data, err := os.ReadFile(cleanFile)
		if err != nil {
			return nil, fmt.Errorf("读取用例文件 %s 失败: %w", cleanFile, err)
		}
		var list []bench.CaseSpec
		ext := strings.ToLower(filepath.Ext(cleanFile))
		if ext == ".yaml" || ext == ".yml" {
			if err := yaml.Unmarshal(data, &list); err != nil {
				return nil, fmt.Errorf("解析 YAML 用例文件失败: %w", err)
			}
		} else {
			if err := json.Unmarshal(data, &list); err != nil {
				return nil, fmt.Errorf("解析 JSON 用例文件失败: %w", err)
			}
		}
		if len(list) == 0 {
			return nil, errors.New("用例文件中未包含任何 CaseSpec")
		}
		return list, nil
	}

	var commits []string
	for _, item := range strings.Split(commitsCSV, ",") {
		if c := strings.TrimSpace(item); c != "" {
			commits = append(commits, c)
		}
	}
	if len(commits) == 0 {
		return nil, errors.New("请通过 -commits 指定至少一个历史前置提交，或通过 -cases-file 提供批量任务定义")
	}
	if strings.TrimSpace(taskRaw) == "" {
		return nil, errors.New("请通过 -task 指定任务描述与验收要求")
	}

	tasks := strings.Split(taskRaw, "||")
	out := make([]bench.CaseSpec, 0, len(commits))
	for i, sha := range commits {
		desc := strings.TrimSpace(tasks[0])
		if i < len(tasks) && strings.TrimSpace(tasks[i]) != "" {
			desc = strings.TrimSpace(tasks[i])
		}
		out = append(out, bench.CaseSpec{
			CaseID:          fmt.Sprintf("case-%d", i+1),
			PreCommit:       sha,
			WorkType:        engine.WorkType(strings.TrimSpace(workType)),
			TargetModule:    strings.TrimSpace(module),
			TaskDescription: desc,
			TestCommand:     strings.TrimSpace(testCmd),
			BuildCommand:    strings.TrimSpace(buildCmd),
		})
	}
	return out, nil
}
