// Package model 定义开发任务主表、关联需求表、自动化门禁结果表、改动范围三区标记表及预览会话表（4.1、7.6、8.5 ~ 8.9 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(
		&WorkItem{},
		&WorkItemRequirement{},
		&WorkGate{},
		&WorkScopeChange{},
		&PreviewSession{},
	)
}

// WorkItem 表示一次自动研发闭环任务，独立维护状态：preparing(准备中)、awaiting_plan(待确认方案)、coding(开发中)、awaiting_answer(等待答复)、pending_acceptance(待验收)、accepted(已通过)、paused(已暂停)、abandoned(已放弃)。
type WorkItem struct {
	ID                           uint64     `gorm:"primaryKey;autoIncrement;comment:开发任务主键ID" json:"id"`
	ProductID                    uint64     `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	PrimaryModuleCode            string     `gorm:"size:64;not null;default:'';comment:主归属业务模块编码（用于划定本模块区）" json:"primaryModuleCode"`
	ParentReleaseID              uint64     `gorm:"index;not null;default:0;comment:若为发布失败新建的关联修复任务则记录原失败版本ID" json:"parentReleaseId"`
	Title                        string     `gorm:"size:255;not null;comment:开发任务标题" json:"title"`
	Status                       string     `gorm:"size:32;not null;default:'preparing';index;comment:任务状态（preparing/awaiting_plan/coding/awaiting_answer/pending_acceptance/accepted/paused/abandoned）" json:"status"`
	BranchName                   string     `gorm:"size:128;not null;default:'';comment:Git工作分支名（如autoship/12-fix-login）" json:"branchName"`
	CandidateCommitSHA           string     `gorm:"size:64;not null;default:'';comment:当前待验收候选提交完整哈希" json:"candidateCommitSha"`
	AcceptedCommitSHA            string     `gorm:"size:64;not null;default:'';comment:验收通过时锁定的提交哈希（8.10节防篡改绑定）" json:"acceptedCommitSha"`
	PlanConfirmRequired          bool       `gorm:"not null;default:true;comment:是否开启方案确认门（默认true）" json:"planConfirmRequired"`
	PlanDocumentJSON             string     `gorm:"type:mediumtext;comment:一页纸技术方案（拟动模块、公共文件、是否动表结构）JSON" json:"planDocumentJson,omitempty"`
	TodoListJSON                 string     `gorm:"type:text;comment:编码会话内置待办清单状态JSON" json:"todoListJson,omitempty"`
	PendingQuestionJSON          string     `gorm:"type:text;comment:ask_owner等待管理员答复的问题详情JSON" json:"pendingQuestionJson,omitempty"`
	ScopeSummaryJSON             string     `gorm:"type:mediumtext;comment:代码计算的改动范围第一屏汇总（本模块/其他模块数/公共区数）JSON" json:"scopeSummaryJson,omitempty"`
	ImpactAuditJSON              string     `gorm:"type:mediumtext;comment:公共区改动触发的系统性影响审计结论JSON" json:"impactAuditJson,omitempty"`
	AcceptancePkgJSON            string     `gorm:"type:longtext;comment:完整结构化验收包JSON" json:"acceptancePkgJson,omitempty"`
	MigrationRisk                string     `gorm:"size:32;not null;default:'none';comment:数据库迁移风险分级（none/additive/data_rewrite/destructive）" json:"migrationRisk"`
	DestructiveMigrationApproved bool       `gorm:"not null;default:false;comment:破坏性表结构迁移是否已经管理员单独审批放行" json:"destructiveMigrationApproved"`
	OwnerReviewComment           string     `gorm:"type:text;comment:管理员最近一次验收通过或打回的意见" json:"ownerReviewComment,omitempty"`
	TokenBudget                  int        `gorm:"not null;default:200000;comment:本任务Token预算上限" json:"tokenBudget"`
	MaxTurns                     int        `gorm:"not null;default:60;comment:最大对话轮数上限" json:"maxTurns"`
	MaxDurationMinutes           int        `gorm:"not null;default:60;comment:最大执行时长分钟数上限" json:"maxDurationMinutes"`
	AgyModel                     string     `gorm:"size:96;not null;default:'';comment:本任务调用Antigravity编码使用的模型ID（为空使用默认）" json:"agyModel"`
	AgyEffort                    string     `gorm:"size:32;not null;default:'';comment:本任务调用Antigravity编码使用的推理强度（为空使用默认）" json:"agyEffort"`
	PromptTokensUsed             int        `gorm:"not null;default:0;comment:累计已消耗输入Token数" json:"promptTokensUsed"`
	CompletionTokensUsed         int        `gorm:"not null;default:0;comment:累计已消耗输出Token数" json:"completionTokensUsed"`
	DurationSeconds              int64      `gorm:"not null;default:0;comment:累计实际运行耗时秒数" json:"durationSeconds"`
	ActiveReleaseID              uint64     `gorm:"index;not null;default:0;comment:验收通过后关联生成的版本ID" json:"activeReleaseId"`
	StartedAt                    *time.Time `gorm:"comment:实际开始执行时间" json:"startedAt,omitempty"`
	AcceptedAt                   *time.Time `gorm:"comment:验收通过时间" json:"acceptedAt,omitempty"`
	CreatedAt                    time.Time  `gorm:"index;not null;comment:任务创建时间" json:"createdAt"`
	UpdatedAt                    time.Time  `gorm:"not null;comment:任务更新时间" json:"updatedAt"`
}

// TableName 返回开发任务物理表名。
func (WorkItem) TableName() string { return "work_items" }

// TableComment 返回开发任务物理表中文注释。
func (WorkItem) TableComment() string { return "自动开发任务状态、预算、方案与验收包主表" }

// WorkItemRequirement 维护开发任务与所承载需求之间的关联关系。
type WorkItemRequirement struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:任务需求关联主键ID" json:"id"`
	ProductID     uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	WorkItemID    uint64    `gorm:"uniqueIndex:uk_work_req;not null;comment:开发任务ID" json:"workItemId"`
	RequirementID uint64    `gorm:"uniqueIndex:uk_work_req;index;not null;comment:需求ID" json:"requirementId"`
	CreatedAt     time.Time `gorm:"not null;comment:关联创建时间" json:"createdAt"`
}

