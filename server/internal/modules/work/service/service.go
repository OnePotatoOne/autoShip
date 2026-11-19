// Package service 实现开发任务编排、方案确认门、ask_owner 答复、三区改动范围与验收包接收、验收通过/打回/放弃及开发 Agent 事件同步。
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentSvc "autoship/internal/modules/agent/service"
	llmModel "autoship/internal/modules/llm/model"
	llmSvc "autoship/internal/modules/llm/service"
	notifySvc "autoship/internal/modules/notify/service"
	prodSvc "autoship/internal/modules/product/service"
	reqModel "autoship/internal/modules/requirement/model"
	runModel "autoship/internal/modules/run/model"
	runSvc "autoship/internal/modules/run/service"
	"autoship/internal/modules/work/model"
	"autoship/internal/modules/work/repository"
	"autoship/pkg/envelope"
)

// RequirementBridge 解耦开发任务与需求模块交互的回调接口。
type RequirementBridge interface {
	GetByIDs(ids []uint64) ([]reqModel.Requirement, error)
	OnWorkAbandoned(reqIDs []uint64, workItemID uint64, actor string)
}

// ReleaseCreator 解耦验收通过后创建版本记录并下发发布指令的接口。
type ReleaseCreator interface {
	CreatePublishingRelease(workItem *model.WorkItem, reqs []reqModel.Requirement, customVersion string, actor string) (uint64, string, error)
	GetReleaseAndLatestDeployStatus(releaseID uint64) (releaseStatus, deployStatus string)
}

// Service 封装开发任务全生命周期编排与开发 Agent 双向消息处理。
type Service struct {
	repo       *repository.Repository
	products   *prodSvc.Service
	agents     *agentSvc.Service
	runs       *runSvc.Service
	llm        *llmSvc.Service
	notifier   *notifySvc.Notifier
	reqBridge  RequirementBridge
	relCreator ReleaseCreator
}

// New 创建开发任务服务实例。
// 参数 repo 为开发任务仓储，products/agents/runs/llm/notifier 为协作模块服务；返回 *Service。
func New(
	repo *repository.Repository,
	products *prodSvc.Service,
	agents *agentSvc.Service,
	runs *runSvc.Service,
	llm *llmSvc.Service,
	notifier *notifySvc.Notifier,
) *Service {
	return &Service{
		repo:     repo,
		products: products,
		agents:   agents,
		runs:     runs,
		llm:      llm,
		notifier: notifier,
	}
}

// SetBridges 注入需求桥接器与版本创建器。
// 参数 rb 为需求回调，rc 为版本回调；无返回值。
func (s *Service) SetBridges(rb RequirementBridge, rc ReleaseCreator) {
	s.reqBridge = rb
	s.relCreator = rc
}

// StartWorkForRequirements 实现 requirement.WorkStarter 接口：创建开发任务，组装完整产品档案与需求规格下发开发 Agent。
// 参数 productID 为产品主键，reqIDs 为关联需求切片，planConfirm 为是否开启方案确认门，tokenBudget/maxTurns/maxMinutes 为预算，actor 为操作人；返回新建任务 ID 与错误。
func (s *Service) StartWorkForRequirements(productID uint64, reqIDs []uint64, planConfirm bool, tokenBudget, maxTurns, maxMinutes int, _ string) (uint64, error) {
	return s.createAndDispatchWork(productID, 0, "", reqIDs, planConfirm, tokenBudget, maxTurns, maxMinutes)
}

// CreateFixWorkForFailedRelease 为推 tag 后发布 CI 失败的版本新建一条关联修复任务（落实用户已确认的发布失败重发策略）。
// 参数 productID/failedReleaseID 为失败版本信息，reqIDs 为原版本包含的需求集合，failedVer/note 为修复说明；返回新建修复开发任务 ID 与错误。
func (s *Service) CreateFixWorkForFailedRelease(productID, failedReleaseID uint64, reqIDs []uint64, failedVer, note string) (uint64, error) {
	title := fmt.Sprintf("修复版本 %s 发布失败问题", failedVer)
	if strings.TrimSpace(note) != "" {
		title += "：" + strings.TrimSpace(note)
	}
	return s.createAndDispatchWork(productID, failedReleaseID, title, reqIDs, true, 200000, 60, 60)
}

