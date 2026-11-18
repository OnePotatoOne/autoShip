// Package sandbox 实现 macOS 权限档案沙箱（设计文档 8.3 节），支持 off / protected / custom 三档 SBPL 规则生成、sandbox-exec 进程包裹、拦截路径提取与一键放行策略更新。
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"autoship/internal/devagent/config"
	"autoship/pkg/desensitize"
)

// Mode 定义产品权限档案的三种隔离档位枚举。
type Mode string

const (
	// ModeOff 表示关闭系统级沙箱限制，仅依赖工具守卫与审计日志。
	ModeOff Mode = "off"
	// ModeProtected 表示默认保护档：默认放行读取但拦截敏感清单，写入严格限定在白名单目录，网络完全放行。
	ModeProtected Mode = "protected"
	// ModeCustom 表示自定义档：在保护档基础上按产品配置增减允许/禁止路径（支持后台一键放行）。
	ModeCustom Mode = "custom"
)

// DefaultSandboxExecBin 是 macOS 系统自带沙箱命令的标准路径。
const DefaultSandboxExecBin = "/usr/bin/sandbox-exec"

// ProfileSpec 汇总生成单个子进程 SBPL 权限档案所需的上下文目录与产品规则。
type ProfileSpec struct {
	ProductCode            string   // 产品唯一编码
	WorkID                 string   // 当前开发任务 ID
	Mode                   Mode     // 沙箱档位：off | protected | custom
	HomeDir                string   // 当前用户主目录（留空自动获取）
	AutoshipRootDir        string   // autoShip 工作根目录（如 ~/autoship）
	CurrentRepoDir         string   // 当前产品专用 Git 仓库目录（如 ~/autoship/repos/<product>）
	WorktreeDir            string   // 当前任务独立 worktree 目录（如 ~/autoship/work/<product>/<workId>）
	DepCacheDir            string   // 跨任务共享依赖缓存目录（如 ~/autoship/cache/<product>）
	TempDir                string   // 本任务专属临时目录（如 /tmp/autoship-<workId>）
	AgentStateDir          string   // Agent 自身状态与发件箱目录（敏感，禁止子进程读取）
	AgentPKIDir            string   // Agent 自身证书与私钥目录（敏感，禁止子进程读取）
	AgentConfigFile        string   // Agent 配置文件路径（敏感，禁止子进程读取）
	ReleaseKeyPaths        []string // 任何落盘的发布密钥或 deploy key 路径（禁止模型子进程读取）
	ProductAllowWritePaths []string // 产品档案声明允许写入的额外目录
	CustomAllowReadPaths   []string // custom 档位下额外放行读取的路径
	CustomDenyReadPaths    []string // custom 档位下额外禁止读取的路径
	CustomAllowWritePaths  []string // custom 档位下额外放行写入的路径
	CustomDenyWritePaths   []string // custom 档位下额外禁止写入的路径
	SandboxExecBin         string   // 可选覆盖的 sandbox-exec 二进制路径（默认 /usr/bin/sandbox-exec）
}

// QuickActionOption 描述上报给 Server 管理后台的一键处置动作建议（8.3 节）。
type QuickActionOption struct {
	Action      string `json:"action"`      // 动作枚举：allow_path（加入允许清单）或 switch_off（切到关闭档）
	TargetPath  string `json:"targetPath"`  // 当 Action=allow_path 时对应的被拦截路径
	Description string `json:"description"` // 后台展示给管理员的按钮文案
}

// InterceptEvent 是子进程触发权限档案拦截后提取并上报给 Server 活动流的结构化事件。
type InterceptEvent struct {
	EventType        string              `json:"eventType"`        // 固定为 evt.sandbox.intercepted
	ProductCode      string              `json:"productCode"`      // 所属产品编码
	WorkID           string              `json:"workId"`           // 所属开发任务 ID
	Mode             Mode                `json:"mode"`             // 触发拦截时的沙箱档位
	Operation        string              `json:"operation"`        // 推断的被拦操作类型：read | write | unknown
	BlockedPaths     []string            `json:"blockedPaths"`     // 提取出的具体被拦截文件路径列表
	Command          string              `json:"command"`          // 被执行的脱敏命令摘要
	RawExcerpt       string              `json:"rawExcerpt"`       // 脱敏后的错误输出摘录
	SuggestedActions []QuickActionOption `json:"suggestedActions"` // 供后台一键放行或关闭沙箱的快捷动作
	OccurredAt       int64               `json:"occurredAt"`       // 发生时刻秒级时间戳
}

