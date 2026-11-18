// Package publisher 实现验收通过后的确定性自动发布流水线（设计文档 8.10 节）与独立手动打包上传能力（供 autoship-release 复用）：涵盖验收提交核对、主分支 rebase 冲突打回、SemVer 计算、仅限变更日志提交强校验、快进合并推送、GitHub REST API（支持代理）CI 轮询与断点续传下载、钥匙串 Ed25519 签名打包、Server 分片续传上传及发布后资源清理与项目地图刷新。
package publisher

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/preview"
	"autoship/internal/devagent/workspace"
	"autoship/pkg/crypto"
	"autoship/pkg/keychain"
	"autoship/pkg/manifest"
	"autoship/pkg/pki"
	"autoship/pkg/semver"
)

const (
	// ReleaseKeychainService 是发布清单签名私钥在 macOS 钥匙串中的固定服务标识。
	ReleaseKeychainService = "autoship.release.signing"
	// ReleaseKeychainAccount 是发布清单 Ed25519 私钥在钥匙串中的固定账号键名。
	ReleaseKeychainAccount = "release_ed25519_priv"
	// AgentIdentityAccount 是开发 Agent 自身请求签名私钥在钥匙串中的账号键名。
	AgentIdentityAccount = "agent_ed25519_priv"
	// DefaultChunkSizeBytes 是分片上传到 Server 的默认单片大小（2 MB）。
	DefaultChunkSizeBytes = 2 * 1024 * 1024
)

var (
	// ErrCommitMismatch 表示当前工作树 HEAD 与人工验收通过时绑定的 Commit SHA 不一致。
	ErrCommitMismatch = errors.New("工作树当前提交与验收绑定 Commit SHA 不一致，拒绝发布")
	// ErrRebaseConflictNeedReaccept 表示拉取最新主分支执行 rebase 时发生代码冲突，必须中止并退回开发重新验收（8.10 第 1 步）。
	ErrRebaseConflictNeedReaccept = errors.New("任务分支 rebase 最新主分支发生冲突，已自动中止；须回到开发阶段解决冲突并重新通过人工验收")
	// ErrChangelogCommitTouchedCode 表示发布阶段生成的变更日志提交修改了 CHANGELOG 以外的文件（8.10 第 3 步硬性拦截）。
	ErrChangelogCommitTouchedCode = errors.New("变更日志提交包含了非变更日志文件改动，违反发布安全红线")
	// ErrReleaseCIFailed 表示推送 tag 触发的产品 CI 工作流执行失败（8.10 第 6 步）。
	ErrReleaseCIFailed = errors.New("产品发布 CI 执行失败")
)

// PublishRequest 定义触发一次完整自动发布流水线的输入指令参数。
type PublishRequest struct {
	ReleaseID         uint64                `json:"releaseId"`         // Server 侧预创建的版本记录 ID
	WorkID            string                `json:"workId"`            // 关联开发任务 ID
	Product           config.ProductProfile `json:"product"`           // 产品档案
	WorktreeDir       string                `json:"worktreeDir"`       // 任务独立 worktree 目录
	TaskBranch        string                `json:"taskBranch"`        // 任务分支名（如 autoship/w-1-fix）
	AcceptedCommitSHA string                `json:"acceptedCommitSha"` // 验收通过时严格绑定的任务分支提交哈希
	LatestVersion     string                `json:"latestVersion"`     // 当前已发布的最新版本号（如 v1.2.0，首版可为空）
	OverrideVersion   string                `json:"overrideVersion"`   // 人工在验收弹窗手动指定的目标版本号（可为空）
	HasFeature        bool                  `json:"hasFeature"`        // 本次版本是否包含新功能/改进类需求
	ChangelogBody     string                `json:"changelogBody"`     // 变更日志小节正文（为空时自动生成摘要）
	MigrationRisk     manifest.MigrationRisk `json:"migrationRisk"`    // 门禁评定的数据库迁移风险等级
	TestDBName        string                `json:"testDbName"`        // 发布成功后一并销毁的测试库名（可为空）
}

// ProjectMap 描述每次发布完成后自动刷新并同步回 Server 作为“产品知识”的简化项目地图（8.4 & 8.10 节）。
type ProjectMap struct {
	ProductCode string              `json:"productCode"` // 产品编码
	Version     string              `json:"version"`     // 刚发布的版本号
	CommitSHA   string              `json:"commitSha"`   // 对应主分支提交哈希
	GeneratedAt int64               `json:"generatedAt"` // 生成秒级时间戳
	Modules     map[string][]string `json:"modules"`     // 模块 -> 目录列表
	TopDirs     []string            `json:"topDirs"`     // 仓库顶层目录清单
	BuildCmd    string              `json:"buildCmd"`    // 构建命令
	TestCmd     string              `json:"testCmd"`     // 测试命令
}

