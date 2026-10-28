package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/muyi99/agent-memory/internal/agent/qoder"
)

const (
	dirName         = ".agent-memory"
	defaultInterval = 30 * time.Second
	defaultMemLimit = 20
)

type Config struct {
	DataDir string        `toml:"data_dir"`
	Qoder   QoderConfig   `toml:"qoder"`
	Scan    ScanConfig    `toml:"scan"`
	Handoff HandoffConfig `toml:"handoff"`
}

type QoderConfig struct {
	Disabled bool   `toml:"disabled"`
	Home     string `toml:"home"`
}

type ScanConfig struct {
	Interval       time.Duration `toml:"interval"`
	MemoryListSize int           `toml:"memory_list_size"`
}

// HandoffConfig 控制交接简报的口径：预算、带几张任务卡、会话多久算写完。
type HandoffConfig struct {
	BudgetTokens int           `toml:"budget_tokens"`
	RecentCards  int           `toml:"recent_cards"`
	SettleIdle   time.Duration `toml:"settle_idle"`
}

// Default 以 userHome 为基准生成缺省值。把 home 作为参数传进来，
// 这样测试不需要改环境变量。
func Default(userHome string) Config {
	return Config{
		DataDir: filepath.Join(userHome, dirName),
		Qoder:   QoderConfig{},
		Scan:    ScanConfig{Interval: defaultInterval, MemoryListSize: defaultMemLimit},
		Handoff: HandoffConfig{BudgetTokens: 1500, RecentCards: 3, SettleIdle: 5 * time.Minute},
	}
}

func ConfigPath(userHome string) string {
	return filepath.Join(userHome, dirName, "config.toml")
}

func (c Config) DBPath() string { return filepath.Join(c.DataDir, "memory.db") }

// QoderHome 返回 Qoder 配置根；显式配置优先，其次自动发现，禁用时返回空串。
func (c Config) QoderHome() string {
	if c.Qoder.Disabled {
		return ""
	}
	if c.Qoder.Home != "" {
		return c.Qoder.Home
	}
	base := c.userBaseForDiscovery()
	if home, ok := qoder.DiscoverHome(base); ok {
		return home
	}
	return ""
}

// userBaseForDiscovery: 发现用的基准目录。DataDir 是输出目录，不是 agent 的安装位置，
// 因此优先取 DataDir 的上一级（通常就是用户目录），取不到再回退用户主目录。
func (c Config) userBaseForDiscovery() string {
	if c.DataDir != "" {
		if parent := filepath.Dir(c.DataDir); parent != "" && parent != c.DataDir {
			if _, err := os.Stat(filepath.Join(parent, ".qoder-cn")); err == nil {
				return parent
			}
			if _, err := os.Stat(filepath.Join(parent, ".qoder")); err == nil {
				return parent
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// Load 读配置；文件不存在时返回缺省值而不是错误——init 之前本来就没有配置文件。
// 未知键报错：拼错的键被静默忽略比报错更难查。
func Load(path, userHome string) (Config, error) {
	c := Default(userHome)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	meta, err := toml.Decode(string(data), &c)
	if err != nil {
		return c, fmt.Errorf("parse config %s: %w", path, err)
	}
	if unknown := meta.Undecoded(); len(unknown) > 0 {
		keys := make([]string, 0, len(unknown))
		for _, k := range unknown {
			keys = append(keys, k.String())
		}
		return c, fmt.Errorf("config %s has unknown keys: %s", path, strings.Join(keys, ", "))
	}
	if c.DataDir == "" {
		c.DataDir = Default(userHome).DataDir
	}
	if c.Scan.Interval <= 0 {
		c.Scan.Interval = defaultInterval
	}
	if c.Scan.MemoryListSize <= 0 {
		c.Scan.MemoryListSize = defaultMemLimit
	}
	if c.Handoff.BudgetTokens <= 0 {
		c.Handoff.BudgetTokens = Default(userHome).Handoff.BudgetTokens
	}
	if c.Handoff.RecentCards <= 0 {
		c.Handoff.RecentCards = Default(userHome).Handoff.RecentCards
	}
	if c.Handoff.SettleIdle <= 0 {
		c.Handoff.SettleIdle = Default(userHome).Handoff.SettleIdle
	}
	return c, nil
}

// Save 写出配置文件。权限 0600：将来这里会放 LLM 端点等敏感项。
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}
