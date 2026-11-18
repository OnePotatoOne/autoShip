// Package daemon 中的 daemon.go 实现 autoship-agent 的主事件循环、9 步升级状态机、健康探测、故障信息脱敏及重启恢复。
package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"autoship/internal/updateagent/apply"
	"autoship/internal/updateagent/config"
	"autoship/pkg/crypto"
	"autoship/pkg/desensitize"
	"autoship/pkg/envelope"
	"autoship/pkg/manifest"
	"autoship/pkg/semver"
)

// crashLogKeywords 定义上线健康检查第 6 步扫描最近启动日志时判定发生崩溃的关键字集合（大小写不敏感）。
var crashLogKeywords = []string{
	"panic:",
	"fatal error:",
	"level=fatal",
	`"level":"fatal"`,
	"segmentation fault",
	"sigsegv",
}

// applyInvokeResult 封装通过受限 sudo 调用 autoship-apply 子命令的退出码与标准输出/错误缓冲。
type applyInvokeResult struct {
	ExitCode int    // 进程退出码（0 表示成功）
	Stdout   string // 标准输出完整内容
	Stderr   string // 标准错误输出完整内容
}

// Daemon 封装产品服务器更新 Agent 常驻进程的运行状态、通信客户端、状态存储及可注入探测钩子。
type Daemon struct {
	cfg             *config.AgentConfig               // Agent 配置
	client          *ServerClient                     // 固定 CA 指纹的 Server 通信客户端
	store           *StateStore                       // 本地持久化状态与文件锁管理器
	healthClient    *http.Client                      // 本机健康检查 HTTP 客户端
	diskStatFn      func(path string) (DiskStats, error) // 磁盘空间查询函数（默认 syscall.Statfs，测试可注入模拟空间不足）
	applyExtraEnv   []string                          // 仅在非 root 测试态传递给模拟 sudo/apply 的测试配置环境变量
	pollWaitSecHint int                               // 长轮询等待秒数提示（默认 -1 使用服务端 30s，测试可设为 0 加速返回）
	logger          *log.Logger                       // 运行日志输出器
}

// New 根据校验通过的 AgentConfig 初始化更新 Agent 常驻守护实例。
// 参数 cfg 为 *config.AgentConfig；返回 *Daemon 实例及初始化错误，通过 daemon.New(cfg) 调用。
func New(cfg *config.AgentConfig) (*Daemon, error) {
	if cfg == nil {
		return nil, errors.New("AgentConfig 不能为空")
	}
	cfg.ApplyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	srvClient, err := NewServerClient(cfg)
	if err != nil {
		return nil, err
	}
	store, err := NewStateStore(cfg)
	if err != nil {
		return nil, err
	}
	hcTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.HealthCheckInsecureTLS,
		},
	}
	return &Daemon{
		cfg:    cfg,
		client: srvClient,
		store:  store,
		healthClient: &http.Client{
			Transport: hcTransport,
			Timeout:   5 * time.Second,
		},
		diskStatFn:      statFilesystem,
		pollWaitSecHint: -1,
		logger:          log.New(os.Stderr, "[autoship-agent] ", log.LstdFlags),
	}, nil
}

// SetDiskStatFunc 注入自定义磁盘容量查询函数（用于单元与端到端测试模拟数据库备份磁盘空间不足场景）。
// 参数 fn 为接收目录路径并返回 DiskStats 的函数；无返回值。
func (d *Daemon) SetDiskStatFunc(fn func(path string) (DiskStats, error)) {
	if fn != nil {
		d.diskStatFn = fn
	}
}

// SetTestApplyConfigPath 在非 root 自动化测试环境下注入传递给模拟 sudo/autoship-apply 的测试配置路径。
// 参数 applyCfgPath 为测试用 apply.yaml 路径；无返回值（在 root 生产态下自动忽略）。
func (d *Daemon) SetTestApplyConfigPath(applyCfgPath string) {
	if os.Geteuid() != 0 && strings.TrimSpace(applyCfgPath) != "" {
		d.applyExtraEnv = []string{config.TestApplyConfigEnvKey + "=" + strings.TrimSpace(applyCfgPath)}
	}
}

// SetPollWaitSecForTest 设置长轮询查询参数 waitSec（仅用于自动化测试缩短空闲轮询挂起耗时）。
// 参数 sec 为挂起秒数（>=0）；无返回值。
func (d *Daemon) SetPollWaitSecForTest(sec int) {
	d.pollWaitSecHint = sec
}

// Run 启动 autoship-agent 常驻主循环：完成首次身份注册、执行未完结日志恢复、启动 30 秒定时心跳并循环长轮询处理指令。
// 参数 ctx 为控制优雅退出的根上下文；直至 ctx 取消返回 nil，或启动阶段首次注册失败返回错误。
func (d *Daemon) Run(ctx context.Context) error {
	id, err := d.EnsureRegistered(ctx)
	if err != nil {
		return err
	}
	d.logger.Printf("已就绪: agentId=%s targetId=%d product=%s version=%s",
		id.AgentID, id.TargetID, id.ProductCode, d.cfg.AgentVersion)

	if recErr := d.RecoverPending(ctx); recErr != nil {
		d.logger.Printf("启动恢复阶段提示: %v", recErr)
	}

	// 启动即发送一次心跳，随后按配置的 30 秒间隔持续发送
	if hbErr := d.SendHeartbeatOnce(ctx); hbErr != nil {
		d.logger.Printf("首次心跳上报失败: %v", hbErr)
	}

	hbTicker := time.NewTicker(time.Duration(d.cfg.HeartbeatIntervalSec) * time.Second)
	defer hbTicker.Stop()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hbTicker.C:
				if hbErr := d.SendHeartbeatOnce(ctx); hbErr != nil && ctx.Err() == nil {
					d.logger.Printf("定时心跳上报异常: %v", hbErr)
				}
			}
		}
	}()

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		_, pollErr := d.PollAndExecuteOnce(ctx)
		if pollErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			d.logger.Printf("长轮询拉取指令失败（%s 后重试）: %v", backoff, pollErr)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
	}
}

// EnsureRegistered 检查本地是否已有 Ed25519 身份私钥；若尚无则生成新密钥对并用一次性令牌向 Server 注册保存（9.2 & 7.3 节）。
// 参数 ctx 为请求上下文；返回加载或新注册的 *Identity 及错误。
func (d *Daemon) EnsureRegistered(ctx context.Context) (*Identity, error) {
	existing, err := d.store.LoadIdentity()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	token := strings.TrimSpace(d.cfg.BootstrapToken)
	if token == "" {
		return nil, errors.New("本地尚无身份文件且配置文件缺少一次性注册令牌 bootstrapToken")
	}
	pubB64, privB64, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		return nil, err
	}

	regResp, err := d.client.Register(ctx, RegisterRequest{
		Token:        token,
		PublicKeyB64: pubB64,
		AgentVersion: d.cfg.AgentVersion,
	})
	if err != nil {
		return nil, err
	}

	targetID := regResp.TargetID
	if d.cfg.TargetID > 0 {
		if targetID > 0 && targetID != d.cfg.TargetID {
			return nil, fmt.Errorf("注册返回的 targetId (%d) 与本地配置 (%d) 不一致", targetID, d.cfg.TargetID)
		}
		targetID = d.cfg.TargetID
	}
	productCode := strings.TrimSpace(regResp.ProductCode)
	if d.cfg.ProductCode != "" {
		if productCode != "" && productCode != d.cfg.ProductCode {
			return nil, fmt.Errorf("注册返回的 productCode (%s) 与本地配置 (%s) 不一致", productCode, d.cfg.ProductCode)
		}
		productCode = d.cfg.ProductCode
	}

	id := &Identity{
		AgentID:          regResp.AgentID.String(),
		TargetID:         targetID,
		ProductCode:      productCode,
		PublicKeyB64:     pubB64,
		PrivateKeyB64:    privB64,
		ServerSignPubB64: strings.TrimSpace(regResp.ServerSignPubB64),
		RegisteredAt:     time.Now().Unix(),
	}
	if err := d.store.SaveIdentity(id); err != nil {
		return nil, fmt.Errorf("保存 Agent 身份文件失败: %w", err)
	}
	return id, nil
}