// TableName 返回开发任务需求关联物理表名。
func (WorkItemRequirement) TableName() string { return "work_item_requirements" }

// TableComment 返回开发任务需求关联物理表中文注释。
func (WorkItemRequirement) TableComment() string { return "开发任务与需求多对多关联表" }

// WorkGate 记录开发任务在校验阶段各道自动化门禁（构建、全量测试、注释、UTF-8、依赖、测试完整性、回滚演练、页面巡检、独立评审、公共区审计）的核验结果。
type WorkGate struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement;comment:门禁记录主键ID" json:"id"`
	ProductID    uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	WorkItemID   uint64    `gorm:"uniqueIndex:uk_work_gate;not null;comment:开发任务ID" json:"workItemId"`
	GateCode     string    `gorm:"uniqueIndex:uk_work_gate;size:48;not null;comment:门禁唯一编码（build/test/comments/utf8/deps/rollback_drill/review/impact_audit等）" json:"gateCode"`
	GateName     string    `gorm:"size:96;not null;comment:门禁中文名称" json:"gateName"`
	Status       string    `gorm:"size:24;not null;default:'pending';comment:门禁状态（pending/passed/failed/blocked）" json:"status"`
	EvidenceJSON string    `gorm:"type:mediumtext;comment:真实执行退出码与日志输出摘录证据JSON" json:"evidenceJson"`
	UpdatedAt    time.Time `gorm:"not null;comment:最近核验时间" json:"updatedAt"`
	CreatedAt    time.Time `gorm:"not null;comment:记录创建时间" json:"createdAt"`
}

// TableName 返回任务门禁物理表名。
func (WorkGate) TableName() string { return "work_gates" }

