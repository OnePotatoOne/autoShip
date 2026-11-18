// Package apply 实现产品服务器 root 提权小工具 autoship-apply 的核心执行器（对应设计文档 9.1 节）。
// 本包强制执行无通配固定子命令、TOCTOU 隔离复制、Server 动作票据验签、制品包二次验签、环境变量清空、部署账本维护及备份目录摘要汇总。
package apply

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"autoship/internal/updateagent/config"
	"autoship/pkg/envelope"
	"autoship/pkg/manifest"
	"autoship/pkg/semver"
)

const (
	// ActionUpgrade 表示原子升级并安装新部署钩子与业务制品的子命令。
	ActionUpgrade = "upgrade"
	// ActionRollback 表示按部署 ID 幂等回滚到升级前版本的子命令。
	ActionRollback = "rollback"
	// ActionBackup 表示在专用 0700 目录下调用钩子导出数据库的固定子命令。
	ActionBackup = "backup"
	// ActionLogs 表示读取最近 200 行服务日志的固定子命令。
	ActionLogs = "logs"
	// ActionCleanup 表示通知产品部署钩子清理过期旧程序备份的固定子命令。
	ActionCleanup = "cleanup"

	// StagingTicketFileName 是暂存目录中存放动作票据的固定文件名。
	StagingTicketFileName = "ticket.json"
	// StagingBundleFileName 是暂存目录中存放签名制品包的固定文件名。
	StagingBundleFileName = "bundle.tar.gz"

	// SafeMinimalPath 是调用产品部署钩子时唯一保留的最小安全 PATH 环境变量值。
	SafeMinimalPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	// maxTicketFileBytes 限制票据文件最大读取体积（64 KiB），防止异常大文件耗尽内存。
	maxTicketFileBytes = 64 * 1024
	// maxLogsOutputBytes 限制单次 logs 200 输出的最大字节数（256 KiB）。
	maxLogsOutputBytes = 256 * 1024
)

// SafeHookEnv 返回调用产品部署钩子时使用的极简环境变量切片（清空全部继承环境，仅保留安全 PATH）。
// 无入参；返回只含单个 PATH 项的字符串切片，供所有钩子子进程调用前赋值 cmd.Env。
func SafeHookEnv() []string {
	return []string{"PATH=" + SafeMinimalPath}
}

// LedgerEntry 记录单次部署在 root 专属账本（/var/lib/autoship-apply/ledger.json）中的权威元数据。
type LedgerEntry struct {
	DeploymentID string `json:"deploymentId"` // 部署记录唯一标识
	ProductCode  string `json:"productCode"`  // 产品唯一编码
	Version      string `json:"version"`      // 本次部署的目标语义化版本号
	HookPath     string `json:"hookPath"`     // 安装并使用的部署钩子绝对路径
	BinarySHA256 string `json:"binarySha256"` // 业务程序制品 SHA-256 摘要
	InstalledAt  int64  `json:"installedAt"`  // 安装记录秒级 Unix 时间戳
	Status       string `json:"status"`       // 状态：installing | installed | install_failed | rolled_back
}

// Ledger 表示持久化在 /var/lib/autoship-apply/ledger.json 的部署账本根结构。
type Ledger struct {
	Deployments map[string]LedgerEntry `json:"deployments"` // deployment_id -> 账本条目映射
}

// BackupFileInfo 描述数据库备份目录内单个导出文件的相对名称、字节大小与 SHA-256 校验和。
type BackupFileInfo struct {
	Name   string `json:"name"`   // 相对于本次部署备份目录的文件路径
	Size   int64  `json:"size"`   // 文件字节大小
	SHA256 string `json:"sha256"` // 文件内容小写十六进制 SHA-256
}

// BackupResult 是 autoship-apply backup 执行成功后输出到标准输出的 JSON 汇总结构，供 autoship-agent 读取并上报 Server。
type BackupResult struct {
	ProductCode   string           `json:"productCode"`   // 产品唯一编码
	DeploymentID  string           `json:"deploymentId"`  // 关联部署唯一标识
	BackupDir     string           `json:"backupDir"`     // 备份专用目录绝对路径
	Files         []BackupFileInfo `json:"files"`         // 备份文件明细清单（按相对路径升序排列）
	TotalBytes    int64            `json:"totalBytes"`    // 备份目录文件总字节数
	SummarySHA256 string           `json:"summarySha256"` // 基于全量文件清单计算的确定性汇总 SHA-256
	CreatedAt     int64            `json:"createdAt"`     // 备份完成秒级 Unix 时间戳
}

