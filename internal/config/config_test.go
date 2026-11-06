package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefault_DataDirUnderUserHome(t *testing.T) {
	home := t.TempDir()
	c := Default(home)
	if c.DataDir != filepath.Join(home, ".agent-memory") {
		t.Fatalf("DataDir = %q", c.DataDir)
	}
	if c.Qoder.Disabled {
		t.Fatal("qoder should default to enabled")
	}
	if c.Scan.Interval != 30*time.Second {
		t.Fatalf("interval = %v, want 30s", c.Scan.Interval)
	}
}

func TestDefault_TraeAndWorkBuddySections(t *testing.T) {
	c := Default(t.TempDir())
	for name, disabled := range map[string]bool{"trae": c.Trae.Disabled, "workbuddy": c.WorkBuddy.Disabled} {
		if disabled {
			t.Fatalf("%s should default to enabled", name)
		}
	}
	if c.Trae.Home != "" || c.WorkBuddy.Home != "" {
		t.Fatalf("default homes should be empty (auto discovery), got %q / %q", c.Trae.Home, c.WorkBuddy.Home)
	}
	if len(c.Trae.WorkspaceRoots) != 0 {
		t.Fatalf("default workspace_roots should be empty (derived from APPDATA), got %v", c.Trae.WorkspaceRoots)
	}
}

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"), "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if c.Scan.Interval != 30*time.Second {
		t.Fatalf("want defaults, got %+v", c)
	}
}

func TestLoad_PartialFileKeepsOtherDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "data_dir = \"/tmp/am\"\n[qoder]\nhome = \"/opt/qoder-cn\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/tmp/am" {
		t.Fatalf("data_dir = %q", c.DataDir)
	}
	if c.Qoder.Home != "/opt/qoder-cn" {
		t.Fatalf("qoder.home = %q", c.Qoder.Home)
	}
	if c.Scan.Interval != 30*time.Second {
		t.Fatalf("unset scan interval should keep default, got %v", c.Scan.Interval)
	}
}

func TestLoad_RejectsUnknownTopLevelKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("dat_dir = \"/tmp/x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "/fake/home"); err == nil {
		t.Fatal("typo'd key should be an error, not silently ignored")
	}
}

