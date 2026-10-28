# 桌面体验优化 · 阶段二（托盘、自启、快捷方式、窗口状态）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让桌面版具备「关窗进托盘、托盘可开窗/同步/停止、开机自启、桌面与开始菜单快捷方式、记住窗口大小、只允许一个实例、有应用图标」，并解决升级时 exe 被占用的问题。

**Architecture:** 托盘与窗口都在 `agent-memory-app.exe` 进程内：托盘跑在独立 goroutine，窗口在主 goroutine；关窗后进程驻留托盘，托盘「打开界面」重建窗口，「停止服务并退出」先调 `POST /v1/shutdown` 再退出。Windows 专属能力用 `syscall` 调 `kernel32/user32/shell32`，不引入 CGO；托盘用 `getlantern/systray`（已实测 `CGO_ENABLED=0` 可编译）。

**Tech Stack:** Go 1.22.6、`github.com/getlantern/systray`、`golang.org/x/sys/windows/registry`、`github.com/jchv/go-webview2`、`syscall`。

**上游 spec:** `docs/superpowers/specs/2026-10-28-desktop-ux-design.md`（第 5 节、6、8 验收 4–7、10）。阶段一必须先完成（页面与三个接口）。

---

## 开工约定

- 分支从阶段一合并后的 `master` 新建 `feat/desktop-ux-2`。
- Windows + `core.autocrlf=true`：格式检查用 `git show :<path> | gofmt -d`，不要用 `gofmt -l .`。
- 本阶段会**真实创建**快捷方式、写 `HKCU` 注册表、显示托盘图标：每项都要在临时 `USERPROFILE` 下先测，再用真实环境验收一次，验收后按需保留或撤销。
- 非 Windows 平台：新文件用构建标签隔离，`desktop.Run` 保持现有「开窗即用、关窗退出」的行为。

---

## 文件结构

```
internal/desktop/app.go                重构 Run：单实例、托盘、窗口循环、窗口状态
internal/desktop/tray_windows.go       托盘实现（菜单、图标、动作）
internal/desktop/tray_other.go         非 Windows 空实现
internal/desktop/singleinstance_windows.go  命名互斥 + 已有窗口前台
internal/desktop/singleinstance_other.go
internal/desktop/windowstate.go        窗口位置大小的读写与校验（平台无关）
internal/desktop/windowstate_windows.go    通过 Win32 读取/套用窗口位置
internal/desktop/shortcuts_windows.go  桌面/开始菜单快捷方式
internal/desktop/autostart_windows.go  HKCU Run 读写
internal/desktop/autostart_other.go    501/不支持
internal/desktop/assets/app.ico        图标（生成物，提交）
internal/desktop/assets/icon.go        go:embed 图标字节
tools/mkicon/main.go                   图标生成器（PNG→ICO）
cmd/agent-memory-app/main.go           参数：--tray-only / --install-shortcuts / --uninstall-shortcuts
internal/daemon/web.go                 + POST /v1/shutdown、GET/POST /v1/desktop-settings
internal/daemon/server.go              Config 增加 ShutdownFunc、AppPath；注册路由
internal/cli/serve.go                  把 shutdown 回调与 app 路径传给 daemon
internal/daemon/index.html             增加「设置：开机时自动运行」勾选
```

---

## Task 1: 图标生成与应用图标

**Files:**
- Create: `tools/mkicon/main.go`
- Create: `internal/desktop/assets/icon.go`
- Generated: `internal/desktop/assets/app.ico`、`cmd/agent-memory-app/rsrc.syso`

- [ ] **Step 1: 写图标生成器**

