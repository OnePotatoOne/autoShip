// Package daemon 实现产品服务器无特权常驻进程 autoship-agent 的身份注册、心跳上报、长轮询指令拉取、断点续传下载、9 步安全升级编排、自动回滚及本地持久化恢复（对应设计文档 9.2 ~ 9.5 节）。
package daemon

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"autoship/internal/updateagent/apply"
	"autoship/pkg/envelope"
	"autoship/pkg/manifest"
)

const (
	// CmdDeployUpgrade 是 Server 下发的版本升级指令类型编码。
	CmdDeployUpgrade = "cmd.deploy.upgrade"
	// CmdDeployExecute 是兼容统一信封示例的部署升级别名编码。
	CmdDeployExecute = "cmd.deploy.execute"
	// CmdDeployRollback 是 Server 下发的按部署 ID 手动回滚指令类型编码。
	CmdDeployRollback = "cmd.deploy.rollback"
	// CmdDeployBackup 是 Server 下发的独立数据库备份指令类型编码。
	CmdDeployBackup = "cmd.deploy.backup"

	// StatusBackingUp 表示部署正处于数据库备份阶段（4.1 节：备份中）。
	StatusBackingUp = "backing_up"
	// StatusDeploying 表示部署正处于预检、下载、替换安装或上线健康检查阶段（4.1 节：部署中）。
	StatusDeploying = "deploying"
	// StatusSucceeded 表示部署或独立操作已顺利完成且健康检查通过（4.1 节：成功）。
	StatusSucceeded = "succeeded"
	// StatusRolledBack 表示升级被阻断或上线检查失败后已安全回到旧版本（4.1 节：失败已回滚）。
	StatusRolledBack = "rolled_back"
	// StatusRollbackFailed 表示升级失败且自动回滚或旧版本恢复检查同样失败，需触发飞书紧急通知（4.1 节：回滚失败）。
	StatusRollbackFailed = "rollback_failed"

	// HeaderAgentID 是携带已登记 Agent 编号的请求头名称。
	HeaderAgentID = "X-AutoShip-Agent-Id"
	// HeaderTimestamp 是携带秒级 Unix 时间戳的请求头名称。
	HeaderTimestamp = "X-AutoShip-Timestamp"
	// HeaderNonce 是携带随机防重放令牌的请求头名称。
	HeaderNonce = "X-AutoShip-Nonce"
	// HeaderSignature 是携带 Ed25519 请求签名的请求头名称。
	HeaderSignature = "X-AutoShip-Signature"
)

// FlexibleID 兼容反序列化服务端返回的 uint64 数字型或字符串型 Agent ID，统一存储为十进制/原样字符串。
type FlexibleID string

// UnmarshalJSON 将 JSON 数字或字符串安全解析为 FlexibleID。
// 参数 data 为原始 JSON 字段字节；成功返回 nil，类型不合法时返回错误，由 encoding/json 自动调用。
func (f *FlexibleID) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*f = ""
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*f = FlexibleID(strings.TrimSpace(s))
		return nil
	}
	var num uint64
	if err := json.Unmarshal(data, &num); err == nil {
		*f = FlexibleID(strconv.FormatUint(num, 10))
		return nil
	}
	return fmt.Errorf("无法解析 Agent ID 字段: %s", trimmed)
}

// String 返回标准字符串形式的标识符。
// 无入参；返回去空白后的字符串。
func (f FlexibleID) String() string {
	return strings.TrimSpace(string(f))
}

// Identity 保存 autoship-agent 首次注册后持久化在 <stateDir>/identity.json 的 Ed25519 密钥对与目标绑定信息。
type Identity struct {
	AgentID          string `json:"agentId"`                    // Server 分配的 Agent 唯一标识（用于 X-AutoShip-Agent-Id）
	TargetID         uint64 `json:"targetId"`                   // 授权绑定的部署目标 ID
	ProductCode      string `json:"productCode"`                // 授权绑定的产品唯一编码
	PublicKeyB64     string `json:"publicKeyB64"`               // 本地生成的 Ed25519 公钥（Base64）
	PrivateKeyB64    string `json:"privateKeyB64"`              // 本地生成的 Ed25519 私钥（Base64，0600 落盘保护）
	ServerSignPubB64 string `json:"serverSignPubB64,omitempty"` // 注册时服务端下发的动作票据验签公钥副本
	RegisteredAt     int64  `json:"registeredAt"`               // 注册成功秒级 Unix 时间戳
}

// RegisterRequest 定义首次启动调用 POST /agent/v1/deploy/register 的请求体结构（7.3 第 2 步）。
type RegisterRequest struct {
	Token        string `json:"token"`        // 管理后台签发的一次性注册令牌
	PublicKeyB64 string `json:"publicKeyB64"` // 本地生成的 Ed25519 公钥（Base64）
	AgentVersion string `json:"agentVersion"` // 当前更新 Agent 语义化版本号
}