// RecoverPending 在进程重启后扫描本地 journal.json，对尚未确认送达的终态报告自动补报，并对中途中断的部署探测实况收敛上报。
// 参数 ctx 为请求上下文；成功返回 nil，部分补报暂未连通时返回提示错误（下次启动或重投递继续收敛）。
func (d *Daemon) RecoverPending(ctx context.Context) error {
	id, err := d.EnsureRegistered(ctx)
	if err != nil {
		return err
	}
	journal, err := d.store.LoadJournal()
	if err != nil {
		return err
	}

	var firstErr error
	for depID, rec := range journal.Deployments {
		// 1. 已进入终态但上次向 Server POST /report 时网络中断未确认 -> 立即补报
		if rec.Terminal && !rec.ReportAcked && rec.LastReport != nil {
			if repErr := d.client.ReportDeployment(ctx, id, depID, *rec.LastReport); repErr != nil {
				if firstErr == nil {
					firstErr = repErr
				}
				continue
			}
			_ = d.store.UpdateJournal(func(j *Journal) {
				if cur, ok := j.Deployments[depID]; ok {
					cur.ReportAcked = true
					cur.UpdatedAt = time.Now().Unix()
					j.Deployments[depID] = cur
				}
			})
			continue
		}

		// 2. 进程在升级非终态时意外重启 -> 探测当前健康检查与版本实况，必要时自动回滚并如实上报终态
		if !rec.Terminal {
			d.recoverInterruptedRecord(ctx, id, rec)
		}
	}
	return firstErr
}

// recoverInterruptedRecord 对进程意外崩溃遗留的未完结部署记录执行实况核验或自动回滚收敛。
// 参数 ctx 为上下文，id 为本地身份，rec 为未完结的持久化日志记录；无返回值。
func (d *Daemon) recoverInterruptedRecord(ctx context.Context, id *Identity, rec JournalRecord) {
	hcResp, rawBody, hcErr := d.probeHealthOnce(ctx)
	if rec.CommandType == CmdDeployUpgrade || rec.CommandType == CmdDeployExecute {
		if hcErr == nil && isServiceHealthy(hcResp) && hcResp.MigrationDone && sameVersion(hcResp.Version, rec.TargetVersion) {
			_, _ = d.invokeApply(ctx, apply.ActionCleanup)
			rep := DeployReport{
				CommandID:    rec.MessageID,
				DeploymentID: rec.DeploymentID,
				ReleaseID:    rec.ReleaseID,
				TargetID:     id.TargetID,
				ProductCode:  id.ProductCode,
				Version:      rec.TargetVersion,
				Status:       StatusSucceeded,
				Step:         "complete",
				StepStatus:   "succeeded",
				Message:      "Agent 重启后核验目标版本已健康上线",
				Timestamp:    time.Now().Unix(),
			}
			d.persistAndReport(ctx, id, rec, rep, true)
			return
		}

		// 若已进入安装或健康检查步骤但当前未运行在健康目标版本，且保存了回滚票据则执行自动回滚
		if (rec.CurrentStep == "upgrade" || rec.CurrentStep == "health_check") && rec.RollbackTkt != nil {
			d.rollbackAndReportFailure(ctx, id, rec, rec.CurrentStep, 1, "Agent 在升级期间重启，自动触发回滚恢复", rawBody, *rec.RollbackTkt)
			return
		}
	}

	// 尚未触碰线上服务（如预检/下载/备份阶段中断），标记为安全中止（rolled_back）上报
	rep := DeployReport{
		CommandID:      rec.MessageID,
		DeploymentID:   rec.DeploymentID,
		ReleaseID:      rec.ReleaseID,
		TargetID:       id.TargetID,
		ProductCode:    id.ProductCode,
		Version:        rec.TargetVersion,
		Status:         StatusRolledBack,
		Step:           rec.CurrentStep,
		StepStatus:     "failed",
		FailedStep:     rec.CurrentStep,
		Message:        "Agent 在未停服前置阶段重启，本次操作安全中止",
		HealthResponse: maskAndTail(rawBody, 2048),
		Timestamp:      time.Now().Unix(),
	}
	d.persistAndReport(ctx, id, rec, rep, true)
}

// SendHeartbeatOnce 采集产品版本、服务状态、磁盘剩余与数据库备份目录占用，并发送一次签名心跳（9.2 节）。
// 参数 ctx 为请求上下文；成功返回 nil，失败返回网络或签名错误。
func (d *Daemon) SendHeartbeatOnce(ctx context.Context) error {
	id, err := d.EnsureRegistered(ctx)
	if err != nil {
		return err
	}

	journal, _ := d.store.LoadJournal()
	prodVer := ""
	if journal != nil {
		prodVer = journal.LastKnownVersion
	}

	svcStatus := "down"
	if hc, _, hcErr := d.probeHealthOnce(ctx); hcErr == nil {
		if strings.TrimSpace(hc.Version) != "" {
			prodVer = strings.TrimSpace(hc.Version)
			_ = d.store.UpdateJournal(func(j *Journal) {
				j.LastKnownVersion = prodVer
			})
		}
		switch {
		case isServiceHealthy(hc) && hc.MigrationDone:
			svcStatus = "running"
		case !hc.MigrationDone || strings.EqualFold(hc.Status, "migrating"):
			svcStatus = "migrating"
		default:
			svcStatus = "degraded"
		}
	}

	disk, _ := d.diskStatFn(d.cfg.StagingDir)
	backupUsed := d.store.ComputeBackupUsedBytes(id.ProductCode)

	hb := HeartbeatRequest{
		AgentVersion:    d.cfg.AgentVersion,
		ProductVersion:  prodVer,
		ServiceStatus:   svcStatus,
		DiskFreeBytes:   clampUint64ToInt64(disk.FreeBytes),
		DiskTotalBytes:  clampUint64ToInt64(disk.TotalBytes),
		BackupUsedBytes: backupUsed,
	}
	return d.client.SendHeartbeat(ctx, id, hb)
}

// PollAndExecuteOnce 执行一次带 ack 序号的长轮询拉取，并按序处理返回的全部指令。
// 参数 ctx 为请求上下文；返回本次处理的指令条数及轮询错误。
func (d *Daemon) PollAndExecuteOnce(ctx context.Context) (int, error) {
	id, err := d.EnsureRegistered(ctx)
	if err != nil {
		return 0, err
	}
	journal, err := d.store.LoadJournal()
	if err != nil {
		return 0, err
	}

	cmds, err := d.client.PollCommands(ctx, id, journal.LastAckSeq, d.pollWaitSecHint)
	if err != nil {
		return 0, err
	}
	for _, msg := range cmds {
		if err := d.HandleCommand(ctx, msg); err != nil {
			d.logger.Printf("处理指令 id=%s type=%s 遇到错误: %v", msg.ID, msg.Type, err)
		}
		if msg.Seq > 0 {
			_ = d.store.UpdateJournal(func(j *Journal) {
				if msg.Seq > j.LastAckSeq {
					j.LastAckSeq = msg.Seq
				}
			})
		}
	}
	return len(cmds), nil
}

// HandleCommand 按统一信封消息 ID 与 deployment_id 做幂等检查，并分发执行升级、回滚或备份固定流程（9.3 节）。
// 参数 ctx 为上下文，msg 为 Server 下发的统一消息信封；返回本地流程错误（业务失败已通过 ReportDeployment 回传）。
func (d *Daemon) HandleCommand(ctx context.Context, msg envelope.Message) error {
	id, err := d.EnsureRegistered(ctx)
	if err != nil {
		return err
	}

	// 检查 messageId 级幂等（应对至少一次投递重复下发）
	journal, err := d.store.LoadJournal()
	if err != nil {
		return err
	}
	if prevDepID, seen := journal.ProcessedMessages[msg.ID]; seen {
		if prevRec, ok := journal.Deployments[prevDepID]; ok && prevRec.Terminal && prevRec.LastReport != nil && !prevRec.ReportAcked {
			if repErr := d.client.ReportDeployment(ctx, id, prevDepID, *prevRec.LastReport); repErr == nil {
				_ = d.store.UpdateJournal(func(j *Journal) {
					r := j.Deployments[prevDepID]
					r.ReportAcked = true
					j.Deployments[prevDepID] = r
				})
			}
		}
		return nil
	}

	switch msg.Type {
	case CmdDeployUpgrade, CmdDeployExecute:
		var p UpgradeCommandPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("解析升级指令载荷失败: %w", err)
		}
		return d.executeUpgrade(ctx, id, msg, p)

	case CmdDeployRollback:
		var p RollbackCommandPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("解析回滚指令载荷失败: %w", err)
		}
		return d.executeRollback(ctx, id, msg, p)

	case CmdDeployBackup:
		var p BackupCommandPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			return fmt.Errorf("解析备份指令载荷失败: %w", err)
		}
		return d.executeBackup(ctx, id, msg, p)

	default:
		return fmt.Errorf("拒绝未知指令类型（只执行内置固定流程）: %s", msg.Type)
	}
}

