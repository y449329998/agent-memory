//go:build windows

package desktop

// showAppWindow 优先把已存在的窗口提到前台；窗口已关闭才请求新开一个。
func showAppWindow() {
	if focusExistingWindow(appTitle) {
		return
	}
	requestWindow()
}

// closeAppWindow 关闭主窗口，让阻塞在窗口消息循环里的主 goroutine 回到主循环。
func closeAppWindow() {
	if h := findWindow(appTitle); h != 0 {
		procPostMessageW.Call(h, wmClose, 0, 0)
	}
}