// TableComment 返回任务门禁物理表中文注释。
func (WorkGate) TableComment() string { return "开发任务自动化门禁校验状态与真实证据表" }

// WorkScopeChange 逐文件记录本次开发任务的改动区域分类（本模块区 / 其他模块区 / 公共区）、行数统计及模型给出的跨模块改动理由（8.5 节）。
type WorkScopeChange struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement;comment:改动范围明细主键ID" json:"id"`
	ProductID    uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	WorkItemID   uint64    `gorm:"index;not null;comment:开发任务ID" json:"workItemId"`
	FilePath     string    `gorm:"size:512;not null;comment:相对仓库根目录的文件路径" json:"filePath"`
	ModuleCode   string    `gorm:"size:64;not null;default:'';comment:文件命中的业务模块编码" json:"moduleCode"`
	ZoneType     string    `gorm:"size:32;not null;index;comment:范围分区（current_module本模块/other_module其他模块/public_zone公共区）" json:"zoneType"`
	ChangeAction string    `gorm:"size:16;not null;default:'modified';comment:变更动作（added/modified/deleted）" json:"changeAction"`
	AddedLines   int       `gorm:"not null;default:0;comment:新增代码行数" json:"addedLines"`
	DeletedLines int       `gorm:"not null;default:0;comment:删除代码行数" json:"deletedLines"`
	IsTestFile   bool      `gorm:"not null;default:false;comment:是否为既有测试文件修改（验收包单列）" json:"isTestFile"`
	Reason       string    `gorm:"size:512;not null;default:'';comment:跨模块、公共区或修改既有测试的说明理由" json:"reason"`
	CreatedAt    time.Time `gorm:"not null;comment:记录时间" json:"createdAt"`
}

// TableName 返回改动范围明细物理表名。
func (WorkScopeChange) TableName() string { return "work_scope_changes" }

// TableComment 返回改动范围明细物理表中文注释。
func (WorkScopeChange) TableComment() string { return "开发任务文件级三区改动范围与跨区理由标记表" }

// PreviewSession 记录 Mac 上为待验收任务启动的 tailnet 直连 HTTPS 预览实例地址与随机专用测试账号（7.6 与 8.9 节）。
type PreviewSession struct {
	ID            uint64     `gorm:"primaryKey;autoIncrement;comment:预览会话主键ID" json:"id"`
	ProductID     uint64     `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	WorkItemID    uint64     `gorm:"uniqueIndex;not null;comment:关联开发任务ID" json:"workItemId"`
	PreviewURL    string     `gorm:"size:255;not null;comment:Tailnet直连HTTPS访问地址（如https://100.64.0.2:13601）" json:"previewUrl"`
	Port          int        `gorm:"not null;default:0;comment:Mac本机分配的预览监听端口" json:"port"`
	TestUsername  string     `gorm:"size:64;not null;default:'';comment:预览专用随机演示账号名" json:"testUsername"`
	TestPassword  string     `gorm:"size:128;not null;default:'';comment:预览专用随机演示密码" json:"testPassword"`
	ProcessPID    int        `gorm:"not null;default:0;comment:开发Agent登记的预览服务进程PID" json:"processPid"`
	Status        string     `gorm:"size:24;not null;default:'running';comment:预览状态（running运行中/stopped已停止）" json:"status"`
	ExpiresAt     time.Time  `gorm:"index;not null;comment:空闲24小时自动停止截止时间" json:"expiresAt"`
	StoppedAt     *time.Time `gorm:"comment:实际停止回收时间" json:"stoppedAt,omitempty"`
	CreatedAt     time.Time  `gorm:"not null;comment:预览启动时间" json:"createdAt"`
	UpdatedAt     time.Time  `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回预览会话物理表名。
func (PreviewSession) TableName() string { return "preview_sessions" }

// TableComment 返回预览会话物理表中文注释。
func (PreviewSession) TableComment() string { return "开发任务Mac本地Tailnet预览实例会话表" }
