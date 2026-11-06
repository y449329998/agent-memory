package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/summarize"
	"github.com/muyi99/agent-memory/internal/surface"
)

type settingsView struct {
	Server  serverSettings  `json:"server"`
	Desktop settingsDesktop `json:"desktop"`
	Scan    scanSettings    `json:"scan"`
	LLM     llmSettings     `json:"llm"`
	Data    dataSettings    `json:"data"`
}

type serverSettings struct {
	Port        int    `json:"port"`
	RunningPort int    `json:"running_port"`
	MCPBinary   string `json:"mcp_binary"`
}

type settingsDesktop struct {
	Autostart   bool   `json:"autostart"`
	Command     string `json:"command"`
	CloseToTray bool   `json:"close_to_tray"`
}

type scanSettings struct {
	IntervalSeconds int  `json:"interval_seconds"`
	QoderDisabled   bool `json:"qoder_disabled"`
	TraeDisabled    bool `json:"trae_disabled"`
	WorkBuddyOff    bool `json:"workbuddy_disabled"`
	BudgetTokens    int  `json:"budget_tokens"`
	RecentCards     int  `json:"recent_cards"`
	SettleIdleSec   int  `json:"settle_idle_seconds"`
}

type llmSettings struct {
	Enabled          bool   `json:"enabled"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	MaxInputChars    int    `json:"max_input_chars"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	SummaryPending   int    `json:"summary_pending"`
	SummaryReady     int    `json:"summary_ready"`
	SummaryFailed    int    `json:"summary_failed"`
}

type dataSettings struct {
	DataDir    string `json:"data_dir"`
	DBSize     int64  `json:"db_size"`
	LastBackup string `json:"last_backup"`
}

func (s *Server) settingsPath() string {
	if s.cfg.ConfigPath != "" {
		return s.cfg.ConfigPath
	}
	if s.cfg.HomeDir != "" {
		return config.ConfigPath(s.cfg.HomeDir)
	}
	return filepath.Join(s.cfg.DataDir, "config.toml")
}

func (s *Server) loadSettings() (config.Config, error) {
	return config.Load(s.settingsPath(), s.cfg.HomeDir)
}

func (s *Server) saveSettings(c config.Config) error {
	path := s.settingsPath()
	if data, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak-"+time.Now().Format("20060102-150405"), data, 0o600)
	}
	return config.Save(path, c)
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadSettings()
	if err != nil {
		httpError(w, err)
		return
	}
	key, _ := config.LoadLLMKey(cfg.LLMKeyPath())
	autostart, command := desktopAutostartState()
	var size int64
	if st, err := os.Stat(cfg.DBPath()); err == nil {
		size = st.Size()
	}
	counts := map[string]int{}
	if s.st != nil {
		counts, _ = s.st.SummaryCounts(r.Context())
	}
	writeJSON(w, http.StatusOK, settingsView{
		Server:  serverSettings{Port: cfg.Server.Port, RunningPort: s.Port(), MCPBinary: s.mcpBinaryPath()},
		Desktop: settingsDesktop{Autostart: autostart, Command: command, CloseToTray: cfg.Desktop.CloseToTray},
		Scan: scanSettings{
			IntervalSeconds: int(cfg.Scan.Interval / time.Second),
			QoderDisabled:   cfg.Qoder.Disabled, TraeDisabled: cfg.Trae.Disabled, WorkBuddyOff: cfg.WorkBuddy.Disabled,
			BudgetTokens: cfg.Handoff.BudgetTokens, RecentCards: cfg.Handoff.RecentCards,
			SettleIdleSec: int(cfg.Handoff.SettleIdle / time.Second),
		},
		LLM: llmSettings{
			Enabled: cfg.LLM.Enabled, BaseURL: cfg.LLM.BaseURL, Model: cfg.LLM.Model,
			TimeoutSeconds: int(cfg.LLM.Timeout / time.Second), MaxInputChars: cfg.LLM.MaxInputChars,
			APIKeyConfigured: key != "",
			SummaryPending:   counts["pending"] + counts["processing"],
			SummaryReady:     counts["ready"], SummaryFailed: counts["failed"],
		},
		Data: dataSettings{DataDir: cfg.DataDir, DBSize: size, LastBackup: s.lastBackup(cfg.DataDir)},
	})
}

