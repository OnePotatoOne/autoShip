package guard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"autoship/pkg/desensitize"
)

// DecisionAction 表示工具守卫对单次工具调用做出的放行、标记、需审批或阻断结论。
type DecisionAction string

const (
	// ActionAllow 表示本模块内安全操作，直接放行。
	ActionAllow DecisionAction = "allow"
	// ActionAllowMarked 表示跨模块或公共区修改，记录理由并打标记后放行（8.5 & 8.8 节）。
	ActionAllowMarked DecisionAction = "allow_marked"
	// ActionRequireApproval 表示命中受保护路径、依赖变更或破坏性迁移，需上报审批（8.8 节）。
	ActionRequireApproval DecisionAction = "require_approval"
	// ActionDeny 表示命中硬禁止规则（越界路径、git push、sudo、curl|sh、全局 git 配置等），直接拒绝执行。
	ActionDeny DecisionAction = "deny"
)

// AuditEntry 记录每次工具调用经守卫检查后的完整审计条目（同步上报 Server 活动流）。
type AuditEntry struct {
	Timestamp int64          `json:"timestamp"` // 检查发生时刻秒级 Unix 时间戳
	ToolName  string         `json:"toolName"`  // 工具名（read_file / write_file / run_command 等）
	Target    string         `json:"target"`    // 相对路径或脱敏后的命令文本
	Zone      Zone           `json:"zone"`      // 涉及的改动分区（命令检查时可为空）
	Action    DecisionAction `json:"action"`    // 守卫裁决动作
	Reason    string         `json:"reason"`    // 模型提交的改动理由或守卫拦截原因说明
}

// WriteCheckResult 返回守卫对单次写文件操作的三区判定、提醒消息及审计结论。
type WriteCheckResult struct {
	AbsPath  string         `json:"absPath"`  // 校验通过的 worktree 内绝对路径
	RelPath  string         `json:"relPath"`  // worktree 相对正斜杠路径
	Zone     Zone           `json:"zone"`     // 所属三区枚举
	Module   string         `json:"module"`   // 归属模块名称
	Action   DecisionAction `json:"action"`   // allow / allow_marked / require_approval / deny
	Reminder string         `json:"reminder"` // 注入回模型工具结果中的跨模块/公共区提醒文案
}

// CommandCheckResult 返回守卫对单次 Shell 命令执行的静态安全检查结论。
type CommandCheckResult struct {
	Action  DecisionAction `json:"action"`  // allow / require_approval / deny
	Message string         `json:"message"` // 拒绝原因或审批提示
}

var (
	// gitPushRe 检测任何形式的 git push 命令（含 git -C xxx push、git --no-pager push 等变体）
	gitPushRe = regexp.MustCompile(`(?im)(?:^|[;&|()\s` + "`" + `])git(?:\s+-[^\s]+|\s+--[^\s]+|\s+-C\s+\S+)*\s+push(?:\s|$|[;&|)])`)
	// sudoRe 检测作为独立命令词出现的 sudo 提权调用
	sudoRe = regexp.MustCompile(`(?im)(?:^|[;&|()\s` + "`" + `])sudo(?:\s|$)`)
	// pipeShellRe 检测 curl/wget 下载内容直接通过管道交由 sh/bash/zsh 执行的远程脚本注入模式
	pipeShellRe = regexp.MustCompile(`(?im)\b(?:curl|wget)\b[^;&|\r\n]*\|\s*(?:env\s+)?(?:/bin/|/usr/bin/)?(?:ba|z)?sh\b`)
	// gitGlobalCfgRe 检测修改宿主用户全局或系统级 git 配置的命令
	gitGlobalCfgRe = regexp.MustCompile(`(?im)\bgit(?:\s+\S+)*\s+config\b[^;&|\r\n]*(?:--global|--system|\.gitconfig)`)
	// gitResetOutsideRe 检测显式通过 -C 指向其他目录执行 git reset --hard 的越界重置操作
	gitResetOutsideRe = regexp.MustCompile(`(?im)\bgit\s+-C\s+(\S+)\s+reset\s+--hard\b`)
	// destructiveSQLRe 检测命令行直接执行的破坏性 DDL 语句（需审批）
	destructiveSQLRe = regexp.MustCompile(`(?im)\b(?:DROP\s+TABLE|DROP\s+COLUMN|TRUNCATE\s+TABLE)\b`)
	// depMutateCmdRe 检测命令行直接拉取或升级第三方依赖包的操作（需走依赖门禁审查）
	depMutateCmdRe = regexp.MustCompile(`(?im)\b(?:go\s+get\s+\S+|npm\s+(?:install|i|add|update)\s+[^-\s]\S*|pnpm\s+(?:add|update)\s+\S+|yarn\s+(?:add|upgrade)\s+\S+)`)
)