// createAndDispatchWork 内部通用创建开发任务并组装产品完整档案投递给开发 Agent；参数为产品ID、父失败版本ID、标题、需求ID列表与预算配置；返回新建开发任务 ID 与错误。
func (s *Service) createAndDispatchWork(productID, parentReleaseID uint64, overrideTitle string, reqIDs []uint64, planConfirm bool, tokenBudget, maxTurns, maxMinutes int) (uint64, error) {
	bundle, err := s.products.GetFullBundle(productID)
	if err != nil || bundle == nil {
		return 0, errors.New("目标产品档案不存在")
	}
	if !bundle.Product.AutoDevEnabled {
		return 0, errors.New("该产品尚未开启自动开发能力")
	}

	var reqs []reqModel.Requirement
	if s.reqBridge != nil && len(reqIDs) > 0 {
		reqs, _ = s.reqBridge.GetByIDs(reqIDs)
	}
	title := overrideTitle
	primaryMod := ""
	if len(reqs) > 0 {
		if title == "" {
			title = reqs[0].Title
		}
		primaryMod = reqs[0].ModuleCode
	}
	if title == "" {
		title = "自动研发任务"
	}
	if tokenBudget <= 0 {
		tokenBudget = 200000
	}
	if maxTurns <= 0 {
		maxTurns = 60
	}
	if maxMinutes <= 0 {
		maxMinutes = 60
	}

	now := time.Now()
	item := &model.WorkItem{
		ProductID:           productID,
		PrimaryModuleCode:   primaryMod,
		ParentReleaseID:     parentReleaseID,
		Title:               title,
		Status:              "preparing",
		PlanConfirmRequired: planConfirm,
		TokenBudget:         tokenBudget,
		MaxTurns:            maxTurns,
		MaxDurationMinutes:  maxMinutes,
		StartedAt:           &now,
	}
	if err := s.repo.CreateWithRequirements(item, reqIDs); err != nil {
		return 0, err
	}
	item.BranchName = fmt.Sprintf("autoship/%d-task", item.ID)
	_ = s.repo.Save(item)

	payload := map[string]any{
		"workItemId":          item.ID,
		"branchName":          item.BranchName,
		"primaryModuleCode":   item.PrimaryModuleCode,
		"parentReleaseId":     item.ParentReleaseID,
		"planConfirmRequired": item.PlanConfirmRequired,
		"budget": map[string]any{
			"tokenBudget":        item.TokenBudget,
			"maxTurns":           item.MaxTurns,
			"maxDurationMinutes": item.MaxDurationMinutes,
		},
		"productBundle": bundle,
		"requirements":  reqs,
	}
	// 若开发 Agent 已注册则入队投递；若本地尚未连接开发 Agent，任务保持 preparing 排队等待上线
	_, _, _ = s.agents.DispatchToDevAgent("cmd.work.start", payload, 72*time.Hour)
	_ = s.runs.RecordActivity(&runModel.RunActivity{
		ProductID:  productID,
		WorkItemID: item.ID,
		Stage:      "prepare",
		Kind:       "info",
		Summary:    "开发任务已创建并进入开发 Agent 投递队列",
	})
	return item.ID, nil
}

// GetLinkedObjectStatuses 实现 requirement.WorkStarter 接口：返回开发任务、版本与部署的各自实时状态。
// 参数 workItemID 为任务主键；返回三段状态字符串。
func (s *Service) GetLinkedObjectStatuses(workItemID uint64) (workStatus, releaseStatus, deployStatus string) {
	item, err := s.repo.GetByID(workItemID)
	if err != nil || item == nil {
		return "", "", ""
	}
	workStatus = item.Status
	if item.ActiveReleaseID > 0 && s.relCreator != nil {
		releaseStatus, deployStatus = s.relCreator.GetReleaseAndLatestDeployStatus(item.ActiveReleaseID)
	}
	return workStatus, releaseStatus, deployStatus
}

// List 分页查询开发任务列表。
// 参数 productID/status/page/pageSize 为查询条件；返回列表、总数与错误。
func (s *Service) List(productID uint64, status string, page, pageSize int) ([]model.WorkItem, int64, error) {
	return s.repo.List(productID, status, page, pageSize)
}