```go
// tools/mkicon 生成 256x256 的应用图标：深色圆角方块 + 两个环形箭头。
// 只依赖标准库，输出 PNG 与仅含一个 PNG 条目的 ICO。
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

func main() {
	out := "internal/desktop/assets"
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	img := render(256)
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "app.png"), pngBuf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	ico := wrapICO(pngBuf.Bytes(), 256)
	if err := os.WriteFile(filepath.Join(out, "app.ico"), ico, 0o644); err != nil {
		log.Fatal(err)
	}
	log.Println("wrote app.png / app.ico")
}

func render(size int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{24, 24, 27, 255}      // #18181b
	fg := color.RGBA{129, 140, 248, 255}   // #818cf8
	accent := color.RGBA{79, 70, 229, 255} // #4f46e5
	cx, cy := float64(size)/2, float64(size)/2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			r := math.Hypot(dx, dy)
			// 圆角方块底
			corner := 56.0
			if insideRounded(x, y, size, corner) {
				img.Set(x, y, bg)
			}
			// 两个环形箭头：半径 78 与 52，各留 90° 缺口
			if (r > 74 && r < 86) || (r > 48 && r < 60) {
				ang := math.Atan2(dy, dx)
				gap := math.Mod(ang+math.Pi*2, math.Pi*2)
				if gap > math.Pi/2 && gap < math.Pi {
					img.Set(x, y, fg)
				}
			}
			// 箭头三角（简单两笔）
			if (x > 150 && x < 190 && y > 60 && y < 90) || (x > 66 && x < 106 && y > 170 && y < 200) {
				img.Set(x, y, accent)
			}
		}
	}
	return img
}

func insideRounded(x, y, size int, r float64) bool {
	c := float64(size) - r
	fx, fy := float64(x), float64(y)
	if fx < r && fy < r {
		return math.Hypot(r-fx, r-fy) <= r
	}
	if fx > c && fy < r {
		return math.Hypot(fx-c, r-fy) <= r
	}
	if fx < r && fy > c {
		return math.Hypot(r-fx, fy-c) <= r
	}
	if fx > c && fy > c {
		return math.Hypot(fx-c, fy-c) <= r
	}
	return true
}

// wrapICO 组装单条目 ICO（PNG 负载，Vista+ 支持）。
func wrapICO(pngData []byte, size int) []byte {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, uint16(0))    // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))    // type icon
	binary.Write(&buf, binary.LittleEndian, uint16(1))    // count
	buf.WriteByte(0)                                      // width 256
	buf.WriteByte(0)                                      // height 256
	buf.WriteByte(0)                                      // palette
	buf.WriteByte(0)                                      // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))    // planes
	binary.Write(&buf, binary.LittleEndian, uint16(32))   // bpp
	binary.Write(&buf, binary.LittleEndian, uint32(len(pngData)))
	binary.Write(&buf, binary.LittleEndian, uint32(6+16)) // offset
	buf.Write(pngData)
	return buf.Bytes()
}
```

- [ ] **Step 2: 生成图标**

Run: `go run ./tools/mkicon`
Expected: `internal/desktop/assets/app.png` 与 `app.ico` 生成（ICO 约 1–3KB）

- [ ] **Step 3: 嵌入图标供托盘使用**

```go
// internal/desktop/assets/icon.go
package assets

import _ "embed"

// AppIcon 是托盘与快捷方式使用的 ICO 字节。
//
//go:embed app.ico
var AppIcon []byte
```

- [ ] **Step 4: 生成 exe 图标资源**

```bash
go install github.com/akavel/rsrc@latest
"$(go env GOPATH)/bin/rsrc" -ico internal/desktop/assets/app.ico -o cmd/agent-memory-app/rsrc.syso
```

Expected: 生成 `cmd/agent-memory-app/rsrc.syso`；`go build -o agent-memory-app.exe ./cmd/agent-memory-app` 后资源管理器中 exe 显示新图标。

- [ ] **Step 5: 构建验证并提交**

Run: `go build ./... && go test ./... -count=1`
Expected: PASS

```bash
git add tools/mkicon internal/desktop/assets cmd/agent-memory-app/rsrc.syso
git commit -m "feat: 生成并嵌入应用图标"
```

---

## Task 2: 托盘与关窗驻留

**Files:**
- Create: `internal/desktop/tray_windows.go`、`tray_other.go`
- Modify: `internal/desktop/app.go`
- Test: `internal/desktop/app_test.go`（追加，非 GUI 路径）

- [ ] **Step 1: 定义托盘动作接口（便于测试）**

`internal/desktop/tray_windows.go`：

