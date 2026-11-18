// Command autoship-apply 是部署在产品服务器上由受限 sudo 以 root 身份调用的最小特权执行器（对应设计文档 9.1 节）。
// 本程序严禁接受任何可变参数或通配符，仅支持 upgrade、rollback、backup、logs、cleanup 五个固定无参子命令。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"autoship/internal/updateagent/apply"
	"autoship/internal/updateagent/config"
)

// main 作为 autoship-apply 可执行程序入口，校验固定子命令参数并委托 apply.Executor 执行受保护操作。
// 无直接入参（通过 os.Args[1:] 读取唯一子命令）；成功以状态码 0 退出，失败输出错误至 stderr 并以非零退出码退出。
func main() {
	code := runMain(os.Args[1:])
	os.Exit(code)
}

// runMain 执行配置加载、信号监听与固定子命令调度，并返回进程退出码。
// 参数 args 为不含程序名的命令行参数列表；成功返回 0，失败返回钩子退出码或 1。
func runMain(args []string) int {
	if _, err := apply.ValidateArgs(args); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "autoship-apply 参数校验失败: %v\n", err)
		return 2
	}

	cfg, err := config.LoadApplyConfig()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "autoship-apply 加载安全配置失败: %v\n", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	execEngine := apply.NewExecutor(cfg)
	if err := execEngine.Run(ctx, args, os.Stdout, os.Stderr); err != nil {
		var hookErr *apply.HookExecError
		if errors.As(err, &hookErr) && hookErr.ExitCode > 0 {
			_, _ = fmt.Fprintf(os.Stderr, "autoship-apply: %v\n", err)
			return hookErr.ExitCode
		}
		_, _ = fmt.Fprintf(os.Stderr, "autoship-apply: %v\n", err)
		return 1
	}
	return 0
}