func (s *Server) lastBackup(dataDir string) string {
	entries, _ := os.ReadDir(filepath.Join(dataDir, "backups"))
	var last string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "memory-") {
			last = filepath.Join(dataDir, "backups", e.Name())
		}
	}
	return last
}

type serviceSettingsInput struct {
	Port          int    `json:"port"`
	Autostart     *bool  `json:"autostart"`
	CloseToTray   *bool  `json:"close_to_tray"`
	MCPBinaryPath string `json:"mcp_binary_path"`
}

func (s *Server) handleSettingsService(w http.ResponseWriter, r *http.Request) {
	var in serviceSettingsInput
	if err := decodeJSON(r, &in); err != nil {
		httpError(w, err)
		return
	}
	cfg, err := s.loadSettings()
	if err != nil {
		httpError(w, err)
		return
	}
	oldCfg := cfg
	if in.Port < 1024 || in.Port > 65535 {
		http.Error(w, "端口必须在 1024–65535", http.StatusBadRequest)
		return
	}
	mcpBinary := strings.TrimSpace(in.MCPBinaryPath)
	if mcpBinary != "" && !filepath.IsAbs(mcpBinary) {
		http.Error(w, "MCP 启动程序必须是绝对路径", http.StatusBadRequest)
		return
	}
	cfg.Server.Port = in.Port
	cfg.MCP.BinaryPath = mcpBinary
	if in.CloseToTray != nil {
		cfg.Desktop.CloseToTray = *in.CloseToTray
	}
	if in.Autostart != nil {
		appPath, ok := desktopAppPathFromFile(s.cfg.DataDir)
		if !ok && *in.Autostart {
			http.Error(w, "还没有打开过桌面应用：先运行一次 agent-memory-app.exe", http.StatusBadRequest)
			return
		}
		if err := desktopSetAutostart(*in.Autostart, appPath); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := s.saveSettings(cfg); err != nil {
		httpError(w, err)
		return
	}
	oldBinary := s.cfg.MCPBinary
	oldRunning := s.Port()
	listening := s.listening()
	s.cfg.MCPBinary = cfg.MCP.BinaryPath
	warnings := []string{}
	// 比较「正在监听的端口」而不是配置端口：CLI 用 --port 0 启动时两者可能不同，
	// 用户保存配置端口后应当真正切过去。未启动监听（单测/嵌入调用）只写配置。
	portChanged := listening && in.Port != oldRunning
	if listening && (portChanged || cfg.MCP.BinaryPath != oldCfg.MCP.BinaryPath) {
		if err := s.relinkAgents(r.Context(), in.Port); err != nil {
			if !portChanged {
				// 只改启动程序时没有端口可回滚，直接恢复配置并尽量把 agent 配置改回去。
				_ = s.saveSettings(oldCfg)
				s.cfg.MCPBinary = oldBinary
				_ = s.relinkAgents(r.Context(), oldRunning)
				returnWithError(w, "更新 agent MCP 配置失败，已回滚："+err.Error())
				return
			}
			warnings = append(warnings, "更新 agent MCP 配置失败："+err.Error())
		}
	}
	if portChanged {
		if err := s.Rebind(r.Context(), in.Port); err != nil {
			_ = s.saveSettings(oldCfg)
			s.cfg.MCPBinary = oldBinary
			if relinkErr := s.relinkAgents(r.Context(), oldRunning); relinkErr != nil {
				warnings = append(warnings, "回滚 agent MCP 配置失败："+relinkErr.Error())
			}
			returnWithError(w, "新端口不可用，已回滚："+err.Error())
			return
		}
		s.cfg.Port = in.Port
	}
	resultPort := in.Port
	if listening {
		resultPort = s.Port()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"port": resultPort, "warnings": warnings,
	})
}

