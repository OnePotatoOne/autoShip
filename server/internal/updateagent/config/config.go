// Package config 提供产品服务器更新 Agent（autoship-agent）与 root 提权小工具（autoship-apply）的配置模型、默认值初始化及安全校验逻辑。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"

	"autoship/pkg/envelope"
	"autoship/pkg/pki"
	"autoship/pkg/semver"
)

const (
	// DefaultAgentConfigPath 是 autoship-agent 常驻进程的默认配置文件路径。
	DefaultAgentConfigPath = "/etc/autoship/update-agent.yaml"
	// DefaultApplyConfigPath 是 autoship-apply（root）的默认受保护配置文件路径。
	DefaultApplyConfigPath = "/etc/autoship/apply.yaml"
	// TestApplyConfigEnvKey 是仅在非 root（euid != 0）自动化测试环境下允许覆盖 autoship-apply 配置路径的环境变量名。
	TestApplyConfigEnvKey = "AUTOSHIP_APPLY_TEST_CONFIG"
)

// AgentConfig 定义无特权常驻进程 autoship-agent 的运行配置（对应设计文档 9.2 节，不持有任何数据库账号）。
type AgentConfig struct {
	AgentVersion              string `yaml:"agentVersion"`              // 更新 Agent 自身语义化版本号（如 v0.1.0）
	ServerURL                 string `yaml:"serverURL"`                 // autoShip Server 公网入口 HTTPS 地址（禁止明文 HTTP）
	CAFingerprint             string `yaml:"caFingerprint"`             // autoShip 根 CA 证书 SHA-256 指纹（64 位十六进制）
	BootstrapToken            string `yaml:"bootstrapToken"`            // 首次启动向 Server 注册 Ed25519 公钥的一次性令牌
	TargetID                  uint64 `yaml:"targetId"`                  // 部署目标 ID（为 0 时以首次注册响应为准）
	ProductCode               string `yaml:"productCode"`               // 绑定的产品唯一编码（为空时以首次注册响应为准）
	ReleasePubPath            string `yaml:"releasePubPath"`            // 发布清单验签公钥路径（默认 /etc/autoship/release.pub，root 所有、只读）
	ServerPubPath             string `yaml:"serverPubPath"`             // Server 动作票据验签公钥路径（默认 /etc/autoship/server.pub，root 所有、只读）
	StateDir                  string `yaml:"stateDir"`                  // 常驻进程状态目录（存放 identity.json、journal.json 与 deploy.lock）
	StagingDir                string `yaml:"stagingDir"`                // 固定暂存目录（默认 /var/lib/autoship-agent/staging/current）
	BackupRootDir             string `yaml:"backupRootDir"`             // 数据库备份根目录（默认 /var/backups/autoship）
	BackupQuotaBytes          uint64 `yaml:"backupQuotaBytes"`          // 数据库备份存储上限字节数（超限时上报飞书/后台告警，不自动删旧备份）
	MinFreeDiskBytes          uint64 `yaml:"minFreeDiskBytes"`          // 部署前要求保留的最小基础空闲磁盘字节数
	EstimatedDBBackupBytes    uint64 `yaml:"estimatedDbBackupBytes"`    // 需备份数据库时的最小预估备份占用字节数（空闲不足则阻断部署）
	ApplyBinPath              string `yaml:"applyBinPath"`              // autoship-apply 可执行文件绝对路径（默认 /usr/local/sbin/autoship-apply）
	SudoBinPath               string `yaml:"sudoBinPath"`               // sudo 可执行文件路径（默认 /usr/bin/sudo，测试中可注入模拟 sudo）
	UseSudo                   bool   `yaml:"useSudo"`                   // 是否通过 sudo -n 调用 autoship-apply（生产环境固定为 true）
	HealthCheckURL            string `yaml:"healthCheckURL"`            // 产品免登录健康检查接口地址（如 http://127.0.0.1:8080/healthz）
	HealthCheckInsecureTLS    bool   `yaml:"healthCheckInsecureTLS"`    // 本机 HTTPS 健康检查是否跳过自签证书主机名校验
	HealthCheckIntervalMs     int    `yaml:"healthCheckIntervalMs"`     // 上线健康检查轮询间隔毫秒数（默认 2000ms）
	HealthCheckTimeoutSec     int    `yaml:"healthCheckTimeoutSec"`     // 普通版本上线健康检查总等待超时秒数（默认 30s）
	MigrationTimeoutSec       int    `yaml:"migrationTimeoutSec"`       // 含迁移版本的扩展上线等待超时秒数（默认 180s，9.4 第 6 步）
	HeartbeatIntervalSec      int    `yaml:"heartbeatIntervalSec"`      // 心跳上报周期秒数（默认 30s，9.2 节）
	PollTimeoutSec            int    `yaml:"pollTimeoutSec"`            // 长轮询单次 HTTP 超时秒数（默认 35s，配合服务端 30s 挂起）
	HTTPTimeoutSec            int    `yaml:"httpTimeoutSec"`            // 常规 API 请求超时秒数（默认 30s）
	DownloadTimeoutSec        int    `yaml:"downloadTimeoutSec"`        // 制品包断点续传单次连接超时秒数（默认 600s）
	AllowFirstInstallWhenDown bool   `yaml:"allowFirstInstallWhenDown"` // 首次全新部署尚无运行实例时是否允许跳过运行中预检
}

