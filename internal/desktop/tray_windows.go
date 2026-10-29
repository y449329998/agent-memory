//go:build windows

package desktop

import (
	"fmt"
	"log"

	"github.com/getlantern/systray"
	"github.com/muyi99/agent-memory/internal/desktop/assets"
)

// trayActions 是托盘需要的动作集合；由 app 注入，托盘本身不碰业务。
type trayActions struct {
	OpenWindow  func()
	Sync        func()
	OpenLog     func()
	AutostartOn func() bool
	ToggleAuto  func(bool) error
	StopAndQuit func()
}

// startTray 在独立 goroutine 里启动托盘，返回一个「已就绪」通道。
// 失败时由调用方降级为「关窗即退出」，不影响主窗口。
func startTray(actions trayActions, logger *log.Logger) (<-chan struct{}, error) {
	ready := make(chan struct{})
	started := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				started <- fmt.Errorf("tray panic: %v", r)
			}
		}()
		systray.Run(func() {
			systray.SetIcon(assets.AppIcon)
			systray.SetTitle("agent-memory")
			systray.SetTooltip("agent-memory 共享记忆")
			open := systray.AddMenuItem("打开界面", "显示共享记忆窗口")
			syncItem := systray.AddMenuItem("立即同步", "扫描各 agent 的记忆文件")
			logItem := systray.AddMenuItem("打开日志", "用记事本打开 app.log")
			auto := systray.AddMenuItemCheckbox("开机自动运行", "登录后在系统托盘常驻", actions.AutostartOn())
			stop := systray.AddMenuItem("停止服务并退出", "停止后台服务并关闭托盘")

			started <- nil
			close(ready)

			for {
				select {
				case <-open.ClickedCh:
					actions.OpenWindow()
				case <-syncItem.ClickedCh:
					actions.Sync()
				case <-logItem.ClickedCh:
					actions.OpenLog()
				case <-auto.ClickedCh:
					want := !auto.Checked()
					if err := actions.ToggleAuto(want); err != nil {
						logger.Printf("切换开机自启失败：%v", err)
						continue
					}
					if want {
						auto.Check()
					} else {
						auto.Uncheck()
					}
				case <-stop.ClickedCh:
					actions.StopAndQuit()
					return
				}
			}
		}, func() {})
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return ready, nil
}
