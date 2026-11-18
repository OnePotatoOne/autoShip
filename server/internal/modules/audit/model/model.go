// Package model 定义平台操作审计日志与动态配置存储的数据表模型。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(&AuditLog{}, &Setting{})
}

// AuditLog 记录管理后台操作、Agent 指令下发、方案/验收审批、版本发布与生产部署的全量不可篡改审计轨迹。
type AuditLog struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement;comment:审计记录主键ID" json:"id"`
	ProductID  uint64    `gorm:"index;not null;default:0;comment:关联产品ID（0表示平台全局操作）" json:"productId"`
	ActorType  string    `gorm:"size:32;not null;index;comment:操作主体类型（admin/dev_agent/update_agent/open_api/system）" json:"actorType"`
	ActorID    string    `gorm:"size:64;not null;index;comment:操作主体标识（管理员用户名、AgentID或AppID）" json:"actorId"`
	Module     string    `gorm:"size:48;not null;index;comment:所属业务模块编码" json:"module"`
	Action     string    `gorm:"size:64;not null;index;comment:执行动作标识" json:"action"`
	TargetType string    `gorm:"size:48;not null;default:'';comment:目标对象类型" json:"targetType"`
	TargetID   string    `gorm:"size:64;not null;default:'';comment:目标对象ID" json:"targetId"`
	Method     string    `gorm:"size:16;not null;default:'';comment:HTTP请求方法" json:"method"`
	Path       string    `gorm:"size:255;not null;default:'';comment:请求路径" json:"path"`
	ClientIP   string    `gorm:"size:64;not null;default:'';comment:来源客户端IP地址" json:"clientIp"`
	StatusCode int       `gorm:"not null;default:200;comment:处理结果状态码" json:"statusCode"`
	ErrorMsg   string    `gorm:"size:512;not null;default:'';comment:失败原因公开摘要（不含敏感堆栈）" json:"errorMsg"`
	DetailJSON string    `gorm:"type:text;comment:脱敏后的结构化操作详情JSON" json:"detailJson"`
	CreatedAt  time.Time `gorm:"index;not null;comment:审计发生时间" json:"createdAt"`
}

// TableName 返回审计日志物理表名。
func (AuditLog) TableName() string { return "audit_logs" }

// TableComment 返回审计日志物理表中文注释。
func (AuditLog) TableComment() string { return "平台全量安全与操作审计日志表" }

// Setting 保存平台运行时可在管理后台调整的键值设置（如飞书 Webhook、模型通道模式等，敏感值带 ENC: 加密存储）。
type Setting struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;comment:配置项主键ID" json:"id"`
	ProductID   uint64    `gorm:"uniqueIndex:uk_prod_key;not null;default:0;comment:所属产品ID（0表示平台级全局设置）" json:"productId"`
	SettingKey  string    `gorm:"uniqueIndex:uk_prod_key;size:96;not null;comment:配置键名称" json:"settingKey"`
	SettingVal  string    `gorm:"type:text;comment:配置键值内容（敏感项以ENC:加密存储）" json:"settingVal"`
	Description string    `gorm:"size:255;not null;default:'';comment:配置项中文说明" json:"description"`
	UpdatedBy   string    `gorm:"size:64;not null;default:'system';comment:最近修改人" json:"updatedBy"`
	CreatedAt   time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
	UpdatedAt   time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回系统设置物理表名。
func (Setting) TableName() string { return "settings" }

// TableComment 返回系统设置物理表中文注释。
func (Setting) TableComment() string { return "平台与产品动态配置键值表" }