// HookExecError 封装产品部署钩子执行失败时的退出码与标准错误摘要，便于上层原样透传退出码。
type HookExecError struct {
	Subcommand string // 触发失败的钩子子命令名
	ExitCode   int    // 子进程非零退出码
	StderrTail string // 截断后的错误输出末尾
	Err        error  // 底层执行错误
}

// Error 返回包含钩子子命令、退出码及错误详情的格式化描述文本。
// 无入参；返回错误字符串，满足标准 error 接口。
func (e *HookExecError) Error() string {
	if strings.TrimSpace(e.StderrTail) != "" {
		return fmt.Sprintf("执行部署钩子 %s 失败（退出码 %d）: %s", e.Subcommand, e.ExitCode, strings.TrimSpace(e.StderrTail))
	}
	return fmt.Sprintf("执行部署钩子 %s 失败（退出码 %d）: %v", e.Subcommand, e.ExitCode, e.Err)
}

// Unwrap 返回底层原始错误以支持 errors.Is / errors.As 链式断言。
// 无入参；返回底层 error。
func (e *HookExecError) Unwrap() error {
	return e.Err
}

// ValidateArgs 严格校验命令行参数列表，仅允许单一无参固定子命令（杜绝 sudo 通配参数与选项注入）。
// 参数 args 为不含程序名 os.Args[0] 的参数切片（即 os.Args[1:]）；合法时返回子命令字符串与 nil，否则返回错误。
func ValidateArgs(args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("autoship-apply 仅接受单一固定子命令，实际参数个数: %d", len(args))
	}
	sub := args[0]
	switch sub {
	case ActionUpgrade, ActionRollback, ActionBackup, ActionLogs, ActionCleanup:
		return sub, nil
	default:
		return "", fmt.Errorf("不支持的固定子命令: %q（仅允许 upgrade|rollback|backup|logs|cleanup）", sub)
	}
}

// Executor 封装 autoship-apply 的全部特权受控操作及可注入时钟（供单元测试验证过期票据）。
type Executor struct {
	cfg   *config.ApplyConfig // 受保护路径与超时配置
	nowFn func() time.Time    // 获取当前时刻的函数，默认 time.Now
}

// NewExecutor 基于给定的 ApplyConfig 创建 Executor 实例。
// 参数 cfg 为已校验的 *config.ApplyConfig（传 nil 时使用默认安全配置）；返回 *Executor 实例，通过 apply.NewExecutor(cfg) 调用。
func NewExecutor(cfg *config.ApplyConfig) *Executor {
	if cfg == nil {
		cfg = config.DefaultApplyConfig()
	}
	return &Executor{
		cfg:   cfg,
		nowFn: time.Now,
	}
}

// SetNowFunc 覆盖内部时钟函数（仅用于测试票据有效期边界）。
// 参数 fn 为返回当前时间的函数；无返回值。
func (e *Executor) SetNowFunc(fn func() time.Time) {
	if fn != nil {
		e.nowFn = fn
	}
}

// Run 校验命令行参数并分发执行对应的特权子命令。
// 参数 ctx 为控制超时的上下文，args 为 os.Args[1:]，stdout/stderr 为标准输出与错误写入流；成功返回 nil，失败返回错误。
func (e *Executor) Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := e.cfg.Validate(); err != nil {
		return err
	}
	action, err := ValidateArgs(args)
	if err != nil {
		return err
	}

	hookTimeout := time.Duration(e.cfg.HookTimeoutSec) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, hookTimeout)
	defer cancel()

	switch action {
	case ActionUpgrade:
		return e.runUpgrade(runCtx, stdout, stderr)
	case ActionRollback:
		return e.runRollback(runCtx, stdout, stderr)
	case ActionBackup:
		return e.runBackup(runCtx, stdout, stderr)
	case ActionLogs:
		return e.runLogs(runCtx, stdout, stderr)
	case ActionCleanup:
		return e.runCleanup(runCtx, stdout, stderr)
	default:
		return fmt.Errorf("未知子命令: %s", action)
	}
}

