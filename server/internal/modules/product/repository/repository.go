// Package repository 封装产品档案、模块表、范围规则、权限档案及产品知识的数据库访问方法。
package repository

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	"autoship/internal/modules/product/model"
)

// Repository 封装产品领域 5 张数据表的持久化操作。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建产品模块仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// ListProducts 返回全部产品列表（按 ID 升序）。
// 无入参；返回产品切片及查询错误。
func (r *Repository) ListProducts() ([]model.Product, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var list []model.Product
	err := db.Order("id ASC").Find(&list).Error
	return list, err
}

// GetProductByID 根据产品主键查询单个产品档案。
// 参数 id 为产品主键；不存在返回 (nil, nil)。
func (r *Repository) GetProductByID(id uint64) (*model.Product, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var p model.Product
	err := db.First(&p, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &p, err
}

// GetProductByCode 根据产品唯一编码查询产品档案。
// 参数 code 为产品编码；不存在返回 (nil, nil)。
func (r *Repository) GetProductByCode(code string) (*model.Product, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var p model.Product
	err := db.Where("code = ?", strings.TrimSpace(code)).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &p, err
}

// CreateProduct 在事务中新建产品，并自动初始化一条默认“保护（protected）”档位的权限档案。
// 参数 p 为产品实体指针；返回事务错误。
func (r *Repository) CreateProduct(p *model.Product) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(p).Error; err != nil {
			return err
		}
		prof := &model.ProductPermissionProfile{
			ProductID:             p.ID,
			Mode:                  "protected",
			AllowedWritePathsJSON: "[]",
			ExtraDeniedReadJSON:   "[]",
		}
		return tx.Create(prof).Error
	})
}

// UpdateProduct 更新已有产品基础配置。
// 参数 p 为产品模型指针；返回更新错误。
func (r *Repository) UpdateProduct(p *model.Product) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(p).Error
}

// DeleteProduct 删除指定产品及其子配置（模块、范围规则、权限档案、知识库）。
// 参数 id 为产品主键；返回事务错误。
func (r *Repository) DeleteProduct(id uint64) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		_ = tx.Where("product_id = ?", id).Delete(&model.ProductModule{}).Error
		_ = tx.Where("product_id = ?", id).Delete(&model.ProductScopeRule{}).Error
		_ = tx.Where("product_id = ?", id).Delete(&model.ProductPermissionProfile{}).Error
		_ = tx.Where("product_id = ?", id).Delete(&model.ProductKnowledge{}).Error
		return tx.Delete(&model.Product{}, id).Error
	})
}

// ListModules 查询指定产品下的所有业务模块。
// 参数 productID 为产品主键；返回模块切片与错误。
func (r *Repository) ListModules(productID uint64) ([]model.ProductModule, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var list []model.ProductModule
	err := db.Where("product_id = ?", productID).Order("id ASC").Find(&list).Error
	return list, err
}

// ReplaceModules 在事务中整体替换某产品的模块列表。
// 参数 productID 为产品主键，modules 为新模块切片；返回错误。
func (r *Repository) ReplaceModules(productID uint64, modules []model.ProductModule) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("product_id = ?", productID).Delete(&model.ProductModule{}).Error; err != nil {
			return err
		}
		for i := range modules {
			modules[i].ID = 0
			modules[i].ProductID = productID
			if err := tx.Create(&modules[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListScopeRules 查询指定产品的公共区与受保护路径规则。
// 参数 productID 为产品主键；返回范围规则列表与错误。
func (r *Repository) ListScopeRules(productID uint64) ([]model.ProductScopeRule, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var list []model.ProductScopeRule
	err := db.Where("product_id = ?", productID).Order("id ASC").Find(&list).Error
	return list, err
}

// ReplaceScopeRules 整体更新某产品的范围规则配置。
// 参数 productID 为产品主键，rules 为新范围规则切片；返回事务错误。
func (r *Repository) ReplaceScopeRules(productID uint64, rules []model.ProductScopeRule) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("product_id = ?", productID).Delete(&model.ProductScopeRule{}).Error; err != nil {
			return err
		}
		for i := range rules {
			rules[i].ID = 0
			rules[i].ProductID = productID
			if err := tx.Create(&rules[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// GetOrInitPermissionProfile 获取指定产品的沙箱权限档案；若历史数据不存在则自动创建默认保护档。
// 参数 productID 为产品主键；返回权限档案指针与错误。
func (r *Repository) GetOrInitPermissionProfile(productID uint64) (*model.ProductPermissionProfile, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var prof model.ProductPermissionProfile
	err := db.Where("product_id = ?", productID).First(&prof).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		prof = model.ProductPermissionProfile{
			ProductID:             productID,
			Mode:                  "protected",
			AllowedWritePathsJSON: "[]",
			ExtraDeniedReadJSON:   "[]",
		}
		if createErr := db.Create(&prof).Error; createErr != nil {
			return nil, createErr
		}
		return &prof, nil
	}
	return &prof, err
}

// SavePermissionProfile 保存产品沙箱权限档案修改。
// 参数 prof 为权限档案指针；返回保存错误。
func (r *Repository) SavePermissionProfile(prof *model.ProductPermissionProfile) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(prof).Error
}

// ListKnowledge 查询指定产品的知识库列表（可选按关键字检索）。
// 参数 productID 为产品 ID，keyword 为可选匹配词；返回知识条目切片与错误。
func (r *Repository) ListKnowledge(productID uint64, keyword string) ([]model.ProductKnowledge, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	q := db.Where("product_id = ?", productID)
	if strings.TrimSpace(keyword) != "" {
		like := "%" + strings.TrimSpace(keyword) + "%"
		q = q.Where("title LIKE ? OR content LIKE ?", like, like)
	}
	var list []model.ProductKnowledge
	err := q.Order("id ASC").Find(&list).Error
	return list, err
}

// ReplaceManualKnowledge 替换人工维护的产品知识条目（保留发布流水线同步的 auto_map 条目或按传入全量覆盖）。
// 参数 productID 为产品 ID，items 为知识切片；返回事务错误。
func (r *Repository) ReplaceManualKnowledge(productID uint64, items []model.ProductKnowledge) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("product_id = ? AND source = 'manual'", productID).Delete(&model.ProductKnowledge{}).Error; err != nil {
			return err
		}
		for i := range items {
			items[i].ID = 0
			items[i].ProductID = productID
			if items[i].Source == "" {
				items[i].Source = "manual"
			}
			if err := tx.Create(&items[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// SyncAutoProjectMap 由开发 Agent 发布成功后刷新本产品的自动项目地图知识条目（8.4 节）。
// 参数 productID 为产品 ID，mapContent 为结构化项目地图说明；返回保存错误。
func (r *Repository) SyncAutoProjectMap(productID uint64, mapContent string) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		_ = tx.Where("product_id = ? AND source = 'auto_map'", productID).Delete(&model.ProductKnowledge{}).Error
		item := &model.ProductKnowledge{
			ProductID: productID,
			Category:  "project_map",
			Title:     "自动同步项目地图",
			Content:   mapContent,
			Source:    "auto_map",
		}
		return tx.Create(item).Error
	})
}