// PublishResult 记录自动发布流水线全部 9 个步骤跑通后的产出摘要。
type PublishResult struct {
	ReleaseID     uint64                  `json:"releaseId"`     // 版本 ID
	Version       string                  `json:"version"`       // 最终发布的语义化版本号
	Tag           string                  `json:"tag"`           // 推送的 Git Tag
	ReleaseCommit string                  `json:"releaseCommit"` // 含变更日志的最终主分支提交 SHA
	BundlePath    string                  `json:"bundlePath"`    // 本地已签名制品包路径
	BundleSize    int64                   `json:"bundleSize"`    // 制品包字节大小
	BundleSHA256  string                  `json:"bundleSha256"`  // 制品包整包 SHA-256
	Manifest      manifest.ReleaseManifest `json:"manifest"`     // 已签名发布清单
	CIWorkflowURL string                  `json:"ciWorkflowUrl"` // GitHub CI 运行链接
	ProjectMap    *ProjectMap             `json:"projectMap"`    // 刷新后的项目地图
}

// UploadInitResponse 定义 Server `/agent/v1/dev/releases/:releaseId/upload/init` 返回的会话与已传分片列表（支持断点续传）。
type UploadInitResponse struct {
	UploadID       string `json:"uploadId"`       // 上传会话 ID
	UploadedChunks []int  `json:"uploadedChunks"` // 服务端已成功落盘的分片下标列表
}

// Pipeline 编排开发 Agent 的自动发布流水线、GitHub REST API 交互与 Server 分片续传客户端。
type Pipeline struct {
	agentCfg       *config.AgentConfig
	secrets        keychain.Store
	wsManager      *workspace.Manager
	dbManager      *workspace.DBManager
	previewManager *preview.Manager
	githubBaseURL  string
	serverClient   *http.Client
	chunkSize      int

	lockMu       sync.Mutex
	productLocks map[string]*sync.Mutex
}

// NewPipeline 创建自动发布流水线实例。
// 参数 agentCfg 为全局配置，secrets 为钥匙串存取接口，ws/db/pv 为工作区、数据库与预览管理器；返回 *Pipeline 及 TLS 初始化错误。
func NewPipeline(
	agentCfg *config.AgentConfig,
	secrets keychain.Store,
	ws *workspace.Manager,
	db *workspace.DBManager,
	pv *preview.Manager,
) (*Pipeline, error) {
	if agentCfg == nil {
		agentCfg = config.Default()
	}
	if secrets == nil {
		secrets = keychain.NewDefaultStore()
	}

	var srvClient *http.Client
	if strings.TrimSpace(agentCfg.CAFingerprint) != "" {
		u, _ := url.Parse(agentCfg.ServerURL)
		host := ""
		if u != nil {
			host = u.Hostname()
		}
		if c, err := pki.NewCAPinnedHTTPClient(agentCfg.CAFingerprint, host, 2*time.Minute); err == nil {
			srvClient = c
		}
	}
	if srvClient == nil {
		srvClient = &http.Client{Timeout: 2 * time.Minute}
	}

	return &Pipeline{
		agentCfg:       agentCfg,
		secrets:        secrets,
		wsManager:      ws,
		dbManager:      db,
		previewManager: pv,
		githubBaseURL:  "https://api.github.com",
		serverClient:   srvClient,
		chunkSize:      DefaultChunkSizeBytes,
		productLocks:   make(map[string]*sync.Mutex),
	}, nil
}

