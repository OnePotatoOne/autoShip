// Package model 定义产品版本（Release）与签名制品清单的数据表模型（4.1、8.10 与 11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(&Release{}, &ReleaseArtifact{})
}

// Release 表示一次产品的正式发布版本，独立维护状态：publishing(发布中)、publish_failed(发布失败)、pending_deploy(待部署)、deployed(已部署)、superseded(已取代)。
type Release struct {
	ID                 uint64     `gorm:"primaryKey;autoIncrement;comment:版本主键ID" json:"id"`
	ProductID          uint64     `gorm:"uniqueIndex:uk_prod_ver;not null;comment:所属产品ID" json:"productId"`
	Version            string     `gorm:"uniqueIndex:uk_prod_ver;size:48;not null;comment:语义化版本号（如v1.2.0）" json:"version"`
	TagName            string     `gorm:"size:64;not null;comment:Git标签名" json:"tagName"`
	WorkItemID         uint64     `gorm:"index;not null;default:0;comment:来源开发任务ID（阶段1手动发布工具上传时可为0）" json:"workItemId"`
	RequirementIDsJSON string     `gorm:"type:text;comment:本版本包含的需求ID列表JSON" json:"requirementIdsJson"`
	CommitSHA          string     `gorm:"size:64;not null;default:'';comment:发布Tag对应的主分支提交完整哈希" json:"commitSha"`
	Changelog          string     `gorm:"type:mediumtext;comment:本版本变更日志小节正文" json:"changelog"`
	MigrationRisk      string     `gorm:"size:32;not null;default:'none';comment:数据库迁移风险等级（none/additive/data_rewrite/destructive）" json:"migrationRisk"`
	MinAgentVersion    string     `gorm:"size:48;not null;default:'v0.1.0';comment:要求的最低更新Agent版本" json:"minAgentVersion"`
	CIRunURL           string     `gorm:"size:255;not null;default:'';comment:产品CI工作流运行详情链接" json:"ciRunUrl"`
	TagPushed          bool       `gorm:"not null;default:false;comment:Git标签是否已推送至远端仓库（决定失败后是退回原任务还是新建修复任务）" json:"tagPushed"`
	Status             string     `gorm:"size:32;not null;default:'publishing';index;comment:版本状态（publishing/publish_failed/pending_deploy/deployed/superseded）" json:"status"`
	FailureReason      string     `gorm:"type:text;comment:发布流水线失败原因与CI错误摘要" json:"failureReason,omitempty"`
	FixWorkItemID      uint64     `gorm:"not null;default:0;comment:发布失败后一键新建的关联修复开发任务ID" json:"fixWorkItemId"`
	ManifestJSON       string     `gorm:"type:mediumtext;comment:经Ed25519私钥签名的完整制品清单JSON" json:"manifestJson,omitempty"`
	SignatureB64       string     `gorm:"size:255;not null;default:'';comment:制品清单Ed25519数字签名Base64" json:"signatureB64"`
	PublishedAt        *time.Time `gorm:"comment:制品验签上传完成进入待部署的时间" json:"publishedAt,omitempty"`
	DeployedAt         *time.Time `gorm:"comment:全部启用目标部署成功的时间" json:"deployedAt,omitempty"`
	CreatedBy          string     `gorm:"size:64;not null;default:'system';comment:触发发布人" json:"createdBy"`
	CreatedAt          time.Time  `gorm:"index;not null;comment:版本记录创建时间" json:"createdAt"`
	UpdatedAt          time.Time  `gorm:"not null;comment:版本更新时间" json:"updatedAt"`
}

// TableName 返回版本主表物理表名。
func (Release) TableName() string { return "releases" }

// TableComment 返回版本主表物理表中文注释。
func (Release) TableComment() string { return "产品发布版本状态、变更日志与签名清单主表" }

// ReleaseArtifact 记录版本对应的落地 .tar.gz 制品包路径、大小与 SHA-256 校验值。
type ReleaseArtifact struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;comment:制品记录主键ID" json:"id"`
	ProductID   uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	ReleaseID   uint64    `gorm:"uniqueIndex;not null;comment:关联版本ID" json:"releaseId"`
	FileName    string    `gorm:"size:255;not null;comment:下载制品包文件名" json:"fileName"`
	StoragePath string    `gorm:"size:512;not null;comment:制品库本地相对磁盘路径" json:"-"`
	SizeBytes   int64     `gorm:"not null;comment:制品包总字节数" json:"sizeBytes"`
	SHA256      string    `gorm:"size:64;not null;comment:制品包整包SHA256摘要" json:"sha256"`
	CreatedAt   time.Time `gorm:"not null;comment:入库时间" json:"createdAt"`
}

// TableName 返回版本制品物理表名。
func (ReleaseArtifact) TableName() string { return "release_artifacts" }

// TableComment 返回版本制品物理表中文注释。
func (ReleaseArtifact) TableComment() string { return "版本签名制品包磁盘文件元数据表" }
