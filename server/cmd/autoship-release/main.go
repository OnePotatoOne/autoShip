// Command autoship-release 是运行在个人 Mac 上的独立发布与签名上传命令行工具（对应设计文档第 8.10 节与第 12 节阶段 1），支持发布密钥初始化、本地制品与部署钩子打包签名、制品包离线验签、分片续传上传以及完整 9 步自动发布触发。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"autoship/internal/devagent/config"
	"autoship/internal/devagent/publisher"
	"autoship/pkg/crypto"
	"autoship/pkg/keychain"
	"autoship/pkg/manifest"
)

// releaseToolVersion 标识独立发布工具版本号。
var releaseToolVersion = "v0.3.0"

// main 根据首个位置子命令（keygen / pack / verify / upload / publish / version）分发执行发布动作。
// 无入参；无返回值（执行失败时打印中文原因并返回非零退出码）。
func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sub := strings.ToLower(strings.TrimSpace(os.Args[1]))
	args := os.Args[2:]

	var err error
	switch sub {
	case "version", "-version", "--version":
		fmt.Printf("autoship-release %s\n", releaseToolVersion)
		return
	case "keygen":
		err = cmdKeygen(args)
	case "pack":
		err = cmdPack(ctx, args)
	case "verify":
		err = cmdVerify(args)
	case "upload":
		err = cmdUpload(ctx, args)
	case "publish":
		err = cmdPublish(ctx, args)
	default:
		printUsage()
		err = fmt.Errorf("未知子命令: %s", sub)
	}
	if err != nil {
		log.Fatalf("[autoship-release] 执行失败: %v", err)
	}
}

// printUsage 向标准错误输出 autoship-release 的全部子命令帮助说明。
// 无入参；无返回值。
func printUsage() {
	fmt.Fprintln(os.Stderr, `用法: autoship-release <subcommand> [options]

可用子命令:
  keygen   在 macOS 钥匙串中生成或读取发布签名 Ed25519 密钥对并输出公钥
  pack     将编译产物与部署钩子打包并用钥匙串 Ed25519 私钥签名（可选直传 Server）
  verify   使用发布公钥离线核验 .tar.gz 制品包的签名与文件 SHA-256 完整性
  upload   将已签名的 .tar.gz 制品包分片断点续传上传到 autoShip Server
  publish  在指定任务 worktree 上执行完整 9 步自动发布流水线
  version  显示工具版本号`)
}

// cmdKeygen 生成或展示存储在 macOS 钥匙串中的发布清单签名公钥。
// 参数 args 为子命令参数切片；成功返回 nil，失败返回错误。
func cmdKeygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	rotate := fs.Bool("rotate", false, "强制重新生成新的发布签名 Ed25519 密钥对（覆盖旧私钥）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	store := keychain.NewDefaultStore()
	if !*rotate {
		if existingPriv, err := store.Get(publisher.ReleaseKeychainService, publisher.ReleaseKeychainAccount); err == nil && existingPriv != "" {
			if pub, pErr := store.Get(publisher.ReleaseKeychainService, "release_ed25519_pub"); pErr == nil && pub != "" {
				fmt.Printf("已存在发布签名公钥 (Base64):\n%s\n", strings.TrimSpace(pub))
				return nil
			}
		}
	}

	pub, priv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		return err
	}
	if err := store.Set(publisher.ReleaseKeychainService, publisher.ReleaseKeychainAccount, priv); err != nil {
		return err
	}
	_ = store.Set(publisher.ReleaseKeychainService, "release_ed25519_pub", pub)
	fmt.Printf("已生成并存入 macOS 钥匙串的发布签名公钥 (Base64):\n%s\n", pub)
	return nil
}

