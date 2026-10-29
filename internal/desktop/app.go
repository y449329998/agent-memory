// Package desktop 提供双击即用的桌面入口：确保配置与数据库存在，
// 复用或拉起 serve，用 WebView2（失败时回退浏览器 --app 窗口）打开共享记忆页面，
// 并驻留系统托盘——关窗不退出，托盘可重开窗口、同步、停止服务。
package desktop

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/store"
)

const (
	appTitle     = "agent-memory 共享记忆"
	windowWidth  = 1200
	windowHeight = 820

	// serveStartTimeout 是等待子进程写出 port 文件并应答 healthz 的上限。
	serveStartTimeout = 20 * time.Second
	healthTimeout     = 500 * time.Millisecond
	portPollInterval  = 200 * time.Millisecond
)

// Options 是 Run 的输入。SelfPath 用于把自己重新拉起为 serve 子进程；
// Logger 为空时写 <DataDir>/app.log。
type Options struct {
	UserHome string
	SelfPath string
	Logger   *log.Logger
	// TrayOnly 为 true 时只驻留托盘、不打开窗口（开机自启用）。
	TrayOnly bool
}

// windowReq 容量为 1：托盘连续点击只保留一次「开窗」请求，
// 避免关闭窗口瞬间连点导致叠出多个窗口。
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

// Run 启动桌面应用：单实例 → 托盘 → 主窗口（TrayOnly 时不开窗）。
// 关窗后进程驻留托盘；托盘「停止服务并退出」才结束进程。
func Run(ctx context.Context, opts Options) error {
	if err := acquireSingleInstance(); err != nil {
		if errors.Is(err, errAlready) {
			// 已在运行的实例自己决定是聚焦窗口还是新开；这里通知完就退出。
			if !signalShowWindow() {
				focusExistingWindow(appTitle)
				MessageBox("agent-memory", "agent-memory 已在运行（见系统托盘）。")
			}
			return nil
		}
		return err
	}
	defer releaseSingleInstance()
	startShowListener()

	cfg, logger, closeLog, err := bootConfig(opts)
	if err != nil {
		return err
	}
	defer closeLog()
	if err := writeDesktopFile(cfg.DataDir, opts.SelfPath); err != nil {
		logger.Printf("写入 desktop.json 失败：%v", err)
	}

	port, err := ensureServe(ctx, opts.SelfPath, cfg, logger)
	if err != nil {
		return err
	}
	token, err := ensureToken(cfg)
	if err != nil {
		return fmt.Errorf("读取访问令牌失败：%w", err)
	}
	link := buildURL(port, token)

	quit := make(chan struct{}, 1)
	actions := trayActions{
		OpenWindow: showAppWindow,
		Sync: func() {
			if err := postJSON(port, token, "/v1/scan"); err != nil {
				logger.Printf("托盘同步失败：%v", err)
			}
		},
		OpenLog: func() {
			if err := exec.Command("notepad", cfg.AppLogPath()).Start(); err != nil {
				logger.Printf("打开日志失败：%v", err)
			}
		},
		AutostartOn: func() bool { on, _ := autostartEnabled(); return on },
		ToggleAuto:  func(v bool) error { return setAutostart(v, opts.SelfPath) },
		StopAndQuit: func() {
			if err := postJSON(port, token, "/v1/shutdown"); err != nil {
				logger.Printf("停止服务失败：%v", err)
			}
			select {
			case quit <- struct{}{}:
			default:
			}
		},
	}

	_, trayDone, err := startTray(actions, logger)
	if err != nil {
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
		case <-trayDone:
			// 托盘没了就没有再打开窗口的入口，直接退出，避免留下不可达的进程。
			return nil
		case <-takeWindowRequest():
			openWindowOnce(link, cfg, logger)
		case <-ctx.Done():
			return nil
		}
	}
}

// bootConfig 合并「准备配置/数据库」与「打开日志」两步。
func bootConfig(opts Options) (config.Config, *log.Logger, func(), error) {
	cfg, err := ensureConfig(opts.UserHome)
	if err != nil {
		return config.Config{}, nil, nil, fmt.Errorf("初始化配置失败：%w", err)
	}
	logger, closeLog, err := openAppLog(cfg, opts.Logger)
	if err != nil {
		return config.Config{}, nil, nil, fmt.Errorf("打开日志失败：%w", err)
	}
	logger.Printf("desktop 启动：self=%s data=%s trayOnly=%v", opts.SelfPath, cfg.DataDir, opts.TrayOnly)
	return cfg, logger, closeLog, nil
}

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

