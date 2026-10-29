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
	muHandle     uintptr
	errAlready   = errors.New("agent-memory 已在运行")
)

// acquireSingleInstance 用命名互斥保证一次登录只运行一个桌面进程。
func acquireSingleInstance() error {
	name, _ := syscall.UTF16PtrFromString(`Global\agent-memory-desktop`)
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