// cmdPack 将指定的二进制程序与产品部署钩子打包成带签名清单的 `.tar.gz`，并在提供 release-id 时一并分片上传。
// 参数 ctx 为上下文，args 为子命令参数；成功返回 nil，失败返回错误。
func cmdPack(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pack", flag.ContinueOnError)
	cfgPath := fs.String("config", "", "可选开发 Agent 配置文件路径（直传 Server 时使用）")
	productCode := fs.String("product", "", "产品唯一编码（必填）")
	version := fs.String("version", "", "语义化版本号，如 v1.2.0（必填）")
	tag := fs.String("tag", "", "Git Tag 名（默认等于 version）")
	commitSHA := fs.String("commit", "", "对应 Git Commit SHA（必填）")
	binaryPath := fs.String("binary", "", "业务制品文件路径（必填）")
	hookPath := fs.String("hook", "", "产品部署钩子脚本/程序路径（必填）")
	changelog := fs.String("changelog", "", "本版本变更日志文本")
	migRisk := fs.String("migration-risk", string(manifest.RiskNone), "迁移风险分级: none | additive | data_rewrite | destructive")
	minAgentVer := fs.String("min-agent-version", "v0.1.0", "要求最低更新 Agent 版本")
	outBundle := fs.String("out", "", "输出的 .tar.gz 制品包路径（必填）")
	releaseID := fs.Uint64("release-id", 0, "可选：打包完成后立即分片上传至指定 Server Release ID")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *productCode == "" || *version == "" || *commitSHA == "" || *binaryPath == "" || *hookPath == "" || *outBundle == "" {
		return errors.New("pack 缺少必填参数（需同时指定 -product -version -commit -binary -hook -out）")
	}
	tagVal := strings.TrimSpace(*tag)
	if tagVal == "" {
		tagVal = strings.TrimSpace(*version)
	}

	agentCfg := config.Default()
	if strings.TrimSpace(*cfgPath) != "" {
		loaded, err := config.Load(config.ExpandPath(*cfgPath))
		if err != nil {
			return err
		}
		agentCfg = loaded
	}

	secrets := keychain.NewDefaultStore()
	pipe, err := publisher.NewPipeline(agentCfg, secrets, nil, nil, nil)
	if err != nil {
		return err
	}

	m := manifest.ReleaseManifest{
		SchemaVersion:   1,
		ProductCode:     strings.TrimSpace(*productCode),
		Version:         strings.TrimSpace(*version),
		Tag:             tagVal,
		CommitSHA:       strings.TrimSpace(*commitSHA),
		Changelog:       *changelog,
		MigrationRisk:   manifest.MigrationRisk(strings.TrimSpace(*migRisk)),
		MinAgentVersion: strings.TrimSpace(*minAgentVer),
		CreatedAt:       time.Now().Unix(),
	}
	outAbs := config.ExpandPath(*outBundle)
	size, sum, err := pipe.PackAndSignBundle(outAbs, &m, config.ExpandPath(*binaryPath), config.ExpandPath(*hookPath))
	if err != nil {
		return err
	}
	fmt.Printf("打包并签名完成: %s (size=%d bytes, sha256=%s)\n", outAbs, size, sum)

	if *releaseID > 0 {
		if err := pipe.UploadBundleResumable(ctx, *releaseID, outAbs, size, sum, m); err != nil {
			return err
		}
		fmt.Printf("已通过分片续传成功上传至 Server Release #%d\n", *releaseID)
	}
	return nil
}

// cmdVerify 使用给定公钥解压核验 `.tar.gz` 制品包的数字签名与包内文件哈希。
// 参数 args 为子命令参数；验签通过返回 nil，否则返回错误。
func cmdVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	bundlePath := fs.String("bundle", "", "待校验的 .tar.gz 制品包路径（必填）")
	pubKey := fs.String("pub-key", "", "Base64 发布签名公钥（留空时尝试从本机钥匙串读取）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*bundlePath) == "" {
		return errors.New("请通过 -bundle 指定制品包路径")
	}
	pub := strings.TrimSpace(*pubKey)
	if pub == "" {
		v, err := keychain.NewDefaultStore().Get(publisher.ReleaseKeychainService, "release_ed25519_pub")
		if err != nil {
			return fmt.Errorf("未传入 -pub-key 且无法从钥匙串读取公钥: %w", err)
		}
		pub = v
	}

	tmpDir, err := os.MkdirTemp("", "autoship-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	m, binFile, hookFile, err := manifest.UnpackAndVerifyBundle(config.ExpandPath(*bundlePath), tmpDir, pub)
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(map[string]any{
		"verified": true,
		"manifest": m,
		"binary":   filepath.Base(binFile),
		"hook":     filepath.Base(hookFile),
	}, "", "  ")
	fmt.Println(string(out))
	return nil
}

