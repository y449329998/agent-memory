// agent-memory-app 是双击即用的桌面入口：正常启动打开共享记忆窗口并驻留托盘；
// 带 --serve 时把同一个 exe 当作 serve 子进程；带 --tray-only 时只驻留托盘。
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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--serve":
			args := append([]string{"serve"}, os.Args[2:]...)
			os.Exit(cli.Run(args, os.Stdout, os.Stderr))
		case "--install-shortcuts":
			self, _ := os.Executable()
			if err := desktop.InstallShortcuts(self); err != nil {
				desktop.MessageBox("agent-memory", err.Error())
				os.Exit(1)
			}
			desktop.MessageBox("agent-memory", "已创建桌面与开始菜单快捷方式。")
			os.Exit(0)
		case "--uninstall-shortcuts":
			if err := desktop.UninstallShortcuts(); err != nil {
				desktop.MessageBox("agent-memory", err.Error())
				os.Exit(1)
			}
			os.Exit(0)
		}
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

	trayOnly := len(os.Args) > 1 && os.Args[1] == "--tray-only"
	if err := desktop.Run(ctx, desktop.Options{UserHome: home, SelfPath: self, TrayOnly: trayOnly}); err != nil {
		desktop.MessageBox("agent-memory", err.Error())
		os.Exit(1)
	}
}