// Guard 封装单个任务 worktree 边界内的文件读写守卫、危险命令拦截器及审计日志收集器。
type Guard struct {
	worktreeDir    string
	realWorktree   string
	scope          *ScopeAnalyzer
	protectedPaths []string
	requireReason  bool
	mu             sync.Mutex
	auditLogs      []AuditEntry
	onAudit        func(AuditEntry)
}

// NewGuard 创建锁定在指定 worktreeDir 的工具守卫实例。
// 参数 worktreeDir 为本任务独立工作树根目录，scope 为三区范围分析器，protectedPaths 为产品声明的可选受保护相对路径列表，onAudit 为可选实时审计上报回调；返回 *Guard 及路径解析错误。
func NewGuard(worktreeDir string, scope *ScopeAnalyzer, protectedPaths []string, onAudit func(AuditEntry)) (*Guard, error) {
	cleaned := filepath.Clean(strings.TrimSpace(worktreeDir))
	if cleaned == "" {
		return nil, errors.New("worktreeDir 不能为空")
	}
	absWT, err := filepath.Abs(cleaned)
	if err != nil {
		return nil, fmt.Errorf("解析 worktree 绝对路径失败: %w", err)
	}
	realWT := absWT
	if resolved, evalErr := filepath.EvalSymlinks(absWT); evalErr == nil {
		realWT = resolved
	}
	var normProtected []string
	for _, p := range protectedPaths {
		if np := normalizeRelSlash(p); np != "" {
			normProtected = append(normProtected, np)
		}
	}
	return &Guard{
		worktreeDir:    absWT,
		realWorktree:   realWT,
		scope:          scope,
		protectedPaths: normProtected,
		requireReason:  true,
		onAudit:        onAudit,
	}, nil
}

// WorktreeDir 返回当前守卫锁定的工作树绝对路径。
// 无入参；返回绝对路径字符串。
func (g *Guard) WorktreeDir() string {
	return g.worktreeDir
}

// Scope 返回绑定的三区范围分析器指针。
// 无入参；返回 *ScopeAnalyzer。
func (g *Guard) Scope() *ScopeAnalyzer {
	return g.scope
}

// CheckRead 校验文件读取或目录浏览路径是否严格位于当前任务 worktree 内部（防止通过 `../` 或符号链接逃逸读取外部机密）。
// 参数 rawPath 为相对或绝对目标路径；越界返回错误，合规返回规范化后的绝对路径与相对路径。
func (g *Guard) CheckRead(rawPath string) (absPath string, relPath string, err error) {
	absPath, relPath, err = g.resolveInsideWorktree(rawPath)
	if err != nil {
		g.recordAudit("read_file", rawPath, "", ActionDeny, err.Error())
		return "", "", err
	}
	zone := ZoneCurrentModule
	if g.scope != nil {
		zone, _ = g.scope.ClassifyPath(relPath)
	}
	g.recordAudit("read_file", relPath, zone, ActionAllow, "")
	return absPath, relPath, nil
}

