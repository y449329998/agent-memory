package desktop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/daemon"
)

func TestEnsureConfigCreatesConfigAndDB(t *testing.T) {
	home := t.TempDir()

	cfg, err := ensureConfig(home)
	if err != nil {
		t.Fatalf("ensureConfig: %v", err)
	}
	if cfg.DataDir != filepath.Join(home, ".agent-memory") {
		t.Fatalf("DataDir = %q, want default under home", cfg.DataDir)
	}
	if _, err := os.Stat(config.ConfigPath(home)); err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	if _, err := os.Stat(cfg.DBPath()); err != nil {
		t.Fatalf("db file not created: %v", err)
	}

	// 再跑一次必须幂等，不能因为文件已存在而报错。
	if _, err := ensureConfig(home); err != nil {
		t.Fatalf("ensureConfig second run: %v", err)
	}
}

func TestEnsureTokenReusesExisting(t *testing.T) {
	cfg := config.Default(t.TempDir())

	first, err := ensureToken(cfg)
	if err != nil {
		t.Fatalf("ensureToken: %v", err)
	}
	second, err := ensureToken(cfg)
	if err != nil {
		t.Fatalf("ensureToken second run: %v", err)
	}
	if first != second {
		t.Fatalf("token changed between runs: %q vs %q", first, second)
	}
}

func TestFindRunningDaemonHealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	port := serverPort(t, srv)
	cfg := config.Default(t.TempDir())
	if err := daemon.WritePort(cfg.PortPath(), port); err != nil {
		t.Fatalf("write port: %v", err)
	}

	got, ok := findRunningDaemon(cfg)
	if !ok {
		t.Fatal("findRunningDaemon = false, want true")
	}
	if got != port {
		t.Fatalf("port = %d, want %d", got, port)
	}
}

func TestFindRunningDaemonUnhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	port := serverPort(t, srv)
	cfg := config.Default(t.TempDir())
	if err := daemon.WritePort(cfg.PortPath(), port); err != nil {
		t.Fatalf("write port: %v", err)
	}

	if _, ok := findRunningDaemon(cfg); ok {
		t.Fatal("findRunningDaemon = true for unhealthy server, want false")
	}
}

func TestFindRunningDaemonNoPortFile(t *testing.T) {
	cfg := config.Default(t.TempDir())
	if _, ok := findRunningDaemon(cfg); ok {
		t.Fatal("findRunningDaemon = true without port file, want false")
	}
}

func TestStartServeTimeout(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("fake serve 用 ping 制造常驻进程，仅 Windows 可用")
	}
	cfg := config.Default(t.TempDir())
	start := func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "ping", "-n", "3", "127.0.0.1")
		prepareChild(cmd)
		return cmd, nil
	}

	begin := time.Now()
	_, _, err := startServeWith(context.Background(), cfg, 400*time.Millisecond, start)
	if err == nil {
		t.Fatal("startServeWith = nil, want timeout error")
	}
	if !strings.Contains(err.Error(), "未能在") {
		t.Fatalf("error = %v, want timeout message", err)
	}
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Fatalf("startServeWith took %s, expected to give up around the 400ms deadline", elapsed)
	}
	logPath := filepath.Join(cfg.DataDir, "app.log")
	data, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("app.log not written: %v", readErr)
	}
	if !strings.Contains(string(data), "启动 serve 子进程") {
		t.Fatalf("app.log missing startup line:\n%s", data)
	}
}

func TestBuildURL(t *testing.T) {
	token := "abc def+ghi/jkl?mno&pqr=中文"
	link := buildURL(47123, token)

	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:47123" || u.Path != "/" {
		t.Fatalf("unexpected url %q", link)
	}
	if got := u.Query().Get("token"); got != token {
		t.Fatalf("token = %q, want %q", got, token)
	}
	if strings.Contains(link, " ") {
		t.Fatalf("url contains unescaped space: %q", link)
	}
	if !strings.Contains(link, "token="+url.QueryEscape(token)) {
		t.Fatalf("token not query-escaped: %q", link)
	}
}

func TestWebview2AvailableIn(t *testing.T) {
	dir := t.TempDir()
	if !webview2AvailableIn([]string{dir}) {
		t.Fatal("existing runtime dir not detected")
	}
	if webview2AvailableIn([]string{filepath.Join(dir, "missing")}) {
		t.Fatal("missing runtime dir reported available")
	}

	file := filepath.Join(dir, "Application")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if webview2AvailableIn([]string{file}) {
		t.Fatal("regular file reported as runtime dir")
	}
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse server port: %v", err)
	}
	return port
}

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
}

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

func TestConfiguredServePort(t *testing.T) {
	cfg := config.Default(t.TempDir())
	if got := configuredServePort(cfg); got != 47123 {
		t.Fatalf("default port = %d", got)
	}
	cfg.Server.Port = 47200
	if got := configuredServePort(cfg); got != 47200 {
		t.Fatalf("configured port = %d", got)
	}
	cfg.Server.Port = 0
	if got := configuredServePort(cfg); got != 47123 {
		t.Fatalf("fallback port = %d", got)
	}
}

func TestCloseToTrayReadsLatestConfig(t *testing.T) {
	home := t.TempDir()
	cfg := config.Default(home)
	if !closeToTray(home, cfg) {
		t.Fatal("default should keep tray on close")
	}
	cfg.Desktop.CloseToTray = false
	if err := config.Save(config.ConfigPath(home), cfg); err != nil {
		t.Fatal(err)
	}
	if closeToTray(home, config.Default(home)) {
		t.Fatal("closeToTray must re-read config, not use fallback")
	}
	// 用户目录为空时退回启动时配置。
	if !closeToTray("", config.Default(home)) {
		t.Fatal("empty home should use fallback")
	}
}

func TestDesktopFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readDesktopFile(dir); ok {
		t.Fatal("empty dir should have no desktop.json")
	}
	if err := writeDesktopFile(dir, `C:\bin\agent-memory-app.exe`); err != nil {
		t.Fatal(err)
	}
	f, ok := readDesktopFile(dir)
	if !ok || f.AppPath != `C:\bin\agent-memory-app.exe` {
		t.Fatalf("roundtrip: %+v ok=%v", f, ok)
	}
}
