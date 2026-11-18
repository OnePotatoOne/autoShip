// Package model 定义开发任务运行活动流与本地 JSONL 会话记录引用的数据表模型（5.1、8.1 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(&RunActivity{}, &RunTranscript{})
}

// RunActivity 存储开发 Agent 在规格、编码、校验、独立评审及公共区审计阶段同步回传的实时活动流条目。
type RunActivity struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement;comment:活动记录主键ID" json:"id"`
	ProductID  uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	WorkItemID uint64    `gorm:"index;not null;comment:所属开发任务ID" json:"workItemId"`
	Stage      string    `gorm:"size:32;not null;default:'coding';comment:所属阶段（prepare/spec/coding/validate/review/impact_audit/publish）" json:"stage"`
	Kind       string    `gorm:"size:32;not null;default:'info';comment:活动类别（info/tool_call/scope_mark/sandbox_block/gate/warn/error）" json:"kind"`
	Summary    string    `gorm:"size:512;not null;comment:活动可读摘要说明" json:"summary"`
	DetailJSON string    `gorm:"type:mediumtext;comment:脱敏后的结构化详情JSON" json:"detailJson,omitempty"`
	CreatedAt  time.Time `gorm:"index;not null;comment:产生时间" json:"createdAt"`
}

// TableName 返回运行活动物理表名。
func (RunActivity) TableName() string { return "run_activities" }

// TableComment 返回运行活动物理表中文注释。
func (RunActivity) TableComment() string { return "开发Agent任务执行实时活动流表" }

// RunTranscript 保存开发 Agent 本机 JSONL 完整会话记录文件的引用与摘要（8.1 节：完整 JSONL 落盘在 Mac，Server 保存引用、摘要与用量）。
type RunTranscript struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement;comment:会话引用主键ID" json:"id"`
	ProductID        uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	WorkItemID       uint64    `gorm:"index;not null;comment:所属开发任务ID" json:"workItemId"`
	SessionRole      string    `gorm:"size:32;not null;comment:会话角色（spec/coder/reviewer/impact_auditor）" json:"sessionRole"`
	LocalFilePath    string    `gorm:"size:512;not null;comment:Mac本机JSONL会话文件绝对路径" json:"localFilePath"`
	TurnCount        int       `gorm:"not null;default:0;comment:本会话交互轮数" json:"turnCount"`
	PromptTokens     int       `gorm:"not null;default:0;comment:输入消耗Token数" json:"promptTokens"`
	CompletionTokens int       `gorm:"not null;default:0;comment:输出消耗Token数" json:"completionTokens"`
	Summary          string    `gorm:"type:text;comment:会话结论摘要" json:"summary"`
	CreatedAt        time.Time `gorm:"not null;comment:记录创建时间" json:"createdAt"`
	UpdatedAt        time.Time `gorm:"not null;comment:记录更新时间" json:"updatedAt"`
}

// TableName 返回会话记录引用物理表名。
func (RunTranscript) TableName() string { return "run_transcripts" }

// TableComment 返回会话记录引用物理表中文注释。
func (RunTranscript) TableComment() string { return "开发任务本机JSONL会话文件引用与用量摘要表" }
