// Package service 实现部署目标管理、确认部署与签名动作票据签发、手动回滚与手动数据库备份、分步进度上报及全启用目标成功联动上线。
package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"autoship/internal/config"
	agentSvc "autoship/internal/modules/agent/service"
	"autoship/internal/modules/deploy/model"
	"autoship/internal/modules/deploy/repository"
	notifySvc "autoship/internal/modules/notify/service"
	prodSvc "autoship/internal/modules/product/service"
	relModel "autoship/internal/modules/release/model"
	"autoship/pkg/crypto"
	"autoship/pkg/desensitize"
	"autoship/pkg/envelope"
)

// ReleaseSuccessNotifier 解耦所有启用目标部署成功后通知 release 模块更新版本与需求状态的回调接口。
type ReleaseSuccessNotifier interface {
	OnAllEnabledTargetsSucceeded(releaseID uint64)
}

// Service 封装部署中心核心业务逻辑。
type Service struct {
	repo        *repository.Repository
	cfg         *config.ServerConfig
	encKey      []byte
	products    *prodSvc.Service
	agents      *agentSvc.Service
	notifier    *notifySvc.Notifier
	relNotifier ReleaseSuccessNotifier
}

// New 创建部署中心服务实例。
// 参数 repo 为部署仓储，cfg/encKey 为服务端票据签名配置，products/agents/notifier 为协作服务；返回 *Service。
func New(
	repo *repository.Repository,
	cfg *config.ServerConfig,
	encKey []byte,
	products *prodSvc.Service,
	agents *agentSvc.Service,
	notifier *notifySvc.Notifier,
) *Service {
	return &Service{
		repo:     repo,
		cfg:      cfg,
		encKey:   encKey,
		products: products,
		agents:   agents,
		notifier: notifier,
	}
}

// SetReleaseNotifier 注入版本全部目标部署成功回调。
// 参数 rn 为版本通知接口；无返回值。
func (s *Service) SetReleaseNotifier(rn ReleaseSuccessNotifier) {
	s.relNotifier = rn
}

// GetTargetBasic 实现 agent.TargetLookup 接口：返回指定部署目标的产品 ID、产品编码与备份字节配额。
// 参数 targetID 为目标主键；返回基础元数据与是否存在标志。
func (s *Service) GetTargetBasic(targetID uint64) (uint64, string, int64, bool, error) {
	tgt, err := s.repo.GetTargetByID(targetID)
	if err != nil || tgt == nil {
		return 0, "", 0, false, err
	}
	prod, _ := s.products.GetByID(tgt.ProductID)
	pcode := ""
	if prod != nil {
		pcode = prod.Code
	}
	return tgt.ProductID, pcode, tgt.BackupQuotaMB * 1024 * 1024, true, nil
}

// ListTargets 查询部署目标列表。
// 参数 productID 为可选产品 ID；返回目标切片与错误。
func (s *Service) ListTargets(productID uint64) ([]model.DeployTarget, error) {
	return s.repo.ListTargets(productID)
}

// CreateTarget 新建部署目标。
// 参数 in 为目标参数；返回创建后的对象与错误。
func (s *Service) CreateTarget(in *model.DeployTarget) error {
	if in.ProductID == 0 || strings.TrimSpace(in.Name) == "" {
		return errors.New("请指定所属产品并填写部署目标名称")
	}
	if in.BackupQuotaMB <= 0 {
		in.BackupQuotaMB = 10240
	}
	if in.BackupRetainCount <= 0 {
		in.BackupRetainCount = 5
	}
	return s.repo.CreateTarget(in)
}

