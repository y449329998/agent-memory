//go:build windows

package desktop

import (
	"strings"
	"syscall"
	"unsafe"
)

var (
	user32DLL       = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32DLL.NewProc("MessageBoxW")
)

const (
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
)

// MessageBox 弹出模态错误框。纯 syscall 调 user32，不依赖 cgo。
// 失败时静默返回：桌面入口已经没有更合适的报错通道了。
func MessageBox(title, text string) {
	title = strings.ReplaceAll(title, "\x00", "")
	text = strings.ReplaceAll(text, "\x00", "")
	titlePtr, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	textPtr, err := syscall.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procMessageBoxW.Call(
		0,
		uintptr(unsafe.Pointer(textPtr)),
		uintptr(unsafe.Pointer(titlePtr)),
		uintptr(mbOK|mbIconError|mbSetForeground),
	)
}