// runUpgrade 执行完整的 TOCTOU 安全复制、双公钥验签、钩子原子替换、部署调用及账本落盘流程（9.1 第 3 步）。
// 参数 ctx 为执行上下文，stdout/stderr 为输出流；成功返回 nil，任何验签或安装异常返回错误。
func (e *Executor) runUpgrade(ctx context.Context, stdout, stderr io.Writer) error {
	// 1. 在校验任何内容前先把暂存区的票据与制品包复制到 root 专属 0700 临时目录，彻底消除 TOCTOU 竞争窗口
	workDir, ticket, err := e.stageAndVerifyTicket(ActionUpgrade, []string{ActionUpgrade}, true)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	releasePubB64, err := readTrimmedKeyFile(e.cfg.ReleasePubPath)
	if err != nil {
		return fmt.Errorf("读取发布验签公钥失败: %w", err)
	}

	isolatedBundle := filepath.Join(workDir, StagingBundleFileName)
	unpackDir := filepath.Join(workDir, "unpacked")
	relManifest, binaryPath, pkgHookPath, err := manifest.UnpackAndVerifyBundle(isolatedBundle, unpackDir, releasePubB64)
	if err != nil {
		return fmt.Errorf("制品包二次验签或解包失败: %w", err)
	}

	// 核对票据与制品清单的产品编码和版本号一致，防止用旧版合法签名包冒充本次升级目标版本
	if relManifest.ProductCode != ticket.ProductCode {
		return fmt.Errorf("制品清单产品编码 (%s) 与动作票据 (%s) 不一致", relManifest.ProductCode, ticket.ProductCode)
	}
	if strings.TrimSpace(ticket.Version) != "" && !sameSemver(relManifest.Version, ticket.Version) {
		return fmt.Errorf("制品清单版本号 (%s) 与动作票据目标版本 (%s) 不一致", relManifest.Version, ticket.Version)
	}
	if binaryPath == "" || pkgHookPath == "" {
		return errors.New("制品包缺少业务程序或部署钩子文件")
	}

	_, binSHA256, err := manifest.HashFile(binaryPath)
	if err != nil {
		return fmt.Errorf("计算解压后业务程序哈希失败: %w", err)
	}

	// 原子替换安装经过发布私钥签名的新部署钩子（权限 0700）
	if err := atomicInstallExecutable(pkgHookPath, e.cfg.HookPath, 0700); err != nil {
		return fmt.Errorf("原子安装部署钩子失败: %w", err)
	}

	// 先将 installing 状态写入账本，保证即使 install 中途异常也可按 deployment_id 定位钩子回滚
	nowUnix := e.nowFn().Unix()
	entry := LedgerEntry{
		DeploymentID: ticket.DeploymentID,
		ProductCode:  ticket.ProductCode,
		Version:      relManifest.Version,
		HookPath:     e.cfg.HookPath,
		BinarySHA256: binSHA256,
		InstalledAt:  nowUnix,
		Status:       "installing",
	}
	if err := e.upsertLedgerEntry(entry); err != nil {
		return fmt.Errorf("写入部署账本预备记录失败: %w", err)
	}

	// 清空环境变量调用部署钩子 install <binaryPath> --deployment <deploymentID>
	outBuf, errBuf, runErr := runCleanHook(ctx, e.cfg.HookPath, "install", binaryPath, "--deployment", ticket.DeploymentID)
	if outBuf.Len() > 0 && stdout != nil {
		_, _ = stdout.Write(tailBytes(outBuf.Bytes(), 16*1024))
	}
	if runErr != nil {
		if errBuf.Len() > 0 && stderr != nil {
			_, _ = stderr.Write(tailBytes(errBuf.Bytes(), 16*1024))
		}
		entry.Status = "install_failed"
		_ = e.upsertLedgerEntry(entry)
		return wrapHookError("install", runErr, errBuf.String(), outBuf.String())
	}

	entry.InstalledAt = e.nowFn().Unix()
	entry.Status = "installed"
	if err := e.upsertLedgerEntry(entry); err != nil {
		return fmt.Errorf("更新部署账本完成状态失败: %w", err)
	}
	return nil
}

