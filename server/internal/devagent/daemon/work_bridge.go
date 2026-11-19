package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/engine"
)

// serverStartWorkPayload 映射 Server 端 work 模块通过 WSS 下发的 `cmd.work.start` JSON 载荷。
type serverStartWorkPayload struct {
	WorkItemID          uint64 `json:"workItemId"`
	BranchName          string `json:"branchName"`
	PrimaryModuleCode   string `json:"primaryModuleCode"`
	ParentReleaseID     uint64 `json:"parentReleaseId"`
	PlanConfirmRequired *bool  `json:"planConfirmRequired"`
	Budget              struct {
		TokenBudget        int `json:"tokenBudget"`
		MaxTurns           int `json:"maxTurns"`
		MaxDurationMinutes int `json:"maxDurationMinutes"`
	} `json:"budget"`
	ProductBundle *struct {
		Product struct {
			ID                uint64 `json:"id"`
			Code              string `json:"code"`
			Name              string `json:"name"`
			RepoURL           string `json:"repoUrl"`
			DefaultBranch     string `json:"defaultBranch"`
			InstallCmd        string `json:"installCmd"`
			BuildCmd          string `json:"buildCmd"`
			TestCmd           string `json:"testCmd"`
			LintCmd           string `json:"lintCmd"`
			CommandTimeoutSec int    `json:"commandTimeoutSec"`
			PreviewStartCmd   string `json:"previewStartCmd"`
			PreviewPortCount  int    `json:"previewPortCount"`
			PreviewDBPrefix   string `json:"previewDbPrefix"`
			PreviewSeedCmd    string `json:"previewSeedCmd"`
			PreviewSupportsTLS bool  `json:"previewSupportsTls"`
			HealthCheckPath   string `json:"healthCheckPath"`
			ChangelogFile     string `json:"changelogFile"`
			DeployHookPath    string `json:"deployHookPath"`
			DevRulesPrompt    string `json:"devRulesPrompt"`
		} `json:"product"`
		Modules []struct {
			Code         string `json:"code"`
			PathsJSON    string `json:"pathsJson"`
			PageURLsJSON string `json:"pageUrlsJson"`
		} `json:"modules"`
		ScopeRules []struct {
			ZoneType    string `json:"zoneType"`
			PathPattern string `json:"pathPattern"`
		} `json:"scopeRules"`
		PermissionProfile *struct {
			Mode                  string `json:"mode"`
			AllowedWritePathsJSON string `json:"allowedWritePathsJson"`
			ExtraDeniedReadJSON   string `json:"extraDeniedReadJson"`
		} `json:"permissionProfile"`
	} `json:"productBundle"`
	Requirements []struct {
		ID                 uint64 `json:"id"`
		IssueType          string `json:"issueType"`
		ModuleCode         string `json:"moduleCode"`
		Title              string `json:"title"`
		Description        string `json:"description"`
		ExpectedBehavior   string `json:"expectedBehavior"`
		ActualBehavior     string `json:"actualBehavior"`
		ReproSteps         string `json:"reproSteps"`
		OutOfScope         string `json:"outOfScope"`
		AcceptanceCriteria string `json:"acceptanceCriteria"`
		EnvInfoJSON        string `json:"envInfoJson"`
	} `json:"requirements"`
}

// persistedTaskSnapshot 保存可跨重启恢复的单个开发任务请求与最新技术方案。
type persistedTaskSnapshot struct {
	WorkItemID uint64             `json:"workItemId"`
	ProductID  uint64             `json:"productId"`
	TaskReq    engine.TaskRequest `json:"taskReq"`
	LastPlan   *engine.PlanSpec   `json:"lastPlan,omitempty"`
}