// DefaultAgentConfig 创建填充生产环境安全默认值的 AgentConfig 实例。
// 无入参；返回带默认路径、超时与配额参数的 *AgentConfig，通过 config.DefaultAgentConfig() 调用。
func DefaultAgentConfig() *AgentConfig {
	return &AgentConfig{
		AgentVersion:           "v0.1.0",
		ReleasePubPath:         "/etc/autoship/release.pub",
		ServerPubPath:          "/etc/autoship/server.pub",
		StateDir:               "/var/lib/autoship-agent",
		StagingDir:             "/var/lib/autoship-agent/staging/current",
		BackupRootDir:          "/var/backups/autoship",
		BackupQuotaBytes:       10 * 1024 * 1024 * 1024, // 10 GiB
		MinFreeDiskBytes:       512 * 1024 * 1024,       // 512 MiB
		EstimatedDBBackupBytes: 256 * 1024 * 1024,       // 256 MiB
		ApplyBinPath:           "/usr/local/sbin/autoship-apply",
		SudoBinPath:            "/usr/bin/sudo",
		UseSudo:                true,
		HealthCheckIntervalMs:  2000,
		HealthCheckTimeoutSec:  30,
		MigrationTimeoutSec:    180,
		HeartbeatIntervalSec:   30,
		PollTimeoutSec:         35,
		HTTPTimeoutSec:         30,
		DownloadTimeoutSec:     600,
	}
}