// runRollback 验证票据后按 deployment_id 查账本，清空环境变量幂等调用部署钩子回滚（9.1 第 4 步）。
// 参数 ctx 为执行上下文，stdout/stderr 为输出流；成功返回 nil，账本缺失或回滚失败返回错误。
func (e *Executor) runRollback(ctx context.Context, stdout, stderr io.Writer) error {
	// 允许独立手动回滚票据（action=rollback）以及升级第 7 步自动回滚使用的当次升级票据（action=upgrade）
	workDir, ticket, err := e.stageAndVerifyTicket(ActionRollback, []string{ActionRollback, ActionUpgrade}, false)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	ledger, err := e.loadLedger()
	if err != nil {
		return fmt.Errorf("加载部署账本失败: %w", err)
	}
	entry, ok := ledger.Deployments[ticket.DeploymentID]
	if !ok {
		return fmt.Errorf("部署账本中未找到 deployment_id=%s 的记录，拒绝回滚", ticket.DeploymentID)
	}
	if entry.ProductCode != "" && entry.ProductCode != ticket.ProductCode {
		return fmt.Errorf("票据产品编码 (%s) 与账本记录 (%s) 不匹配", ticket.ProductCode, entry.ProductCode)
	}

	hookPath := strings.TrimSpace(entry.HookPath)
	if hookPath == "" {
		hookPath = e.cfg.HookPath
	}
	if err := ensureRegularExecutable(hookPath); err != nil {
		return fmt.Errorf("部署钩子不可用: %w", err)
	}

	outBuf, errBuf, runErr := runCleanHook(ctx, hookPath, "rollback", "--deployment", ticket.DeploymentID)
	if outBuf.Len() > 0 && stdout != nil {
		_, _ = stdout.Write(tailBytes(outBuf.Bytes(), 16*1024))
	}
	if runErr != nil {
		if errBuf.Len() > 0 && stderr != nil {
			_, _ = stderr.Write(tailBytes(errBuf.Bytes(), 16*1024))
		}
		return wrapHookError("rollback", runErr, errBuf.String(), outBuf.String())
	}

	entry.Status = "rolled_back"
	if err := e.upsertLedgerEntry(entry); err != nil {
		return fmt.Errorf("更新账本回滚状态失败: %w", err)
	}
	return nil
}

// runBackup 验证票据后创建 0700 备份专用目录，调用钩子导出数据库并扫描输出备份文件清单与 SHA-256 JSON（9.1 第 5 步）。
// 参数 ctx 为执行上下文，stdout/stderr 为输出流（stdout 仅输出纯净 JSON 供 autoship-agent 解析）；成功返回 nil，失败返回错误。
func (e *Executor) runBackup(ctx context.Context, stdout, stderr io.Writer) error {
	// 允许独立备份票据（action=backup）以及升级第 4 步前置备份使用的升级票据（action=upgrade）
	needBundleIfHookMissing := !regularFileExists(e.cfg.HookPath) && regularFileExists(filepath.Join(e.cfg.StagingDir, StagingBundleFileName))
	workDir, ticket, err := e.stageAndVerifyTicket(ActionBackup, []string{ActionBackup, ActionUpgrade}, needBundleIfHookMissing)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	// 若首次部署前服务器上尚未落盘钩子且暂存区已有签名升级包，先用 release.pub 验签提取部署钩子
	if !regularFileExists(e.cfg.HookPath) && needBundleIfHookMissing && ticket.Action == ActionUpgrade {
		if err := e.bootstrapHookFromIsolatedBundle(workDir, ticket); err != nil {
			return err
		}
	}
	if err := ensureRegularExecutable(e.cfg.HookPath); err != nil {
		return fmt.Errorf("执行数据库备份前未找到可用部署钩子: %w", err)
	}

	backupDir := filepath.Join(e.cfg.BackupBaseDir, ticket.ProductCode, ticket.DeploymentID)
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return fmt.Errorf("创建备份专用目录失败: %w", err)
	}
	if err := os.Chmod(backupDir, 0700); err != nil {
		return fmt.Errorf("设置备份专用目录 0700 权限失败: %w", err)
	}

	outBuf, errBuf, runErr := runCleanHook(ctx, e.cfg.HookPath, "backup-db", "--deployment", ticket.DeploymentID, "--dir", backupDir)
	if runErr != nil {
		if errBuf.Len() > 0 && stderr != nil {
			_, _ = stderr.Write(tailBytes(errBuf.Bytes(), 16*1024))
		}
		return wrapHookError("backup-db", runErr, errBuf.String(), outBuf.String())
	}

	summary, err := scanBackupDirectory(backupDir, ticket.ProductCode, ticket.DeploymentID, e.nowFn())
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("序列化数据库备份结果 JSON 失败: %w", err)
	}
	if stdout != nil {
		_, _ = stdout.Write(append(encoded, '\n'))
	}
	return nil
}