// SetGitHubBaseURL 允许单元测试将 GitHub REST API 基地址指向本地 httptest Mock 服务。
// 参数 baseURL 为模拟 API 地址；无返回值。
func (p *Pipeline) SetGitHubBaseURL(baseURL string) {
	if strings.TrimSpace(baseURL) != "" {
		p.githubBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
}

// SetServerHTTPClient 允许单元测试注入自定义 HTTP 客户端与分片大小以验证分片续传逻辑。
// 参数 client 为 HTTP 客户端，chunkBytes 为单片字节数；无返回值。
func (p *Pipeline) SetServerHTTPClient(client *http.Client, chunkBytes int) {
	if client != nil {
		p.serverClient = client
	}
	if chunkBytes > 0 {
		p.chunkSize = chunkBytes
	}
}

// Publish 严格按设计文档 8.10 节的 9 个固定步骤串行执行单产品自动发布。
// 参数 ctx 为上下文，req 为发布请求；成功返回 *PublishResult，发生 rebase 冲突、变更日志越界或 CI 失败时返回明确错误。
func (p *Pipeline) Publish(ctx context.Context, req *PublishRequest) (*PublishResult, error) {
	if req == nil {
		return nil, errors.New("PublishRequest 不能为空")
	}
	req.Product.ApplyDefaults()

	// 同一产品的发布严格串行执行（8.10 节末尾要求）
	prodLock := p.lockForProduct(req.Product.ProductCode)
	prodLock.Lock()
	defer prodLock.Unlock()

	gitEnv := buildSSHGitEnv(req.Product.DeployKeyPath)
	defaultBranch := req.Product.DefaultBranch

	// 步骤 1：核对工作树当前 HEAD 与验收绑定的 Commit SHA 完全一致，拉取最新主分支并执行 rebase
	curHead, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.AcceptedCommitSHA) != "" && curHead != strings.TrimSpace(req.AcceptedCommitSHA) {
		return nil, fmt.Errorf("%w: 当前=%s 验收绑定=%s", ErrCommitMismatch, curHead, req.AcceptedCommitSHA)
	}

	oldOriginMain, _ := runGitCmd(ctx, req.WorktreeDir, gitEnv, "rev-parse", "origin/"+defaultBranch)
	if _, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "fetch", "origin", defaultBranch); err != nil {
		return nil, fmt.Errorf("拉取最新主分支 origin/%s 失败: %w", defaultBranch, err)
	}
	newOriginMain, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "rev-parse", "origin/"+defaultBranch)
	if err != nil {
		return nil, err
	}
	if _, rebaseErr := runGitCmd(ctx, req.WorktreeDir, gitEnv, "rebase", "origin/"+defaultBranch); rebaseErr != nil {
		// 一旦发生冲突立即 abort 回退，绝不让模型解完冲突直接发布
		_, _ = runGitCmd(ctx, req.WorktreeDir, gitEnv, "rebase", "--abort")
		return nil, fmt.Errorf("%w (%v)", ErrRebaseConflictNeedReaccept, rebaseErr)
	}
	if oldOriginMain != newOriginMain {
		// 主分支在验收期间有推进且干净 rebase 通过，重跑构建与全量测试确认兼容
		if err := runShellChecks(ctx, req.WorktreeDir, nonEmptyCmds(req.Product.BuildCmd.Cmd, req.Product.TestCmd.Cmd)); err != nil {
			return nil, fmt.Errorf("rebase 最新主分支后重跑校验未通过: %w", err)
		}
	}

	// 步骤 2：计算目标语义化版本号（支持验收弹窗手动覆盖并做防降级校验）
	targetVersion := strings.TrimSpace(req.OverrideVersion)
	if targetVersion != "" {
		ok, verErr := semver.IsUpgrade(req.LatestVersion, targetVersion)
		if verErr != nil || !ok {
			return nil, fmt.Errorf("手动指定版本号 %q 不合法或未高于当前版本 %q", targetVersion, req.LatestVersion)
		}
		parsed, _ := semver.Parse(targetVersion)
		targetVersion = parsed.String()
	} else {
		nextVer, verErr := semver.Next(req.LatestVersion, req.HasFeature)
		if verErr != nil {
			return nil, err
		}
		targetVersion = nextVer
	}

	// 步骤 3：生成变更日志小节并单独提交；用代码强制核查该 commit 仅修改了 CHANGELOG 文件！
	changelogRel := filepath.ToSlash(strings.TrimSpace(req.Product.Release.ChangelogFile))
	changelogBody := strings.TrimSpace(req.ChangelogBody)
	if changelogBody == "" {
		changelogBody = fmt.Sprintf("- 完成开发任务 %s 并通过门禁验收", req.WorkID)
	}
	if err := prependChangelogSection(req.WorktreeDir, changelogRel, targetVersion, changelogBody); err != nil {
		return nil, err
	}
	if _, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "add", "--", changelogRel); err != nil {
		return nil, err
	}
	if _, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "commit", "-m", fmt.Sprintf("chore(release): %s changelog", targetVersion)); err != nil {
		return nil, fmt.Errorf("提交变更日志失败: %w", err)
	}
	if err := VerifyChangelogOnlyCommit(ctx, req.WorktreeDir, "HEAD", changelogRel); err != nil {
		return nil, err
	}
	releaseCommitSHA, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}

	// 步骤 4：合并前在任务分支上执行预检命令（如任务分支测试）
	if err := runShellChecks(ctx, req.WorktreeDir, req.Product.Release.PreMergeChecks); err != nil {
		return nil, fmt.Errorf("任务分支合并前检查未通过: %w", err)
	}

	// 步骤 5：快进推送到远端主分支并打 tag 推送，触发产品发布 CI
	pushBranchSpec := fmt.Sprintf("HEAD:refs/heads/%s", defaultBranch)
	if _, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "push", "origin", pushBranchSpec); err != nil {
		return nil, fmt.Errorf("快进推送主分支 %s 失败: %w", defaultBranch, err)
	}
	if _, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "tag", targetVersion, releaseCommitSHA); err != nil {
		return nil, fmt.Errorf("创建版本 Tag %s 失败: %w", targetVersion, err)
	}
	if _, err := runGitCmd(ctx, req.WorktreeDir, gitEnv, "push", "origin", "refs/tags/"+targetVersion); err != nil {
		return nil, fmt.Errorf("推送版本 Tag %s 失败: %w", targetVersion, err)
	}

	// 步骤 6：通过 GitHub REST API（支持本地代理）轮询 CI 结果、核对 Commit SHA 并断点续传下载制品
	stagingDir, err := os.MkdirTemp("", "autoship-release-stage-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stagingDir)

	ciURL, downloadedBin, downloadedHook, err := p.pollCIAndDownloadArtifacts(ctx, req.Product, releaseCommitSHA, targetVersion, req.WorktreeDir, stagingDir)
	if err != nil {
		return nil, err
	}

	// 步骤 7：读取钥匙串中的发布签名 Ed25519 私钥，使用 manifest.PackBundle 打包并签名
	migRisk := req.MigrationRisk
	if migRisk == "" {
		migRisk = manifest.RiskNone
	}
	manifestObj := manifest.ReleaseManifest{
		SchemaVersion:   1,
		ProductCode:     req.Product.ProductCode,
		Version:         targetVersion,
		Tag:             targetVersion,
		CommitSHA:       releaseCommitSHA,
		Changelog:       changelogBody,
		MigrationRisk:   migRisk,
		MinAgentVersion: req.Product.Release.MinAgentVersion,
		CreatedAt:       time.Now().Unix(),
	}
	bundlePath := filepath.Join(p.agentCfg.RootDir, "releases", fmt.Sprintf("%s-%s.tar.gz", req.Product.ProductCode, targetVersion))
	bundleSize, bundleHash, err := p.PackAndSignBundle(bundlePath, &manifestObj, downloadedBin, downloadedHook)
	if err != nil {
		return nil, err
	}

	// 步骤 8：分片断点续传上传到 autoShip Server
	if req.ReleaseID > 0 && strings.TrimSpace(p.agentCfg.ServerURL) != "" {
		if err := p.UploadBundleResumable(ctx, req.ReleaseID, bundlePath, bundleSize, bundleHash, manifestObj); err != nil {
			return nil, err
		}
	}

	// 步骤 9：生成最新项目地图，随后清理 worktree、测试库与预览实例
	projMap := GenerateProjectMap(req.WorktreeDir, req.Product, targetVersion, releaseCommitSHA)
	if p.previewManager != nil && req.WorkID != "" {
		_ = p.previewManager.Stop(ctx, req.WorkID)
	}
	if p.dbManager != nil && req.TestDBName != "" {
		_ = p.dbManager.DropDatabase(ctx, req.Product.Preview.DBPrefix, req.TestDBName)
	}
	if p.wsManager != nil && req.WorkID != "" {
		_ = p.wsManager.RemoveTaskWorktree(ctx, req.Product.ProductCode, req.WorkID)
	}

	return &PublishResult{
		ReleaseID:     req.ReleaseID,
		Version:       targetVersion,
		Tag:           targetVersion,
		ReleaseCommit: releaseCommitSHA,
		BundlePath:    bundlePath,
		BundleSize:    bundleSize,
		BundleSHA256:  bundleHash,
		Manifest:      manifestObj,
		CIWorkflowURL: ciURL,
		ProjectMap:    projMap,
	}, nil
}

