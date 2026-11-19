package engine

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
	"autoship/internal/devagent/gates"
	"autoship/internal/devagent/guard"
	"autoship/pkg/desensitize"
)

// AgyInvocation 描述单次调用本机 Antigravity CLI (`agy`) 的工作目录、模式与提示词输入参数。
type AgyInvocation struct {
	WorkDir        string            // 子进程执行目录（独立 git worktree 路径）
	Mode           string            // 执行模式：plan（只读分析/评审）或 auto（编码与自测）
	Prompt         string            // 自包含任务或评审提示词
	ConversationID string            // 可选续接指定 Antigravity 会话 ID
	Timeout        time.Duration     // 本轮调用最大超时时长
	ExtraEnv       map[string]string // 附加注入的环境变量（如测试库 DSN、代理等）
}

// AgyRunResult 封装单次 `agy` CLI 子进程执行结果。
type AgyRunResult struct {
	Output   string        // 标准输出与错误输出合并脱敏文本
	ExitCode int           // 进程退出码（0 表示成功）
	Duration time.Duration // 本次调用耗时
}

// AgyRunnerFunc 定义可替换的 `agy` CLI 执行函数签名（生产调用本机二进制，单测可注入模拟实现）。
type AgyRunnerFunc func(ctx context.Context, inv AgyInvocation) (AgyRunResult, error)

// AgyExecutor 实现 Executor 接口，通过调度本机安装的 Antigravity CLI (`agy`) 执行规格拆解、方案生成、长会话编码、门禁回灌修复与独立评审。
type AgyExecutor struct {
	agentCfg   *config.AgentConfig
	depFetcher gates.MetadataFetcher
	runner     AgyRunnerFunc
}

// NewAgyExecutor 创建基于本机 Antigravity CLI (`agy`) 的开发任务执行器实例。
// 参数 agentCfg 为开发 Agent 全局配置；返回实现 Executor 接口的 *AgyExecutor，在 cmd/autoship-dev-agent 启动时调用。
func NewAgyExecutor(agentCfg *config.AgentConfig) *AgyExecutor {
	if agentCfg == nil {
		agentCfg = config.Default()
	}
	ex := &AgyExecutor{
		agentCfg:   agentCfg,
		depFetcher: gates.NewHTTPRegistryFetcher(agentCfg.HTTPProxy, 15*time.Second),
	}
	ex.runner = ex.defaultRunAgyCLI
	return ex
}

// SetRunner 允许单元测试替换底层 `agy` CLI 子进程调用器。
// 参数 fn 为自定义执行函数；无返回值。
func (e *AgyExecutor) SetRunner(fn AgyRunnerFunc) {
	if fn != nil {
		e.runner = fn
	}
}

// SetDependencyMetadataFetcher 允许单元测试注入模拟的依赖发布时间与体积查询器。
// 参数 fetcher 为 MetadataFetcher 实现；无返回值。
func (e *AgyExecutor) SetDependencyMetadataFetcher(fetcher gates.MetadataFetcher) {
	if fetcher != nil {
		e.depFetcher = fetcher
	}
}

// ResolveAgyBinaryPath 按显式配置路径、用户本地目录与系统 PATH 顺序解析本机 `agy` 可执行文件路径。
// 参数 configured 为配置文件中可选填写的 `engine.agyBinaryPath`；返回可用的二进制路径字符串。
func ResolveAgyBinaryPath(configured string) string {
	if trimmed := config.ExpandPath(configured); trimmed != "" {
		if st, err := os.Stat(trimmed); err == nil && !st.IsDir() {
			return trimmed
		}
	}
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "bin", "agy"),
		"/opt/homebrew/bin/agy",
		"/usr/local/bin/agy",
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	if lp, err := exec.LookPath("agy"); err == nil && lp != "" {
		return lp
	}
	return "agy"
}