// runLogs 清空环境变量调用部署钩子读取最近 200 行服务日志并输出到 stdout（9.1 第 6 步）。
// 参数 ctx 为执行上下文，stdout/stderr 为输出流；成功返回 nil，钩子异常返回错误。
func (e *Executor) runLogs(ctx context.Context, stdout, stderr io.Writer) error {
	if err := ensureRegularExecutable(e.cfg.HookPath); err != nil {
		return fmt.Errorf("读取日志前检查部署钩子失败: %w", err)
	}
	outBuf, errBuf, runErr := runCleanHook(ctx, e.cfg.HookPath, "logs", "200")
	if runErr != nil {
		if errBuf.Len() > 0 && stderr != nil {
			_, _ = stderr.Write(tailBytes(errBuf.Bytes(), 16*1024))
		}
		return wrapHookError("logs", runErr, errBuf.String(), outBuf.String())
	}
	if stdout != nil && outBuf.Len() > 0 {
		_, _ = stdout.Write(tailBytes(outBuf.Bytes(), maxLogsOutputBytes))
	}
	return nil
}

// runCleanup 清空环境变量调用部署钩子清理过期的历史程序备份（9.1 第 6 步）。
// 参数 ctx 为执行上下文，stdout/stderr 为输出流；成功返回 nil，失败返回错误。
func (e *Executor) runCleanup(ctx context.Context, stdout, stderr io.Writer) error {
	if err := ensureRegularExecutable(e.cfg.HookPath); err != nil {
		return fmt.Errorf("清理旧备份前检查部署钩子失败: %w", err)
	}
	outBuf, errBuf, runErr := runCleanHook(ctx, e.cfg.HookPath, "cleanup")
	if outBuf.Len() > 0 && stdout != nil {
		_, _ = stdout.Write(tailBytes(outBuf.Bytes(), 16*1024))
	}
	if runErr != nil {
		if errBuf.Len() > 0 && stderr != nil {
			_, _ = stderr.Write(tailBytes(errBuf.Bytes(), 16*1024))
		}
		return wrapHookError("cleanup", runErr, errBuf.String(), outBuf.String())
	}
	return nil
}

