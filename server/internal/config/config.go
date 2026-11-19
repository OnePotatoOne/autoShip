// Package config 管理 autoShip Server 的全局配置文件解析、默认值补齐及主盐值自动初始化。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"autoship/pkg/crypto"
)

// ServerConfig 定义 autoShip Server 的完整配置项。
type ServerConfig struct {
	TailnetListenAddr string         `yaml:"tailnetListenAddr"` // 管理后台与开发 Agent 监听地址（只绑定 tailnet IP 或本机回环，如 127.0.0.1:8444）
	TailnetSANIPs     []string       `yaml:"tailnetSanIPs"`     // 后台证书额外写入的 tailnet SAN IP 列表
	PublicListenAddr  string         `yaml:"publicListenAddr"`  // 公网机器对机器入口（仅挂载 /open/v1 与 /agent/v1/deploy，如 0.0.0.0:8443）
	PublicSANIPs      []string       `yaml:"publicSanIPs"`      // 公网证书额外写入的弹性公网 SAN IP 列表
	PublicAllowIPs    []string       `yaml:"publicAllowIPs"`    // 公网端口全局额外放行 IP/CIDR（除各接入方与部署目标已登记白名单外）
	DataDir           string         `yaml:"dataDir"`           // 运行时数据根目录（存放 PKI 证书、制品库、上传附件）
	CookiePrefix      string         `yaml:"cookiePrefix"`      // 管理后台专属 Cookie 前缀（默认 autoship_，避免与其他系统冲突）
	MasterSalt        string         `yaml:"masterSalt"`        // 用于派生 AES-256 配置加密密钥的主盐值（首次启动自动生成）
	ServerSignPubB64  string         `yaml:"serverSignPubB64"`  // Server 签发部署动作票据的 Ed25519 公钥（安装更新 Agent 时同步写入 /etc/autoship/server.pub）
	ServerSignPrivEnc string         `yaml:"serverSignPrivEnc"` // Server 签发部署动作票据的加密 Ed25519 私钥
	ArtifactKeepCount int            `yaml:"artifactKeepCount"` // 每个产品保留最近多少个版本的制品包（默认 10）
	Database          DatabaseConfig `yaml:"database"`          // MySQL 8 数据库连接参数
	LLM               LLMConfig      `yaml:"llm"`               // 客服与分拣模型调用配置（支持中继或直连开关）
	Notify            NotifyConfig   `yaml:"notify"`            // 飞书通知 Webhook 默认配置
	Workflow          WorkflowConfig `yaml:"workflow"`          // 需求入池与状态变更触发自动研发的流转策略配置
}

// DatabaseConfig 定义 MySQL 8 数据库连接池与库名参数。
type DatabaseConfig struct {
	Host            string `yaml:"host"`            // 数据库主机地址
	Port            int    `yaml:"port"`            // 数据库端口（默认 3306）
	User            string `yaml:"user"`            // 数据库用户名
	Password        string `yaml:"password"`        // 数据库密码（支持 ENC: 加密存储）
	DBName          string `yaml:"dbName"`          // 数据库名（默认 autoship）
	MaxOpenConns    int    `yaml:"maxOpenConns"`    // 最大打开连接数
	MaxIdleConns    int    `yaml:"maxIdleConns"`    // 最大空闲连接数
	ConnMaxLifetime int    `yaml:"connMaxLifetime"` // 连接最长存活秒数
}

// DSN 使用解密后的明文密码构造 GORM MySQL 连接字符串。
// 参数 plainPassword 为已解密的数据库口令；返回标准 MySQL DSN 字符串。
func (d DatabaseConfig) DSN(plainPassword string) string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=5s",
		d.User, plainPassword, d.Host, d.Port, d.DBName)
}

