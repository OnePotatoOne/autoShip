// Package service 提供审计日志脱敏记录与平台运行时设置编排服务。
package service

import (
	"encoding/json"
	"strings"
	"time"

	"autoship/internal/config"
	"autoship/internal/modules/audit/model"
	"autoship/internal/modules/audit/repository"
	"autoship/pkg/crypto"
	"autoship/pkg/desensitize"
)

// Service 封装审计日志记录与全局系统设置读写逻辑。
type Service struct {
	repo              *repository.Repository
	cfg               *config.ServerConfig
	cfgPath           string
	encKey            []byte
	publicRebinder    func(newAddr string) error
	engineCfgSyncer   func(agyModel, agyEffort string)
}

// New 创建审计与系统设置业务服务实例。
// 参数 repo 为审计仓储，cfg 为内存全局配置指针，cfgPath 为配置文件持久化路径，encKey 为 32 字节加密密钥；返回 *Service。
func New(repo *repository.Repository, cfg *config.ServerConfig, cfgPath string, encKey []byte) *Service {
	return &Service{
		repo:    repo,
		cfg:     cfg,
		cfgPath: cfgPath,
		encKey:  encKey,
	}
}

// SetPublicListenerRebinder 注入外部调用独立端口的运行时热切换回调，使后台修改外部端口后无需手工重启即可立即生效。
// 参数 fn 为接收新 host:port 并在进程内重绑 HTTPS 监听器的回调函数；无返回值，在 main 启动入口注入。
func (s *Service) SetPublicListenerRebinder(fn func(newAddr string) error) {
	s.publicRebinder = fn
}

// SetEngineConfigSyncer 注入向在线开发 Agent 同步默认 Antigravity 模型与推理强度的回调函数。
// 参数 fn 为同步回调；无返回值，在 router.Build 阶段注入。
func (s *Service) SetEngineConfigSyncer(fn func(agyModel, agyEffort string)) {
	s.engineCfgSyncer = fn
}

