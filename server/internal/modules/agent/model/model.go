// Package model 定义开发 Agent 与更新 Agent 的身份注册、在线心跳及持久化指令队列数据表模型（7.1 ~ 7.3 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(&Agent{}, &AgentCommand{})
}

// Agent 表示已在管理后台登记的开发 Agent（Mac）或更新 Agent（产品服务器），使用一次性令牌换取 Ed25519 公钥身份，按范围授权且可随时吊销。
type Agent struct {
	ID                 uint64     `gorm:"primaryKey;autoIncrement;comment:Agent主键ID" json:"id"`
	Name               string     `gorm:"size:128;not null;comment:Agent标识名称" json:"name"`
	AgentType          string     `gorm:"size:24;not null;index;comment:Agent类型（dev开发Agent/update更新Agent）" json:"agentType"`
	DeployTargetID     uint64     `gorm:"index;not null;default:0;comment:更新Agent绑定的专属部署目标ID（开发Agent为0）" json:"deployTargetId"`
	ProductID          uint64     `gorm:"index;not null;default:0;comment:更新Agent所属产品ID（开发Agent为0表示跨产品）" json:"productId"`
	ProductCode        string     `gorm:"size:64;not null;default:'';comment:更新Agent绑定产品编码" json:"productCode"`
	AllowedIPsCSV      string     `gorm:"size:512;not null;default:'';comment:更新Agent访问公网口的固定来源IP白名单" json:"allowedIpsCsv"`
	RegTokenHash       string     `gorm:"size:128;not null;default:'';index;comment:一次性注册令牌SHA256摘要（登记后立即清空）" json:"-"`
	RegTokenExpiresAt  *time.Time `gorm:"comment:一次性注册令牌过期时间（生成后15分钟有效）" json:"regTokenExpiresAt,omitempty"`
	PublicKeyB64       string     `gorm:"size:128;not null;default:'';comment:登记的Ed25519公钥Base64" json:"publicKeyB64"`
	TailnetIP          string     `gorm:"size:64;not null;default:'';comment:开发Agent在tailnet内的IP地址" json:"tailnetIp"`
	RelayListenAddr    string     `gorm:"size:128;not null;default:'';comment:开发Agent受限模型中继HTTPS地址（如100.64.0.2:9445）" json:"relayListenAddr"`
	AgentVersion       string     `gorm:"size:48;not null;default:'';comment:最近上报的Agent程序版本号" json:"agentVersion"`
	ProductVersion     string     `gorm:"size:48;not null;default:'';comment:更新Agent上报的目标服务器当前产品版本号" json:"productVersion"`
	ServiceStatus      string     `gorm:"size:32;not null;default:'';comment:目标服务器产品服务运行状态" json:"serviceStatus"`
	DiskFreeBytes      int64      `gorm:"not null;default:0;comment:目标服务器剩余可用磁盘字节数" json:"diskFreeBytes"`
	DiskTotalBytes     int64      `gorm:"not null;default:0;comment:目标服务器总磁盘字节数" json:"diskTotalBytes"`
	BackupUsedBytes    int64      `gorm:"not null;default:0;comment:目标服务器数据库备份专用目录占用字节数" json:"backupUsedBytes"`
	Status             string     `gorm:"size:24;not null;default:'pending';index;comment:注册状态（pending待登记/active正常/revoked已吊销）" json:"status"`
	Online             bool       `gorm:"-" json:"online"`
	LastHeartbeatAt    *time.Time `gorm:"comment:最近一次心跳上报时间" json:"lastHeartbeatAt,omitempty"`
	RegisteredAt       *time.Time `gorm:"comment:首次完成公钥登记时间" json:"registeredAt,omitempty"`
	CreatedAt          time.Time  `gorm:"not null;comment:记录创建时间" json:"createdAt"`
	UpdatedAt          time.Time  `gorm:"not null;comment:记录更新时间" json:"updatedAt"`
}

// TableName 返回 Agent 注册物理表名。
func (Agent) TableName() string { return "agents" }

// TableComment 返回 Agent 注册物理表中文注释。
func (Agent) TableComment() string { return "开发Agent与更新Agent身份凭据及心跳状态表" }

// AgentCommand 表示 Server 下发给指定 Agent 的持久化指令队列记录，状态遵循：pending(待投递) → delivered(已投递) → running(执行中) → succeeded/failed/expired（7.1 节）。
type AgentCommand struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement;comment:指令自增序号（同时作为单调递增seq）" json:"seq"`
	CommandUID  string     `gorm:"uniqueIndex;size:64;not null;comment:全局唯一指令幂等ID" json:"id"`
	AgentID     uint64     `gorm:"index:idx_agent_seq;not null;comment:接收该指令的目标Agent主键ID" json:"agentId"`
	CommandType string     `gorm:"size:64;not null;comment:指令类型编码" json:"type"`
	PayloadJSON string     `gorm:"type:mediumtext;comment:指令完整业务载荷JSON" json:"payloadJson"`
	Status      string     `gorm:"size:24;not null;default:'pending';index;comment:队列状态（pending/delivered/running/succeeded/failed/expired）" json:"status"`
	ErrorMsg    string     `gorm:"size:512;not null;default:'';comment:执行失败原因摘要" json:"errorMsg"`
	ExpiresAt   time.Time  `gorm:"index;not null;comment:指令投递过期时间" json:"expiresAt"`
	DeliveredAt *time.Time `gorm:"comment:首次投递到Agent的时间" json:"deliveredAt,omitempty"`
	FinishedAt  *time.Time `gorm:"comment:指令执行终态时间" json:"finishedAt,omitempty"`
	CreatedAt   time.Time  `gorm:"not null;comment:指令入队时间" json:"createdAt"`
	UpdatedAt   time.Time  `gorm:"not null;comment:指令状态更新时间" json:"updatedAt"`
}

// TableName 返回 Agent 指令队列物理表名。
func (AgentCommand) TableName() string { return "agent_commands" }

// TableComment 返回 Agent 指令队列物理表中文注释。
func (AgentCommand) TableComment() string { return "Server向Agent下发的持久化幂等指令队列表" }
