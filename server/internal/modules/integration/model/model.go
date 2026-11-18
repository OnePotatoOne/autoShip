// Package model 定义业务接入方鉴权档案、接入方与产品关联、Open API 增量事件流及幂等键缓存表模型（5.5 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(
		&Integration{},
		&IntegrationProduct{},
		&OpenEvent{},
		&IdempotencyRecord{},
	)
}

// Integration 表示通过标准 Open API 接入 autoShip 的外部业务系统后端，独立持有 AppID/加密密钥及来源 IP 白名单。
type Integration struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:接入方主键ID" json:"id"`
	Name          string    `gorm:"size:128;not null;comment:接入方系统名称" json:"name"`
	AppID         string    `gorm:"uniqueIndex;size:64;not null;comment:OpenAPI调用公钥标识AppID" json:"appId"`
	AppSecretEnc  string    `gorm:"size:512;not null;comment:AES256加密存储的HMAC签名密钥" json:"-"`
	AllowedIPsCSV string    `gorm:"size:1024;not null;default:'';comment:允许访问公网口的来源IP或CIDR白名单（逗号分隔）" json:"allowedIpsCsv"`
	RateLimitRPM  int       `gorm:"not null;default:300;comment:该接入方每分钟最大请求配额" json:"rateLimitRpm"`
	Enabled       bool      `gorm:"not null;default:true;comment:是否启用该接入方" json:"enabled"`
	ProductIDs    []uint64  `gorm:"-" json:"productIds,omitempty"`
	CreatedAt     time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
	UpdatedAt     time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回接入方物理表名。
func (Integration) TableName() string { return "integrations" }

// TableComment 返回接入方物理表中文注释。
func (Integration) TableComment() string { return "外部业务系统OpenAPI接入方鉴权与IP白名单表" }

// IntegrationProduct 维护接入方与允许提交反馈的产品之间的多对多绑定关系。
type IntegrationProduct struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:关联主键ID" json:"id"`
	IntegrationID uint64    `gorm:"uniqueIndex:uk_int_prod;not null;comment:接入方ID" json:"integrationId"`
	ProductID     uint64    `gorm:"uniqueIndex:uk_int_prod;index;not null;comment:绑定的产品ID" json:"productId"`
	CreatedAt     time.Time `gorm:"not null;comment:绑定创建时间" json:"createdAt"`
}

// TableName 返回接入方产品绑定物理表名。
func (IntegrationProduct) TableName() string { return "integration_products" }

// TableComment 返回接入方产品绑定物理表中文注释。
func (IntegrationProduct) TableComment() string { return "接入方与产品多对多授权关联表" }

// OpenEvent 存储供接入方后端经 `GET /open/v1/events?cursor=` 增量轮询拉取的状态变更、AI 追问及版本上线事件（5.5 节：Server 不主动回调接入方）。
type OpenEvent struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:自增事件游标ID（即cursor）" json:"cursor"`
	IntegrationID uint64    `gorm:"index;not null;comment:归属接入方ID" json:"integrationId"`
	ProductID     uint64    `gorm:"index;not null;comment:关联产品ID" json:"productId"`
	EventType     string    `gorm:"size:48;not null;index;comment:事件类型（issue.status_changed/issue.follow_up/issue.released）" json:"eventType"`
	IssueID       uint64    `gorm:"index;not null;default:0;comment:关联需求ID" json:"issueId"`
	ExternalUID   string    `gorm:"size:128;not null;default:'';comment:关联外部提交人用户ID" json:"externalUid"`
	PayloadJSON   string    `gorm:"type:text;comment:事件详细业务载荷JSON" json:"payloadJson"`
	CreatedAt     time.Time `gorm:"index;not null;comment:事件产生时间" json:"createdAt"`
}

// TableName 返回开放事件物理表名。
func (OpenEvent) TableName() string { return "open_events" }

// TableComment 返回开放事件物理表中文注释。
func (OpenEvent) TableComment() string { return "OpenAPI接入方增量拉取状态事件流表" }

// IdempotencyRecord 缓存带 `Idempotency-Key` 的 Open API 写请求处理响应，保证网络重试时返回同一结果（5.5 节）。
type IdempotencyRecord struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:幂等记录主键ID" json:"id"`
	IntegrationID uint64    `gorm:"uniqueIndex:uk_int_idem;not null;comment:接入方ID" json:"integrationId"`
	IdemKey       string    `gorm:"uniqueIndex:uk_int_idem;size:128;not null;comment:客户端传入的Idempotency-Key" json:"idemKey"`
	Method        string    `gorm:"size:16;not null;comment:首次请求HTTP方法" json:"method"`
	Path          string    `gorm:"size:255;not null;comment:首次请求路径" json:"path"`
	StatusCode    int       `gorm:"not null;comment:缓存的HTTP响应状态码" json:"statusCode"`
	ResponseJSON  string    `gorm:"type:mediumtext;comment:缓存的响应JSON正文" json:"responseJson"`
	ExpiresAt     time.Time `gorm:"index;not null;comment:幂等缓存过期时间" json:"expiresAt"`
	CreatedAt     time.Time `gorm:"not null;comment:首次记录时间" json:"createdAt"`
}

// TableName 返回幂等记录物理表名。
func (IdempotencyRecord) TableName() string { return "idempotency_keys" }

// TableComment 返回幂等记录物理表中文注释。
func (IdempotencyRecord) TableComment() string { return "OpenAPI写接口幂等键响应缓存表" }