// VerifyChangelogOnlyCommit 由代码强制校验指定提交（如 HEAD）修改的文件列表必须且仅能包含声明的变更日志文件（8.10 第 3 步）。
// 参数 ctx 为上下文，worktreeDir 为仓库目录，commitRef 为待校验提交，expectedChangelogRel 为变更日志相对路径；越界修改返回 ErrChangelogCommitTouchedCode。
func VerifyChangelogOnlyCommit(ctx context.Context, worktreeDir, commitRef, expectedChangelogRel string) error {
	out, err := runGitCmd(ctx, worktreeDir, nil, "diff-tree", "--no-commit-id", "--name-only", "-r", commitRef)
	if err != nil {
		return err
	}
	want := filepath.ToSlash(strings.TrimSpace(expectedChangelogRel))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || (len(lines) == 1 && lines[0] == "") {
		return fmt.Errorf("%w: 变更日志提交为空", ErrChangelogCommitTouchedCode)
	}
	for _, line := range lines {
		got := filepath.ToSlash(strings.TrimSpace(line))
		if got != want {
			return fmt.Errorf("%w: 仅允许修改 %s，实际检测到修改了 %s", ErrChangelogCommitTouchedCode, want, got)
		}
	}
	return nil
}

// PackAndSignBundle 从钥匙串读取 Ed25519 发布签名私钥并调用 `manifest.PackBundle` 生成签名制品包（供自动发布与 autoship-release CLI 共用）。
// 参数 outputTarGz 为输出路径，m 为发布清单指针，binaryPath/hookPath 为业务二进制与部署钩子路径；返回包大小、整包 SHA-256 及错误。
func (p *Pipeline) PackAndSignBundle(outputTarGz string, m *manifest.ReleaseManifest, binaryPath, hookPath string) (int64, string, error) {
	releasePriv, err := p.secrets.Get(ReleaseKeychainService, ReleaseKeychainAccount)
	if err != nil {
		return 0, "", fmt.Errorf("从钥匙串读取发布签名私钥失败 (%s/%s): %w", ReleaseKeychainService, ReleaseKeychainAccount, err)
	}
	return manifest.PackBundle(outputTarGz, m, binaryPath, hookPath, releasePriv)
}

