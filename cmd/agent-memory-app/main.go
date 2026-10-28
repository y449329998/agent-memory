// agent-memory-app 是双击即用的桌面入口：正常启动会打开共享记忆窗口；
// 带 --serve 时把同一个 exe 当作 serve 子进程（供 desktop 自己拉起）。
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/muyi99/agent-memory/internal/cli"
	"github.com/muyi99/agent-memory/internal/desktop"
)

func main() {
	// 同一个 exe 充当 serve 子进程：--serve 等价于 `serve` 子命令。
	// cli.Run 只认识子命令名，所以把 --serve 换成 serve，其余参数原样转发。
	if len(os.Args) > 1 && os.Args[1] == "--serve" {
		args := append([]string{"serve"}, os.Args[2:]...)
		os.Exit(cli.Run(args, os.Stdout, os.Stderr))
	}

	home, err := os.UserHomeDir()
	if err != nil {
		desktop.MessageBox("agent-memory", "无法定位用户目录："+err.Error())
		os.Exit(1)
	}
	self, err := os.Executable()
	if err != nil {
		desktop.MessageBox("agent-memory", "无法定位程序自身路径："+err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := desktop.Run(ctx, desktop.Options{UserHome: home, SelfPath: self}); err != nil {
		desktop.MessageBox("agent-memory", err.Error())
		os.Exit(1)
	}
}