// Record 对详情对象执行脱敏序列化并写入审计表。
// 参数 entry 为审计基础信息，detail 为可选结构化详情（会自动过滤密钥与密码）；无返回值，内部忽略非致命落库异常以免阻断主业务。
func (s *Service) Record(entry model.AuditLog, detail any) {
	if detail != nil && entry.DetailJSON == "" {
		if raw, err := json.Marshal(detail); err == nil {
			entry.DetailJSON = desensitize.MaskSecrets(string(raw))
		}
	} else if entry.DetailJSON != "" {
		entry.DetailJSON = desensitize.MaskSecrets(entry.DetailJSON)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	_ = s.repo.CreateAudit(&entry)
}

// List 查询分页审计记录。
// 参数 f 为筛选条件；返回审计记录列表、总数及错误。
func (s *Service) List(f repository.ListFilter) ([]model.AuditLog, int64, error) {
	return s.repo.ListAudits(f)
}

// SystemSettingsView 表示面向管理后台展示的脱敏全局设置视图。
type SystemSettingsView struct {
	LLM               LLMSettingsView      `json:"llm"`               // 模型调用设置
	Notify            NotifySettingsView   `json:"notify"`            // 飞书通知设置
	Workflow          WorkflowSettingsView `json:"workflow"`          // 需求自动进入研发与状态流转策略
	PKI               PKISettingsView      `json:"pki"`               // 证书与监听地址只读视图
	ArtifactKeepCount int                  `json:"artifactKeepCount"` // 制品保留份数
}

// WorkflowSettingsView 表示面向管理后台展示的需求研发流转自动化与默认 Antigravity 模型配置视图。
type WorkflowSettingsView struct {
	AutoStartOnNewRequirement bool   `json:"autoStartOnNewRequirement"` // 新增需求是否无需人工确认直接自动进入研发状态
	AutoStartOnConfirmStatus  bool   `json:"autoStartOnConfirmStatus"`  // 人工确认立项改状态（scheduled/in_progress）后是否自动启动开发任务
	DefaultPlanConfirm        bool   `json:"defaultPlanConfirm"`        // 自动触发研发时是否默认开启方案确认门
	DefaultAgyModel           string `json:"defaultAgyModel"`           // 调用 agy 开发时的全局默认模型 ID（空串表示跟随本机 agy 默认）
	DefaultAgyEffort          string `json:"defaultAgyEffort"`          // 调用 agy 开发时的全局默认推理强度（空串默认，可选 low/medium/high/xhigh/max）
	DefaultTokenBudget        int    `json:"defaultTokenBudget"`        // 默认单任务 Token 预算
	DefaultMaxTurns           int    `json:"defaultMaxTurns"`           // 默认单任务最大轮数
	DefaultMaxMinutes         int    `json:"defaultMaxMinutes"`         // 默认单任务最大分钟数
}

// LLMSettingsView 表示脱敏后的模型通道配置视图。
type LLMSettingsView struct {
	Mode              string `json:"mode"`              // relay 或 direct
	DirectBaseURL     string `json:"directBaseURL"`     // 直连地址
	HasDirectAPIKey   bool   `json:"hasDirectApiKey"`   // 是否已配置直连密钥
	IntakeModel       string `json:"intakeModel"`       // 客服角色模型名
	TriageModel       string `json:"triageModel"`       // 分拣角色模型名
	MaxSessionTokens  int    `json:"maxSessionTokens"`  // 会话最大 Token 预算
	MaxRequestTokens  int    `json:"maxRequestTokens"`  // 单次请求最大输出 Token
	MaskPIIBeforeSend bool   `json:"maskPIIBeforeSend"` // 是否发送前脱敏 IP/邮箱
}

// NotifySettingsView 表示飞书通知配置视图。
type NotifySettingsView struct {
	FeishuWebhookURL string `json:"feishuWebhookURL"` // 飞书 Webhook 地址
	Enabled          bool   `json:"enabled"`          // 是否启用推送
}

// PKISettingsView 表示自建根 CA 指纹以及管理后台端口与独立外部调用端口配置视图。
type PKISettingsView struct {
	CAFingerprint     string   `json:"caFingerprint"`     // 根 CA SHA-256 指纹
	PublicListenAddr  string   `json:"publicListenAddr"`  // 独立外部调用监听地址与端口（如 0.0.0.0:9443）
	PublicAllowIPs    []string `json:"publicAllowIPs"`    // 外部调用端口全局额外放行 IP/CIDR 列表
	TailnetListenAddr string   `json:"tailnetListenAddr"` // Tailnet 管理后台监听地址（与外部端口物理隔离）
	ServerSignPubB64  string   `json:"serverSignPubB64"`  // Server 动作票据验签公钥
}

// UpdateSettingsInput 定义后台修改全局设置时的请求载荷。
type UpdateSettingsInput struct {
	LLM struct {
		Mode              string `json:"mode"`
		DirectBaseURL     string `json:"directBaseURL"`
		DirectAPIKey      string `json:"directApiKey"` // 为空表示保持原密钥不变
		IntakeModel       string `json:"intakeModel"`
		TriageModel       string `json:"triageModel"`
		MaxSessionTokens  int    `json:"maxSessionTokens"`
		MaxRequestTokens  int    `json:"maxRequestTokens"`
		MaskPIIBeforeSend bool   `json:"maskPIIBeforeSend"`
	} `json:"llm"`
	Notify struct {
		FeishuWebhookURL string `json:"feishuWebhookURL"`
		Enabled          bool   `json:"enabled"`
	} `json:"notify"`
	Workflow *struct {
		AutoStartOnNewRequirement bool   `json:"autoStartOnNewRequirement"`
		AutoStartOnConfirmStatus  bool   `json:"autoStartOnConfirmStatus"`
		DefaultPlanConfirm        bool   `json:"defaultPlanConfirm"`
		DefaultAgyModel           string `json:"defaultAgyModel"`
		DefaultAgyEffort          string `json:"defaultAgyEffort"`
		DefaultTokenBudget        int    `json:"defaultTokenBudget"`
		DefaultMaxTurns           int    `json:"defaultMaxTurns"`
		DefaultMaxMinutes         int    `json:"defaultMaxMinutes"`
	} `json:"workflow"`
	PublicListenAddr  string    `json:"publicListenAddr"`  // 可选修改的独立外部调用监听地址或端口号（如 0.0.0.0:9443 或 9443）
	PublicAllowIPs    *[]string `json:"publicAllowIPs"`    // 可选修改的外部调用全局附加 IP/CIDR 白名单
	ArtifactKeepCount int       `json:"artifactKeepCount"`
}

// GetSettingsView 组装当前平台全局设置的脱敏视图。
// 参数 caFingerprint 为启动加载的根 CA 指纹；返回 SystemSettingsView。
func (s *Service) GetSettingsView(caFingerprint string) SystemSettingsView {
	allowIPs := make([]string, 0, len(s.cfg.PublicAllowIPs))
	allowIPs = append(allowIPs, s.cfg.PublicAllowIPs...)
	return SystemSettingsView{
		LLM: LLMSettingsView{
			Mode:              s.cfg.LLM.Mode,
			DirectBaseURL:     s.cfg.LLM.DirectBaseURL,
			HasDirectAPIKey:   s.cfg.LLM.DirectAPIKeyEnc != "",
			IntakeModel:       s.cfg.LLM.IntakeModel,
			TriageModel:       s.cfg.LLM.TriageModel,
			MaxSessionTokens:  s.cfg.LLM.MaxSessionTokens,
			MaxRequestTokens:  s.cfg.LLM.MaxRequestTokens,
			MaskPIIBeforeSend: s.cfg.LLM.MaskPIIBeforeSend,
		},
		Notify: NotifySettingsView{
			FeishuWebhookURL: s.cfg.Notify.FeishuWebhookURL,
			Enabled:          s.cfg.Notify.Enabled,
		},
		Workflow: WorkflowSettingsView{
			AutoStartOnNewRequirement: s.cfg.Workflow.AutoStartOnNewRequirement,
			AutoStartOnConfirmStatus:  s.cfg.Workflow.AutoStartOnConfirmStatus,
			DefaultPlanConfirm:        s.cfg.Workflow.DefaultPlanConfirm,
			DefaultAgyModel:           s.cfg.Workflow.DefaultAgyModel,
			DefaultAgyEffort:          s.cfg.Workflow.DefaultAgyEffort,
			DefaultTokenBudget:        s.cfg.Workflow.DefaultTokenBudget,
			DefaultMaxTurns:           s.cfg.Workflow.DefaultMaxTurns,
			DefaultMaxMinutes:         s.cfg.Workflow.DefaultMaxMinutes,
		},
		PKI: PKISettingsView{
			CAFingerprint:     caFingerprint,
			PublicListenAddr:  s.cfg.PublicListenAddr,
			PublicAllowIPs:    allowIPs,
			TailnetListenAddr: s.cfg.TailnetListenAddr,
			ServerSignPubB64:  s.cfg.ServerSignPubB64,
		},
		ArtifactKeepCount: s.cfg.ArtifactKeepCount,
	}
}

// UpdateSettings 更新内存配置（若修改了外部调用端口则先校验端口隔离并热切换监听器）、写入 settings 表并同步回写 YAML 配置文件。
// 参数 in 为修改内容，actor 为管理员账号；成功返回 nil，端口冲突或绑定失败时返回具体错误。
func (s *Service) UpdateSettings(in UpdateSettingsInput, actor string) error {
	if strings.TrimSpace(in.PublicListenAddr) != "" {
		normalizedAddr, err := config.NormalizeAndValidatePublicListenAddr(in.PublicListenAddr, s.cfg.TailnetListenAddr)
		if err != nil {
			return err
		}
		if normalizedAddr != s.cfg.PublicListenAddr {
			if s.publicRebinder != nil {
				if err := s.publicRebinder(normalizedAddr); err != nil {
					return err
				}
			}
			s.cfg.PublicListenAddr = normalizedAddr
			_ = s.repo.UpsertSetting(&model.Setting{
				ProductID:   0,
				SettingKey:  "public_listen_addr",
				SettingVal:  normalizedAddr,
				Description: "独立外部调用监听地址与端口",
				UpdatedBy:   actor,
			})
		}
	}
	if in.PublicAllowIPs != nil {
		cleaned := make([]string, 0, len(*in.PublicAllowIPs))
		for _, ip := range *in.PublicAllowIPs {
			if t := strings.TrimSpace(ip); t != "" {
				cleaned = append(cleaned, t)
			}
		}
		s.cfg.PublicAllowIPs = cleaned
	}

	if in.LLM.Mode == "relay" || in.LLM.Mode == "direct" {
		s.cfg.LLM.Mode = in.LLM.Mode
	}
	s.cfg.LLM.DirectBaseURL = in.LLM.DirectBaseURL
	if in.LLM.DirectAPIKey != "" {
		enc, err := crypto.EncryptField(in.LLM.DirectAPIKey, s.encKey)
		if err != nil {
			return err
		}
		s.cfg.LLM.DirectAPIKeyEnc = enc
	}
	if in.LLM.IntakeModel != "" {
		s.cfg.LLM.IntakeModel = in.LLM.IntakeModel
	}
	if in.LLM.TriageModel != "" {
		s.cfg.LLM.TriageModel = in.LLM.TriageModel
	}
	if in.LLM.MaxSessionTokens > 0 {
		s.cfg.LLM.MaxSessionTokens = in.LLM.MaxSessionTokens
	}
	if in.LLM.MaxRequestTokens > 0 {
		s.cfg.LLM.MaxRequestTokens = in.LLM.MaxRequestTokens
	}
	s.cfg.LLM.MaskPIIBeforeSend = in.LLM.MaskPIIBeforeSend
	s.cfg.Notify.FeishuWebhookURL = in.Notify.FeishuWebhookURL
	s.cfg.Notify.Enabled = in.Notify.Enabled
	if in.ArtifactKeepCount > 0 {
		s.cfg.ArtifactKeepCount = in.ArtifactKeepCount
	}
	if in.Workflow != nil {
		s.cfg.Workflow.AutoStartOnNewRequirement = in.Workflow.AutoStartOnNewRequirement
		s.cfg.Workflow.AutoStartOnConfirmStatus = in.Workflow.AutoStartOnConfirmStatus
		s.cfg.Workflow.DefaultPlanConfirm = in.Workflow.DefaultPlanConfirm
		s.cfg.Workflow.DefaultAgyModel = strings.TrimSpace(in.Workflow.DefaultAgyModel)
		s.cfg.Workflow.DefaultAgyEffort = strings.TrimSpace(in.Workflow.DefaultAgyEffort)
		if in.Workflow.DefaultTokenBudget > 0 {
			s.cfg.Workflow.DefaultTokenBudget = in.Workflow.DefaultTokenBudget
		}
		if in.Workflow.DefaultMaxTurns > 0 {
			s.cfg.Workflow.DefaultMaxTurns = in.Workflow.DefaultMaxTurns
		}
		if in.Workflow.DefaultMaxMinutes > 0 {
			s.cfg.Workflow.DefaultMaxMinutes = in.Workflow.DefaultMaxMinutes
		}
		autoNewVal := "false"
		if s.cfg.Workflow.AutoStartOnNewRequirement {
			autoNewVal = "true"
		}
		_ = s.repo.UpsertSetting(&model.Setting{
			ProductID:   0,
			SettingKey:  "workflow_auto_start_new_requirement",
			SettingVal:  autoNewVal,
			Description: "新增需求是否自动进入研发状态",
			UpdatedBy:   actor,
		})
		_ = s.repo.UpsertSetting(&model.Setting{
			ProductID:   0,
			SettingKey:  "workflow_default_agy_model",
			SettingVal:  s.cfg.Workflow.DefaultAgyModel,
			Description: "调用 Antigravity (agy) 开发时的默认模型 ID",
			UpdatedBy:   actor,
		})
		if s.engineCfgSyncer != nil {
			s.engineCfgSyncer(s.cfg.Workflow.DefaultAgyModel, s.cfg.Workflow.DefaultAgyEffort)
		}
	}

	_ = s.repo.UpsertSetting(&model.Setting{
		ProductID:   0,
		SettingKey:  "feishu_webhook_url",
		SettingVal:  s.cfg.Notify.FeishuWebhookURL,
		Description: "飞书机器人通知地址",
		UpdatedBy:   actor,
	})
	_ = s.repo.UpsertSetting(&model.Setting{
		ProductID:   0,
		SettingKey:  "llm_mode",
		SettingVal:  s.cfg.LLM.Mode,
		Description: "模型调用通道模式",
		UpdatedBy:   actor,
	})

	if s.cfgPath != "" {
		return config.Save(s.cfgPath, s.cfg)
	}
	return nil
}