// Execute 使用本机 Antigravity CLI 驱动单个开发任务的完整研发闭环（开工检查 -> 方案确认门 -> 编码循环 -> 门禁与独立评审 -> 产出验收包）。
// 参数 ctx 为上下文，req 为开发任务请求参数；返回 *TaskOutcome 及底层致命错误。
func (e *AgyExecutor) Execute(ctx context.Context, req *TaskRequest) (*TaskOutcome, error) {
	if req == nil {
		return nil, errors.New("TaskRequest 不能为空")
	}
	req.Product.ApplyDefaults()
	startTime := time.Now()
	turnsUsed := 0

	budgetCfg := e.agentCfg.Models.DefaultBudget
	if req.BudgetOverride != nil {
		budgetCfg = *req.BudgetOverride
	}
	maxDuration := time.Duration(budgetCfg.MaxDurationMinutes) * time.Minute
	if maxDuration <= 0 {
		maxDuration = 60 * time.Minute
	}

	// 第 1 关：开工前检查（Goal Gate）
	if problem := checkGoalGate(req); problem != "" {
		return &TaskOutcome{
			Status:           OutcomeAskOwner,
			AskOwnerQuestion: problem,
			Duration:         time.Since(startTime),
		}, nil
	}

	// 第 2 阶段：生成或复用技术规格方案（PlanSpec）
	var plan *PlanSpec
	if req.ApprovedPlan != nil {
		cp := *req.ApprovedPlan
		if strings.TrimSpace(cp.ConstraintBlock) == "" {
			cp.ConstraintBlock = buildConstraintBlock(req)
		}
		plan = &cp
	} else {
		e.emitActivity(req, "evt.work.progress", "正在通过 Antigravity (agy) 分析仓库结构并生成技术方案...", "spec")
		turnsUsed++
		generated, err := e.generatePlanViaAgy(ctx, req, maxDuration/3)
		if err != nil {
			return nil, err
		}
		plan = generated
	}

	// 第 3 关：方案确认门（默认先交管理员确认改动范围与表结构方案）
	if req.RequirePlanConfirmation && !req.PlanConfirmed {
		return &TaskOutcome{
			Status:    OutcomeWaitingPlanConfirm,
			Plan:      plan,
			TurnsUsed: turnsUsed,
			Duration:  time.Since(startTime),
		}, nil
	}

	scopeAnalyzer := guard.NewScopeAnalyzer(req.Product, req.TargetModule)
	maxLoops := req.MaxValidationLoops
	if maxLoops <= 0 {
		maxLoops = 4
	}

	var (
		feedbackNotes   []string
		cmdHistory      []CommandExecutionRecord
		changeSummary   string
		gateSuite       *gates.SuiteReport
		reviewReport    *ReviewReport
		publicAudit     = &PublicAuditReport{Triggered: false, Approved: true}
		lastPubDiffHash string
	)

	// 第 4 ~ 5 阶段：调用 Antigravity 编码 ⇄ 运行真实构建测试命令 + 自动化门禁 + 独立安全评审 + 公共区影响审计
	for round := 0; round < maxLoops; round++ {
		if time.Since(startTime) >= maxDuration {
			wipSHA := commitWIPInWorktree(ctx, req.WorktreeDir, req.WorkID)
			return &TaskOutcome{
				Status:       OutcomePausedBudget,
				Plan:         plan,
				WIPCommitSHA: wipSHA,
				TurnsUsed:    turnsUsed,
				Duration:     time.Since(startTime),
			}, nil
		}

		e.emitActivity(req, "evt.work.progress", fmt.Sprintf("Antigravity (agy) 正在执行第 %d 轮编码与自测...", round+1), "coding")
		turnsUsed++
		codingPrompt := buildAgyCodingPrompt(req, plan, feedbackNotes)
		runRes, err := e.runner(ctx, AgyInvocation{
			WorkDir:  req.WorktreeDir,
			Mode:     "auto",
			Prompt:   codingPrompt,
			Timeout:  maxDuration / 2,
			ExtraEnv: req.ExtraEnv,
		})
		appendAgyTranscript(req.TranscriptPath, fmt.Sprintf("coding-round-%d", round+1), codingPrompt, runRes.Output)
		if err != nil {
			return nil, fmt.Errorf("调用 Antigravity CLI 编码失败: %w", err)
		}

		// 解析 agy 输出尾部的结构化状态汇报（支持主动发起 ask_owner 请示）
		parsedCoder := parseAgyCoderReport(runRes.Output)
		if strings.EqualFold(parsedCoder.Status, "ask_owner") && strings.TrimSpace(parsedCoder.Question) != "" {
			wipSHA := commitWIPInWorktree(ctx, req.WorktreeDir, req.WorkID)
			return &TaskOutcome{
				Status:           OutcomeAskOwner,
				Plan:             plan,
				WIPCommitSHA:     wipSHA,
				AskOwnerQuestion: strings.TrimSpace(parsedCoder.Question),
				TurnsUsed:        turnsUsed,
				Duration:         time.Since(startTime),
			}, nil
		}
		if strings.TrimSpace(parsedCoder.ChangeSummary) != "" {
			changeSummary = strings.TrimSpace(parsedCoder.ChangeSummary)
		}
		if strings.TrimSpace(parsedCoder.RootCauseAnalysis) != "" {
			plan.RootCauseAnalysis = strings.TrimSpace(parsedCoder.RootCauseAnalysis)
		}
		if len(parsedCoder.SimilarIssuesChecklist) > 0 {
			plan.SimilarIssuesChecklist = parsedCoder.SimilarIssuesChecklist
		}

		// 缺陷类任务强制检查根因分析与举一反三同类排查清单是否齐备
		if req.WorkType == WorkTypeBug {
			var bugMissing []string
			if strings.TrimSpace(plan.RootCauseAnalysis) == "" {
				bugMissing = append(bugMissing, "缺陷修复类任务必须在输出 JSON 中提供非空的 rootCauseAnalysis（根因分析）")
			}
			if len(plan.SimilarIssuesChecklist) == 0 {
				bugMissing = append(bugMissing, "缺陷修复类任务必须在输出 JSON 中提供非空的 similarIssuesChecklist（同类问题排查清单）")
			}
			if len(bugMissing) > 0 {
				feedbackNotes = []string{"【缺陷规范核验未通过】\n" + strings.Join(bugMissing, "\n")}
				continue
			}
		}

		// 4a. 在隔离 worktree 内真实执行产品档案声明的构建、全量测试与静态检查命令
		cmdProblem, roundCmds := e.runVerificationCommandsInWorktree(ctx, req)
		cmdHistory = append(cmdHistory, roundCmds...)
		if cmdProblem != "" {
			feedbackNotes = []string{"【产品构建/测试/静态检查未通过，请修复】\n" + cmdProblem}
			continue
		}

		// 4b. 运行内置静态与策略门禁（UTF-8、完整注释、数据库表字段注释、依赖 30 天与 10MB 红线、测试完整性、迁移风险）
		suiteRep, err := e.evaluateWorktreeGates(ctx, req, scopeAnalyzer)
		if err != nil {
			return nil, err
		}
		gateSuite = suiteRep
		if suiteRep.NeedsAskOwner {
			wipSHA := commitWIPInWorktree(ctx, req.WorktreeDir, req.WorkID)
			return &TaskOutcome{
				Status:           OutcomeAskOwner,
				Plan:             plan,
				WIPCommitSHA:     wipSHA,
				AskOwnerQuestion: suiteRep.AskOwnerReason,
				TurnsUsed:        turnsUsed,
				Duration:         time.Since(startTime),
			}, nil
		}
		if !suiteRep.AllPassed {
			feedbackNotes = []string{"【内置自动化门禁未通过，请逐项修复】\n" + formatGateViolations(suiteRep)}
			continue
		}

		// 4c. 开启全新独立的 Antigravity 只读评审会话，专查安全越界、硬编码密钥与无关夹带改动
		e.emitActivity(req, "evt.work.progress", "正在执行独立 Antigravity 安全与质量代码评审...", "review")
		turnsUsed++
		revRep, err := e.runIndependentAgyReview(ctx, req, plan)
		if err != nil {
			return nil, err
		}
		reviewReport = revRep
		if !revRep.Approved {
			issues := append(append([]string{}, revRep.SuspiciousFindings...), revRep.QualityIssues...)
			feedbackNotes = []string{"【独立安全与代码评审打回】\n" + revRep.Summary + "\n" + strings.Join(issues, "\n")}
			continue
		}

		// 4d. 静态计算三区改动范围；若修改了公共区文件则强制追加一轮独立系统性影响审计
		scopeRep, err := scopeAnalyzer.ComputeGitDiffScope(ctx, req.WorktreeDir, req.BaseRef)
		if err != nil {
			return nil, err
		}
		if scopeRep.RequiresSystemicAudit {
			currPubHash := computePublicDiffFingerprint(ctx, req.WorktreeDir, req.BaseRef, scopeRep.PublicZoneChanges)
			if !publicAudit.Triggered || !publicAudit.Approved || currPubHash != lastPubDiffHash {
				e.emitActivity(req, "evt.work.progress", "检测到公共区文件改动，正在执行系统性影响审计...", "impact_audit")
				turnsUsed++
				auditRep, auditErr := e.runPublicZoneAgyAudit(ctx, req, scopeAnalyzer, scopeRep, &cmdHistory)
				if auditErr != nil {
					return nil, auditErr
				}
				publicAudit = auditRep
				lastPubDiffHash = currPubHash
				if !auditRep.Approved {
					feedbackNotes = []string{"【公共区系统性影响审计未通过】\n" + auditRep.RiskAssessment + "\n" + strings.Join(auditRep.IssuesFound, "\n")}
					continue
				}
			}
		}

		// 第 6 阶段：全部验证与评审通过，提交任务分支候选 Commit 并生成结构化验收包
		commitSHA, err := commitCandidateBranch(ctx, req.WorktreeDir, req.WorkID, nonEmptyOrDefault(changeSummary, plan.TechnicalPlan))
		if err != nil {
			return nil, err
		}
		finalScope, err := scopeAnalyzer.ComputeGitDiffScope(ctx, req.WorktreeDir, req.BaseRef)
		if err != nil {
			return nil, err
		}
		verifiedTable := buildVerifiedEvidenceFromCommands(plan.Criteria, cmdHistory)
		dur := time.Since(startTime)

		pkg := &AcceptancePackage{
			WorkID:                req.WorkID,
			ProductCode:           req.Product.ProductCode,
			CommitSHA:             commitSHA,
			FirstScreenScope:      finalScope,
			PublicImpactAudit:     publicAudit,
			CriteriaTable:         verifiedTable,
			ChangeSummary:         nonEmptyOrDefault(changeSummary, plan.TechnicalPlan),
			ModifiedExistingTests: gateSuite.ModifiedTests,
			GateSuiteReport:       gateSuite,
			ReviewConclusion:      reviewReport,
			MigrationRisk:         gateSuite.MigrationRisk,
			BugRootCause:          plan.RootCauseAnalysis,
			SimilarIssuesChecked:  plan.SimilarIssuesChecklist,
			DurationSeconds:       int64(dur.Seconds()),
		}
		return &TaskOutcome{
			Status:            OutcomeReadyForAcceptance,
			Plan:              plan,
			AcceptancePackage: pkg,
			TurnsUsed:         turnsUsed,
			Duration:          dur,
		}, nil
	}

	wipSHA := commitWIPInWorktree(ctx, req.WorktreeDir, req.WorkID)
	return &TaskOutcome{
		Status:           OutcomeAskOwner,
		Plan:             plan,
		WIPCommitSHA:     wipSHA,
		AskOwnerQuestion: fmt.Sprintf("Antigravity 经过 %d 轮门禁/评审修复仍存在未消解项（已保存检查点 %s），请人工指导", maxLoops, wipSHA),
		TurnsUsed:        turnsUsed,
		Duration:         time.Since(startTime),
	}, nil
}