```go
//go:build windows

package desktop

import (
	"fmt"
	"log"
	"os/exec"

	"github.com/getlantern/systray"
	"github.com/muyi99/agent-memory/internal/desktop/assets"
)

// trayActions 是托盘需要的动作集合；由 app 注入，托盘本身不碰业务。
type trayActions struct {
	OpenWindow   func()
	Sync         func()
	OpenLog      func()
	AutostartOn  func() bool   // 当前是否开启
	ToggleAuto   func(bool) error
	StopAndQuit  func()
}

// startTray 在独立 goroutine 里启动托盘，返回一个「已就绪」通道。
// 失败时返回错误由调用方降级为「关窗即退出」。
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
			auto := systray.AddMenuItemCheckbox("开机自动运行", "登录后驻留托盘", actions.AutostartOn())
			stop := systray.AddMenuItem("停止服务并退出", "停止后台服务并关闭托盘")
			close(ready)
			started <- nil

			go func() {
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
			}()
		}, func() {})
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return ready, nil
}
```

`tray_other.go`：

```go
//go:build !windows

package desktop

import (
	"errors"
	"log"
)

type trayActions struct {
	OpenWindow  func()
	Sync        func()
	OpenLog     func()
	AutostartOn func() bool
	ToggleAuto  func(bool) error
	StopAndQuit func()
}

func startTray(trayActions, *log.Logger) (<-chan struct{}, error) {
	return nil, errors.New("当前平台不支持系统托盘")
}
```

- [ ] **Step 2: 重构 `Run` 支持关窗驻留**

在 `app.go` 顶部把 `Run` 拆成：`Run`（单实例 + 托盘 + 窗口循环）与 `openWindowOnce`（原开窗逻辑）。核心循环：

```go
// Run 启动桌面应用：确保服务 → 托盘 → 主窗口（TrayOnly 时不开窗）。
// 关窗后进程驻留托盘；托盘「停止服务并退出」才结束进程。
func Run(ctx context.Context, opts Options) error {
	if err := acquireSingleInstance(); err != nil {
		if errors.Is(err, errAlready) {
			focusExistingWindow(appTitle)
			MessageBox("agent-memory", "agent-memory 已在运行（见系统托盘）。")
			return nil
		}
		return err
	}
	defer releaseSingleInstance()

	cfg, logger, closeLog, err := bootConfig(opts)
	if err != nil {
		return err
	}
	defer closeLog()
	_ = writeDesktopFile(cfg.DataDir, opts.SelfPath)

	port, err := ensureServe(ctx, opts.SelfPath, cfg, logger)
	if err != nil {
		return err
	}
	token, err := ensureToken(cfg)
	if err != nil {
		return err
	}
	link := buildURL(port, token)

	quit := make(chan struct{}, 1)
	actions := trayActions{
		OpenWindow:  func() { requestWindow() },
		Sync:        func() { _ = postJSON(port, token, "/v1/scan") },
		OpenLog:     func() { _ = exec.Command("notepad", cfg.AppLogPath()).Start() },
		AutostartOn: func() bool { on, _ := autostartEnabled(); return on },
		ToggleAuto:  func(v bool) error { return setAutostart(v, opts.SelfPath) },
		StopAndQuit: func() {
			_ = postJSON(port, token, "/v1/shutdown")
			select {
			case quit <- struct{}{}:
			default:
			}
		},
	}
	if _, err := startTray(actions, logger); err != nil {
		logger.Printf("托盘启动失败，降级为关窗即退出：%v", err)
		if !opts.TrayOnly {
			openWindowOnce(link, cfg, logger)
		}
		return nil
	}

	if !opts.TrayOnly {
		openWindowOnce(link, cfg, logger)
	}
	for {
		select {
		case <-quit:
			return nil
		case <-takeWindowRequest():
			openWindowOnce(link, cfg, logger)
		case <-ctx.Done():
			return nil
		}
	}
}
```

`Options` 增加字段：

```go
type Options struct {
	UserHome string
	SelfPath string
	Logger   *log.Logger
	// TrayOnly 为 true 时只驻留托盘、不打开窗口（开机自启用）。
	TrayOnly bool
}
```

`ensureServe` 签名（从原 `Run` 抽出）：