// CheckWrite 校验写文件/改文件请求：强制锁定在当前 worktree，核查受保护路径，对其他模块与公共区打标记并要求模型给出改动理由。
// 参数 rawPath 为目标文件路径，reason 为模型声明的改动理由；返回 *WriteCheckResult 及拦截/缺失理由错误。
func (g *Guard) CheckWrite(rawPath, reason string) (*WriteCheckResult, error) {
	absPath, relPath, err := g.resolveInsideWorktree(rawPath)
	if err != nil {
		g.recordAudit("write_file", rawPath, "", ActionDeny, err.Error())
		return nil, err
	}
	// 禁止模型直接覆写 .git 钩子或底层配置以防逃逸或注入推送钩子
	if relPath == ".git" || strings.HasPrefix(relPath, ".git/") {
		msg := "严禁直接写入 .git 元数据或 Git Hooks 目录"
		g.recordAudit("write_file", relPath, "", ActionDeny, msg)
		return nil, errors.New(msg)
	}

	zone := ZoneCurrentModule
	modName := "default"
	if g.scope != nil {
		zone, modName = g.scope.ClassifyPath(relPath)
	}

	// 检查是否命中产品档案声明的受保护路径（需人工审批）
	for _, prot := range g.protectedPaths {
		if relPath == prot || strings.HasPrefix(relPath, prot+"/") {
			msg := fmt.Sprintf("路径 %s 属于产品受保护路径 (%s)，修改前必须通过 ask_owner 请求人工审批", relPath, prot)
			g.recordAudit("write_file", relPath, zone, ActionRequireApproval, msg)
			return &WriteCheckResult{
				AbsPath:  absPath,
				RelPath:  relPath,
				Zone:     zone,
				Module:   modName,
				Action:   ActionRequireApproval,
				Reminder: msg,
			}, errors.New(msg)
		}
	}

	trimmedReason := strings.TrimSpace(reason)
	if zone == ZoneOtherModule || zone == ZonePublic {
		if g.requireReason && trimmedReason == "" {
			msg := fmt.Sprintf("你正在修改本模块之外的文件 %s（分区=%s，归属=%s），请在调用写文件工具时提供非空 reason 说明修改理由", relPath, zone, modName)
			g.recordAudit("write_file", relPath, zone, ActionDeny, msg)
			return nil, errors.New(msg)
		}
		if g.scope != nil {
			g.scope.RecordWriteReason(relPath, trimmedReason)
		}
		reminder := fmt.Sprintf("[改动范围标记] 已记录其他模块 (%s) 文件 %s 的改动与理由: %s", modName, relPath, trimmedReason)
		if zone == ZonePublic {
			reminder = fmt.Sprintf("[%s] 你修改了公共区文件 %s（理由: %s），校验阶段将强制追加一轮系统性影响审计", PublicBadge, relPath, trimmedReason)
		}
		g.recordAudit("write_file", relPath, zone, ActionAllowMarked, trimmedReason)
		return &WriteCheckResult{
			AbsPath:  absPath,
			RelPath:  relPath,
			Zone:     zone,
			Module:   modName,
			Action:   ActionAllowMarked,
			Reminder: reminder,
		}, nil
	}

	g.recordAudit("write_file", relPath, zone, ActionAllow, trimmedReason)
	return &WriteCheckResult{
		AbsPath: absPath,
		RelPath: relPath,
		Zone:    zone,
		Module:  modName,
		Action:  ActionAllow,
	}, nil
}