// LLMConfig 定义 Server 侧客服澄清与自动分拣的大模型通道（7.5 节：中继与直连共用同一套客户端，仅地址不同）。
type LLMConfig struct {
	Mode              string `yaml:"mode"`              // 通道模式：relay（默认经开发 Agent tailnet 中继）或 direct（直连 OpenAI 兼容接口）
	DirectBaseURL     string `yaml:"directBaseURL"`     // direct 模式下的接口基地址（如 https://api.example.com/v1）
	DirectAPIKeyEnc   string `yaml:"directApiKeyEnc"`   // direct 模式下的加密 API Key
	IntakeModel       string `yaml:"intakeModel"`       // 客服澄清角色使用的模型名（relay 模式下传逻辑角色 intake，direct 模式下传具体模型名）
	TriageModel       string `yaml:"triageModel"`       // 需求自动分拣角色使用的模型名
	MaxSessionTokens  int    `yaml:"maxSessionTokens"`  // 单个澄清会话的最大累计 Token 预算（默认 64000）
	MaxRequestTokens  int    `yaml:"maxRequestTokens"`  // 单次模型生成的最大输出 Token 数（默认 4096）
	MaskPIIBeforeSend bool   `yaml:"maskPIIBeforeSend"` // 提交给模型前是否默认对 IP/邮箱脱敏（默认 true）
}

// NotifyConfig 定义飞书 Webhook 通知配置。
type NotifyConfig struct {
	FeishuWebhookURL string `yaml:"feishuWebhookURL"` // 飞书自定义机器人 Webhook 地址
	Enabled          bool   `yaml:"enabled"`          // 是否启用飞书推送
}

// WorkflowConfig 定义新增需求入池与需求状态变更触发自动研发的全局流转策略。
type WorkflowConfig struct {
	AutoStartOnNewRequirement bool `yaml:"autoStartOnNewRequirement"` // 新增需求（外部提交或后台录入）是否自动进入研发状态并下发开发任务（默认 false：先由管理员确认改状态后再开发）
	AutoStartOnConfirmStatus  bool `yaml:"autoStartOnConfirmStatus"`  // 管理员确认要做并将状态改为 scheduled 或 in_progress 时是否自动启动开发任务（默认 true）
	DefaultPlanConfirm        bool `yaml:"defaultPlanConfirm"`        // 自动触发研发任务时是否默认开启“方案确认门”（默认 true）
	DefaultTokenBudget        int  `yaml:"defaultTokenBudget"`        // 自动触发研发任务时的默认 Token 预算上限（默认 400000）
	DefaultMaxTurns           int  `yaml:"defaultMaxTurns"`           // 自动触发研发任务时的默认最大交互轮数（默认 60）
	DefaultMaxMinutes         int  `yaml:"defaultMaxMinutes"`         // 自动触发研发任务时的默认最大执行分钟数（默认 90）
}

// Default 返回带安全默认值的 ServerConfig 实例。
// 无入参；返回填充好默认监听端口、目录及预算上限的 *ServerConfig。
func Default() *ServerConfig {
	return &ServerConfig{
		TailnetListenAddr: "127.0.0.1:8444",
		PublicListenAddr:  "0.0.0.0:8443",
		DataDir:           "./data",
		CookiePrefix:      "autoship_",
		ArtifactKeepCount: 10,
		Database: DatabaseConfig{
			Host:            "127.0.0.1",
			Port:            3306,
			User:            "root",
			DBName:          "autoship",
			MaxOpenConns:    32,
			MaxIdleConns:    8,
			ConnMaxLifetime: 1800,
		},
		LLM: LLMConfig{
			Mode:              "relay",
			IntakeModel:       "intake",
			TriageModel:       "triage",
			MaxSessionTokens:  64000,
			MaxRequestTokens:  4096,
			MaskPIIBeforeSend: true,
		},
		Notify: NotifyConfig{
			Enabled: false,
		},
		Workflow: WorkflowConfig{
			AutoStartOnNewRequirement: false,
			AutoStartOnConfirmStatus:  true,
			DefaultPlanConfirm:        true,
			DefaultTokenBudget:        400000,
			DefaultMaxTurns:           60,
			DefaultMaxMinutes:         90,
		},
	}
}