// SelfCheckResult 记录阶段 0 或启动自检时 macOS 沙箱机制的实测验证结果。
type SelfCheckResult struct {
	Platform          string `json:"platform"`          // 当前操作系统（如 darwin）
	Available         bool   `json:"available"`         // sandbox-exec 是否可用且语义符合预期
	AllowedReadWrite  bool   `json:"allowedReadWrite"`  // 白名单目录读写是否正常通过
	SensitiveBlocked  bool   `json:"sensitiveBlocked"`  // 敏感目录读取是否被成功拦截
	OutsideWriteBlock bool   `json:"outsideWriteBlock"` // 白名单外写入是否被成功拦截
	DenialParsed      bool   `json:"denialParsed"`      // 拦截输出能否准确解析出被拦路径
	FallbackMode      Mode   `json:"fallbackMode"`      // 推荐运行档位（可用为 protected，不可用退回 off）
	Detail            string `json:"detail"`            // 详细诊断说明
}

// BuildSpecFromProduct 根据全局 AgentConfig、产品档案与当前任务目录组装完整的 ProfileSpec。
// 参数 agentCfg 为全局配置，prod 为产品档案，workID 为任务 ID，worktreeDir 与 tempDir 为任务工作树和临时目录；返回可直接传入 BuildSBPL 或 WrapCommand 的 ProfileSpec。
func BuildSpecFromProduct(agentCfg *config.AgentConfig, prod config.ProductProfile, workID, worktreeDir, tempDir string) ProfileSpec {
	mode := Mode(strings.TrimSpace(prod.Sandbox.Mode))
	if mode == "" {
		mode = ModeProtected
	}
	var releaseKeys []string
	if strings.TrimSpace(prod.DeployKeyPath) != "" {
		releaseKeys = append(releaseKeys, prod.DeployKeyPath)
	}
	return ProfileSpec{
		ProductCode:            prod.ProductCode,
		WorkID:                 workID,
		Mode:                   mode,
		AutoshipRootDir:        agentCfg.RootDir,
		CurrentRepoDir:         filepath.Join(agentCfg.ReposDir(), prod.ProductCode),
		WorktreeDir:            worktreeDir,
		DepCacheDir:            filepath.Join(agentCfg.CacheDir(), prod.ProductCode),
		TempDir:                tempDir,
		AgentStateDir:          agentCfg.StateDir(),
		AgentPKIDir:            agentCfg.PKIDir(),
		ReleaseKeyPaths:        releaseKeys,
		ProductAllowWritePaths: prod.Sandbox.ExtraWritePaths,
		CustomAllowReadPaths:   prod.Sandbox.CustomAllowRead,
		CustomDenyReadPaths:    prod.Sandbox.CustomDenyRead,
		CustomAllowWritePaths:  prod.Sandbox.CustomAllowWrite,
		CustomDenyWritePaths:   prod.Sandbox.CustomDenyWrite,
	}
}

