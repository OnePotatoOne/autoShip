// Package main 是 autoShip Server 的单文件启动入口，负责初始化自建根 CA 与双服务端证书、连接 MySQL 8、启动模型探活并监听 Tailnet 后台端口与公网机器端口（全程强制 TLS，禁止明文 HTTP）。
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
	"syscall"
	"time"

	"autoship/internal/config"
	"autoship/internal/database"
	"autoship/internal/router"
	"autoship/pkg/pki"
)

// version 在编译时可通过 -ldflags "-X main.version=vX.Y.Z" 注入。
var version = "v0.3.0-dev"

// main 解析命令行参数，初始化 PKI、数据库与双 HTTPS 监听器，并在收到退出信号时优雅关停。
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

	publicSANs := append([]string{}, cfg.PublicSANIPs...)
	if host, _, splitErr := net.SplitHostPort(cfg.PublicListenAddr); splitErr == nil && net.ParseIP(host) != nil && !net.ParseIP(host).IsUnspecified() {
		publicSANs = append(publicSANs, host)
	}
	publicCert, err := ca.EnsureServerCert(cfg.PKIDir(), "public", "autoShip Public Gateway", publicSANs, []string{"localhost"})
	if err != nil {
		log.Fatalf("[FATAL] 签发公网入口证书失败: %v", err)
	}

	if _, dbErr := database.Init(cfg.Database, encKey); dbErr != nil {
		log.Printf("[WARN] MySQL 数据库暂未连通（将在配置就绪后重连）: %v", dbErr)
	} else {
		log.Printf("[INFO] MySQL 数据库 %s 迁移及表/字段注释核验完成", cfg.Database.DBName)
	}

	app := router.Build(cfg, *configPath, encKey, ca, database.Get)
	stopCh := make(chan struct{})
	app.LLMService.StartProbeLoop(stopCh)

	tailnetSrv := &http.Server{
		Addr:              cfg.TailnetListenAddr,
		Handler:           app.TailnetEngine,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{tailnetCert},
		},
	}

	publicSrv := &http.Server{
		Addr:              cfg.PublicListenAddr,
		Handler:           app.PublicEngine,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{publicCert},
		},
	}

	go func() {
		ln, listenErr := net.Listen("tcp", cfg.TailnetListenAddr)
		if listenErr != nil {
			log.Fatalf("[FATAL] 监听 Tailnet 地址 %s 失败: %v", cfg.TailnetListenAddr, listenErr)
		}
		log.Printf("[INFO] 管理后台与开发 Agent HTTPS 监听于 https://%s", cfg.TailnetListenAddr)
		if serveErr := tailnetSrv.ServeTLS(ln, "", ""); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Fatalf("[FATAL] Tailnet HTTPS 服务异常退出: %v", serveErr)
		}
	}()

	go func() {
		ln, listenErr := net.Listen("tcp", cfg.PublicListenAddr)
		if listenErr != nil {
			log.Fatalf("[FATAL] 监听公网地址 %s 失败: %v", cfg.PublicListenAddr, listenErr)
		}
		log.Printf("[INFO] 公网机器入口（Open API & 更新 Agent）HTTPS 监听于 https://%s", cfg.PublicListenAddr)
		if serveErr := publicSrv.ServeTLS(ln, "", ""); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Fatalf("[FATAL] 公网 HTTPS 服务异常退出: %v", serveErr)
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
	_ = publicSrv.Shutdown(ctx)
}