// decodeInboundWorkRequest 兼容解析直接传入的 engine.TaskRequest 与 Server 下发的 cmd.work.start 复合载荷。
// 参数 rawPayload 为指令 JSON 原文；返回解析后的 TaskRequest、关联的数值 workItemId/productId 及错误。
func (d *Daemon) decodeInboundWorkRequest(rawPayload json.RawMessage) (engine.TaskRequest, uint64, uint64, error) {
	var direct engine.TaskRequest
	if err := json.Unmarshal(rawPayload, &direct); err == nil && strings.TrimSpace(direct.WorkID) != "" {
		numID, _ := strconv.ParseUint(strings.TrimPrefix(direct.WorkID, "work-"), 10, 64)
		return direct, numID, 0, nil
	}

	var srv serverStartWorkPayload
	if err := json.Unmarshal(rawPayload, &srv); err != nil {
		return engine.TaskRequest{}, 0, 0, err
	}
	if srv.WorkItemID == 0 {
		return engine.TaskRequest{}, 0, 0, fmt.Errorf("缺少有效的 workId 或 workItemId")
	}

	workID := fmt.Sprintf("work-%d", srv.WorkItemID)
	var prod config.ProductProfile
	var productID uint64
	if srv.ProductBundle != nil {
		p := srv.ProductBundle.Product
		productID = p.ID
		// 若本地 dev-agent.yaml 已配置该产品的私钥/PAT 等本机字段，先以本地档案为基底合并 Server 最新配置
		d.mu.Lock()
		if localProd, ok := d.productsByCode[p.Code]; ok {
			prod = localProd
		}
		d.mu.Unlock()

		prod.ProductCode = p.Code
		prod.Name = p.Name
		if strings.TrimSpace(p.RepoURL) != "" {
			prod.RepoURL = p.RepoURL
		}
		if strings.TrimSpace(p.DefaultBranch) != "" {
			prod.DefaultBranch = p.DefaultBranch
		}
		timeoutSec := p.CommandTimeoutSec
		if timeoutSec <= 0 {
			timeoutSec = 600
		}
		if strings.TrimSpace(p.InstallCmd) != "" {
			prod.InstallCmd = config.CommandSpec{Cmd: p.InstallCmd, TimeoutSeconds: timeoutSec}
		}
		if strings.TrimSpace(p.BuildCmd) != "" {
			prod.BuildCmd = config.CommandSpec{Cmd: p.BuildCmd, TimeoutSeconds: timeoutSec}
		}
		if strings.TrimSpace(p.TestCmd) != "" {
			prod.TestCmd = config.CommandSpec{Cmd: p.TestCmd, TimeoutSeconds: timeoutSec}
		}
		if strings.TrimSpace(p.LintCmd) != "" {
			prod.LintCmd = config.CommandSpec{Cmd: p.LintCmd, TimeoutSeconds: timeoutSec}
		}
		if strings.TrimSpace(p.PreviewStartCmd) != "" {
			prod.Preview.StartCmd = p.PreviewStartCmd
			prod.Preview.PortCount = p.PreviewPortCount
			prod.Preview.DBPrefix = p.PreviewDBPrefix
			prod.Preview.SeedCmd = p.PreviewSeedCmd
			prod.Preview.SupportsExternalTLS = p.PreviewSupportsTLS
			prod.Preview.HealthPath = p.HealthCheckPath
		}
		if strings.TrimSpace(p.ChangelogFile) != "" {
			prod.Release.ChangelogFile = p.ChangelogFile
		}
		if strings.TrimSpace(p.DeployHookPath) != "" {
			prod.Release.HookRepoPath = p.DeployHookPath
		}
		if strings.TrimSpace(p.DevRulesPrompt) != "" {
			prod.CodingRules = append(prod.CodingRules, p.DevRulesPrompt)
		}
		if len(srv.ProductBundle.Modules) > 0 {
			if prod.Modules == nil {
				prod.Modules = make(map[string][]string)
			}
			for _, m := range srv.ProductBundle.Modules {
				var paths []string
				_ = json.Unmarshal([]byte(m.PathsJSON), &paths)
				if m.Code != "" && len(paths) > 0 {
					prod.Modules[m.Code] = paths
				}
			}
		}
		for _, sr := range srv.ProductBundle.ScopeRules {
			if sr.ZoneType == "public_zone" && sr.PathPattern != "" {
				prod.PublicZoneDirs = append(prod.PublicZoneDirs, sr.PathPattern)
			} else if sr.ZoneType == "protected" && sr.PathPattern != "" {
				prod.ProtectedPaths = append(prod.ProtectedPaths, sr.PathPattern)
			}
		}
		if pp := srv.ProductBundle.PermissionProfile; pp != nil && pp.Mode != "" {
			prod.Sandbox.Mode = pp.Mode
			_ = json.Unmarshal([]byte(pp.AllowedWritePathsJSON), &prod.Sandbox.CustomAllowWrite)
			_ = json.Unmarshal([]byte(pp.ExtraDeniedReadJSON), &prod.Sandbox.CustomDenyRead)
		}
	}
	prod.ApplyDefaults()

	workType := engine.WorkTypeFeature
	var reqBlocks []string
	var envJSON string
	var criteria []engine.AcceptanceCriterion
	for idx, r := range srv.Requirements {
		if idx == 0 {
			switch strings.ToLower(r.IssueType) {
			case "bug":
				workType = engine.WorkTypeBug
			case "improvement":
				workType = engine.WorkTypeImprovement
			default:
				workType = engine.WorkTypeFeature
			}
			envJSON = r.EnvInfoJSON
		}
		block := fmt.Sprintf("需求 #%d：%s\n背景描述：%s\n期望结果：%s\n复现或实际现象：%s %s\n不包含范围：%s\n验收标准：\n%s",
			r.ID, r.Title, r.Description, r.ExpectedBehavior, r.ReproSteps, r.ActualBehavior, r.OutOfScope, r.AcceptanceCriteria)
		reqBlocks = append(reqBlocks, block)
		if strings.TrimSpace(r.AcceptanceCriteria) != "" {
			criteria = append(criteria, engine.AcceptanceCriterion{
				ID:          fmt.Sprintf("AC-%d", idx+1),
				Description: strings.TrimSpace(r.AcceptanceCriteria),
				VerifyHint:  prod.TestCmd.Cmd,
			})
		}
	}

	planConfirm := true
	if srv.PlanConfirmRequired != nil {
		planConfirm = *srv.PlanConfirmRequired
	}

	taskReq := engine.TaskRequest{
		WorkID:                  workID,
		Product:                 prod,
		WorkType:                workType,
		TargetModule:            srv.PrimaryModuleCode,
		RequirementRaw:          strings.Join(reqBlocks, "\n\n---\n\n"),
		EnvironmentJSON:         envJSON,
		InitialCriteria:         criteria,
		RequirePlanConfirmation: planConfirm,
		BudgetOverride: &config.TaskBudget{
			MaxTokens:          srv.Budget.TokenBudget,
			MaxTurns:           srv.Budget.MaxTurns,
			MaxDurationMinutes: srv.Budget.MaxDurationMinutes,
		},
	}
	return taskReq, srv.WorkItemID, productID, nil
}

