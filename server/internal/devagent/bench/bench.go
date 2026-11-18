// Package bench 实现阶段 0 验证基准核心编排器（设计文档第 12 节，供 cmd/autoship-bench 调用）：在隔离 worktree 中回到真实历史前置提交运行自研开发引擎最小循环，用产品自身测试做裁判，并合并沙箱可用性自检与模型多模态看图探测结果生成结构化阶段 0 验证报告。
package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/engine"
	"autoship/internal/devagent/sandbox"
	"autoship/internal/devagent/workspace"
	"autoship/pkg/keychain"
)

// CaseSpec 描述阶段 0 验证中单个历史回放任务（如 1 个缺陷、1 个单模块功能、1 个前后端联动任务）。
type CaseSpec struct {
	CaseID          string          `json:"caseId" yaml:"caseId"`                   // 用例编号（如 case-1-bug）
	PreCommit       string          `json:"preCommit" yaml:"preCommit"`             // 回到改动之前的基准 Git Commit SHA 或分支引用
	WorkType        engine.WorkType `json:"workType" yaml:"workType"`               // bug | feature | improvement
	TargetModule    string          `json:"targetModule" yaml:"targetModule"`       // 目标模块名
	TaskDescription string          `json:"taskDescription" yaml:"taskDescription"` // 交给引擎的自然语言任务与验收要求
	TestCommand     string          `json:"testCommand" yaml:"testCommand"`         // 作为最终客观裁判的产品自身测试命令
	BuildCommand    string          `json:"buildCommand" yaml:"buildCommand"`       // 可选构建命令
}

// RunOptions 定义运行一次阶段 0 基准测试的配置参数。
type RunOptions struct {
	RepoPath       string              `json:"repoPath"`       // 本地目标代码仓库路径（绝不直接修改原目录）
	ProductCode    string              `json:"productCode"`    // 产品编码标识（默认 bench-product）
	BaseURL        string              `json:"baseUrl"`        // 模型网关地址（默认 http://127.0.0.1:8788/v1）
	ModelName      string              `json:"modelName"`      // 验证模型名（默认 deepseek-v4.1-flash）
	APIKey         string              `json:"apiKey"`         // 可选模型密钥
	SandboxMode    string              `json:"sandboxMode"`    // 验证时采用的沙箱档位（protected 或 off）
	Budget         config.TaskBudget   `json:"budget"`         // 单任务 Token / 时长 / 轮数预算上限
	Modules        map[string][]string `json:"modules"`        // 可选产品模块划分
	PublicZoneDirs []string            `json:"publicZoneDirs"` // 可选公共区目录列表
	Cases          []CaseSpec          `json:"cases"`          // 待回放验证的历史任务列表
	WorkRootDir    string              `json:"workRootDir"`    // 隔离工作根目录（为空时使用系统临时目录并在结束后清理）
}

// CaseResult 记录单个历史回放用例的通过状态、裁判测试退出码、预算合规性、Token 用量与耗时。
type CaseResult struct {
	CaseID             string               `json:"caseId"`             // 用例编号
	PreCommit          string               `json:"preCommit"`          // 起始提交哈希
	WorkType           engine.WorkType      `json:"workType"`           // 任务类型
	Passed             bool                 `json:"passed"`             // 最终是否达标（引擎未超预算 + 独立裁判测试退出码为 0）
	EngineStatus       engine.OutcomeStatus `json:"engineStatus"`       // 引擎结束状态
	RefereeTestPassed  bool                 `json:"refereeTestPassed"`  // 独立运行产品测试命令是否通过
	RefereeOutput      string               `json:"refereeOutput"`      // 裁判测试输出摘录
	WithinBudget       bool                 `json:"withinBudget"`       // 是否未超出预算上限
	ChannelStable      bool                 `json:"channelStable"`      // 长会话期间模型通道是否稳定无中断错误
	TotalTokens        int                  `json:"totalTokens"`        // 消耗总 Token 数
	TurnsUsed          int                  `json:"turnsUsed"`          // 对话轮数
	DurationSeconds    float64              `json:"durationSeconds"`    // 耗时秒数
	CandidateCommitSHA string               `json:"candidateCommitSha"` // 产出的候选提交哈希
	FailureReason      string               `json:"failureReason"`      // 未通过时的具体原因说明
}