// cmdUpload 读取本地已签名的 `.tar.gz` 制品包，校验结构后按分片断点续传协议上传到 Server。
// 参数 ctx 为上下文，args 为子命令参数；成功返回 nil，失败返回错误。
func cmdUpload(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	cfgPath := fs.String("config", filepath.Join(config.Default().RootDir, "dev-agent.yaml"), "开发 Agent YAML 配置文件路径")
	releaseID := fs.Uint64("release-id", 0, "Server 侧目标 Release ID（必填）")
	bundlePath := fs.String("bundle", "", "本地已签名的 .tar.gz 制品包路径（必填）")
	pubKey := fs.String("pub-key", "", "可选发布签名公钥（传入时上传前先做一次本地自验签）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *releaseID == 0 || strings.TrimSpace(*bundlePath) == "" {
		return errors.New("upload 必须指定 -release-id 与 -bundle")
	}

	agentCfg, err := config.Load(config.ExpandPath(*cfgPath))
	if err != nil {
		return err
	}
	secrets := keychain.NewDefaultStore()
	pub := strings.TrimSpace(*pubKey)
	if pub == "" {
		pub, _ = secrets.Get(publisher.ReleaseKeychainService, "release_ed25519_pub")
	}
	if pub == "" {
		return errors.New("缺少发布公钥用于提取验证包内清单，请传入 -pub-key 或先运行 keygen")
	}

	tmpDir, err := os.MkdirTemp("", "autoship-upload-extract-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	cleanBundle := config.ExpandPath(*bundlePath)
	parsedManifest, _, _, err := manifest.UnpackAndVerifyBundle(cleanBundle, tmpDir, pub)
	if err != nil {
		return err
	}
	size, hash, err := manifest.HashFile(cleanBundle)
	if err != nil {
		return err
	}

	pipe, err := publisher.NewPipeline(agentCfg, secrets, nil, nil, nil)
	if err != nil {
		return err
	}
	if err := pipe.UploadBundleResumable(ctx, *releaseID, cleanBundle, size, hash, *parsedManifest); err != nil {
		return err
	}
	fmt.Printf("制品包 %s 已成功分片上传至 Server Release #%d\n", cleanBundle, *releaseID)
	return nil
}

// cmdPublish 从命令行触发完整 9 步自动发布流水线（核对提交、rebase、SemVer、CHANGELOG 强检、推 tag、轮询 CI、签名打包、上传）。
// 参数 ctx 为上下文，args 为子命令参数；成功返回 nil，失败返回错误。
func cmdPublish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	cfgPath := fs.String("config", filepath.Join(config.Default().RootDir, "dev-agent.yaml"), "开发 Agent 配置文件路径")
	productCode := fs.String("product", "", "目标产品编码（必填，须存在于配置的 products 中）")
	workID := fs.String("work-id", "", "开发任务 ID")
	worktreeDir := fs.String("worktree", "", "任务 worktree 目录路径（必填）")
	releaseID := fs.Uint64("release-id", 0, "Server 侧 Release ID（大于 0 时自动上传）")
	acceptedSHA := fs.String("accepted-commit", "", "验收绑定的提交 SHA（必填）")
	latestVer := fs.String("latest-version", "", "当前线上最新版本号（如 v1.0.0）")
	overrideVer := fs.String("override-version", "", "手动指定版本号")
	hasFeature := fs.Bool("has-feature", false, "是否包含新功能以递增次版本号")
	changelog := fs.String("changelog", "", "变更日志正文")
	migRisk := fs.String("migration-risk", string(manifest.RiskNone), "迁移风险等级")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *productCode == "" || *worktreeDir == "" || *acceptedSHA == "" {
		return errors.New("publish 必须指定 -product、-worktree 与 -accepted-commit")
	}
	agentCfg, err := config.Load(config.ExpandPath(*cfgPath))
	if err != nil {
		return err
	}
	var matchedProd *config.ProductProfile
	for i := range agentCfg.Products {
		if agentCfg.Products[i].ProductCode == *productCode {
			matchedProd = &agentCfg.Products[i]
			break
		}
	}
	if matchedProd == nil {
		return fmt.Errorf("配置文件中未找到产品档案 %q", *productCode)
	}

	pipe, err := publisher.NewPipeline(agentCfg, keychain.NewDefaultStore(), nil, nil, nil)
	if err != nil {
		return err
	}
	res, err := pipe.Publish(ctx, &publisher.PublishRequest{
		ReleaseID:         *releaseID,
		WorkID:            *workID,
		Product:           *matchedProd,
		WorktreeDir:       config.ExpandPath(*worktreeDir),
		AcceptedCommitSHA: *acceptedSHA,
		LatestVersion:     *latestVer,
		OverrideVersion:   *overrideVer,
		HasFeature:        *hasFeature,
		ChangelogBody:     *changelog,
		MigrationRisk:     manifest.MigrationRisk(*migRisk),
	})
	if err != nil {
		return err
	}
	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	return nil
}
