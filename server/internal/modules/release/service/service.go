// Package service 实现版本定号、发布指令派发、分片续传 Ed25519 清单验签、推 tag 前后失败分流与一键新建关联修复任务。
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentSvc "autoship/internal/modules/agent/service"
	artSvc "autoship/internal/modules/artifact/service"
	notifySvc "autoship/internal/modules/notify/service"
	prodSvc "autoship/internal/modules/product/service"
	"autoship/internal/modules/release/model"
	"autoship/internal/modules/release/repository"
	reqModel "autoship/internal/modules/requirement/model"
	workModel "autoship/internal/modules/work/model"
	"autoship/pkg/manifest"
	"autoship/pkg/semver"
)

// WorkFixBridge 解耦版本发布失败时回调开发任务模块处理冲突回退或新建关联修复任务的接口。
type WorkFixBridge interface {
	ReturnToCodingOnPreTagConflict(workItemID uint64, reason string)
	CreateFixWorkForFailedRelease(productID, failedReleaseID uint64, reqIDs []uint64, failedVer, note string) (uint64, error)
}

// DeployBridge 解耦后台确认部署与查询版本关联部署记录的接口。
type DeployBridge interface {
	ConfirmDeployments(rel *model.Release, art *model.ReleaseArtifact, targetIDs []uint64, backupDB bool, actor string) (any, error)
	ListDeploymentsAndTargets(productID, releaseID uint64) (deployments any, targets any, latestStatus string)
}

// RequirementLookup 解耦查询版本包含需求详情与通知需求已上线的接口。
type RequirementLookup interface {
	GetByIDs(ids []uint64) ([]reqModel.Requirement, error)
	OnReleaseDeployedAllTargets(reqIDs []uint64, version string)
}

// Service 封装产品版本生命周期管理。
type Service struct {
	repo       *repository.Repository
	artifacts  *artSvc.Service
	products   *prodSvc.Service
	agents     *agentSvc.Service
	notifier   *notifySvc.Notifier
	workBridge WorkFixBridge
	depBridge  DeployBridge
	reqLookup  RequirementLookup
}

// New 创建版本管理服务实例。
// 参数 repo 为版本仓储，artifacts/products/agents/notifier 为协作服务；返回 *Service。
func New(
	repo *repository.Repository,
	artifacts *artSvc.Service,
	products *prodSvc.Service,
	agents *agentSvc.Service,
	notifier *notifySvc.Notifier,
) *Service {
	return &Service{
		repo:      repo,
		artifacts: artifacts,
		products:  products,
		agents:    agents,
		notifier:  notifier,
	}
}

// SetBridges 注入开发任务桥接器、部署桥接器与需求查询器。
// 参数 wb/db/rl 为各协作接口；无返回值。
func (s *Service) SetBridges(wb WorkFixBridge, db DeployBridge, rl RequirementLookup) {
	s.workBridge = wb
	s.depBridge = db
	s.reqLookup = rl
}