// BuildSBPL 根据给定的 ProfileSpec 生成标准 macOS SBPL（Sandbox Profile Language）规则文本。
// 参数 spec 为沙箱上下文定义；当 Mode=off 时返回空字符串与 nil，当 Mode=protected/custom 时返回完整 SBPL 字符串，若路径含非法控制字符则返回错误。
func BuildSBPL(spec ProfileSpec) (string, error) {
	switch spec.Mode {
	case "", ModeProtected:
		spec.Mode = ModeProtected
	case ModeOff:
		return "", nil
	case ModeCustom:
	default:
		return "", fmt.Errorf("不支持的权限档案档位: %q", spec.Mode)
	}

	home := strings.TrimSpace(spec.HomeDir)
	if home == "" {
		h, err := os.UserHomeDir()
		if err == nil {
			home = h
		}
	}

	var b strings.Builder
	b.WriteString("(version 1)\n")
	// 默认放行进程执行、系统调用与通用文件读取；网络显式放行以保障 go mod / npm 等依赖正常下载
	b.WriteString("(allow default)\n")
	b.WriteString("(allow network*)\n")
	// 默认禁止所有磁盘文件写入，仅对下方白名单目录逐项开启
	b.WriteString("(deny file-write*)\n")
	b.WriteString("(allow file-write* (literal \"/dev/null\") (literal \"/dev/zero\") (literal \"/dev/random\") (literal \"/dev/urandom\") (literal \"/dev/tty\") (subpath \"/dev/fd\"))\n")

	// 1. 组装敏感读取拦截清单：SSH 密钥、系统与用户钥匙串、其他产品仓库与工作区、Agent 自身凭据与状态、签名/部署私钥
	var sensitiveReadPaths []string
	if home != "" {
		sensitiveReadPaths = append(sensitiveReadPaths,
			filepath.Join(home, ".ssh"),
			filepath.Join(home, "Library", "Keychains"),
		)
	}
	sensitiveReadPaths = append(sensitiveReadPaths, "/Library/Keychains")
	if spec.AutoshipRootDir != "" {
		sensitiveReadPaths = append(sensitiveReadPaths,
			filepath.Join(spec.AutoshipRootDir, "repos"),
			filepath.Join(spec.AutoshipRootDir, "work"),
			filepath.Join(spec.AutoshipRootDir, "state"),
			filepath.Join(spec.AutoshipRootDir, "pki"),
		)
	}
	if spec.AgentStateDir != "" {
		sensitiveReadPaths = append(sensitiveReadPaths, spec.AgentStateDir)
	}
	if spec.AgentPKIDir != "" {
		sensitiveReadPaths = append(sensitiveReadPaths, spec.AgentPKIDir)
	}
	if spec.AgentConfigFile != "" {
		sensitiveReadPaths = append(sensitiveReadPaths, spec.AgentConfigFile)
	}
	sensitiveReadPaths = append(sensitiveReadPaths, spec.ReleaseKeyPaths...)

	if err := writeSBPLPathRule(&b, "deny", "file-read*", "subpath", sensitiveReadPaths); err != nil {
		return "", err
	}

	// 2. 在拦截整个 repos/ 和 work/ 根目录后，放行根目录本身的元数据访问（literal）以及当前产品仓库与本任务 worktree 的读取
	var parentLiterals []string
	if spec.AutoshipRootDir != "" {
		parentLiterals = append(parentLiterals,
			spec.AutoshipRootDir,
			filepath.Join(spec.AutoshipRootDir, "repos"),
			filepath.Join(spec.AutoshipRootDir, "work"),
		)
		if spec.ProductCode != "" {
			parentLiterals = append(parentLiterals, filepath.Join(spec.AutoshipRootDir, "work", spec.ProductCode))
		}
	}
	if err := writeSBPLPathRule(&b, "allow", "file-read*", "literal", parentLiterals); err != nil {
		return "", err
	}

	var allowReadSubpaths []string
	for _, p := range []string{spec.CurrentRepoDir, spec.WorktreeDir, spec.DepCacheDir, spec.TempDir} {
		if strings.TrimSpace(p) != "" {
			allowReadSubpaths = append(allowReadSubpaths, p)
		}
	}
	allowReadSubpaths = append(allowReadSubpaths, spec.ProductAllowWritePaths...)
	if err := writeSBPLPathRule(&b, "allow", "file-read*", "subpath", allowReadSubpaths); err != nil {
		return "", err
	}

	// 3. 组装保护档允许写入清单：本任务 worktree、本产品仓库 .git（供 git worktree 提交索引与对象）、依赖缓存目录、临时目录及产品声明目录
	var allowWriteSubpaths []string
	if strings.TrimSpace(spec.WorktreeDir) != "" {
		allowWriteSubpaths = append(allowWriteSubpaths, spec.WorktreeDir)
	}
	if strings.TrimSpace(spec.CurrentRepoDir) != "" {
		allowWriteSubpaths = append(allowWriteSubpaths, filepath.Join(spec.CurrentRepoDir, ".git"))
	}
	if strings.TrimSpace(spec.DepCacheDir) != "" {
		allowWriteSubpaths = append(allowWriteSubpaths, spec.DepCacheDir)
	}
	if strings.TrimSpace(spec.TempDir) != "" {
		allowWriteSubpaths = append(allowWriteSubpaths, spec.TempDir)
	}
	allowWriteSubpaths = append(allowWriteSubpaths, spec.ProductAllowWritePaths...)
	if err := writeSBPLPathRule(&b, "allow", "file-write*", "subpath", allowWriteSubpaths); err != nil {
		return "", err
	}

	// 4. custom 档位：支持在保护档基础上增减读写路径（后台一键放行写入 CustomAllowReadPaths / CustomAllowWritePaths，优先级最高）
	if spec.Mode == ModeCustom {
		if err := writeSBPLPathRule(&b, "deny", "file-read*", "subpath", spec.CustomDenyReadPaths); err != nil {
			return "", err
		}
		if err := writeSBPLPathRule(&b, "allow", "file-read*", "subpath", spec.CustomAllowReadPaths); err != nil {
			return "", err
		}
		if err := writeSBPLPathRule(&b, "deny", "file-write*", "subpath", spec.CustomDenyWritePaths); err != nil {
			return "", err
		}
		if err := writeSBPLPathRule(&b, "allow", "file-write*", "subpath", spec.CustomAllowWritePaths); err != nil {
			return "", err
		}
	}

	return b.String(), nil
}

