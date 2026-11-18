// Package repository 封装版本记录与制品元数据的持久化查询以及旧未部署版本取代更新操作。
package repository

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"autoship/internal/modules/release/model"
	"autoship/pkg/semver"
)

// Repository 提供 releases 与 release_artifacts 表的数据访问方法。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建版本模块仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// List 按产品与状态倒序分页查询版本列表。
// 参数 productID/status/page/pageSize 为查询条件；返回版本切片、总数与错误。
func (r *Repository) List(productID uint64, status string, page, pageSize int) ([]model.Release, int64, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, 0, errors.New("数据库尚未连接")
	}
	q := db.Model(&model.Release{})
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
	var list []model.Release
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// GetByID 根据主键读取单个版本记录。
// 参数 id 为版本主键；未找到返回 (nil, nil)。
func (r *Repository) GetByID(id uint64) (*model.Release, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var rel model.Release
	err := db.First(&rel, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &rel, err
}

// LatestSemanticVersion 查询某产品历史上已创建的最大语义化版本号（供自动计算下一版本号）。
// 参数 productID 为产品主键；返回最大版本号字符串（无记录时返回空串）。
func (r *Repository) LatestSemanticVersion(productID uint64) string {
	db := r.dbProvider()
	if db == nil {
		return ""
	}
	var list []model.Release
	_ = db.Where("product_id = ?", productID).Find(&list).Error
	var bestStr string
	var bestVer semver.Version
	hasBest := false
	for _, item := range list {
		v, err := semver.Parse(item.Version)
		if err != nil {
			continue
		}
		if !hasBest || v.Compare(bestVer) > 0 {
			bestVer = v
			bestStr = v.String()
			hasBest = true
		}
	}
	return bestStr
}

// Create 新建版本记录。
// 参数 rel 为版本指针；返回创建错误。
func (r *Repository) Create(rel *model.Release) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(rel).Error
}

// Save 保存版本记录修改。
// 参数 rel 为版本指针；返回错误。
func (r *Repository) Save(rel *model.Release) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(rel).Error
}

// SaveArtifact 保存或更新某版本关联的制品元数据。
// 参数 art 为制品实体指针；返回写入错误。
func (r *Repository) SaveArtifact(art *model.ReleaseArtifact) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "release_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"file_name", "storage_path", "size_bytes", "sha256"}),
	}).Create(art).Error
}

// GetArtifact 查询某版本的落地制品元数据。
// 参数 releaseID 为版本主键；不存在返回 (nil, nil)。
func (r *Repository) GetArtifact(releaseID uint64) (*model.ReleaseArtifact, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var art model.ReleaseArtifact
	err := db.Where("release_id = ?", releaseID).First(&art).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &art, err
}

// SupersedeOlderPendingReleases 当更高版本部署成功后，将该产品所有版本号低于 deployedVersion 且仍处于 pending_deploy 的旧版本标记为 superseded（已取代）（4.1 节）。
// 参数 productID 为产品主键，deployedVersion 为刚刚完成部署的新版本号；无返回值。
func (r *Repository) SupersedeOlderPendingReleases(productID uint64, deployedVersion string) {
	db := r.dbProvider()
	if db == nil {
		return
	}
	curVer, err := semver.Parse(deployedVersion)
	if err != nil {
		return
	}
	var pending []model.Release
	if err := db.Where("product_id = ? AND status = 'pending_deploy'", productID).Find(&pending).Error; err != nil {
		return
	}
	for _, old := range pending {
		if oldVer, parseErr := semver.Parse(old.Version); parseErr == nil && oldVer.Compare(curVer) < 0 {
			_ = db.Model(&model.Release{}).Where("id = ?", old.ID).Update("status", "superseded").Error
		}
	}
}

// CountByStatus 统计处于指定状态的版本数量（供总览页使用）。
// 参数 statuses 为状态切片；返回计数与错误。
func (r *Repository) CountByStatus(statuses ...string) (int64, error) {
	db := r.dbProvider()
	if db == nil {
		return 0, nil
	}
	var count int64
	err := db.Model(&model.Release{}).Where("status IN ?", statuses).Count(&count).Error
	return count, err
}