// UpdateTarget 修改部署目标配置。
// 参数 id 为目标主键，in 为新属性；返回更新后的目标与错误。
func (s *Service) UpdateTarget(id uint64, in model.DeployTarget) (*model.DeployTarget, error) {
	existing, err := s.repo.GetTargetByID(id)
	if err != nil || existing == nil {
		return nil, errors.New("部署目标不存在")
	}
	if strings.TrimSpace(in.Name) != "" {
		existing.Name = strings.TrimSpace(in.Name)
	}
	if in.Environment != "" {
		existing.Environment = in.Environment
	}
	existing.ServerIP = strings.TrimSpace(in.ServerIP)
	if in.HealthCheckURL != "" {
		existing.HealthCheckURL = strings.TrimSpace(in.HealthCheckURL)
	}
	if in.BackupQuotaMB > 0 {
		existing.BackupQuotaMB = in.BackupQuotaMB
	}
	if in.BackupRetainCount > 0 {
		existing.BackupRetainCount = in.BackupRetainCount
	}
	existing.Enabled = in.Enabled
	if err := s.repo.SaveTarget(existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// DeleteTarget 删除部署目标。
// 参数 id 为目标主键；返回删除错误。
func (s *Service) DeleteTarget(id uint64) error {
	return s.repo.DeleteTarget(id)
}

// ListDeploymentsAndTargets 实现 release.DeployBridge 接口：读取某版本的历史部署与可选部署目标列表。
// 参数 productID/releaseID 为关联 ID；返回部署切片、目标切片与最新部署状态。
func (s *Service) ListDeploymentsAndTargets(productID, releaseID uint64) (any, any, string) {
	deps, _, _ := s.repo.ListDeployments(productID, 0, releaseID, "", 1, 50)
	targets, _ := s.repo.ListTargets(productID)
	latest := ""
	if len(deps) > 0 {
		latest = deps[0].Status
	}
	return deps, targets, latest
}

// ConfirmDeployments 实现 release.DeployBridge 接口：生成由 Server Ed25519 私钥签名的升级票据，并向每个选定目标的更新 Agent 下发升级指令（9.4 & 9.5 节）。
// 参数 rel 为目标版本，art 为落地制品，targetIDs 为选定目标列表（空则默认该产品全部启用目标），manualBackupDB 为管理员手动勾选备份，actor 为确认管理员；返回创建的部署单列表与错误。
func (s *Service) ConfirmDeployments(rel *relModel.Release, art *relModel.ReleaseArtifact, targetIDs []uint64, manualBackupDB bool, actor string) (any, error) {
	prod, err := s.products.GetByID(rel.ProductID)
	if err != nil || prod == nil {
		return nil, errors.New("关联产品不存在")
	}
	allTargets, err := s.repo.ListTargets(rel.ProductID)
	if err != nil || len(allTargets) == 0 {
		return nil, errors.New("该产品尚未配置任何部署目标")
	}

	selected := make([]model.DeployTarget, 0)
	if len(targetIDs) == 0 {
		for _, t := range allTargets {
			if t.Enabled {
				selected = append(selected, t)
			}
		}
	} else {
		want := make(map[uint64]bool, len(targetIDs))
		for _, id := range targetIDs {
			want[id] = true
		}
		for _, t := range allTargets {
			if want[t.ID] {
				selected = append(selected, t)
			}
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("未找到任何可下发的启用部署目标")
	}

	// 9.5 节：迁移风险为 data_rewrite 或管理员在弹窗勾选时强制先备份数据库
	needBackup := manualBackupDB || rel.MigrationRisk == "data_rewrite"

	privB64, err := crypto.DecryptField(s.cfg.ServerSignPrivEnc, s.encKey)
	if err != nil || privB64 == "" {
		return nil, errors.New("读取 Server 动作票据签名私钥失败")
	}

	now := time.Now()
	created := make([]model.Deployment, 0, len(selected))
	for _, tgt := range selected {
		depUID := fmt.Sprintf("dep_%d_%s", rel.ID, strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
		initStatus := "deploying"
		if needBackup {
			initStatus = "backing_up"
		}
		dep := model.Deployment{
			DeploymentUID:   depUID,
			ProductID:       rel.ProductID,
			ReleaseID:       rel.ID,
			TargetID:        tgt.ID,
			TargetVersion:   rel.Version,
			PreviousVersion: tgt.CurrentVersion,
			BackupDB:        needBackup,
			Status:          initStatus,
			ConfirmedBy:     actor,
			StartedAt:       &now,
		}
		if err := s.repo.CreateDeployment(&dep); err != nil {
			return nil, err
		}

		ticket := envelope.ActionTicket{
			TicketID:     "tkt_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
			Action:       "upgrade",
			ProductCode:  prod.Code,
			TargetID:     tgt.ID,
			DeploymentID: dep.DeploymentUID,
			ReleaseID:    rel.ID,
			Version:      rel.Version,
			BackupDB:     needBackup,
			IssuedAt:     now.Unix(),
			ExpiresAt:    now.Add(2 * time.Hour).Unix(),
		}
		if err := ticket.Sign(privB64); err != nil {
			return nil, err
		}

		cmdPayload := map[string]any{
			"ticket":       ticket,
			"bundleSize":   art.SizeBytes,
			"bundleSha256": art.SHA256,
			"downloadPath": fmt.Sprintf("/agent/v1/deploy/artifacts/%d/download", rel.ID),
		}
		_, _ = s.agents.DispatchToTargetUpdateAgent(tgt.ID, "cmd.deploy.upgrade", cmdPayload, 2*time.Hour)
		created = append(created, dep)
	}
	return created, nil
}

// TriggerManualRollback 在管理后台针对某次历史部署下发按 deployment_id 定位的签名回滚指令（9.3 节）。
// 参数 deploymentID 为部署记录主键，reason 为回滚说明，actor 为管理员；返回更新后的部署单与错误。
func (s *Service) TriggerManualRollback(deploymentID uint64, reason, actor string) (*model.Deployment, error) {
	dep, err := s.repo.GetDeploymentByID(deploymentID)
	if err != nil || dep == nil {
		return nil, errors.New("部署记录不存在")
	}
	prod, err := s.products.GetByID(dep.ProductID)
	if err != nil || prod == nil {
		return nil, errors.New("关联产品不存在")
	}
	privB64, err := crypto.DecryptField(s.cfg.ServerSignPrivEnc, s.encKey)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	ticket := envelope.ActionTicket{
		TicketID:     "tkt_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Action:       "rollback",
		ProductCode:  prod.Code,
		TargetID:     dep.TargetID,
		DeploymentID: dep.DeploymentUID,
		ReleaseID:    dep.ReleaseID,
		Version:      dep.TargetVersion,
		IssuedAt:     now.Unix(),
		ExpiresAt:    now.Add(1 * time.Hour).Unix(),
	}
	if err := ticket.Sign(privB64); err != nil {
		return nil, err
	}
	if _, err := s.agents.DispatchToTargetUpdateAgent(dep.TargetID, "cmd.deploy.rollback", map[string]any{
		"ticket": ticket,
		"reason": reason,
		"actor":  actor,
	}, time.Hour); err != nil {
		return nil, err
	}
	_ = s.repo.UpsertStep(&model.DeploymentStep{
		ProductID:    dep.ProductID,
		DeploymentID: dep.ID,
		StepName:     "manual_rollback",
		Status:       "running",
		Message:      "管理员手动下发按部署 ID 回滚指令：" + reason,
	})
	return dep, nil
}

// TriggerManualBackup 在管理后台手动触发某个部署目标立即执行一次数据库备份（9.3 节）。
// 参数 targetID 为部署目标主键；返回生成的备份标识 UID 与错误。
func (s *Service) TriggerManualBackup(targetID uint64) (string, error) {
	tgt, err := s.repo.GetTargetByID(targetID)
	if err != nil || tgt == nil {
		return "", errors.New("部署目标不存在")
	}
	prod, err := s.products.GetByID(tgt.ProductID)
	if err != nil || prod == nil {
		return "", errors.New("关联产品不存在")
	}
	privB64, err := crypto.DecryptField(s.cfg.ServerSignPrivEnc, s.encKey)
	if err != nil {
		return "", err
	}
	backupUID := "bk_" + strings.ReplaceAll(uuid.NewString()[:10], "-", "")
	now := time.Now()
	ticket := envelope.ActionTicket{
		TicketID:     "tkt_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Action:       "backup",
		ProductCode:  prod.Code,
		TargetID:     tgt.ID,
		DeploymentID: backupUID,
		Version:      chooseVer(tgt.CurrentVersion, "v0.0.0"),
		BackupDB:     true,
		IssuedAt:     now.Unix(),
		ExpiresAt:    now.Add(1 * time.Hour).Unix(),
	}
	if err := ticket.Sign(privB64); err != nil {
		return "", err
	}
	if _, err := s.agents.DispatchToTargetUpdateAgent(tgt.ID, "cmd.deploy.backup", map[string]any{
		"ticket": ticket,
	}, time.Hour); err != nil {
		return "", err
	}
	return backupUID, nil
}

// AgentReportInput 定义更新 Agent 上报分步进度、数据库备份结果及终态故障详情的请求体（9.4 第 9 步）。
type AgentReportInput struct {
	CommandID      string `json:"commandId"`
	Status         string `json:"status"` // backing_up | deploying | succeeded | rolled_back | rollback_failed
	StepName       string `json:"stepName"`
	StepStatus     string `json:"stepStatus"` // running | succeeded | failed
	Message        string `json:"message"`
	ExitCode       int    `json:"exitCode"`
	ErrorTail      string `json:"errorTail"`
	LogTail        string `json:"logTail"`
	HealthResponse string `json:"healthResponse"`
	BackupInfo     *struct {
		DirPath      string `json:"dirPath"`
		TotalBytes   int64  `json:"totalBytes"`
		FileListJSON string `json:"fileListJson"`
		Checksum     string `json:"checksum"`
	} `json:"backupInfo,omitempty"`
}

// HandleAgentReport 处理已鉴权更新 Agent 上报的进度与终态（校验该 Agent 仅能上报其绑定的 deployTargetId 记录），并在全部启用目标成功时推进版本与需求上线。
// 参数 agentTargetID/agentProductID 为更新 Agent 授权范围，deploymentUID 为安全部署标识，in 为上报详情；返回处理错误。
func (s *Service) HandleAgentReport(agentTargetID, agentProductID uint64, deploymentUID string, in AgentReportInput) error {
	// 支持独立手动备份（无 deployment 表记录，仅落库 db_backups）
	if strings.HasPrefix(deploymentUID, "bk_") {
		if in.BackupInfo != nil && in.BackupInfo.DirPath != "" {
			_ = s.repo.SaveBackup(&model.DBBackup{
				ProductID:     agentProductID,
				TargetID:      agentTargetID,
				DeploymentUID: deploymentUID,
				BackupDir:     in.BackupInfo.DirPath,
				FileListJSON:  in.BackupInfo.FileListJSON,
				SizeBytes:     in.BackupInfo.TotalBytes,
				Checksum:      in.BackupInfo.Checksum,
			})
		}
		if in.CommandID != "" && (in.StepStatus == "succeeded" || in.StepStatus == "failed") {
			_ = s.agents.MarkCommandStatus(in.CommandID, in.StepStatus, in.ErrorTail)
		}
		return nil
	}

	dep, err := s.repo.GetDeploymentByUID(deploymentUID)
	if err != nil || dep == nil {
		return errors.New("指定的部署记录不存在")
	}
	if dep.TargetID != agentTargetID {
		return errors.New("禁止跨部署目标越权上报进度")
	}

	if in.StepName != "" {
		_ = s.repo.UpsertStep(&model.DeploymentStep{
			ProductID:    dep.ProductID,
			DeploymentID: dep.ID,
			StepName:     in.StepName,
			Status:       chooseVer(in.StepStatus, "running"),
			Message:      desensitize.MaskSecrets(in.Message),
		})
	}
	if in.BackupInfo != nil && in.BackupInfo.DirPath != "" {
		_ = s.repo.SaveBackup(&model.DBBackup{
			ProductID:     dep.ProductID,
			TargetID:      dep.TargetID,
			DeploymentID:  dep.ID,
			DeploymentUID: dep.DeploymentUID,
			BackupDir:     in.BackupInfo.DirPath,
			FileListJSON:  in.BackupInfo.FileListJSON,
			SizeBytes:     in.BackupInfo.TotalBytes,
			Checksum:      in.BackupInfo.Checksum,
		})
	}

	if in.Status != "" {
		dep.Status = in.Status
	}
	if in.StepStatus == "failed" && in.StepName != "" {
		dep.FailedStep = in.StepName
	}
	if in.ExitCode != 0 {
		dep.ExitCode = in.ExitCode
	}
	if in.ErrorTail != "" {
		dep.ErrorTail = desensitize.MaskSecrets(in.ErrorTail)
	}
	if in.LogTail != "" {
		dep.LogTail = desensitize.MaskSecrets(in.LogTail)
	}
	if in.HealthResponse != "" {
		dep.HealthResponse = desensitize.MaskSecrets(in.HealthResponse)
	}

	now := time.Now()
	switch dep.Status {
	case "succeeded":
		dep.FinishedAt = &now
		_ = s.repo.SaveDeployment(dep)
		if tgt, _ := s.repo.GetTargetByID(dep.TargetID); tgt != nil {
			tgt.CurrentVersion = dep.TargetVersion
			_ = s.repo.SaveTarget(tgt)
		}
		if in.CommandID != "" {
			_ = s.agents.MarkCommandStatus(in.CommandID, "succeeded", "")
		}
		// 严格执行多目标规则：当该产品下所有启用的部署目标对本版本均已部署成功时，流转版本与需求状态
		if s.repo.AllEnabledTargetsSucceeded(dep.ProductID, dep.ReleaseID) && s.relNotifier != nil {
			s.relNotifier.OnAllEnabledTargetsSucceeded(dep.ReleaseID)
		}
		return nil

	case "rolled_back":
		dep.FinishedAt = &now
		_ = s.repo.SaveDeployment(dep)
		if in.CommandID != "" {
			_ = s.agents.MarkCommandStatus(in.CommandID, "failed", dep.ErrorTail)
		}
		if s.notifier != nil {
			s.notifier.NotifyAsync(notifySvc.EventDeployFailed,
				fmt.Sprintf("部署失败已自动回滚（%s）", dep.TargetVersion),
				fmt.Sprintf("失败步骤：%s ｜ 退出码：%d\n错误摘要：%s", dep.FailedStep, dep.ExitCode, dep.ErrorTail),
			)
		}
		return nil

	case "rollback_failed":
		dep.FinishedAt = &now
		_ = s.repo.SaveDeployment(dep)
		if in.CommandID != "" {
			_ = s.agents.MarkCommandStatus(in.CommandID, "failed", dep.ErrorTail)
		}
		if s.notifier != nil {
			s.notifier.NotifyAsync(notifySvc.EventRollbackFailedUrgent,
				fmt.Sprintf("部署目标 #%d 升级 %s 失败且自动回滚也失败！", dep.TargetID, dep.TargetVersion),
				fmt.Sprintf("请立即人工介入处理！失败步骤：%s ｜ 错误：%s", dep.FailedStep, dep.ErrorTail),
			)
		}
		return nil
	}

	return s.repo.SaveDeployment(dep)
}

// ListDeployments 分页查询部署单。
// 参数 productID/targetID/status/page/pageSize 为条件；返回列表、总数与错误。
func (s *Service) ListDeployments(productID, targetID uint64, status string, page, pageSize int) ([]model.Deployment, int64, error) {
	return s.repo.ListDeployments(productID, targetID, 0, status, page, pageSize)
}

// DeploymentDetailBundle 聚合单次部署详情、分步日志与关联数据库备份。
type DeploymentDetailBundle struct {
	Deployment *model.Deployment      `json:"deployment"`
	Steps      []model.DeploymentStep `json:"steps"`
	Backups    []model.DBBackup       `json:"backups"`
}

// GetDeploymentDetail 查询单次部署的完整分步日志与备份记录。
// 参数 id 为部署主键；不存在返回 (nil, nil)。
func (s *Service) GetDeploymentDetail(id uint64) (*DeploymentDetailBundle, error) {
	dep, err := s.repo.GetDeploymentByID(id)
	if err != nil || dep == nil {
		return nil, err
	}
	steps, _ := s.repo.ListSteps(id)
	bks, _ := s.repo.ListBackups(dep.ProductID, dep.TargetID, dep.ID)
	return &DeploymentDetailBundle{
		Deployment: dep,
		Steps:      steps,
		Backups:    bks,
	}, nil
}

// ListBackups 查询数据库备份记录列表。
// 参数 productID/targetID 为可选筛选；返回备份数组与错误。
func (s *Service) ListBackups(productID, targetID uint64) ([]model.DBBackup, error) {
	return s.repo.ListBackups(productID, targetID, 0)
}

// chooseVer 返回首个非空白字符串，若首个为空白则返回兜底默认值；参数 a 为优先值，b 为默认值；返回非空文本。
func chooseVer(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
