// Package model 定义需求库主表、相似需求关联表、提出人关联表及需求时间线事件表（4.1、5.4 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(
		&Requirement{},
		&RequirementLink{},
		&RequirementReporter{},
		&RequirementEvent{},
	)
}

// Requirement 表示归档进需求池的一条缺陷或新功能/改进需求，维护面向提交人的业务状态，进入 in_progress 后在视图上联动展示开发任务、版本与部署的各自状态。
type Requirement struct {
	ID                 uint64    `gorm:"primaryKey;autoIncrement;comment:需求主键ID" json:"id"`
	ProductID          uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	ConversationID     uint64    `gorm:"index;not null;default:0;comment:来源澄清会话ID（后台手动录入为0）" json:"conversationId"`
	ModuleCode         string    `gorm:"size:64;not null;default:'';index;comment:归属产品业务模块编码" json:"moduleCode"`
	IssueType          string    `gorm:"size:32;not null;default:'feature';index;comment:需求类型（bug缺陷/feature新功能与改进）" json:"issueType"`
	Title              string    `gorm:"size:255;not null;comment:结构化需求标题" json:"title"`
	Description        string    `gorm:"type:mediumtext;comment:结构化需求详述或问题背景" json:"description"`
	ExpectedBehavior   string    `gorm:"type:text;comment:期望行为或期望结果" json:"expectedBehavior"`
	ActualBehavior     string    `gorm:"type:text;comment:缺陷实际结果与影响范围" json:"actualBehavior"`
	ReproSteps         string    `gorm:"type:text;comment:缺陷复现操作步骤与频率" json:"reproSteps"`
	OutOfScope         string    `gorm:"type:text;comment:明确不包含的范围" json:"outOfScope"`
	AcceptanceCriteria string    `gorm:"type:mediumtext;comment:可验证的验收标准（Given/When/Then）" json:"acceptanceCriteria"`
	SummaryCardJSON    string    `gorm:"type:mediumtext;comment:提交人已确认的完整需求卡或复现卡JSON" json:"summaryCardJson"`
	PrototypeID        uint64    `gorm:"not null;default:0;comment:关联最终低保真HTML原型版本ID" json:"prototypeId"`
	EnvInfoJSON        string    `gorm:"type:text;comment:脱敏后的提交环境信息JSON（版本/路由/角色等）" json:"envInfoJson"`
	Priority           string    `gorm:"size:16;not null;default:'P2';index;comment:优先级（P0/P1/P2/P3）" json:"priority"`
	EffortSize         string    `gorm:"size:16;not null;default:'M';comment:工作量估计（S/M/L）" json:"effortSize"`
	ReadinessScore     int       `gorm:"not null;default:80;comment:可开工度评分（0-100，衡量验收标准可验证程度）" json:"readinessScore"`
	TagsCSV            string    `gorm:"size:255;not null;default:'';comment:分类标签（逗号分隔）" json:"tagsCsv"`
	Status             string    `gorm:"size:32;not null;default:'submitted';index;comment:业务状态（clarifying/pending_ai/submitted/triaged/scheduled/in_progress/released/closed）" json:"status"`
	CloseReason        string    `gorm:"size:32;not null;default:'';comment:关闭原因（rejected/duplicate/deferred/answered）" json:"closeReason"`
	CloseNote          string    `gorm:"size:512;not null;default:'';comment:关闭备注说明" json:"closeNote"`
	TriagePending      bool      `gorm:"not null;default:false;index;comment:模型离线时是否排队等待自动分拣" json:"triagePending"`
	ActiveWorkItemID   uint64    `gorm:"index;not null;default:0;comment:当前关联开发任务ID" json:"activeWorkItemId"`
	ReleasedVersion    string    `gorm:"size:48;not null;default:'';comment:最终已上线版本号" json:"releasedVersion"`
	Archived           bool      `gorm:"not null;default:false;index;comment:是否已只读归档" json:"archived"`
	WorkStatus         string    `gorm:"-" json:"workStatus,omitempty"`
	ReleaseStatus      string    `gorm:"-" json:"releaseStatus,omitempty"`
	DeployStatus       string    `gorm:"-" json:"deployStatus,omitempty"`
	CreatedAt          time.Time `gorm:"index;not null;comment:提交创建时间" json:"createdAt"`
	UpdatedAt          time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回需求主表物理表名。
func (Requirement) TableName() string { return "requirements" }

// TableComment 返回需求主表物理表中文注释。
func (Requirement) TableComment() string { return "产品需求与缺陷池主表" }

// RequirementLink 记录同产品内相似或相关需求之间的双向/单向关联关系（5.4 节：首版提示并建立关联，正式合并后置）。
type RequirementLink struct {
	ID                  uint64    `gorm:"primaryKey;autoIncrement;comment:需求关联主键ID" json:"id"`
	ProductID           uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	SourceRequirementID uint64    `gorm:"uniqueIndex:uk_req_link;not null;comment:源需求ID" json:"sourceRequirementId"`
	TargetRequirementID uint64    `gorm:"uniqueIndex:uk_req_link;index;not null;comment:目标相似需求ID" json:"targetRequirementId"`
	LinkType            string    `gorm:"size:32;not null;default:'similar';comment:关联类型（similar相似/related相关/duplicate重复参考）" json:"linkType"`
	CreatedBy           string    `gorm:"size:64;not null;default:'ai';comment:建立关联来源（ai或管理员名）" json:"createdBy"`
	CreatedAt           time.Time `gorm:"not null;comment:关联创建时间" json:"createdAt"`
}

// TableName 返回需求关联物理表名。
func (RequirementLink) TableName() string { return "requirement_links" }

// TableComment 返回需求关联物理表中文注释。
func (RequirementLink) TableComment() string { return "相似与相关需求关联关系表" }

// RequirementReporter 记录某条需求的原始提出人与附议关注提交人列表（供状态变化与版本上线时生成增量通知事件）。
type RequirementReporter struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:需求提出人关联主键ID" json:"id"`
	ProductID     uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	RequirementID uint64    `gorm:"uniqueIndex:uk_req_rep;not null;comment:需求ID" json:"requirementId"`
	ReporterID    uint64    `gorm:"uniqueIndex:uk_req_rep;index;not null;comment:提交人主键ID" json:"reporterId"`
	IntegrationID uint64    `gorm:"index;not null;default:0;comment:来源接入方ID" json:"integrationId"`
	ExternalUID   string    `gorm:"size:128;not null;default:'';index;comment:外部业务系统用户ID" json:"externalUid"`
	DisplayName   string    `gorm:"size:128;not null;default:'';comment:提交人显示名称" json:"displayName"`
	CreatedAt     time.Time `gorm:"not null;comment:记录时间" json:"createdAt"`
}

