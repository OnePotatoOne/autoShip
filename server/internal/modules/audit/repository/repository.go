// Package repository 封装审计日志与动态配置的数据持久化查询方法。
package repository

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"autoship/internal/modules/audit/model"
)

// Repository 提供 audit_logs 与 settings 表的读写封装。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建绑定数据库获取函数的审计仓储实例。
// 参数 dbProvider 为返回当前 *gorm.DB 的函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// CreateAudit 写入一条不可变审计记录。
// 参数 entry 为待保存的 AuditLog 指针；数据库未就绪时静默跳过，写入失败返回错误。
func (r *Repository) CreateAudit(entry *model.AuditLog) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Create(entry).Error
}

// ListFilter 定义审计日志分页筛选条件。
type ListFilter struct {
	ProductID uint64 // 按产品筛选（0 不限）
	Module    string // 按模块筛选
	Action    string // 按动作筛选
	ActorID   string // 按操作人筛选
	Page      int    // 页码
	PageSize  int    // 每页条数
}

// ListAudits 按筛选条件倒序分页查询审计日志。
// 参数 f 为筛选与分页参数；返回记录切片、总条数及查询错误。
func (r *Repository) ListAudits(f ListFilter) ([]model.AuditLog, int64, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, 0, errors.New("数据库尚未初始化")
	}
	q := db.Model(&model.AuditLog{})
	if f.ProductID > 0 {
		q = q.Where("product_id = ?", f.ProductID)
	}
	if f.Module != "" {
		q = q.Where("module = ?", f.Module)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.ActorID != "" {
		q = q.Where("actor_id LIKE ?", "%"+f.ActorID+"%")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 || f.PageSize > 100 {
		f.PageSize = 20
	}
	var list []model.AuditLog
	err := q.Order("id DESC").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&list).Error
	return list, total, err
}

// UpsertSetting 新增或按 (product_id, setting_key) 唯一索引更新动态配置项。
// 参数 item 为配置模型指针；成功返回 nil，失败返回错误。
func (r *Repository) UpsertSetting(item *model.Setting) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未初始化")
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "product_id"}, {Name: "setting_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"setting_val", "description", "updated_by", "updated_at"}),
	}).Create(item).Error
}

// GetSetting 查询指定产品与键名的动态配置。
// 参数 productID 为产品 ID（0 表示全局），key 为配置键；未找到时返回 (nil, nil)，异常返回错误。
func (r *Repository) GetSetting(productID uint64, key string) (*model.Setting, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var item model.Setting
	err := db.Where("product_id = ? AND setting_key = ?", productID, key).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &item, err
}

// ListGlobalSettings 查询全部平台级（product_id=0）动态设置。
// 无入参；返回配置列表及查询错误。
func (r *Repository) ListGlobalSettings() ([]model.Setting, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.Setting
	err := db.Where("product_id = 0").Order("id ASC").Find(&list).Error
	return list, err
}