// CreatePublishingRelease 实现 work.ReleaseCreator 接口：验收通过时自动按需求类型计算新版本号（支持弹窗覆盖），创建 publishing 版本并向开发 Agent 下发发布指令。
// 参数 workItem 为已通过的开发任务，reqs 为包含的需求列表，customVersion 为可选指定版本，actor 为管理员；返回 releaseID、版本号与错误。
func (s *Service) CreatePublishingRelease(workItem *workModel.WorkItem, reqs []reqModel.Requirement, customVersion string, actor string) (uint64, string, error) {
	hasFeature := false
	reqIDs := make([]uint64, 0, len(reqs))
	for _, r := range reqs {
		reqIDs = append(reqIDs, r.ID)
		if r.IssueType != "bug" {
			hasFeature = true
		}
	}
	ver := strings.TrimSpace(customVersion)
	if ver == "" {
		latest := s.repo.LatestSemanticVersion(workItem.ProductID)
		nextVer, err := semver.Next(latest, hasFeature)
		if err != nil {
			return 0, "", err
		}
		ver = nextVer
	} else {
		parsed, err := semver.Parse(ver)
		if err != nil {
			return 0, "", err
		}
		ver = parsed.String()
	}

	reqIDsBytes, _ := json.Marshal(reqIDs)
	rel := &model.Release{
		ProductID:          workItem.ProductID,
		Version:            ver,
		TagName:            ver,
		WorkItemID:         workItem.ID,
		RequirementIDsJSON: string(reqIDsBytes),
		CommitSHA:          workItem.AcceptedCommitSHA,
		MigrationRisk:      workItem.MigrationRisk,
		Status:             "publishing",
		CreatedBy:          actor,
	}
	if err := s.repo.Create(rel); err != nil {
		return 0, "", fmt.Errorf("创建版本记录失败（请确认版本号 %s 未重复）: %w", ver, err)
	}

	bundle, _ := s.products.GetFullBundle(workItem.ProductID)
	_, _, _ = s.agents.DispatchToDevAgent("cmd.release.publish", map[string]any{
		"releaseId":         rel.ID,
		"workItemId":        workItem.ID,
		"branchName":        workItem.BranchName,
		"version":           rel.Version,
		"tagName":           rel.TagName,
		"acceptedCommitSha": workItem.AcceptedCommitSHA,
		"migrationRisk":     workItem.MigrationRisk,
		"productBundle":     bundle,
		"requirements":      reqs,
	}, 24*time.Hour)

	return rel.ID, rel.Version, nil
}

// EnsureReleaseForManualUpload 在阶段 1 使用独立 Mac 发布工具上传制品包时，若尚无预创建的 release 记录则自动创建一条 publishing 版本供分片上传使用。
// 参数 m 为已签名的制品清单；返回对应的 *model.Release 与错误。
func (s *Service) EnsureReleaseForManualUpload(m manifest.ReleaseManifest) (*model.Release, error) {
	prod, err := s.products.GetByCode(m.ProductCode)
	if err != nil || prod == nil {
		return nil, errors.New("清单对应的产品编码不存在")
	}
	ver, err := semver.Parse(m.Version)
	if err != nil {
		return nil, err
	}
	rel := &model.Release{
		ProductID:          prod.ID,
		Version:            ver.String(),
		TagName:            m.Tag,
		RequirementIDsJSON: "[]",
		CommitSHA:          m.CommitSHA,
		Changelog:          m.Changelog,
		MigrationRisk:      string(m.MigrationRisk),
		MinAgentVersion:    m.MinAgentVersion,
		TagPushed:          true,
		Status:             "publishing",
		CreatedBy:          "release-cli",
	}
	if err := s.repo.Create(rel); err != nil {
		return nil, err
	}
	return rel, nil
}

// InitBundleUpload 校验待上传制品包清单的 Ed25519 数字签名（对照产品档案登记的发布公钥）并开启分片续传会话。
// 参数 releaseID 为版本 ID（0 表示由清单自动建单），totalSize/sha256Hex 为整包信息，m 为签名清单；返回实际 releaseID、uploadID 与错误。
func (s *Service) InitBundleUpload(releaseID uint64, totalSize int64, sha256Hex string, m manifest.ReleaseManifest) (uint64, string, error) {
	var rel *model.Release
	var err error
	if releaseID == 0 {
		rel, err = s.EnsureReleaseForManualUpload(m)
		if err != nil {
			return 0, "", err
		}
		releaseID = rel.ID
	} else {
		rel, err = s.repo.GetByID(releaseID)
		if err != nil || rel == nil {
			return 0, "", errors.New("目标版本记录不存在")
		}
	}

	prod, err := s.products.GetByID(rel.ProductID)
	if err != nil || prod == nil {
		return 0, "", errors.New("产品档案不存在")
	}
	if prod.Code != m.ProductCode {
		return 0, "", errors.New("制品清单产品编码与版本归属产品不一致")
	}
	if strings.TrimSpace(prod.ReleaseSignPubB64) == "" {
		return 0, "", errors.New("该产品尚未配置发布签名公钥（releaseSignPubB64），拒绝接收未验签制品")
	}
	if err := m.Verify(prod.ReleaseSignPubB64); err != nil {
		return 0, "", fmt.Errorf("制品包清单 Ed25519 验签失败: %w", err)
	}

	mRaw, _ := json.Marshal(m)
	uploadID, err := s.artifacts.InitUpload(rel.ID, prod.Code, rel.Version, totalSize, sha256Hex, string(mRaw))
	if err != nil {
		return 0, "", err
	}
	return rel.ID, uploadID, nil
}

