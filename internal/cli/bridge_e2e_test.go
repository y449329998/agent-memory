package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/mcp"
	"github.com/muyi99/agent-memory/internal/store"
)

// TestBridgeToEndToEnd 覆盖 agent 实际走的完整链路：
// stdio 消息 → 桥 → HTTP → daemon → store → 结果文本。
// 每条消息都经过 ServeStdio（换行分隔的 stdin/stdout），而不是直接调 Send，
// 这样分帧与通知过滤也在链路里。
func TestBridgeToEndToEnd(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const token = "e2e-token-012345678901234567890123456789"
	srv := daemon.New(daemon.Config{Token: token, BudgetTokens: 1500, RecentCards: 3}, st)
	upstream := httptest.NewServer(srv.Handler())
	defer upstream.Close()

	bridge := mcp.NewBridge(0, token)
	bridge.SetEndpoint(upstream.URL + "/mcp")

	// initialize
	resp := roundTrip(t, bridge, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}},
	})
	if got := resp["result"].(map[string]any)["protocolVersion"]; got != "2025-06-18" {
		t.Fatalf("initialize = %v", resp)
	}

	// tools/list
	resp = roundTrip(t, bridge, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 12 {
		t.Fatalf("want 12 tools over the bridge, got %d", len(tools))
	}
	t.Logf("stdio -> HTTP -> daemon: %d tools", len(tools))

	// memory_add 写进共享库
	resp = roundTrip(t, bridge, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "memory_add",
			"arguments": map[string]any{"text": "端到端写入的记忆条目", "type": "convention", "scope": "user"},
		},
	})
	out := toolText(t, resp)
	if !strings.Contains(out, `"created": true`) {
		t.Fatalf("add result should report creation:\n%s", out)
	}
	t.Logf("memory_add -> %s", strings.Join(strings.Fields(out), " "))

	// 另一个查询命中它
	resp = roundTrip(t, bridge, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "memory_search", "arguments": map[string]any{"query": "端到端"}},
	})
	out = toolText(t, resp)
	if !strings.Contains(out, "端到端写入的记忆条目") {
		t.Fatalf("search over bridge missed the written memory:\n%s", out)
	}
	t.Logf("memory_search -> %s", strings.Join(strings.Fields(out), " "))

	// handoff_get 对未知项目给出可读错误而不是崩溃
	resp = roundTrip(t, bridge, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": "handoff_get", "arguments": map[string]any{"project_path": `Z:\nope`}},
	})
	if res := resp["result"].(map[string]any); res["isError"] != true {
		t.Fatalf("unknown project should be a tool error: %v", res)
	}
}

// roundTrip 把一条 JSON-RPC 消息按 agent 的方式写进桥的 stdin，
// 读回桥写到 stdout 的那一行。单次会话只含一条消息，足够隔离断言。
func roundTrip(t *testing.T, b *mcp.Bridge, msg map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	var out bytes.Buffer
	if err := b.ServeStdio(context.Background(), bytes.NewReader(raw), &out); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatalf("no reply for %v", msg["method"])
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("bridge wrote invalid json: %v\n%s", err, line)
	}
	return resp
}

func toolText(t *testing.T, resp map[string]any) string {
	t.Helper()
	if resp["error"] != nil {
		t.Fatalf("jsonrpc error: %v", resp["error"])
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", resp)
	}
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("no content blocks: %v", res)
	}
	return content[0].(map[string]any)["text"].(string)
}

func TestServeAndMCPSharePortFileAcrossProcesses(t *testing.T) {
	// 这一步只验证端口文件的契约，不起真进程
	dir := t.TempDir()
	portPath := filepath.Join(dir, "port")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	actual := ln.Addr().(*net.TCPAddr).Port

	if err := daemon.WritePort(portPath, actual); err != nil {
		t.Fatal(err)
	}
	got, err := daemon.ReadPort(portPath)
	if err != nil {
		t.Fatal(err)
	}
	if got != actual {
		t.Fatalf("port file = %d want %d", got, actual)
	}
	// 端口文件残留但进程已消失时，读它仍会成功而连接会失败：桥要给出可行动错误
	if err := daemon.WritePort(portPath, 1); err != nil {
		t.Fatal(err)
	}
	if p, err := daemon.ReadPort(portPath); err != nil || p != 1 {
		t.Fatalf("stale port file should still be readable, got %d/%v", p, err)
	}
}