```go
// ensureServe 复用或拉起 serve，返回实际端口。子进程不随窗口退出。
func ensureServe(ctx context.Context, selfPath string, cfg config.Config, logger *log.Logger) (int, error) {
	if port, ok := findRunningDaemon(cfg); ok {
		logger.Printf("复用已在运行的 serve：端口 %d", port)
		return port, nil
	}
	_, port, err := startServe(ctx, selfPath, cfg)
	if err != nil {
		logger.Printf("启动 serve 失败：%v", err)
		return 0, fmt.Errorf("启动共享记忆服务失败（端口 %d 可能被占用）：%w", preferredPort, err)
	}
	logger.Printf("已启动内置 serve：端口 %d（关窗后继续运行）", port)
	return port, nil
}
```

窗口请求用包级容量为 1 的 channel，避免托盘连续点击压出多个窗口：

```go
var windowReq = make(chan struct{}, 1)

func requestWindow() {
	select {
	case windowReq <- struct{}{}:
	default:
	}
}
func takeWindowRequest() <-chan struct{} { return windowReq }
func resetWindowRequests() {
	for {
		select {
		case <-windowReq:
		default:
			return
		}
	}
}
```

`openWindowOnce(link, cfg, logger)` 内部：按窗口状态打开、阻塞到关闭、关闭后保存位置（Task 3 补）。首次窗口在托盘就绪后由 `Run` 直接打开；之后只由托盘请求触发。

说明：
- 由单实例互斥保证只有一个进程，因此 `windowReq` 不会跨进程冲突。
- `postJSON` 用 Bearer 调对应端点；`/v1/shutdown` 在 Task 5 实现。
- `cmd/agent-memory-app/main.go` 增加 `--tray-only` 解析，传给 `Options.TrayOnly`。

- [ ] **Step 3: 非 GUI 单测**

`app_test.go` 追加：

```go
func TestWindowRequestChannelBuffersOne(t *testing.T) {
	resetWindowRequests()
	requestWindow()
	requestWindow() // 第二次不应阻塞
	select {
	case <-takeWindowRequest():
	default:
		t.Fatal("want buffered request")
	}
	select {
	case <-takeWindowRequest():
		t.Fatal("only one request should be buffered")
	default:
	}
}
```

（在 `app.go` 中实现 `resetWindowRequests`/`requestWindow`/`takeWindowRequest` 三个小函数，包级 channel 容量 1。）

- [ ] **Step 4: 构建与测试**

Run: `go get github.com/getlantern/systray@latest && go build ./... && go test ./... -count=1`
Expected: PASS；`go vet ./...` 无输出

- [ ] **Step 5: 手工验证（窗口与托盘）**

1. 启动 `agent-memory-app.exe`，确认窗口与托盘图标同时出现；
2. 关闭窗口 → 进程仍在（任务管理器），托盘菜单「打开界面」能重新出窗；
3. 托盘「立即同步」→ 打开窗口看统计是否更新；
4. 托盘「停止服务并退出」→ 进程消失、`netstat` 无 47123 监听。

- [ ] **Step 6: 提交**

```bash
git add internal/desktop internal/desktop/assets go.mod go.sum
git commit -m "feat: 桌面托盘与关窗驻留，可重开窗口与停止服务"
```

---

## Task 3: 单实例与窗口状态记忆

**Files:**
- Create: `internal/desktop/singleinstance_windows.go`、`singleinstance_other.go`
- Create: `internal/desktop/windowstate.go`、`windowstate_windows.go`
- Test: `internal/desktop/windowstate_test.go`

- [ ] **Step 1: 单实例（Windows 命名互斥）**

```go
//go:build windows

package desktop

import (
	"errors"

	"golang.org/x/sys/windows"
)

var (
	muHandle    windows.Handle
	errAlready  = errors.New("already running")
)

func acquireSingleInstance() error {
	name, _ := windows.UTF16PtrFromString(`Global\agent-memory-desktop`)
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return errAlready
		}
		return err
	}
	if h == 0 {
		return errAlready
	}
	muHandle = h
	return nil
}

func releaseSingleInstance() {
	if muHandle != 0 {
		windows.CloseHandle(muHandle)
		muHandle = 0
	}
}
```

注意：`CreateMutex` 在已存在时返回有效句柄且 `GetLastError` 为 `ERROR_ALREADY_EXISTS`；`golang.org/x/sys/windows` 的封装会把它变成错误。实现时以「返回 error 且错误信息包含 already exists」判定，并在测试里用两次调用验证。非 Windows 版本返回 nil（不做限制）。

