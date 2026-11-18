// Package model 定义部署目标（DeployTarget）、部署执行记录（Deployment）、部署分步日志（DeploymentStep）及数据库备份记录（DBBackup）表模型（4.1、9.4、9.5 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(
		&DeployTarget{},
		&Deployment{},
		&DeploymentStep{},
		&DBBackup{},
	)
}

// DeployTarget 表示产品的一个具体生产或预发服务器部署目标，配置固定健康检查地址、数据库备份存储上限及保留份数。
type DeployTarget struct {
	ID                uint64    `gorm:"primaryKey;autoIncrement;comment:部署目标主键ID" json:"id"`
	ProductID         uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	Name              string    `gorm:"size:128;not null;comment:部署目标名称（如生产主节点1）" json:"name"`
	Environment       string    `gorm:"size:32;not null;default:'prod';comment:环境标识（prod/staging）" json:"environment"`
	ServerIP          string    `gorm:"size:64;not null;default:'';comment:目标服务器公网或内网标识IP" json:"serverIp"`
	HealthCheckURL    string    `gorm:"size:255;not null;default:'http://127.0.0.1:8080/api/healthz';comment:更新Agent在本机探测的免登录健康检查地址" json:"healthCheckUrl"`
	BackupQuotaMB     int64     `gorm:"not null;default:10240;comment:本目标数据库备份专用目录最大存储上限MB（超限触发告警）" json:"backupQuotaMb"`
	BackupRetainCount int       `gorm:"not null;default:5;comment:建议保留备份份数说明" json:"backupRetainCount"`
	CurrentVersion    string    `gorm:"size:48;not null;default:'';comment:目标当前运行的产品版本号" json:"currentVersion"`
	BackupUsedBytes   int64     `gorm:"not null;default:0;comment:最近心跳上报的备份目录实际占用字节数" json:"backupUsedBytes"`
	QuotaExceeded     bool      `gorm:"-" json:"quotaExceeded"`
	Enabled           bool      `gorm:"not null;default:true;index;comment:是否启用该部署目标（产品下所有启用目标均成功时版本才流转为已部署）" json:"enabled"`
	CreatedAt         time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
	UpdatedAt         time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回部署目标物理表名。
func (DeployTarget) TableName() string { return "deploy_targets" }

// TableComment 返回部署目标物理表中文注释。
func (DeployTarget) TableComment() string { return "产品服务器部署目标与备份配额配置表" }

// Deployment 表示一次版本向单个部署目标下发的升级或回滚记录，状态维护：pending_confirm(待确认)、backing_up(备份中)、deploying(部署中)、succeeded(成功)、rolled_back(失败已回滚)、rollback_failed(回滚失败)。
type Deployment struct {
	ID              uint64     `gorm:"primaryKey;autoIncrement;comment:部署主键ID" json:"id"`
	DeploymentUID   string     `gorm:"uniqueIndex;size:64;not null;comment:传递给autoship-apply与部署钩子的唯一安全部署标识ID" json:"deploymentUid"`
	ProductID       uint64     `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	ReleaseID       uint64     `gorm:"index;not null;comment:关联发布版本ID" json:"releaseId"`
	TargetID        uint64     `gorm:"index;not null;comment:目标部署服务器ID" json:"targetId"`
	TargetVersion   string     `gorm:"size:48;not null;comment:本次部署目标版本号" json:"targetVersion"`
	PreviousVersion string     `gorm:"size:48;not null;default:'';comment:升级前目标运行版本号" json:"previousVersion"`
	BackupDB        bool       `gorm:"not null;default:false;comment:本次部署前是否要求先执行数据库备份" json:"backupDb"`
	Status          string     `gorm:"size:32;not null;default:'pending_confirm';index;comment:部署状态（pending_confirm/backing_up/deploying/succeeded/rolled_back/rollback_failed）" json:"status"`
	FailedStep      string     `gorm:"size:64;not null;default:'';comment:故障发生步骤名称" json:"failedStep,omitempty"`
	ExitCode        int        `gorm:"not null;default:0;comment:故障子命令退出码" json:"exitCode"`
	ErrorTail       string     `gorm:"type:text;comment:脱敏后的错误输出末尾" json:"errorTail,omitempty"`
	LogTail         string     `gorm:"type:mediumtext;comment:脱敏后的服务启动日志末尾" json:"logTail,omitempty"`
	HealthResponse  string     `gorm:"type:text;comment:上线健康检查接口最后一次原始响应" json:"healthResponse,omitempty"`
	ConfirmedBy     string     `gorm:"size:64;not null;default:'';comment:点击确认部署或回滚的管理员" json:"confirmedBy"`
	StartedAt       *time.Time `gorm:"comment:开始下发执行时间" json:"startedAt,omitempty"`
	FinishedAt      *time.Time `gorm:"comment:执行终态结束时间" json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `gorm:"index;not null;comment:记录创建时间" json:"createdAt"`
	UpdatedAt       time.Time  `gorm:"not null;comment:记录更新时间" json:"updatedAt"`
}

// TableName 返回部署记录物理表名。
func (Deployment) TableName() string { return "deployments" }

// TableComment 返回部署记录物理表中文注释。
func (Deployment) TableComment() string { return "版本生产升级与回滚执行记录主表" }

// DeploymentStep 记录单次部署在 9.4 节各固定步骤（验签、预检、续传下载、数据库备份、安装升级、上线健康检查、回滚、清理）的分步进度与输出。
type DeploymentStep struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement;comment:分步日志主键ID" json:"id"`
	ProductID    uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	DeploymentID uint64    `gorm:"uniqueIndex:uk_dep_step;not null;comment:关联部署记录主键ID" json:"deploymentId"`
	StepName     string    `gorm:"uniqueIndex:uk_dep_step;size:64;not null;comment:固定步骤编码" json:"stepName"`
	Status       string    `gorm:"size:24;not null;default:'running';comment:步骤状态（running/succeeded/failed）" json:"status"`
	Message      string    `gorm:"size:512;not null;default:'';comment:步骤进度说明摘要" json:"message"`
	UpdatedAt    time.Time `gorm:"not null;comment:步骤更新时间" json:"updatedAt"`
	CreatedAt    time.Time `gorm:"not null;comment:步骤开始时间" json:"createdAt"`
}