// UploadChunk 写入制品包单个分片数据。
// 参数 releaseID/uploadID/index/data 为分片参数；返回写入字节数与错误。
func (s *Service) UploadChunk(releaseID uint64, uploadID string, index int, data []byte) (int64, error) {
	return s.artifacts.WriteChunk(releaseID, uploadID, index, data)
}

// CompleteBundleUpload 完成分片合并与哈希核验，将版本状态推进为 pending_deploy（待部署），同步项目地图并发送飞书通知（8.10 第 8-9 步）。
// 参数 releaseID/uploadID 为上传标识，ciRunURL 为可选 CI 链接，projectMap 为刷新后的项目地图；返回更新后的 *model.Release 与错误。
func (s *Service) CompleteBundleUpload(releaseID uint64, uploadID, ciRunURL, projectMap string) (*model.Release, error) {
	rel, err := s.repo.GetByID(releaseID)
	if err != nil || rel == nil {
		return nil, errors.New("版本记录不存在")
	}
	relPath, size, hashHex, manifestJSON, err := s.artifacts.CompleteUpload(releaseID, uploadID)
	if err != nil {
		return nil, err
	}

	var m manifest.ReleaseManifest
	_ = json.Unmarshal([]byte(manifestJSON), &m)

	now := time.Now()
	rel.CommitSHA = m.CommitSHA
	rel.Changelog = m.Changelog
	rel.MigrationRisk = string(m.MigrationRisk)
	rel.MinAgentVersion = m.MinAgentVersion
	rel.ManifestJSON = manifestJSON
	rel.SignatureB64 = m.SignatureB64
	rel.TagPushed = true
	if ciRunURL != "" {
		rel.CIRunURL = ciRunURL
	}
	rel.Status = "pending_deploy"
	rel.PublishedAt = &now
	if err := s.repo.Save(rel); err != nil {
		return nil, err
	}

	art := &model.ReleaseArtifact{
		ProductID:   rel.ProductID,
		ReleaseID:   rel.ID,
		FileName:    fmt.Sprintf("%s-%s-bundle.tar.gz", m.ProductCode, rel.Version),
		StoragePath: relPath,
		SizeBytes:   size,
		SHA256:      hashHex,
	}
	_ = s.repo.SaveArtifact(art)

	if strings.TrimSpace(projectMap) != "" {
		_ = s.products.SyncProjectMap(rel.ProductID, projectMap)
	}
	if s.notifier != nil {
		s.notifier.NotifyAsync(notifySvc.EventReleaseReady,
			fmt.Sprintf("版本 %s 已发布，待确认部署", rel.Version),
			fmt.Sprintf("迁移风险等级：%s ｜ Commit：%s\n请登录管理后台确认部署。", rel.MigrationRisk, rel.CommitSHA),
		)
	}
	return rel, nil
}

