// Package config 定义 Mac 开发 Agent（autoship-dev-agent）的全局配置、模型通道配置、产品档案运行时契约及默认值初始化逻辑。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"autoship/pkg/envelope"
)

// AgentConfig 定义 Mac 开发 Agent 的顶层 YAML 配置结构（对应设计文档 7.5、7.6、8.1、8.2 节）。
type AgentConfig struct {
	AgentID            string             `yaml:"agentId" json:"agentId"`                       // 开发 Agent 在 Server 侧登记的唯一标识
	ServerURL          string             `yaml:"serverUrl" json:"serverUrl"`                   // Server tailnet HTTPS 基地址（如 https://100.64.0.10:8444）
	ServerWSPath       string             `yaml:"serverWsPath" json:"serverWsPath"`             // 指令与事件 WSS 路径（默认 /agent/v1/dev/ws）
	CAFingerprint      string             `yaml:"caFingerprint" json:"caFingerprint"`           // autoShip 根 CA 的 64 位 SHA-256 指纹
	BootstrapToken     string             `yaml:"bootstrapToken" json:"bootstrapToken"`         // 首次注册用的一次性令牌（注册完成后可清空）
	ServerPublicKeyB64 string             `yaml:"serverPublicKeyB64" json:"serverPublicKeyB64"` // Server 请求模型中继接口时用于验签的 Ed25519 公钥
	TailnetIP          string             `yaml:"tailnetIp" json:"tailnetIp"`                   // 本 Mac 的 tailnet IP（用于模型中继与预览监听及证书 SAN）
	RootDir            string             `yaml:"rootDir" json:"rootDir"`                       // 工作根目录（默认 ~/autoship，隔离于个人工作目录之外）
	MaxParallelTasks   int                `yaml:"maxParallelTasks" json:"maxParallelTasks"`     // 最大并行开发任务数（16GB 内存默认 1）
	KeychainService    string             `yaml:"keychainService" json:"keychainService"`       // macOS 钥匙串默认服务命名空间
	HTTPProxy          string             `yaml:"httpProxy" json:"httpProxy"`                   // 访问 GitHub / 依赖源时的可选本地 HTTP/SOCKS 代理地址
	ChromeBinaryPath   string              `yaml:"chromeBinaryPath" json:"chromeBinaryPath"`     // 本机已安装 Google Chrome 可执行文件路径（留空自动探测）
	FeishuWebhookURL   string              `yaml:"feishuWebhookUrl" json:"feishuWebhookUrl"`     // 飞书自定义机器人 Webhook 地址（ask_owner 通知用）
	Engine             EngineBackendConfig `yaml:"engine" json:"engine"`                         // 开发任务编码执行器配置（支持 builtin 自研引擎或 agy Antigravity CLI）
	Relay              RelayConfig         `yaml:"relay" json:"relay"`                           // 模型受限中继服务配置（7.5 节）
	Models             ModelsConfig       `yaml:"models" json:"models"`                         // 本地/备选大模型通道池与任务预算配置（8.1 & 8.4 节）
	WorkspaceDB        TestDBAdminConfig  `yaml:"workspaceDb" json:"workspaceDb"`               // 测试库与预览库动态创建/销毁的管理连接配置（8.2 节）
	StateDB            LocalStateDBConfig `yaml:"stateDb" json:"stateDb"`                       // Agent 自身独立状态库 autoship_agent 配置（8.1 节）
	PortPool           PortPoolConfig     `yaml:"portPool" json:"portPool"`                     // 测试与预览实例端口池范围
	Preview            PreviewGlobalCfg   `yaml:"preview" json:"preview"`                       // 预览实例全局生命周期配置
	Products           []ProductProfile   `yaml:"products" json:"products"`                     // 本地静态兜底产品档案列表（运行时可由 Server 指令动态覆盖）
}