// WorkDetailBundle 聚合开发任务详情视图所需的全部关联记录。
type WorkDetailBundle struct {
	WorkItem       *model.WorkItem         `json:"workItem"`
	Requirements   []reqModel.Requirement  `json:"requirements"`
	Gates          []model.WorkGate        `json:"gates"`
	ScopeChanges   []model.WorkScopeChange `json:"scopeChanges"`
	Activities     []runModel.RunActivity  `json:"activities"`
	Transcripts    []runModel.RunTranscript `json:"transcripts"`
	PreviewSession *model.PreviewSession   `json:"previewSession,omitempty"`
}

// GetDetail 获取单个开发任务完整聚合信息。
// 参数 id 为任务主键；不存在返回 (nil, nil)。
func (s *Service) GetDetail(id uint64) (*WorkDetailBundle, error) {
	item, err := s.repo.GetByID(id)
	if err != nil || item == nil {
		return nil, err
	}
	reqIDs, _ := s.repo.GetRequirementIDs(id)
	var reqs []reqModel.Requirement
	if s.reqBridge != nil && len(reqIDs) > 0 {
		reqs, _ = s.reqBridge.GetByIDs(reqIDs)
	}
	gates, _ := s.repo.ListGates(id)
	scopes, _ := s.repo.ListScopeChanges(id)
	acts, _ := s.runs.ListActivities(id)
	trs, _ := s.runs.ListTranscripts(id)
	prev, _ := s.repo.GetPreviewSession(id)

	return &WorkDetailBundle{
		WorkItem:       item,
		Requirements:   reqs,
		Gates:          gates,
		ScopeChanges:   scopes,
		Activities:     acts,
		Transcripts:    trs,
		PreviewSession: prev,
	}, nil
}

// ConfirmPlan 处理管理员对一页纸技术方案与改动范围的确认或驳回修改（8.5 & 8.6 节）。
// 参数 id 为任务主键，approved 表示是否同意继续编码，comment 为指导意见；返回更新后的任务与错误。
func (s *Service) ConfirmPlan(id uint64, approved bool, comment string) (*model.WorkItem, error) {
	item, err := s.repo.GetByID(id)
	if err != nil || item == nil {
		return nil, errors.New("开发任务不存在")
	}
	if approved {
		item.Status = "coding"
	} else {
		item.Status = "preparing"
	}
	if err := s.repo.Save(item); err != nil {
		return nil, err
	}
	_, _, _ = s.agents.DispatchToDevAgent("cmd.work.plan_decision", map[string]any{
		"workItemId": item.ID,
		"approved":   approved,
		"comment":    comment,
	}, 24*time.Hour)
	return item, nil
}

// AnswerQuestion 处理管理员在后台回答开发 Agent 发出的 `ask_owner` 提问，并唤醒任务继续执行（8.4 节）。
// 参数 id 为任务主键，questionID 为问题 ID，answer 为答复内容；返回更新后的任务与错误。
func (s *Service) AnswerQuestion(id uint64, questionID, answer string) (*model.WorkItem, error) {
	item, err := s.repo.GetByID(id)
	if err != nil || item == nil {
		return nil, errors.New("开发任务不存在")
	}
	item.PendingQuestionJSON = ""
	item.Status = "coding"
	if err := s.repo.Save(item); err != nil {
		return nil, err
	}
	_, _, _ = s.agents.DispatchToDevAgent("cmd.work.answer", map[string]any{
		"workItemId": item.ID,
		"questionId": questionID,
		"answer":     answer,
	}, 24*time.Hour)
	return item, nil
}

// Accept 验收通过开发任务（8.7 & 8.10 节）：检查破坏性迁移是否已单独审批，锁定候选提交 SHA，生成版本记录并触发发布流水线。
// 参数 id 为任务主键，customVersion 为可选覆盖版本号，approveDestructive 为破坏性表结构迁移单独放行确认，comment 为意见，actor 为管理员；返回任务、版本ID与错误。
func (s *Service) Accept(id uint64, customVersion string, approveDestructive bool, comment, actor string) (*model.WorkItem, uint64, error) {
	item, err := s.repo.GetByID(id)
	if err != nil || item == nil {
		return nil, 0, errors.New("开发任务不存在")
	}
	if item.MigrationRisk == "destructive" && !approveDestructive && !item.DestructiveMigrationApproved {
		return nil, 0, errors.New("本任务包含破坏性表结构变更，必须显式勾选单独审批后方可发布")
	}
	now := time.Now()
	item.Status = "accepted"
	item.AcceptedCommitSHA = item.CandidateCommitSHA
	item.OwnerReviewComment = comment
	item.AcceptedAt = &now
	if approveDestructive {
		item.DestructiveMigrationApproved = true
	}

	reqIDs, _ := s.repo.GetRequirementIDs(item.ID)
	var reqs []reqModel.Requirement
	if s.reqBridge != nil && len(reqIDs) > 0 {
		reqs, _ = s.reqBridge.GetByIDs(reqIDs)
	}

	var releaseID uint64
	if s.relCreator != nil {
		rid, _, relErr := s.relCreator.CreatePublishingRelease(item, reqs, customVersion, actor)
		if relErr != nil {
			return nil, 0, relErr
		}
		releaseID = rid
		item.ActiveReleaseID = releaseID
	}
	if err := s.repo.Save(item); err != nil {
		return nil, 0, err
	}
	return item, releaseID, nil
}