// Phase0Report 汇总阶段 0 完整验证结论（沙箱可用性、模型看图能力、各历史任务指标及整体准入建议，第 12 节）。
type Phase0Report struct {
	GeneratedAt   string                  `json:"generatedAt"`   // 报告生成 RFC3339 时间
	ModelTested   string                  `json:"modelTested"`   // 被测模型名称
	BaseURL       string                  `json:"baseUrl"`       // 模型网关地址
	SandboxCheck  *sandbox.SelfCheckResult `json:"sandboxCheck"` // macOS 权限档案可用性自检结果
	VisionProbe   *engine.VisionProbeResult `json:"visionProbe"` // 模型是否支持多模态看图探测结果
	CaseResults   []CaseResult            `json:"caseResults"`   // 各真实任务回放结果明细
	PassedCount   int                     `json:"passedCount"`   // 通过任务数
	TotalCount    int                     `json:"totalCount"`    // 总任务数
	AllCasesPass  bool                    `json:"allCasesPass"`  // 基准任务是否全部达标
	TotalTokens   int                     `json:"totalTokens"`   // 全部用例总 Token 消耗
	TotalSeconds  float64                 `json:"totalSeconds"`  // 总耗时秒数
	Recommendation string                 `json:"recommendation"` // 阶段 0 数据驱动结论建议
}

