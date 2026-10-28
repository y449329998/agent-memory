// Package desktop 提供双击即用的桌面入口：确保配置与数据库存在，
// 复用或拉起 serve，然后用 WebView2（失败时回退浏览器 --app 窗口）打开共享记忆页面。
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
}

// Run 完成桌面版的完整流程：配置 → 令牌 → 复用/启动 serve → 打开窗口。
// 窗口关闭后返回；只有本进程拉起的 serve 才会在退出时被结束。
func Run(ctx context.Context, opts Options) error {
	cfg, err := ensureConfig(opts.UserHome)
	if err != nil {
		return fmt.Errorf("初始化配置失败：%w", err)
	}

	logger, closeLog, err := openAppLog(cfg, opts.Logger)
	if err != nil {
		return fmt.Errorf("打开日志失败：%w", err)
	}
	defer closeLog()
	logger.Printf("desktop 启动：self=%s data=%s", opts.SelfPath, cfg.DataDir)

	token, err := ensureToken(cfg)
	if err != nil {
		return fmt.Errorf("读取访问令牌失败：%w", err)
	}

	port, running := findRunningDaemon(cfg)
	if running {
		logger.Printf("复用已在运行的 serve：端口 %d", port)
	} else {
		_, p, err := startServe(ctx, opts.SelfPath, cfg)
		if err != nil {
			logger.Printf("启动 serve 失败：%v", err)
			return fmt.Errorf("启动共享记忆服务失败（端口 %d 可能被占用）：%w", preferredPort, err)
		}
		port = p
		// 服务不随窗口退出：agent 的 MCP 调用不该因为用户关掉窗口就中断。
		// 停服务请用任务管理器或重启机器；后续可加托盘菜单。
		logger.Printf("已启动内置 serve：端口 %d（关窗后继续运行）", port)
	}

	link := buildURL(port, token)
	if webview2Available() {
		if err := openWindow(link); err != nil {
			// 窗口失败不致命：写清原因后回退到浏览器 --app 模式。
			logger.Printf("WebView2 窗口打开失败：%v；改用浏览器 --app 模式", err)
		} else {
			logger.Printf("窗口已关闭")
			return nil
		}
	} else {
		logger.Printf("未检测到 WebView2 运行时；改用浏览器 --app 模式")
	}

	if err := openBrowserApp(link); err != nil {
		logger.Printf("浏览器窗口打开失败：%v", err)
		return fmt.Errorf("打开窗口失败：%w", err)
	}
	logger.Printf("浏览器窗口已关闭")
	return nil
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

// preferredPort 与 `link` 写进 agent 配置的端口保持一致：
// 桌面版若随机取端口，MCP 桥会连不上写死端口的那份配置。
const preferredPort = 47123

// startServe 以 `selfPath --serve --port 47123` 拉起自身作为 serve 子进程。
// 固定端口而不是随机端口，是为了与 link 生成的 MCP 配置对齐。
// 子进程不绑定父进程的 ctx：窗口关闭后它要继续为 agent 的 MCP 调用服务。
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
	logPath := filepath.Join(cfg.DataDir, "app.log")
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
	path := filepath.Join(cfg.DataDir, "app.log")
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return log.New(f, "", log.LstdFlags), func() { _ = f.Close() }, nil
}
