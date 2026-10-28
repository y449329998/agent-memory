//go:build !windows

package desktop

import (
	"errors"
	"os/exec"
)

// prepareChild 在非 Windows 上不需要隐藏控制台。
func prepareChild(cmd *exec.Cmd) {}

// openWindow 只有 Windows 构建带 webview2；其他平台交给浏览器回退。
func openWindow(link string) error {
	return errors.New("WebView2 窗口仅支持 Windows")
}