// RunPhase0Benchmark 执行阶段 0 全套验证：沙箱自检 → 模型看图探测 → 逐个前置 commit 隔离建树跑开发引擎与独立测试裁判 → 输出结构化报告。
// 参数 ctx 为上下文，opts 为验证选项，secrets 为可选钥匙串；返回 *Phase0Report 及初始化致命错误。
func RunPhase0Benchmark(ctx context.Context, opts RunOptions, secrets keychain.Store) (*Phase0Report, error) {
	if strings.TrimSpace(opts.RepoPath) != "" && len(opts.Cases) == 0 {
		return nil, errors.New("至少需要指定 1 个历史验证任务 CaseSpec")
	}
	if strings.TrimSpace(opts.ProductCode) == "" {
		opts.ProductCode = "bench-product"
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		opts.BaseURL = "http://127.0.0.1:8788/v1"
	}
	if strings.TrimSpace(opts.ModelName) == "" {
		opts.ModelName = "deepseek-v4.1-flash"
	}
	if secrets == nil {
		secrets = keychain.NewDefaultStore()
	}

	cleanupRoot := func() {}
	workRoot := config.ExpandPath(opts.WorkRootDir)
	if workRoot == "" {
		tmp, err := os.MkdirTemp("", "autoship-bench-*")
		if err != nil {
			return nil, err
		}
		workRoot = tmp
		cleanupRoot = func() { _ = os.RemoveAll(tmp) }
	}
	defer cleanupRoot()

	agentCfg := config.Default()
	agentCfg.RootDir = workRoot
	agentCfg.Models.Profiles = []config.ModelProfile{
		{
			Name:            "bench-profile",
			BaseURL:         opts.BaseURL,
			Model:           opts.ModelName,
			KeychainAccount: "llm.local-hub",
			APIKey:          opts.APIKey,
			TimeoutSeconds:  180,
			MaxRetries:      2,
		},
	}
	if opts.Budget.MaxTokens > 0 {
		agentCfg.Models.DefaultBudget.MaxTokens = opts.Budget.MaxTokens
	}
	if opts.Budget.MaxDurationMinutes > 0 {
		agentCfg.Models.DefaultBudget.MaxDurationMinutes = opts.Budget.MaxDurationMinutes
	}
	if opts.Budget.MaxTurns > 0 {
		agentCfg.Models.DefaultBudget.MaxTurns = opts.Budget.MaxTurns
	}

	// 1. 运行 macOS 权限档案沙箱可用性自检
	sbCheck, _ := sandbox.SelfCheck(ctx, filepath.Join(workRoot, "sandbox-selfcheck"))

	// 2. 构造自研开发引擎并运行多模态看图探测
	devEng := engine.NewDevEngine(agentCfg, secrets, nil)
	visionRes := devEng.LLMClient().ProbeVisionCapability(ctx, agentCfg.Models.Profiles)

	report := &Phase0Report{
		GeneratedAt:  time.Now().Format(time.RFC3339),
		ModelTested:  opts.ModelName,
		BaseURL:      opts.BaseURL,
		SandboxCheck: sbCheck,
		VisionProbe:  visionRes,
		TotalCount:   len(opts.Cases),
	}

	if len(opts.Cases) == 0 {
		report.AllCasesPass = true
		report.Recommendation = buildRecommendation(report)
		return report, nil
	}

	wsMgr, err := workspace.NewManager(workRoot)
	if err != nil {
		return nil, err
	}

	effectiveSandboxMode := strings.TrimSpace(opts.SandboxMode)
	if effectiveSandboxMode == "" {
		if sbCheck != nil && sbCheck.Available {
			effectiveSandboxMode = string(sandbox.ModeProtected)
		} else {
			effectiveSandboxMode = string(sandbox.ModeOff)
		}
	}

	// 3. 逐个回放历史任务并在结束后用产品自身测试命令做独立裁判
	for idx, cs := range opts.Cases {
		caseID := strings.TrimSpace(cs.CaseID)
		if caseID == "" {
			caseID = fmt.Sprintf("case-%d", idx+1)
		}
		caseRes := runSingleBenchCase(ctx, wsMgr, devEng, opts, cs, caseID, effectiveSandboxMode, workRoot)
		report.CaseResults = append(report.CaseResults, caseRes)
		report.TotalTokens += caseRes.TotalTokens
		report.TotalSeconds += caseRes.DurationSeconds
		if caseRes.Passed {
			report.PassedCount++
		}
	}

	report.AllCasesPass = report.PassedCount == report.TotalCount
	report.Recommendation = buildRecommendation(report)
	return report, nil
}

