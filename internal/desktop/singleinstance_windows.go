//go:build windows

package desktop

import (
	"errors"
	"syscall"
	"unsafe"
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	procCreateMu = kernel32.NewProc("CreateMutexW")
	procCloseH   = kernel32.NewProc("CloseHandle")
	procCreateEv = kernel32.NewProc("CreateEventW")
	procOpenEv   = kernel32.NewProc("OpenEventW")
	procSetEv    = kernel32.NewProc("SetEvent")
	procWaitEv   = kernel32.NewProc("WaitForSingleObject")
	muHandle     uintptr
	errAlready   = errors.New("agent-memory 已在运行")
)

const (
	showEventName     = `Local\agent-memory-desktop-show`
	eventModifyState  = 0x0002
	waitInfinite      = 0xFFFFFFFF
	waitObjectSuccess = 0
)

// acquireSingleInstance 用命名互斥保证一次登录只运行一个桌面进程。
func acquireSingleInstance() error {
	name, _ := syscall.UTF16PtrFromString(`Local\agent-memory-desktop`)
	h, _, err := procCreateMu.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return err
	}
	if errno, ok := err.(syscall.Errno); ok && errno == syscall.ERROR_ALREADY_EXISTS {
		procCloseH.Call(h)
		return errAlready
	}
	muHandle = h
	return nil
}

func releaseSingleInstance() {
	if muHandle != 0 {
		procCloseH.Call(muHandle)
		muHandle = 0
	}
}

// startShowListener 监听第二个实例的唤起信号；窗口已关也能重新打开。
func startShowListener() {
	name, _ := syscall.UTF16PtrFromString(showEventName)
	h, _, _ := procCreateEv.Call(0, 0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return
	}
	go func() {
		for {
			if r, _, _ := procWaitEv.Call(h, waitInfinite); r != waitObjectSuccess {
				return
			}
			showAppWindow()
		}
	}()
}

// signalShowWindow 通知已在运行的实例显示窗口；老版本没有该事件时返回 false。
func signalShowWindow() bool {
	name, _ := syscall.UTF16PtrFromString(showEventName)
	h, _, _ := procOpenEv.Call(eventModifyState, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return false
	}
	defer procCloseH.Call(h)
	r, _, _ := procSetEv.Call(h)
	return r != 0
}