// ReportPublishFailure 处理开发 Agent 上报的发布阶段失败：推 tag 前冲突退回原开发任务重过验收；推 tag 后 CI 失败标为 publish_failed（不移动已有 tag）。
// 参数 releaseID 为版本主键，tagPushed 表示是否已推送 tag，hasConflict 表示是否 rebase 冲突，ciRunURL/reason 为失败详情；返回更新后的版本与错误。
func (s *Service) ReportPublishFailure(releaseID uint64, tagPushed, hasConflict bool, ciRunURL, reason string) (*model.Release, error) {
	rel, err := s.repo.GetByID(releaseID)
	if err != nil || rel == nil {
		return nil, errors.New("版本不存在")
	}
	rel.Status = "publish_failed"
	rel.TagPushed = tagPushed
	rel.FailureReason = reason
	if ciRunURL != "" {
		rel.CIRunURL = ciRunURL
	}
	_ = s.repo.Save(rel)

	if !tagPushed && hasConflict && rel.WorkItemID > 0 && s.workBridge != nil {
		s.workBridge.ReturnToCodingOnPreTagConflict(rel.WorkItemID, reason)
	}
	if s.notifier != nil {
		s.notifier.NotifyAsync(notifySvc.EventPublishFailed,
			fmt.Sprintf("版本 %s 发布失败", rel.Version),
			reason,
		)
	}
	return rel, nil
}

// CreateFixWorkForRelease 在管理后台对处于 publish_failed 的版本一键新建一条关联修复开发任务（落实用户已确认方案，修复后以新版本号重发）。
// 参数 releaseID 为失败版本主键，note 为补充说明；返回新建的修复任务 ID 与错误。
func (s *Service) CreateFixWorkForRelease(releaseID uint64, note string) (uint64, error) {
	rel, err := s.repo.GetByID(releaseID)
	if err != nil || rel == nil {
		return 0, errors.New("版本不存在")
	}
	if rel.Status != "publish_failed" {
		return 0, errors.New("仅发布失败的版本支持新建关联修复任务")
	}
	if s.workBridge == nil {
		return 0, errors.New("开发任务桥接器未初始化")
	}
	var reqIDs []uint64
	_ = json.Unmarshal([]byte(rel.RequirementIDsJSON), &reqIDs)
	fixID, err := s.workBridge.CreateFixWorkForFailedRelease(rel.ProductID, rel.ID, reqIDs, rel.Version, note)
	if err != nil {
		return 0, err
	}
	rel.FixWorkItemID = fixID
	_ = s.repo.Save(rel)
	return fixID, nil
}

// OnAllEnabledTargetsSucceeded 当某版本在该产品所有启用的部署目标上均部署成功后回调：将版本流转为 deployed（已部署），取代更早的未部署旧版本，并推进包含的需求为 released（已上线）。
// 参数 releaseID 为版本主键；无返回值。
func (s *Service) OnAllEnabledTargetsSucceeded(releaseID uint64) {
	rel, err := s.repo.GetByID(releaseID)
	if err != nil || rel == nil {
		return
	}
	now := time.Now()
	rel.Status = "deployed"
	rel.DeployedAt = &now
	_ = s.repo.Save(rel)

	s.repo.SupersedeOlderPendingReleases(rel.ProductID, rel.Version)

	var reqIDs []uint64
	_ = json.Unmarshal([]byte(rel.RequirementIDsJSON), &reqIDs)
	if s.reqLookup != nil && len(reqIDs) > 0 {
		s.reqLookup.OnReleaseDeployedAllTargets(reqIDs, rel.Version)
	}
}

// GetReleaseAndLatestDeployStatus 实现 work.ReleaseCreator 接口。
// 参数 releaseID 为版本主键；返回版本状态与最新部署状态。
func (s *Service) GetReleaseAndLatestDeployStatus(releaseID uint64) (string, string) {
	rel, err := s.repo.GetByID(releaseID)
	if err != nil || rel == nil {
		return "", ""
	}
	var depStatus string
	if s.depBridge != nil {
		_, _, depStatus = s.depBridge.ListDeploymentsAndTargets(rel.ProductID, rel.ID)
	}
	return rel.Status, depStatus
}