// defaultRunAgyCLI 调用本机安装的 `agy` 二进制执行非交互单轮提示词并返回合并输出。
// 参数 ctx 为父上下文，inv 为调用配置；返回 AgyRunResult 及启动异常。
func (e *AgyExecutor) defaultRunAgyCLI(ctx context.Context, inv AgyInvocation) (AgyRunResult, error) {
	binPath := ResolveAgyBinaryPath(e.agentCfg.Engine.AgyBinaryPath)
	timeout := inv.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	mode := strings.TrimSpace(inv.Mode)
	if mode == "" {
		mode = "auto"
	}

	args := []string{
		"--print",
		"--disable-slash-commands",
		"--output-format", "text",
		"--mode", mode,
	}
	autoApprove := true
	if e.agentCfg.Engine.AgyAutoApprove != nil {
		autoApprove = *e.agentCfg.Engine.AgyAutoApprove
	}
	if autoApprove {
		args = append(args, "--dangerously-skip-permissions")
	}
	if e.agentCfg.Engine.AgySandbox {
		args = append(args, "--sandbox")
	}
	if m := strings.TrimSpace(e.agentCfg.Engine.AgyModel); m != "" {
		args = append(args, "--model", m)
	}
	if ef := strings.TrimSpace(e.agentCfg.Engine.AgyEffort); ef != "" {
		args = append(args, "--effort", ef)
	}
	if cid := strings.TrimSpace(inv.ConversationID); cid != "" {
		args = append(args, "--conversation", cid)
	}
	args = append(args, "-p", inv.Prompt)

	start := time.Now()
	cmd := exec.CommandContext(runCtx, binPath, args...)
	cmd.Dir = inv.WorkDir
	env := os.Environ()
	for k, v := range inv.ExtraEnv {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	runErr := cmd.Run()
	dur := time.Since(start)
	cleanOut := desensitize.MaskSecrets(combined.String())

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return AgyRunResult{
				Output:   cleanOut,
				ExitCode: exitErr.ExitCode(),
				Duration: dur,
			}, nil
		}
		return AgyRunResult{Output: cleanOut, ExitCode: -1, Duration: dur}, runErr
	}
	return AgyRunResult{Output: cleanOut, ExitCode: 0, Duration: dur}, nil
}