// saveTaskSnapshot 将任务的请求参数与最新方案持久化到本地状态目录，以便人工审批方案或回复提问后无缝恢复执行。
func (d *Daemon) saveTaskSnapshot(snap persistedTaskSnapshot) {
	if snap.WorkItemID == 0 {
		return
	}
	dir := filepath.Join(d.cfg.StateDir(), "tasks")
	_ = os.MkdirAll(dir, 0700)
	// 清空不可序列化的函数回调副本后写入磁盘
	cp := snap
	cp.TaskReq.OnActivity = nil
	cp.TaskReq.OnSandboxIntercept = nil
	if raw, err := json.Marshal(cp); err == nil {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", snap.WorkItemID)), raw, 0600)
	}
}

// loadTaskSnapshot 按 workItemId 读取已保存的任务上下文快照。
func (d *Daemon) loadTaskSnapshot(workItemID uint64) (*persistedTaskSnapshot, bool) {
	if workItemID == 0 {
		return nil, false
	}
	p := filepath.Join(d.cfg.StateDir(), "tasks", fmt.Sprintf("%d.json", workItemID))
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var snap persistedTaskSnapshot
	if json.Unmarshal(raw, &snap) != nil {
		return nil, false
	}
	return &snap, true
}

// emitServerCompatibleOutcome 将开发引擎返回的阶段性或最终结果同步转换为 Server 侧 work 模块消费的标准事件。
func (d *Daemon) emitServerCompatibleOutcome(workItemID uint64, outcome *engine.TaskOutcome, pvURL, pvUser, pvPass string) {
	if workItemID == 0 || outcome == nil {
		return
	}
	switch outcome.Status {
	case engine.OutcomeWaitingPlanConfirm:
		planRaw, _ := json.Marshal(outcome.Plan)
		_, _ = d.EmitEvent("evt.work.plan_ready", map[string]any{
			"workItemId": workItemID,
			"plan":       json.RawMessage(planRaw),
		})

	case engine.OutcomeAskOwner:
		qRaw, _ := json.Marshal(map[string]string{
			"id":       "q-1",
			"question": outcome.AskOwnerQuestion,
		})
		_, _ = d.EmitEvent("evt.work.ask_owner", map[string]any{
			"workItemId": workItemID,
			"question":   json.RawMessage(qRaw),
			"summary":    outcome.AskOwnerQuestion,
		})

	case engine.OutcomePausedBudget:
		_, _ = d.EmitEvent("evt.work.progress", map[string]any{
			"workItemId":      workItemID,
			"status":          "paused",
			"stage":           "coding",
			"kind":            "warn",
			"summary":         "任务达到时长或 Token 预算上限，已自动保存 WIP 提交并暂停",
			"durationSeconds": int64(outcome.Duration.Seconds()),
		})

	case engine.OutcomeReadyForAcceptance:
		pkg := outcome.AcceptancePackage
		if pkg == nil {
			return
		}
		pkgRaw, _ := json.Marshal(pkg)
		scopeRaw, _ := json.Marshal(pkg.FirstScreenScope)
		auditRaw, _ := json.Marshal(pkg.PublicImpactAudit)

		otherModCnt := 0
		pubCnt := 0
		var scopeItems []map[string]any
		if pkg.FirstScreenScope != nil {
			pubCnt = len(pkg.FirstScreenScope.PublicZoneChanges)
			for _, m := range pkg.FirstScreenScope.ByModule {
				if m.Zone != "primary_module" && m.Zone != "public_zone" {
					otherModCnt++
				}
				for _, f := range m.Files {
					scopeItems = append(scopeItems, map[string]any{
						"zoneType":     string(m.Zone),
						"moduleCode":   m.ModuleName,
						"filePath":     f.FilePath,
						"addedLines":   f.AddedLines,
						"deletedLines": f.DeletedLines,
					})
				}
			}
		}

		var gateItems []map[string]any
		if pkg.GateSuiteReport != nil {
			for _, g := range pkg.GateSuiteReport.Results {
				gateItems = append(gateItems, map[string]any{
					"gateCode": g.GateName,
					"gateName": g.GateName,
					"status":   string(g.Status),
					"summary":  g.Summary,
				})
			}
		}

		var pvObj map[string]any
		if pvURL != "" {
			pvObj = map[string]any{
				"previewUrl":   pvURL,
				"status":       "running",
				"testUsername": pvUser,
				"testPassword": pvPass,
			}
		}

		_, _ = d.EmitEvent("evt.work.ready_for_acceptance", map[string]any{
			"workItemId":         workItemID,
			"candidateCommitSha": pkg.CommitSHA,
			"migrationRisk":      string(pkg.MigrationRisk),
			"otherModuleCount":   otherModCnt,
			"publicZoneCount":    pubCnt,
			"scopeSummary":       json.RawMessage(scopeRaw),
			"impactAudit":        json.RawMessage(auditRaw),
			"acceptancePackage":  json.RawMessage(pkgRaw),
			"scopeChanges":       scopeItems,
			"gates":              gateItems,
			"preview":            pvObj,
		})
	}
}