```go
//go:build !windows

package desktop

var errAlready error

func acquireSingleInstance() error { return nil }
func releaseSingleInstance()       {}
```

- [ ] **Step 2: 把已有窗口提到前台**

```go
//go:build windows

package desktop

import (
	"syscall"
	"unsafe"
)

var (
	user32           = syscall.NewLazyDLL("user32.dll")
	procFindWindowW  = user32.NewProc("FindWindowW")
	procSetForeground = user32.NewProc("SetForegroundWindow")
	procShowWindow   = user32.NewProc("ShowWindow")
)

// focusExistingWindow 按标题找窗口并提到前台；找不到返回 false。
func focusExistingWindow(title string) bool {
	t, _ := syscall.UTF16PtrFromString(title)
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return false
	}
	const swRestore = 9
	procShowWindow.Call(hwnd, swRestore)
	procSetForeground.Call(hwnd)
	return true
}
```

- [ ] **Step 3: 窗口状态（可测的纯逻辑 + Win32 套用）**

```go
// internal/desktop/windowstate.go（平台无关）
package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type windowState struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func windowStatePath(dataDir string) string { return filepath.Join(dataDir, "window.json") }

func loadWindowState(dataDir string) (windowState, bool) {
	data, err := os.ReadFile(windowStatePath(dataDir))
	if err != nil {
		return windowState{}, false
	}
	var st windowState
	if err := json.Unmarshal(data, &st); err != nil {
		return windowState{}, false
	}
	return st, true
}

func saveWindowState(dataDir string, st windowState) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(windowStatePath(dataDir), data, 0o644)
}

// clampWindowState 把窗口限制在当前工作区内；越界则回到默认大小并居中。
func clampWindowState(st windowState, screenW, screenH, defaultW, defaultH int) windowState {
	if st.W < 800 || st.H < 600 || st.X < -screenW || st.Y < -screenH ||
		st.X > screenW-200 || st.Y > screenH-200 || st.W > screenW*2 || st.H > screenH*2 {
		return windowState{W: defaultW, H: defaultH, X: (screenW - defaultW) / 2, Y: (screenH - defaultH) / 2}
	}
	return st
}
```

```go
// internal/desktop/windowstate_windows.go
//go:build windows

package desktop

import (
	"syscall"
	"unsafe"
)

var procGetSystemMetrics = user32.NewProc("GetSystemMetrics")

func screenSize() (int, int) {
	sw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	return int(sw), int(sh)
}

// applyWindowState 用 Win32 把窗口移动/缩放到指定位置。
func applyWindowState(hwnd uintptr, st windowState) {
	if hwnd == 0 {
		return
	}
	procMoveWindow.Call(hwnd, uintptr(st.X), uintptr(st.Y), uintptr(st.W), uintptr(st.H), 1)
}
```

`MoveWindow` 需要新增 `procMoveWindow = user32.NewProc("MoveWindow")`；`openWindow` 创建窗口后按标题 `FindWindowW` 取句柄，读旧状态并 `applyWindowState`；窗口关闭后 `GetWindowRect` 取最终位置写回 `window.json`。

- [ ] **Step 4: 单测（窗口状态纯逻辑）**

```go
package desktop

import (
	"path/filepath"
	"testing"
)

func TestWindowStateRoundTripAndClamp(t *testing.T) {
	dir := t.TempDir()
	if _, ok := loadWindowState(dir); ok {
		t.Fatal("empty dir should have no state")
	}
	if err := saveWindowState(dir, windowState{X: 100, Y: 80, W: 1000, H: 700}); err != nil {
		t.Fatal(err)
	}
	st, ok := loadWindowState(dir)
	if !ok || st.W != 1000 || st.X != 100 {
		t.Fatalf("roundtrip: %+v ok=%v", st, ok)
	}

	off := clampWindowState(windowState{X: -5000, Y: -5000, W: 1200, H: 820}, 1920, 1080, 1200, 820)
	if off.X != 360 || off.Y != 130 || off.W != 1200 {
		t.Fatalf("off-screen clamp: %+v", off)
	}
	small := clampWindowState(windowState{X: 10, Y: 10, W: 300, H: 200}, 1920, 1080, 1200, 820)
	if small.W != 1200 || small.H != 820 {
		t.Fatalf("too-small clamp: %+v", small)
	}
	keep := clampWindowState(windowState{X: 50, Y: 40, W: 1100, H: 760}, 1920, 1080, 1200, 820)
	if keep.X != 50 || keep.W != 1100 {
		t.Fatalf("valid state should be kept: %+v", keep)
	}
	_ = filepath.Join
}
```

