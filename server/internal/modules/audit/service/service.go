// Package service 提供审计日志脱敏记录与平台运行时设置编排服务。
package service

import (
	"encoding/json"
	"time"

	"autoship/internal/config"
	"autoship/internal/modules/audit/model"
	"autoship/internal/modules/audit/repository"
	"autoship/pkg/crypto"
	"autoship/pkg/desensitize"
)

// Service 封装审计日志记录与全局系统设置读写逻辑。
type Service struct {
	repo       *repository.Repository
	cfg        *config.ServerConfig
	cfgPath    string
	encKey     []byte
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

// WorkflowSettingsView 表示面向管理后台展示的需求研发流转自动化配置视图。
type WorkflowSettingsView struct {
	AutoStartOnNewRequirement bool `json:"autoStartOnNewRequirement"` // 新增需求是否无需人工确认直接自动进入研发状态
	AutoStartOnConfirmStatus  bool `json:"autoStartOnConfirmStatus"`  // 人工确认立项改状态（scheduled/in_progress）后是否自动启动开发任务
	DefaultPlanConfirm        bool `json:"defaultPlanConfirm"`        // 自动触发研发时是否默认开启方案确认门
	DefaultTokenBudget        int  `json:"defaultTokenBudget"`        // 默认单任务 Token 预算
	DefaultMaxTurns           int  `json:"defaultMaxTurns"`           // 默认单任务最大轮数
	DefaultMaxMinutes         int  `json:"defaultMaxMinutes"`         // 默认单任务最大分钟数
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

// PKISettingsView 表示只读展示的自建根 CA 指纹与监听信息。
type PKISettingsView struct {
	CAFingerprint     string `json:"caFingerprint"`     // 根 CA SHA-256 指纹
	PublicListenAddr  string `json:"publicListenAddr"`  // 公网监听端口
	TailnetListenAddr string `json:"tailnetListenAddr"` // Tailnet 后台监听地址
	ServerSignPubB64  string `json:"serverSignPubB64"`  // Server 动作票据验签公钥
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
		AutoStartOnNewRequirement bool `json:"autoStartOnNewRequirement"`
		AutoStartOnConfirmStatus  bool `json:"autoStartOnConfirmStatus"`
		DefaultPlanConfirm        bool `json:"defaultPlanConfirm"`
		DefaultTokenBudget        int  `json:"defaultTokenBudget"`
		DefaultMaxTurns           int  `json:"defaultMaxTurns"`
		DefaultMaxMinutes         int  `json:"defaultMaxMinutes"`
	} `json:"workflow"`
	ArtifactKeepCount int `json:"artifactKeepCount"`
}

// GetSettingsView 组装当前平台全局设置的脱敏视图。
// 参数 caFingerprint 为启动加载的根 CA 指纹；返回 SystemSettingsView。
func (s *Service) GetSettingsView(caFingerprint string) SystemSettingsView {
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
			DefaultTokenBudget:        s.cfg.Workflow.DefaultTokenBudget,
			DefaultMaxTurns:           s.cfg.Workflow.DefaultMaxTurns,
			DefaultMaxMinutes:         s.cfg.Workflow.DefaultMaxMinutes,
		},
		PKI: PKISettingsView{
			CAFingerprint:     caFingerprint,
			PublicListenAddr:  s.cfg.PublicListenAddr,
			TailnetListenAddr: s.cfg.TailnetListenAddr,
			ServerSignPubB64:  s.cfg.ServerSignPubB64,
		},
		ArtifactKeepCount: s.cfg.ArtifactKeepCount,
	}
}

// UpdateSettings 更新内存配置、写入 settings 表并同步回写 YAML 配置文件。
// 参数 in 为修改内容，actor 为管理员账号；成功返回 nil，失败返回错误。
func (s *Service) UpdateSettings(in UpdateSettingsInput, actor string) error {
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