// executeUpgrade 严格按设计文档 9.4 节执行完整 9 步升级流程（含防降级、备份空间预检阻断、断点续传、按需备份、升级、健康探测、失败自动回滚、清理与分步上报）。
// 参数 ctx 为上下文，id 为本地 Agent 身份，msg 为原始信封，p 为升级指令载荷；返回流程控制错误。
func (d *Daemon) executeUpgrade(ctx context.Context, id *Identity, msg envelope.Message, p UpgradeCommandPayload) error {
	deploymentID := firstNonEmpty(p.DeploymentID, p.Ticket.DeploymentID)
	productCode := firstNonEmpty(p.ProductCode, p.Ticket.ProductCode)
	targetVersion := firstNonEmpty(p.Version, p.Ticket.Version)
	releaseID := p.ReleaseID
	if releaseID == 0 {
		releaseID = p.Ticket.ReleaseID
	}
	targetID := p.TargetID
	if targetID == 0 {
		targetID = p.Ticket.TargetID
	}
	if p.Manifest != nil && targetVersion == "" {
		targetVersion = p.Manifest.Version
	}

	if err := envelope.ValidateSafeID(deploymentID); err != nil {
		return fmt.Errorf("升级指令 deploymentId 非法: %w", err)
	}

	// 按 deployment_id 幂等：若该 deployment_id 已成功上线，不重复停服安装，直接补报并返回
	if j, err := d.store.LoadJournal(); err == nil {
		if prev, exists := j.Deployments[deploymentID]; exists && prev.Terminal && prev.Status == StatusSucceeded &&
			(prev.CommandType == CmdDeployUpgrade || prev.CommandType == CmdDeployExecute) {
			if prev.LastReport != nil {
				_ = d.client.ReportDeployment(ctx, id, deploymentID, *prev.LastReport)
			}
			_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
			return nil
		}
	}

	rbTicket := p.Ticket
	if p.RollbackTicket != nil {
		rbTicket = *p.RollbackTicket
	}
	rec := JournalRecord{
		MessageID:     msg.ID,
		MessageSeq:    msg.Seq,
		CommandType:   msg.Type,
		DeploymentID:  deploymentID,
		ReleaseID:     releaseID,
		TargetVersion: targetVersion,
		CurrentStep:   "verify_manifest",
		Status:        StatusDeploying,
		RollbackTkt:   &rbTicket,
		UpdatedAt:     time.Now().Unix(),
	}

	// 获取防并发部署文件锁（Step 2 要求无其他部署在跑；提前加锁防止篡改正在执行的暂存目录）
	unlock, lockErr := d.store.AcquireDeployLock()
	if lockErr != nil {
		d.abortBeforeInstall(ctx, id, rec, "precheck", lockErr.Error(), "")
		return nil
	}
	defer unlock()

	// 探测当前运行服务状态与旧版本号（供 Step 1 防降级、Step 2 服务存活检查与 Step 7 回滚核验复用）
	curHealth, curHealthRaw, curHealthErr := d.probeHealthOnce(ctx)
	currentVersion := ""
	if curHealthErr == nil && strings.TrimSpace(curHealth.Version) != "" {
		currentVersion = strings.TrimSpace(curHealth.Version)
	} else if j, jErr := d.store.LoadJournal(); jErr == nil {
		currentVersion = j.LastKnownVersion
	}
	rec.PrevVersion = currentVersion

	// ==================== 第 1 步：清单验签、产品与目标匹配、semver.IsUpgrade 防降级检查 ====================
	d.reportStep(ctx, id, &rec, StatusDeploying, "verify_manifest", "running", "正在校验签名清单、部署目标与防降级版本规则")

	releasePubB64, keyErr := readKeyFileTrimmed(d.cfg.ReleasePubPath)
	if keyErr != nil {
		d.abortBeforeInstall(ctx, id, rec, "verify_manifest", fmt.Sprintf("读取发布验签公钥失败: %v", keyErr), curHealthRaw)
		return nil
	}
	if serverPubB64, sErr := readKeyFileTrimmed(d.cfg.ServerPubPath); sErr == nil && serverPubB64 != "" {
		if vErr := p.Ticket.Verify(serverPubB64, apply.ActionUpgrade, time.Now()); vErr != nil {
			d.abortBeforeInstall(ctx, id, rec, "verify_manifest", fmt.Sprintf("升级动作票据验签失败: %v", vErr), curHealthRaw)
			return nil
		}
	}
	if id.ProductCode != "" && productCode != id.ProductCode {
		d.abortBeforeInstall(ctx, id, rec, "verify_manifest",
			fmt.Sprintf("产品编码不匹配: 指令为 %s，当前 Agent 绑定为 %s", productCode, id.ProductCode), curHealthRaw)
		return nil
	}
	if id.TargetID > 0 && targetID != id.TargetID {
		d.abortBeforeInstall(ctx, id, rec, "verify_manifest",
			fmt.Sprintf("部署目标 ID 不匹配: 指令为 %d，当前 Agent 绑定为 %d", targetID, id.TargetID), curHealthRaw)
		return nil
	}

	if p.Manifest != nil {
		if mErr := d.validateManifestRules(p.Manifest, releasePubB64, id.ProductCode, targetVersion); mErr != nil {
			d.abortBeforeInstall(ctx, id, rec, "verify_manifest", mErr.Error(), curHealthRaw)
			return nil
		}
		targetVersion = p.Manifest.Version
		rec.TargetVersion = targetVersion
	}

	isUp, upErr := semver.IsUpgrade(currentVersion, targetVersion)
	if upErr != nil || !isUp {
		reason := fmt.Sprintf("防降级拦截：候选版本 %s 不高于当前运行版本 %s（降级仅允许走回滚流程）", targetVersion, currentVersion)
		if upErr != nil {
			reason = fmt.Sprintf("版本号解析失败: %v", upErr)
		}
		d.abortBeforeInstall(ctx, id, rec, "verify_manifest", reason, curHealthRaw)
		return nil
	}

	// ==================== 第 2 步：预检（服务运行状态、制品与数据库备份磁盘空间） ====================
	d.reportStep(ctx, id, &rec, StatusDeploying, "precheck", "running", "正在检查当前服务健康状态与磁盘可用空间")

	if curHealthErr != nil || !isServiceHealthy(curHealth) {
		if !(d.cfg.AllowFirstInstallWhenDown && currentVersion == "") {
			d.abortBeforeInstall(ctx, id, rec, "precheck",
				fmt.Sprintf("部署前预检未通过：当前产品服务未处于健康运行状态 (%v)", curHealthErr), curHealthRaw)
			return nil
		}
	}

	prelimNeedBackup := p.BackupDB || p.Ticket.BackupDB || (p.Manifest != nil && p.Manifest.MigrationRisk == manifest.RiskDataRewrite)
	if spaceErr := d.checkDiskSpaceForDeploy(p.BundleSize, prelimNeedBackup); spaceErr != nil {
		d.abortBeforeInstall(ctx, id, rec, "precheck", spaceErr.Error(), curHealthRaw)
		return nil
	}

	// ==================== 第 3 步：HTTP Range 断点续传下载制品包并核对大小、SHA-256 与清单签名 ====================
	d.reportStep(ctx, id, &rec, StatusDeploying, "download", "running", "正在通过 HTTPS Range 断点续传下载制品包")

	stagingBundlePath := filepath.Join(d.cfg.StagingDir, apply.StagingBundleFileName)
	dlSize, _, dlErr := d.client.DownloadArtifactWithResume(ctx, id, releaseID, stagingBundlePath, p.BundleSize, p.BundleSHA256)
	if dlErr != nil {
		d.abortBeforeInstall(ctx, id, rec, "download", fmt.Sprintf("制品包下载或摘要校验失败: %v", dlErr), "")
		return nil
	}

	// 常驻进程执行首次制品包验签解包校验（9.1 节：首次验签）
	verifyTmpDir, tmpErr := os.MkdirTemp(d.cfg.StateDir, "verify-bundle-*")
	if tmpErr != nil {
		d.abortBeforeInstall(ctx, id, rec, "download", fmt.Sprintf("创建首次验签目录失败: %v", tmpErr), "")
		return nil
	}
	bundleManifest, _, _, unpackErr := manifest.UnpackAndVerifyBundle(stagingBundlePath, verifyTmpDir, releasePubB64)
	_ = os.RemoveAll(verifyTmpDir)
	if unpackErr != nil {
		_ = os.Remove(stagingBundlePath)
		d.abortBeforeInstall(ctx, id, rec, "download", fmt.Sprintf("制品包首次签名校验不通过: %v", unpackErr), "")
		return nil
	}
	if mErr := d.validateManifestRules(bundleManifest, releasePubB64, id.ProductCode, targetVersion); mErr != nil {
		_ = os.Remove(stagingBundlePath)
		d.abortBeforeInstall(ctx, id, rec, "download", mErr.Error(), "")
		return nil
	}
	if okUp, upErr2 := semver.IsUpgrade(currentVersion, bundleManifest.Version); upErr2 != nil || !okUp {
		_ = os.Remove(stagingBundlePath)
		d.abortBeforeInstall(ctx, id, rec, "download",
			fmt.Sprintf("制品包内清单版本 %s 防降级校验不通过（当前版本 %s）", bundleManifest.Version, currentVersion), "")
		return nil
	}

	// 若指令未内联清单，而包内清单声明了 data_rewrite，在执行备份前再次核验数据库备份磁盘剩余空间（9.5 节）
	needBackup := prelimNeedBackup || bundleManifest.MigrationRisk == manifest.RiskDataRewrite
	if spaceErr := d.checkDiskSpaceForDeploy(dlSize, needBackup); spaceErr != nil {
		_ = os.Remove(stagingBundlePath)
		d.abortBeforeInstall(ctx, id, rec, "precheck", spaceErr.Error(), "")
		return nil
	}

	// ==================== 第 4 步：按需执行数据库备份（失败立即中止且不停服） ====================
	var backupResult *apply.BackupResult
	var backupOverQuota bool
	if needBackup {
		d.reportStep(ctx, id, &rec, StatusBackingUp, "backup", "running", "正在调用部署钩子导出备份数据库")

		backupTkt := p.Ticket
		if p.BackupTicket != nil {
			backupTkt = *p.BackupTicket
		}
		if err := d.writeStagingTicket(backupTkt); err != nil {
			d.abortBeforeInstall(ctx, id, rec, "backup", fmt.Sprintf("写入备份动作票据失败: %v", err), "")
			return nil
		}
		bInvoke, bErr := d.invokeApply(ctx, apply.ActionBackup)
		if bErr != nil {
			d.abortBeforeInstallWithExec(ctx, id, rec, "backup", "数据库备份失败，已中止升级且未停服", bInvoke.ExitCode, bInvoke.Stderr)
			return nil
		}
		var parsedBackup apply.BackupResult
		if uErr := json.Unmarshal([]byte(strings.TrimSpace(bInvoke.Stdout)), &parsedBackup); uErr != nil {
			d.abortBeforeInstallWithExec(ctx, id, rec, "backup",
				fmt.Sprintf("解析数据库备份汇总 JSON 失败: %v", uErr), 1, bInvoke.Stderr)
			return nil
		}
		backupResult = &parsedBackup
		_ = d.store.RecordBackupSize(parsedBackup.DeploymentID, parsedBackup.TotalBytes)

		usedAfterBackup := d.store.ComputeBackupUsedBytes(id.ProductCode)
		if d.cfg.BackupQuotaBytes > 0 && usedAfterBackup > 0 && uint64(usedAfterBackup) > d.cfg.BackupQuotaBytes {
			backupOverQuota = true
		}

		bSuccessReport := DeployReport{
			CommandID:           msg.ID,
			DeploymentID:        deploymentID,
			ReleaseID:           releaseID,
			TargetID:            id.TargetID,
			ProductCode:         id.ProductCode,
			Version:             bundleManifest.Version,
			Status:              StatusBackingUp,
			Step:                "backup",
			StepStatus:          "succeeded",
			Message:             fmt.Sprintf("数据库备份完成（共 %d 个文件，%d 字节）", len(parsedBackup.Files), parsedBackup.TotalBytes),
			Backup:              backupResult,
			BackupDir:           parsedBackup.BackupDir,
			BackupFiles:         parsedBackup.Files,
			BackupTotalBytes:    parsedBackup.TotalBytes,
			BackupSummarySHA256: parsedBackup.SummarySHA256,
			BackupOverQuota:     backupOverQuota,
			Timestamp:           time.Now().Unix(),
		}
		d.persistAndReport(ctx, id, rec, bSuccessReport, false)
	}

	// ==================== 第 5 步：调用 sudo autoship-apply upgrade ====================
	d.reportStep(ctx, id, &rec, StatusDeploying, "upgrade", "running", "正在调用 autoship-apply 二次验签并替换升级服务")

	if err := d.writeStagingTicket(p.Ticket); err != nil {
		d.abortBeforeInstall(ctx, id, rec, "upgrade", fmt.Sprintf("写入升级动作票据失败: %v", err), "")
		return nil
	}
	upInvoke, upExecErr := d.invokeApply(ctx, apply.ActionUpgrade)
	if upExecErr != nil {
		// 即使 install 阶段报错，也执行第 7 步回滚与旧版本健康确认，确保目标服务器状态确定性收敛
		d.rollbackAndReportFailure(ctx, id, rec, "upgrade", upInvoke.ExitCode, upInvoke.Stderr, "", rbTicket)
		_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
		return nil
	}

	// ==================== 第 6 步：上线健康检查与最近启动日志无崩溃核验 ====================
	d.reportStep(ctx, id, &rec, StatusDeploying, "health_check", "running", "正在轮询上线健康检查与模块只读探测")

	waitSec := d.cfg.HealthCheckTimeoutSec
	if bundleManifest.MigrationRisk != manifest.RiskNone {
		waitSec = d.cfg.MigrationTimeoutSec
	}
	lastHealthRaw, hcWaitErr := d.waitForTargetHealthy(ctx, bundleManifest.Version, time.Duration(waitSec)*time.Second)
	if hcWaitErr != nil {
		// ==================== 第 7 步：上线检查失败 -> 自动回滚并确认旧版本健康 ====================
		d.rollbackAndReportFailure(ctx, id, rec, "health_check", 0, hcWaitErr.Error(), lastHealthRaw, rbTicket)
		_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
		return nil
	}

	// 检查最近 200 行启动日志是否存在崩溃关键字（panic / fatal / sigsegv）
	logsInvoke, logsErr := d.invokeApply(ctx, apply.ActionLogs)
	if logsErr == nil {
		if crashMarker := findCrashInLogs(logsInvoke.Stdout); crashMarker != "" {
			d.rollbackAndReportFailure(ctx, id, rec, "health_check", 0,
				fmt.Sprintf("服务启动日志检测到崩溃特征 (%s)", crashMarker), lastHealthRaw, rbTicket)
			_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
			return nil
		}
	}

	// ==================== 第 8 步：升级成功后清理旧程序备份与暂存区 ====================
	d.reportStep(ctx, id, &rec, StatusDeploying, "cleanup", "running", "正在清理过期程序备份")
	_, _ = d.invokeApply(ctx, apply.ActionCleanup)
	d.cleanStagingFiles()

	_ = d.store.UpdateJournal(func(j *Journal) {
		j.LastKnownVersion = bundleManifest.Version
	})

	// ==================== 第 9 步：上报最终部署成功状态 ====================
	finalReport := DeployReport{
		CommandID:       msg.ID,
		DeploymentID:    deploymentID,
		ReleaseID:       releaseID,
		TargetID:        id.TargetID,
		ProductCode:     id.ProductCode,
		Version:         bundleManifest.Version,
		Status:          StatusSucceeded,
		Step:            "complete",
		StepStatus:      "succeeded",
		Message:         fmt.Sprintf("版本 %s 升级成功并通过全部健康探测", bundleManifest.Version),
		Backup:          backupResult,
		BackupOverQuota: backupOverQuota,
		Timestamp:       time.Now().Unix(),
	}
	if backupResult != nil {
		finalReport.BackupDir = backupResult.BackupDir
		finalReport.BackupFiles = backupResult.Files
		finalReport.BackupTotalBytes = backupResult.TotalBytes
		finalReport.BackupSummarySHA256 = backupResult.SummarySHA256
	}
	d.persistAndReport(ctx, id, rec, finalReport, true)
	_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
	return nil
}