// WrapCommand 在 darwin 平台且非 off 档位时使用 `/usr/bin/sandbox-exec -p <profile>` 包裹目标子进程命令。
// 参数 ctx 为生命周期上下文，spec 为沙箱规则配置，name 与 args 为原始程序及参数；返回组装好的 *exec.Cmd、实际生效的 SBPL 文本及错误。
func WrapCommand(ctx context.Context, spec ProfileSpec, name string, args ...string) (*exec.Cmd, string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, "", errors.New("子进程命令名不能为空")
	}
	sbpl, err := BuildSBPL(spec)
	if err != nil {
		return nil, "", err
	}
	sandboxBin := strings.TrimSpace(spec.SandboxExecBin)
	if sandboxBin == "" {
		sandboxBin = DefaultSandboxExecBin
	}

	var cmd *exec.Cmd
	// 在 macOS 平台（或测试显式注入 SandboxExecBin）且非关闭档时启用 sandbox-exec 包裹
	if sbpl != "" && (runtime.GOOS == "darwin" || strings.TrimSpace(spec.SandboxExecBin) != "") {
		wrappedArgs := make([]string, 0, len(args)+3)
		wrappedArgs = append(wrappedArgs, "-p", sbpl, name)
		wrappedArgs = append(wrappedArgs, args...)
		cmd = exec.CommandContext(ctx, sandboxBin, wrappedArgs...)
	} else {
		cmd = exec.CommandContext(ctx, name, args...)
	}

	if strings.TrimSpace(spec.WorktreeDir) != "" {
		cmd.Dir = spec.WorktreeDir
	}
	if strings.TrimSpace(spec.TempDir) != "" {
		// 将子进程默认临时目录引导至本任务已授权写入的专属临时目录，防止工具链写系统根临时目录受阻
		env := os.Environ()
		env = setOrReplaceEnv(env, "TMPDIR", spec.TempDir)
		cmd.Env = env
	}
	return cmd, sbpl, nil
}

var (
	// colonPermDeniedRe 匹配标准 POSIX 工具输出，如 `cat: /Users/x/.ssh/id_rsa: Operation not permitted` 或 `open /path/x: operation not permitted`
	colonPermDeniedRe = regexp.MustCompile(`(?im)(?:^|:\s*|\bopen\s+|\bmkdir\s+|\bstat\s+|\bremove\s+|\bunlink\s+|\brename\s+)['"]?(/[^:'"\r\n]+?)['"]?\s*:\s*operation not permitted`)
	// quotedPermDeniedRe 匹配 Node/Python 等运行时输出，如 `EPERM: operation not permitted, open '/path/to/file'` 或 `[Errno 1] Operation not permitted: '/path'`
	quotedPermDeniedRe = regexp.MustCompile(`(?im)operation not permitted[^'\r\n]*['"](/[^'"\r\n]+)['"]`)
	// sbplTraceDenyRe 匹配 macOS 沙箱审计轨迹，如 `deny(1) file-write-create /private/tmp/foo`
	sbplTraceDenyRe = regexp.MustCompile(`(?im)\bdeny(?:\(\d+\))?\s+file-(read|write)[a-z-]*\s+(/[^\s'"\r\n]+)`)
)