// EngineBackendConfig 定义开发任务底层编码引擎模式与本机 Antigravity CLI (`agy`) 集成参数。
type EngineBackendConfig struct {
	Driver         string `yaml:"driver" json:"driver"`                 // 编码驱动：builtin（内置自研会话引擎）或 agy（调用本机 Antigravity CLI 执行研发）
	AgyBinaryPath  string `yaml:"agyBinaryPath" json:"agyBinaryPath"`   // agy CLI 可执行文件路径（留空自动探测 ~/.local/bin/agy 与 PATH）
	AgyModel       string `yaml:"agyModel" json:"agyModel"`             // 可选指定 Antigravity 会话模型名（留空使用 agy 默认配置）
	AgyEffort      string `yaml:"agyEffort" json:"agyEffort"`           // 可选推理强度（low/medium/high/xhigh/max，留空使用默认）
	AgyAutoApprove *bool  `yaml:"agyAutoApprove" json:"agyAutoApprove"` // 无人值守研发模式下是否自动放行 agy 工具调用（nil 时默认 true，由 autoShip 外层隔离 worktree 与门禁把关）
	AgySandbox     bool   `yaml:"agySandbox" json:"agySandbox"`         // 是否叠加启用 agy 自身的 --sandbox 标志（默认 false）
}

// RelayConfig 定义开发 Agent 在 tailnet 地址上暴露的受限模型中继服务配置（7.5 节）。
type RelayConfig struct {
	Enabled               bool                        `yaml:"enabled" json:"enabled"`                             // 是否启用模型中继监听
	ListenAddr            string                      `yaml:"listenAddr" json:"listenAddr"`                       // 监听地址（必须绑定 tailnet IP 或回环地址，如 127.0.0.1:8445）
	CertFile              string                      `yaml:"certFile" json:"certFile"`                           // 服务端 TLS 证书路径（默认 <rootDir>/pki/agent.crt）
	KeyFile               string                      `yaml:"keyFile" json:"keyFile"`                             // 服务端 TLS 私钥路径（默认 <rootDir>/pki/agent.key）
	MaxTokensLimit        int                         `yaml:"maxTokensLimit" json:"maxTokensLimit"`               // 单次中继请求强制截断的 max_tokens 上限（默认 4096）
	RequestsPerMinute     int                         `yaml:"requestsPerMinute" json:"requestsPerMinute"`         // 整体每分钟最大请求数限流（默认 60）
	Burst                 int                         `yaml:"burst" json:"burst"`                                 // 令牌桶突发容量（默认 15）
	IntakeConcurrency     int                         `yaml:"intakeConcurrency" json:"intakeConcurrency"`         // 客服对话（intake 角色）专属高优并发槽数量（默认 4）
	BackgroundConcurrency int                         `yaml:"backgroundConcurrency" json:"backgroundConcurrency"` // 分拣等后台低优请求并发槽数量（默认 2）
	RoleModels            map[string]RoleTargetConfig `yaml:"roleModels" json:"roleModels"`                       // 逻辑角色（intake/triage）到本地 Profile 与具体模型名的映射
}

// RoleTargetConfig 描述单个逻辑角色映射到的本地模型通道 Profile 与模型名称。
type RoleTargetConfig struct {
	ProfileName string `yaml:"profileName" json:"profileName"` // 对应 Models.Profiles 中的名称
	ModelName   string `yaml:"modelName" json:"modelName"`     // 转发给上游接口的实际模型名称
}

// ModelsConfig 定义开发引擎使用的多模型 Profile 列表、角色映射及默认单任务预算。
type ModelsConfig struct {
	Profiles      []ModelProfile      `yaml:"profiles" json:"profiles"`           // 按优先级排序的模型通道列表（失败时顺序故障转移）
	RoleMapping   map[string][]string `yaml:"roleMapping" json:"roleMapping"`     // 引擎内部角色（coder/reviewer/auditor/changelog）对应的 Profile 顺序
	DefaultBudget TaskBudget          `yaml:"defaultBudget" json:"defaultBudget"` // 单个开发任务的默认 Token、时长与轮数上限
}