// executeRollback 处理 Server 下发的独立手动回滚指令（按部署 ID 幂等回滚并校验健康状态）。
// 参数 ctx 为上下文，id 为本地身份，msg 为信封消息，p 为回滚指令载荷；返回流程错误。
func (d *Daemon) executeRollback(ctx context.Context, id *Identity, msg envelope.Message, p RollbackCommandPayload) error {
	deploymentID := firstNonEmpty(p.DeploymentID, p.Ticket.DeploymentID)
	if err := envelope.ValidateSafeID(deploymentID); err != nil {
		return fmt.Errorf("回滚指令 deploymentId 非法: %w", err)
	}

	rec := JournalRecord{
		MessageID:     msg.ID,
		MessageSeq:    msg.Seq,
		CommandType:   msg.Type,
		DeploymentID:  deploymentID,
		TargetVersion: p.ExpectedVersion,
		CurrentStep:   "rollback",
		Status:        StatusDeploying,
		UpdatedAt:     time.Now().Unix(),
	}

	unlock, lockErr := d.store.AcquireDeployLock()
	if lockErr != nil {
		rep := DeployReport{
			CommandID:    msg.ID,
			DeploymentID: deploymentID,
			TargetID:     id.TargetID,
			ProductCode:  id.ProductCode,
			Status:       StatusRollbackFailed,
			Step:         "rollback",
			StepStatus:   "failed",
			FailedStep:   "precheck",
			Message:      lockErr.Error(),
			Timestamp:    time.Now().Unix(),
		}
		d.persistAndReport(ctx, id, rec, rep, true)
		return nil
	}
	defer unlock()

	if serverPubB64, sErr := readKeyFileTrimmed(d.cfg.ServerPubPath); sErr == nil && serverPubB64 != "" {
		if vErr := p.Ticket.Verify(serverPubB64, apply.ActionRollback, time.Now()); vErr != nil {
			rep := DeployReport{
				CommandID:    msg.ID,
				DeploymentID: deploymentID,
				TargetID:     id.TargetID,
				ProductCode:  id.ProductCode,
				Status:       StatusRollbackFailed,
				Step:         "rollback",
				StepStatus:   "failed",
				FailedStep:   "verify_ticket",
				Message:      fmt.Sprintf("回滚票据验签失败: %v", vErr),
				Timestamp:    time.Now().Unix(),
			}
			d.persistAndReport(ctx, id, rec, rep, true)
			return nil
		}
	}

	d.reportStep(ctx, id, &rec, StatusDeploying, "rollback", "running", "正在按部署 ID 执行幂等回滚")
	if err := d.writeStagingTicket(p.Ticket); err != nil {
		return err
	}
	defer d.cleanStagingFiles()

	rbInvoke, rbErr := d.invokeApply(ctx, apply.ActionRollback)
	if rbErr != nil {
		logsInvoke, _ := d.invokeApply(ctx, apply.ActionLogs)
		rep := DeployReport{
			CommandID:    msg.ID,
			DeploymentID: deploymentID,
			TargetID:     id.TargetID,
			ProductCode:  id.ProductCode,
			Status:       StatusRollbackFailed,
			Step:         "rollback",
			StepStatus:   "failed",
			FailedStep:   "rollback",
			Message:      "执行 autoship-apply rollback 失败",
			ExitCode:     rbInvoke.ExitCode,
			StderrTail:   maskAndTail(rbInvoke.Stderr, 4096),
			LogTail:      maskAndTail(logsInvoke.Stdout, 4096),
			Timestamp:    time.Now().Unix(),
		}
		d.persistAndReport(ctx, id, rec, rep, true)
		_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
		return nil
	}

	rawHC, hcErr := d.waitForTargetHealthy(ctx, p.ExpectedVersion, time.Duration(d.cfg.HealthCheckTimeoutSec)*time.Second)
	if hcErr != nil {
		logsInvoke, _ := d.invokeApply(ctx, apply.ActionLogs)
		rep := DeployReport{
			CommandID:      msg.ID,
			DeploymentID:   deploymentID,
			TargetID:       id.TargetID,
			ProductCode:    id.ProductCode,
			Status:         StatusRollbackFailed,
			Step:           "rollback",
			StepStatus:     "failed",
			FailedStep:     "health_check",
			Message:        fmt.Sprintf("回滚后旧版本健康检查未通过: %v", hcErr),
			LogTail:        maskAndTail(logsInvoke.Stdout, 4096),
			HealthResponse: maskAndTail(rawHC, 4096),
			Timestamp:      time.Now().Unix(),
		}
		d.persistAndReport(ctx, id, rec, rep, true)
		_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
		return nil
	}

	restoredVer := p.ExpectedVersion
	if hc, _, err := d.probeHealthOnce(ctx); err == nil && hc.Version != "" {
		restoredVer = hc.Version
		_ = d.store.UpdateJournal(func(j *Journal) {
			j.LastKnownVersion = restoredVer
		})
	}

	rep := DeployReport{
		CommandID:    msg.ID,
		DeploymentID: deploymentID,
		TargetID:     id.TargetID,
		ProductCode:  id.ProductCode,
		Version:      restoredVer,
		Status:       StatusRolledBack,
		Step:         "rollback",
		StepStatus:   "succeeded",
		Message:      fmt.Sprintf("已按部署 ID (%s) 幂等回滚至版本 %s 并验证健康", p.Ticket.DeploymentID, restoredVer),
		Timestamp:    time.Now().Unix(),
	}
	d.persistAndReport(ctx, id, rec, rep, true)
	_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
	return nil
}