// stageAndVerifyTicket 将暂存区中的 ticket.json（及按需包含的 bundle.tar.gz）复制到 root 专属 0700 工作目录后再验签。
// 参数 subcmd 为当前执行的子命令名，allowedTicketActions 为允许的票据动作枚举，copyBundle 控制是否一并隔离复制 bundle.tar.gz；
// 返回隔离工作目录路径（如 /var/lib/autoship-apply/work/<ticketId>）、验签通过的 *envelope.ActionTicket 及错误。
func (e *Executor) stageAndVerifyTicket(subcmd string, allowedTicketActions []string, copyBundle bool) (string, *envelope.ActionTicket, error) {
	if err := os.MkdirAll(e.cfg.WorkBaseDir, 0700); err != nil {
		return "", nil, fmt.Errorf("创建 root 隔离工作根目录失败: %w", err)
	}
	if err := os.Chmod(e.cfg.WorkBaseDir, 0700); err != nil {
		return "", nil, fmt.Errorf("设置 root 隔离工作根目录 0700 权限失败: %w", err)
	}

	tmpDir, err := os.MkdirTemp(e.cfg.WorkBaseDir, ".staging-copy-*")
	if err != nil {
		return "", nil, fmt.Errorf("创建临时隔离工作目录失败: %w", err)
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.RemoveAll(tmpDir)
		}
	}()
	if err := os.Chmod(tmpDir, 0700); err != nil {
		return "", nil, fmt.Errorf("设置临时隔离目录权限失败: %w", err)
	}

	srcTicket := filepath.Join(e.cfg.StagingDir, StagingTicketFileName)
	dstTicket := filepath.Join(tmpDir, StagingTicketFileName)
	if err := copyRegularFileNoFollow(srcTicket, dstTicket, maxTicketFileBytes, 0600); err != nil {
		return "", nil, fmt.Errorf("复制暂存区动作票据失败: %w", err)
	}

	if copyBundle {
		srcBundle := filepath.Join(e.cfg.StagingDir, StagingBundleFileName)
		dstBundle := filepath.Join(tmpDir, StagingBundleFileName)
		if err := copyRegularFileNoFollow(srcBundle, dstBundle, 0, 0600); err != nil {
			return "", nil, fmt.Errorf("复制暂存区制品包失败: %w", err)
		}
	}

	// 所有解析与验签均在 root 0700 目录内的副本上执行，暂存区后续任何修改都无法影响结果
	rawTicket, err := os.ReadFile(dstTicket)
	if err != nil {
		return "", nil, fmt.Errorf("读取隔离副本动作票据失败: %w", err)
	}
	var ticket envelope.ActionTicket
	if err := json.Unmarshal(rawTicket, &ticket); err != nil {
		return "", nil, fmt.Errorf("解析动作票据 JSON 失败: %w", err)
	}
	if err := envelope.ValidateSafeID(ticket.TicketID); err != nil {
		return "", nil, fmt.Errorf("动作票据 ticketId 格式非法: %w", err)
	}

	actionAllowed := false
	for _, allowed := range allowedTicketActions {
		if ticket.Action == allowed {
			actionAllowed = true
			break
		}
	}
	if !actionAllowed {
		return "", nil, fmt.Errorf("子命令 %s 不接受动作类型为 %q 的票据", subcmd, ticket.Action)
	}

	serverPubB64, err := readTrimmedKeyFile(e.cfg.ServerPubPath)
	if err != nil {
		return "", nil, fmt.Errorf("读取 Server 验签公钥失败: %w", err)
	}
	if err := ticket.Verify(serverPubB64, ticket.Action, e.nowFn()); err != nil {
		return "", nil, fmt.Errorf("动作票据签名或时效校验不通过: %w", err)
	}

	// 将验证通过的临时目录重命名为 /var/lib/autoship-apply/work/<ticketId>（0700）
	finalWorkDir := filepath.Join(e.cfg.WorkBaseDir, ticket.TicketID)
	_ = os.RemoveAll(finalWorkDir)
	if err := os.Rename(tmpDir, finalWorkDir); err != nil {
		return "", nil, fmt.Errorf("锁定隔离工作目录失败: %w", err)
	}
	if err := os.Chmod(finalWorkDir, 0700); err != nil {
		_ = os.RemoveAll(finalWorkDir)
		return "", nil, fmt.Errorf("锁定隔离工作目录权限失败: %w", err)
	}
	cleanupTmp = false
	return finalWorkDir, &ticket, nil
}

// bootstrapHookFromIsolatedBundle 在首次升级前尚无已安装钩子时，从隔离副本的签名制品包中验签并原子安装部署钩子。
// 参数 workDir 为已隔离的 0700 工作目录，ticket 为已验签的升级动作票据；成功返回 nil，失败返回错误。
func (e *Executor) bootstrapHookFromIsolatedBundle(workDir string, ticket *envelope.ActionTicket) error {
	releasePubB64, err := readTrimmedKeyFile(e.cfg.ReleasePubPath)
	if err != nil {
		return fmt.Errorf("读取发布验签公钥失败: %w", err)
	}
	bundlePath := filepath.Join(workDir, StagingBundleFileName)
	unpackDir := filepath.Join(workDir, "unpacked-hook")
	relManifest, _, pkgHookPath, err := manifest.UnpackAndVerifyBundle(bundlePath, unpackDir, releasePubB64)
	if err != nil {
		return fmt.Errorf("首次备份前校验制品包钩子失败: %w", err)
	}
	if relManifest.ProductCode != ticket.ProductCode {
		return fmt.Errorf("制品包产品编码 (%s) 与票据 (%s) 不一致", relManifest.ProductCode, ticket.ProductCode)
	}
	if strings.TrimSpace(ticket.Version) != "" && !sameSemver(relManifest.Version, ticket.Version) {
		return fmt.Errorf("制品包版本 (%s) 与票据 (%s) 不一致", relManifest.Version, ticket.Version)
	}
	return atomicInstallExecutable(pkgHookPath, e.cfg.HookPath, 0700)
}

// loadLedger 从 root 专属账本文件加载历史部署记录（文件不存在时返回空映射）。
// 无入参；返回 *Ledger 实例及解析错误。
func (e *Executor) loadLedger() (*Ledger, error) {
	raw, err := os.ReadFile(e.cfg.LedgerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Ledger{Deployments: make(map[string]LedgerEntry)}, nil
		}
		return nil, err
	}
	var ledger Ledger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return nil, fmt.Errorf("解析账本 JSON 失败: %w", err)
	}
	if ledger.Deployments == nil {
		ledger.Deployments = make(map[string]LedgerEntry)
	}
	return &ledger, nil
}