// ensureConfig 读取配置；配置文件不存在时写入默认值。
// 随后打开一次共享库，让首次使用者双击后就有可用的 memory.db。
func ensureConfig(userHome string) (config.Config, error) {
	if userHome == "" {
		return config.Config{}, errors.New("user home is empty")
	}
	path := config.ConfigPath(userHome)
	if _, err := os.Stat(path); err != nil {
		if !os.IsNotExist(err) {
			return config.Config{}, fmt.Errorf("stat config: %w", err)
		}
		if err := config.Save(path, config.Default(userHome)); err != nil {
			return config.Config{}, fmt.Errorf("write config: %w", err)
		}
	}
	cfg, err := config.Load(path, userHome)
	if err != nil {
		return config.Config{}, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return config.Config{}, fmt.Errorf("open store: %w", err)
	}
	if err := st.Close(); err != nil {
		return config.Config{}, fmt.Errorf("close store: %w", err)
	}
	return cfg, nil
}

// ensureToken 复用 serve 与 stdio 桥共用的令牌文件，不另起一套。
func ensureToken(cfg config.Config) (string, error) {
	return daemon.LoadOrCreateToken(cfg.TokenPath())
}

// findRunningDaemon 只有在 port 文件可读且 /v1/healthz 返回 200 时才认为
// 已有实例在运行——残留的 port 文件或别的程序占用同一端口都不算。
func findRunningDaemon(cfg config.Config) (int, bool) {
	port, err := daemon.ReadPort(cfg.PortPath())
	if err != nil {
		return 0, false
	}
	if !healthy(port, healthTimeout) {
		return 0, false
	}
	return port, true
}

func healthy(port int, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/healthz", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// postJSON 用 Bearer 调一个无 body 的写接口。
func postJSON(port int, token, path string) error {
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s 返回 %d", path, resp.StatusCode)
	}
	return nil
}

// preferredPort 与 `link` 写进 agent 配置的端口保持一致：
// 桌面版若随机取端口，MCP 桥会连不上写死端口的那份配置。
const preferredPort = 47123

// startServe 以 `selfPath --serve --port 47123` 拉起自身作为 serve 子进程。
// 固定端口是为了与 link 生成的 MCP 配置对齐；子进程不绑定父进程的 ctx，
// 窗口关闭后它要继续为 agent 的 MCP 调用服务。
// 调用方必须确认没有健康实例在运行：这里会删掉可能残留的 port 文件。
func startServe(ctx context.Context, selfPath string, cfg config.Config) (*exec.Cmd, int, error) {
	return startServeWith(ctx, cfg, serveStartTimeout, func(context.Context) (*exec.Cmd, error) {
		cmd := exec.Command(selfPath, "--serve", "--port", fmt.Sprint(preferredPort))
		prepareChild(cmd)
		return cmd, nil
	})
}

// startServeWith 把「启动哪个命令」抽出来，测试可以换成假的常驻进程，
// 专门验证端口轮询的超时路径。
func startServeWith(ctx context.Context, cfg config.Config, timeout time.Duration, startFn func(context.Context) (*exec.Cmd, error)) (*exec.Cmd, int, error) {
	portPath := cfg.PortPath()
	if err := os.Remove(portPath); err != nil && !os.IsNotExist(err) {
		return nil, 0, fmt.Errorf("remove stale port file: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, 0, fmt.Errorf("create data dir: %w", err)
	}
	logPath := cfg.AppLogPath()
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, 0, fmt.Errorf("open app.log: %w", err)
	}

	cmd, err := startFn(ctx)
	if err != nil {
		logFile.Close()
		return nil, 0, err
	}
	fmt.Fprintf(logFile, "=== %s 启动 serve 子进程 ===\n", time.Now().Format(time.RFC3339))
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, 0, fmt.Errorf("start serve: %w", err)
	}

	done := make(chan struct{})
	go func() {
		waitErr := cmd.Wait()
		fmt.Fprintf(logFile, "=== %s serve 子进程退出：%v ===\n", time.Now().Format(time.RFC3339), waitErr)
		logFile.Close()
		close(done)
	}()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if port, err := daemon.ReadPort(portPath); err == nil && healthy(port, healthTimeout) {
			return cmd, port, nil
		}
		select {
		case <-ctx.Done():
			terminateAndWait(cmd, done)
			return nil, 0, ctx.Err()
		case <-time.After(portPollInterval):
		}
	}
	terminateAndWait(cmd, done)
	return nil, 0, fmt.Errorf("serve 未能在 %s 内就绪（详见 %s）", timeout, logPath)
}

