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
	if got.Handoff.BudgetTokens != 1500 || got.Handoff.RecentCards != 3 {
		t.Fatalf("round trip lost handoff defaults: %+v", got.Handoff)
	}
}

func TestDefault_HandoffSection(t *testing.T) {
	c := Default(t.TempDir())
	if c.Handoff.BudgetTokens != 1500 {
		t.Fatalf("budget = %d, want 1500", c.Handoff.BudgetTokens)
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