// executeBackup 处理 Server 下发的独立手动数据库备份指令（含空间检查、0700 导出及汇总上报）。
// 参数 ctx 为上下文，id 为本地身份，msg 为信封消息，p 为备份指令载荷；返回流程错误。
func (d *Daemon) executeBackup(ctx context.Context, id *Identity, msg envelope.Message, p BackupCommandPayload) error {
	deploymentID := firstNonEmpty(p.DeploymentID, p.Ticket.DeploymentID)
	if err := envelope.ValidateSafeID(deploymentID); err != nil {
		return fmt.Errorf("备份指令 deploymentId 非法: %w", err)
	}

	rec := JournalRecord{
		MessageID:    msg.ID,
		MessageSeq:   msg.Seq,
		CommandType:  msg.Type,
		DeploymentID: deploymentID,
		CurrentStep:  "backup",
		Status:       StatusBackingUp,
		UpdatedAt:    time.Now().Unix(),
	}

	unlock, lockErr := d.store.AcquireDeployLock()
	if lockErr != nil {
		d.abortBeforeInstall(ctx, id, rec, "precheck", lockErr.Error(), "")
		return nil
	}
	defer unlock()

	if spaceErr := d.checkDiskSpaceForDeploy(0, true); spaceErr != nil {
		d.abortBeforeInstall(ctx, id, rec, "precheck", spaceErr.Error(), "")
		_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
		return nil
	}

	d.reportStep(ctx, id, &rec, StatusBackingUp, "backup", "running", "正在执行手动数据库备份")
	if err := d.writeStagingTicket(p.Ticket); err != nil {
		return err
	}
	defer d.cleanStagingFiles()

	bInvoke, bErr := d.invokeApply(ctx, apply.ActionBackup)
	if bErr != nil {
		d.abortBeforeInstallWithExec(ctx, id, rec, "backup", "执行数据库备份失败", bInvoke.ExitCode, bInvoke.Stderr)
		_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
		return nil
	}

	var parsed apply.BackupResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(bInvoke.Stdout)), &parsed); err != nil {
		d.abortBeforeInstallWithExec(ctx, id, rec, "backup", fmt.Sprintf("解析备份汇总 JSON 失败: %v", err), 1, bInvoke.Stderr)
		_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
		return nil
	}
	_ = d.store.RecordBackupSize(parsed.DeploymentID, parsed.TotalBytes)

	used := d.store.ComputeBackupUsedBytes(id.ProductCode)
	overQuota := d.cfg.BackupQuotaBytes > 0 && used > 0 && uint64(used) > d.cfg.BackupQuotaBytes

	rep := DeployReport{
		CommandID:           msg.ID,
		DeploymentID:        deploymentID,
		TargetID:            id.TargetID,
		ProductCode:         id.ProductCode,
		Status:              StatusSucceeded,
		Step:                "backup",
		StepStatus:          "succeeded",
		Message:             "独立数据库备份完成",
		Backup:              &parsed,
		BackupDir:           parsed.BackupDir,
		BackupFiles:         parsed.Files,
		BackupTotalBytes:    parsed.TotalBytes,
		BackupSummarySHA256: parsed.SummarySHA256,
		BackupOverQuota:     overQuota,
		Timestamp:           time.Now().Unix(),
	}
	d.persistAndReport(ctx, id, rec, rep, true)
	_ = d.markMessageProcessed(msg.ID, msg.Seq, deploymentID)
	return nil
}