// Reject 验收打回：附带打回意见将开发任务退回 coding（开发中）继续修复（4.1 节）。
// 参数 id 为任务主键，comment 为打回修改意见；返回更新后的任务与错误。
func (s *Service) Reject(id uint64, comment string) (*model.WorkItem, error) {
	if strings.TrimSpace(comment) == "" {
		return nil, errors.New("打回任务时请填写具体的修改意见")
	}
	item, err := s.repo.GetByID(id)
	if err != nil || item == nil {
		return nil, errors.New("开发任务不存在")
	}
	item.Status = "coding"
	item.OwnerReviewComment = comment
	if err := s.repo.Save(item); err != nil {
		return nil, err
	}
	_, _, _ = s.agents.DispatchToDevAgent("cmd.work.review_decision", map[string]any{
		"workItemId": item.ID,
		"action":     "reject",
		"comment":    comment,
	}, 24*time.Hour)
	return item, nil
}

// Abandon 放弃当前开发任务，停止预览实例并将关联需求状态退回 scheduled（待排期）（4.1 节）。
// 参数 id 为任务主键，comment 为原因，actor 为管理员；返回更新后的任务与错误。
func (s *Service) Abandon(id uint64, comment, actor string) (*model.WorkItem, error) {
	item, err := s.repo.GetByID(id)
	if err != nil || item == nil {
		return nil, errors.New("开发任务不存在")
	}
	item.Status = "abandoned"
	item.OwnerReviewComment = comment
	if err := s.repo.Save(item); err != nil {
		return nil, err
	}
	reqIDs, _ := s.repo.GetRequirementIDs(item.ID)
	if s.reqBridge != nil && len(reqIDs) > 0 {
		s.reqBridge.OnWorkAbandoned(reqIDs, item.ID, actor)
	}
	_, _, _ = s.agents.DispatchToDevAgent("cmd.work.review_decision", map[string]any{
		"workItemId": item.ID,
		"action":     "abandon",
		"comment":    comment,
	}, 24*time.Hour)
	return item, nil
}

// ReturnToCodingOnPreTagConflict 当发布流水线在推 tag 前发生主分支 rebase 冲突时（8.10 第 1 步），将任务退回 coding 并清空已通过标记以便修复后重新验收。
// 参数 workItemID 为任务主键，reason 为冲突说明；无返回值。
func (s *Service) ReturnToCodingOnPreTagConflict(workItemID uint64, reason string) {
	item, err := s.repo.GetByID(workItemID)
	if err != nil || item == nil {
		return
	}
	item.Status = "coding"
	item.AcceptedCommitSHA = ""
	item.ActiveReleaseID = 0
	_ = s.repo.Save(item)
	_ = s.runs.RecordActivity(&runModel.RunActivity{
		ProductID:  item.ProductID,
		WorkItemID: item.ID,
		Stage:      "publish",
		Kind:       "warn",
		Summary:    "合并主分支发生冲突，按契约退回开发阶段修复并须重新验收：" + reason,
	})
}

