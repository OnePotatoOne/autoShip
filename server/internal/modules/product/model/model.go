// Package model 定义产品档案、模块表、范围规则、权限档案及产品知识的数据表模型（对应设计文档第 6、8.3、8.5、11 节）。
package model

import (
	"time"

	"autoship/internal/database"
)

func init() {
	database.RegisterModels(
		&Product{},
		&ProductModule{},
		&ProductScopeRule{},
		&ProductPermissionProfile{},
		&ProductKnowledge{},
	)
}

// Product 表示接入 autoShip 的一个业务产品档案，定义其代码仓库、构建/测试/检查命令、预览配方、发布配方与签名公钥。
type Product struct {
	ID                  uint64    `gorm:"primaryKey;autoIncrement;comment:产品主键ID" json:"id"`
	Code                string    `gorm:"uniqueIndex;size:64;not null;comment:产品唯一英文编码（如seccenter）" json:"code"`
	Name                string    `gorm:"size:128;not null;comment:产品中文显示名称" json:"name"`
	Description         string    `gorm:"size:512;not null;default:'';comment:产品功能简介" json:"description"`
	RepoURL             string    `gorm:"size:255;not null;default:'';comment:Git代码仓库地址" json:"repoUrl"`
	DefaultBranch       string    `gorm:"size:64;not null;default:'main';comment:默认主分支名称" json:"defaultBranch"`
	InstallCmd          string    `gorm:"size:512;not null;default:'';comment:安装依赖命令" json:"installCmd"`
	BuildCmd            string    `gorm:"size:512;not null;default:'';comment:全量构建命令" json:"buildCmd"`
	TestCmd             string    `gorm:"size:512;not null;default:'';comment:全量测试命令" json:"testCmd"`
	LintCmd             string    `gorm:"size:512;not null;default:'';comment:静态检查命令" json:"lintCmd"`
	CommandTimeoutSec   int       `gorm:"not null;default:900;comment:单条构建或测试命令最大耗时上限秒数" json:"commandTimeoutSec"`
	PreviewStartCmd     string    `gorm:"size:512;not null;default:'';comment:Mac本地预览实例启动命令" json:"previewStartCmd"`
	PreviewPortCount    int       `gorm:"not null;default:1;comment:单个预览实例需分配的端口数量" json:"previewPortCount"`
	PreviewDBPrefix     string    `gorm:"size:48;not null;default:'as_preview_';comment:预览与测试数据库命名前缀" json:"previewDbPrefix"`
	PreviewSeedCmd      string    `gorm:"size:512;not null;default:'';comment:预览实例演示数据生成命令" json:"previewSeedCmd"`
	PreviewSupportsTLS  bool      `gorm:"not null;default:false;comment:预览程序是否原生支持外部TLS证书参数（不支持则由开发Agent做本地TLS代理）" json:"previewSupportsTls"`
	HealthCheckPath     string    `gorm:"size:128;not null;default:'/api/healthz';comment:免登录健康检查接口相对路径" json:"healthCheckPath"`
	ChangelogFile       string    `gorm:"size:128;not null;default:'CHANGELOG.md';comment:版本变更日志相对文件路径" json:"changelogFile"`
	DeployHookPath      string    `gorm:"size:255;not null;default:'deploy/hook.sh';comment:仓库内随制品打包的部署钩子相对路径" json:"deployHookPath"`
	ReleaseSignPubB64   string    `gorm:"size:128;not null;default:'';comment:用于核验本产品制品清单的Ed25519发布公钥" json:"releaseSignPubB64"`
	DevRulesPrompt      string    `gorm:"type:text;comment:以最高优先级注入给编码和评审模型的产品级开发规范约束" json:"devRulesPrompt"`
	IntakeEnabled       bool      `gorm:"not null;default:true;comment:是否启用需求收集能力" json:"intakeEnabled"`
	AutoDevEnabled      bool      `gorm:"not null;default:true;comment:是否启用自动开发能力" json:"autoDevEnabled"`
	AutoDeployEnabled   bool      `gorm:"not null;default:true;comment:是否启用自动部署能力" json:"autoDeployEnabled"`
	PlanConfirmDefault  bool      `gorm:"not null;default:true;comment:开工后是否默认开启方案确认门" json:"planConfirmDefault"`
	CreatedAt           time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
	UpdatedAt           time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回产品档案物理表名。
func (Product) TableName() string { return "products" }

// TableComment 返回产品档案物理表中文注释。
func (Product) TableComment() string { return "接入产品档案与构建预览发布配方表" }

// ProductModule 定义产品下的业务模块划分及其对应的目录前缀与页面路由（用于分拣归属、改动范围三区划分与页面巡检）。
type ProductModule struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;comment:模块记录主键ID" json:"id"`
	ProductID   uint64    `gorm:"uniqueIndex:uk_prod_mod;not null;comment:所属产品ID" json:"productId"`
	Code        string    `gorm:"uniqueIndex:uk_prod_mod;size:64;not null;comment:模块唯一编码（如auth、asset）" json:"code"`
	Name        string    `gorm:"size:128;not null;comment:模块中文名称" json:"name"`
	PathsJSON   string    `gorm:"type:text;comment:本模块源码与测试目录前缀列表JSON" json:"pathsJson"`
	TestCmd     string    `gorm:"size:512;not null;default:'';comment:本模块独立增量测试命令" json:"testCmd"`
	PageURLsJSON string   `gorm:"type:text;comment:本模块用于Chrome页面巡检的主路由列表JSON" json:"pageUrlsJson"`
	Description string    `gorm:"size:255;not null;default:'';comment:模块职责说明" json:"description"`
	CreatedAt   time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
	UpdatedAt   time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回产品模块物理表名。
func (ProductModule) TableName() string { return "product_modules" }

// TableComment 返回产品模块物理表中文注释。
func (ProductModule) TableComment() string { return "产品业务模块目录与巡检路由表" }

// ProductScopeRule 定义产品内的公共区目录与可选受保护路径规则（8.5 节：公共区改动触发系统性影响审计，受保护路径触发人工审批）。
type ProductScopeRule struct {
	ID          uint64    `gorm:"primaryKey;autoIncrement;comment:范围规则主键ID" json:"id"`
	ProductID   uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	ZoneType    string    `gorm:"size:32;not null;comment:区域类型（public_zone公共区/protected受保护路径）" json:"zoneType"`
	PathPattern string    `gorm:"size:255;not null;comment:目录或通配路径规则（如web/src/app/shared/**、go.mod）" json:"pathPattern"`
	Note        string    `gorm:"size:255;not null;default:'';comment:规则用途备注说明" json:"note"`
	CreatedAt   time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
	UpdatedAt   time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
}

// TableName 返回范围规则物理表名。
func (ProductScopeRule) TableName() string { return "product_scope_rules" }

// TableComment 返回范围规则物理表中文注释。
func (ProductScopeRule) TableComment() string { return "产品代码改动范围与公共区规则表" }

// ProductPermissionProfile 定义开发 Agent 在本 Mac 运行本产品任务时的系统级沙箱权限档案（8.3 节：off / protected / custom）。
type ProductPermissionProfile struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement;comment:权限档案主键ID" json:"id"`
	ProductID            uint64    `gorm:"uniqueIndex;not null;comment:所属产品ID（一对一）" json:"productId"`
	Mode                 string    `gorm:"size:24;not null;default:'protected';comment:隔离档位（off关闭/protected默认保护/custom自定义）" json:"mode"`
	AllowedWritePathsJSON string   `gorm:"type:text;comment:除worktree与缓存外允许写入的额外目录列表JSON" json:"allowedWritePathsJson"`
	ExtraDeniedReadJSON  string    `gorm:"type:text;comment:在默认敏感清单之外追加禁止读取的路径列表JSON" json:"extraDeniedReadJson"`
	LastBlockedPath      string    `gorm:"size:512;not null;default:'';comment:最近一次被沙箱拦截的具体路径（供后台一键放行）" json:"lastBlockedPath"`
	UpdatedAt            time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
	CreatedAt            time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
}

