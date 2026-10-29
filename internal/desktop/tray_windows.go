//go:build windows

package desktop

import (
	"log"
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// 托盘自己画：getlantern/systray 把窗口和消息泵放在一个未锁定的 goroutine 里，
// Go 调度器换线程后点击消息就再也到不了托盘窗口（图标在、右键没菜单）。
// 这里自己 LockOSThread + 隐藏窗口 + Shell_NotifyIcon，保证窗口和消息泵同线程。

const (
	wmTrayMsg = 0x8000 + 1 // WM_APP + 1

	wmContextMenu   = 0x007B
	wmRButtonUp     = 0x0205
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmCommand       = 0x0111
	wmClose         = 0x0010
	wmDestroy       = 0x0002
	wmNull          = 0x0000

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004

	nimAdd    = 0x00000000
	nimDelete = 0x00000002

	mfString    = 0x00000000
	mfChecked   = 0x00000008
	mfSeparator = 0x00000800

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
	tpmBottomAlign = 0x0020

	idiApplication = 32512
)

const (
	trayCmdOpen = 1001 + iota
	trayCmdSync
	trayCmdLog
	trayCmdAutostart
	trayCmdStop
)

type trayEvent int

const (
	trayEvOpen trayEvent = iota
	trayEvSync
	trayEvLog
	trayEvToggleAuto
	trayEvStop
)

// trayEvents 缓冲几格：窗口过程只投递不执行，耗时动作由独立 goroutine 处理。
var trayEvents = make(chan trayEvent, 8)

// trayActions 是托盘需要的动作集合；由 app 注入，托盘本身不碰业务。
type trayActions struct {
	OpenWindow  func()
	Sync        func()
	OpenLog     func()
	AutostartOn func() bool
	ToggleAuto  func(bool) error
	StopAndQuit func()
}

var (
	procRegisterClassW    = user32.NewProc("RegisterClassW")
	procCreateWindowExW   = user32.NewProc("CreateWindowExW")
	procDefWindowProcW    = user32.NewProc("DefWindowProcW")
	procGetMessageW       = user32.NewProc("GetMessageW")
	procTranslateMessage  = user32.NewProc("TranslateMessage")
	procDispatchMessageW  = user32.NewProc("DispatchMessageW")
	procPostQuitMessage   = user32.NewProc("PostQuitMessage")
	procDestroyWindow     = user32.NewProc("DestroyWindow")
	procPostMessageW      = user32.NewProc("PostMessageW")
	procCreatePopupMenu   = user32.NewProc("CreatePopupMenu")
	procAppendMenuW       = user32.NewProc("AppendMenuW")
	procDestroyMenu       = user32.NewProc("DestroyMenu")
	procTrackPopupMenu    = user32.NewProc("TrackPopupMenu")
	procGetCursorPos      = user32.NewProc("GetCursorPos")
	procRegisterWindowMsg = user32.NewProc("RegisterWindowMessageW")
	procLoadIconW         = user32.NewProc("LoadIconW")
	procDestroyIcon       = user32.NewProc("DestroyIcon")
	procGetModuleHandleW  = kernel32.NewProc("GetModuleHandleW")

	shell32              = syscall.NewLazyDLL("shell32.dll")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procExtractIconExW   = shell32.NewProc("ExtractIconExW")
)

type wndClass struct {
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
}

type point struct {
	X, Y int32
}

type winMsg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

// notifyIconData 对应 NOTIFYICONDATAW（V3/V4 布局，x64 下 976 字节）。
type notifyIconData struct {
	cbSize           uint32
	Hwnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type trayState struct {
	hwnd uintptr
	icon uintptr
}

var (
	tray            trayState
	taskbarCreated  uint32
	trayWndProcAddr = syscall.NewCallback(trayWndProc)
)

// startTray 起一个锁定 OS 线程的托盘消息泵，返回「就绪」与「已退出」通道。
func startTray(actions trayActions, logger *log.Logger) (<-chan struct{}, <-chan struct{}, error) {
	ready := make(chan struct{})
	done := make(chan struct{})
	started := make(chan error, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)

		if err := createTrayWindow(); err != nil {
			started <- err
			return
		}
		started <- nil
		close(ready)

		go runTrayActions(actions, logger)
		trayMessageLoop(logger)
	}()

	if err := <-started; err != nil {
		return nil, nil, err
	}
	return ready, done, nil
}

func createTrayWindow() error {
	hinst, _, _ := procGetModuleHandleW.Call(0)
	className, _ := syscall.UTF16PtrFromString("AgentMemoryTrayWindow")
	wc := wndClass{
		WndProc:   trayWndProcAddr,
		Instance:  hinst,
		ClassName: className,
	}
	if atom, _, err := procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return err
	}
	title, _ := syscall.UTF16PtrFromString("agent-memory tray")
	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		0, 0, 0, 0, 0,
		0, 0, hinst, 0,
	)
	if hwnd == 0 {
		return err
	}
	tray.hwnd = hwnd
	if !addTrayIcon(hwnd) {
		return syscall.EINVAL
	}
	name, _ := syscall.UTF16PtrFromString("TaskbarCreated")
	id, _, _ := procRegisterWindowMsg.Call(uintptr(unsafe.Pointer(name)))
	taskbarCreated = uint32(id)
	return nil
}

