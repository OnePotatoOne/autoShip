// Package repository 提供管理员账号与管理会话的数据库持久化操作（含 OTP 时间步原子行锁更新）。
package repository

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"autoship/internal/modules/auth/model"
)

// Repository 封装 users 与 admin_sessions 表的数据访问层。
type Repository struct {
	dbProvider func() *gorm.DB
}

// New 创建认证仓储实例。
// 参数 dbProvider 为全局数据库连接获取函数；返回 *Repository。
func New(dbProvider func() *gorm.DB) *Repository {
	return &Repository{dbProvider: dbProvider}
}

// CountUsers 统计数据库中已创建的管理员账号总数，用于判断是否首次启动未初始化。
// 无入参；返回用户数及查询错误。
func (r *Repository) CountUsers() (int64, error) {
	db := r.dbProvider()
	if db == nil {
		return 0, errors.New("数据库尚未连接")
	}
	var count int64
	err := db.Model(&model.User{}).Count(&count).Error
	return count, err
}

// CreateUser 创建新的管理员账号。
// 参数 u 为待持久化的 User 模型指针；返回创建错误。
func (r *Repository) CreateUser(u *model.User) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(u).Error
}

// FindByUsername 根据登录用户名查找管理员。
// 参数 username 为登录名；不存在返回 (nil, nil)，成功返回 (*model.User, nil)。
func (r *Repository) FindByUsername(username string) (*model.User, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var u model.User
	err := db.Where("username = ?", username).First(&u).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &u, err
}

// FindByID 根据主键 ID 查找管理员。
// 参数 id 为用户主键；不存在返回 (nil, nil)。
func (r *Repository) FindByID(id uint64) (*model.User, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var u model.User
	err := db.First(&u, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &u, err
}

// UpdateUser 保存管理员字段变更。
// 参数 u 为修改后的管理员对象指针；返回更新错误。
func (r *Repository) UpdateUser(u *model.User) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Save(u).Error
}

// WithLockedUser 在数据库事务中对指定用户加 SELECT ... FOR UPDATE 行锁执行回调，防止并发 OTP 重放攻击。
// 参数 userID 为目标用户 ID，fn 为事务内校验与更新回调；返回事务结果错误。
func (r *Repository) WithLockedUser(userID uint64, fn func(u *model.User) error) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var u model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&u, userID).Error; err != nil {
			return err
		}
		if err := fn(&u); err != nil {
			return err
		}
		return tx.Save(&u).Error
	})
}

// CreateSession 持久化一条新的管理员登录会话。
// 参数 sess 为会话模型指针；返回写入错误。
func (r *Repository) CreateSession(sess *model.AdminSession) error {
	db := r.dbProvider()
	if db == nil {
		return errors.New("数据库尚未连接")
	}
	return db.Create(sess).Error
}

// FindActiveSession 根据会话令牌哈希查询尚未过期的有效管理员会话。
// 参数 tokenHash 为 Cookie 原始令牌的 SHA-256 哈希，now 为当前时刻；有效返回会话指针，否则返回 (nil, nil)。
func (r *Repository) FindActiveSession(tokenHash string, now time.Time) (*model.AdminSession, error) {
	db := r.dbProvider()
	if db == nil {
		return nil, errors.New("数据库尚未连接")
	}
	var sess model.AdminSession
	err := db.Where("session_token = ? AND expires_at > ?", tokenHash, now).First(&sess).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &sess, err
}

// DeleteSessionByHash 注销指定会话令牌对应的记录。
// 参数 tokenHash 为会话令牌哈希；返回删除错误。
func (r *Repository) DeleteSessionByHash(tokenHash string) error {
	db := r.dbProvider()
	if db == nil {
		return nil
	}
	return db.Where("session_token = ?", tokenHash).Delete(&model.AdminSession{}).Error
}