// FormatMarkdown 将 Phase0Report 渲染为清晰可读的 Markdown 阶段 0 评估报告。
// 参数 r 为阶段 0 报告指针；返回 UTF-8 Markdown 文本。
func (r *Phase0Report) FormatMarkdown() string {
	var b strings.Builder
	b.WriteString("# autoShip 阶段 0 验证基准报告\n\n")
	b.WriteString(fmt.Sprintf("- 生成时间：`%s`\n", r.GeneratedAt))
	b.WriteString(fmt.Sprintf("- 被测模型：`%s`（通道：`%s`）\n", r.ModelTested, r.BaseURL))
	if r.VisionProbe != nil {
		b.WriteString(fmt.Sprintf("- 多模态看图能力：`%v`（耗时 `%d ms`，说明：%s）\n",
			r.VisionProbe.Supported, r.VisionProbe.LatencyMs, r.VisionProbe.Detail))
	}
	if r.SandboxCheck != nil {
		b.WriteString(fmt.Sprintf("- 权限档案沙箱自检：`%v`（推荐档位：`%s`，详情：%s）\n",
			r.SandboxCheck.Available, r.SandboxCheck.FallbackMode, r.SandboxCheck.Detail))
	}
	b.WriteString(fmt.Sprintf("- 任务总体通过率：`%d / %d` ｜ 累计 Token：`%d` ｜ 总耗时：`%.2f s`\n\n",
		r.PassedCount, r.TotalCount, r.TotalTokens, r.TotalSeconds))

	if len(r.CaseResults) > 0 {
		b.WriteString("## 历史回放任务结果明细\n\n")
		b.WriteString("| 用例 ID | 类型 | 前置 Commit | 最终达标 | 裁判测试 | 未超预算 | 通道稳定 | 轮数 | Token | 耗时(s) |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
		for _, c := range r.CaseResults {
			b.WriteString(fmt.Sprintf("| `%s` | `%s` | `%s` | `%v` | `%v` | `%v` | `%v` | %d | %d | %.2f |\n",
				c.CaseID, c.WorkType, shortSHA(c.PreCommit), c.Passed, c.RefereeTestPassed,
				c.WithinBudget, c.ChannelStable, c.TurnsUsed, c.TotalTokens, c.DurationSeconds))
		}
		b.WriteString("\n")
	}
	b.WriteString("## 评估结论与建议\n\n")
	b.WriteString(r.Recommendation + "\n")
	return b.String()
}

// SaveReportFiles 将阶段 0 验证报告同时保存为 `.json` 与 `.md`（若路径以 `.md` 结尾则同目录旁路输出 `.json`，反之亦然）。
// 参数 outPath 为用户指定的输出路径，r 为报告对象；成功返回 nil，失败返回错误。
func SaveReportFiles(outPath string, r *Phase0Report) error {
	cleaned := config.ExpandPath(outPath)
	if cleaned == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cleaned), 0755); err != nil {
		return err
	}
	jsonBytes, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	mdBytes := []byte(r.FormatMarkdown())

	if strings.HasSuffix(strings.ToLower(cleaned), ".md") {
		if err := os.WriteFile(cleaned, mdBytes, 0644); err != nil {
			return err
		}
		return os.WriteFile(strings.TrimSuffix(cleaned, filepath.Ext(cleaned))+".json", jsonBytes, 0644)
	}
	if err := os.WriteFile(cleaned, jsonBytes, 0644); err != nil {
		return err
	}
	return os.WriteFile(strings.TrimSuffix(cleaned, filepath.Ext(cleaned))+".md", mdBytes, 0644)
}