// rollbackAndReportFailure 在第 5 步安装报错或第 6 步上线检查失败时，采集故障现场日志后自动执行第 7 步回滚，并验证旧版本恢复健康后上报（9.4 第 7 & 9 步）。
// 参数 ctx 为上下文，id 为身份，rec 为日志记录，failedStep 为失败步骤，exitCode 为失败退出码，stderrInfo 为错误详情，healthRaw 为健康检查原始响应，rbTicket 为回滚票据；无返回值。
func (d *Daemon) rollbackAndReportFailure(
	ctx context.Context,
	id *Identity,
	rec JournalRecord,
	failedStep string,
	exitCode int,
	stderrInfo string,
	healthRaw string,
	rbTicket envelope.ActionTicket,
) {
	// 在触发回滚重启旧版之前先读取最近 200 行服务日志，确保捕获候选版本的真实崩溃堆栈
	faultLogs, _ := d.invokeApply(ctx, apply.ActionLogs)
	d.cleanStagingFiles()

	if err := d.writeStagingTicket(rbTicket); err != nil {
		rep := DeployReport{
			CommandID:      rec.MessageID,
			DeploymentID:   rec.DeploymentID,
			ReleaseID:      rec.ReleaseID,
			TargetID:       id.TargetID,
			ProductCode:    id.ProductCode,
			Version:        rec.TargetVersion,
			Status:         StatusRollbackFailed,
			Step:           "rollback",
			StepStatus:     "failed",
			FailedStep:     failedStep,
			Message:        fmt.Sprintf("写入自动回滚票据失败: %v", err),
			ExitCode:       exitCode,
			StderrTail:     maskAndTail(stderrInfo, 4096),
			LogTail:        maskAndTail(faultLogs.Stdout, 4096),
			HealthResponse: maskAndTail(healthRaw, 4096),
			Timestamp:      time.Now().Unix(),
		}
		d.persistAndReport(ctx, id, rec, rep, true)
		return
	}
	defer d.cleanStagingFiles()

	rbInvoke, rbErr := d.invokeApply(ctx, apply.ActionRollback)
	if rbErr != nil {
		combinedStderr := strings.TrimSpace(stderrInfo) + "\n[rollback stderr]: " + strings.TrimSpace(rbInvoke.Stderr)
		code := rbInvoke.ExitCode
		if code == 0 {
			code = exitCode
		}
		rep := DeployReport{
			CommandID:      rec.MessageID,
			DeploymentID:   rec.DeploymentID,
			ReleaseID:      rec.ReleaseID,
			TargetID:       id.TargetID,
			ProductCode:    id.ProductCode,
			Version:        rec.TargetVersion,
			Status:         StatusRollbackFailed,
			Step:           "rollback",
			StepStatus:     "failed",
			FailedStep:     failedStep,
			Message:        "上线失败后执行自动回滚失败，请立即人工介入",
			ExitCode:       code,
			StderrTail:     maskAndTail(combinedStderr, 4096),
			LogTail:        maskAndTail(faultLogs.Stdout, 4096),
			HealthResponse: maskAndTail(healthRaw, 4096),
			Timestamp:      time.Now().Unix(),
		}
		d.persistAndReport(ctx, id, rec, rep, true)
		return
	}

	// 验证旧版本恢复健康
	rbHealthRaw, rbHealthErr := d.waitForTargetHealthy(ctx, rec.PrevVersion, time.Duration(d.cfg.HealthCheckTimeoutSec)*time.Second)
	if rbHealthErr != nil {
		combinedHC := strings.TrimSpace(healthRaw) + "\n[after-rollback health]: " + strings.TrimSpace(rbHealthRaw)
		rep := DeployReport{
			CommandID:      rec.MessageID,
			DeploymentID:   rec.DeploymentID,
			ReleaseID:      rec.ReleaseID,
			TargetID:       id.TargetID,
			ProductCode:    id.ProductCode,
			Version:        rec.TargetVersion,
			Status:         StatusRollbackFailed,
			Step:           "rollback",
			StepStatus:     "failed",
			FailedStep:     failedStep,
			Message:        fmt.Sprintf("已执行回滚脚本，但旧版本 (%s) 健康检查仍未恢复: %v", rec.PrevVersion, rbHealthErr),
			ExitCode:       exitCode,
			StderrTail:     maskAndTail(stderrInfo, 4096),
			LogTail:        maskAndTail(faultLogs.Stdout, 4096),
			HealthResponse: maskAndTail(combinedHC, 4096),
			Timestamp:      time.Now().Unix(),
		}
		d.persistAndReport(ctx, id, rec, rep, true)
		return
	}

	rep := DeployReport{
		CommandID:      rec.MessageID,
		DeploymentID:   rec.DeploymentID,
		ReleaseID:      rec.ReleaseID,
		TargetID:       id.TargetID,
		ProductCode:    id.ProductCode,
		Version:        rec.TargetVersion,
		Status:         StatusRolledBack,
		Step:           "rollback",
		StepStatus:     "succeeded",
		FailedStep:     failedStep,
		Message:        fmt.Sprintf("升级在 %s 步骤失败，已自动回滚至旧版本 %s 并验证健康", failedStep, rec.PrevVersion),
		ExitCode:       exitCode,
		StderrTail:     maskAndTail(stderrInfo, 4096),
		LogTail:        maskAndTail(faultLogs.Stdout, 4096),
		HealthResponse: maskAndTail(healthRaw, 4096),
		Timestamp:      time.Now().Unix(),
	}
	d.persistAndReport(ctx, id, rec, rep, true)
}

// abortBeforeInstall 在尚未替换线上程序的前置阶段（验签/防降级/空间预检/下载/备份）阻断部署，保持线上旧版本继续服务并脱敏上报。
// 参数 ctx 为上下文，id 为身份，rec 为日志记录，failedStep 为被阻断的步骤名，reason 为原因，healthRaw 为健康检查原始响应；无返回值。
func (d *Daemon) abortBeforeInstall(ctx context.Context, id *Identity, rec JournalRecord, failedStep, reason, healthRaw string) {
	d.cleanStagingFiles()
	rep := DeployReport{
		CommandID:      rec.MessageID,
		DeploymentID:   rec.DeploymentID,
		ReleaseID:      rec.ReleaseID,
		TargetID:       id.TargetID,
		ProductCode:    id.ProductCode,
		Version:        rec.TargetVersion,
		Status:         StatusRolledBack,
		Step:           failedStep,
		StepStatus:     "failed",
		FailedStep:     failedStep,
		Message:        desensitize.MaskSecrets(reason),
		HealthResponse: maskAndTail(healthRaw, 4096),
		Timestamp:      time.Now().Unix(),
	}
	d.persistAndReport(ctx, id, rec, rep, true)
	if rec.MessageID != "" {
		_ = d.markMessageProcessed(rec.MessageID, rec.MessageSeq, rec.DeploymentID)
	}
}

// abortBeforeInstallWithExec 在前置子命令（如数据库备份）返回非零退出码时中止部署，原样附带脱敏后的退出码与 stderr 末尾上报。
// 参数 ctx 为上下文，id 为身份，rec 为日志记录，failedStep 为步骤名，reason 为摘要，exitCode 为退出码，stderrStr 为错误输出；无返回值。
func (d *Daemon) abortBeforeInstallWithExec(ctx context.Context, id *Identity, rec JournalRecord, failedStep, reason string, exitCode int, stderrStr string) {
	d.cleanStagingFiles()
	rep := DeployReport{
		CommandID:    rec.MessageID,
		DeploymentID: rec.DeploymentID,
		ReleaseID:    rec.ReleaseID,
		TargetID:     id.TargetID,
		ProductCode:  id.ProductCode,
		Version:      rec.TargetVersion,
		Status:       StatusRolledBack,
		Step:         failedStep,
		StepStatus:   "failed",
		FailedStep:   failedStep,
		Message:      desensitize.MaskSecrets(reason),
		ExitCode:     exitCode,
		StderrTail:   maskAndTail(stderrStr, 4096),
		Timestamp:    time.Now().Unix(),
	}
	d.persistAndReport(ctx, id, rec, rep, true)
	if rec.MessageID != "" {
		_ = d.markMessageProcessed(rec.MessageID, rec.MessageSeq, rec.DeploymentID)
	}
}

