// Package repository 封装运行活动流与会话文件引用的数据库读写。
package repository

import (
	"gorm.io/gorm"

	"autoship/internal/modules/run/model"
)

// Repository 提供 run_activities 与 run_transcripts 表的数据操作。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建运行记录仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// AppendActivity 追加一条任务执行活动记录。
// 参数 act 为活动模型指针；返回写入错误。
func (r *Repository) AppendActivity(act *model.RunActivity) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Create(act).Error
}

// ListActivities 查询某开发任务的最近活动流（按时间正序）。
// 参数 workItemID 为任务主键，limit 为条数上限；返回活动列表与错误。
func (r *Repository) ListActivities(workItemID uint64, limit int) ([]model.RunActivity, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var list []model.RunActivity
	err := db.Where("work_item_id = ?", workItemID).Order("id ASC").Limit(limit).Find(&list).Error
	return list, err
}

// SaveTranscript 新增或更新开发任务会话引用摘要。
// 参数 tr 为会话引用模型指针；返回写入错误。
func (r *Repository) SaveTranscript(tr *model.RunTranscript) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Save(tr).Error
}

// ListTranscripts 查询某开发任务的全部会话记录引用。
// 参数 workItemID 为任务主键；返回引用列表与错误。
func (r *Repository) ListTranscripts(workItemID uint64) ([]model.RunTranscript, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.RunTranscript
	err := db.Where("work_item_id = ?", workItemID).Order("id ASC").Find(&list).Error
	return list, err
}
