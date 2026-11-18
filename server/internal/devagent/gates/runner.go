package gates

import (
	"context"
	"time"

	"autoship/pkg/manifest"
)

// SuiteInput 汇总执行一整轮自动化静态与策略门禁所需的全部输入材料。
type SuiteInput struct {
	WorktreeDir       string             // 当前工作树绝对路径
	ChangedFiles      []string           // 本次任务新增或修改的相对文件路径列表
	DependencyChanges []DependencyChange // 解析出的 go.mod / package.json 依赖变动项
	DepFetcher        MetadataFetcher    // 依赖发布时间与体积元数据查询器
	TestDiffs         []TestFileDiffInput // 测试文件新旧对比列表
	MigrationDiffText string             // SQL 与模型文件 diff 文本
	Now               time.Time          // 当前校对时刻（留空取 time.Now()）
}

// SuiteReport 汇总单轮全部门禁检查器的执行结果、整体是否通过、是否需要 ask_owner 以及最终迁移风险等级。
type SuiteReport struct {
	AllPassed      bool                   `json:"allPassed"`      // 是否全部通过（无 failed 且无 ask_owner）
	NeedsAskOwner  bool                   `json:"needsAskOwner"`  // 是否存在需要暂停上报飞书由人工决策的门禁
	AskOwnerReason string                 `json:"askOwnerReason"` // 合并后的飞书请示说明
	MigrationRisk  manifest.MigrationRisk `json:"migrationRisk"`  // 本次变更的最高数据库迁移风险等级
	ModifiedTests  []ModifiedTestRecord   `json:"modifiedTests"`  // 单列的既有测试改动清单
	Results        []Result               `json:"results"`        // 各门禁逐项结果
}

// RunStaticAndPolicySuite 顺序运行 UTF-8、Go 注释、TS 注释、数据库表/字段注释、依赖策略、测试完整性及迁移风险分级门禁。
// 参数 ctx 为上下文，in 为门禁套件输入；返回聚合后的 *SuiteReport。
func RunStaticAndPolicySuite(ctx context.Context, in SuiteInput) *SuiteReport {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	results := []Result{
		CheckUTF8Files(in.WorktreeDir, in.ChangedFiles),
		CheckGoComments(in.WorktreeDir, in.ChangedFiles),
		CheckTSComments(in.WorktreeDir, in.ChangedFiles),
		CheckDatabaseComments(in.WorktreeDir, in.ChangedFiles),
		CheckDependencyPolicy(ctx, in.DependencyChanges, in.DepFetcher, now),
	}

	testRes := CheckTestIntegrity(in.TestDiffs)
	results = append(results, testRes)

	migRisk, migReasons := ClassifyMigrationRisk(in.ChangedFiles, in.MigrationDiffText)
	migRes := Result{
		GateName:      "migration_risk",
		Status:        StatusPassed,
		MigrationRisk: migRisk,
		Summary:       "无破坏性表结构迁移（风险等级: " + string(migRisk) + "）",
	}
	if migRisk == manifest.RiskDestructive {
		prompt := "检测到破坏性数据库迁移（" + joinNonEmpty(migReasons, "; ") + "），默认阻断发布，须单独人工审批"
		migRes.Status = StatusAskOwner
		migRes.Summary = prompt
		migRes.AskOwnerPrompt = prompt
		migRes.Violations = []Violation{{Rule: "migration.destructive", Message: prompt}}
	}
	results = append(results, migRes)

	report := &SuiteReport{
		AllPassed:     true,
		MigrationRisk: migRisk,
		ModifiedTests: testRes.ModifiedTests,
		Results:       results,
	}
	var askPrompts []string
	for _, r := range results {
		switch r.Status {
		case StatusFailed:
			report.AllPassed = false
		case StatusAskOwner:
			report.AllPassed = false
			report.NeedsAskOwner = true
			if r.AskOwnerPrompt != "" {
				askPrompts = append(askPrompts, r.AskOwnerPrompt)
			}
		}
	}
	report.AskOwnerReason = joinNonEmpty(askPrompts, "\n")
	return report
}

// joinNonEmpty 以分隔符拼接非空字符串切片。
func joinNonEmpty(items []string, sep string) string {
	var clean []string
	for _, it := range items {
		if it != "" {
			clean = append(clean, it)
		}
	}
	if len(clean) == 0 {
		return ""
	}
	res := clean[0]
	for i := 1; i < len(clean); i++ {
		res += sep + clean[i]
	}
	return res
}