// CheckCommand 静态审计模型请求执行的 Shell 命令，阻断 git push、sudo、curl|sh、git 全局配置修改及越界删除/重置（8.8 节）。
// 参数 rawCmd 为原始命令字符串；合规返回 ActionAllow（或需审批提示），违规返回 ActionDeny 及明确拦截错误。
func (g *Guard) CheckCommand(rawCmd string) (*CommandCheckResult, error) {
	trimmed := strings.TrimSpace(rawCmd)
	if trimmed == "" {
		return nil, errors.New("命令内容不能为空")
	}

	if gitPushRe.MatchString(trimmed) {
		msg := "工具守卫拦截：严禁模型直接执行 git push（代码推送仅由验收通过后的发布流水线执行）"
		g.recordAudit("run_command", trimmed, "", ActionDeny, msg)
		return &CommandCheckResult{Action: ActionDeny, Message: msg}, errors.New(msg)
	}
	if sudoRe.MatchString(trimmed) {
		msg := "工具守卫拦截：严禁执行 sudo 提权命令"
		g.recordAudit("run_command", trimmed, "", ActionDeny, msg)
		return &CommandCheckResult{Action: ActionDeny, Message: msg}, errors.New(msg)
	}
	if pipeShellRe.MatchString(trimmed) {
		msg := "工具守卫拦截：严禁执行 curl/wget 管道直接交由 shell 运行（curl | sh）"
		g.recordAudit("run_command", trimmed, "", ActionDeny, msg)
		return &CommandCheckResult{Action: ActionDeny, Message: msg}, errors.New(msg)
	}
	if gitGlobalCfgRe.MatchString(trimmed) {
		msg := "工具守卫拦截：严禁修改宿主 git 全局或系统级配置（git config --global/--system）"
		g.recordAudit("run_command", trimmed, "", ActionDeny, msg)
		return &CommandCheckResult{Action: ActionDeny, Message: msg}, errors.New(msg)
	}

	// 检查 git -C <dir> reset --hard 是否指向了当前 worktree 之外
	for _, m := range gitResetOutsideRe.FindAllStringSubmatch(trimmed, -1) {
		if len(m) >= 2 {
			targetDir := strings.Trim(m[1], `"'`)
			if _, _, err := g.resolveInsideWorktree(targetDir); err != nil {
				msg := fmt.Sprintf("工具守卫拦截：禁止在当前 worktree 之外执行 git reset --hard (%s)", targetDir)
				g.recordAudit("run_command", trimmed, "", ActionDeny, msg)
				return &CommandCheckResult{Action: ActionDeny, Message: msg}, errors.New(msg)
			}
		}
	}

	// 检查 rm 删除命令是否包含越出当前 worktree 的路径（如 rm -rf ../、rm -rf /etc、rm ~/xxx）
	if viol := g.checkOutsideDeleteTokens(trimmed); viol != "" {
		g.recordAudit("run_command", trimmed, "", ActionDeny, viol)
		return &CommandCheckResult{Action: ActionDeny, Message: viol}, errors.New(viol)
	}

	// 破坏性 SQL 与直接新增/升级第三方依赖：标记需走门禁与人工审批
	if destructiveSQLRe.MatchString(trimmed) {
		msg := "检测到疑似破坏性数据库变更命令（DROP/TRUNCATE），将触发迁移风险门禁并需人工审批"
		g.recordAudit("run_command", trimmed, ZonePublic, ActionRequireApproval, msg)
		return &CommandCheckResult{Action: ActionRequireApproval, Message: msg}, nil
	}
	if depMutateCmdRe.MatchString(trimmed) {
		msg := "检测到第三方依赖变更命令，后续将由依赖策略检查器核验发布时间（满 30 天）与体积上限（≤10MB）"
		g.recordAudit("run_command", trimmed, ZonePublic, ActionRequireApproval, msg)
		return &CommandCheckResult{Action: ActionRequireApproval, Message: msg}, nil
	}

	g.recordAudit("run_command", trimmed, "", ActionAllow, "")
	return &CommandCheckResult{Action: ActionAllow}, nil
}

// AuditLogs 返回当前守卫自创建以来积累的全部工具调用审计记录副本。
// 无入参；返回 []AuditEntry 切片。
func (g *Guard) AuditLogs() []AuditEntry {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]AuditEntry, len(g.auditLogs))
	copy(out, g.auditLogs)
	return out
}

