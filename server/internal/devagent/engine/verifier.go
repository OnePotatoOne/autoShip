package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VerifyCriteriaEvidence 由确定性 Go 代码核验每条验收标准是否绑定了本次会话真实产生且未因后续改代码而失效的通过证据（8.4 节核心要求：绝不轻信模型自述）。
// 参数 criteria 为规格阶段确定的验收标准列表，bindings 为模型登记的绑定关系，cmdHistory 为运行时真实捕获的命令执行历史，lastFileEditNano 为最后一次写文件纳秒时间戳，worktreeDir 为工作树目录；返回逐条核验清单及未通过错误明细。
func VerifyCriteriaEvidence(
	criteria []AcceptanceCriterion,
	bindings map[string]EvidenceBinding,
	cmdHistory []CommandExecutionRecord,
	lastFileEditNano int64,
	worktreeDir string,
) ([]VerifiedEvidence, []string) {
	runMap := make(map[string]CommandExecutionRecord, len(cmdHistory))
	for _, rec := range cmdHistory {
		runMap[rec.RunID] = rec
	}

	var verified []VerifiedEvidence
	var problems []string

	for _, ac := range criteria {
		b, exists := bindings[ac.ID]
		if !exists {
			problems = append(problems, fmt.Sprintf("验收标准 %s (%s) 尚未调用 bind_evidence 绑定任何真实执行证据", ac.ID, ac.Description))
			verified = append(verified, VerifiedEvidence{
				CriterionID: ac.ID,
				Description: ac.Description,
				Passed:      false,
			})
			continue
		}

		item := VerifiedEvidence{
			CriterionID:    ac.ID,
			Description:    ac.Description,
			RunID:          b.RunID,
			TestName:       b.TestName,
			ScreenshotPath: b.ScreenshotPath,
		}

		hasValidCmd := false
		if strings.TrimSpace(b.RunID) != "" {
			rec, ok := runMap[strings.TrimSpace(b.RunID)]
			if !ok {
				problems = append(problems, fmt.Sprintf("验收标准 %s 绑定的命令运行记录 %q 在真实执行历史中不存在（禁止虚构证据）", ac.ID, b.RunID))
			} else if rec.ExitCode != 0 {
				problems = append(problems, fmt.Sprintf("验收标准 %s 绑定的命令 %q (runId=%s) 退出码为 %d，未通过", ac.ID, rec.Command, rec.RunID, rec.ExitCode))
			} else if lastFileEditNano > 0 && rec.ExecutedAt < lastFileEditNano {
				problems = append(problems, fmt.Sprintf("验收标准 %s 绑定的命令 %s 执行时间早于最后一次代码修改，请重新运行测试并更新证据", ac.ID, rec.RunID))
			} else if strings.TrimSpace(b.TestName) != "" &&
				!strings.Contains(rec.Command, b.TestName) &&
				!strings.Contains(rec.Output, b.TestName) {
				problems = append(problems, fmt.Sprintf("验收标准 %s 声明的测试名 %q 未出现在命令 %q 及其真实输出中", ac.ID, b.TestName, rec.Command))
			} else {
				hasValidCmd = true
				item.Command = rec.Command
				item.ExitCode = rec.ExitCode
				item.OutputExcerpt = truncateStr(rec.Output, 400)
			}
		}

		hasValidShot := false
		if strings.TrimSpace(b.ScreenshotPath) != "" {
			shotTarget := b.ScreenshotPath
			if !filepath.IsAbs(shotTarget) {
				shotTarget = filepath.Join(worktreeDir, filepath.FromSlash(shotTarget))
			}
			if st, err := os.Stat(shotTarget); err == nil && !st.IsDir() && st.Size() > 0 {
				hasValidShot = true
				item.ScreenshotPath = shotTarget
			} else {
				problems = append(problems, fmt.Sprintf("验收标准 %s 绑定的截图文件 %q 不存在或为空文件", ac.ID, b.ScreenshotPath))
			}
		}

		if !hasValidCmd && !hasValidShot {
			if strings.TrimSpace(b.RunID) == "" && strings.TrimSpace(b.ScreenshotPath) == "" {
				problems = append(problems, fmt.Sprintf("验收标准 %s 的证据绑定既无有效 runId 也无真实截图路径", ac.ID))
			}
			item.Passed = false
		} else {
			item.Passed = true
		}
		verified = append(verified, item)
	}

	return verified, problems
}

// StallDetector 监控编码会话中的连续相同错误或连续同一失败测试（连续重复 3 次提醒换思路，仍无进展则升级触发 ask_owner，8.4 节）。
type StallDetector struct {
	lastSignature string
	repeatCount   int
	warnedOnThis  bool
}

// StallDecision 表示停滞检测器对最新一次失败做出的处置动作。
type StallDecision string

const (
	// StallNone 表示进展正常或尚未达到停滞阈值。
	StallNone StallDecision = "none"
	// StallWarnPivot 表示同一错误连续出现满 3 次，向会话注入强制切换排查思路的提醒。
	StallWarnPivot StallDecision = "warn_pivot"
	// StallAskOwner 表示提醒换思路后依然重复同一错误无进展，升级暂停并通过飞书求助人工。
	StallAskOwner StallDecision = "ask_owner"
)

// ObserveSuccess 在工具或测试成功推进时重置连续错误计数。
// 无入参；无返回值。
func (s *StallDetector) ObserveSuccess() {
	s.lastSignature = ""
	s.repeatCount = 0
	s.warnedOnThis = false
}

// ObserveFailure 记录一次工具错误或测试失败特征签名，判断是否满 3 次需警告换思路或再次卡死需 ask_owner。
// 参数 rawErrSig 为错误摘要或失败测试名；返回 StallDecision。
func (s *StallDetector) ObserveFailure(rawErrSig string) StallDecision {
	norm := normalizeErrorSignature(rawErrSig)
	if norm == "" {
		return StallNone
	}
	if norm != s.lastSignature {
		s.lastSignature = norm
		s.repeatCount = 1
		s.warnedOnThis = false
		return StallNone
	}
	s.repeatCount++
	if s.repeatCount == 3 && !s.warnedOnThis {
		s.warnedOnThis = true
		return StallWarnPivot
	}
	if s.repeatCount > 3 && s.warnedOnThis {
		return StallAskOwner
	}
	return StallNone
}

// normalizeErrorSignature 归一化错误输出的首行关键信息，过滤时间戳干扰。
func normalizeErrorSignature(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	firstLine := strings.SplitN(trimmed, "\n", 2)[0]
	if len(firstLine) > 180 {
		firstLine = firstLine[:180]
	}
	return strings.ToLower(strings.TrimSpace(firstLine))
}