// TestRealServeProcess 用临时 HOME 启动真实的 serve 子进程（--port 0），
// 从它写下的端口文件取端口、确认 /v1/healthz 可达，然后杀掉并确认端口释放。
// 全程不读也不写任何 agent 配置：HOME 指向临时目录。
func TestRealServeProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("真实子进程测试在 -short 下跳过")
	}

	bin := filepath.Join(t.TempDir(), "agent-memory-e2e")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "./cmd/agent-memory")
	build.Dir = moduleRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	home := t.TempDir()
	cmd := exec.Command(bin, "serve", "--port", "0", "--no-watch")
	// Windows 的 os.UserHomeDir 读 USERPROFILE，其他系统读 HOME；两个都给。
	cmd.Env = append(os.Environ(), "USERPROFILE="+home, "HOME="+home)
	var log bytes.Buffer
	cmd.Stdout = &log
	cmd.Stderr = &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	var (
		killOnce sync.Once
		done     = make(chan struct{})
		waitErr  error
	)
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()
	stop := func() {
		killOnce.Do(func() {
			_ = cmd.Process.Kill()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Logf("serve 子进程在 Kill 后 5s 仍未退出")
			}
		})
	}
	t.Cleanup(stop)
	// serveLog 只在进程结束后读取，避免与 os/exec 的复制 goroutine 竞争。
	serveLog := func() string {
		select {
		case <-done:
			return log.String()
		default:
			return "(serve 仍在运行，日志未读取)"
		}
	}

	portPath := filepath.Join(home, ".agent-memory", "port")
	port, err := waitForPortFile(portPath, 20*time.Second, done)
	if err != nil {
		stop()
		t.Fatalf("%v\nserve 输出:\n%s", err, serveLog())
	}

	// 端口文件里的端口就是实际监听端口：healthz 必须在这个端口上应答。
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/healthz", port))
	if err != nil {
		stop()
		t.Fatalf("healthz on port %d: %v\nserve 输出:\n%s", port, err, serveLog())
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		stop()
		t.Fatalf("read healthz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		stop()
		t.Fatalf("healthz status = %d, body=%s\nserve 输出:\n%s", resp.StatusCode, body, serveLog())
	}
	var health map[string]any
	if err := json.Unmarshal(body, &health); err != nil || health["ok"] != true {
		stop()
		t.Fatalf("healthz body = %q (err=%v)", body, err)
	}

	// token 文件与端口文件同目录，是 link 传给 agent 的凭据来源。
	token, err := os.ReadFile(filepath.Join(home, ".agent-memory", "token"))
	if err != nil || len(strings.TrimSpace(string(token))) < 32 {
		stop()
		t.Fatalf("token file: len=%d err=%v", len(bytes.TrimSpace(token)), err)
	}

	t.Logf("serve 子进程：pid=%d port=%d healthz=%s", cmd.Process.Pid, port, strings.TrimSpace(string(body)))

	// 杀干净：进程退出后端口不再接受连接。
	stop()
	select {
	case <-done:
	default:
		t.Fatal("serve 子进程仍在运行")
	}
	if waitErr == nil {
		t.Logf("serve 在 Kill 后正常退出（waitErr=nil）")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if dialErr != nil {
			break
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("serve 被杀后端口 %d 仍可连接", port)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !strings.Contains(serveLog(), "serve on 127.0.0.1") {
		t.Fatalf("serve 启动日志缺少提示:\n%s", serveLog())
	}
}

// waitForPortFile 轮询端口文件；子进程先退出时立即返回错误，不空等。
func waitForPortFile(path string, timeout time.Duration, done <-chan struct{}) (int, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return 0, errors.New("serve 在写下端口文件之前就退出了")
		default:
		}
		port, err := daemon.ReadPort(path)
		if err == nil {
			return port, nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return 0, fmt.Errorf("等待端口文件 %s 超时: %v", path, lastErr)
}

// moduleRoot 从测试工作目录向上找 go.mod，供 go build 使用。
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("找不到 go.mod")
		}
		dir = parent
	}
}