// ParseDenialOutput 分析子进程标准错误与标准输出，识别沙箱权限拒绝信息并提取被拦截的具体路径，生成可上报 Server 的 InterceptEvent。
// 参数 productCode/workID/mode/command 为执行上下文，combinedOutput 为命令输出文本；若未检测到沙箱拦截则返回 nil。
func ParseDenialOutput(productCode, workID string, mode Mode, command, combinedOutput string) *InterceptEvent {
	if mode == ModeOff || strings.TrimSpace(combinedOutput) == "" {
		return nil
	}
	lower := strings.ToLower(combinedOutput)
	hasSandboxKeyword := strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "sandbox-exec:") ||
		(strings.Contains(lower, "deny") && strings.Contains(lower, "file-"))
	if !hasSandboxKeyword {
		return nil
	}

	seenPaths := make(map[string]bool)
	var blockedPaths []string
	addPath := func(raw string) {
		cleaned := strings.TrimSpace(raw)
		if cleaned == "" || !strings.HasPrefix(cleaned, "/") {
			return
		}
		cleaned = filepath.Clean(cleaned)
		if !seenPaths[cleaned] {
			seenPaths[cleaned] = true
			blockedPaths = append(blockedPaths, cleaned)
		}
	}

	operation := "unknown"
	for _, m := range sbplTraceDenyRe.FindAllStringSubmatch(combinedOutput, -1) {
		if len(m) >= 3 {
			if operation == "unknown" {
				operation = strings.ToLower(m[1])
			}
			addPath(m[2])
		}
	}
	for _, m := range colonPermDeniedRe.FindAllStringSubmatch(combinedOutput, -1) {
		if len(m) >= 2 {
			addPath(m[1])
		}
	}
	for _, m := range quotedPermDeniedRe.FindAllStringSubmatch(combinedOutput, -1) {
		if len(m) >= 2 {
			addPath(m[1])
		}
	}

	// 根据常见命令前缀辅助推断读写类别，便于后台展示更直观的拦截原因
	if operation == "unknown" {
		if strings.Contains(lower, "touch:") || strings.Contains(lower, "mkdir") || strings.Contains(lower, "tee:") || strings.Contains(lower, "rm:") || strings.Contains(lower, "cp:") {
			operation = "write"
		} else if strings.Contains(lower, "cat:") || strings.Contains(lower, "head:") || strings.Contains(lower, "ls:") {
			operation = "read"
		}
	}

	var actions []QuickActionOption
	for _, p := range blockedPaths {
		actions = append(actions, QuickActionOption{
			Action:      "allow_path",
			TargetPath:  p,
			Description: fmt.Sprintf("将该路径加入产品 %s 的允许清单: %s", productCode, p),
		})
	}
	actions = append(actions, QuickActionOption{
		Action:      "switch_off",
		Description: fmt.Sprintf("将产品 %s 的权限档案降级为“关闭”档", productCode),
	})

	excerpt := desensitize.MaskSecrets(strings.TrimSpace(combinedOutput))
	if len(excerpt) > 1024 {
		excerpt = excerpt[:1024] + "...(truncated)"
	}

	return &InterceptEvent{
		EventType:        "evt.sandbox.intercepted",
		ProductCode:      productCode,
		WorkID:           workID,
		Mode:             mode,
		Operation:        operation,
		BlockedPaths:     blockedPaths,
		Command:          desensitize.MaskSecrets(command),
		RawExcerpt:       excerpt,
		SuggestedActions: actions,
		OccurredAt:       time.Now().Unix(),
	}
}