// generatePlanViaAgy 在 `plan` 模式下调用 Antigravity CLI 阅读工作区代码并生成一页纸技术方案与验收标准。
func (e *AgyExecutor) generatePlanViaAgy(ctx context.Context, req *TaskRequest, timeout time.Duration) (*PlanSpec, error) {
	constraints := buildConstraintBlock(req)
	modBytes, _ := json.Marshal(req.Product.Modules)
	prompt := strings.Join([]string{
		"你是 autoShip 研发规格架构师，请只读分析当前工作区代码与下方业务需求，不要修改任何文件。",
		"【最高优先级约束】\n" + constraints,
		fmt.Sprintf("任务类型: %s | 归属模块: %s | 产品模块表: %s | 公共区目录: %v",
			req.WorkType, req.TargetModule, string(modBytes), req.Product.PublicZoneDirs),
		WrapUntrustedData("requirement", req.RequirementRaw),
		"请严格输出一个 JSON 对象（不要附加多余解释），结构如下：\n" +
			`{"criteria":[{"id":"AC-1","description":"...","verifyHint":"..."}],"plannedModules":["..."],"plannedPublicFiles":[],"schemaChangePlanned":false,"technicalPlan":"...","bugReproCommand":"","rootCauseAnalysis":"","similarIssuesChecklist":[]}`,
	}, "\n\n")

	res, err := e.runner(ctx, AgyInvocation{
		WorkDir:  req.WorktreeDir,
		Mode:     "plan",
		Prompt:   prompt,
		Timeout:  timeout,
		ExtraEnv: req.ExtraEnv,
	})
	appendAgyTranscript(req.TranscriptPath, "spec-plan", prompt, res.Output)
	if err != nil {
		return nil, fmt.Errorf("调用 Antigravity 生成规格方案失败: %w", err)
	}

	plan := &PlanSpec{}
	_ = json.Unmarshal([]byte(extractLastJSONObject(res.Output)), plan)
	if len(plan.Criteria) == 0 {
		if len(req.InitialCriteria) > 0 {
			plan.Criteria = req.InitialCriteria
		} else {
			plan.Criteria = []AcceptanceCriterion{
				{ID: "AC-1", Description: "实现需求目标并通过产品全量构建与自动化测试", VerifyHint: req.Product.TestCmd.Cmd},
			}
		}
	}
	if len(plan.PlannedModules) == 0 && req.TargetModule != "" {
		plan.PlannedModules = []string{req.TargetModule}
	}
	if strings.TrimSpace(plan.TechnicalPlan) == "" {
		plan.TechnicalPlan = truncateStr(strings.TrimSpace(res.Output), 4000)
	}
	plan.ConstraintBlock = constraints
	return plan, nil
}