// HandleDevEnvelope 实现 agentHandler.DevEventConsumer 接口：消费开发 Agent 经 WSS 或批量同步上报的所有研发进展事件。
// 参数 _ 为发送方 Agent ID，msg 为统一事件信封；返回处理错误。
func (s *Service) HandleDevEnvelope(_ uint64, msg envelope.Message) error {
	switch msg.Type {
	case "evt.work.plan_ready":
		var p struct {
			WorkItemID uint64          `json:"workItemId"`
			Plan       json.RawMessage `json:"plan"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return err
		}
		item, _ := s.repo.GetByID(p.WorkItemID)
		if item != nil {
			item.PlanDocumentJSON = string(p.Plan)
			item.Status = "awaiting_plan"
			_ = s.repo.Save(item)
			if s.notifier != nil {
				s.notifier.NotifyAsync(notifySvc.EventPlanConfirm,
					fmt.Sprintf("开发任务 #%d 方案待确认：%s", item.ID, item.Title),
					"已产出改动模块、公共区文件与表结构评估，请前往后台确认后进入编码。",
				)
			}
		}

	case "evt.work.ask_owner":
		var p struct {
			WorkItemID uint64          `json:"workItemId"`
			Question   json.RawMessage `json:"question"`
			Summary    string          `json:"summary"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return err
		}
		item, _ := s.repo.GetByID(p.WorkItemID)
		if item != nil {
			item.PendingQuestionJSON = string(p.Question)
			item.Status = "awaiting_answer"
			_ = s.repo.Save(item)
			if s.notifier != nil {
				s.notifier.NotifyAsync(notifySvc.EventAskOwner,
					fmt.Sprintf("开发任务 #%d 请求决策：%s", item.ID, item.Title),
					p.Summary,
				)
			}
		}

	case "evt.work.sandbox_blocked":
		var p struct {
			WorkItemID  uint64 `json:"workItemId"`
			ProductID   uint64 `json:"productId"`
			BlockedPath string `json:"blockedPath"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return err
		}
		_ = s.products.RecordBlockedPath(p.ProductID, p.BlockedPath)
		_ = s.runs.RecordActivity(&runModel.RunActivity{
			ProductID:  p.ProductID,
			WorkItemID: p.WorkItemID,
			Stage:      "coding",
			Kind:       "sandbox_block",
			Summary:    "被权限档案拦截：" + p.BlockedPath,
		})

	case "evt.work.progress":
		var p struct {
			WorkItemID       uint64            `json:"workItemId"`
			Status           string            `json:"status"`
			Stage            string            `json:"stage"`
			Kind             string            `json:"kind"`
			Summary          string            `json:"summary"`
			DetailJSON       string            `json:"detailJson"`
			TodoListJSON     string            `json:"todoListJson"`
			PromptTokens     int               `json:"promptTokens"`
			CompletionTokens int               `json:"completionTokens"`
			DurationSeconds  int64             `json:"durationSeconds"`
			Gates            []model.WorkGate  `json:"gates"`
			Transcript       *runModel.RunTranscript `json:"transcript"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return err
		}
		item, _ := s.repo.GetByID(p.WorkItemID)
		if item != nil {
			if p.Status != "" {
				item.Status = p.Status
			}
			if p.TodoListJSON != "" {
				item.TodoListJSON = p.TodoListJSON
			}
			if p.PromptTokens > 0 || p.CompletionTokens > 0 {
				item.PromptTokensUsed += p.PromptTokens
				item.CompletionTokensUsed += p.CompletionTokens
				if s.llm != nil {
					s.llm.RecordExternalUsage(&llmModel.LLMUsage{
						ProductID:        item.ProductID,
						WorkItemID:       item.ID,
						RoleName:         chooseRole(p.Stage),
						PromptTokens:     p.PromptTokens,
						CompletionTokens: p.CompletionTokens,
						TotalTokens:      p.PromptTokens + p.CompletionTokens,
						Succeeded:        true,
					})
				}
			}
			if p.DurationSeconds > item.DurationSeconds {
				item.DurationSeconds = p.DurationSeconds
			}
			_ = s.repo.Save(item)
			if p.Summary != "" {
				_ = s.runs.RecordActivity(&runModel.RunActivity{
					ProductID:  item.ProductID,
					WorkItemID: item.ID,
					Stage:      chooseNonEmptyStr(p.Stage, "coding"),
					Kind:       chooseNonEmptyStr(p.Kind, "info"),
					Summary:    p.Summary,
					DetailJSON: p.DetailJSON,
				})
			}
			for i := range p.Gates {
				p.Gates[i].ProductID = item.ProductID
				p.Gates[i].WorkItemID = item.ID
				_ = s.repo.UpsertGate(&p.Gates[i])
			}
			if p.Transcript != nil {
				p.Transcript.ProductID = item.ProductID
				p.Transcript.WorkItemID = item.ID
				_ = s.runs.SaveTranscript(p.Transcript)
			}
		}

	case "evt.work.ready_for_acceptance":
		var p struct {
			WorkItemID         uint64                  `json:"workItemId"`
			CandidateCommitSHA string                  `json:"candidateCommitSha"`
			MigrationRisk      string                  `json:"migrationRisk"`
			OtherModuleCount   int                     `json:"otherModuleCount"`
			PublicZoneCount    int                     `json:"publicZoneCount"`
			ScopeSummary       json.RawMessage         `json:"scopeSummary"`
			ImpactAudit        json.RawMessage         `json:"impactAudit"`
			AcceptancePackage  json.RawMessage         `json:"acceptancePackage"`
			ScopeChanges       []model.WorkScopeChange `json:"scopeChanges"`
			Gates              []model.WorkGate        `json:"gates"`
			Preview            *model.PreviewSession   `json:"preview"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return err
		}
		item, _ := s.repo.GetByID(p.WorkItemID)
		if item != nil {
			item.Status = "pending_acceptance"
			item.CandidateCommitSHA = p.CandidateCommitSHA
			if p.MigrationRisk != "" {
				item.MigrationRisk = p.MigrationRisk
			}
			item.ScopeSummaryJSON = string(p.ScopeSummary)
			item.ImpactAuditJSON = string(p.ImpactAudit)
			item.AcceptancePkgJSON = string(p.AcceptancePackage)
			_ = s.repo.Save(item)
			_ = s.repo.ReplaceScopeChanges(item.ID, item.ProductID, p.ScopeChanges)
			for i := range p.Gates {
				p.Gates[i].ProductID = item.ProductID
				p.Gates[i].WorkItemID = item.ID
				_ = s.repo.UpsertGate(&p.Gates[i])
			}
			previewAddr := ""
			if p.Preview != nil {
				p.Preview.ProductID = item.ProductID
				p.Preview.WorkItemID = item.ID
				if p.Preview.ExpiresAt.IsZero() {
					p.Preview.ExpiresAt = time.Now().Add(24 * time.Hour)
				}
				_ = s.repo.UpsertPreviewSession(p.Preview)
				previewAddr = p.Preview.PreviewURL
			}
			// 8.5 节：飞书通知摘要必须呈现其他模块与公共区改动统计
			if s.notifier != nil {
				s.notifier.NotifyAsync(notifySvc.EventPendingAcceptance,
					fmt.Sprintf("开发任务 #%d 待验收：%s", item.ID, item.Title),
					fmt.Sprintf("本次改动涉及 %d 个其他模块、%d 处公共区 ｜ 迁移风险：%s ｜ 预览地址：%s",
						p.OtherModuleCount, p.PublicZoneCount, item.MigrationRisk, previewAddr),
				)
			}
		}

	case "evt.work.failed":
		var p struct {
			WorkItemID uint64 `json:"workItemId"`
			Error      string `json:"error"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return err
		}
		item, _ := s.repo.GetByID(p.WorkItemID)
		if item != nil {
			qRaw, _ := json.Marshal(map[string]string{
				"id":       "err-1",
				"question": "开发 Agent 执行遇到异常，请检查环境或答复后重试：" + p.Error,
			})
			item.PendingQuestionJSON = string(qRaw)
			item.Status = "awaiting_answer"
			_ = s.repo.Save(item)
			_ = s.runs.RecordActivity(&runModel.RunActivity{
				ProductID:  item.ProductID,
				WorkItemID: item.ID,
				Stage:      "coding",
				Kind:       "error",
				Summary:    "开发 Agent 执行异常：" + p.Error,
			})
			if s.notifier != nil {
				s.notifier.NotifyAsync(notifySvc.EventAskOwner,
					fmt.Sprintf("开发任务 #%d 执行异常待处理：%s", item.ID, item.Title),
					p.Error,
				)
			}
		}
	}
	return nil
}

// chooseRole 将开发任务执行阶段映射为模型用量统计的角色名；参数 stage 为阶段名；返回 coder/reviewer/auditor。
func chooseRole(stage string) string {
	switch stage {
	case "review":
		return "reviewer"
	case "impact_audit":
		return "auditor"
	default:
		return "coder"
	}
}

// chooseNonEmptyStr 返回首个非空白字符串，为空白时返回默认值 b；参数 a/b 为候选字符串；返回非空文本。
func chooseNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
