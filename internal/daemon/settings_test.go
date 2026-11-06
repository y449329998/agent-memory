package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/store"
)

func newSettingsServer(t *testing.T) (*Server, *store.Store, string, string) {
	t.Helper()
	home := t.TempDir()
	dataDir := filepath.Join(home, ".agent-memory")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := config.ConfigPath(home)
	cfg := config.Default(home)
	cfg.DataDir = dataDir
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	s, st := newTestServer(t, func(c *Config) {
		c.DataDir = dataDir
		c.ConfigPath = cfgPath
		c.HomeDir = home
	})
	return s, st, cfgPath, home
}

func TestSettingsGetDefaults(t *testing.T) {
	s, _, _, _ := newSettingsServer(t)
	rec := webDo(t, s, http.MethodGet, "/v1/settings", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"port":47123`, `"close_to_tray":true`, `"interval_seconds":30`, `"timeout_seconds":30`, `"summary_ready":0`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	if strings.Contains(body, "sk-") {
		t.Fatalf("settings must not expose secrets: %s", body)
	}
}

func TestSettingsServiceSavesTrayAndPort(t *testing.T) {
	s, _, cfgPath, home := newSettingsServer(t)
	rec := webDo(t, s, http.MethodPost, "/v1/settings/service", webToken, map[string]any{
		"port": 47123, "close_to_tray": false,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	cfg, err := config.Load(cfgPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Desktop.CloseToTray {
		t.Fatal("close_to_tray should be saved as false")
	}
	if cfg.Server.Port != 47123 {
		t.Fatalf("port = %d", cfg.Server.Port)
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestSettingsServiceRebindsToConfiguredPort(t *testing.T) {
	s, _, _, _ := newSettingsServer(t)
	target := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	portPath := filepath.Join(t.TempDir(), "port")
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx, portPath) }()
	waitPortFile(t, portPath)

	rec := webDo(t, s, http.MethodPost, "/v1/settings/service", webToken, map[string]any{
		"port": target, "close_to_tray": true,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := waitPortFile(t, portPath); got != target {
		t.Fatalf("port = %d, want %d", got, target)
	}
	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(target) + "/v1/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz code=%d", resp.StatusCode)
	}
	cancel()
	<-done
}

func TestRelinkUsesOriginalBlockDirectory(t *testing.T) {
	s, st, _, home := newSettingsServer(t)
	ctx := context.Background()
	linkedDir := t.TempDir()
	primaryDir := t.TempDir()
	pid, err := st.EnsureProject(ctx, "f:/relink-demo", "relink-demo", "", primaryDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	blockFile := filepath.Join(linkedDir, "AGENTS.md")
	if err := os.WriteFile(blockFile, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentLink(ctx, pid, "qoder", blockFile, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.relinkAgents(ctx, 47999); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(home, ".qoder-cn", "mcp.json")
	raw, err := os.ReadFile(mcpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"47999"`) {
		t.Fatalf("mcp config missing new port: %s", raw)
	}
	if raw, err := os.ReadFile(blockFile); err != nil || string(raw) != "keep me" {
		t.Fatalf("relink must not rewrite the context block: %q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(primaryDir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("relink must not write a block into the project primary path")
	}
}

func TestSettingsServiceRejectsBadPort(t *testing.T) {
	s, _, _, _ := newSettingsServer(t)
	rec := webDo(t, s, http.MethodPost, "/v1/settings/service", webToken, map[string]any{"port": 80}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSettingsScanSavesAndReportsReload(t *testing.T) {
	s, _, cfgPath, home := newSettingsServer(t)
	rec := webDo(t, s, http.MethodPost, "/v1/settings/scan", webToken, map[string]any{
		"interval_seconds": 12, "budget_tokens": 3000, "recent_cards": 2,
		"settle_idle_seconds": 45, "qoder_disabled": true,
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"restart_required":true`) {
		t.Fatalf("without ReloadScan should report restart_required: %s", rec.Body.String())
	}
	cfg, err := config.Load(cfgPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scan.Interval.Seconds() != 12 || cfg.Handoff.BudgetTokens != 3000 ||
		cfg.Handoff.RecentCards != 2 || cfg.Handoff.SettleIdle.Seconds() != 45 || !cfg.Qoder.Disabled {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestSettingsLLMMasksKeyAndRoundTrips(t *testing.T) {
	s, _, cfgPath, home := newSettingsServer(t)
	rec := webDo(t, s, http.MethodPost, "/v1/settings/llm", webToken, map[string]any{
		"enabled": true, "base_url": "http://127.0.0.1:11434/v1", "model": "qwen",
		"timeout_seconds": 20, "max_input_chars": 8000, "api_key": "sk-secret",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("save code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodGet, "/v1/settings", webToken, nil, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("settings leaked key: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"api_key_configured":true`) {
		t.Fatalf("missing configured flag: %s", rec.Body.String())
	}
	cfg, err := config.Load(cfgPath, home)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LLM.Enabled || cfg.LLM.Model != "qwen" || cfg.LLM.Timeout.Seconds() != 20 || cfg.LLM.MaxInputChars != 8000 {
		t.Fatalf("cfg = %+v", cfg.LLM)
	}
	key, err := config.LoadLLMKey(cfg.LLMKeyPath())
	if err != nil || key != "sk-secret" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestSettingsLLMTestCallsConfiguredEndpoint(t *testing.T) {
	s, _, cfgPath, home := newSettingsServer(t)
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"连接正常"}}]}`))
	}))
	defer llm.Close()
	cfg, err := config.Load(cfgPath, home)
	if err != nil {
		t.Fatal(err)
	}
	cfg.LLM.Enabled = true
	cfg.LLM.BaseURL = llm.URL
	cfg.LLM.Model = "fake"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	rec := webDo(t, s, http.MethodPost, "/v1/settings/llm/test", webToken, map[string]any{}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ok":true`) || !strings.Contains(rec.Body.String(), "连接正常") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestSettingsLLMRetryResetsFailed(t *testing.T) {
	s, st, _, _ := newSettingsServer(t)
	ctx := context.Background()
	now := time.Now()
	if err := st.MarkSummaryPending(ctx, "memory", 9, "h", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NextSummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := st.FailSummary(ctx, "memory", 9, "boom", now); err != nil {
		t.Fatal(err)
	}
	rec := webDo(t, s, http.MethodPost, "/v1/settings/llm/retry", webToken, map[string]any{}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"retried":1`) {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	row, _, err := st.SummaryFor(ctx, "memory", 9)
	if err != nil || row.State != "pending" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}