// runSingleBenchCase 为单个回放任务创建指向 PreCommit 的独立 worktree、运行自研引擎并执行独立测试命令裁判。
func runSingleBenchCase(
	ctx context.Context,
	wsMgr *workspace.Manager,
	devEng *engine.DevEngine,
	opts RunOptions,
	cs CaseSpec,
	caseID, sandboxMode, workRoot string,
) CaseResult {
	start := time.Now()
	wType := cs.WorkType
	if wType == "" {
		wType = engine.WorkTypeFeature
	}
	res := CaseResult{
		CaseID:    caseID,
		PreCommit: cs.PreCommit,
		WorkType:  wType,
	}

	prod := config.ProductProfile{
		ProductCode:    opts.ProductCode,
		RepoURL:        config.ExpandPath(opts.RepoPath),
		BuildCmd:       config.CommandSpec{Cmd: cs.BuildCommand, TimeoutSeconds: 600},
		TestCmd:        config.CommandSpec{Cmd: cs.TestCommand, TimeoutSeconds: 600},
		Modules:        opts.Modules,
		PublicZoneDirs: opts.PublicZoneDirs,
		Sandbox:        config.SandboxPolicyConfig{Mode: sandboxMode},
	}

	sess, err := wsMgr.CreateTaskWorktree(ctx, prod, caseID, string(wType), cs.PreCommit)
	if err != nil {
		res.FailureReason = "创建前置提交 worktree 失败: " + err.Error()
		res.DurationSeconds = time.Since(start).Seconds()
		return res
	}
	defer func() { _ = wsMgr.RemoveTaskWorktree(context.Background(), prod.ProductCode, caseID) }()

	req := &engine.TaskRequest{
		WorkID:                  caseID,
		Product:                 prod,
		WorktreeDir:             sess.WorktreeDir,
		BaseRef:                 sess.BaseCommit,
		TempDir:                 sess.TempDir,
		TranscriptPath:          filepath.Join(workRoot, "transcripts", caseID+".jsonl"),
		WorkType:                wType,
		TargetModule:            cs.TargetModule,
		RequirementRaw:          cs.TaskDescription,
		RequirePlanConfirmation: false, // 阶段 0 全自动基准测试直通编码循环
		PlanConfirmed:           true,
	}

	outcome, execErr := devEng.Execute(ctx, req)
	res.DurationSeconds = time.Since(start).Seconds()
	if execErr != nil {
		res.ChannelStable = false
		res.FailureReason = "长会话模型通道或引擎异常中断: " + execErr.Error()
		return res
	}

	res.ChannelStable = true
	res.EngineStatus = outcome.Status
	res.TotalTokens = outcome.TotalTokens
	res.TurnsUsed = outcome.TurnsUsed
	res.WithinBudget = outcome.Status != engine.OutcomePausedBudget
	if outcome.AcceptancePackage != nil {
		res.CandidateCommitSHA = outcome.AcceptancePackage.CommitSHA
	}

	// 由基准器在工作树上独立再跑一次产品测试命令担任最终客观裁判
	if strings.TrimSpace(cs.TestCommand) != "" {
		refOut, refOK := runIndependentRefereeTest(ctx, sess.WorktreeDir, cs.TestCommand)
		res.RefereeTestPassed = refOK
		res.RefereeOutput = refOut
	} else {
		res.RefereeTestPassed = outcome.Status == engine.OutcomeReadyForAcceptance
	}

	res.Passed = res.ChannelStable && res.WithinBudget && res.RefereeTestPassed && outcome.Status == engine.OutcomeReadyForAcceptance
	if !res.Passed && res.FailureReason == "" {
		res.FailureReason = fmt.Sprintf("engineStatus=%s, withinBudget=%v, refereeTest=%v, ask=%s",
			outcome.Status, res.WithinBudget, res.RefereeTestPassed, outcome.AskOwnerQuestion)
	}
	return res
}

// runIndependentRefereeTest 不经过模型会话直接在工作树执行产品测试命令核对退出码。
func runIndependentRefereeTest(ctx context.Context, dir, testCmd string) (string, bool) {
	tCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(tCtx, "/bin/sh", "-c", testCmd)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	txt := strings.TrimSpace(out.String())
	if len(txt) > 1000 {
		txt = txt[:1000] + "..."
	}
	return txt, err == nil
}

// buildRecommendation 根据实测通过率、沙箱与多模态探测数据生成阶段 0 推进结论。
func buildRecommendation(r *Phase0Report) string {
	var notes []string
	if r.AllCasesPass {
		notes = append(notes, fmt.Sprintf("全部 %d 个真实历史任务均在预算内通过产品自身测试裁判，当前模型 (%s) 与自研单长会话循环满足进入阶段 2 的要求。", r.TotalCount, r.ModelTested))
	} else {
		notes = append(notes, fmt.Sprintf("共有 %d/%d 个历史任务未达标，建议先按失败报告拆小任务粒度或调整提示词/模型通道后再推进阶段 2。", r.TotalCount-r.PassedCount, r.TotalCount))
	}
	if r.VisionProbe != nil && !r.VisionProbe.Supported {
		notes = append(notes, "注意：当前模型通道不支持多模态图片直传，阶段 3 客服附件需走文字描述或切换支持看图的模型。")
	}
	if r.SandboxCheck != nil && !r.SandboxCheck.Available {
		notes = append(notes, "注意：当前环境 macOS sandbox-exec 不可用，产品权限档案将自动降级为 off 档（仅依赖工具守卫）。")
	}
	return strings.Join(notes, " ")
}

// shortSHA 截取 Git Commit SHA 前 8 位便于表格展示。
func shortSHA(sha string) string {
	s := strings.TrimSpace(sha)
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
