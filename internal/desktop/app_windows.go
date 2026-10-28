//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"syscall"

	webview2 "github.com/jchv/go-webview2"
)

// prepareChild 让 serve 子进程不弹控制台窗口（双击启动的桌面版不应该闪黑框）。
func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// openWindow 用 WebView2 打开共享记忆页面，阻塞到窗口关闭。
// webview2 的窗口消息循环必须和建窗在同一个 OS 线程上，所以锁定当前线程。
func openWindow(link string) (err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("create webview2 window: %v", r)
		}
	}()

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  appTitle,
			Width:  windowWidth,
			Height: windowHeight,
			Center: true,
		},
	})
	if w == nil {
		return errors.New("webview2 窗口创建失败（运行时可能损坏或版本不兼容）")
	}
	defer w.Destroy()

	// 该版本的 WindowOptions 没有 Hint 字段，用 SetSize 显式套用 HintNone。
	w.SetSize(windowWidth, windowHeight, webview2.HintNone)
	w.Navigate(link)
	w.Run()
	return nil
}