// upsertLedgerEntry 原子更新并落盘单条部署记录到 root 专属账本（权限 0600）。
// 参数 entry 为待写入或更新的账本条目；成功返回 nil，失败返回 IO 错误。
func (e *Executor) upsertLedgerEntry(entry LedgerEntry) error {
	ledger, err := e.loadLedger()
	if err != nil {
		return err
	}
	ledger.Deployments[entry.DeploymentID] = entry

	ledgerDir := filepath.Dir(e.cfg.LedgerPath)
	if err := os.MkdirAll(ledgerDir, 0700); err != nil {
		return fmt.Errorf("创建账本目录失败: %w", err)
	}
	encoded, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}

	tmpFile := fmt.Sprintf("%s.tmp.%d", e.cfg.LedgerPath, os.Getpid())
	f, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return err
	}
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	return os.Rename(tmpFile, e.cfg.LedgerPath)
}

// scanBackupDirectory 扫描数据库备份目录，计算全部导出文件的大小、SHA-256 及汇总校验和（拒绝任何符号链接与空备份）。
// 参数 backupDir 为备份目录路径，productCode/deploymentID 为归属标识，now 为完成时间；返回 *BackupResult 及错误。
func scanBackupDirectory(backupDir, productCode, deploymentID string, now time.Time) (*BackupResult, error) {
	var files []BackupFileInfo
	var totalBytes int64

	walkErr := filepath.WalkDir(backupDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == backupDir {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("数据库备份目录内发现非法符号链接: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("数据库备份目录内发现非常规文件: %s", path)
		}
		sz, sum, hashErr := manifest.HashFile(path)
		if hashErr != nil {
			return hashErr
		}
		rel, relErr := filepath.Rel(backupDir, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, BackupFileInfo{
			Name:   filepath.ToSlash(rel),
			Size:   sz,
			SHA256: sum,
		})
		totalBytes += sz
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("扫描数据库备份目录失败: %w", walkErr)
	}
	if len(files) == 0 || totalBytes <= 0 {
		return nil, errors.New("数据库备份目录为空或导出文件字节数为 0，判定备份失败")
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})
	h := sha256.New()
	for _, item := range files {
		_, _ = fmt.Fprintf(h, "%s|%d|%s\n", item.Name, item.Size, item.SHA256)
	}
	return &BackupResult{
		ProductCode:   productCode,
		DeploymentID:  deploymentID,
		BackupDir:     backupDir,
		Files:         files,
		TotalBytes:    totalBytes,
		SummarySHA256: hex.EncodeToString(h.Sum(nil)),
		CreatedAt:     now.Unix(),
	}, nil
}

// runCleanHook 在清空全部继承环境变量（仅保留最小安全 PATH）的前提下执行产品部署钩子。
// 参数 ctx 为超时上下文，hookPath 为钩子绝对路径，args 为固定契约参数；返回 stdout 缓冲、stderr 缓冲及执行错误。
func runCleanHook(ctx context.Context, hookPath string, args ...string) (*bytes.Buffer, *bytes.Buffer, error) {
	cmd := exec.CommandContext(ctx, hookPath, args...)
	cmd.Env = SafeHookEnv()
	cmd.Dir = "/"
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	return &outBuf, &errBuf, err
}

// wrapHookError 将子进程运行错误包装为携带精确退出码与错误输出末尾的 *HookExecError。
// 参数 subcmd 为钩子子命令，runErr 为 cmd.Run 错误，stderrStr/stdoutStr 为进程输出；返回 *HookExecError。
func wrapHookError(subcmd string, runErr error, stderrStr, stdoutStr string) error {
	code := 1
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) && exitErr.ExitCode() > 0 {
		code = exitErr.ExitCode()
	}
	detail := strings.TrimSpace(stderrStr)
	if detail == "" {
		detail = strings.TrimSpace(stdoutStr)
	}
	if len(detail) > 4096 {
		detail = detail[len(detail)-4096:]
	}
	return &HookExecError{
		Subcommand: subcmd,
		ExitCode:   code,
		StderrTail: detail,
		Err:        runErr,
	}
}

