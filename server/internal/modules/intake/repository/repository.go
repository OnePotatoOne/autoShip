// Package repository 封装提交人、澄清会话、对话消息、附件与 HTML 原型的数据库持久化操作。
package repository

import (
	"errors"

	"gorm.io/gorm"

	"autoship/internal/modules/intake/model"
)

// Repository 提供澄清领域 5 张表的数据库读写方法。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建澄清仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// UpsertReporter 按（integration_id + external_uid）查找或创建外部提交人，并刷新其显示名与属性。
// 参数 rep 为提交人实体指针；返回持久化后的 *model.Reporter 与错误。
func (r *Repository) UpsertReporter(rep *model.Reporter) (*model.Reporter, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var existing model.Reporter
	err := db.Where("integration_id = ? AND external_uid = ?", rep.IntegrationID, rep.ExternalUID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := db.Create(rep).Error; err != nil {
			return nil, err
		}
		return rep, nil
	}
	if err != nil {
		return nil, err
	}
	if rep.DisplayName != "" {
		existing.DisplayName = rep.DisplayName
	}
	if rep.AttributesJSON != "" {
		existing.AttributesJSON = rep.AttributesJSON
	}
	if rep.ProductID > 0 {
		existing.ProductID = rep.ProductID
	}
	_ = db.Save(&existing).Error
	return &existing, nil
}

// CreateConversation 新建澄清会话。
// 参数 conv 为会话实体指针；返回创建错误。
func (r *Repository) CreateConversation(conv *model.Conversation) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(conv).Error
}

// GetConversation 按主键读取澄清会话。
// 参数 id 为会话 ID；不存在返回 (nil, nil)。
func (r *Repository) GetConversation(id uint64) (*model.Conversation, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var conv model.Conversation
	err := db.First(&conv, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &conv, err
}

// SaveConversation 更新会话草稿或状态。
// 参数 conv 为会话指针；返回更新错误。
func (r *Repository) SaveConversation(conv *model.Conversation) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(conv).Error
}

// ListPendingAIConversations 按先进先出（ID 升序）查询离线留言产生的 pending_ai 会话。
// 参数 limit 为单批处理上限；返回会话切片与错误。
func (r *Repository) ListPendingAIConversations(limit int) ([]model.Conversation, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.Conversation
	err := db.Where("status = 'pending_ai'").Order("id ASC").Limit(limit).Find(&list).Error
	return list, err
}

// AppendMessage 保存一条会话消息。
// 参数 msg 为消息模型指针；返回写入错误。
func (r *Repository) AppendMessage(msg *model.ConversationMessage) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(msg).Error
}

// ListMessages 按正序查询某会话的全部消息记录。
// 参数 convID 为会话主键；返回消息切片与错误。
func (r *Repository) ListMessages(convID uint64) ([]model.ConversationMessage, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.ConversationMessage
	err := db.Where("conversation_id = ?", convID).Order("id ASC").Find(&list).Error
	return list, err
}

// CreateAttachment 保存上传图片附件元数据。
// 参数 att 为附件实体指针；返回写入错误。
func (r *Repository) CreateAttachment(att *model.Attachment) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(att).Error
}

// CreatePrototype 保存新生成的单文件 HTML 原型版本（自动递增会话内版本号）。
// 参数 proto 为原型实体指针；返回写入错误。
func (r *Repository) CreatePrototype(proto *model.Prototype) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	var count int64
	_ = db.Model(&model.Prototype{}).Where("conversation_id = ?", proto.ConversationID).Count(&count).Error
	proto.VersionNum = int(count) + 1
	return db.Create(proto).Error
}

// ListPrototypes 查询某会话迭代产生的全部原型版本。
// 参数 convID 为会话主键；返回原型切片与错误。
func (r *Repository) ListPrototypes(convID uint64) ([]model.Prototype, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.Prototype
	err := db.Where("conversation_id = ?", convID).Order("version_num ASC").Find(&list).Error
	return list, err
}
