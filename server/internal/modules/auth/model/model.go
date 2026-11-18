// Package model 定义管理后台管理员账号与持久化安全会话表模型。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(&User{}, &AdminSession{})
}

// User 表示 autoShip 管理后台的管理员账号，支持 bcrypt 口令哈希、RFC 6238 TOTP 动态口令、防重放步数记录及连续失败锁定。
type User struct {
	ID                 uint64     `gorm:"primaryKey;autoIncrement;comment:管理员主键ID" json:"id"`
	Username           string     `gorm:"uniqueIndex;size:64;not null;comment:管理员登录用户名" json:"username"`
	PasswordHash       string     `gorm:"size:255;not null;comment:bcrypt密码哈希值" json:"-"`
	OTPSecretEnc       string     `gorm:"size:512;not null;default:'';comment:加密存储的Base32动态口令密钥" json:"-"`
	OTPEnabled         bool       `gorm:"not null;default:false;comment:是否已启用OTP双因素认证" json:"otpEnabled"`
	OTPLastStep        int64      `gorm:"not null;default:0;comment:最近一次成功消费的TOTP时间步（防30秒内重放）" json:"-"`
	RecoveryHashesJSON string     `gorm:"type:text;comment:剩余可用一次性恢复码SHA256摘要列表JSON" json:"-"`
	FailedLoginCount   int        `gorm:"not null;default:0;comment:连续登录失败次数（用于限流锁定）" json:"-"`
	LockedUntil        *time.Time `gorm:"comment:连续失败触发账户锁定的截止时间" json:"lockedUntil,omitempty"`
	LastLoginAt        *time.Time `gorm:"comment:最近一次成功登录时间" json:"lastLoginAt,omitempty"`
	LastLoginIP        string     `gorm:"size:64;not null;default:'';comment:最近一次成功登录来源IP" json:"lastLoginIp,omitempty"`
	CreatedAt          time.Time  `gorm:"not null;comment:账号创建时间" json:"createdAt"`
	UpdatedAt          time.Time  `gorm:"not null;comment:账号更新时间" json:"updatedAt"`
}

// TableName 返回管理员账号物理表名。
func (User) TableName() string { return "users" }

// TableComment 返回管理员账号物理表中文注释。
func (User) TableComment() string { return "管理后台管理员账号与OTP双因素认证表" }

// AdminSession 表示已登录管理员的服务端会话记录，将 HttpOnly 会话令牌与会话级 CSRF 令牌强绑定（10 节安全要求）。
type AdminSession struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement;comment:会话主键ID" json:"id"`
	SessionToken string    `gorm:"uniqueIndex;size:128;not null;comment:会话令牌SHA256摘要" json:"-"`
	CSRFToken    string    `gorm:"size:128;not null;comment:与本会话绑定的CSRF防护令牌" json:"-"`
	UserID       uint64    `gorm:"index;not null;comment:关联管理员用户ID" json:"userId"`
	Username     string    `gorm:"size:64;not null;comment:冗余管理员用户名" json:"username"`
	ClientIP     string    `gorm:"size:64;not null;default:'';comment:创建会话时的客户端IP" json:"clientIp"`
	UserAgent    string    `gorm:"size:255;not null;default:'';comment:客户端浏览器标识" json:"userAgent"`
	ExpiresAt    time.Time `gorm:"index;not null;comment:会话过期时间" json:"expiresAt"`
	CreatedAt    time.Time `gorm:"not null;comment:会话创建时间" json:"createdAt"`
}

// TableName 返回管理会话物理表名。
func (AdminSession) TableName() string { return "admin_sessions" }

// TableComment 返回管理会话物理表中文注释。
func (AdminSession) TableComment() string { return "管理后台登录会话与绑定CSRF令牌表" }
