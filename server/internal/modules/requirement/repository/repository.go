// Package repository 封装需求池检索、相似需求查找、提出人关联及状态时间线的持久化操作。
package repository

import (
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"autoship/internal/modules/requirement/model"
)

// Repository 提供需求领域 4 张表的读写操作。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建需求仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// Filter 定义需求池多维筛选参数。
type Filter struct {
	ProductID  uint64
	ModuleCode string
	IssueType  string
	Status     string
	Keyword    string
	Page       int
	PageSize   int
}

// List 按条件倒序分页查询需求池。
// 参数 f 为筛选参数；返回需求切片、总记录数及错误。
func (r *Repository) List(f Filter) ([]model.Requirement, int64, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, 0, errors.New("数据库尚未连接")
	}
	q := db.Model(&model.Requirement{})
	if f.ProductID > 0 {
		q = q.Where("product_id = ?", f.ProductID)
	}
	if f.ModuleCode != "" {
		q = q.Where("module_code = ?", f.ModuleCode)
	}
	if f.IssueType != "" {
		q = q.Where("issue_type = ?", f.IssueType)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if strings.TrimSpace(f.Keyword) != "" {
		like := "%" + strings.TrimSpace(f.Keyword) + "%"
		q = q.Where("title LIKE ? OR description LIKE ?", like, like)
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
	var list []model.Requirement
	err := q.Order("id DESC").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&list).Error
	return list, total, err
}

// GetByID 按主键查询单条需求。
// 参数 id 为需求主键；不存在返回 (nil, nil)。
func (r *Repository) GetByID(id uint64) (*model.Requirement, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var req model.Requirement
	err := db.First(&req, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &req, err
}

// GetByIDs 批量读取指定 ID 集合的需求。
// 参数 ids 为需求主键列表；返回需求切片与错误。
func (r *Repository) GetByIDs(ids []uint64) ([]model.Requirement, error) {
	db := r.dbProvider()
	if db == nil || len(ids) == 0 {
		return nil, nil
	}
	var list []model.Requirement
	err := db.Where("id IN ?", ids).Order("id ASC").Find(&list).Error
	return list, err
}

// Create 新建需求记录。
// 参数 req 为需求模型指针；返回创建错误。
func (r *Repository) Create(req *model.Requirement) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(req).Error
}

// Save 保存需求字段变更。
// 参数 req 为需求模型指针；返回更新错误。
func (r *Repository) Save(req *model.Requirement) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(req).Error
}

// SearchSimilar 在同一产品内按关键词模糊检索相似候选需求（供客服 AI search_similar 与自动分拣使用）。
// 参数 productID 为产品 ID，query 为关键词，excludeID 为排除自身 ID，limit 为返回上限；返回匹配列表与错误。
func (r *Repository) SearchSimilar(productID uint64, query string, excludeID uint64, limit int) ([]model.Requirement, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	if limit <= 0 {
		limit = 5
	}
	q := db.Where("product_id = ?", productID)
	if excludeID > 0 {
		q = q.Where("id <> ?", excludeID)
	}
	trimmed := strings.TrimSpace(query)
	if trimmed != "" {
		like := "%" + trimmed + "%"
		q = q.Where("title LIKE ? OR description LIKE ?", like, like)
	}
	var list []model.Requirement
	err := q.Order("id DESC").Limit(limit).Find(&list).Error
	return list, err
}

// ListPendingTriage 查询因模型离线而排队等待自动分拣的需求（按先进先出升序）。
// 参数 limit 为单批处理上限；返回需求列表与错误。
func (r *Repository) ListPendingTriage(limit int) ([]model.Requirement, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.Requirement
	err := db.Where("triage_pending = ?", true).Order("id ASC").Limit(limit).Find(&list).Error
	return list, err
}

// AddLink 建立两条需求之间的关联记录（冲突时忽略）。
// 参数 link 为关联对象指针；返回写入错误。
func (r *Repository) AddLink(link *model.RequirementLink) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(link).Error
}

// ListLinks 查询与某需求相关的全部关联关系。
// 参数 reqID 为需求主键；返回关联记录切片与错误。
func (r *Repository) ListLinks(reqID uint64) ([]model.RequirementLink, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.RequirementLink
	err := db.Where("source_requirement_id = ? OR target_requirement_id = ?", reqID, reqID).Order("id DESC").Find(&list).Error
	return list, err
}

// BindReporter 绑定某需求的提出人记录。
// 参数 rep 为提出人关联指针；返回错误。
func (r *Repository) BindReporter(rep *model.RequirementReporter) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(rep).Error
}

// ListReporters 查询某需求关联的全部提出人。
// 参数 reqID 为需求 ID；返回提出人列表与错误。
func (r *Repository) ListReporters(reqID uint64) ([]model.RequirementReporter, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.RequirementReporter
	err := db.Where("requirement_id = ?", reqID).Order("id ASC").Find(&list).Error
	return list, err
}

// ListIssuesByExternalReporter 查询某接入方下指定外部提交人的历史反馈需求列表（供 `GET /open/v1/reporters/:rid/issues` 使用）。
// 参数 integrationID 为接入方 ID，externalUID 为外部用户标识；返回需求列表与错误。
func (r *Repository) ListIssuesByExternalReporter(integrationID uint64, externalUID string) ([]model.Requirement, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var rels []model.RequirementReporter
	if err := db.Where("integration_id = ? AND external_uid = ?", integrationID, externalUID).Find(&rels).Error; err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return []model.Requirement{}, nil
	}
	ids := make([]uint64, 0, len(rels))
	for _, rel := range rels {
		ids = append(ids, rel.RequirementID)
	}
	var issues []model.Requirement
	err := db.Where("id IN ?", ids).Order("id DESC").Find(&issues).Error
	return issues, err
}

// AppendEvent 追加一条需求时间线记录。
// 参数 ev 为事件指针；返回写入错误。
func (r *Repository) AppendEvent(ev *model.RequirementEvent) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Create(ev).Error
}

// ListEvents 查询某需求的时间线记录（按时间正序）。
// 参数 reqID 为需求主键；返回时间线列表与错误。
func (r *Repository) ListEvents(reqID uint64) ([]model.RequirementEvent, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.RequirementEvent
	err := db.Where("requirement_id = ?", reqID).Order("id ASC").Find(&list).Error
	return list, err
}

// CountByStatus 统计指定状态集合下的需求数量（供总览页使用）。
// 参数 statuses 为状态切片；返回计数与错误。
func (r *Repository) CountByStatus(statuses ...string) (int64, error) {
	db := r.dbProvider()
	if db == nil {
		return 0, nil
	}
	var count int64
	err := db.Model(&model.Requirement{}).Where("status IN ?", statuses).Count(&count).Error
	return count, err
}