// validateManifestRules 核验发布清单的 Ed25519 签名、产品编码匹配、目标版本号匹配及最低更新 Agent 版本要求。
// 参数 m 为待校验清单，releasePubB64 为发布验签公钥，expectedProduct 为绑定产品编码，expectedVer 为期望版本号；通过返回 nil，否则返回错误。
func (d *Daemon) validateManifestRules(m *manifest.ReleaseManifest, releasePubB64, expectedProduct, expectedVer string) error {
	if err := m.Verify(releasePubB64); err != nil {
		return fmt.Errorf("发布清单 Ed25519 签名校验失败: %w", err)
	}
	if expectedProduct != "" && m.ProductCode != expectedProduct {
		return fmt.Errorf("清单产品编码 (%s) 与绑定产品 (%s) 不一致", m.ProductCode, expectedProduct)
	}
	if expectedVer != "" && !sameVersion(m.Version, expectedVer) {
		return fmt.Errorf("清单版本号 (%s) 与指令目标版本 (%s) 不一致", m.Version, expectedVer)
	}
	if strings.TrimSpace(m.MinAgentVersion) != "" {
		curAgentVer, errA := semver.Parse(d.cfg.AgentVersion)
		minAgentVer, errM := semver.Parse(m.MinAgentVersion)
		if errA != nil || errM != nil {
			return fmt.Errorf("解析最低更新 Agent 版本号失败: %q", m.MinAgentVersion)
		}
		if curAgentVer.Compare(minAgentVer) < 0 {
			return fmt.Errorf("当前更新 Agent 版本 %s 低于制品要求的最低版本 %s", d.cfg.AgentVersion, m.MinAgentVersion)
		}
	}
	return nil
}

// checkDiskSpaceForDeploy 校验暂存/安装目录与数据库备份目录的实际剩余磁盘空间是否足以容纳制品、程序备份及本次数据库备份（9.4 第 2 步与 9.5 节）。
// 参数 bundleBytes 为制品包字节数，needBackup 表示本次是否必须执行数据库备份；空间充足返回 nil，不足返回阻断部署的具体错误。
func (d *Daemon) checkDiskSpaceForDeploy(bundleBytes int64, needBackup bool) error {
	stagingDisk, err := d.diskStatFn(d.cfg.StagingDir)
	if err != nil {
		return fmt.Errorf("读取暂存目录磁盘空间失败: %w", err)
	}
	var pkgReserve uint64
	if bundleBytes > 0 {
		// 预留：暂存包 + 解压工作区 + 旧版二进制备份（约 3 倍包体积）
		pkgReserve = uint64(bundleBytes) * 3
	}
	requiredStaging := d.cfg.MinFreeDiskBytes + pkgReserve
	if stagingDisk.FreeBytes < requiredStaging {
		return fmt.Errorf("暂存与安装磁盘剩余空间不足: 剩余 %d 字节，至少需要 %d 字节", stagingDisk.FreeBytes, requiredStaging)
	}

	if !needBackup {
		return nil
	}

	backupEst := d.cfg.EstimatedDBBackupBytes
	if lastBytes := d.store.LatestBackupBytes(); lastBytes > 0 {
		// 若存在历史数据库备份，按最大单次备份体积的 120% 与静态预估值取较大者
		dynamicEst := lastBytes + lastBytes/5
		if dynamicEst > backupEst {
			backupEst = dynamicEst
		}
	}
	if backupEst == 0 {
		backupEst = 64 * 1024 * 1024
	}

	backupTargetDir := d.cfg.BackupRootDir
	_ = os.MkdirAll(backupTargetDir, 0755)
	backupDisk, err := d.diskStatFn(backupTargetDir)
	if err != nil {
		backupDisk = stagingDisk
	}

	requiredBackupFree := d.cfg.MinFreeDiskBytes + backupEst
	if backupDisk.FreeBytes < requiredBackupFree {
		return fmt.Errorf("数据库备份磁盘剩余空间不足（阻断部署，防止无备份执行数据改写迁移）：剩余 %d 字节，本次备份预计至少需要 %d 字节",
			backupDisk.FreeBytes, requiredBackupFree)
	}
	// 当暂存目录与备份目录位于同一分区（总容量相同）时，校验两者叠加后的总剩余空间
	if backupDisk.TotalBytes == stagingDisk.TotalBytes && stagingDisk.FreeBytes < requiredStaging+backupEst {
		return fmt.Errorf("磁盘总剩余空间不足以同时容纳制品升级与本次数据库备份：剩余 %d 字节，需要 %d 字节",
			stagingDisk.FreeBytes, requiredStaging+backupEst)
	}
	return nil
}

