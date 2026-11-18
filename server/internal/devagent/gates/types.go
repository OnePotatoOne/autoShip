// Package gates 实现 autoShip 开发引擎的全部内置自动化门禁检查器（设计文档 8.7 节）：Go/TS 代码注释、数据库表与字段注释、UTF-8 编码、依赖版本与 10MB 体积策略、测试完整性、数据库迁移风险分级与回滚演练、以及基于本地 Chrome CDP 的页面巡检。
package gates

import "autoship/pkg/manifest"

// Status 定义单道自动化门禁的执行结果枚举。
type Status string

const (
	// StatusPassed 表示该项门禁检查完全通过。
	StatusPassed Status = "passed"
	// StatusFailed 表示存在违反开发规范或回归防线的错误，必须打回编码会话修复。
	StatusFailed Status = "failed"
	// StatusAskOwner 表示命中依赖升级、新依赖未满 30 天、体积超 10MB 或破坏性迁移，须暂停并通过飞书上报等待人工决策。
	StatusAskOwner Status = "ask_owner"
	// StatusSkipped 表示当前变更不涉及该检查器适用对象，安全跳过。
	StatusSkipped Status = "skipped"
)

// Violation 描述检查器定位到的单处具体违规位置与修复提示。
type Violation struct {
	Rule    string `json:"rule"`    // 规则标识（如 go.comment.missing、db.column_comment.missing）
	File    string `json:"file"`    // 相对文件路径
	Line    int    `json:"line"`    // 违规所在行号（1-based，无行号时为 0）
	Symbol  string `json:"symbol"`  // 涉及的函数名、类型名、表名、字段名或依赖包名
	Message string `json:"message"` // 人类与大模型可读的中文违规详情
}

// ModifiedTestRecord 记录对既有测试文件或既有测试函数的修改条目及模型给出的理由（供验收包单独列示，8.5 & 8.7 节）。
type ModifiedTestRecord struct {
	FilePath string   `json:"filePath"` // 测试文件相对路径
	Symbols  []string `json:"symbols"`  // 被修改的既有测试函数或用例名列表
	Reason   string   `json:"reason"`   // 模型给出的修改既有测试理由
}

// Result 汇总单道门禁检查器的状态、违规明细与附加元数据。
type Result struct {
	GateName         string               `json:"gateName"`                   // 门禁名称
	Status           Status               `json:"status"`                     // passed / failed / ask_owner / skipped
	Summary          string               `json:"summary"`                    // 简要结论摘要
	Violations       []Violation          `json:"violations,omitempty"`       // 具体违规条目列表
	AskOwnerPrompt   string               `json:"askOwnerPrompt,omitempty"`   // 当 Status=ask_owner 时发送飞书与后台的请示正文
	ModifiedTests    []ModifiedTestRecord `json:"modifiedTests,omitempty"`    // 测试完整性检查识别出的既有测试改动清单
	MigrationRisk    manifest.MigrationRisk `json:"migrationRisk,omitempty"`  // 迁移风险检查得出的风险分级
}