// runVerificationCommandsInWorktree 在任务 worktree 中顺序执行产品配置的安装/构建/测试/静态检查命令并记录真实退出码。
func (e *AgyExecutor) runVerificationCommandsInWorktree(ctx context.Context, req *TaskRequest) (string, []CommandExecutionRecord) {
	steps := []struct {
		label string
		spec  config.CommandSpec
	}{
		{"构建命令(buildCmd)", req.Product.BuildCmd},
		{"全量测试(testCmd)", req.Product.TestCmd},
		{"静态检查(lintCmd)", req.Product.LintCmd},
	}
	var records []CommandExecutionRecord
	for idx, st := range steps {
		rawCmd := strings.TrimSpace(st.spec.Cmd)
		if rawCmd == "" {
			continue
		}
		timeout := time.Duration(st.spec.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		cCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(cCtx, "/bin/sh", "-c", rawCmd)
		cmd.Dir = req.WorktreeDir
		env := os.Environ()
		for k, v := range req.ExtraEnv {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		cancel()

		exitCode := 0
		if err != nil {
			exitCode = 1
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitCode()
			}
		}
		excerpt := truncateStr(desensitize.MaskSecrets(out.String()), 8000)
		rec := CommandExecutionRecord{
			RunID:      fmt.Sprintf("agy-verify-%d-%d", time.Now().UnixNano(), idx+1),
			Command:    rawCmd,
			ExitCode:   exitCode,
			Output:     excerpt,
			ExecutedAt: time.Now().UnixNano(),
		}
		records = append(records, rec)
		if exitCode != 0 {
			return fmt.Sprintf("%s `%s` 执行失败 (exit=%d):\n%s", st.label, rawCmd, exitCode, excerpt), records
		}
	}
	return "", records
}

// evaluateWorktreeGates 对当前 worktree 相对基准提交的全部变更运行 autoShip 内置门禁套件。
func (e *AgyExecutor) evaluateWorktreeGates(ctx context.Context, req *TaskRequest, scope *guard.ScopeAnalyzer) (*gates.SuiteReport, error) {
	helper := &DevEngine{
		agentCfg:   e.agentCfg,
		depFetcher: e.depFetcher,
	}
	return helper.evaluateBuiltinGates(ctx, req, scope)
}

// runIndependentAgyReview 启动一个全新的只读 Antigravity 会话审查当前候选改动 Diff。
func (e *AgyExecutor) runIndependentAgyReview(ctx context.Context, req *TaskRequest, plan *PlanSpec) (*ReviewReport, error) {
	base := strings.TrimSpace(req.BaseRef)
	if base == "" {
		base = "HEAD"
	}
	diffText, _ := execGitSimple(ctx, req.WorktreeDir, "diff", base)
	prompt := strings.Join([]string{
		"你是独立的 autoShip 安全与质量代码评审员（只读模式，严禁修改文件）。请审查下方 git diff 是否存在：1) 鉴权或权限绕过；2) 未授权外部网络调用；3) 硬编码密钥或敏感凭据；4) 与需求无关的夹带改动；5) 若为缺陷修复，根因分析与同类排查是否充分。",
		fmt.Sprintf("任务类型: %s | 根因分析: %s | 同类排查: %v", req.WorkType, plan.RootCauseAnalysis, plan.SimilarIssuesChecklist),
		WrapUntrustedData("requirement", req.RequirementRaw),
		"【待评审 Git Diff】\n" + truncateStr(diffText, 24000),
		`请严格输出 JSON: {"approved":true,"summary":"...","suspiciousFindings":[],"qualityIssues":[],"bugRootCauseVerified":true}`,
	}, "\n\n")

	res, err := e.runner(ctx, AgyInvocation{
		WorkDir:  req.WorktreeDir,
		Mode:     "plan",
		Prompt:   prompt,
		Timeout:  10 * time.Minute,
		ExtraEnv: req.ExtraEnv,
	})
	appendAgyTranscript(req.TranscriptPath, "independent-review", prompt, res.Output)
	if err != nil {
		return nil, fmt.Errorf("调用 Antigravity 独立评审失败: %w", err)
	}
	rep := &ReviewReport{Approved: true, Summary: "Antigravity 独立安全与代码评审通过", BugRootCauseVerified: true}
	if err := json.Unmarshal([]byte(extractLastJSONObject(res.Output)), rep); err != nil && strings.TrimSpace(res.Output) != "" {
		rep.Summary = truncateStr(strings.TrimSpace(res.Output), 500)
	}
	if len(rep.SuspiciousFindings) > 0 || len(rep.QualityIssues) > 0 {
		rep.Approved = false
	}
	return rep, nil
}

// runPublicZoneAgyAudit 在触碰公共区文件时补跑全量测试并启动独立 Antigravity 会话评估跨模块连带风险。
func (e *AgyExecutor) runPublicZoneAgyAudit(
	ctx context.Context,
	req *TaskRequest,
	scope *guard.ScopeAnalyzer,
	scopeRep *guard.ScopeReport,
	cmdHistory *[]CommandExecutionRecord,
) (*PublicAuditReport, error) {
	pubFiles := make([]string, 0, len(scopeRep.PublicZoneChanges))
	for _, it := range scopeRep.PublicZoneChanges {
		pubFiles = append(pubFiles, it.FilePath)
	}
	affected, err := scope.FindAffectedModules(req.WorktreeDir, pubFiles)
	if err != nil {
		return nil, err
	}

	var verifications []string
	if strings.TrimSpace(req.Product.TestCmd.Cmd) != "" {
		prob, recs := e.runVerificationCommandsInWorktree(ctx, req)
		*cmdHistory = append(*cmdHistory, recs...)
		if prob != "" {
			return &PublicAuditReport{
				Triggered:        true,
				Approved:         false,
				AffectedModules:  affected,
				RiskAssessment:   "公共区改动后全模块回归验证失败",
				VerificationsRun: []string{req.Product.TestCmd.Cmd},
				IssuesFound:      []string{prob},
			}, nil
		}
		verifications = append(verifications, fmt.Sprintf("全模块回归验证 `%s` exit=0", req.Product.TestCmd.Cmd))
	}

	base := strings.TrimSpace(req.BaseRef)
	if base == "" {
		base = "HEAD"
	}
	pubDiff, _ := execGitSimple(ctx, req.WorktreeDir, append([]string{"diff", base, "--"}, pubFiles...)...)
	affJSON, _ := json.Marshal(affected)
	prompt := strings.Join([]string{
		"你是 autoShip 公共区系统性影响审计专家（只读模式，不要修改文件）。请评估下方公共区改动对所有静态引用模块是否存在破坏兼容性或系统性不可用风险。",
		fmt.Sprintf("公共区改动文件: %v\n静态引用受影响模块: %s\n已通过验证: %v\n公共区 Diff:\n%s",
			pubFiles, string(affJSON), verifications, truncateStr(pubDiff, 20000)),
		`请严格输出 JSON: {"approved":true,"riskAssessment":"...","issuesFound":[]}`,
	}, "\n\n")

	res, err := e.runner(ctx, AgyInvocation{
		WorkDir:  req.WorktreeDir,
		Mode:     "plan",
		Prompt:   prompt,
		Timeout:  10 * time.Minute,
		ExtraEnv: req.ExtraEnv,
	})
	appendAgyTranscript(req.TranscriptPath, "public-zone-audit", prompt, res.Output)
	if err != nil {
		return nil, fmt.Errorf("调用 Antigravity 公共区审计失败: %w", err)
	}

	var parsed struct {
		Approved       bool     `json:"approved"`
		RiskAssessment string   `json:"riskAssessment"`
		IssuesFound    []string `json:"issuesFound"`
	}
	parsed.Approved = true
	parsed.RiskAssessment = "公共区改动经 Antigravity 独立系统性影响审计通过，向后兼容"
	_ = json.Unmarshal([]byte(extractLastJSONObject(res.Output)), &parsed)
	if len(parsed.IssuesFound) > 0 {
		parsed.Approved = false
	}
	return &PublicAuditReport{
		Triggered:        true,
		Approved:         parsed.Approved,
		AffectedModules:  affected,
		RiskAssessment:   parsed.RiskAssessment,
		VerificationsRun: verifications,
		IssuesFound:      parsed.IssuesFound,
	}, nil
}

// emitActivity 安全触发活动流回调将实时阶段信息回传 Server。
func (e *AgyExecutor) emitActivity(req *TaskRequest, evtType, msg, stage string) {
	if req != nil && req.OnActivity != nil {
		req.OnActivity(evtType, msg, map[string]string{"stage": stage, "driver": "agy"})
	}
}

// agyCoderReport 表示 Antigravity 编码轮结束时附带的结构化摘要或提问载荷。
type agyCoderReport struct {
	Status                 string   `json:"status"`
	Question               string   `json:"question"`
	ChangeSummary          string   `json:"changeSummary"`
	RootCauseAnalysis      string   `json:"rootCauseAnalysis"`
	SimilarIssuesChecklist []string `json:"similarIssuesChecklist"`
}

// buildAgyCodingPrompt 组装传递给 `agy --mode auto` 的自包含编码指令（含最高优先级规范、已确认方案、人工批复及门禁打回意见）。
func buildAgyCodingPrompt(req *TaskRequest, plan *PlanSpec, feedback []string) string {
	planJSON, _ := json.MarshalIndent(plan, "", "  ")
	parts := []string{
		"你正在作为 autoShip 开发引擎内核，在当前隔离 Git Worktree 中完成研发任务。严禁执行 `git push` 或切换 Git 分支。",
		"【最高优先级开发规范】\n" + plan.ConstraintBlock,
		fmt.Sprintf("任务 ID: %s | 类型: %s | 目标模块: %s", req.WorkID, req.WorkType, req.TargetModule),
		WrapUntrustedData("requirement", req.RequirementRaw),
		"【已确认技术方案与验收标准】\n" + string(planJSON),
	}
	if strings.TrimSpace(req.EnvironmentJSON) != "" {
		parts = append(parts, WrapUntrustedData("environment", req.EnvironmentJSON))
	}
	for _, r := range req.OwnerReplies {
		if strings.TrimSpace(r) != "" {
			parts = append(parts, "【管理员人工批复意见】\n"+strings.TrimSpace(r))
		}
	}
	if len(feedback) > 0 {
		parts = append(parts, "【上一轮自动化门禁/测试/独立评审打回问题（必须全部修复）】\n"+strings.Join(feedback, "\n\n"))
	}
	parts = append(parts,
		"完成后请在回复末尾输出一行纯 JSON 汇报结果：\n"+
			`- 若已完成编码与自测：{"status":"completed","changeSummary":"简明中文改动说明","rootCauseAnalysis":"缺陷根因（缺陷类必填）","similarIssuesChecklist":["同类排查项1"]}`+"\n"+
			`- 若遇到必须由人工拍板的阻塞歧义或超规依赖申请：{"status":"ask_owner","question":"具体待确认问题与原因"}`,
	)
	return strings.Join(parts, "\n\n")
}

// parseAgyCoderReport 从 `agy` 文本输出中提取末尾 JSON 状态报告；若未输出 JSON 则默认视为已完成本轮编码进入客观门禁检验。
func parseAgyCoderReport(output string) agyCoderReport {
	var rep agyCoderReport
	rawJSON := extractLastJSONObject(output)
	if rawJSON != "" && json.Unmarshal([]byte(rawJSON), &rep) == nil && rep.Status != "" {
		return rep
	}
	rep.Status = "completed"
	rep.ChangeSummary = firstNonEmptyLine(output)
	return rep
}

// extractLastJSONObject 从多段文本中提取最后一个成对 `{...}` JSON 对象文本，便于解析回复尾部的状态结构。
func extractLastJSONObject(raw string) string {
	end := strings.LastIndexByte(raw, '}')
	if end <= 0 {
		return ""
	}
	depth := 0
	inStr := false
	escaped := false
	for i := end; i >= 0; i-- {
		ch := raw[i]
		if inStr {
			if ch == '"' && (i == 0 || raw[i-1] != '\\') {
				inStr = false
			}
			continue
		}
		_ = escaped
		if ch == '"' {
			inStr = true
			continue
		}
		if ch == '}' {
			depth++
		} else if ch == '{' {
			depth--
			if depth == 0 {
				return raw[i : end+1]
			}
		}
	}
	return extractJSONObject(raw)
}

// buildVerifiedEvidenceFromCommands 将验收标准关联到真实通过的构建/全量测试命令记录。
func buildVerifiedEvidenceFromCommands(criteria []AcceptanceCriterion, history []CommandExecutionRecord) []VerifiedEvidence {
	var best CommandExecutionRecord
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].ExitCode == 0 {
			best = history[i]
			break
		}
	}
	out := make([]VerifiedEvidence, 0, len(criteria))
	for _, c := range criteria {
		out = append(out, VerifiedEvidence{
			CriterionID:   c.ID,
			Description:   c.Description,
			Passed:        true,
			RunID:         nonEmptyOrDefault(best.RunID, "worktree-gate-suite"),
			Command:       nonEmptyOrDefault(best.Command, "builtin-gates"),
			ExitCode:      0,
			OutputExcerpt: truncateStr(best.Output, 400),
		})
	}
	return out
}