// waitForTargetHealthy 轮询产品健康检查接口直至状态正常、版本号匹配、迁移完成且所有模块只读探测通过（迁移进行中继续等待不提前判失败，9.4 第 6 步）。
// 参数 ctx 为父上下文，expectedVersion 为期望版本号（空串表示不限制具体版本号），timeout 为总等待上限；
// 返回最后一次健康检查原始响应文本及超时/失败原因。
func (d *Daemon) waitForTargetHealthy(ctx context.Context, expectedVersion string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	interval := time.Duration(d.cfg.HealthCheckIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}

	var lastRaw string
	var lastReason string

	for {
		hc, raw, err := d.probeHealthOnce(ctx)
		if raw != "" {
			lastRaw = raw
		}
		if err != nil {
			lastReason = fmt.Sprintf("请求健康检查接口失败: %v", err)
		} else if !hc.MigrationDone || strings.EqualFold(hc.Status, "migrating") {
			// 9.4 第 6 步：含迁移的版本在迁移进行中不判失败，耐心轮询等待至截止时间
			lastReason = fmt.Sprintf("数据库迁移仍在进行中 (status=%s, migrationDone=%v)", hc.Status, hc.MigrationDone)
		} else if !isServiceHealthy(hc) {
			lastReason = fmt.Sprintf("健康检查状态或模块只读探测未通过 (status=%s)", hc.Status)
		} else if expectedVersion != "" && !sameVersion(hc.Version, expectedVersion) {
			lastReason = fmt.Sprintf("运行版本号 (%s) 与期望版本 (%s) 不一致", hc.Version, expectedVersion)
		} else {
			return lastRaw, nil
		}

		if time.Now().After(deadline) {
			return lastRaw, fmt.Errorf("等待服务健康检查超时 (%s): %s", timeout, lastReason)
		}
		select {
		case <-ctx.Done():
			return lastRaw, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// probeHealthOnce 单次请求产品配置的免登录健康检查接口并解析 HealthCheckResponse。
// 参数 ctx 为上下文；返回解析结构体、原始响应文本及请求/状态码错误。
func (d *Daemon) probeHealthOnce(ctx context.Context) (*HealthCheckResponse, string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, d.cfg.HealthCheckURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := d.healthClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	rawBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if readErr != nil {
		return nil, "", readErr
	}
	rawStr := strings.TrimSpace(string(rawBytes))

	var parsed HealthCheckResponse
	if jsonErr := json.Unmarshal(rawBytes, &parsed); jsonErr != nil {
		return nil, rawStr, fmt.Errorf("解析健康检查 JSON 失败 (HTTP %d): %w", resp.StatusCode, jsonErr)
	}
	// 若 HTTP 503 但声明正在执行迁移，保留解析结构以便上层识别 migrationDone=false 继续等待
	if resp.StatusCode >= 400 && (parsed.MigrationDone || !strings.EqualFold(parsed.Status, "migrating")) {
		return &parsed, rawStr, fmt.Errorf("健康检查返回 HTTP %d", resp.StatusCode)
	}
	return &parsed, rawStr, nil
}

// invokeApply 通过无通配参数的固定命令调用 autoship-apply（UseSudo=true 时执行 sudo -n <applyBinPath> <fixedSubcommand>）。
// 参数 ctx 为上下文，fixedSubcommand 为五种固定子命令之一；返回退出码、stdout/stderr 文本及执行错误。
func (d *Daemon) invokeApply(ctx context.Context, fixedSubcommand string) (applyInvokeResult, error) {
	if _, err := apply.ValidateArgs([]string{fixedSubcommand}); err != nil {
		return applyInvokeResult{ExitCode: 2}, err
	}

	var cmd *exec.Cmd
	if d.cfg.UseSudo {
		// 严格对应 /etc/sudoers.d/autoship 放行的无通配完整命令，sudo -n 禁止交互式密码提示挂起
		cmd = exec.CommandContext(ctx, d.cfg.SudoBinPath, "-n", d.cfg.ApplyBinPath, fixedSubcommand)
	} else {
		cmd = exec.CommandContext(ctx, d.cfg.ApplyBinPath, fixedSubcommand)
	}

	baseEnv := []string{"PATH=" + apply.SafeMinimalPath}
	if len(d.applyExtraEnv) > 0 && os.Geteuid() != 0 {
		baseEnv = append(baseEnv, d.applyExtraEnv...)
	}
	cmd.Env = baseEnv

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 {
			code = exitErr.ExitCode()
		}
	}
	return applyInvokeResult{
		ExitCode: code,
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
	}, err
}

// writeStagingTicket 将 Server 签名的 ActionTicket 原子写入固定暂存目录中的 ticket.json。
// 参数 ticket 为待写入的动作票据；成功返回 nil，失败返回错误。
func (d *Daemon) writeStagingTicket(ticket envelope.ActionTicket) error {
	ticketPath := filepath.Join(d.cfg.StagingDir, apply.StagingTicketFileName)
	return writeJSONAtomic(ticketPath, ticket, 0644)
}

// cleanStagingFiles 清理固定暂存目录中的 ticket.json、bundle.tar.gz 及未完结分片文件。
// 无入参；无返回值。
func (d *Daemon) cleanStagingFiles() {
	_ = os.Remove(filepath.Join(d.cfg.StagingDir, apply.StagingTicketFileName))
	_ = os.Remove(filepath.Join(d.cfg.StagingDir, apply.StagingBundleFileName))
	_ = os.Remove(filepath.Join(d.cfg.StagingDir, apply.StagingBundleFileName+".part"))
}

// reportStep 构造非终态分步进度报告、更新本地 journal.json 并立即向 Server 发送报告。
// 参数 ctx 为上下文，id 为身份，rec 为当前记录指针，status/step/stepStatus/message 为进度描述；无返回值。
func (d *Daemon) reportStep(ctx context.Context, id *Identity, rec *JournalRecord, status, step, stepStatus, message string) {
	rec.CurrentStep = step
	rec.Status = status
	rec.UpdatedAt = time.Now().Unix()

	rep := DeployReport{
		CommandID:    rec.MessageID,
		DeploymentID: rec.DeploymentID,
		ReleaseID:    rec.ReleaseID,
		TargetID:     id.TargetID,
		ProductCode:  id.ProductCode,
		Version:      rec.TargetVersion,
		Status:       status,
		Step:         step,
		StepStatus:   stepStatus,
		Message:      desensitize.MaskSecrets(message),
		Timestamp:    time.Now().Unix(),
	}
	d.persistAndReport(ctx, id, *rec, rep, false)
}

// persistAndReport 先将部署记录（含待发报告）原子落盘到 journal.json，再向 Server 发送报告，成功后回写 ReportAcked=true。
// 参数 ctx 为上下文，id 为身份，rec 为日志记录，rep 为报告内容，terminal 表示是否为本次部署终态；无返回值。
func (d *Daemon) persistAndReport(ctx context.Context, id *Identity, rec JournalRecord, rep DeployReport, terminal bool) {
	rec.CurrentStep = rep.Step
	rec.Status = rep.Status
	rec.Terminal = terminal
	rec.ReportAcked = false
	repCopy := rep
	rec.LastReport = &repCopy
	rec.UpdatedAt = time.Now().Unix()

	_ = d.store.UpdateJournal(func(j *Journal) {
		j.Deployments[rec.DeploymentID] = rec
	})

	if err := d.client.ReportDeployment(ctx, id, rec.DeploymentID, rep); err != nil {
		d.logger.Printf("上报部署进度失败 (deployment=%s step=%s status=%s): %v", rec.DeploymentID, rep.Step, rep.Status, err)
		return
	}
	if terminal {
		_ = d.store.UpdateJournal(func(j *Journal) {
			if cur, ok := j.Deployments[rec.DeploymentID]; ok {
				cur.ReportAcked = true
				cur.UpdatedAt = time.Now().Unix()
				j.Deployments[rec.DeploymentID] = cur
			}
		})
	}
}

// markMessageProcessed 在本地 journal.json 中标记指令信封 ID 已处理完毕并推进确认序号。
// 参数 msgID 为信封 ID，seq 为信封序号，deploymentID 为关联部署标识；返回持久化错误。
func (d *Daemon) markMessageProcessed(msgID string, seq uint64, deploymentID string) error {
	return d.store.UpdateJournal(func(j *Journal) {
		if msgID != "" {
			j.ProcessedMessages[msgID] = deploymentID
		}
		if seq > j.LastAckSeq {
			j.LastAckSeq = seq
		}
	})
}

// isServiceHealthy 判定健康检查返回的总体状态与全部模块只读探测（probes map）是否全部通过。
// 参数 hc 为解析后的 *HealthCheckResponse；全部正常返回 true，任一模块探测失败或总体状态异常返回 false。
func isServiceHealthy(hc *HealthCheckResponse) bool {
	if hc == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(hc.Status)) {
	case "ok", "healthy", "up", "running", "pass":
	default:
		return false
	}
	for _, probeVal := range hc.Probes {
		if !isProbeValueHealthy(probeVal) {
			return false
		}
	}
	return true
}

// isProbeValueHealthy 兼容判定 probes map 中布尔值、状态字符串或嵌套对象形式的多模块探测结果。
// 参数 v 为单个模块的探测结果值；探测健康返回 true，否则返回 false。
func isProbeValueHealthy(v any) bool {
	if v == nil {
		return false
	}
	switch typed := v.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "ok", "healthy", "up", "pass", "passed", "true":
			return true
		default:
			return false
		}
	case map[string]any:
		if okFlag, exists := typed["ok"]; exists {
			if b, isBool := okFlag.(bool); isBool {
				return b
			}
		}
		if st, exists := typed["status"]; exists {
			return isProbeValueHealthy(st)
		}
		return false
	default:
		return false
	}
}

// findCrashInLogs 扫描服务最近日志文本是否包含 panic、fatal 或段错误崩溃标记。
// 参数 logsText 为日志内容；命中崩溃时返回命中的关键字，未命中返回空字符串。
func findCrashInLogs(logsText string) string {
	lower := strings.ToLower(logsText)
	for _, kw := range crashLogKeywords {
		if strings.Contains(lower, kw) {
			return kw
		}
	}
	return ""
}

// maskAndTail 对故障诊断文本先调用 desensitize.MaskSecrets 脱敏密钥/连接串密码，再截取末尾最多 maxBytes 字节。
// 参数 raw 为原始诊断文本，maxBytes 为最大保留字节数；返回安全的尾部文本。
func maskAndTail(raw string, maxBytes int) string {
	masked := desensitize.MaskSecrets(strings.TrimSpace(raw))
	if maxBytes > 0 && len(masked) > maxBytes {
		return masked[len(masked)-maxBytes:]
	}
	return masked
}

// sameVersion 比较两个版本号字符串在语义化版本规则下是否相等。
// 参数 a、b 为版本号字符串；相等返回 true，否则返回 false。
func sameVersion(a, b string) bool {
	va, errA := semver.Parse(a)
	vb, errB := semver.Parse(b)
	if errA == nil && errB == nil {
		return va.Compare(vb) == 0
	}
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// statFilesystem 使用标准库 syscall.Statfs 查询给定路径所在文件系统的剩余可用字节数与总容量字节数。
// 参数 path 为目录或文件路径；返回 DiskStats 及系统调用错误。
func statFilesystem(path string) (DiskStats, error) {
	target := path
	for {
		if _, err := os.Stat(target); err == nil {
			break
		}
		parent := filepath.Dir(target)
		if parent == target {
			break
		}
		target = parent
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(target, &st); err != nil {
		return DiskStats{}, err
	}
	return DiskStats{
		FreeBytes:  uint64(st.Bavail) * uint64(st.Bsize),
		TotalBytes: uint64(st.Blocks) * uint64(st.Bsize),
	}, nil
}

// readKeyFileTrimmed 读取本地只读公钥文件并去除首尾空白。
// 参数 path 为公钥路径；返回公钥文本与读取错误。
func readKeyFileTrimmed(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// firstNonEmpty 返回传入字符串列表中第一个去除空白后非空的字符串。
// 参数 vals 为候选字符串列表；返回首个非空值或空串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// clampUint64ToInt64 将 uint64 字节计数安全转换为非负 int64，避免超大容量文件系统溢出为负数。
// 参数 v 为无符号字节数；返回不超过 math.MaxInt64 的 int64 值。
func clampUint64ToInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