// copyRegularFileNoFollow 拒绝符号链接并将暂存区常规文件复制到隔离目标路径（可选限制最大字节数）。
// 参数 srcPath 为源路径，dstPath 为目标路径，maxBytes 为最大允许字节数（0 表示不限），perm 为目标文件权限；返回错误。
func copyRegularFileNoFollow(srcPath, dstPath string, maxBytes int64, perm os.FileMode) error {
	linfo, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}
	if !linfo.Mode().IsRegular() {
		return fmt.Errorf("拒绝复制符号链接或非常规文件: %s", srcPath)
	}
	if maxBytes > 0 && linfo.Size() > maxBytes {
		return fmt.Errorf("文件体积超出安全上限 (%d > %d): %s", linfo.Size(), maxBytes, srcPath)
	}

	srcFile, err := os.OpenFile(srcPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	finfo, err := srcFile.Stat()
	if err != nil {
		return err
	}
	if !finfo.Mode().IsRegular() {
		return fmt.Errorf("打开后检测到非常规文件: %s", srcPath)
	}

	dstFile, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if err := dstFile.Chmod(perm); err != nil {
		_ = dstFile.Close()
		return err
	}

	var reader io.Reader = srcFile
	if maxBytes > 0 {
		reader = io.LimitReader(srcFile, maxBytes+1)
	}
	written, copyErr := io.Copy(dstFile, reader)
	syncErr := dstFile.Sync()
	closeErr := dstFile.Close()
	if copyErr != nil {
		return copyErr
	}
	if maxBytes > 0 && written > maxBytes {
		return fmt.Errorf("复制过程中文件体积超出上限: %s", srcPath)
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// atomicInstallExecutable 将经过验签的可执行脚本或二进制以指定权限原子替换到固定路径（如 0700 部署钩子）。
// 参数 srcPath 为源文件路径，dstPath 为目标安装路径，perm 为权限位；成功返回 nil，失败返回错误。
func atomicInstallExecutable(srcPath, dstPath string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return err
	}
	tmpDst := fmt.Sprintf("%s.tmp.%d", dstPath, os.Getpid())
	if err := copyRegularFileNoFollow(srcPath, tmpDst, 0, perm); err != nil {
		_ = os.Remove(tmpDst)
		return err
	}
	if os.Geteuid() == 0 {
		_ = os.Chown(tmpDst, 0, 0)
	}
	if err := os.Chmod(tmpDst, perm); err != nil {
		_ = os.Remove(tmpDst)
		return err
	}
	if err := os.Rename(tmpDst, dstPath); err != nil {
		_ = os.Remove(tmpDst)
		return err
	}
	return nil
}

// readTrimmedKeyFile 读取公钥文件内容并去除首尾空白字符（同时拒绝符号链接）。
// 参数 path 为公钥文件绝对路径；返回非空公钥字符串及错误。
func readTrimmedKeyFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("公钥文件不能为符号链接或目录: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	val := strings.TrimSpace(string(raw))
	if val == "" {
		return "", fmt.Errorf("公钥文件内容为空: %s", path)
	}
	return val, nil
}

// ensureRegularExecutable 检查部署钩子路径是否存在、为常规文件且具备可执行位。
// 参数 path 为部署钩子文件绝对路径；合法返回 nil，否则返回错误。
func ensureRegularExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("部署钩子不是常规文件: %s", path)
	}
	if info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("部署钩子缺少可执行权限: %s", path)
	}
	return nil
}

// regularFileExists 判断目标路径是否存在常规文件（不跟随符号链接）。
// 参数 path 为待检查文件路径；是常规文件返回 true，否则返回 false。
func regularFileExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// sameSemver 比较两个版本号字符串在语义化版本意义下是否完全相等（兼容带或不带 v 前缀）。
// 参数 a、b 为待比较版本号；相等返回 true，否则返回 false。
func sameSemver(a, b string) bool {
	va, errA := semver.Parse(a)
	vb, errB := semver.Parse(b)
	if errA == nil && errB == nil {
		return va.Compare(vb) == 0
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// tailBytes 截取字节切片末尾最多 maxLen 字节。
// 参数 data 为原始字节切片，maxLen 为最大保留长度；返回截断后的字节切片。
func tailBytes(data []byte, maxLen int) []byte {
	if len(data) <= maxLen || maxLen <= 0 {
		return data
	}
	return data[len(data)-maxLen:]
}