// TableName 返回需求提出人关联物理表名。
func (RequirementReporter) TableName() string { return "requirement_reporters" }

// TableComment 返回需求提出人关联物理表中文注释。
func (RequirementReporter) TableComment() string { return "需求与外部提出人关联表" }

// RequirementEvent 记录需求从提交、分拣、开工、发布到上线的完整状态时间线。
type RequirementEvent struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:时间线事件主键ID" json:"id"`
	ProductID     uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	RequirementID uint64    `gorm:"index;not null;comment:关联需求ID" json:"requirementId"`
	EventType     string    `gorm:"size:48;not null;comment:事件类型编码" json:"eventType"`
	FromStatus    string    `gorm:"size:32;not null;default:'';comment:变更前状态" json:"fromStatus"`
	ToStatus      string    `gorm:"size:32;not null;default:'';comment:变更后状态" json:"toStatus"`
	Actor         string    `gorm:"size:64;not null;default:'system';comment:触发者标识" json:"actor"`
	Summary       string    `gorm:"size:512;not null;default:'';comment:时间线中文可读摘要" json:"summary"`
	CreatedAt     time.Time `gorm:"index;not null;comment:发生时间" json:"createdAt"`
}

// TableName 返回需求时间线物理表名。
func (RequirementEvent) TableName() string { return "requirement_events" }

// TableComment 返回需求时间线物理表中文注释。
func (RequirementEvent) TableComment() string { return "需求全生命周期状态变更时间线表" }
