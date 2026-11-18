// Command autoship-agent 是运行在产品服务器上低权限专用账号下的自动更新常驻守护进程（对应设计文档第 9 节）。
// 本程序仅主动发起固定根 CA 指纹的 HTTPS 请求连接 autoShip Server，执行心跳、长轮询拉取、断点续传、首次验签、健康探测与分步上报。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"autoship/internal/updateagent/config"
	"autoship/internal/updateagent/daemon"
)

// buildVersion 保存编译期可通过 -ldflags "-X main.buildVersion=vX.Y.Z" 注入的更新 Agent 默认版本号。
var buildVersion = "v0.1.0"

// main 作为 autoship-agent 常驻程序入口，解析启动标志、加载配置并运行主事件循环直至收到终止信号。
// 无直接入参（通过命令行读取 -config 或 -version）；正常退出状态码为 0，配置或注册错误以状态码 1 退出。
func main() {
	code := runAgentMain(os.Args[1:])
	os.Exit(code)
}

// runAgentMain 执行命令行参数解析、配置校验、系统信号监听及 Daemon 启动，并返回退出码。
// 参数 args 为不含程序名的命令行参数切片；正常退出返回 0，失败返回非零退出码。
func runAgentMain(args []string) int {
	fs := flag.NewFlagSet("autoship-agent", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultAgentConfigPath, "更新 Agent YAML 配置文件路径")
	showVersion := fs.Bool("version", false, "输出 autoship-agent 版本号并退出")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintf(os.Stdout, "autoship-agent %s\n", buildVersion)
		return 0
	}

	cfg, err := config.LoadAgentConfig(*cfgPath)
	if err != nil {
		log.Printf("[autoship-agent] 加载配置文件失败: %v", err)
		return 1
	}

	svc, err := daemon.New(cfg)
	if err != nil {
		log.Printf("[autoship-agent] 初始化守护进程失败: %v", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := svc.Run(ctx); err != nil {
		log.Printf("[autoship-agent] 守护进程异常退出: %v", err)
		return 1
	}
	return 0
}