// List 分页查询版本列表。
// 参数 productID/status/page/pageSize 为筛选条件；返回版本切片、总数与错误。
func (s *Service) List(productID uint64, status string, page, pageSize int) ([]model.Release, int64, error) {
	return s.repo.List(productID, status, page, pageSize)
}

// ReleaseDetailBundle 聚合版本详情与“确认部署”弹窗所需的全部数据（变更日志、需求、制品、历史部署及目标当前版本）。
type ReleaseDetailBundle struct {
	Release       *model.Release          `json:"release"`
	Artifacts     []model.ReleaseArtifact `json:"artifacts"`
	Requirements  []reqModel.Requirement  `json:"requirements"`
	Deployments   any                     `json:"deployments"`
	DeployTargets any                     `json:"deployTargets"`
}

// GetDetail 查询单个版本的完整详情。
// 参数 id 为版本主键；不存在返回 (nil, nil)。
func (s *Service) GetDetail(id uint64) (*ReleaseDetailBundle, error) {
	rel, err := s.repo.GetByID(id)
	if err != nil || rel == nil {
		return nil, err
	}
	var arts []model.ReleaseArtifact
	if art, _ := s.repo.GetArtifact(id); art != nil {
		arts = append(arts, *art)
	}
	var reqIDs []uint64
	_ = json.Unmarshal([]byte(rel.RequirementIDsJSON), &reqIDs)
	var reqs []reqModel.Requirement
	if s.reqLookup != nil && len(reqIDs) > 0 {
		reqs, _ = s.reqLookup.GetByIDs(reqIDs)
	}
	var deps, targets any
	if s.depBridge != nil {
		deps, targets, _ = s.depBridge.ListDeploymentsAndTargets(rel.ProductID, rel.ID)
	}
	return &ReleaseDetailBundle{
		Release:       rel,
		Artifacts:     arts,
		Requirements:  reqs,
		Deployments:   deps,
		DeployTargets: targets,
	}, nil
}

// ConfirmDeploy 在后台弹窗点“确认部署”后校验版本状态并委托 deploy 模块签发动作票据下发更新 Agent。
// 参数 releaseID 为版本主键，targetIDs 为选定目标列表（为空默认该产品全部启用目标），backupDB 为手动勾选先备份数据库，actor 为管理员；返回创建的部署单列表与错误。
func (s *Service) ConfirmDeploy(releaseID uint64, targetIDs []uint64, backupDB bool, actor string) (any, error) {
	rel, err := s.repo.GetByID(releaseID)
	if err != nil || rel == nil {
		return nil, errors.New("版本不存在")
	}
	if rel.Status != "pending_deploy" && rel.Status != "deployed" {
		return nil, fmt.Errorf("当前版本状态为 %s，不可执行部署下发", rel.Status)
	}
	art, err := s.repo.GetArtifact(releaseID)
	if err != nil || art == nil {
		return nil, errors.New("该版本尚未上传有效签名制品包")
	}
	if s.depBridge == nil {
		return nil, errors.New("部署模块未就绪")
	}
	return s.depBridge.ConfirmDeployments(rel, art, targetIDs, backupDB, actor)
}

// GetArtifactForTarget 校验请求下载制品包的更新 Agent 所属产品与该版本归属产品严格一致（7.3 第 3 步按范围授权）。
// 参数 releaseID 为版本主键，agentProductID 为更新 Agent 绑定的产品 ID；返回 *model.ReleaseArtifact 与错误。
func (s *Service) GetArtifactForTarget(releaseID, agentProductID uint64) (*model.ReleaseArtifact, error) {
	rel, err := s.repo.GetByID(releaseID)
	if err != nil || rel == nil {
		return nil, errors.New("目标版本不存在")
	}
	if rel.ProductID != agentProductID {
		return nil, errors.New("禁止跨产品下载制品包")
	}
	art, err := s.repo.GetArtifact(releaseID)
	if err != nil || art == nil {
		return nil, errors.New("制品包记录不存在")
	}
	return art, nil
}