- [ ] **Step 5: 验证**

Run: `go test ./internal/desktop/ -count=1 && go build ./...`
Expected: PASS

手工：调整窗口大小/位置 → 关闭 → 从托盘重开 → 位置恢复；把 `window.json` 改成负数坐标 → 重开居中。

- [ ] **Step 6: 提交**

```bash
git add internal/desktop
git commit -m "feat: 单实例与窗口位置记忆"
```

---

## Task 4: 桌面与开始菜单快捷方式

**Files:**
- Create: `internal/desktop/shortcuts_windows.go`、`shortcuts_other.go`
- Modify: `cmd/agent-memory-app/main.go`

- [ ] **Step 1: 实现（PowerShell WScript.Shell，无需依赖）**

```go
//go:build windows

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// InstallShortcuts 在桌面与开始菜单创建 agent-memory 快捷方式。
func InstallShortcuts(exePath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	desktop := filepath.Join(home, "Desktop", "agent-memory.lnk")
	startMenu := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "agent-memory.lnk")
	for _, lnk := range []string{desktop, startMenu} {
		if err := createShortcut(lnk, exePath); err != nil {
			return fmt.Errorf("创建 %s 失败：%w", lnk, err)
		}
	}
	return nil
}

func UninstallShortcuts() error {
	home, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(home, "Desktop", "agent-memory.lnk"),
		filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "agent-memory.lnk"),
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除 %s 失败：%w", p, err)
		}
	}
	return nil
}

func createShortcut(lnk, target string) error {
	script := fmt.Sprintf(
		`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='%s';$s.WorkingDirectory='%s';$s.IconLocation='%s,0';$s.Save()`,
		strings.ReplaceAll(lnk, `'`, `''`), strings.ReplaceAll(target, `'`, `''`),
		strings.ReplaceAll(filepath.Dir(target), `'`, `''`), strings.ReplaceAll(target, `'`, `''`))
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
```

import 补 `"strings"`；非 Windows 版本两个函数返回 `errors.New("当前平台不支持快捷方式")`。

- [ ] **Step 2: 接入参数**

`cmd/agent-memory-app/main.go` 增加：

```go
switch args[0] {
case "--serve":
	os.Exit(cli.Run(args[1:], os.Stdout, os.Stderr))
case "--install-shortcuts":
	self, _ := os.Executable()
	if err := desktop.InstallShortcuts(self); err != nil {
		desktop.MessageBox("agent-memory", err.Error())
		os.Exit(1)
	}
	desktop.MessageBox("agent-memory", "已创建桌面与开始菜单快捷方式。")
	os.Exit(0)
case "--uninstall-shortcuts":
	if err := desktop.UninstallShortcuts(); err != nil {
		desktop.MessageBox("agent-memory", err.Error())
		os.Exit(1)
	}
	os.Exit(0)
}
```

- [ ] **Step 3: 验证（临时 HOME 优先）**

1. 设 `USERPROFILE`/`APPDATA` 指向临时目录，运行 `agent-memory-app.exe --install-shortcuts`，检查两个 `.lnk` 存在；
2. 再跑 `--uninstall-shortcuts`，确认删除；
3. 用真实环境执行一次安装，双击桌面快捷方式能开窗；图标显示正常。

- [ ] **Step 4: 提交**

```bash
git add internal/desktop/shortcuts_*.go cmd/agent-memory-app/main.go
git commit -m "feat: 桌面与开始菜单快捷方式"
```

---

## Task 5: 开机自启（注册表 + 端点 + 界面开关）与停止服务

**Files:**
- Create: `internal/desktop/autostart_windows.go`、`autostart_other.go`、`desktopfile.go`
- Modify: `internal/daemon/server.go`、`web.go`、`internal/cli/serve.go`
- Modify: `internal/daemon/index.html`
- Test: `internal/daemon/web_test.go`（追加）

