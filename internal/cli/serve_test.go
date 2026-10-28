package cli

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServe_RefusesSecondInstance(t *testing.T) {
	home, _ := isolateHomeWithRepo(t)
	dataDir := filepath.Join(home, ".agent-memory")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 占住端口文件并让端口可达：模拟"已经有一个 serve 在跑"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(dataDir, "port"), []byte(strconv.Itoa(port)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	code := run(t, &body, "serve", "--port", strconv.Itoa(port))
	if code == 0 {
		t.Fatalf("second serve should refuse, got exit 0: %s", body.String())
	}
	if !strings.Contains(body.String(), "已在运行") {
		t.Fatalf("want 已在运行 message, got %q", body.String())
	}
}

// TestResolvePollSeconds_UsesConfigWhenFlagOmitted 回归：--poll 的默认值不能写死 30，
// 0 必须解释为「用配置里的 scan.interval」；显式正值仍然优先。
func TestResolvePollSeconds_UsesConfigWhenFlagOmitted(t *testing.T) {
	cases := []struct {
		flag     int
		interval time.Duration
		want     int
	}{
		{0, 45 * time.Second, 45},
		{0, 1500 * time.Millisecond, 1},
		{7, 45 * time.Second, 7},
	}
	for _, tc := range cases {
		if got := resolvePollSeconds(tc.flag, tc.interval); got != tc.want {
			t.Fatalf("resolvePollSeconds(%d, %v) = %d, want %d", tc.flag, tc.interval, got, tc.want)
		}
	}
}

func TestLinkDryRunWritesNothing(t *testing.T) {
	home, _ := isolateHomeWithRepo(t)
	qoderDir := filepath.Join(home, ".qoder-cn")
	if err := os.MkdirAll(qoderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(qoderDir, "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(mcpPath)
	projectDir := filepath.Join(home, "code", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, ".agent-memory")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "token"), []byte("tok0123456789012345678901234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	if code := run(t, &body, "link", "qoder", "--project", projectDir, "--dry-run"); code != 0 {
		t.Fatalf("dry run should succeed without a daemon, got %d: %s", code, body.String())
	}
	for _, want := range []string{"mcp.json", "AGENTS.md", "dry-run"} {
		if !strings.Contains(body.String(), want) {
			t.Fatalf("plan output missing %q:\n%s", want, body.String())
		}
	}
	after, _ := os.ReadFile(mcpPath)
	if string(before) != string(after) {
		t.Fatal("dry run modified the agent config")
	}
	if _, err := os.Stat(filepath.Join(projectDir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("dry run installed a context block")
	}
	// dry run 的承诺是「一个字节都不写」：连库文件都不该被创建。
	if _, err := os.Stat(filepath.Join(dataDir, "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("dry run created the database: %v", err)
	}
}

func TestLinkWithoutRunningDaemonIsRefused(t *testing.T) {
	home, _ := isolateHomeWithRepo(t)
	qoderDir := filepath.Join(home, ".qoder-cn")
	if err := os.MkdirAll(qoderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qoderDir, "mcp.json"), []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(home, "code", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var body bytes.Buffer
	code := run(t, &body, "link", "qoder", "--project", projectDir)
	if code == 0 {
		t.Fatal("link needs a running serve to know the port; it must not write a half-config")
	}
	if !strings.Contains(body.String(), "serve") {
		t.Fatalf("error should point at serve: %q", body.String())
	}
}

func TestMCPCommandWithoutDaemonFailsClearly(t *testing.T) {
	home, _ := isolateHomeWithRepo(t)
	dataDir := filepath.Join(home, ".agent-memory")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	code := runIn(t, &body, "mcp")
	if code == 0 {
		t.Fatal("mcp without a port file should fail")
	}
	if !strings.Contains(body.String(), "agent-memory serve") {
		t.Fatalf("want actionable message, got %q", body.String())
	}
}

func TestJSONOutputOfStatsIsStable(t *testing.T) {
	home, _ := isolateHomeWithRepo(t)
	var body bytes.Buffer
	run(t, &body, "init")
	body.Reset()
	if code := run(t, &body, "stats", "--json"); code != 0 {
		t.Fatalf("stats exit %d: %s", code, body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(body.Bytes(), &out); err != nil {
		t.Fatalf("stats --json must be valid json: %v\n%s", err, body.String())
	}
	if _, ok := out["memories"]; !ok {
		t.Fatalf("stats json missing memories key: %s", body.String())
	}
	_ = home
}
