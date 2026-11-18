// Package repository 封装接入方档案、绑定产品关系、增量开放事件及幂等缓存表的数据持久化逻辑。
package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"

	"autoship/internal/modules/integration/model"
)

// Repository 提供接入方领域 4 张数据表的读写封装。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建接入方模块仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// ListIntegrations 查询所有接入方并附带其绑定的产品 ID 列表。
// 无入参；返回接入方列表及查询错误。
func (r *Repository) ListIntegrations() ([]model.Integration, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var list []model.Integration
	if err := db.Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	for i := range list {
		pids, _ := r.GetBoundProductIDs(list[i].ID)
		list[i].ProductIDs = pids
	}
	return list, nil
}

// GetByAppID 根据 AppID 查询启用的接入方及其绑定产品 ID 列表。
// 参数 appID 为请求头携带的 AppID；未找到返回 (nil, nil)。
func (r *Repository) GetByAppID(appID string) (*model.Integration, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var item model.Integration
	err := db.Where("app_id = ?", appID).First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.ProductIDs, _ = r.GetBoundProductIDs(item.ID)
	return &item, nil
}

// GetByID 根据主键查询单个接入方。
// 参数 id 为接入方主键；未找到返回 (nil, nil)。
func (r *Repository) GetByID(id uint64) (*model.Integration, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var item model.Integration
	err := db.First(&item, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	item.ProductIDs, _ = r.GetBoundProductIDs(item.ID)
	return &item, nil
}

// GetBoundProductIDs 查询指定接入方已授权绑定的所有产品 ID。
// 参数 integrationID 为接入方 ID；返回 uint64 切片与错误。
func (r *Repository) GetBoundProductIDs(integrationID uint64) ([]uint64, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var rows []model.IntegrationProduct
	if err := db.Where("integration_id = ?", integrationID).Order("product_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ProductID)
	}
	return ids, nil
}

// Create 在事务中保存接入方并建立产品绑定关系。
// 参数 item 为接入方实体指针，productIDs 为绑定的产品 ID 切片；返回错误。
func (r *Repository) Create(item *model.Integration, productIDs []uint64) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(item).Error; err != nil {
			return err
		}
		for _, pid := range productIDs {
			if err := tx.Create(&model.IntegrationProduct{IntegrationID: item.ID, ProductID: pid}).Error; err != nil {
				return err
			}
		}
		item.ProductIDs = productIDs
		return nil
	})
}

// Update 在事务中修改接入方基础属性及绑定的产品列表。
// 参数 item 为修改后的接入方指针，productIDs 为最新绑定产品切片；返回错误。
func (r *Repository) Update(item *model.Integration, productIDs []uint64) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(item).Error; err != nil {
			return err
		}
		if err := tx.Where("integration_id = ?", item.ID).Delete(&model.IntegrationProduct{}).Error; err != nil {
			return err
		}
		for _, pid := range productIDs {
			if err := tx.Create(&model.IntegrationProduct{IntegrationID: item.ID, ProductID: pid}).Error; err != nil {
				return err
			}
		}
		item.ProductIDs = productIDs
		return nil
	})
}

// Delete 删除接入方及其产品关联记录。
// 参数 id 为接入方主键；返回事务错误。
func (r *Repository) Delete(id uint64) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		_ = tx.Where("integration_id = ?", id).Delete(&model.IntegrationProduct{}).Error
		return tx.Delete(&model.Integration{}, id).Error
	})
}

// AppendEvent 追加一条供接入方轮询消费的增量状态事件。
// 参数 ev 为事件对象指针；返回写入错误。
func (r *Repository) AppendEvent(ev *model.OpenEvent) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Create(ev).Error
}

// ListEventsAfter 按游标升序拉取某接入方的增量事件。
// 参数 integrationID 为接入方 ID，cursor 为上次消费到的最大事件 ID，limit 为单次最多条数；返回事件列表与错误。
func (r *Repository) ListEventsAfter(integrationID, cursor uint64, limit int) ([]model.OpenEvent, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var list []model.OpenEvent
	err := db.Where("integration_id = ? AND id > ?", integrationID, cursor).
		Order("id ASC").Limit(limit).Find(&list).Error
	return list, err
}

// FindIdempotency 查找某接入方尚未过期的幂等键缓存记录。
// 参数 integrationID 为接入方 ID，idemKey 为客户端幂等键，now 为当前时刻；未命中返回 (nil, nil)。
func (r *Repository) FindIdempotency(integrationID uint64, idemKey string, now time.Time) (*model.IdempotencyRecord, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var rec model.IdempotencyRecord
	err := db.Where("integration_id = ? AND idem_key = ? AND expires_at > ?", integrationID, idemKey, now).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &rec, err
}

// SaveIdempotency 保存写接口响应到幂等缓存表。
// 参数 rec 为幂等记录指针；返回保存错误。
func (r *Repository) SaveIdempotency(rec *model.IdempotencyRecord) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Create(rec).Error
}
