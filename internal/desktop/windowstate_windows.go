//go:build windows

package desktop

import (
	"syscall"
	"unsafe"
)

var (
	user32              = syscall.NewLazyDLL("user32.dll")
	procFindWindowW     = user32.NewProc("FindWindowW")
	procSetForeground   = user32.NewProc("SetForegroundWindow")
	procShowWindow      = user32.NewProc("ShowWindow")
	procMoveWindow      = user32.NewProc("MoveWindow")
	procGetWindowRect   = user32.NewProc("GetWindowRect")
	procIsIconic        = user32.NewProc("IsIconic")
	procGetSystemMetric = user32.NewProc("GetSystemMetrics")
)

type rect struct {
	Left, Top, Right, Bottom int32
}

func findWindow(title string) uintptr {
	t, _ := syscall.UTF16PtrFromString(title)
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	return hwnd
}

// focusExistingWindow 按标题找窗口并提到前台；找不到返回 false。
func focusExistingWindow(title string) bool {
	hwnd := findWindow(title)
	if hwnd == 0 {
		return false
	}
	const swRestore = 9
	procShowWindow.Call(hwnd, swRestore)
	procSetForeground.Call(hwnd)
	return true
}

func screenSize() (int, int) {
	sw, _, _ := procGetSystemMetric.Call(0) // SM_CXSCREEN
	sh, _, _ := procGetSystemMetric.Call(1) // SM_CYSCREEN
	return int(sw), int(sh)
}

// currentWindowRect 读取窗口当前位置；最小化或失败返回零值。
func currentWindowRect(hwnd uintptr) (windowState, bool) {
	if hwnd == 0 {
		return windowState{}, false
	}
	if min, _, _ := procIsIconic.Call(hwnd); min != 0 {
		return windowState{}, false
	}
	var r rect
	if ok, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return windowState{}, false
	}
	return windowState{X: int(r.Left), Y: int(r.Top), W: int(r.Right - r.Left), H: int(r.Bottom - r.Top)}, true
}

func applyWindowState(hwnd uintptr, st windowState) {
	if hwnd == 0 {
		return
	}
	procMoveWindow.Call(hwnd, uintptr(st.X), uintptr(st.Y), uintptr(st.W), uintptr(st.H), 1)
}
