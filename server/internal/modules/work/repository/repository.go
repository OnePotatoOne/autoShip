// Package repository 封装开发任务、需求绑定、门禁结果、改动范围明细及预览实例的数据持久化操作。
package repository

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"autoship/internal/modules/work/model"
)

// Repository 提供开发任务领域 5 张表的数据库读写方法。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建开发任务仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// List 按可选产品与状态倒序分页查询开发任务。
// 参数 productID 为产品过滤，status 为状态过滤，page/pageSize 为分页参数；返回任务列表、总数与错误。
func (r *Repository) List(productID uint64, status string, page, pageSize int) ([]model.WorkItem, int64, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, 0, errors.New("数据库尚未连接")
	}
	q := db.Model(&model.WorkItem{})
	if productID > 0 {
		q = q.Where("product_id = ?", productID)
	}
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	var list []model.WorkItem
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// GetByID 根据主键读取单个开发任务。
// 参数 id 为任务主键；未找到返回 (nil, nil)。
func (r *Repository) GetByID(id uint64) (*model.WorkItem, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var w model.WorkItem
	err := db.First(&w, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &w, err
}

// CreateWithRequirements 在事务中新建开发任务并绑定关联的需求 ID 集合。
// 参数 item 为任务模型指针，reqIDs 为关联需求列表；返回事务错误。
func (r *Repository) CreateWithRequirements(item *model.WorkItem, reqIDs []uint64) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(item).Error; err != nil {
			return err
		}
		for _, rid := range reqIDs {
			rel := &model.WorkItemRequirement{
				ProductID:     item.ProductID,
				WorkItemID:    item.ID,
				RequirementID: rid,
			}
			if err := tx.Create(rel).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// Save 保存开发任务变更。
// 参数 item 为任务指针；返回错误。
func (r *Repository) Save(item *model.WorkItem) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(item).Error
}

// GetRequirementIDs 查询某开发任务绑定的全部需求主键。
// 参数 workItemID 为开发任务 ID；返回需求 ID 切片与错误。
func (r *Repository) GetRequirementIDs(workItemID uint64) ([]uint64, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var rows []model.WorkItemRequirement
	if err := db.Where("work_item_id = ?", workItemID).Order("requirement_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.RequirementID)
	}
	return ids, nil
}

// UpsertGate 新增或按（work_item_id, gate_code）更新门禁状态与证据。
// 参数 gate 为门禁记录指针；返回写入错误。
func (r *Repository) UpsertGate(gate *model.WorkGate) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "work_item_id"}, {Name: "gate_code"}},
		DoUpdates: clause.AssignmentColumns([]string{"gate_name", "status", "evidence_json", "updated_at"}),
	}).Create(gate).Error
}

// ListGates 查询某开发任务的所有门禁记录。
// 参数 workItemID 为任务 ID；返回门禁切片与错误。
func (r *Repository) ListGates(workItemID uint64) ([]model.WorkGate, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.WorkGate
	err := db.Where("work_item_id = ?", workItemID).Order("id ASC").Find(&list).Error
	return list, err
}

// ReplaceScopeChanges 全量刷新某开发任务的最终三区改动范围文件明细。
// 参数 workItemID/productID 为关联主键，changes 为改动明细列表；返回事务错误。
func (r *Repository) ReplaceScopeChanges(workItemID, productID uint64, changes []model.WorkScopeChange) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("work_item_id = ?", workItemID).Delete(&model.WorkScopeChange{}).Error; err != nil {
			return err
		}
		for i := range changes {
			changes[i].ID = 0
			changes[i].WorkItemID = workItemID
			changes[i].ProductID = productID
			if err := tx.Create(&changes[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListScopeChanges 查询某开发任务的三区改动范围明细。
// 参数 workItemID 为任务 ID；返回明细切片与错误。
func (r *Repository) ListScopeChanges(workItemID uint64) ([]model.WorkScopeChange, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.WorkScopeChange
	err := db.Where("work_item_id = ?", workItemID).Order("zone_type DESC, file_path ASC").Find(&list).Error
	return list, err
}

// UpsertPreviewSession 创建或更新某开发任务的预览实例记录。
// 参数 sess 为预览会话指针；返回错误。
func (r *Repository) UpsertPreviewSession(sess *model.PreviewSession) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "work_item_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"preview_url", "port", "test_username", "test_password", "process_pid", "status", "expires_at", "stopped_at", "updated_at"}),
	}).Create(sess).Error
}

// GetPreviewSession 查询某开发任务的预览实例信息。
// 参数 workItemID 为任务 ID；未启动返回 (nil, nil)。
func (r *Repository) GetPreviewSession(workItemID uint64) (*model.PreviewSession, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var sess model.PreviewSession
	err := db.Where("work_item_id = ?", workItemID).First(&sess).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &sess, err
}

// CountByStatus 统计指定状态集合下的开发任务数量（供总览页使用）。
// 参数 statuses 为状态枚举列表；返回计数与错误。
func (r *Repository) CountByStatus(statuses ...string) (int64, error) {
	db := r.dbProvider()
	if db == nil {
		return 0, nil
	}
	var count int64
	err := db.Model(&model.WorkItem{}).Where("status IN ?", statuses).Count(&count).Error
	return count, err
}