// relinkAgents 只重写已登记 agent 的 MCP 配置（端口/启动程序），
// 不碰项目里的受管区块；Trae/WorkBuddy 不参与。
// projectDir 取自当初安装区块的 AGENTS.md/CLAUDE.md 所在目录，避免 worktree
// 或多 checkout 时把配置写到另一个工作区。
func (s *Server) relinkAgents(ctx context.Context, newPort int) error {
	links, err := s.st.ListAgentLinks(ctx)
	if err != nil {
		return err
	}
	opts := surface.LinkOptions{
		Home: s.cfg.HomeDir, Binary: s.mcpBinaryPath(), Token: s.cfg.Token, Port: newPort,
	}
	var errs []string
	for _, link := range links {
		switch link.Agent {
		case "qoder", "opencode", "claude":
		default:
			continue
		}
		projectDir := filepath.Dir(link.BlockFile)
		if projectDir == "." {
			project, ok, err := s.st.ProjectByID(ctx, link.ProjectID)
			if err != nil || !ok {
				continue
			}
			projectDir = project.PrimaryPath
		}
		opts.Target = link.Agent
		opts.ProjectDir = projectDir
		plan, err := surface.PlanLink(link.Agent, opts)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", link.Agent, err))
			continue
		}
		// 端口变更只改 MCP 配置，不重装项目里的受管区块：区块内容是项目
		// 上下文，和端口无关，重写只会带来不必要的文件改动。
		if _, _, err := surface.UpsertJSONKey(plan.MCPPath, plan.MCPRoot, surface.MCPName, plan.MCPEntry); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", link.Agent, err))
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "；"))
	}
	return nil
}

func returnWithError(w http.ResponseWriter, msg string) {
	http.Error(w, msg, http.StatusBadRequest)
}

type scanSettingsInput struct {
	IntervalSeconds int   `json:"interval_seconds"`
	QoderDisabled   *bool `json:"qoder_disabled"`
	TraeDisabled    *bool `json:"trae_disabled"`
	WorkBuddyOff    *bool `json:"workbuddy_disabled"`
	BudgetTokens    int   `json:"budget_tokens"`
	RecentCards     int   `json:"recent_cards"`
	SettleIdleSec   int   `json:"settle_idle_seconds"`
}

func (s *Server) handleSettingsScan(w http.ResponseWriter, r *http.Request) {
	var in scanSettingsInput
	if err := decodeJSON(r, &in); err != nil {
		httpError(w, err)
		return
	}
	if in.IntervalSeconds < 5 {
		http.Error(w, "扫描间隔不能小于 5 秒", http.StatusBadRequest)
		return
	}
	cfg, err := s.loadSettings()
	if err != nil {
		httpError(w, err)
		return
	}
	cfg.Scan.Interval = time.Duration(in.IntervalSeconds) * time.Second
	if in.QoderDisabled != nil {
		cfg.Qoder.Disabled = *in.QoderDisabled
	}
	if in.TraeDisabled != nil {
		cfg.Trae.Disabled = *in.TraeDisabled
	}
	if in.WorkBuddyOff != nil {
		cfg.WorkBuddy.Disabled = *in.WorkBuddyOff
	}
	if in.BudgetTokens > 0 {
		cfg.Handoff.BudgetTokens = in.BudgetTokens
	}
	if in.RecentCards > 0 {
		cfg.Handoff.RecentCards = in.RecentCards
	}
	if in.SettleIdleSec > 0 {
		cfg.Handoff.SettleIdle = time.Duration(in.SettleIdleSec) * time.Second
	}
	if err := s.saveSettings(cfg); err != nil {
		httpError(w, err)
		return
	}
	reloaded := false
	if s.cfg.ReloadScan != nil {
		if err := s.cfg.ReloadScan(r.Context(), cfg); err != nil {
			returnWithError(w, "保存成功，但扫描器重载失败："+err.Error())
			return
		}
		reloaded = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"reloaded": reloaded, "restart_required": !reloaded})
}