// resolveInsideWorktree 将输入路径解析为绝对路径并验证其本身与符号链接真实目标均严格落在 worktree 之内。
func (g *Guard) resolveInsideWorktree(rawPath string) (absTarget string, relSlash string, err error) {
	trimmed := strings.TrimSpace(rawPath)
	if trimmed == "" || trimmed == "." {
		return g.worktreeDir, "", nil
	}
	var candidate string
	if filepath.IsAbs(trimmed) {
		candidate = filepath.Clean(trimmed)
	} else {
		candidate = filepath.Join(g.worktreeDir, trimmed)
	}
	absTarget, err = filepath.Abs(candidate)
	if err != nil {
		return "", "", fmt.Errorf("解析路径失败: %w", err)
	}

	if !isSubpathOrEqual(g.worktreeDir, absTarget) && !isSubpathOrEqual(g.realWorktree, absTarget) {
		return "", "", fmt.Errorf("工具守卫拦截：禁止访问当前 worktree 之外的路径 %q", rawPath)
	}

	// 回溯最近存在的祖先目录解析符号链接，阻止通过 worktree 内软链接逃逸到宿主敏感目录
	realTarget := resolveExistingAncestorSymlink(absTarget)
	if realTarget != "" && !isSubpathOrEqual(g.realWorktree, realTarget) {
		return "", "", fmt.Errorf("工具守卫拦截：检测到符号链接越出当前 worktree 边界 (%q -> %q)", rawPath, realTarget)
	}

	rel, relErr := filepath.Rel(g.worktreeDir, absTarget)
	if relErr != nil || strings.HasPrefix(rel, "..") {
		rel2, relErr2 := filepath.Rel(g.realWorktree, realTarget)
		if relErr2 != nil || strings.HasPrefix(rel2, "..") {
			return "", "", fmt.Errorf("路径相对化越界: %q", rawPath)
		}
		rel = rel2
	}
	return absTarget, normalizeRelSlash(rel), nil
}

// checkOutsideDeleteTokens 解析命令段中的 rm / unlink / git clean 参数，拦截指向 worktree 外部或上级目录的删除动作。
func (g *Guard) checkOutsideDeleteTokens(cmd string) string {
	segments := strings.FieldsFunc(cmd, func(r rune) bool {
		return r == ';' || r == '|' || r == '&' || r == '\n'
	})
	for _, seg := range segments {
		tokens := strings.Fields(strings.TrimSpace(seg))
		if len(tokens) == 0 {
			continue
		}
		head := filepath.Base(tokens[0])
		if head != "rm" && head != "unlink" && head != "rmdir" {
			continue
		}
		for _, tok := range tokens[1:] {
			cleaned := strings.Trim(tok, `"'`)
			if cleaned == "" || strings.HasPrefix(cleaned, "-") {
				continue
			}
			if strings.HasPrefix(cleaned, "~/") || cleaned == "~" || cleaned == "/" {
				return fmt.Sprintf("工具守卫拦截：严禁删除宿主主目录或根目录路径 (%s)", cleaned)
			}
			if _, _, err := g.resolveInsideWorktree(cleaned); err != nil {
				return fmt.Sprintf("工具守卫拦截：严禁删除当前 worktree 之外的路径 (%s)", cleaned)
			}
		}
	}
	return ""
}

// recordAudit 追加一条脱敏后的工具调用审计日志，并在配置了回调时同步触发上报。
func (g *Guard) recordAudit(tool, target string, zone Zone, action DecisionAction, reason string) {
	entry := AuditEntry{
		Timestamp: time.Now().Unix(),
		ToolName:  tool,
		Target:    desensitize.MaskSecrets(target),
		Zone:      zone,
		Action:    action,
		Reason:    desensitize.MaskSecrets(reason),
	}
	g.mu.Lock()
	g.auditLogs = append(g.auditLogs, entry)
	cb := g.onAudit
	g.mu.Unlock()
	if cb != nil {
		cb(entry)
	}
}

// isSubpathOrEqual 判断 child 是否等于 root 或位于 root 目录树内部。
func isSubpathOrEqual(root, child string) bool {
	cleanRoot := filepath.Clean(root)
	cleanChild := filepath.Clean(child)
	if cleanRoot == cleanChild {
		return true
	}
	return strings.HasPrefix(cleanChild, cleanRoot+string(os.PathSeparator))
}

// resolveExistingAncestorSymlink 解析目标路径或其最近存在父目录的真实物理路径。
func resolveExistingAncestorSymlink(target string) string {
	curr := filepath.Clean(target)
	var suffix []string
	for {
		if resolved, err := filepath.EvalSymlinks(curr); err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return ""
		}
		suffix = append(suffix, filepath.Base(curr))
		curr = parent
	}
}