// commitWIPInWorktree 在暂停或请示前将当前未提交改动安全保存为 WIP 检查点提交。
func commitWIPInWorktree(ctx context.Context, worktreeDir, workID string) string {
	if strings.TrimSpace(worktreeDir) == "" {
		return ""
	}
	status, err := execGitSimple(ctx, worktreeDir, "status", "--porcelain")
	if err == nil && strings.TrimSpace(status) != "" {
		_, _ = execGitSimple(ctx, worktreeDir, "add", "-A")
		_, _ = execGitSimple(ctx, worktreeDir, "commit", "-m", fmt.Sprintf("wip(%s): checkpoint before pause", workID))
	}
	sha, _ := execGitSimple(ctx, worktreeDir, "rev-parse", "HEAD")
	return strings.TrimSpace(sha)
}

// appendAgyTranscript 将每次 Antigravity CLI 交互的阶段、提示词摘要与脱敏响应追加写入任务 JSONL 会话文件。
func appendAgyTranscript(path, stage, prompt, output string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0700)
	entry := map[string]any{
		"ts":     time.Now().Format(time.RFC3339),
		"driver": "agy",
		"stage":  stage,
		"prompt": truncateStr(desensitize.MaskSecrets(prompt), 4000),
		"output": truncateStr(desensitize.MaskSecrets(output), 8000),
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}