// UploadBundleResumable 按 `/upload/init` → `/upload/chunk/:index`（自动跳过服务端已有分片并带单分片重试）→ `/upload/complete` 执行断点续传上传（8.10 第 8 步）。
// 参数 ctx 为上下文，releaseID 为版本 ID，bundlePath 为包路径，totalSize/bundleSHA256 为包大小与哈希，m 为已签名清单；成功返回 nil，失败返回错误。
func (p *Pipeline) UploadBundleResumable(
	ctx context.Context,
	releaseID uint64,
	bundlePath string,
	totalSize int64,
	bundleSHA256 string,
	m manifest.ReleaseManifest,
) error {
	chunkBytes := p.chunkSize
	if chunkBytes <= 0 {
		chunkBytes = DefaultChunkSizeBytes
	}
	totalChunks := int((totalSize + int64(chunkBytes) - 1) / int64(chunkBytes))
	if totalChunks == 0 {
		totalChunks = 1
	}

	// 1. 初始化或恢复上传会话
	initPath := fmt.Sprintf("/agent/v1/dev/releases/%d/upload/init", releaseID)
	initBody, err := json.Marshal(map[string]any{
		"totalSize":   totalSize,
		"bundleSha256": bundleSHA256,
		"chunkSize":   chunkBytes,
		"totalChunks": totalChunks,
		"manifest":    m,
	})
	if err != nil {
		return err
	}
	initRespBytes, err := p.doSignedServerRequest(ctx, http.MethodPost, initPath, "application/json", initBody, nil)
	if err != nil {
		return fmt.Errorf("初始化制品包分片上传失败: %w", err)
	}
	var initResp UploadInitResponse
	_ = json.Unmarshal(initRespBytes, &initResp)

	doneSet := make(map[int]bool, len(initResp.UploadedChunks))
	for _, idx := range initResp.UploadedChunks {
		doneSet[idx] = true
	}

	// 2. 逐片读取磁盘并仅上传服务端尚未持有的分片（支持断网重试）
	f, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, chunkBytes)
	for idx := 0; idx < totalChunks; idx++ {
		offset := int64(idx) * int64(chunkBytes)
		n, readErr := f.ReadAt(buf, offset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("读取制品分片 #%d 失败: %w", idx, readErr)
		}
		if doneSet[idx] {
			continue
		}
		chunkData := buf[:n]
		sum := sha256.Sum256(chunkData)
		chunkHash := hex.EncodeToString(sum[:])

		chunkPath := fmt.Sprintf("/agent/v1/dev/releases/%d/upload/chunk/%d", releaseID, idx)
		extraHeaders := map[string]string{
			"X-AutoShip-Upload-ID":    initResp.UploadID,
			"X-AutoShip-Chunk-SHA256": chunkHash,
		}

		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(time.Duration(attempt*150) * time.Millisecond)
			}
			_, lastErr = p.doSignedServerRequest(ctx, http.MethodPut, chunkPath, "application/octet-stream", chunkData, extraHeaders)
			if lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			return fmt.Errorf("上传制品分片 #%d 连续重试失败: %w", idx, lastErr)
		}
	}

	// 3. 通知 Server 合并校验整包 SHA-256 与清单签名，切换版本为“待部署”
	compPath := fmt.Sprintf("/agent/v1/dev/releases/%d/upload/complete", releaseID)
	compBody, err := json.Marshal(map[string]any{
		"uploadId":     initResp.UploadID,
		"bundleSha256": bundleSHA256,
		"totalSize":    totalSize,
		"manifest":     m,
	})
	if err != nil {
		return err
	}
	if _, err := p.doSignedServerRequest(ctx, http.MethodPost, compPath, "application/json", compBody, nil); err != nil {
		return fmt.Errorf("完成制品包分片合并校验失败: %w", err)
	}
	return nil
}