func TestQoderHome_OverrideThenDiscovery(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	if err := os.MkdirAll(filepath.Join(home, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Default(root)
	if got := c.QoderHome(); got != home {
		t.Fatalf("discovered %q want %q", got, home)
	}

	c.Qoder.Home = filepath.Join(root, "manual")
	if got := c.QoderHome(); got != c.Qoder.Home {
		t.Fatalf("explicit home should win, got %q", got)
	}

	c.Qoder.Disabled = true
	if got := c.QoderHome(); got != "" {
		t.Fatalf("disabled agent should resolve to empty, got %q", got)
	}
}

func TestQoderHome_EmptyWhenNothingFound(t *testing.T) {
	// 发现失败会回退到 os.UserHomeDir()；真实用户目录里通常就有 .qoder*，
	// 因此把它指到一个空目录，避免测试随开发机环境变红。
	empty := t.TempDir()
	t.Setenv("HOME", empty)
	t.Setenv("USERPROFILE", empty)
	c := Default(filepath.Join(t.TempDir(), "nothing"))
	if got := c.QoderHome(); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestLoad_TraeAndWorkBuddyOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := `[trae]
home = '/opt/trae-cn'
workspace_roots = ['/ws/a', '/ws/b']

[workbuddy]
home = '/opt/workbuddy'
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if c.Trae.Home != "/opt/trae-cn" || len(c.Trae.WorkspaceRoots) != 2 ||
		c.Trae.WorkspaceRoots[0] != "/ws/a" || c.Trae.WorkspaceRoots[1] != "/ws/b" {
		t.Fatalf("trae section not loaded: %+v", c.Trae)
	}
	if c.WorkBuddy.Home != "/opt/workbuddy" {
		t.Fatalf("workbuddy section not loaded: %+v", c.WorkBuddy)
	}
	// 没写的部分保持缺省：这些 agent 不是被禁用，只是配置里省略了。
	if c.Trae.Disabled || c.WorkBuddy.Disabled || c.Qoder.Disabled {
		t.Fatalf("missing sections must not disable agents: %+v", c)
	}
	if got := c.TraeWorkspaceRoots(); len(got) != 2 || got[0] != "/ws/a" {
		t.Fatalf("explicit workspace_roots should win, got %v", got)
	}
}

func TestLoad_DisablesTraeAndWorkBuddy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[trae]\ndisabled = true\n[workbuddy]\ndisabled = true\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if c.TraeHome() != "" || c.WorkBuddyHome() != "" {
		t.Fatalf("disabled agents should resolve to empty: %q / %q", c.TraeHome(), c.WorkBuddyHome())
	}
	if roots := c.TraeWorkspaceRoots(); roots != nil {
		t.Fatalf("disabled trae should not expose workspace roots, got %v", roots)
	}
}

func TestLoad_RejectsUnknownKeysInAgentSections(t *testing.T) {
	for name, body := range map[string]string{
		"trae":      "[trae]\nworkspace_root = ['/ws']\n",
		"workbuddy": "[workbuddy]\nhomee = '/x'\n",
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, "/fake/home"); err == nil {
			t.Fatalf("%s: typo'd key should be an error, not silently ignored", name)
		}
	}
}

func TestTraeHome_OverrideThenDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)
	home := filepath.Join(root, ".trae-cn")
	if err := os.MkdirAll(filepath.Join(home, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Default(root)
	if got := c.TraeHome(); got != home {
		t.Fatalf("discovered %q want %q", got, home)
	}

	c.Trae.Home = filepath.Join(root, "manual")
	if got := c.TraeHome(); got != c.Trae.Home {
		t.Fatalf("explicit home should win, got %q", got)
	}

	c.Trae.Disabled = true
	if got := c.TraeHome(); got != "" {
		t.Fatalf("disabled agent should resolve to empty, got %q", got)
	}
}

func TestWorkBuddyHome_OverrideThenDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)
	home := filepath.Join(root, ".workbuddy")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "MEMORY.md"), []byte("- x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := Default(root)
	if got := c.WorkBuddyHome(); got != home {
		t.Fatalf("discovered %q want %q", got, home)
	}

	c.WorkBuddy.Home = filepath.Join(root, "manual")
	if got := c.WorkBuddyHome(); got != c.WorkBuddy.Home {
		t.Fatalf("explicit home should win, got %q", got)
	}

	c.WorkBuddy.Disabled = true
	if got := c.WorkBuddyHome(); got != "" {
		t.Fatalf("disabled agent should resolve to empty, got %q", got)
	}
}

// TestTraeWorkspaceRoots_AppDataFallback：APPDATA 为空时按 <用户目录>/AppData/Roaming
// 推导，且显式配置始终优先——非 Windows 环境没有 APPDATA，这是唯一可用的锚点。
func TestTraeWorkspaceRoots_AppDataFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APPDATA", "")
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)

	c := Default(root)
	got := c.TraeWorkspaceRoots()
	want := []string{
		filepath.Join(root, "AppData", "Roaming", "Trae CN", "User", "workspaceStorage"),
		filepath.Join(root, "AppData", "Roaming", "Trae", "User", "workspaceStorage"),
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("roots = %v, want %v", got, want)
	}

	roaming := filepath.Join(root, "Roaming")
	t.Setenv("APPDATA", roaming)
	got = c.TraeWorkspaceRoots()
	if got[0] != filepath.Join(roaming, "Trae CN", "User", "workspaceStorage") ||
		got[1] != filepath.Join(roaming, "Trae", "User", "workspaceStorage") {
		t.Fatalf("APPDATA should be honored, got %v", got)
	}

	c.Trae.WorkspaceRoots = []string{filepath.Join(root, "custom")}
	if got := c.TraeWorkspaceRoots(); len(got) != 1 || got[0] != filepath.Join(root, "custom") {
		t.Fatalf("explicit workspace_roots should win, got %v", got)
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := Default("/fake/home")
	c.DataDir = "/tmp/mydata"
	c.Scan.Interval = 45 * time.Second
	c.Handoff.SettleIdle = 90 * time.Second
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/tmp/mydata" || got.Scan.Interval != 45*time.Second {
		t.Fatalf("round trip lost values: %+v", got)
	}
	if got.Handoff.SettleIdle != 90*time.Second {
		t.Fatalf("round trip lost handoff settle idle: %v", got.Handoff.SettleIdle)
	}
	if got.Handoff.BudgetTokens != 4000 || got.Handoff.RecentCards != 3 {
		t.Fatalf("round trip lost handoff defaults: %+v", got.Handoff)
	}
}

func TestDefault_HandoffSection(t *testing.T) {
	c := Default(t.TempDir())
	if c.Handoff.BudgetTokens != 4000 {
		t.Fatalf("budget = %d, want 4000", c.Handoff.BudgetTokens)
	}
	if c.Handoff.RecentCards != 3 {
		t.Fatalf("recent cards = %d, want 3", c.Handoff.RecentCards)
	}
	if c.Handoff.SettleIdle != 5*time.Minute {
		t.Fatalf("settle idle = %v, want 5m", c.Handoff.SettleIdle)
	}
}

func TestLoad_HandoffOverridesWithZeroGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "[handoff]\nbudget_tokens = 800\nrecent_cards = 0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if c.Handoff.BudgetTokens != 800 {
		t.Fatalf("budget = %d", c.Handoff.BudgetTokens)
	}
	if c.Handoff.RecentCards != 3 {
		t.Fatalf("zero recent_cards should fall back to default, got %d", c.Handoff.RecentCards)
	}
	if c.Handoff.SettleIdle != 5*time.Minute {
		t.Fatalf("unset settle_idle should fall back to default, got %v", c.Handoff.SettleIdle)
	}
}

func TestDefaultSettingsGroups(t *testing.T) {
	c := Default(`C:\Users\demo`)
	if c.Server.Port != 47123 {
		t.Fatalf("port = %d", c.Server.Port)
	}
	if !c.Desktop.CloseToTray {
		t.Fatal("close_to_tray should default true")
	}
	if c.LLM.Enabled || c.LLM.Timeout != 30*time.Second || c.LLM.MaxInputChars != 12000 {
		t.Fatalf("llm defaults = %+v", c.LLM)
	}
}

func TestSaveLoadSettingsGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := Default(t.TempDir())
	c.Server.Port = 47124
	c.Desktop.CloseToTray = false
	c.LLM = LLMConfig{Enabled: true, BaseURL: "http://127.0.0.1:11434/v1", Model: "qwen", Timeout: 20 * time.Second, MaxInputChars: 8000}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got.Server.Port != 47124 || got.Desktop.CloseToTray || got.LLM.Model != "qwen" ||
		got.LLM.Timeout != 20*time.Second || got.LLM.MaxInputChars != 8000 {
		t.Fatalf("loaded = %+v", got)
	}
}

func TestLLMKeyRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm.key")
	if err := SaveLLMKey(path, "sk-test-123"); err != nil {
		t.Fatal(err)
	}
	got, err := LoadLLMKey(path)
	if err != nil || got != "sk-test-123" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if err := SaveLLMKey(path, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty key should remove file, stat err=%v", err)
	}
}

func TestDBPathAndConfigPath(t *testing.T) {
	c := Default("/fake/home")
	c.DataDir = filepath.Join(t.TempDir(), "am")
	if filepath.Base(c.DBPath()) != "memory.db" {
		t.Fatalf("DBPath = %q", c.DBPath())
	}
	if filepath.Base(ConfigPath("/fake/home")) != "config.toml" {
		t.Fatalf("ConfigPath = %q", ConfigPath("/fake/home"))
	}
}