// ApplyQuickAction 执行来自管理后台的一键放行路径或一键降级关闭动作（8.3 节）。
// 参数 policy 为待修改的产品权限档案指针，action 为 "allow_path" 或 "switch_off"，targetPath 为待放行路径；成功返回 nil，参数非法返回错误。
func ApplyQuickAction(policy *config.SandboxPolicyConfig, action, targetPath string) error {
	if policy == nil {
		return errors.New("sandbox policy 不能为空")
	}
	switch strings.TrimSpace(action) {
	case "switch_off":
		policy.Mode = string(ModeOff)
		return nil
	case "allow_path":
		cleaned := config.ExpandPath(targetPath)
		if cleaned == "" || !filepath.IsAbs(cleaned) {
			return fmt.Errorf("待放行路径必须是非空绝对路径: %q", targetPath)
		}
		// 从默认保护档升级为 custom 档位，将该路径同时加入读写放行清单确保立即解除阻塞
		if policy.Mode == "" || policy.Mode == string(ModeProtected) {
			policy.Mode = string(ModeCustom)
		}
		policy.CustomAllowRead = appendUnique(policy.CustomAllowRead, cleaned)
		policy.CustomAllowWrite = appendUnique(policy.CustomAllowWrite, cleaned)
		return nil
	default:
		return fmt.Errorf("未知的权限档案快捷动作: %q", action)
	}
}

// SelfCheck 在当前机器上运行一次真实沙箱隔离自检（用于阶段 0 基准工具 autoship-bench 与守护进程启动自检）。
// 参数 ctx 为超时上下文，tempRoot 为自检使用的临时目录（为空时自动创建并销毁）；返回 *SelfCheckResult 及执行错误。
func SelfCheck(ctx context.Context, tempRoot string) (*SelfCheckResult, error) {
	res := &SelfCheckResult{
		Platform:     runtime.GOOS,
		FallbackMode: ModeOff,
	}
	if runtime.GOOS != "darwin" {
		res.Detail = "当前非 darwin 平台，macOS sandbox-exec 不适用，自动回退至 off 档"
		return res, nil
	}
	if _, err := os.Stat(DefaultSandboxExecBin); err != nil {
		res.Detail = fmt.Sprintf("未找到 %s: %v，自动回退至 off 档", DefaultSandboxExecBin, err)
		return res, nil
	}

	cleanup := func() {}
	if strings.TrimSpace(tempRoot) == "" {
		dir, err := os.MkdirTemp("", "autoship-sandbox-check-*")
		if err != nil {
			return res, fmt.Errorf("创建沙箱自检临时目录失败: %w", err)
		}
		tempRoot = dir
		cleanup = func() { _ = os.RemoveAll(dir) }
	}
	defer cleanup()

	fakeAutoship := filepath.Join(tempRoot, "autoship")
	currRepo := filepath.Join(fakeAutoship, "repos", "prod-a")
	otherRepo := filepath.Join(fakeAutoship, "repos", "prod-b")
	worktree := filepath.Join(fakeAutoship, "work", "prod-a", "task-1")
	taskTmp := filepath.Join(tempRoot, "task-tmp")
	outsideDir := filepath.Join(tempRoot, "outside")
	for _, d := range []string{currRepo, otherRepo, worktree, taskTmp, outsideDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			return res, err
		}
	}
	otherSecret := filepath.Join(otherRepo, "secret.txt")
	if err := os.WriteFile(otherSecret, []byte("top-secret"), 0600); err != nil {
		return res, err
	}

	spec := ProfileSpec{
		ProductCode:     "prod-a",
		WorkID:          "task-1",
		Mode:            ModeProtected,
		AutoshipRootDir: fakeAutoship,
		CurrentRepoDir:  currRepo,
		WorktreeDir:     worktree,
		TempDir:         taskTmp,
	}

	// 1. 测试允许目录写入与读取
	allowFile := filepath.Join(worktree, "ok.txt")
	okOut, okErr := runWrapped(ctx, spec, "/bin/sh", "-c", fmt.Sprintf("echo pass > %q && /bin/cat %q", allowFile, allowFile))
	res.AllowedReadWrite = okErr == nil && strings.TrimSpace(okOut) == "pass"

	// 2. 测试跨产品敏感目录读取拦截与路径解析
	readOut, readErr := runWrapped(ctx, spec, "/bin/cat", otherSecret)
	res.SensitiveBlocked = readErr != nil
	evtRead := ParseDenialOutput("prod-a", "task-1", ModeProtected, "/bin/cat "+otherSecret, readOut)

	// 3. 测试白名单外写入拦截
	outsideFile := filepath.Join(outsideDir, "forbid.txt")
	writeOut, writeErr := runWrapped(ctx, spec, "/usr/bin/touch", outsideFile)
	res.OutsideWriteBlock = writeErr != nil
	evtWrite := ParseDenialOutput("prod-a", "task-1", ModeProtected, "/usr/bin/touch "+outsideFile, writeOut)

	res.DenialParsed = evtRead != nil && len(evtRead.BlockedPaths) > 0 && evtWrite != nil && len(evtWrite.BlockedPaths) > 0
	res.Available = res.AllowedReadWrite && res.SensitiveBlocked && res.OutsideWriteBlock && res.DenialParsed
	if res.Available {
		res.FallbackMode = ModeProtected
		res.Detail = "macOS sandbox-exec 自检全部通过：白名单读写正常，跨产品读取与越界写入均成功拦截并提取路径"
	} else {
		res.Detail = fmt.Sprintf("自检未完全通过(rw=%v, readBlock=%v, writeBlock=%v, parsed=%v)，建议降级 off 档",
			res.AllowedReadWrite, res.SensitiveBlocked, res.OutsideWriteBlock, res.DenialParsed)
	}
	return res, nil
}

