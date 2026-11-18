// Package repository 封装 llm_usage 表的数据写入与聚合查询。
package repository

import (
	"gorm.io/gorm"

	"autoship/internal/modules/llm/model"
)

// Repository 提供模型调用用量表的读写方法。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建模型用量仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// Record 保存一条模型调用 Token 消耗记录。
// 参数 item 为用量模型指针；返回保存错误。
func (r *Repository) Record(item *model.LLMUsage) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Create(item).Error
}

// SumConversationTokens 统计单个客服澄清会话累计已消耗的总 Token 数（用于强制执行会话预算上限）。
// 参数 conversationID 为会话主键；返回累计 Token 数及查询错误。
func (r *Repository) SumConversationTokens(conversationID uint64) (int, error) {
	db := r.dbProvider()
	if db == nil || conversationID == 0 {
		return 0, nil
	}
	var sum int64
	err := db.Model(&model.LLMUsage{}).
		Where("conversation_id = ?", conversationID).
		Select("COALESCE(SUM(total_tokens), 0)").Scan(&sum).Error
	return int(sum), err
}