// GenerateProjectMap 扫描工作树顶层目录结构并结合产品档案命令导出结构化项目地图（8.4 & 8.10 节）。
// 参数 worktreeDir 为仓库路径，prod 为产品档案，version/commitSHA 为发布的版本与提交；返回 *ProjectMap。
func GenerateProjectMap(worktreeDir string, prod config.ProductProfile, version, commitSHA string) *ProjectMap {
	var topDirs []string
	if entries, err := os.ReadDir(worktreeDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				topDirs = append(topDirs, e.Name())
			}
		}
	}
	sort.Strings(topDirs)
	return &ProjectMap{
		ProductCode: prod.ProductCode,
		Version:     version,
		CommitSHA:   commitSHA,
		GeneratedAt: time.Now().Unix(),
		Modules:     prod.Modules,
		TopDirs:     topDirs,
		BuildCmd:    prod.BuildCmd.Cmd,
		TestCmd:     prod.TestCmd.Cmd,
	}
}

// pollCIAndDownloadArtifacts 通过 GitHub REST API 轮询指定 commitSHA 对应的工作流运行结果，校验来源一致后断点续传下载制品文件。
func (p *Pipeline) pollCIAndDownloadArtifacts(
	ctx context.Context,
	prod config.ProductProfile,
	expectedCommitSHA, tag, worktreeDir, stagingDir string,
) (ciHTMLURL string, binaryPath string, hookPath string, err error) {
	httpCli := p.newGitHubHTTPClient()
	pat := ""
	if prod.GitHubPATAccount != "" {
		pat, _ = p.secrets.Get(p.agentCfg.KeychainService, prod.GitHubPATAccount)
	}

	runsURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs?head_sha=%s",
		p.githubBaseURL, prod.GitHubOwner, prod.GitHubRepo, url.QueryEscape(expectedCommitSHA))

	var matchedRunID int64
	for {
		if err := ctx.Err(); err != nil {
			return "", "", "", err
		}
		raw, status, reqErr := doGitHubGET(ctx, httpCli, runsURL, pat)
		if reqErr != nil || status != http.StatusOK {
			return "", "", "", fmt.Errorf("查询 GitHub Actions 运行状态失败 (HTTP %d): %v", status, reqErr)
		}
		var listResp struct {
			WorkflowRuns []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				HeadSHA    string `json:"head_sha"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
			} `json:"workflow_runs"`
		}
		if err := json.Unmarshal(raw, &listResp); err != nil {
			return "", "", "", err
		}

		foundCompleted := false
		for _, run := range listResp.WorkflowRuns {
			if prod.Release.CIWorkflowName != "" && !strings.Contains(run.Name, prod.Release.CIWorkflowName) {
				continue
			}
			// 严格核对 CI 运行对应的提交哈希必须等于本次发布的 commitSHA（8.10 第 7 步）
			if !strings.EqualFold(run.HeadSHA, expectedCommitSHA) {
				continue
			}
			ciHTMLURL = run.HTMLURL
			if run.Status != "completed" {
				break
			}
			if run.Conclusion != "success" {
				return ciHTMLURL, "", "", fmt.Errorf("%w: conclusion=%s url=%s", ErrReleaseCIFailed, run.Conclusion, ciHTMLURL)
			}
			matchedRunID = run.ID
			foundCompleted = true
			break
		}
		if foundCompleted {
			break
		}
		select {
		case <-ctx.Done():
			return "", "", "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	// 获取该次运行产出的 Artifacts 下载地址（或 Release Assets）并执行 Range 断点续传下载
	artListURL := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/artifacts",
		p.githubBaseURL, prod.GitHubOwner, prod.GitHubRepo, matchedRunID)
	artRaw, status, err := doGitHubGET(ctx, httpCli, artListURL, pat)
	if err != nil || status != http.StatusOK {
		return ciHTMLURL, "", "", fmt.Errorf("查询 CI 制品列表失败 (HTTP %d): %v", status, err)
	}
	var artResp struct {
		Artifacts []struct {
			Name               string `json:"name"`
			ArchiveDownloadURL string `json:"archive_download_url"`
			SHA256             string `json:"digest"` // GitHub API 返回 sha256:xxx 摘要（可选校验）
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(artRaw, &artResp); err != nil || len(artResp.Artifacts) == 0 {
		return ciHTMLURL, "", "", errors.New("CI 运行成功但未产出任何可下载制品")
	}

	chosen := artResp.Artifacts[0]
	for _, a := range artResp.Artifacts {
		if prod.Release.ArtifactAssetName != "" && a.Name == prod.Release.ArtifactAssetName {
			chosen = a
			break
		}
	}

	dlTarget := filepath.Join(stagingDir, chosen.Name)
	if err := downloadWithResume(ctx, httpCli, chosen.ArchiveDownloadURL, pat, dlTarget); err != nil {
		return ciHTMLURL, "", "", fmt.Errorf("断点续传下载 CI 制品失败: %w", err)
	}
	if strings.HasPrefix(strings.ToLower(chosen.SHA256), "sha256:") {
		wantDigest := strings.TrimPrefix(strings.ToLower(chosen.SHA256), "sha256:")
		_, gotHash, hashErr := manifest.HashFile(dlTarget)
		if hashErr != nil || !strings.EqualFold(gotHash, wantDigest) {
			return ciHTMLURL, "", "", fmt.Errorf("下载制品 SHA-256 摘要校验不匹配: 期望 %s 实际 %s", wantDigest, gotHash)
		}
	}

	// 若下载产物为 GitHub Actions 标准 zip 包，自动解出其中的业务二进制与可选部署钩子
	binaryPath = dlTarget
	if isZipArchive(dlTarget) {
		extractedFiles, uzErr := unzipToDir(dlTarget, filepath.Join(stagingDir, "unpacked"))
		if uzErr != nil {
			return ciHTMLURL, "", "", uzErr
		}
		for _, ef := range extractedFiles {
			if filepath.Base(ef) == filepath.Base(prod.Release.HookRepoPath) {
				hookPath = ef
			} else if binaryPath == dlTarget {
				binaryPath = ef
			}
		}
	}

	// 若制品压缩包内未单独包含部署钩子，从本次验收一致的发布提交工作树中提取签名
	if hookPath == "" && strings.TrimSpace(prod.Release.HookRepoPath) != "" {
		candidateHook := filepath.Join(worktreeDir, filepath.FromSlash(prod.Release.HookRepoPath))
		if st, statErr := os.Stat(candidateHook); statErr == nil && !st.IsDir() {
			hookPath = candidateHook
		}
	}
	if hookPath == "" {
		return ciHTMLURL, "", "", fmt.Errorf("未找到产品部署钩子文件 (%s)，无法生成合规制品包", prod.Release.HookRepoPath)
	}
	return ciHTMLURL, binaryPath, hookPath, nil
}

// doSignedServerRequest 使用开发 Agent 自身 Ed25519 私钥签名并向 Server 发送 HTTPS 请求。
func (p *Pipeline) doSignedServerRequest(
	ctx context.Context,
	method, pathWithQuery, contentType string,
	body []byte,
	extraHeaders map[string]string,
) ([]byte, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(p.agentCfg.ServerURL), "/")
	req, err := http.NewRequestWithContext(ctx, method, baseURL+pathWithQuery, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	if agentPriv, keyErr := p.secrets.Get(p.agentCfg.KeychainService, AgentIdentityAccount); keyErr == nil && agentPriv != "" {
		ts := fmt.Sprintf("%d", time.Now().Unix())
		nonce, _ := crypto.GenerateRandomHex(12)
		sig, sigErr := crypto.SignHTTPRequestEd25519(agentPriv, method, req.URL.RequestURI(), ts, nonce, body)
		if sigErr != nil {
			return nil, sigErr
		}
		req.Header.Set("X-AutoShip-Agent-ID", p.agentCfg.AgentID)
		req.Header.Set("X-AutoShip-Timestamp", ts)
		req.Header.Set("X-AutoShip-Nonce", nonce)
		req.Header.Set("X-AutoShip-Signature", sig)
	}

	resp, err := p.serverClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Server 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	return respBytes, nil
}

// newGitHubHTTPClient 创建支持可选本地代理（HTTPProxy）的 GitHub API 客户端（14 节风险对策：国内访问 GitHub 支持代理）。
func (p *Pipeline) newGitHubHTTPClient() *http.Client {
	tr := &http.Transport{}
	if strings.TrimSpace(p.agentCfg.HTTPProxy) != "" {
		if u, err := url.Parse(strings.TrimSpace(p.agentCfg.HTTPProxy)); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &http.Client{Transport: tr, Timeout: 2 * time.Minute}
}

// lockForProduct 获取指定产品专属的发布互斥锁。
func (p *Pipeline) lockForProduct(productCode string) *sync.Mutex {
	p.lockMu.Lock()
	defer p.lockMu.Unlock()
	m, ok := p.productLocks[productCode]
	if !ok {
		m = &sync.Mutex{}
		p.productLocks[productCode] = m
	}
	return m
}

// prependChangelogSection 将新版本变更日志小节插入到 CHANGELOG 文件顶部（不存在则新建）。
func prependChangelogSection(worktreeDir, relChangelog, version, body string) error {
	fullPath := filepath.Join(worktreeDir, filepath.FromSlash(relChangelog))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	existing, _ := os.ReadFile(fullPath)
	dateStr := time.Now().Format("2006-01-02")
	newSection := fmt.Sprintf("## %s (%s)\n\n%s\n\n", version, dateStr, strings.TrimSpace(body))
	var combined string
	if len(existing) == 0 {
		combined = "# Changelog\n\n" + newSection
	} else {
		combined = newSection + string(existing)
	}
	return os.WriteFile(fullPath, []byte(combined), 0644)
}

// downloadWithResume 使用 HTTP Range 头将远程制品断点续传下载到本地目标文件（中途断流自动从已落盘字节偏移继续下载）。
func downloadWithResume(ctx context.Context, client *http.Client, downloadURL, pat, destPath string) error {
	for attempt := 0; attempt < 4; attempt++ {
		var offset int64
		if st, err := os.Stat(destPath); err == nil {
			offset = st.Size()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
		if err != nil {
			return err
		}
		if pat != "" {
			req.Header.Set("Authorization", "Bearer "+pat)
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := client.Do(req)
		if err != nil {
			if attempt == 3 {
				return err
			}
			continue
		}

		flags := os.O_CREATE | os.O_WRONLY
		if resp.StatusCode == http.StatusPartialContent && offset > 0 {
			flags |= os.O_APPEND
		} else if resp.StatusCode == http.StatusOK {
			flags |= os.O_TRUNC
		} else {
			_ = resp.Body.Close()
			return fmt.Errorf("下载返回 HTTP %d", resp.StatusCode)
		}

		out, err := os.OpenFile(destPath, flags, 0755)
		if err != nil {
			_ = resp.Body.Close()
			return err
		}
		_, copyErr := io.Copy(out, resp.Body)
		_ = out.Close()
		_ = resp.Body.Close()
		if copyErr == nil {
			return nil
		}
		if attempt == 3 {
			return copyErr
		}
	}
	return nil
}

// doGitHubGET 发起携带细粒度 PAT 的 GitHub REST API GET 请求。
func doGitHubGET(ctx context.Context, client *http.Client, apiURL, pat string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if pat != "" {
		req.Header.Set("Authorization", "Bearer "+pat)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	return data, resp.StatusCode, err
}

// isZipArchive 通过文件头魔数 PK\x03\x04 判断文件是否为 ZIP 归档。
func isZipArchive(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	n, _ := f.Read(magic[:])
	return n == 4 && magic[0] == 'P' && magic[1] == 'K' && magic[2] == 3 && magic[3] == 4
}

// unzipToDir 安全解压 ZIP 文件到目标目录并返回解出的常规文件路径列表。
func unzipToDir(zipPath, destDir string) ([]string, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return nil, err
	}
	var out []string
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		clean := filepath.Clean(zf.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return nil, fmt.Errorf("拒绝 ZIP 内路径穿越条目: %s", zf.Name)
		}
		target := filepath.Join(destDir, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return nil, err
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, err
		}
		wf, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			_ = rc.Close()
			return nil, err
		}
		_, cpErr := io.Copy(wf, rc)
		_ = wf.Close()
		_ = rc.Close()
		if cpErr != nil {
			return nil, cpErr
		}
		out = append(out, target)
	}
	return out, nil
}

// buildSSHGitEnv 组装带产品专用 Deploy Key 的 git 环境变量。
func buildSSHGitEnv(deployKeyPath string) []string {
	cleaned := config.ExpandPath(deployKeyPath)
	if cleaned == "" {
		return nil
	}
	env := os.Environ()
	env = append(env, fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %q -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new", cleaned))
	return env
}

// runGitCmd 执行 git 命令并返回去首尾空白的文本结果。
func runGitCmd(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// runShellChecks 顺序执行一组预检命令。
func runShellChecks(ctx context.Context, dir string, cmds []string) error {
	for _, raw := range cmds {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", trimmed)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("命令 %q 失败: %w (%s)", trimmed, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// nonEmptyCmds 过滤空字符串命令并返回非空切片。
func nonEmptyCmds(items ...string) []string {
	var out []string
	for _, s := range items {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}