type llmSettingsInput struct {
	Enabled       bool   `json:"enabled"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	TimeoutSec    int    `json:"timeout_seconds"`
	MaxInputChars int    `json:"max_input_chars"`
	APIKey        string `json:"api_key"`
	ClearAPIKey   bool   `json:"clear_api_key"`
}

func (s *Server) handleSettingsLLM(w http.ResponseWriter, r *http.Request) {
	var in llmSettingsInput
	if err := decodeJSON(r, &in); err != nil {
		httpError(w, err)
		return
	}
	cfg, err := s.loadSettings()
	if err != nil {
		httpError(w, err)
		return
	}
	oldCfg := cfg
	baseURL := strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			http.Error(w, "base_url 必须是 http:// 或 https:// 地址", http.StatusBadRequest)
			return
		}
	}
	cfg.LLM.Enabled = in.Enabled
	cfg.LLM.BaseURL = baseURL
	cfg.LLM.Model = strings.TrimSpace(in.Model)
	if in.TimeoutSec > 0 {
		cfg.LLM.Timeout = time.Duration(in.TimeoutSec) * time.Second
	}
	if in.MaxInputChars > 0 {
		cfg.LLM.MaxInputChars = in.MaxInputChars
	}
	if err := s.saveSettings(cfg); err != nil {
		httpError(w, err)
		return
	}
	if in.ClearAPIKey {
		if err := config.SaveLLMKey(cfg.LLMKeyPath(), ""); err != nil {
			_ = s.saveSettings(oldCfg)
			httpError(w, err)
			return
		}
	} else if apiKey := strings.TrimSpace(in.APIKey); apiKey != "" {
		if err := config.SaveLLMKey(cfg.LLMKeyPath(), apiKey); err != nil {
			_ = s.saveSettings(oldCfg)
			httpError(w, err)
			return
		}
	}
	reloaded := false
	if s.cfg.ReloadLLM != nil {
		if err := s.cfg.ReloadLLM(cfg); err != nil {
			returnWithError(w, "保存成功，但摘要 worker 重载失败："+err.Error())
			return
		}
		reloaded = true
	}
	// 扫描器缓存了「摘要是否就绪」，LLM 开关变化后必须重建，否则新扫描出的
	// 记忆/任务不会进入摘要队列。
	if s.cfg.ReloadScan != nil {
		if err := s.cfg.ReloadScan(r.Context(), cfg); err != nil {
			returnWithError(w, "保存成功，但扫描器重载失败："+err.Error())
			return
		}
		reloaded = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"reloaded": reloaded, "restart_required": !reloaded})
}

func (s *Server) handleSettingsLLMRetry(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.RetryFailedSummaries(r.Context(), time.Now())
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"retried": n})
}

func (s *Server) handleSettingsLLMTest(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.loadSettings()
	if err != nil {
		httpError(w, err)
		return
	}
	key, err := config.LoadLLMKey(cfg.LLMKeyPath())
	if err != nil {
		httpError(w, err)
		return
	}
	if cfg.LLM.BaseURL == "" || cfg.LLM.Model == "" {
		http.Error(w, "请先填写 base_url 与 model", http.StatusBadRequest)
		return
	}
	client := summarize.NewClient(cfg.LLM.BaseURL, key, cfg.LLM.Model, cfg.LLM.Timeout)
	start := time.Now()
	summary, err := client.Summarize(r.Context(), "用一句话确认模型连接正常。")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "model": cfg.LLM.Model, "elapsed_ms": time.Since(start).Milliseconds(),
		"sample": summary,
	})
}

var errSettingsUnavailable = errors.New("settings unavailable")