// ModelProfile 描述单个 OpenAI Chat Completions 兼容的模型接口配置（8.1 节）。
type ModelProfile struct {
	Name            string `yaml:"name" json:"name"`                       // Profile 唯一名称（如 local-hub）
	BaseURL         string `yaml:"baseUrl" json:"baseUrl"`                 // 接口基地址（默认 http://127.0.0.1:8788/v1）
	Model           string `yaml:"model" json:"model"`                     // 默认模型名（默认 deepseek-v4.1-flash）
	Format          string `yaml:"format" json:"format"`                   // 协议格式（固定兼容 openai）
	KeychainAccount string `yaml:"keychainAccount" json:"keychainAccount"` // 存储在钥匙串中的 API Key 条目名
	APIKey          string `yaml:"apiKey" json:"apiKey"`                   // 仅用于本地测试或环境变量注入的可选密钥字段（生产存钥匙串）
	TimeoutSeconds  int    `yaml:"timeoutSeconds" json:"timeoutSeconds"`   // 单次 HTTP 调用超时秒数（默认 180）
	MaxRetries      int    `yaml:"maxRetries" json:"maxRetries"`           // 当前 Profile 遇到瞬时错误的退避重试次数（默认 2）
	SupportsVision  *bool  `yaml:"supportsVision" json:"supportsVision"`   // 是否支持多模态图片输入（nil 表示未探测或按需探测）
}

// TaskBudget 定义单个开发任务的资源消耗硬上限（8.4 节）。
type TaskBudget struct {
	MaxTokens          int `yaml:"maxTokens" json:"maxTokens"`                   // 单任务累计最大 Token 数（默认 500000）
	MaxDurationMinutes int `yaml:"maxDurationMinutes" json:"maxDurationMinutes"` // 单任务最大执行分钟数（默认 60）
	MaxTurns           int `yaml:"maxTurns" json:"maxTurns"`                     // 单次编码会话最大模型交互轮数（默认 80）
}