// RegisterResponse 定义 POST /agent/v1/deploy/register 返回的身份绑定结果。
type RegisterResponse struct {
	AgentID          FlexibleID `json:"agentId"`                    // 服务端分配的 Agent ID（兼容 uint64 与 string）
	TargetID         uint64     `json:"targetId"`                   // 绑定的部署目标 ID
	ProductCode      string     `json:"productCode"`                // 绑定的产品唯一编码
	ServerSignPubB64 string     `json:"serverSignPubB64,omitempty"` // 可选返回的 Server 票据签名公钥
}

// HeartbeatRequest 定义每 30 秒调用 POST /agent/v1/deploy/heartbeat 上报的状态载荷（9.2 节）。
type HeartbeatRequest struct {
	AgentVersion    string `json:"agentVersion"`    // 更新 Agent 程序版本
	ProductVersion  string `json:"productVersion"`  // 当前目标服务器运行的产品版本
	ServiceStatus   string `json:"serviceStatus"`   // 产品服务运行状态：running | migrating | degraded | down
	DiskFreeBytes   int64  `json:"diskFreeBytes"`   // 可用磁盘字节数
	DiskTotalBytes  int64  `json:"diskTotalBytes"`  // 总磁盘字节数
	BackupUsedBytes int64  `json:"backupUsedBytes"` // 数据库备份专用目录已用字节数
}

// PollResponse 定义 GET /agent/v1/deploy/poll?ack=<seq> 返回的指令列表结构。
type PollResponse struct {
	Commands []envelope.Message `json:"commands"` // 待执行的统一消息信封数组
}

// UpgradeCommandPayload 定义 cmd.deploy.upgrade 指令的业务载荷结构。
type UpgradeCommandPayload struct {
	DeploymentID   string                    `json:"deploymentId"`             // 部署记录唯一标识（为空时回退读取 Ticket.DeploymentID）
	ReleaseID      uint64                    `json:"releaseId"`                // 版本主键 ID（为 0 时回退读取 Ticket.ReleaseID）
	TargetID       uint64                    `json:"targetId"`                 // 部署目标 ID（为 0 时回退读取 Ticket.TargetID）
	ProductCode    string                    `json:"productCode"`              // 产品唯一编码（为空时回退读取 Ticket.ProductCode）
	Version        string                    `json:"version"`                  // 目标升级版本号（为空时回退读取 Ticket.Version）
	BundleSize     int64                     `json:"bundleSize,omitempty"`     // 预期制品包总字节数（大于 0 时强校验）
	BundleSHA256   string                    `json:"bundleSha256,omitempty"`   // 预期制品包整包 SHA-256（非空时强校验）
	BackupDB       bool                      `json:"backupDb"`                 // 后台确认部署时是否手动勾选备份数据库
	Manifest       *manifest.ReleaseManifest `json:"manifest,omitempty"`       // 可选内联下发的签名发布清单（用于下载前前置验签与防降级拦截）
	Ticket         envelope.ActionTicket     `json:"ticket"`                   // Server 签发的 upgrade 动作票据
	BackupTicket   *envelope.ActionTicket    `json:"backupTicket,omitempty"`   // 可选独立签发的 backup 动作票据
	RollbackTicket *envelope.ActionTicket    `json:"rollbackTicket,omitempty"` // 可选独立签发的 rollback 动作票据
}

// RollbackCommandPayload 定义 cmd.deploy.rollback 手动回滚指令的业务载荷结构。
type RollbackCommandPayload struct {
	DeploymentID    string                `json:"deploymentId"`              // 回滚上报的目标部署记录 ID（为空时使用 Ticket.DeploymentID）
	ExpectedVersion string                `json:"expectedVersion,omitempty"` // 可选期望回滚后恢复到的旧版本号
	Ticket          envelope.ActionTicket `json:"ticket"`                    // Server 签发的 rollback 动作票据
}

// BackupCommandPayload 定义 cmd.deploy.backup 手动数据库备份指令的业务载荷结构。
type BackupCommandPayload struct {
	DeploymentID string                `json:"deploymentId"` // 关联部署或备份记录 ID（为空时使用 Ticket.DeploymentID）
	Ticket       envelope.ActionTicket `json:"ticket"`       // Server 签发的 backup 动作票据
}

// HealthCheckResponse 表示产品免登录健康检查接口返回的探测结构（第 6 节与 9.4 第 6 步）。
type HealthCheckResponse struct {
	Status        string         `json:"status"`        // 服务总体状态（如 ok / healthy / running / migrating）
	Version       string         `json:"version"`       // 当前运行实例的语义化版本号
	MigrationDone bool           `json:"migrationDone"` // 启动期数据库迁移是否已全部完成
	Probes        map[string]any `json:"probes"`        // 各业务模块只读探测结果映射
}