- [ ] **Step 1: 注册表读写**

```go
//go:build windows

package desktop

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func setAutostart(enabled bool, exePath string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开注册表失败：%w", err)
	}
	defer k.Close()
	if !enabled {
		if err := k.DeleteValue("agent-memory"); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("删除自启失败：%w", err)
		}
		return nil
	}
	value := fmt.Sprintf(`"%s" --tray-only`, exePath)
	if err := k.SetStringValue("agent-memory", value); err != nil {
		return fmt.Errorf("写入自启失败：%w", err)
	}
	return nil
}

func autostartEnabled() (bool, string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("agent-memory")
	if err != nil {
		return false, ""
	}
	return v != "", v
}
```

非 Windows：`setAutostart` 返回 `errors.New("当前平台不支持开机自启")`，`autostartEnabled` 返回 `false, ""`。

- [ ] **Step 2: 桌面应用写出自身路径**

```go
// internal/desktop/desktopfile.go
package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type desktopFile struct {
	AppPath string `json:"app_path"`
}

func desktopFilePath(dataDir string) string { return filepath.Join(dataDir, "desktop.json") }

func writeDesktopFile(dataDir, appPath string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(desktopFile{AppPath: appPath})
	return os.WriteFile(desktopFilePath(dataDir), data, 0o644)
}

func readDesktopFile(dataDir string) (desktopFile, bool) {
	data, err := os.ReadFile(desktopFilePath(dataDir))
	if err != nil {
		return desktopFile{}, false
	}
	var f desktopFile
	if err := json.Unmarshal(data, &f); err != nil || f.AppPath == "" {
		return desktopFile{}, false
	}
	return f, true
}
```

在 `Run` 启动时调用 `writeDesktopFile(cfg.DataDir, opts.SelfPath)`。

- [ ] **Step 3: daemon 端点**

`Config` 增加 `Shutdown func()`；`server.go` 注册：

```go
	s.mux.HandleFunc("POST /v1/shutdown", s.writeAuth(s.handleShutdown))
	s.mux.HandleFunc("GET /v1/desktop-settings", s.auth(s.handleDesktopSettingsGet))
	s.mux.HandleFunc("POST /v1/desktop-settings", s.writeAuth(s.handleDesktopSettingsSet))
```

```go
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	if s.cfg.Shutdown != nil {
		go s.cfg.Shutdown()
	}
}

func (s *Server) handleDesktopSettingsGet(w http.ResponseWriter, r *http.Request) {
	enabled, value := desktopAutostartState()
	writeJSON(w, http.StatusOK, map[string]any{"autostart": enabled, "command": value})
}

func (s *Server) handleDesktopSettingsSet(w http.ResponseWriter, r *http.Request) {
	var req struct{ Autostart bool `json:"autostart"` }
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if err := desktopSetAutostart(req.Autostart); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"autostart": req.Autostart})
}
```

平台桥接放 `internal/daemon/desktop_settings_windows.go` / `_other.go`：

```go
//go:build windows
package daemon

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

const autostartRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func desktopAutostartState() (bool, string) {
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false, ""
	}
	defer k.Close()
	v, _, err := k.GetStringValue("agent-memory")
	if err != nil {
		return false, ""
	}
	return true, v
}

func desktopSetAutostart(enabled bool) error {
	path, ok := desktopAppPathFromFile()
	if !ok {
		return fmt.Errorf("还没有打开过桌面应用：先运行一次 agent-memory-app.exe")
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, autostartRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if !enabled {
		if err := k.DeleteValue("agent-memory"); err != nil && err != registry.ErrNotExist {
			return err
		}
		return nil
	}
	return k.SetStringValue("agent-memory", fmt.Sprintf(`"%s" --tray-only`, path))
}
```

`desktopAppPathFromFile` 读 `<DataDir>/desktop.json`（复用 `desktop` 包的读取逻辑会有 import 环：`desktop` 已经 import `daemon`。因此这里用 10 行本地实现，直接读 JSON `app_path`）。

`serve` 的 `Config` 注入 `Shutdown`：在 `cmdServe` 里用 `context.WithCancel`，把 `cancel` 传给 daemon 的 `Config.Shutdown`；`ListenAndServe` 用该 ctx。