// TestDBAdminConfig 定义用于按产品前缀创建和清理测试库/预览库的本地 MySQL 连接配置（8.2 节）。
type TestDBAdminConfig struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`                 // 是否启用本地 MySQL 测试库动态生命周期管理
	Host            string `yaml:"host" json:"host"`                       // MySQL 主机地址（默认 127.0.0.1）
	Port            int    `yaml:"port" json:"port"`                       // MySQL 端口（默认 3306）
	AdminUser       string `yaml:"adminUser" json:"adminUser"`             // 仅用于建库/删库的管理账号（与 Agent 状态库隔离）
	KeychainAccount string `yaml:"keychainAccount" json:"keychainAccount"` // 管理账号密码在钥匙串中的条目名
	Password        string `yaml:"password" json:"password"`               // 测试注入密码（生产优先走钥匙串）
}

// LocalStateDBConfig 定义开发 Agent 自身状态持久化使用的独立本地 MySQL 库配置（8.1 节：与测试库账号分开）。
type LocalStateDBConfig struct {
	Enabled         bool   `yaml:"enabled" json:"enabled"`                 // 是否启用本地 MySQL autoship_agent 状态库
	Host            string `yaml:"host" json:"host"`                       // MySQL 主机地址（默认 127.0.0.1）
	Port            int    `yaml:"port" json:"port"`                       // MySQL 端口（默认 3306）
	User            string `yaml:"user" json:"user"`                       // Agent 状态库专用账号（严禁暴露给被测程序或大模型）
	KeychainAccount string `yaml:"keychainAccount" json:"keychainAccount"` // 状态库密码在钥匙串中的条目名
	Password        string `yaml:"password" json:"password"`               // 测试注入密码
	DBName          string `yaml:"dbName" json:"dbName"`                   // 状态库名称（默认 autoship_agent）
}

// PortPoolConfig 定义分配给本地测试服务与预览实例的动态 TCP 端口池区间。
type PortPoolConfig struct {
	StartPort int `yaml:"startPort" json:"startPort"` // 起始端口号（含，默认 19000）
	EndPort   int `yaml:"endPort" json:"endPort"`     // 结束端口号（含，默认 19200）
}

// PreviewGlobalCfg 定义预览实例的全局空闲回收参数（7.6 节）。
type PreviewGlobalCfg struct {
	IdleTimeoutHours int `yaml:"idleTimeoutHours" json:"idleTimeoutHours"` // 预览无访问自动停止的小时数（默认 24）
}

// CommandSpec 描述产品档案中单条构建、测试或检查命令及其超时限制（第 6 节）。
type CommandSpec struct {
	Cmd            string `yaml:"cmd" json:"cmd"`                       // Shell 命令字符串
	TimeoutSeconds int    `yaml:"timeoutSeconds" json:"timeoutSeconds"` // 命令最长允许运行秒数（默认 600）
}

// SandboxPolicyConfig 定义单个产品的 macOS 权限档案策略（8.3 节）。
type SandboxPolicyConfig struct {
	Mode             string   `yaml:"mode" json:"mode"`                         // 档位：off | protected（默认） | custom
	ExtraWritePaths  []string `yaml:"extraWritePaths" json:"extraWritePaths"`   // 产品声明的额外可写目录
	CustomAllowRead  []string `yaml:"customAllowRead" json:"customAllowRead"`   // custom 档位下显式放行读取的路径（含后台一键放行项）
	CustomDenyRead   []string `yaml:"customDenyRead" json:"customDenyRead"`     // custom 档位下额外禁止读取的路径
	CustomAllowWrite []string `yaml:"customAllowWrite" json:"customAllowWrite"` // custom 档位下显式放行写入的路径（含后台一键放行项）
	CustomDenyWrite  []string `yaml:"customDenyWrite" json:"customDenyWrite"`   // custom 档位下额外禁止写入的路径
}

// PreviewRecipeConfig 定义产品预览实例的启动配方与 TLS 接入方式（第 6 节 & 7.6 节）。
type PreviewRecipeConfig struct {
	StartCmd            string   `yaml:"startCmd" json:"startCmd"`                       // 启动候选预览服务的命令
	PortCount           int      `yaml:"portCount" json:"portCount"`                     // 需要的端口数量（默认 1）
	SupportsExternalTLS bool     `yaml:"supportsExternalTls" json:"supportsExternalTls"` // 产品自身是否支持直接加载外部 TLS 证书文件
	CertEnvVar          string   `yaml:"certEnvVar" json:"certEnvVar"`                   // 注入外部证书路径的环境变量名（如 TLS_CERT_FILE）
	KeyEnvVar           string   `yaml:"keyEnvVar" json:"keyEnvVar"`                     // 注入外部私钥路径的环境变量名（如 TLS_KEY_FILE）
	PortEnvVar          string   `yaml:"portEnvVar" json:"portEnvVar"`                   // 注入监听端口的环境变量名（默认 PORT）
	DBDSNEnvVar         string   `yaml:"dbDsnEnvVar" json:"dbDsnEnvVar"`                 // 注入预览数据库 DSN 的环境变量名（默认 DATABASE_DSN）
	DBPrefix            string   `yaml:"dbPrefix" json:"dbPrefix"`                       // 测试库与预览库的命名前缀（默认取产品编码下划线化）
	SeedCmd             string   `yaml:"seedCmd" json:"seedCmd"`                         // 生成演示数据（非生产数据）的命令
	HealthPath          string   `yaml:"healthPath" json:"healthPath"`                   // 免登录健康检查相对路径（如 /healthz）
	PagesToInspect      []string `yaml:"pagesToInspect" json:"pagesToInspect"`           // CDP 页面巡检需要访问的相对路径列表
}

// ReleaseRecipeConfig 定义产品自动发布与 CI 制品提取配方（第 6 节 & 8.10 节）。
type ReleaseRecipeConfig struct {
	ChangelogFile     string   `yaml:"changelogFile" json:"changelogFile"`         // 变更日志相对路径（默认 CHANGELOG.md）
	PreMergeChecks    []string `yaml:"preMergeChecks" json:"preMergeChecks"`       // 合并主分支前在任务分支上执行的检查命令列表
	CIWorkflowName    string   `yaml:"ciWorkflowName" json:"ciWorkflowName"`       // GitHub Actions 发布工作流名称（可为空表示匹配该 SHA 全部运行）
	ArtifactAssetName string   `yaml:"artifactAssetName" json:"artifactAssetName"` // CI 产出的目标制品文件名或匹配名
	HookRepoPath      string   `yaml:"hookRepoPath" json:"hookRepoPath"`           // 仓库内产品部署钩子相对路径（如 deploy/ctl.sh）
	MinAgentVersion   string   `yaml:"minAgentVersion" json:"minAgentVersion"`     // 要求最低更新 Agent 版本号（默认 v0.1.0）
}

// ProductProfile 完整描述单个接入“自动开发 / 自动发布”能力的产品档案（第 6 节）。
type ProductProfile struct {
	ProductCode      string              `yaml:"productCode" json:"productCode"`           // 产品唯一标识编码（如 seccenter）
	Name             string              `yaml:"name" json:"name"`                         // 产品显示名称
	RepoURL          string              `yaml:"repoUrl" json:"repoUrl"`                   // Git 仓库克隆地址
	DefaultBranch    string              `yaml:"defaultBranch" json:"defaultBranch"`       // 主分支名（默认 main）
	DeployKeyPath    string              `yaml:"deployKeyPath" json:"deployKeyPath"`       // 仅对该仓库有效的 SSH Deploy Key 私钥路径
	GitHubOwner      string              `yaml:"githubOwner" json:"githubOwner"`           // GitHub 仓库所属组织或用户名
	GitHubRepo       string              `yaml:"githubRepo" json:"githubRepo"`             // GitHub 仓库名
	GitHubPATAccount string              `yaml:"githubPatAccount" json:"githubPatAccount"` // 细粒度 GitHub PAT 在钥匙串中的账号键名
	InstallCmd       CommandSpec         `yaml:"installCmd" json:"installCmd"`             // 安装依赖命令
	BuildCmd         CommandSpec         `yaml:"buildCmd" json:"buildCmd"`                 // 全量编译命令
	TestCmd          CommandSpec         `yaml:"testCmd" json:"testCmd"`                   // 全量测试命令
	LintCmd          CommandSpec         `yaml:"lintCmd" json:"lintCmd"`                   // 静态检查命令
	Modules          map[string][]string `yaml:"modules" json:"modules"`                   // 模块名 -> 模块相对目录前缀切片
	PublicZoneDirs   []string            `yaml:"publicZoneDirs" json:"publicZoneDirs"`     // 公共区目录或文件前缀列表（改动触发系统性影响审计）
	ProtectedPaths   []string            `yaml:"protectedPaths" json:"protectedPaths"`     // 可选受保护路径列表（改动须审批）
	CodingRules      []string            `yaml:"codingRules" json:"codingRules"`           // 注入最高优先级约束块的开发规范条目
	DBTestUser       string              `yaml:"dbTestUser" json:"dbTestUser"`             // 该产品专用的测试库数据库用户名
	DBTestPassword   string              `yaml:"dbTestPassword" json:"dbTestPassword"`     // 该产品测试库密码（或由钥匙串读取）
	Sandbox          SandboxPolicyConfig `yaml:"sandbox" json:"sandbox"`                   // 该产品的 macOS 权限档案配置
	Preview          PreviewRecipeConfig `yaml:"preview" json:"preview"`                   // 预览实例启动配方
	Release          ReleaseRecipeConfig `yaml:"release" json:"release"`                   // 发布流水线配方
}

// Default 返回带安全默认值的开发 Agent 配置对象（默认连接本机 127.0.0.1:8788/v1 的 deepseek-v4.1-flash）。
// 无入参；返回初始化完毕的 *AgentConfig，供 Load 或单元测试调用。
func Default() *AgentConfig {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		home = os.TempDir()
	}
	rootDir := filepath.Join(home, "autoship")
	return &AgentConfig{
		AgentID:          "mac-dev-agent-01",
		ServerURL:        "https://127.0.0.1:8444",
		ServerWSPath:     "/agent/v1/dev/ws",
		TailnetIP:        "127.0.0.1",
		RootDir:          rootDir,
		MaxParallelTasks: 1,
		KeychainService:  "autoship.devagent",
		Engine: EngineBackendConfig{
			Driver: "builtin",
		},
		Relay: RelayConfig{
			Enabled:               true,
			ListenAddr:            "127.0.0.1:8445",
			CertFile:              filepath.Join(rootDir, "pki", "agent.crt"),
			KeyFile:               filepath.Join(rootDir, "pki", "agent.key"),
			MaxTokensLimit:        4096,
			RequestsPerMinute:     60,
			Burst:                 15,
			IntakeConcurrency:     4,
			BackgroundConcurrency: 2,
			RoleModels: map[string]RoleTargetConfig{
				"intake": {ProfileName: "local-hub", ModelName: "deepseek-v4.1-flash"},
				"triage": {ProfileName: "local-hub", ModelName: "deepseek-v4.1-flash"},
			},
		},
		Models: ModelsConfig{
			Profiles: []ModelProfile{
				{
					Name:            "local-hub",
					BaseURL:         "http://127.0.0.1:8788/v1",
					Model:           "deepseek-v4.1-flash",
					Format:          "openai",
					KeychainAccount: "llm.local-hub",
					TimeoutSeconds:  180,
					MaxRetries:      2,
				},
			},
			RoleMapping: map[string][]string{
				"coder":     {"local-hub"},
				"reviewer":  {"local-hub"},
				"auditor":   {"local-hub"},
				"changelog": {"local-hub"},
			},
			DefaultBudget: TaskBudget{
				MaxTokens:          500000,
				MaxDurationMinutes: 60,
				MaxTurns:           80,
			},
		},
		WorkspaceDB: TestDBAdminConfig{
			Enabled:         false,
			Host:            "127.0.0.1",
			Port:            3306,
			AdminUser:       "autoship_test_admin",
			KeychainAccount: "db.workspace_admin",
		},
		StateDB: LocalStateDBConfig{
			Enabled:         false,
			Host:            "127.0.0.1",
			Port:            3306,
			User:            "autoship_agent_state",
			KeychainAccount: "db.agent_state",
			DBName:          "autoship_agent",
		},
		PortPool: PortPoolConfig{
			StartPort: 19000,
			EndPort:   19200,
		},
		Preview: PreviewGlobalCfg{
			IdleTimeoutHours: 24,
		},
	}
}

// ExpandPath 将路径开头的 "~/" 展开为当前用户主目录并执行 filepath.Clean。
// 参数 rawPath 为原始配置路径；返回展开并规范化后的绝对或相对路径，供各模块解析目录时调用。
func ExpandPath(rawPath string) string {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" {
		return ""
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if trimmed == "~" {
				return filepath.Clean(home)
			}
			return filepath.Join(home, trimmed[2:])
		}
	}
	return filepath.Clean(trimmed)
}

// Load 从指定 YAML 文件加载开发 Agent 配置，自动补齐默认值并执行合法性校验。
// 参数 path 为配置文件磁盘路径；返回解析后的 *AgentConfig 及可能的文件读取/解析/校验错误。
func Load(path string) (*AgentConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取开发 Agent 配置文件 %s 失败: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析开发 Agent YAML 配置失败: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save 将 AgentConfig 以 UTF-8 编码序列化保存到目标 YAML 文件（权限 0600）。
// 参数 path 为输出文件路径，cfg 为待保存配置指针；成功返回 nil，失败返回错误。
func Save(path string, cfg *AgentConfig) error {
	if cfg == nil {
		return errors.New("配置对象不能为空")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("创建配置父目录失败: %w", err)
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化 AgentConfig 失败: %w", err)
	}
	return os.WriteFile(path, raw, 0600)
}

// ApplyDefaults 补齐用户未显式填写的缺省字段与派生目录路径。
// 无入参；直接就地修改接收者 *AgentConfig。
func (c *AgentConfig) ApplyDefaults() {
	c.RootDir = ExpandPath(c.RootDir)
	if c.RootDir == "" {
		c.RootDir = Default().RootDir
	}
	if strings.TrimSpace(c.ServerWSPath) == "" {
		c.ServerWSPath = "/agent/v1/dev/ws"
	}
	if c.MaxParallelTasks <= 0 {
		c.MaxParallelTasks = 1
	}
	if strings.TrimSpace(c.KeychainService) == "" {
		c.KeychainService = "autoship.devagent"
	}
	if strings.TrimSpace(c.Engine.Driver) == "" {
		c.Engine.Driver = "builtin"
	} else {
		c.Engine.Driver = strings.ToLower(strings.TrimSpace(c.Engine.Driver))
	}
	c.Engine.AgyBinaryPath = ExpandPath(c.Engine.AgyBinaryPath)
	if c.Engine.AgyAutoApprove == nil {
		defaultAuto := true
		c.Engine.AgyAutoApprove = &defaultAuto
	}
	if c.Relay.CertFile == "" {
		c.Relay.CertFile = filepath.Join(c.RootDir, "pki", "agent.crt")
	} else {
		c.Relay.CertFile = ExpandPath(c.Relay.CertFile)
	}
	if c.Relay.KeyFile == "" {
		c.Relay.KeyFile = filepath.Join(c.RootDir, "pki", "agent.key")
	} else {
		c.Relay.KeyFile = ExpandPath(c.Relay.KeyFile)
	}
	if c.Relay.MaxTokensLimit <= 0 {
		c.Relay.MaxTokensLimit = 4096
	}
	if c.Relay.RequestsPerMinute <= 0 {
		c.Relay.RequestsPerMinute = 60
	}
	if c.Relay.Burst <= 0 {
		c.Relay.Burst = 15
	}
	if c.Relay.IntakeConcurrency <= 0 {
		c.Relay.IntakeConcurrency = 4
	}
	if c.Relay.BackgroundConcurrency <= 0 {
		c.Relay.BackgroundConcurrency = 2
	}
	if c.Models.DefaultBudget.MaxTokens <= 0 {
		c.Models.DefaultBudget.MaxTokens = 500000
	}
	if c.Models.DefaultBudget.MaxDurationMinutes <= 0 {
		c.Models.DefaultBudget.MaxDurationMinutes = 60
	}
	if c.Models.DefaultBudget.MaxTurns <= 0 {
		c.Models.DefaultBudget.MaxTurns = 80
	}
	if c.PortPool.StartPort <= 0 {
		c.PortPool.StartPort = 19000
	}
	if c.PortPool.EndPort < c.PortPool.StartPort {
		c.PortPool.EndPort = c.PortPool.StartPort + 200
	}
	if c.Preview.IdleTimeoutHours <= 0 {
		c.Preview.IdleTimeoutHours = 24
	}
	if strings.TrimSpace(c.StateDB.DBName) == "" {
		c.StateDB.DBName = "autoship_agent"
	}
	for i := range c.Products {
		c.Products[i].ApplyDefaults()
	}
}

// Validate 检查开发 Agent 核心配置（安全标识符、HTTPS 强制、模型列表、端口池及产品档案）是否合法。
// 无入参；合法返回 nil，不合法返回描述性错误。
func (c *AgentConfig) Validate() error {
	if err := envelope.ValidateSafeID(c.AgentID); err != nil {
		return fmt.Errorf("agentId 非法: %w", err)
	}
	if strings.TrimSpace(c.ServerURL) != "" && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(c.ServerURL)), "https://") {
		return errors.New("serverUrl 必须使用 https://（设计文档第 2 节禁止明文 HTTP）")
	}
	switch strings.ToLower(strings.TrimSpace(c.Engine.Driver)) {
	case "", "builtin", "agy":
	default:
		return fmt.Errorf("engine.driver 非法: %q（仅支持 builtin 或 agy）", c.Engine.Driver)
	}
	isAgy := strings.EqualFold(strings.TrimSpace(c.Engine.Driver), "agy")
	if (!isAgy || c.Relay.Enabled) && len(c.Models.Profiles) == 0 {
		return errors.New("启用 builtin 引擎或 relay 中继时，models.profiles 至少需要配置一个模型通道")
	}
	for _, p := range c.Models.Profiles {
		if strings.TrimSpace(p.Name) == "" || strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.Model) == "" {
			return fmt.Errorf("模型 Profile %q 缺少 name/baseUrl/model 必填项", p.Name)
		}
	}
	if c.PortPool.StartPort < 1024 || c.PortPool.EndPort > 65535 || c.PortPool.EndPort < c.PortPool.StartPort {
		return fmt.Errorf("端口池范围非法: %d-%d", c.PortPool.StartPort, c.PortPool.EndPort)
	}
	for _, prod := range c.Products {
		if err := prod.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ReposDir 返回所有产品专用 Git 克隆所在的根目录（如 ~/autoship/repos）。
// 无入参；返回目录绝对路径字符串。
func (c *AgentConfig) ReposDir() string {
	return filepath.Join(c.RootDir, "repos")
}

// WorktreesDir 返回所有开发任务独立 worktree 所在的根目录（如 ~/autoship/work）。
// 无入参；返回目录绝对路径字符串。
func (c *AgentConfig) WorktreesDir() string {
	return filepath.Join(c.RootDir, "work")
}

// CacheDir 返回跨任务共享依赖缓存根目录（如 ~/autoship/cache）。
// 无入参；返回目录绝对路径字符串。
func (c *AgentConfig) CacheDir() string {
	return filepath.Join(c.RootDir, "cache")
}

// StateDir 返回本地发件箱、命令幂等记录与运行时状态目录（如 ~/autoship/state）。
// 无入参；返回目录绝对路径字符串。
func (c *AgentConfig) StateDir() string {
	return filepath.Join(c.RootDir, "state")
}

// PKIDir 返回 Agent 自身 TLS 证书与私钥存储目录（如 ~/autoship/pki）。
// 无入参；返回目录绝对路径字符串。
func (c *AgentConfig) PKIDir() string {
	return filepath.Join(c.RootDir, "pki")
}

// TranscriptsDir 返回会话记录 JSONL 文件落盘目录（如 ~/autoship/transcripts）。
// 无入参；返回目录绝对路径字符串。
func (c *AgentConfig) TranscriptsDir() string {
	return filepath.Join(c.RootDir, "transcripts")
}

// FindProfile 按名称在 Models.Profiles 中查找对应的模型通道定义。
// 参数 name 为 Profile 名称；命中返回 (ModelProfile, true)，未命中返回零值与 false。
func (c *AgentConfig) FindProfile(name string) (ModelProfile, bool) {
	for _, p := range c.Models.Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return ModelProfile{}, false
}

// ProfilesForRole 根据引擎内部角色（如 coder/reviewer/auditor）按顺序解析故障转移的模型 Profile 切片。
// 参数 role 为引擎角色标识；若未专门配置该角色映射，则按全局 Profiles 顺序兜底返回。
func (c *AgentConfig) ProfilesForRole(role string) []ModelProfile {
	names := c.Models.RoleMapping[role]
	if len(names) == 0 {
		out := make([]ModelProfile, len(c.Models.Profiles))
		copy(out, c.Models.Profiles)
		return out
	}
	var resolved []ModelProfile
	for _, n := range names {
		if prof, ok := c.FindProfile(n); ok {
			resolved = append(resolved, prof)
		}
	}
	if len(resolved) == 0 {
		out := make([]ModelProfile, len(c.Models.Profiles))
		copy(out, c.Models.Profiles)
		return out
	}
	return resolved
}

// ApplyDefaults 补齐单个产品档案的默认分支、沙箱默认保护档、预览端口环境变量及发布变更日志路径。
// 无入参；直接修改接收者 *ProductProfile。
func (p *ProductProfile) ApplyDefaults() {
	if strings.TrimSpace(p.DefaultBranch) == "" {
		p.DefaultBranch = "main"
	}
	p.DeployKeyPath = ExpandPath(p.DeployKeyPath)
	if strings.TrimSpace(p.Sandbox.Mode) == "" {
		p.Sandbox.Mode = "protected"
	}
	for i, ep := range p.Sandbox.ExtraWritePaths {
		p.Sandbox.ExtraWritePaths[i] = ExpandPath(ep)
	}
	if p.Preview.PortCount <= 0 {
		p.Preview.PortCount = 1
	}
	if strings.TrimSpace(p.Preview.PortEnvVar) == "" {
		p.Preview.PortEnvVar = "PORT"
	}
	if strings.TrimSpace(p.Preview.DBDSNEnvVar) == "" {
		p.Preview.DBDSNEnvVar = "DATABASE_DSN"
	}
	if strings.TrimSpace(p.Preview.DBPrefix) == "" && p.ProductCode != "" {
		safe := strings.ReplaceAll(strings.ReplaceAll(p.ProductCode, "-", "_"), ".", "_")
		p.Preview.DBPrefix = safe
	}
	if strings.TrimSpace(p.Release.ChangelogFile) == "" {
		p.Release.ChangelogFile = "CHANGELOG.md"
	}
	if strings.TrimSpace(p.Release.MinAgentVersion) == "" {
		p.Release.MinAgentVersion = "v0.1.0"
	}
}

// Validate 校验产品档案的产品编码与沙箱模式枚举是否合法。
// 无入参；合法返回 nil，非法返回具体错误。
func (p *ProductProfile) Validate() error {
	if err := envelope.ValidateSafeID(p.ProductCode); err != nil {
		return fmt.Errorf("产品编码非法: %w", err)
	}
	switch p.Sandbox.Mode {
	case "", "off", "protected", "custom":
	default:
		return fmt.Errorf("产品 %s 的 sandbox.mode 非法: %q", p.ProductCode, p.Sandbox.Mode)
	}
	return nil
}