// TableName 返回权限档案物理表名。
func (ProductPermissionProfile) TableName() string { return "product_permission_profiles" }

// TableComment 返回权限档案物理表中文注释。
func (ProductPermissionProfile) TableComment() string { return "产品开发Agent沙箱权限配置档案表" }

// ProductKnowledge 存储供客服 AI 当场解答咨询、引导提问以及生成产品风格 HTML 原型的结构化产品知识（每次发布后可由项目地图自动刷新）。
type ProductKnowledge struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement;comment:知识条目主键ID" json:"id"`
	ProductID uint64    `gorm:"index;not null;comment:所属产品ID" json:"productId"`
	Category  string    `gorm:"size:32;not null;index;comment:知识分类（overview/module/faq/ui_style/project_map）" json:"category"`
	Title     string    `gorm:"size:128;not null;comment:知识条目标题" json:"title"`
	Content   string    `gorm:"type:mediumtext;comment:知识正文内容（UTF-8 Markdown或JSON）" json:"content"`
	Source    string    `gorm:"size:32;not null;default:'manual';comment:维护来源（manual人工录入/auto_map发布自动同步）" json:"source"`
	UpdatedAt time.Time `gorm:"not null;comment:更新时间" json:"updatedAt"`
	CreatedAt time.Time `gorm:"not null;comment:创建时间" json:"createdAt"`
}

// TableName 返回产品知识物理表名。
func (ProductKnowledge) TableName() string { return "product_knowledge" }

// TableComment 返回产品知识物理表中文注释。
func (ProductKnowledge) TableComment() string { return "产品领域知识库与界面风格规范表" }
