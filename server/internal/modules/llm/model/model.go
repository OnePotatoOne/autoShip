// Package model 定义平台客服澄清与自动分拣的大模型调用 Token 用量统计表（5.1 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(&LLMUsage{})
}

// LLMUsage 记录每次客服对话、需求自动分拣或开发任务同步回传的模型 Token 消耗与耗时明细。
type LLMUsage struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement;comment:用量记录主键ID" json:"id"`
	ProductID        uint64    `gorm:"index;not null;default:0;comment:关联产品ID" json:"productId"`
	ConversationID   uint64    `gorm:"index;not null;default:0;comment:关联澄清会话ID（非客服场景为0）" json:"conversationId"`
	WorkItemID       uint64    `gorm:"index;not null;default:0;comment:关联开发任务ID（非研发场景为0）" json:"workItemId"`
	RoleName         string    `gorm:"size:32;not null;index;comment:逻辑角色（intake/triage/coder/reviewer/auditor）" json:"roleName"`
	ModelName        string    `gorm:"size:96;not null;default:'';comment:实际调用的模型标识" json:"modelName"`
	PromptTokens     int       `gorm:"not null;default:0;comment:输入Prompt消耗Token数" json:"promptTokens"`
	CompletionTokens int       `gorm:"not null;default:0;comment:生成回复消耗Token数" json:"completionTokens"`
	TotalTokens      int       `gorm:"not null;default:0;comment:本次请求合计Token数" json:"totalTokens"`
	LatencyMs        int64     `gorm:"not null;default:0;comment:模型请求端到端耗时毫秒数" json:"latencyMs"`
	Succeeded        bool      `gorm:"not null;default:true;comment:请求是否成功返回" json:"succeeded"`
	CreatedAt        time.Time `gorm:"index;not null;comment:记录时间" json:"createdAt"`
}

// TableName 返回模型用量物理表名。
func (LLMUsage) TableName() string { return "llm_usage" }

// TableComment 返回模型用量物理表中文注释。
func (LLMUsage) TableComment() string { return "大模型角色调用Token消耗与耗时统计表" }