// LoadAgentConfig 从指定 YAML 文件读取并校验更新 Agent 配置。
// 参数 path 为 YAML 配置文件路径（为空时使用 DefaultAgentConfigPath）；返回校验通过的 *AgentConfig 及错误，供 autoship-agent 启动时调用。
func LoadAgentConfig(path string) (*AgentConfig, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultAgentConfigPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取更新 Agent 配置文件 %s 失败: %w", path, err)
	}
	cfg := DefaultAgentConfig()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("解析更新 Agent YAML 配置 %s 失败: %w", path, err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ApplyDefaults 为 AgentConfig 中未显式填写的可选字段补齐安全默认值。
// 无入参；无返回值，直接原地修改接收者字段，供 LoadAgentConfig 或测试代码构造配置后调用。
func (c *AgentConfig) ApplyDefaults() {
	def := DefaultAgentConfig()
	if strings.TrimSpace(c.AgentVersion) == "" {
		c.AgentVersion = def.AgentVersion
	}
	if strings.TrimSpace(c.ReleasePubPath) == "" {
		c.ReleasePubPath = def.ReleasePubPath
	}
	if strings.TrimSpace(c.ServerPubPath) == "" {
		c.ServerPubPath = def.ServerPubPath
	}
	if strings.TrimSpace(c.StateDir) == "" {
		c.StateDir = def.StateDir
	}
	if strings.TrimSpace(c.StagingDir) == "" {
		c.StagingDir = filepath.Join(c.StateDir, "staging", "current")
	}
	if strings.TrimSpace(c.BackupRootDir) == "" {
		c.BackupRootDir = def.BackupRootDir
	}
	if strings.TrimSpace(c.ApplyBinPath) == "" {
		c.ApplyBinPath = def.ApplyBinPath
	}
	if strings.TrimSpace(c.SudoBinPath) == "" {
		c.SudoBinPath = def.SudoBinPath
	}
	if c.HealthCheckIntervalMs <= 0 {
		c.HealthCheckIntervalMs = def.HealthCheckIntervalMs
	}
	if c.HealthCheckTimeoutSec <= 0 {
		c.HealthCheckTimeoutSec = def.HealthCheckTimeoutSec
	}
	if c.MigrationTimeoutSec <= 0 {
		c.MigrationTimeoutSec = def.MigrationTimeoutSec
	}
	if c.MigrationTimeoutSec < c.HealthCheckTimeoutSec {
		c.MigrationTimeoutSec = c.HealthCheckTimeoutSec * 2
	}
	if c.HeartbeatIntervalSec <= 0 {
		c.HeartbeatIntervalSec = def.HeartbeatIntervalSec
	}
	if c.PollTimeoutSec <= 0 {
		c.PollTimeoutSec = def.PollTimeoutSec
	}
	if c.HTTPTimeoutSec <= 0 {
		c.HTTPTimeoutSec = def.HTTPTimeoutSec
	}
	if c.DownloadTimeoutSec <= 0 {
		c.DownloadTimeoutSec = def.DownloadTimeoutSec
	}
}

// Validate 严格校验 AgentConfig 的 HTTPS 协议要求、CA 指纹合法性、版本号及路径非空约束。
// 无入参；配置合法返回 nil，存在缺失或违规项时返回描述性错误，通过 cfg.Validate() 调用。
func (c *AgentConfig) Validate() error {
	if _, err := semver.Parse(c.AgentVersion); err != nil {
		return fmt.Errorf("agentVersion 格式非法: %w", err)
	}
	rawURL := strings.TrimSpace(c.ServerURL)
	if rawURL == "" {
		return errors.New("serverURL 不能为空")
	}
	parsedURL, err := url.Parse(rawURL)
	if err != nil || parsedURL.Host == "" {
		return fmt.Errorf("serverURL 格式非法: %q", c.ServerURL)
	}
	// 严格遵循设计文档第 2 节决策 7：绝不允许明文 HTTP 连接 Server
	if !strings.EqualFold(parsedURL.Scheme, "https") {
		return errors.New("serverURL 必须使用 https 加密协议，禁止明文 http")
	}
	normFP, err := pki.NormalizeFingerprint(c.CAFingerprint)
	if err != nil {
		return fmt.Errorf("caFingerprint 校验失败: %w", err)
	}
	c.CAFingerprint = normFP
	c.ServerURL = strings.TrimRight(rawURL, "/")

	if strings.TrimSpace(c.ProductCode) != "" {
		if err := envelope.ValidateSafeID(strings.TrimSpace(c.ProductCode)); err != nil {
			return fmt.Errorf("productCode 非法: %w", err)
		}
		c.ProductCode = strings.TrimSpace(c.ProductCode)
	}
	if strings.TrimSpace(c.HealthCheckURL) == "" {
		return errors.New("healthCheckURL 不能为空")
	}
	hcURL, err := url.Parse(strings.TrimSpace(c.HealthCheckURL))
	if err != nil || hcURL.Host == "" || (hcURL.Scheme != "http" && hcURL.Scheme != "https") {
		return fmt.Errorf("healthCheckURL 格式非法: %q", c.HealthCheckURL)
	}
	if strings.TrimSpace(c.StateDir) == "" || strings.TrimSpace(c.StagingDir) == "" {
		return errors.New("stateDir 与 stagingDir 不能为空")
	}
	if strings.TrimSpace(c.ReleasePubPath) == "" {
		return errors.New("releasePubPath 不能为空")
	}
	if strings.TrimSpace(c.ApplyBinPath) == "" {
		return errors.New("applyBinPath 不能为空")
	}
	return nil
}

// IdentityFilePath 返回常驻进程持久化 Ed25519 身份凭据的本地文件路径。
// 无入参；返回 <stateDir>/identity.json 绝对或相对路径，供 daemon 模块读写身份使用。
func (c *AgentConfig) IdentityFilePath() string {
	return filepath.Join(c.StateDir, "identity.json")
}

// JournalFilePath 返回常驻进程持久化部署操作状态日志的本地文件路径。
// 无入参；返回 <stateDir>/journal.json 路径，供重启恢复与幂等控制使用。
func (c *AgentConfig) JournalFilePath() string {
	return filepath.Join(c.StateDir, "journal.json")
}

// DeployLockFilePath 返回防止并发部署的排他文件锁路径。
// 无入参；返回 <stateDir>/deploy.lock 路径，供升级/回滚预检加锁调用。
func (c *AgentConfig) DeployLockFilePath() string {
	return filepath.Join(c.StateDir, "deploy.lock")
}

// BackupIndexFilePath 返回常驻进程记录每次成功备份字节数的索引文件路径。
// 无入参；返回 <stateDir>/backups_index.json 路径，用于在 root 0700 备份目录下仍能准确统计已用空间。
func (c *AgentConfig) BackupIndexFilePath() string {
	return filepath.Join(c.StateDir, "backups_index.json")
}

// ApplyConfig 定义 root 提权小工具 autoship-apply 的受保护路径配置（绝不接受命令行参数或生产环境变量注入）。
type ApplyConfig struct {
	StagingDir     string `yaml:"stagingDir"`     // autoship-agent 写入票据和制品包的固定暂存目录
	WorkBaseDir    string `yaml:"workBaseDir"`    // root 专属隔离工作目录根路径（0700，防 TOCTOU）
	LedgerPath     string `yaml:"ledgerPath"`     // root 专属部署账本路径（默认 /var/lib/autoship-apply/ledger.json）
	ServerPubPath  string `yaml:"serverPubPath"`  // root 持有的 Server 动作票据验签公钥路径（/etc/autoship/server.pub）
	ReleasePubPath string `yaml:"releasePubPath"` // root 持有的发布签名验签公钥路径（/etc/autoship/release.pub）
	HookPath       string `yaml:"hookPath"`       // 固定产品部署钩子安装路径（0700，root 所有）
	BackupBaseDir  string `yaml:"backupBaseDir"`  // 数据库备份专用根目录（默认 /var/backups/autoship）
	HookTimeoutSec int    `yaml:"hookTimeoutSec"` // 单次调用部署钩子的最大超时秒数（默认 600s）
}

// DefaultApplyConfig 返回 autoship-apply 的内置安全默认路径配置。
// 无入参；返回指向 /etc/autoship、/var/lib/autoship-apply 等受保护目录的 *ApplyConfig。
func DefaultApplyConfig() *ApplyConfig {
	return &ApplyConfig{
		StagingDir:     "/var/lib/autoship-agent/staging/current",
		WorkBaseDir:    "/var/lib/autoship-apply/work",
		LedgerPath:     "/var/lib/autoship-apply/ledger.json",
		ServerPubPath:  "/etc/autoship/server.pub",
		ReleasePubPath: "/etc/autoship/release.pub",
		HookPath:       "/usr/local/libexec/autoship/deploy-hook",
		BackupBaseDir:  "/var/backups/autoship",
		HookTimeoutSec: 600,
	}
}

// LoadApplyConfig 安全加载 autoship-apply 配置。
// 当以 root（euid == 0）运行时，强制忽略所有环境变量，仅读取属主为 root 且无组/其他写权限的 /etc/autoship/apply.yaml（不存在则用内置默认值）；
// 当以非 root（euid != 0）运行于单元/集成测试环境时，允许通过 AUTOSHIP_APPLY_TEST_CONFIG 指定测试配置文件。
// 无入参；返回校验通过的 *ApplyConfig 及安全检查错误，供 cmd/autoship-apply/main.go 调用。
func LoadApplyConfig() (*ApplyConfig, error) {
	return loadApplyConfigWithEUID(os.Geteuid(), DefaultApplyConfigPath, os.Getenv(TestApplyConfigEnvKey))
}

// loadApplyConfigWithEUID 按指定有效用户 ID 与候选路径执行受保护配置加载（抽出 euid 参数以便单元测试覆盖 root 安全分支）。
// 参数 euid 为当前进程有效 UID，prodPath 为生产固定配置路径，testOverridePath 为环境变量提供的测试配置路径；返回 *ApplyConfig 及错误。
func loadApplyConfigWithEUID(euid int, prodPath, testOverridePath string) (*ApplyConfig, error) {
	cfg := DefaultApplyConfig()
	targetPath := prodPath
	requireRootOwner := euid == 0

	// 生产 root 运行态下严禁从环境变量读取任何路径覆盖，彻底阻断环境注入攻击面
	if euid != 0 && strings.TrimSpace(testOverridePath) != "" {
		targetPath = strings.TrimSpace(testOverridePath)
	}

	info, err := os.Lstat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			// 若显式指定了测试配置但文件不存在，立即报错以便排障；默认路径不存在则安全回退内置默认值
			if euid != 0 && strings.TrimSpace(testOverridePath) != "" {
				return nil, fmt.Errorf("测试配置文件不存在: %s", targetPath)
			}
			return cfg, nil
		}
		return nil, fmt.Errorf("检查 autoship-apply 配置文件状态失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("拒绝加载非普通文件或符号链接的配置文件: %s", targetPath)
	}
	if info.Mode().Perm()&0022 != 0 {
		return nil, fmt.Errorf("autoship-apply 配置文件权限过宽（禁止组或其他用户可写）: %s (%04o)", targetPath, info.Mode().Perm())
	}
	if requireRootOwner {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
			return nil, fmt.Errorf("autoship-apply 配置文件必须归 root 所有，实际 UID=%d", stat.Uid)
		}
	}

	raw, err := os.ReadFile(targetPath)
	if err != nil {
		return nil, fmt.Errorf("读取 autoship-apply 配置文件失败: %w", err)
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("解析 autoship-apply 配置文件失败: %w", err)
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ApplyDefaults 补齐 ApplyConfig 中未设置的默认路径与超时参数。
// 无入参；无返回值，原地更新接收者空值字段。
func (c *ApplyConfig) ApplyDefaults() {
	def := DefaultApplyConfig()
	if strings.TrimSpace(c.StagingDir) == "" {
		c.StagingDir = def.StagingDir
	}
	if strings.TrimSpace(c.WorkBaseDir) == "" {
		c.WorkBaseDir = def.WorkBaseDir
	}
	if strings.TrimSpace(c.LedgerPath) == "" {
		c.LedgerPath = def.LedgerPath
	}
	if strings.TrimSpace(c.ServerPubPath) == "" {
		c.ServerPubPath = def.ServerPubPath
	}
	if strings.TrimSpace(c.ReleasePubPath) == "" {
		c.ReleasePubPath = def.ReleasePubPath
	}
	if strings.TrimSpace(c.HookPath) == "" {
		c.HookPath = def.HookPath
	}
	if strings.TrimSpace(c.BackupBaseDir) == "" {
		c.BackupBaseDir = def.BackupBaseDir
	}
	if c.HookTimeoutSec <= 0 {
		c.HookTimeoutSec = def.HookTimeoutSec
	}
}

// Validate 校验 ApplyConfig 所有关键受保护路径均为绝对路径且互不混用暂存区与隔离工作区。
// 无入参；合法返回 nil，存在相对路径或目录重叠风险时返回错误，通过 cfg.Validate() 调用。
func (c *ApplyConfig) Validate() error {
	paths := map[string]string{
		"stagingDir":     c.StagingDir,
		"workBaseDir":    c.WorkBaseDir,
		"ledgerPath":     c.LedgerPath,
		"serverPubPath":  c.ServerPubPath,
		"releasePubPath": c.ReleasePubPath,
		"hookPath":       c.HookPath,
		"backupBaseDir":  c.BackupBaseDir,
	}
	for name, p := range paths {
		clean := filepath.Clean(strings.TrimSpace(p))
		if !filepath.IsAbs(clean) {
			return fmt.Errorf("autoship-apply 配置项 %s 必须为绝对路径: %q", name, p)
		}
	}
	cleanStaging := filepath.Clean(c.StagingDir)
	cleanWork := filepath.Clean(c.WorkBaseDir)
	if cleanStaging == cleanWork || strings.HasPrefix(cleanWork+string(os.PathSeparator), cleanStaging+string(os.PathSeparator)) {
		return errors.New("workBaseDir 不能位于 stagingDir 内部，必须保持特权隔离")
	}
	return nil
}
