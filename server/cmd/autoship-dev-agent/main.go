// Command autoship-dev-agent 是运行在个人 Mac 上的 autoShip 开发 Agent 守护进程入口（设计文档第 7.5、7.6、8 节），负责维持 Server WSS 控制流、提供 tailnet 模型受限中继服务、调度隔离 worktree 自研开发引擎、管理本地预览实例与执行自动发布流水线。
package main

import (
	"bufio"
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
	"autoship/internal/devagent/daemon"
	"autoship/internal/devagent/engine"
	"autoship/internal/devagent/gates"
	"autoship/internal/devagent/preview"
	"autoship/internal/devagent/publisher"
	"autoship/internal/devagent/relay"
	"autoship/internal/devagent/sandbox"
	"autoship/internal/devagent/workspace"
	"autoship/pkg/crypto"
	"autoship/pkg/keychain"
)

// agentVersion 标识当前开发 Agent 程序版本号（构建时可通过 -ldflags 覆盖）。
var agentVersion = "v0.3.0"

// main 解析命令行参数，支持钥匙串密钥初始化、写入机密、本机环境与沙箱自检或启动完整常驻守护进程。
// 无入参；无返回值（遇到致命错误时输出诊断日志并以非零状态码退出）。
func main() {
	defaultCfgPath := filepath.Join(config.Default().RootDir, "dev-agent.yaml")

	cfgPath := flag.String("config", defaultCfgPath, "开发 Agent YAML 配置文件路径")
	showVersion := flag.Bool("version", false, "打印版本号并退出")
	initKeys := flag.Bool("init-keys", false, "在 macOS 钥匙串中初始化 Agent 身份密钥与发布签名密钥并打印公钥")
	setSecretAcct := flag.String("set-secret", "", "从标准输入读取单行机密并存入钥匙串指定条目名（如 llm.local-hub）")
	selfCheckOnly := flag.Bool("self-check", false, "执行 macOS 沙箱、本地 Chrome 与模型多模态看图自检后退出")
	flag.Parse()

	if *showVersion {
		fmt.Printf("autoship-dev-agent %s\n", agentVersion)
		return
	}

	secrets := keychain.NewDefaultStore()
	cfg, err := loadOrDefaultConfig(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	if strings.TrimSpace(*setSecretAcct) != "" {
		if err := handleSaveSecretFromStdin(secrets, cfg.KeychainService, strings.TrimSpace(*setSecretAcct)); err != nil {
			log.Fatalf("写入钥匙串机密失败: %v", err)
		}
		fmt.Printf("已将机密安全写入钥匙串 (%s / %s)\n", cfg.KeychainService, strings.TrimSpace(*setSecretAcct))
		return
	}

	if *initKeys {
		if err := handleInitKeychainKeys(secrets, cfg.KeychainService); err != nil {
			log.Fatalf("初始化钥匙串密钥失败: %v", err)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *selfCheckOnly {
		if err := runStartupSelfCheck(ctx, cfg, secrets); err != nil {
			log.Fatalf("本机自检失败: %v", err)
		}
		return
	}

	if err := runAgentService(ctx, cfg, secrets); err != nil {
		log.Fatalf("开发 Agent 异常退出: %v", err)
	}
}

// loadOrDefaultConfig 加载给定路径的配置文件；若文件尚不存在且处于工具子命令阶段则回退到内存默认配置。
// 参数 path 为配置文件路径；返回 *config.AgentConfig 及格式校验错误。
func loadOrDefaultConfig(path string) (*config.AgentConfig, error) {
	expanded := config.ExpandPath(path)
	if _, err := os.Stat(expanded); errors.Is(err, os.ErrNotExist) {
		cfg := config.Default()
		cfg.ApplyDefaults()
		return cfg, nil
	}
	return config.Load(expanded)
}

// handleSaveSecretFromStdin 从标准输入读取机密文本并保存到 macOS 钥匙串，避免明文密码出现在命令行历史或 YAML 中。
// 参数 store 为钥匙串接口，service 为服务命名空间，account 为条目名；成功返回 nil，失败返回错误。
func handleSaveSecretFromStdin(store keychain.Store, service, account string) error {
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return fmt.Errorf("从标准输入读取密钥失败: %w", err)
	}
	secret := strings.TrimSpace(line)
	if secret == "" {
		return errors.New("输入的密钥内容不能为空")
	}
	return store.Set(service, account, secret)
}

// handleInitKeychainKeys 确保钥匙串内已生成开发 Agent 身份 Ed25519 密钥对与发布清单签名 Ed25519 密钥对，并打印对应公钥供后台与更新 Agent 配置。
// 参数 store 为钥匙串接口，agentKeychainSvc 为 Agent 钥匙串命名空间；成功返回 nil，失败返回错误。
func handleInitKeychainKeys(store keychain.Store, agentKeychainSvc string) error {
	agentPub, err := ensureKeyPairInStore(store, agentKeychainSvc, publisher.AgentIdentityAccount, "agent_ed25519_pub")
	if err != nil {
		return err
	}
	relPub, err := ensureKeyPairInStore(store, publisher.ReleaseKeychainService, publisher.ReleaseKeychainAccount, "release_ed25519_pub")
	if err != nil {
		return err
	}
	fmt.Printf("Agent Identity Public Key (登记到 Server 后台):\n%s\n\n", agentPub)
	fmt.Printf("Release Signing Public Key (写入产品服务器 /etc/autoship/release.pub):\n%s\n", relPub)
	return nil
}

// ensureKeyPairInStore 检查钥匙串内是否存在指定私钥；若不存在则生成新 Ed25519 密钥对存入并返回公钥 Base64。
// 参数 store 为钥匙串接口，service/privAcct/pubAcct 为服务与键名；返回 Base64 公钥及错误。
func ensureKeyPairInStore(store keychain.Store, service, privAcct, pubAcct string) (string, error) {
	if pub, err := store.Get(service, pubAcct); err == nil && strings.TrimSpace(pub) != "" {
		if _, pErr := store.Get(service, privAcct); pErr == nil {
			return strings.TrimSpace(pub), nil
		}
	}
	pub, priv, err := crypto.GenerateEd25519KeyPair()
	if err != nil {
		return "", err
	}
	if err := store.Set(service, privAcct, priv); err != nil {
		return "", err
	}
	_ = store.Set(service, pubAcct, pub)
	return pub, nil
}

// runStartupSelfCheck 执行沙箱可用性、本机 Chrome 路径及模型多模态看图探测，输出 JSON 诊断摘要。
// 参数 ctx 为上下文，cfg 为全局配置，secrets 为钥匙串接口；返回执行错误。
func runStartupSelfCheck(ctx context.Context, cfg *config.AgentConfig, secrets keychain.Store) error {
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	sbRes, _ := sandbox.SelfCheck(checkCtx, "")
	chromeBin := cfg.ChromeBinaryPath
	if chromeBin == "" {
		chromeBin = gates.LocateLocalChromeBinary()
	}
	llmCli := engine.NewFailoverChatClient(cfg.KeychainService, secrets, nil)
	visionRes := llmCli.ProbeVisionCapability(checkCtx, cfg.Models.Profiles)

	summary := map[string]any{
		"version":      agentVersion,
		"sandbox":      sbRes,
		"chromeBinary": chromeBin,
		"visionProbe":  visionRes,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(summary)
}

// runAgentService 组装工作区、端口池、进程表、防休眠、模型中继、预览管理器、发布流水线与 WSS 守护进程并阻塞运行直至收到退出信号。
// 参数 ctx 为信号上下文，cfg 为全局配置，secrets 为钥匙串接口；返回致命启动错误。
func runAgentService(ctx context.Context, cfg *config.AgentConfig, secrets keychain.Store) error {
	wsMgr, err := workspace.NewManager(cfg.RootDir)
	if err != nil {
		return err
	}
	ports, err := workspace.NewPortAllocator(cfg.PortPool.StartPort, cfg.PortPool.EndPort)
	if err != nil {
		return err
	}
	procs := workspace.NewProcessRegistry()
	inhibitor := workspace.NewSleepInhibitor(procs)

	var dbMgr *workspace.DBManager
	if cfg.WorkspaceDB.Enabled {
		dbPass := cfg.WorkspaceDB.Password
		if cfg.WorkspaceDB.KeychainAccount != "" {
			if v, kErr := secrets.Get(cfg.KeychainService, cfg.WorkspaceDB.KeychainAccount); kErr == nil && v != "" {
				dbPass = v
			}
		}
		dbMgr, err = workspace.NewMySQLDBManager(cfg.WorkspaceDB.Host, cfg.WorkspaceDB.Port, cfg.WorkspaceDB.AdminUser, dbPass)
		if err != nil {
			return fmt.Errorf("初始化测试库/预览库管理器失败: %w", err)
		}
		defer dbMgr.Close()
	}

	var beforeModelHook func(context.Context) error
	var relaySrv *relay.Server
	if cfg.Relay.Enabled {
		relaySrv, err = relay.NewServer(cfg, secrets)
		if err != nil {
			return err
		}
		beforeModelHook = relaySrv.WaitUntilIntakeIdle
	}

	devEng := engine.NewDevEngine(cfg, secrets, beforeModelHook)
	pvMgr := preview.NewManager(cfg, ports, procs, inhibitor, dbMgr)
	pvMgr.StartIdleReaperLoop(ctx, 5*time.Minute)
	defer pvMgr.StopAll(context.Background())

	pubPipe, err := publisher.NewPipeline(cfg, secrets, wsMgr, dbMgr, pvMgr)
	if err != nil {
		return err
	}

	agentDaemon, err := daemon.NewDaemon(cfg, secrets, devEng, wsMgr, inhibitor, pvMgr, pubPipe)
	if err != nil {
		return err
	}
	if err := agentDaemon.BootstrapRegisterIfNeeded(ctx); err != nil {
		return err
	}

	if relaySrv != nil {
		listenAddr, startErr := relaySrv.Start()
		if startErr != nil {
			log.Printf("[WARN] 模型中继 HTTPS 未能启动（检查证书 %s 是否已签发）: %v", cfg.Relay.CertFile, startErr)
		} else {
			log.Printf("[INFO] 模型受限中继服务已监听 https://%s", listenAddr)
			defer func() {
				shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = relaySrv.Shutdown(shutCtx)
			}()
		}
	}

	log.Printf("[INFO] autoship-dev-agent %s 已启动，连接 Server: %s%s", agentVersion, cfg.ServerURL, cfg.ServerWSPath)
	return agentDaemon.Run(ctx)
}