// TableName 返回部署分步日志物理表名。
func (DeploymentStep) TableName() string { return "deployment_steps" }

// TableComment 返回部署分步日志物理表中文注释。
func (DeploymentStep) TableComment() string { return "部署过程分步执行状态与日志表" }

// DBBackup 记录由产品部署钩子 `backup-db` 导出到专用目录（如 `/var/backups/autoship/<产品>/<部署ID>/`）的数据库备份清单、大小与恢复提示（9.5 节）。
type DBBackup struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement;comment:数据库备份主键ID" json:"id"`
	ProductID     uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	TargetID      uint64    `gorm:"index;not null;comment:所属部署目标ID" json:"targetId"`
	DeploymentID  uint64    `gorm:"index;not null;default:0;comment:关联部署记录ID（手动触发备份时可为0）" json:"deploymentId"`
	DeploymentUID string    `gorm:"size:64;not null;index;comment:关联安全部署标识ID" json:"deploymentUid"`
	BackupDir     string    `gorm:"size:512;not null;comment:目标服务器root专属备份目录绝对路径" json:"backupDir"`
	FileListJSON  string    `gorm:"type:text;comment:导出备份文件清单JSON" json:"fileListJson"`
	SizeBytes     int64     `gorm:"not null;default:0;comment:本次备份总字节大小" json:"sizeBytes"`
	Checksum      string    `gorm:"size:128;not null;default:'';comment:备份归档校验和" json:"checksum"`
	RestoreGuide  string    `gorm:"size:512;not null;default:'请以root登录目标服务器，按备份目录内SQL/归档手动校核恢复数据库';comment:回滚后人工数据恢复说明" json:"restoreGuide"`
	CreatedAt     time.Time `gorm:"index;not null;comment:备份完成时间" json:"createdAt"`
}

// TableName 返回数据库备份记录物理表名。
func (DBBackup) TableName() string { return "db_backups" }

// TableComment 返回数据库备份记录物理表中文注释。
func (DBBackup) TableComment() string { return "生产升级前数据库导出备份清单与恢复指引表" }