// runWrapped 执行一次沙箱包裹命令并返回合并的 stdout/stderr 文本与退出错误。
func runWrapped(ctx context.Context, spec ProfileSpec, name string, args ...string) (string, error) {
	cmd, _, err := WrapCommand(ctx, spec, name, args...)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	return out.String(), runErr
}

// writeSBPLPathRule 将一组路径展开（同时兼顾 macOS /var 与 /private/var 等符号链接别名）并写入一条 SBPL 规则语句。
func writeSBPLPathRule(b *strings.Builder, effect, action, matchKind string, rawPaths []string) error {
	expanded, err := expandWithRealpaths(rawPaths)
	if err != nil {
		return err
	}
	if len(expanded) == 0 {
		return nil
	}
	b.WriteString("(")
	b.WriteString(effect)
	b.WriteString(" ")
	b.WriteString(action)
	for _, p := range expanded {
		escaped := strings.ReplaceAll(p, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		b.WriteString(fmt.Sprintf(" (%s %q)", matchKind, escaped))
	}
	b.WriteString(")\n")
	return nil
}

// expandWithRealpaths 展开 `~`、清理路径，并为每个路径补充符号链接解析后的真实内核路径（解决 macOS /tmp -> /private/tmp 导致 SBPL 失配的问题）。
func expandWithRealpaths(rawPaths []string) ([]string, error) {
	seen := make(map[string]bool)
	var result []string
	add := func(p string) error {
		if strings.ContainsAny(p, "\r\n\x00") {
			return fmt.Errorf("沙箱路径包含非法控制字符: %q", p)
		}
		if !filepath.IsAbs(p) {
			abs, err := filepath.Abs(p)
			if err == nil {
				p = abs
			}
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
		return nil
	}

	for _, raw := range rawPaths {
		expanded := config.ExpandPath(raw)
		if expanded == "" {
			continue
		}
		if err := add(expanded); err != nil {
			return nil, err
		}
		if resolved := resolveNearestExistingSymlink(expanded); resolved != "" && resolved != expanded {
			if err := add(resolved); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

// resolveNearestExistingSymlink 向上回溯到最近已存在的父目录执行 EvalSymlinks 再拼回子路径，支持尚未创建的目录正确解析 /private 前缀。
func resolveNearestExistingSymlink(target string) string {
	curr := filepath.Clean(target)
	var suffixParts []string
	for {
		realCurr, err := filepath.EvalSymlinks(curr)
		if err == nil {
			for i := len(suffixParts) - 1; i >= 0; i-- {
				realCurr = filepath.Join(realCurr, suffixParts[i])
			}
			return filepath.Clean(realCurr)
		}
		parent := filepath.Dir(curr)
		if parent == curr {
			return ""
		}
		suffixParts = append(suffixParts, filepath.Base(curr))
		curr = parent
	}
}

// setOrReplaceEnv 在环境变量切片中设置或覆盖指定键的值。
func setOrReplaceEnv(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			if !replaced {
				out = append(out, prefix+val)
				replaced = true
			}
			continue
		}
		out = append(out, item)
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}

// appendUnique 向字符串切片去重追加非空项。
func appendUnique(list []string, item string) []string {
	for _, existing := range list {
		if existing == item {
			return list
		}
	}
	return append(list, item)
}
