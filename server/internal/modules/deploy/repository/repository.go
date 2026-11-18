// Package repository 封装部署目标、部署单、分步日志、数据库备份记录及多目标全部成功判定的持久化操作。
package repository

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"autoship/internal/modules/deploy/model"
)

// Repository 提供部署领域 4 张表的数据库操作方法。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建部署模块仓储实例。
// 参数 dbProvider 为数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// ListTargets 查询指定产品（0 表示全部）下的所有部署目标。
// 参数 productID 为产品主键；返回目标列表与错误。
func (r *Repository) ListTargets(productID uint64) ([]model.DeployTarget, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	q := db.Model(&model.DeployTarget{})
	if productID > 0 {
		q = q.Where("product_id = ?", productID)
	}
	var list []model.DeployTarget
	err := q.Order("id ASC").Find(&list).Error
	for i := range list {
		list[i].QuotaExceeded = list[i].BackupQuotaMB > 0 && list[i].BackupUsedBytes > list[i].BackupQuotaMB*1024*1024
	}
	return list, err
}

// GetTargetByID 根据主键读取单个部署目标。
// 参数 id 为部署目标主键；未找到返回 (nil, nil)。
func (r *Repository) GetTargetByID(id uint64) (*model.DeployTarget, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var t model.DeployTarget
	err := db.First(&t, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	t.QuotaExceeded = t.BackupQuotaMB > 0 && t.BackupUsedBytes > t.BackupQuotaMB*1024*1024
	return &t, err
}

// CreateTarget 新建部署目标。
// 参数 t 为目标模型指针；返回创建错误。
func (r *Repository) CreateTarget(t *model.DeployTarget) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(t).Error
}

// SaveTarget 更新部署目标。
// 参数 t 为目标模型指针；返回更新错误。
func (r *Repository) SaveTarget(t *model.DeployTarget) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(t).Error
}

// DeleteTarget 删除指定部署目标。
// 参数 id 为目标主键；返回删除错误。
func (r *Repository) DeleteTarget(id uint64) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Delete(&model.DeployTarget{}, id).Error
}

// ListDeployments 按产品、目标或状态倒序分页查询部署单。
// 参数 productID/targetID/releaseID/status/page/pageSize 为筛选条件；返回部署单列表、总数与错误。
func (r *Repository) ListDeployments(productID, targetID, releaseID uint64, status string, page, pageSize int) ([]model.Deployment, int64, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, 0, errors.New("数据库尚未连接")
	}
	q := db.Model(&model.Deployment{})
	if productID > 0 {
		q = q.Where("product_id = ?", productID)
	}
	if targetID > 0 {
		q = q.Where("target_id = ?", targetID)
	}
	if releaseID > 0 {
		q = q.Where("release_id = ?", releaseID)
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
	var list []model.Deployment
	err := q.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&list).Error
	return list, total, err
}

// GetDeploymentByID 按主键读取部署单。
// 参数 id 为部署单主键；未找到返回 (nil, nil)。
func (r *Repository) GetDeploymentByID(id uint64) (*model.Deployment, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var dep model.Deployment
	err := db.First(&dep, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &dep, err
}

// GetDeploymentByUID 按唯一安全标识 deployment_uid 查询部署单。
// 参数 uid 为字符串标识；未找到返回 (nil, nil)。
func (r *Repository) GetDeploymentByUID(uid string) (*model.Deployment, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var dep model.Deployment
	err := db.Where("deployment_uid = ?", uid).First(&dep).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &dep, err
}

// CreateDeployment 新建一条部署记录。
// 参数 dep 为部署模型指针；返回写入错误。
func (r *Repository) CreateDeployment(dep *model.Deployment) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(dep).Error
}

// SaveDeployment 保存部署单状态与故障诊断信息。
// 参数 dep 为部署模型指针；返回保存错误。
func (r *Repository) SaveDeployment(dep *model.Deployment) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(dep).Error
}

// UpsertStep 新增或更新某次部署的单个固定步骤进度。
// 参数 step 为分步记录指针；返回写入错误。
func (r *Repository) UpsertStep(step *model.DeploymentStep) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "deployment_id"}, {Name: "step_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"status", "message", "updated_at"}),
	}).Create(step).Error
}

// ListSteps 查询某次部署的全部步骤日志（按开始时间升序）。
// 参数 deploymentID 为部署主键；返回步骤列表与错误。
func (r *Repository) ListSteps(deploymentID uint64) ([]model.DeploymentStep, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	var list []model.DeploymentStep
	err := db.Where("deployment_id = ?", deploymentID).Order("id ASC").Find(&list).Error
	return list, err
}

// SaveBackup 记录一条完成的数据库导出备份元数据。
// 参数 bk 为备份记录指针；返回写入错误。
func (r *Repository) SaveBackup(bk *model.DBBackup) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Create(bk).Error
}

// ListBackups 按可选产品或目标倒序查询数据库备份清单。
// 参数 productID/targetID/deploymentID 为过滤条件；返回备份列表与错误。
func (r *Repository) ListBackups(productID, targetID, deploymentID uint64) ([]model.DBBackup, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, nil
	}
	q := db.Model(&model.DBBackup{})
	if productID > 0 {
		q = q.Where("product_id = ?", productID)
	}
	if targetID > 0 {
		q = q.Where("target_id = ?", targetID)
	}
	if deploymentID > 0 {
		q = q.Where("deployment_id = ?", deploymentID)
	}
	var list []model.DBBackup
	err := q.Order("id DESC").Limit(100).Find(&list).Error
	return list, err
}

// AllEnabledTargetsSucceeded 检查指定产品下**所有处于 enabled=true 的部署目标**是否均已对指定 releaseID 存在至少一条 status='succeeded' 的部署记录（严格执行用户确认的多目标状态流转规则）。
// 参数 productID 为产品主键，releaseID 为版本主键；全部启用目标均成功（且至少有 1 个启用目标）返回 true，否则返回 false。
func (r *Repository) AllEnabledTargetsSucceeded(productID, releaseID uint64) bool {
	db := r.dbProvider()
	if db == nil {
		return false
	}
	var enabledTargets []model.DeployTarget
	if err := db.Where("product_id = ? AND enabled = ?", productID, true).Find(&enabledTargets).Error; err != nil || len(enabledTargets) == 0 {
		return false
	}
	for _, tgt := range enabledTargets {
		var cnt int64
		if err := db.Model(&model.Deployment{}).
			Where("product_id = ? AND release_id = ? AND target_id = ? AND status = 'succeeded'", productID, releaseID, tgt.ID).
			Count(&cnt).Error; err != nil || cnt == 0 {
			return false
		}
	}
	return true
}
