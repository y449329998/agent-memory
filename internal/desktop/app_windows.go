//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"

	webview2 "github.com/jchv/go-webview2"
)

// prepareChild 让 serve 子进程不弹控制台窗口（双击启动的桌面版不应该闪黑框）。
func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// openWindow 用 WebView2 打开共享记忆页面，阻塞到窗口关闭。
// initial 非空时套用上次的位置与大小；关闭时通过 onClose 回传最终位置。
// webview2 的窗口消息循环必须和建窗在同一个 OS 线程上，所以锁定当前线程。
func openWindow(link string, initial *windowState, onClose func(windowState)) (err error) {
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

	w.SetSize(windowWidth, windowHeight, webview2.HintNone)
	w.Navigate(link)

	// 窗口句柄要等 WebView2 真正建窗后才出现；立刻查询必然是 0。
	hwnd := waitForWindow(appTitle, 3*time.Second)
	if initial != nil {
		if hwnd != 0 {
			applyWindowState(hwnd, *initial)
		} else {
			w.SetSize(initial.W, initial.H, webview2.HintNone)
		}
	}

	// 关闭时窗口已被销毁，读不到位置；运行期间每秒记录一次，关窗后写最后一次。
	var (
		mu      sync.Mutex
		last    windowState
		hasLast bool
	)
	stop := make(chan struct{})
	if hwnd != 0 {
		go func() {
			t := time.NewTicker(500 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if st, ok := currentWindowRect(hwnd); ok && st.W > 0 {
						mu.Lock()
						last, hasLast = st, true
						mu.Unlock()
					}
				}
			}
		}()
	}

	w.Run()
	close(stop)

	if onClose != nil {
		mu.Lock()
		st, ok := last, hasLast
		mu.Unlock()
		if ok {
			onClose(st)
		}
	}
	return nil
}

// waitForWindow 轮询等待窗口出现；超时返回 0，调用方退回按尺寸设置。
func waitForWindow(title string, timeout time.Duration) uintptr {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h := findWindow(title); h != 0 {
			return h
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0
}