- [ ] **Step 4: 端点测试**

```go
func TestShutdownEndpointCallsHook(t *testing.T) {
	s, _ := newWebServer(t)
	called := make(chan struct{}, 1)
	s.cfg.Shutdown = func() { called <- struct{}{} }
	rec := doWebJSON(t, s, http.MethodPost, "/v1/shutdown", "token-012345678901234567890123456789", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown hook not called")
	}
}
```

桌面设置端点的注册表部分在 Windows 上会真实写 `HKCU`，测试必须用 `t.Setenv("USERPROFILE", ...)` 无法隔离注册表——因此**该端点测试只覆盖 400 分支**（没有 desktop.json 时返回 400），真实读写放在手工验收。

- [ ] **Step 5: 界面「设置」勾选**

在 `index.html` 页脚 `details` 里加：

```html
<details><summary>设置</summary>
  <label class="check"><input id="autostart" type="checkbox"> 开机时自动运行</label>
  <p id="autostart-hint" class="muted">登录后在系统托盘常驻，agent 的 MCP 调用不受影响。</p>
</details>
```

JS：初始化时 `GET /v1/desktop-settings` 设置勾选状态；`change` 时 `POST`，失败回滚并 toast；若返回 400（没打开过桌面应用），提示先双击桌面应用一次。

- [ ] **Step 6: 手工验收**

1. 勾选「开机时自动运行」→ `reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Run /v agent-memory` 显示 `"<app.exe>" --tray-only`；
2. 取消勾选 → 值消失；
3. 托盘勾选项与页面勾选状态一致；
4. 真机重启一次（或注销重登）确认自动出现在托盘——这一步可选，至少确认注册表值正确。

- [ ] **Step 7: 提交**

```bash
git add internal/desktop internal/daemon internal/cli/serve.go
git commit -m "feat: 开机自启、桌面设置端点与停止服务"
```

---

## Task 6: 文档与最终验收

- [ ] **Step 1: 更新 `docs/usage.md`**

- 「10.5 桌面应用」补：关窗进托盘、托盘菜单、开机自启、快捷方式、单实例、窗口记忆、升级步骤（托盘停止服务后替换 exe）；
- 「14 故障排查」补：托盘图标找不到（检查隐藏图标区）、自启被安全软件拦截、快捷方式指向旧路径（重跑 `--install-shortcuts`）。

- [ ] **Step 2: 全量检查**

Run: `go test ./... -count=1 && go vet ./... && go build ./...`
Expected: PASS

- [ ] **Step 3: 切换 exe 与手工验收**

1. 托盘「停止服务并退出」→ 替换 `agent-memory-app.exe` → 双击新版本，托盘与窗口正常；
2. 双击两次只出现一个窗口，第二次提示「已在运行」；
3. 调整窗口大小位置 → 关窗 → 托盘重开 → 位置保持；
4. 桌面与开始菜单快捷方式可用且带图标。

- [ ] **Step 4: 提交**

```bash
git add docs/usage.md
git commit -m "docs: 桌面托盘、自启与快捷方式说明"
```

---

## 完成标准（阶段二）

1. `go test ./... -count=1`、`go vet ./...`、`go build ./...` 全绿。
2. 关窗后托盘驻留，托盘五项功能（打开界面/立即同步/打开日志/开机自启/停止服务并退出）全部可用。
3. 开机自启勾选后 `HKCU\...\Run` 有值且与托盘勾选一致；取消后消失。
4. 桌面与开始菜单快捷方式存在、可启动、带图标。
5. 同一次登录只有一个实例；窗口位置与大小在重启应用后保持。
6. 托盘「停止服务并退出」后 47123 端口释放，exe 可被替换。

## 风险

- **systray 与 WebView2 消息循环**：已实测可编译；若运行时互相阻塞，托盘改为在 `--tray-only` 的独立进程中运行，桌面窗口进程只负责窗口。
- **注册表与快捷方式**：企业策略或安全软件可能拦截；失败时按 spec 第 6 节回滚并提示，不静默。
- **图标 .syso**：`rsrc` 是构建期工具；提交 `.syso` 以避免每次构建安装工具，若团队不接受二进制产物，可在 CI 里生成。