// terminateAndWait 结束子进程并等它被 Wait 回收，确保日志写完再返回。
func terminateAndWait(cmd *exec.Cmd, done <-chan struct{}) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// openWindowOnce 打开一次窗口（阻塞到关闭），期间读写窗口位置。
func openWindowOnce(link string, cfg config.Config, logger *log.Logger) {
	var initial *windowState
	if st, ok := loadWindowState(cfg.DataDir); ok {
		sw, sh := screenSize()
		st = clampWindowState(st, sw, sh, windowWidth, windowHeight)
		initial = &st
	}

	if webview2Available() {
		err := openWindow(link, initial, func(st windowState) {
			if err := saveWindowState(cfg.DataDir, st); err != nil {
				logger.Printf("保存窗口位置失败：%v", err)
			}
		})
		if err == nil {
			logger.Printf("窗口已关闭")
			return
		}
		logger.Printf("WebView2 窗口打开失败：%v；改用浏览器 --app 模式", err)
	} else {
		logger.Printf("未检测到 WebView2 运行时；改用浏览器 --app 模式")
	}

	if err := openBrowserApp(link); err != nil {
		logger.Printf("浏览器窗口打开失败：%v", err)
		MessageBox("agent-memory", "打开窗口失败："+err.Error())
		return
	}
	logger.Printf("浏览器窗口已关闭")
}

func buildURL(port int, token string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/?token=%s", port, url.QueryEscape(token))
}

// webview2Available 判断本机是否装了 WebView2 运行时。
// 非 Windows 一律返回 false（窗口实现也只在 Windows 构建）。
func webview2Available() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	return webview2AvailableIn(webview2RuntimeDirs())
}

// webview2RuntimeDirs 给出 WebView2 运行时的常见安装根目录。
func webview2RuntimeDirs() []string {
	var dirs []string
	for _, base := range []string{os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramFiles")} {
		if base != "" {
			dirs = append(dirs, filepath.Join(base, "Microsoft", "EdgeWebView", "Application"))
		}
	}
	return dirs
}

// webview2AvailableIn 把路径列表作为参数，测试不需要依赖真实安装。
func webview2AvailableIn(dirs []string) bool {
	for _, dir := range dirs {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return true
		}
	}
	return false
}

// openBrowserApp 是 WebView2 不可用时的回退：依次尝试 Edge、Chrome 的应用窗口模式。
func openBrowserApp(link string) error {
	args := []string{
		"--app=" + link,
		fmt.Sprintf("--window-size=%d,%d", windowWidth, windowHeight),
	}
	var tried []string
	for _, exe := range browserCandidates() {
		st, err := os.Stat(exe)
		if err != nil || st.IsDir() {
			continue
		}
		tried = append(tried, exe)
		cmd := exec.Command(exe, args...)
		if err := cmd.Start(); err != nil {
			continue
		}
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("%s 退出：%w", exe, err)
		}
		return nil
	}
	if len(tried) == 0 {
		return errors.New("未找到 Microsoft Edge 或 Google Chrome")
	}
	return fmt.Errorf("无法启动浏览器窗口：%s", strings.Join(tried, "、"))
}

// browserCandidates 按优先级列出 Edge / Chrome 的常见安装路径。
func browserCandidates() []string {
	bases := []string{
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("ProgramFiles"),
		os.Getenv("LOCALAPPDATA"),
	}
	var out []string
	for _, base := range bases {
		if base == "" {
			continue
		}
		out = append(out,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	return out
}

// openAppLog 打开 <DataDir>/app.log（追加、带时间戳）；外部传入 Logger 时直接使用。
func openAppLog(cfg config.Config, external *log.Logger) (*log.Logger, func(), error) {
	if external != nil {
		return external, func() {}, nil
	}
	path := cfg.AppLogPath()
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return log.New(f, "", log.LstdFlags), func() { _ = f.Close() }, nil
}