func trayMessageLoop(logger *log.Logger) {
	var m winMsg
	for {
		ret, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			if logger != nil {
				logger.Printf("托盘消息循环出错：%v", err)
			}
			return
		case 0:
			return
		default:
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}
}

func runTrayActions(actions trayActions, logger *log.Logger) {
	for ev := range trayEvents {
		switch ev {
		case trayEvOpen:
			actions.OpenWindow()
		case trayEvSync:
			actions.Sync()
		case trayEvLog:
			actions.OpenLog()
		case trayEvToggleAuto:
			want := !actions.AutostartOn()
			if err := actions.ToggleAuto(want); err != nil {
				if logger != nil {
					logger.Printf("切换开机自启失败：%v", err)
				}
			}
		case trayEvStop:
			// 先关窗口再摘图标，最后停服务：否则窗口还开着时主线程卡在 WebView 消息循环里，
			// quit 只能等用户手动关窗才会被处理。
			closeAppWindow()
			removeTrayIcon()
			actions.StopAndQuit()
			return
		}
	}
}

func postTrayEvent(ev trayEvent) {
	select {
	case trayEvents <- ev:
	default:
	}
}

func trayMenuEvent(id int) (trayEvent, bool) {
	switch id {
	case trayCmdOpen:
		return trayEvOpen, true
	case trayCmdSync:
		return trayEvSync, true
	case trayCmdLog:
		return trayEvLog, true
	case trayCmdAutostart:
		return trayEvToggleAuto, true
	case trayCmdStop:
		return trayEvStop, true
	}
	return 0, false
}

func trayWndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch uint32(msg) {
	case wmTrayMsg:
		switch uint32(lParam) & 0xffff {
		case wmRButtonUp, wmContextMenu:
			showTrayMenu(hwnd)
		case wmLButtonUp, wmLButtonDblClk:
			postTrayEvent(trayEvOpen)
		}
		return 0
	case wmCommand:
		if ev, ok := trayMenuEvent(int(wParam & 0xffff)); ok {
			postTrayEvent(ev)
		}
		return 0
	case taskbarCreated:
		addTrayIcon(hwnd)
		return 0
	case wmClose:
		removeTrayIcon()
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		destroyTrayIcon()
		procPostQuitMessage.Call(0)
		return 0
	}
	res, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return res
}

func addTrayIcon(hwnd uintptr) bool {
	nid := notifyIconData{
		Hwnd:             hwnd,
		UID:              1,
		UFlags:           nifMessage | nifIcon | nifTip,
		UCallbackMessage: wmTrayMsg,
		HIcon:            tray.iconHandle(),
	}
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	copy(nid.SzTip[:], syscall.StringToUTF16("agent-memory 共享记忆"))
	ok, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
	return ok != 0
}

func removeTrayIcon() {
	if tray.hwnd == 0 {
		return
	}
	nid := notifyIconData{Hwnd: tray.hwnd, UID: 1}
	nid.cbSize = uint32(unsafe.Sizeof(nid))
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

// iconHandle 优先从自身 exe 取图标（与资源管理器/快捷方式一致），失败用系统默认图标。
func (t *trayState) iconHandle() uintptr {
	if t.icon != 0 {
		return t.icon
	}
	if exe, err := os.Executable(); err == nil {
		path, _ := syscall.UTF16PtrFromString(exe)
		var small uintptr
		if n, _, _ := procExtractIconExW.Call(uintptr(unsafe.Pointer(path)), 0, 0, uintptr(unsafe.Pointer(&small)), 1); n > 0 && small != 0 {
			t.icon = small
		}
	}
	if t.icon == 0 {
		t.icon, _, _ = procLoadIconW.Call(0, idiApplication)
	}
	return t.icon
}

func showTrayMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)

	appendItem := func(id int, text string, checked bool) {
		flags := uintptr(mfString)
		if checked {
			flags |= mfChecked
		}
		p, _ := syscall.UTF16PtrFromString(text)
		procAppendMenuW.Call(menu, flags, uintptr(id), uintptr(unsafe.Pointer(p)))
	}
	appendItem(trayCmdOpen, "打开界面", false)
	appendItem(trayCmdSync, "立即同步", false)
	appendItem(trayCmdLog, "打开日志", false)
	on, _ := autostartEnabled()
	appendItem(trayCmdAutostart, "开机自动运行", on)
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
	appendItem(trayCmdStop, "停止服务并退出", false)

	var pt point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// 不先抢前台，点菜单外部不会自动消失。
	procSetForeground.Call(hwnd)
	procTrackPopupMenu.Call(menu, tpmLeftAlign|tpmBottomAlign|tpmRightButton,
		uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	procPostMessageW.Call(hwnd, wmNull, 0, 0)
}

// 保持 DestroyIcon 被引用：托盘退出时是否需要释放由系统统一回收，
// 这里显式释放 exe 提取的小图标，避免每次重启泄漏一个 HICON。
func destroyTrayIcon() {
	if tray.icon != 0 {
		procDestroyIcon.Call(tray.icon)
		tray.icon = 0
	}
}