// DeployReport 定义调用 POST /agent/v1/deploy/deployments/<deploymentId>/report 上报的分步进度与终态诊断载荷（9.4 第 9 步）。
type DeployReport struct {
	CommandID           string                  `json:"commandId,omitempty"`           // 触发本次操作的指令信封 ID
	DeploymentID        string                  `json:"deploymentId"`                  // 部署记录唯一标识
	ReleaseID           uint64                  `json:"releaseId,omitempty"`           // 关联版本 ID
	TargetID            uint64                  `json:"targetId,omitempty"`            // 部署目标 ID
	ProductCode         string                  `json:"productCode,omitempty"`         // 产品唯一编码
	Version             string                  `json:"version,omitempty"`             // 目标版本号
	Status              string                  `json:"status"`                        // 部署大状态：backing_up | deploying | succeeded | rolled_back | rollback_failed
	Step                string                  `json:"step"`                          // 当前步骤编码
	StepStatus          string                  `json:"stepStatus"`                    // 步骤状态：running | succeeded | failed
	Message             string                  `json:"message"`                       // 脱敏后的步骤说明或故障摘要
	FailedStep          string                  `json:"failedStep,omitempty"`          // 失败发生的具体步骤名
	ExitCode            int                     `json:"exitCode,omitempty"`            // 失败子命令退出码
	StderrTail          string                  `json:"stderrTail,omitempty"`          // 脱敏后的标准错误输出末尾
	LogTail             string                  `json:"logTail,omitempty"`             // 脱敏后的产品服务日志末尾
	HealthResponse      string                  `json:"healthResponse,omitempty"`      // 脱敏后的健康检查原始响应末尾
	Backup              *apply.BackupResult     `json:"backup,omitempty"`              // 数据库备份汇总对象（已备份时附带）
	BackupDir           string                  `json:"backupDir,omitempty"`           // 备份目录绝对路径（扁平化兼容字段）
	BackupFiles         []apply.BackupFileInfo  `json:"backupFiles,omitempty"`         // 备份文件列表（扁平化兼容字段）
	BackupTotalBytes    int64                   `json:"backupTotalBytes,omitempty"`    // 备份文件总字节数（扁平化兼容字段）
	BackupSummarySHA256 string                  `json:"backupSummarySha256,omitempty"` // 备份汇总 SHA-256（扁平化兼容字段）
	BackupOverQuota     bool                    `json:"backupOverQuota,omitempty"`     // 备份总占用是否已超出目标配额上限（9.5 节告警）
	Timestamp           int64                   `json:"timestamp"`                     // 上报时刻秒级 Unix 时间戳
}

// DiskStats 表示指定挂载点的可用字节数与总容量字节数。
type DiskStats struct {
	FreeBytes  uint64 // 普通非特权进程可用剩余字节数
	TotalBytes uint64 // 文件系统总容量字节数
}

// JournalRecord 持久化记录单次部署/回滚/备份在本地 journal.json 中的执行轨迹与终态上报确认情况。
type JournalRecord struct {
	MessageID     string                `json:"messageId"`               // 关联信封消息 ID
	MessageSeq    uint64                `json:"messageSeq"`              // 关联信封序号
	CommandType   string                `json:"commandType"`             // 指令类型
	DeploymentID  string                `json:"deploymentId"`            // 部署记录唯一标识
	ReleaseID     uint64                `json:"releaseId,omitempty"`     // 版本 ID
	TargetVersion string                `json:"targetVersion,omitempty"` // 目标升级版本号
	PrevVersion   string                `json:"prevVersion,omitempty"`   // 升级前记录的旧版本号
	CurrentStep   string                `json:"currentStep"`             // 当前或最终停驻的步骤名
	Status        string                `json:"status"`                  // 当前状态
	Terminal      bool                  `json:"terminal"`                // 是否已进入终态
	ReportAcked   bool                  `json:"reportAcked"`             // 终态报告是否已被 Server 成功确认接收
	LastReport    *DeployReport         `json:"lastReport,omitempty"`    // 最近一次上报载荷（供进程重启后补报）
	RollbackTkt   *envelope.ActionTicket `json:"rollbackTicket,omitempty"` // 升级中途重启恢复时可用的签名回滚票据
	UpdatedAt     int64                 `json:"updatedAt"`               // 最后更新秒级时间戳
}

// Journal 是持久化在 <stateDir>/journal.json 的常驻进程本地状态日志根结构。
type Journal struct {
	LastAckSeq        uint64                   `json:"lastAckSeq"`                  // 已处理完成的最大指令序号
	LastKnownVersion  string                   `json:"lastKnownVersion,omitempty"`  // 最近探测到的产品运行版本号
	ProcessedMessages map[string]string        `json:"processedMessages"`           // messageId -> deploymentId 幂等表
	Deployments       map[string]JournalRecord `json:"deployments"`                 // deploymentId -> 最近状态记录表
}