// LoadOrInit 从指定 YAML 路径加载配置；若文件不存在则以默认配置创建，并确保自动生成 MasterSalt 与 Server 票据签名密钥对。
// 参数 path 为配置文件路径；返回配置对象、派生好的 32 字节加密密钥及错误，供 main 启动入口调用。
func LoadOrInit(path string) (*ServerConfig, []byte, error) {
	cfg := Default()
	if data, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, nil, fmt.Errorf("解析配置文件 %s 失败: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}

	dirty := false
	if strings.TrimSpace(cfg.CookiePrefix) == "" {
		cfg.CookiePrefix = "autoship_"
		dirty = true
	}
	if cfg.ArtifactKeepCount <= 0 {
		cfg.ArtifactKeepCount = 10
		dirty = true
	}
	if cfg.Workflow.DefaultTokenBudget <= 0 {
		cfg.Workflow.DefaultTokenBudget = 400000
		dirty = true
	}
	if cfg.Workflow.DefaultMaxTurns <= 0 {
		cfg.Workflow.DefaultMaxTurns = 60
		dirty = true
	}
	if cfg.Workflow.DefaultMaxMinutes <= 0 {
		cfg.Workflow.DefaultMaxMinutes = 90
		dirty = true
	}
	if strings.TrimSpace(cfg.MasterSalt) == "" {
		salt, err := crypto.GenerateRandomHex(16)
		if err != nil {
			return nil, nil, err
		}
		cfg.MasterSalt = salt
		dirty = true
	}
	encKey := crypto.DeriveKey([]byte(cfg.MasterSalt))

	if strings.TrimSpace(cfg.ServerSignPubB64) == "" || strings.TrimSpace(cfg.ServerSignPrivEnc) == "" {
		pub, priv, err := crypto.GenerateEd25519KeyPair()
		if err != nil {
			return nil, nil, err
		}
		privEnc, err := crypto.EncryptField(priv, encKey)
		if err != nil {
			return nil, nil, err
		}
		cfg.ServerSignPubB64 = pub
		cfg.ServerSignPrivEnc = privEnc
		dirty = true
	}

	// 若数据库密码仍是明文且非空，自动转为加密格式回写
	if cfg.Database.Password != "" && !strings.HasPrefix(cfg.Database.Password, "ENC:") {
		encPass, err := crypto.EncryptField(cfg.Database.Password, encKey)
		if err != nil {
			return nil, nil, err
		}
		cfg.Database.Password = encPass
		dirty = true
	}

	if dirty || !fileExists(path) {
		if err := Save(path, cfg); err != nil {
			return nil, nil, err
		}
	}
	return cfg, encKey, nil
}

// Save 将配置结构体以 UTF-8 YAML 格式安全持久化到磁盘（文件权限 0600）。
// 参数 path 为目标文件路径，cfg 为配置结构体；成功返回 nil，失败返回错误。
func Save(path string, cfg *ServerConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化 YAML 配置失败: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}
	defer f.Close()
	_ = f.Chmod(0600)
	if _, err := f.Write(out); err != nil {
		return fmt.Errorf("保存配置文件内容失败: %w", err)
	}
	return nil
}

// PKIDir 返回 PKI 根证书与服务端叶证书所在的子目录路径。
// 无入参；返回拼接后的目录路径。
func (c *ServerConfig) PKIDir() string {
	return filepath.Join(c.DataDir, "pki")
}

// ArtifactDir 返回版本制品包本地磁盘存储目录路径。
// 无入参；返回拼接后的目录路径。
func (c *ServerConfig) ArtifactDir() string {
	return filepath.Join(c.DataDir, "artifacts")
}

// AttachmentDir 返回客服澄清上传截图存储目录路径。
// 无入参；返回拼接后的目录路径。
func (c *ServerConfig) AttachmentDir() string {
	return filepath.Join(c.DataDir, "attachments")
}

// fileExists 检查指定路径是否存在普通文件；参数 path 为磁盘路径；存在普通文件返回 true，否则返回 false，由 LoadOrInit 内部调用。
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
