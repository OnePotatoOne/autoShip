// Package main 是 autoShip Server 的单文件启动入口，负责初始化自建根 CA 与双服务端证书、连接 MySQL 8、启动模型探活并独立监听管理后台端口与可在线热切换的外部调用端口（全程强制 TLS，禁止明文 HTTP）。
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"autoship/internal/config"
	"autoship/internal/database"
	"autoship/internal/router"
	"autoship/pkg/pki"
)

// version 在编译时可通过 -ldflags "-X main.version=vX.Y.Z" 注入。
var version = "v0.3.0-dev"

// PublicGatewayRunner 管理独立于管理后台端口之外的外部接口（`/open/v1/*` 与 `/agent/v1/deploy/*`）HTTPS 监听器，支持后台配置修改后零停机热切换端口。
type PublicGatewayRunner struct {
	mu      sync.Mutex
	ca      *pki.Bundle
	cfg     *config.ServerConfig
	handler http.Handler
	active  *http.Server
}

// NewPublicGatewayRunner 创建外部调用独立端口监听器控制器。
// 参数 ca 为根 CA Bundle，cfg 为服务端全局配置，handler 为只挂载外部路由的 Gin 引擎；返回 *PublicGatewayRunner。
func NewPublicGatewayRunner(ca *pki.Bundle, cfg *config.ServerConfig, handler http.Handler) *PublicGatewayRunner {
	return &PublicGatewayRunner{
		ca:      ca,
		cfg:     cfg,
		handler: handler,
	}
}

// StartOrRebind 校验并尝试绑定新的外部监听地址；确认新端口监听成功后平滑切换并优雅关闭旧监听器，避免目标端口被占用时影响现网可用性。
// 参数 listenAddr 为目标监听地址（如 "0.0.0.0:9443"）；成功返回 nil，端口冲突或监听失败返回具体错误。
func (r *PublicGatewayRunner) StartOrRebind(listenAddr string) error {
	normalized, err := config.NormalizeAndValidatePublicListenAddr(listenAddr, r.cfg.TailnetListenAddr)
	if err != nil {
		return err
	}

	publicSANs := append([]string{}, r.cfg.PublicSANIPs...)
	if host, _, splitErr := net.SplitHostPort(normalized); splitErr == nil && net.ParseIP(host) != nil && !net.ParseIP(host).IsUnspecified() {
		publicSANs = append(publicSANs, host)
	}
	publicCert, err := r.ca.EnsureServerCert(r.cfg.PKIDir(), "public", "autoShip Public Gateway", publicSANs, []string{"localhost"})
	if err != nil {
		return fmt.Errorf("签发外部调用端口证书失败: %w", err)
	}

	ln, err := net.Listen("tcp", normalized)
	if err != nil {
		return fmt.Errorf("无法绑定外部调用端口 %s（请检查端口是否被占用或有无权限）: %w", normalized, err)
	}

	newSrv := &http.Server{
		Addr:              normalized,
		Handler:           r.handler,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{publicCert},
		},
	}

	r.mu.Lock()
	oldSrv := r.active
	r.active = newSrv
	r.mu.Unlock()

	go func() {
		log.Printf("[INFO] 外部调用独立入口（Open API & 更新 Agent）HTTPS 监听于 https://%s", normalized)
		if serveErr := newSrv.ServeTLS(ln, "", ""); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("[ERROR] 外部调用入口 (%s) 异常退出: %v", normalized, serveErr)
		}
	}()

	if oldSrv != nil {
		go func(prev *http.Server) {
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = prev.Shutdown(shutCtx)
			log.Printf("[INFO] 已平滑释放旧外部监听端口 %s", prev.Addr)
		}(oldSrv)
	}
	return nil
}

// Shutdown 在进程收到退出信号时优雅关闭当前活跃的外部接口监听器。
// 参数 ctx 为关停超时上下文；返回关闭结果错误。
func (r *PublicGatewayRunner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	srv := r.active
	r.active = nil
	r.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// main 解析命令行参数，初始化 PKI、数据库与双独立端口 HTTPS 监听器，并在收到退出信号时优雅关停。
func main() {
	var (
		configPath  = flag.String("config", "./configs/server.yaml", "Server YAML 配置文件路径")
		showVersion = flag.Bool("version", false, "打印版本号并退出")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("autoship-server %s\n", version)
		return
	}

	cfg, encKey, err := config.LoadOrInit(*configPath)
	if err != nil {
		log.Fatalf("[FATAL] 加载配置失败: %v", err)
	}

	ca, err := pki.EnsureCA(cfg.PKIDir())
	if err != nil {
		log.Fatalf("[FATAL] 初始化自建根 CA 失败: %v", err)
	}
	log.Printf("[INFO] autoShip Root CA 就绪，固定指纹 SHA-256: %s", ca.CAFingerprint)

	tailnetSANs := append([]string{}, cfg.TailnetSANIPs...)
	if host, _, splitErr := net.SplitHostPort(cfg.TailnetListenAddr); splitErr == nil && net.ParseIP(host) != nil && !net.ParseIP(host).IsUnspecified() {
		tailnetSANs = append(tailnetSANs, host)
	}
	tailnetCert, err := ca.EnsureServerCert(cfg.PKIDir(), "tailnet", "autoShip Tailnet Admin", tailnetSANs, []string{"localhost"})
	if err != nil {
		log.Fatalf("[FATAL] 签发 Tailnet 后台证书失败: %v", err)
	}

	if _, dbErr := database.Init(cfg.Database, encKey); dbErr != nil {
		log.Printf("[WARN] MySQL 数据库暂未连通（将在配置就绪后重连）: %v", dbErr)
	} else {
		log.Printf("[INFO] MySQL 数据库 %s 迁移及表/字段注释核验完成", cfg.Database.DBName)
	}

	app := router.Build(cfg, *configPath, encKey, ca, database.Get)
	stopCh := make(chan struct{})
	app.LLMService.StartProbeLoop(stopCh)

	pubGateway := NewPublicGatewayRunner(ca, cfg, app.PublicEngine)
	if app.AuditService != nil {
		app.AuditService.SetPublicListenerRebinder(pubGateway.StartOrRebind)
	}
	if err := pubGateway.StartOrRebind(cfg.PublicListenAddr); err != nil {
		log.Fatalf("[FATAL] 启动外部调用独立端口 %s 失败: %v", cfg.PublicListenAddr, err)
	}

	tailnetSrv := &http.Server{
		Addr:              cfg.TailnetListenAddr,
		Handler:           app.TailnetEngine,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{tailnetCert},
		},
	}

	go func() {
		ln, listenErr := net.Listen("tcp", cfg.TailnetListenAddr)
		if listenErr != nil {
			log.Fatalf("[FATAL] 监听管理后台地址 %s 失败: %v", cfg.TailnetListenAddr, listenErr)
		}
		log.Printf("[INFO] 管理后台与开发 Agent HTTPS 监听于 https://%s", cfg.TailnetListenAddr)
		if serveErr := tailnetSrv.ServeTLS(ln, "", ""); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Tailnet HTTPS 服务异常退出: %v", serveErr)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("[INFO] 收到停止信号，正在优雅关闭 autoShip Server...")
	close(stopCh)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = tailnetSrv.Shutdown(ctx)
	_ = pubGateway.Shutdown(ctx)
}
