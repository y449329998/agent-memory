# 任务筛选、MCP 教程与设置中心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. 本项目禁止调用子 agent；所有任务在当前会话内 inline 执行。每个 Task 完成先汇报，用户确认后再提交。

**Goal:** 为任务页增加完整筛选，提供可复制的 MCP 教程，建立单页四分区设置中心，并让端口/扫描/LLM 摘要/数据备份可配置且安全生效。

**Architecture:** `config.toml` 作为非敏感配置源，DPAPI 文件保存 LLM Key；daemon 增加 settings/MCP/data/summary API。端口与扫描参数通过服务内重绑/重载生效；摘要由后台 worker 异步生成，原文与规则结果始终保留。设置页为单页左侧四分区，任务与 MCP 为独立页面。

**Tech Stack:** Go 1.22、modernc SQLite、net/http、Windows DPAPI、WebView2、原生 JavaScript、OpenAI 兼容 `/chat/completions`。

---

## 文件结构

新增/修改：

- `internal/config/config.go`：Server/Desktop/LLM/MCP 配置组、默认值、Save。
- `internal/config/llm_secret_windows.go` / `llm_secret_other.go`：DPAPI Key 存取。
- `internal/store/task.go`：任务筛选条件。
- `internal/store/summary.go`：摘要队列表与状态。
- `internal/store/data.go`：备份、导出、导入。
- `internal/summarize/client.go`：OpenAI 兼容客户端。
- `internal/summarize/worker.go`：摘要 worker。
- `internal/surface/relink.go`：端口变更后重写已登记 MCP 配置。
- `internal/daemon/settings.go`：设置 API 与生效编排。
- `internal/daemon/mcp_snippets.go`：MCP 配置生成。
- `internal/daemon/data.go`：备份/导出/导入接口。
- `internal/daemon/server.go`：路由与监听器生命周期。
- `internal/cli/serve.go`：扫描器热重载回调、摘要 worker。
- `internal/desktop/app.go`：close_to_tray 读取。
- `internal/daemon/webui/settings.html`、`mcp.html`：新页面。
- `internal/daemon/webui/app.js` / `app.css`：三联页面交互。
- `internal/daemon/webui/tasks.html` / `app.js`：任务筛选条。
- `internal/daemon/webui/layout.html`：MCP/设置入口。
- `docs/usage.md`：使用说明。

---

### Task 1: 配置模型与 LLM 密钥存储

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/llm_secret_windows.go`
- Create: `internal/config/llm_secret_other.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 增加：

```go
func TestDefaultSettingsGroups(t *testing.T) {
	c := Default(`C:\Users\demo`)
	if c.Server.Port != 47123 {
		t.Fatalf("port = %d", c.Server.Port)
	}
	if !c.Desktop.CloseToTray {
		t.Fatal("close_to_tray should default true")
	}
	if c.LLM.Enabled || c.LLM.Timeout != 30*time.Second {
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
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Server.Port != 47124 || got.Desktop.CloseToTray || got.LLM.Model != "qwen" {
		t.Fatalf("loaded = %+v", got)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/config -run 'TestDefaultSettingsGroups|TestSaveLoadSettingsGroups' -v`

Expected: FAIL，字段不存在。

- [ ] **Step 3: 增加配置组**

在 `internal/config/config.go` 增加：

```go
type Config struct {
	DataDir   string          `toml:"data_dir"`
	Server    ServerConfig    `toml:"server"`
	Desktop   DesktopConfig   `toml:"desktop"`
	MCP       MCPConfig       `toml:"mcp"`
	Qoder     QoderConfig     `toml:"qoder"`
	Trae      TraeConfig      `toml:"trae"`
	WorkBuddy WorkBuddyConfig `toml:"workbuddy"`
	Scan      ScanConfig      `toml:"scan"`
	Handoff   HandoffConfig   `toml:"handoff"`
	LLM       LLMConfig       `toml:"llm"`
}

type ServerConfig struct {
	Port int `toml:"port"`
}

type DesktopConfig struct {
	CloseToTray bool `toml:"close_to_tray"`
}

type MCPConfig struct {
	BinaryPath string `toml:"binary_path"`
}

type LLMConfig struct {
	Enabled       bool          `toml:"enabled"`
	BaseURL       string        `toml:"base_url"`
	Model         string        `toml:"model"`
	Timeout       time.Duration `toml:"timeout"`
	MaxInputChars int           `toml:"max_input_chars"`
}
```

默认值：

```go
Server:  ServerConfig{Port: 47123},
Desktop: DesktopConfig{CloseToTray: true},
LLM:     LLMConfig{Timeout: 30 * time.Second, MaxInputChars: 12000},
```

`Load` 中补齐零值：端口 <=0 用 47123；LLM.Timeout <=0 用 30s；MaxInputChars <=0 用 12000。

- [ ] **Step 4: 实现 DPAPI 密钥文件**

`internal/config/llm_secret_windows.go`：

```go
//go:build windows

package config

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modcrypt32  = windows.NewLazySystemDLL("crypt32.dll")
	procProtect = modcrypt32.NewProc("CryptProtectData")
	procUnprotect = modcrypt32.NewProc("CryptUnprotectData")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func blobFromBytes(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b dataBlob) bytes() []byte {
	if b.pbData == nil || b.cbData == 0 {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func SaveLLMKey(path, key string) error {
	if key == "" {
		_ = os.Remove(path)
		return nil
	}
	in := []byte(key)
	blobIn := blobFromBytes(in)
	var blobOut dataBlob
	r, _, err := procProtect.Call(0, 0, uintptr(unsafe.Pointer(&blobIn)), 0, 0, 0, uintptr(unsafe.Pointer(&blobOut)))
	if r == 0 {
		return err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(blobOut.pbData)))
	return os.WriteFile(path, blobOut.bytes(), 0o600)
}

func LoadLLMKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	blobIn := blobFromBytes(raw)
	var blobOut dataBlob
	r, _, err := procUnprotect.Call(0, 0, uintptr(unsafe.Pointer(&blobIn)), 0, 0, 0, uintptr(unsafe.Pointer(&blobOut)))
	if r == 0 {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(blobOut.pbData)))
	return string(blobOut.bytes()), nil
}
```

`internal/config/llm_secret_other.go`：

```go
//go:build !windows

package config

import "os"

func SaveLLMKey(path, key string) error {
	if key == "" {
		_ = os.Remove(path)
		return nil
	}
	return os.WriteFile(path, []byte(key), 0o600)
}

func LoadLLMKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(raw), err
}
```

在 config 增加 `LLMKeyPath() string` 返回 `DataDir/llm.key`。

- [ ] **Step 5: 运行测试**

Run: `go test ./internal/config -v`

Expected: PASS；Windows DPAPI 测试可另加 round-trip：

```go
func TestLLMKeyRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llm.key")
	if err := SaveLLMKey(path, "sk-test"); err != nil { t.Fatal(err) }
	got, err := LoadLLMKey(path)
	if err != nil || got != "sk-test" { t.Fatalf("got=%q err=%v", got, err) }
}
```

- [ ] **Step 6: 汇报并提交**

经用户确认后提交。

---

### Task 2: 任务筛选后端

**Files:**
- Modify: `internal/store/task.go`
- Modify: `internal/store/task_test.go`
- Modify: `internal/daemon/tasks.go`
- Modify: `internal/daemon/tasks_test.go`

- [ ] **Step 1: 写失败测试**

在 `internal/store/task_test.go` 增加：

```go
func TestListTasksFilters(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/filter", "filter", "", `F:\filter`, now)
	mk := func(goal, owner, source string, at time.Time, step string) int64 {
		id, err := s.CreateTask(ctx, domain.Task{
			ProjectID: pid, Goal: goal, Status: domain.StatusDoing,
			OwnerAgent: owner, SourceAgent: source, CreatedAt: at, UpdatedAt: at,
			Steps: []domain.TaskStep{{Subject: step, Status: domain.StepDoing}},
		})
		if err != nil { t.Fatal(err) }
		return id
	}
	mk("修复导出", "qoder", "qoder", now.Add(-2*time.Hour), "补测试")
	mk("重构登录", "claude", "workbuddy", now.Add(-40*24*time.Hour), "改页面")

	got, err := s.ListTasks(ctx, TaskFilter{
		ProjectID: pid, Query: "补测试", OwnerAgent: "qoder",
		UpdatedAfter: now.Add(-24 * time.Hour), Limit: 20,
	})
	if err != nil || len(got) != 1 || got[0].Goal != "修复导出" {
		t.Fatalf("filter got=%+v err=%v", got, err)
	}
	got, err = s.ListTasks(ctx, TaskFilter{ProjectID: pid, SourceAgent: "workbuddy", Limit: 20})
	if err != nil || len(got) != 1 || got[0].Goal != "重构登录" {
		t.Fatalf("source filter got=%+v err=%v", got, err)
	}
	got, err = s.ListTasks(ctx, TaskFilter{ProjectID: pid, Query: "不存在", Limit: 20})
	if err != nil || len(got) != 0 { t.Fatalf("query got=%+v err=%v", got, err) }
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store -run TestListTasksFilters -v`

Expected: FAIL，字段不存在。

- [ ] **Step 3: 扩展 TaskFilter 与 SQL**

```go
type TaskFilter struct {
	ProjectID       int64
	Query           string
	Status          string
	OwnerAgent      string
	SourceAgent     string
	UpdatedAfter    time.Time
	UpdatedBefore   time.Time
	IncludeArchived bool
	Limit           int
	Offset          int
}
```

在 `ListTasks` 增加：

```go
if q := strings.TrimSpace(f.Query); q != "" {
	like := "%" + q + "%"
	querySQL += ` AND (goal LIKE ? OR next_steps LIKE ? OR files_json LIKE ? OR EXISTS (
	  SELECT 1 FROM task_step ts WHERE ts.task_id = task.id
	  AND (ts.subject LIKE ? OR ts.description LIKE ?)))`
	args = append(args, like, like, like, like, like)
}
if f.OwnerAgent != "" {
	querySQL += ` AND owner_agent = ?`
	args = append(args, f.OwnerAgent)
}
if f.SourceAgent != "" {
	querySQL += ` AND source_agent = ?`
	args = append(args, f.SourceAgent)
}
if !f.UpdatedAfter.IsZero() {
	querySQL += ` AND updated_at >= ?`
	args = append(args, f.UpdatedAfter.Unix())
}
if !f.UpdatedBefore.IsZero() {
	querySQL += ` AND updated_at <= ?`
	args = append(args, f.UpdatedBefore.Unix())
}
```

注意：现有变量名叫 `q`；将 SQL builder 改名 `querySQL`，避免与 Query 冲突。

- [ ] **Step 4: HTTP 参数**

`handleListFormalTasks` 解析：

```go
after, _ := time.Parse(time.RFC3339, r.URL.Query().Get("updated_after"))
before, _ := time.Parse(time.RFC3339, r.URL.Query().Get("updated_before"))
tasks, err := s.st.ListTasks(r.Context(), store.TaskFilter{
	ProjectID: projectID,
	Query: r.URL.Query().Get("q"),
	Status: r.URL.Query().Get("status"),
	OwnerAgent: r.URL.Query().Get("owner_agent"),
	SourceAgent: r.URL.Query().Get("source_agent"),
	UpdatedAfter: after, UpdatedBefore: before,
	IncludeArchived: r.URL.Query().Get("include_archived") == "1",
	Limit: limit,
})
```

如果日期只有 `YYYY-MM-DD`，先 `time.Parse("2006-01-02")`，结束日期加 24h-1s。

- [ ] **Step 5: 运行测试**

Run: `go test ./internal/store ./internal/daemon -run 'Task' -v`

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 3: 任务页筛选 UI

**Files:**
- Modify: `internal/daemon/webui/tasks.html`
- Modify: `internal/daemon/webui/app.js`
- Modify: `internal/daemon/webui/app.css`

- [ ] **Step 1: 增加筛选条**

在 `tasks.html` 的任务列表上方加入：

```html
<form id="task-filters" class="task-filters">
  <input id="tf-q" type="search" placeholder="搜索目标、步骤、下一步、文件">
  <select id="tf-project"><option value="">全部项目</option></select>
  <select id="tf-status">
    <option value="">全部状态</option><option value="doing">进行中</option>
    <option value="blocked">受阻</option><option value="done">已完成</option>
  </select>
  <input id="tf-owner" type="text" placeholder="owner">
  <select id="tf-source"><option value="">全部来源</option></select>
  <select id="tf-range">
    <option value="">全部时间</option><option value="7">最近 7 天</option>
    <option value="30">最近 30 天</option><option value="90">最近 90 天</option>
  </select>
  <button class="stamp small" type="submit">筛选</button>
  <button id="tf-reset" class="stamp small ghost" type="button">重置</button>
  <label class="check"><input id="tasks-archived" type="checkbox"> 显示归档</label>
</form>
```

- [ ] **Step 2: JS 读取/写回 URL**

在 `initTasks` 中：

```js
const q = $('tf-q'), project = $('tf-project'), status = $('tf-status');
const owner = $('tf-owner'), source = $('tf-source'), range = $('tf-range');
const params = new URLSearchParams(location.search);
q.value = params.get('q') || '';
status.value = params.get('status') || '';
owner.value = params.get('owner_agent') || '';
source.value = params.get('source_agent') || '';
range.value = params.get('range') || '';
```

项目下拉从 `/v1/projects` 填充，来源下拉用 `qoder/trae/workbuddy/manual/web/mcp`。

提交：

```js
$('task-filters').addEventListener('submit', function (e) {
  e.preventDefault();
  const next = new URLSearchParams();
  if (q.value.trim()) next.set('q', q.value.trim());
  if (project.value) next.set('project_key', project.value);
  if (status.value) next.set('status', status.value);
  if (owner.value.trim()) next.set('owner_agent', owner.value.trim());
  if (source.value) next.set('source_agent', source.value);
  if (range.value) next.set('range', range.value);
  if ($('tasks-archived').checked) next.set('archived', '1');
  location.search = next.toString();
});
```

`load()` 将参数拼到 `/v1/tasks`：

```js
query.push('q=' + enc(q.value.trim()));
query.push('owner_agent=' + enc(owner.value.trim()));
query.push('source_agent=' + enc(source.value));
if (range.value) {
  const days = Number(range.value);
  const after = new Date(Date.now() - days * 86400000).toISOString();
  query.push('updated_after=' + enc(after));
}
```

- [ ] **Step 3: 样式**

```css
.task-filters { display: grid; grid-template-columns: minmax(220px, 2fr) repeat(5, minmax(120px, 1fr)) auto auto; gap: 10px; align-items: center; padding: 14px 0 18px; border-bottom: 1px solid var(--stroke); }
@media (max-width: 980px) { .task-filters { grid-template-columns: 1fr 1fr; } }
```

- [ ] **Step 4: 验证**

Run:

```bash
node --check internal/daemon/webui/app.js
go test ./internal/daemon -v
```

Expected: PASS；浏览器里输入关键词/状态/来源后 URL 变化、结果刷新。

- [ ] **Step 5: 汇报并提交**

经确认后提交。

---

### Task 4: MCP 配置生成与教程页

**Files:**
- Create: `internal/daemon/mcp_snippets.go`
- Modify: `internal/daemon/server.go`
- Create: `internal/daemon/webui/mcp.html`
- Modify: `internal/daemon/webui.go`
- Test: `internal/daemon/mcp_snippets_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestMCPSnippetsUseLivePortAndToken(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) { c.Port = 47123; c.Token = "tok-live" })
	rec := webDo(t, s, http.MethodGet, "/v1/mcp/snippets", webToken, nil, nil)
	if rec.Code != http.StatusOK { t.Fatalf("code=%d", rec.Code) }
	body := rec.Body.String()
	for _, want := range []string{"47123", "AGENT_MEMORY_TOKEN", "mcpServers", "opencode"} {
		if !strings.Contains(body, want) { t.Fatalf("missing %q: %s", want, body) }
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/daemon -run TestMCPSnippetsUseLivePortAndToken -v`

Expected: 404。

- [ ] **Step 3: 实现 snippet 生成**

`mcp_snippets.go`：

```go
type mcpSnippetsResponse struct {
	Port        int               `json:"port"`
	Binary      string            `json:"binary"`
	TokenMasked string            `json:"token_masked"`
	Verified    []string          `json:"verified"`
	Snippets    map[string]string `json:"snippets"`
}

// daemon.Config 增加：
//   MCPBinary string
// 由 cmdServe 从 cfg.MCP.BinaryPath 注入；空时 mcpBinaryPath 自动找同目录 agent-memory.exe。

func (s *Server) mcpBinaryPath() string {
	if s.cfg.MCPBinary != "" { return s.cfg.MCPBinary }
	exe, err := os.Executable()
	if err != nil { return "" }
	dir := filepath.Dir(exe)
	if p := filepath.Join(dir, "agent-memory.exe"); fileExists(p) { return p }
	return exe
}
```

生成四个字符串：

```go
func (s *Server) mcpSnippets() map[string]string {
	binary := s.mcpBinaryPath()
	port := s.Port()
	portText := strconv.Itoa(port)
	args := []string{"mcp", "--port", portText}
	env := map[string]string{"AGENT_MEMORY_TOKEN": s.cfg.Token}
	marshal := func(v any) string {
		raw, _ := json.MarshalIndent(v, "", "  ")
		return string(raw)
	}
	serverEntry := map[string]any{"command": binary, "args": args, "env": env}
	qoder := marshal(map[string]any{"mcpServers": map[string]any{"agent-memory": serverEntry}})
	claude := marshal(map[string]any{"mcpServers": map[string]any{"agent-memory": serverEntry}})
	opencode := marshal(map[string]any{"mcp": map[string]any{"agent-memory": map[string]any{
		"type": "local", "command": append([]string{binary}, args...),
		"environment": env,
	}}})
	generic := "command: " + binary + "\nargs: [\"mcp\", \"--port\", \"" + portText + "\"]\nenv:\n  AGENT_MEMORY_TOKEN: " + s.cfg.Token + "\n"
	return map[string]string{"qoder": qoder, "opencode": opencode, "claude": claude, "generic": generic}
}
```

`Server` 增加 `Port() int`，返回当前实际监听端口。响应中的 token 只返回掩码，`snippets` 内使用真实 token。

- [ ] **Step 4: 路由与页面**

注册 `GET /v1/mcp/snippets` 与 `GET /mcp`；页面四标签 + 代码块 + 复制按钮。复制使用 `navigator.clipboard.writeText`，失败回退 `window.prompt`。

- [ ] **Step 5: 测试**

Run:

```bash
go test ./internal/daemon -run 'MCP|Pages' -v
node --check internal/daemon/webui/app.js
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 5: 设置 API 与配置写入

**Files:**
- Create: `internal/daemon/settings.go`
- Modify: `internal/daemon/server.go`
- Modify: `internal/daemon/webui.go`
- Create: `internal/daemon/webui/settings.html`
- Test: `internal/daemon/settings_test.go`

- [ ] **Step 1: 写失败测试**

```go
func TestSettingsRoundTripMasksLLMKey(t *testing.T) {
	s, _ := newTestServer(t)
	rec := webDo(t, s, http.MethodPost, "/v1/settings/llm", webToken, map[string]any{
		"enabled": true, "base_url": "http://127.0.0.1:11434/v1", "model": "qwen",
		"api_key": "sk-secret",
	}, nil)
	if rec.Code != http.StatusOK { t.Fatalf("save code=%d body=%s", rec.Code, rec.Body.String()) }
	rec = webDo(t, s, http.MethodGet, "/v1/settings", webToken, nil, nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "sk-secret") {
		t.Fatalf("settings leaked key: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"api_key_configured":true`) {
		t.Fatalf("missing configured flag: %s", rec.Body.String())
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/daemon -run TestSettingsRoundTripMasksLLMKey -v`

Expected: 404。

- [ ] **Step 3: 实现设置模型与 API**

`settings.go` 定义：

```go
type settingsView struct {
	Server  serverSettings  `json:"server"`
	Desktop settingsDesktop `json:"desktop"`
	Scan    scanSettings    `json:"scan"`
	LLM     llmSettings     `json:"llm"`
	Data    dataSettings    `json:"data"`
}

type settingsDesktop struct {
	Autostart   bool   `json:"autostart"`
	Command     string `json:"command"`
	CloseToTray bool   `json:"close_to_tray"`
}
```

- GET `/v1/settings`：读取 config、autostart 状态、DB 大小、LLM key 是否配置。
- POST `/v1/settings/service`：校验端口/托盘，写 config.toml。
- POST `/v1/settings/scan`：校验间隔/agent/budget，写 config。
- POST `/v1/settings/llm`：写 config + `config.SaveLLMKey`；空 api_key 表示保持原值，显式 `clear_api_key=true` 才删除。
- POST `/v1/settings/llm/test`：调用摘要客户端测试。

所有写接口使用 writeAuth，保存前备份 config.toml 到 `.bak-<timestamp>`。

- [ ] **Step 4: 设置页骨架**

`/settings` 单页左侧四个锚点/按钮，右侧四个 `<section>`；每个 section 独立表单与保存按钮。页面 JS 读 GET 填充，保存 POST 对应分组。

- [ ] **Step 5: 测试**

Run:

```bash
go test ./internal/daemon -run 'Settings|Pages' -v
node --check internal/daemon/webui/app.js
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 6: 端口热切换、MCP relink 与扫描重载

**Files:**
- Modify: `internal/daemon/server.go`
- Modify: `internal/cli/serve.go`
- Create: `internal/surface/relink.go`
- Test: `internal/daemon/rebind_test.go`
- Test: `internal/surface/relink_test.go`

- [ ] **Step 1: 写失败测试**

`rebind_test.go`：

```go
func waitPortFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if port, err := ReadPort(path); err == nil && port > 0 {
			return port
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port file %s not ready", path)
	return 0
}

func TestServerRebindPort(t *testing.T) {
	s, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	portPath := filepath.Join(t.TempDir(), "port")
	go s.ListenAndServe(ctx, portPath)
	oldPort := waitPortFile(t, portPath)

	if err := s.Rebind(ctx, 0); err != nil {
		t.Fatal(err)
	}
	newPort := waitPortFile(t, portPath)
	if newPort == oldPort {
		t.Fatalf("port did not change: %d", newPort)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/healthz", newPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz code = %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/daemon -run TestServerRebindPort -v`

Expected: `Rebind` undefined。

- [ ] **Step 3: 重构监听器生命周期**

`Server` 增加：

```go
mu       sync.Mutex
srv      *http.Server
ln       net.Listener
portPath string
port     int
```

`ListenAndServe` 调用内部 `bind(port)`；`Rebind(newPort)`：

1. `net.Listen` 新端口；
2. 写 port 文件；
3. 启动新 `http.Server`；
4. 优雅关闭旧 server；
5. 更新 `s.port`。

失败时关闭新 listener，保留旧 server。

- [ ] **Step 4: 扫描热重载**

`cmdServe` 抽出 `scanController`：

```go
type scanController struct {
	mu     sync.Mutex
	cancel context.CancelFunc
}
func (c *scanController) Reload(ctx context.Context, cfg config.Config) error
```

关闭旧 scanLoop ctx，重新 `buildAdapters(cfg)` 和 `newScanOnce`，启动新 loop。daemon.Config 增加 `ReloadScan func(context.Context, config.Config) error`。

- [ ] **Step 5: 端口 relink**

`surface.RelinkPort(ctx, st, home, binary, token, oldPort, newPort)`：

- 查 `agent_link` 全部记录；
- 对 qoder/opencode/claude 逐个 `PlanLink` + `ApplyLink`；
- 备份由 `UpsertJSONKey` 自动完成；
- 返回修改文件列表。

设置 service 保存时：写配置 → relink → daemon.Rebind/ReloadScan；失败回滚 config 与已修改 MCP 文件。

- [ ] **Step 6: 运行测试**

Run:

```bash
go test ./internal/daemon ./internal/surface ./internal/cli -run 'Rebind|Relink|Scan' -v
```

Expected: PASS。

- [ ] **Step 7: 汇报并提交**

经确认后提交。

---

### Task 7: 摘要表、OpenAI 客户端与 worker

**Files:**
- Create: `internal/store/summary.go`
- Create: `internal/store/summary_test.go`
- Create: `internal/summarize/client.go`
- Create: `internal/summarize/worker.go`
- Create: `internal/summarize/worker_test.go`
- Modify: `internal/store/schema.go`
- Modify: `internal/cli/serve.go`

- [ ] **Step 1: 写失败测试**

`summary_test.go`：

```go
func TestSummaryQueueStates(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if err := s.MarkSummaryPending(ctx, "memory", 7, "hash-1", time.Now()); err != nil { t.Fatal(err) }
	job, ok, err := s.ClaimSummary(ctx, "memory", 7, time.Now())
	if err != nil || !ok { t.Fatalf("claim ok=%v err=%v", ok, err) }
	if job.State != "processing" { t.Fatalf("job = %+v", job) }
	if err := s.SaveSummary(ctx, "memory", 7, "hash-1", "摘要", "qwen", time.Now()); err != nil { t.Fatal(err) }
	row, ok, err := s.Summary(ctx, "memory", 7)
	if err != nil || !ok || row.Summary != "摘要" || row.State != "ready" { t.Fatalf("row=%+v err=%v", row, err) }
}
```

`worker_test.go` 用 `httptest.Server` 模拟 `/chat/completions`，断言 worker 把 memory 文本发过去并保存摘要。

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store ./internal/summarize -run 'Summary|Worker' -v`

Expected: 未定义。

- [ ] **Step 3: 建 summary 表**

在 `schema.go` 增加 spec 中的 summary DDL 与索引：

```sql
CREATE TABLE IF NOT EXISTS summary (
  entity_kind TEXT NOT NULL,
  entity_id INTEGER NOT NULL,
  source_hash TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'pending',
  model TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  requested_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(entity_kind, entity_id)
);
```

- [ ] **Step 4: 实现 store 方法**

- `MarkSummaryPending`
- `ClaimSummary`（事务把 pending/failed 置 processing）
- `SaveSummary`
- `FailSummary`
- `Summary`
- `SummaryCounts`
- `ResetProcessingSummaries`

所有方法使用 Store 单连接，不并发查询。

- [ ] **Step 5: OpenAI 客户端**

`summarize/client.go`：

```go
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	Timeout time.Duration
	HTTP    *http.Client
}

func (c *Client) Summarize(ctx context.Context, prompt string) (string, error) {
	body := map[string]any{
		"model": c.Model,
		"messages": []map[string]string{
			{"role": "system", "content": "你是记忆整理助手，只输出简洁中文摘要，不编造。"},
			{"role": "user", "content": prompt},
		},
		"temperature": 0.2,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: c.Timeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("llm %s: %s", resp.Status, string(payload))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("llm returned empty summary")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
```

校验 base_url 以 http/https 开头；超时默认 30s；响应空文本视为错误。

- [ ] **Step 6: worker**

`worker.go`：

- ticker 5s；
- 每轮从 store 取 pending/failed 且 attempts < 3；
- 加载 memory/task 源文本，生成 prompt；
- 调 client，成功 SaveSummary，失败 FailSummary；
- 未启用/缺少配置直接返回。

在 cmdServe 中：如果 cfg.LLM.Enabled 且配置完整，启动 worker goroutine；否则不启动。

- [ ] **Step 7: 测试**

Run:

```bash
go test ./internal/store ./internal/summarize -v
```

Expected: PASS；未配置时不发请求。

- [ ] **Step 8: 汇报并提交**

经确认后提交。

---

### Task 8: 摘要触发与展示

**Files:**
- Modify: `internal/daemon/mcp.go`（memory_add）
- Modify: `internal/daemon/web.go`（addMemory/update）
- Modify: `internal/ingest/ingest.go` 或 `tasks.go`
- Modify: `internal/domain/types.go`（summary fields）
- Modify: `internal/store/memory.go` / `task.go`（join summary）
- Modify: `internal/daemon/mcp.go` / `tasks.go`（响应字段）
- Modify: `internal/compose/compose.go`
- Modify: `internal/daemon/webui/app.js`

- [ ] **Step 1: 写失败测试**

- memory_add 后 summary 行 state=pending。
- 未配置 LLM 时不创建 pending。
- task 同步后 summary pending。
- `ListMemories`/`ListTasks` JSON 含 `summary`、`summary_state`。
- Handoff 优先使用 ready summary。

- [ ] **Step 2: 触发点**

- `addMemory` 成功后，若 LLM 启用：`MarkSummaryPending("memory", id, hash)`。
- task 创建/更新/同步后：`MarkSummaryPending("task", id, taskHash)`。
- memory/task 内容变化时，source hash 变化才重新排队。

- [ ] **Step 3: 显示**

- `MemoryItem`/`Task` 增加 transient `Summary`、`SummaryState`、`SummaryError` 字段，store 查询时 left join summary 填充。
- 记忆列表显示摘要，带“AI 摘要”标记；编辑页原文不变，另显示摘要。
- 任务列表/详情/HANDOFF 优先显示摘要；摘要 pending/failed 时用原文。
- `RenderTaskExport` 优先摘要。

- [ ] **Step 4: 测试**

Run:

```bash
go test ./internal/store ./internal/daemon ./internal/compose -v
node --check internal/daemon/webui/app.js
```

Expected: PASS。

- [ ] **Step 5: 汇报并提交**

经确认后提交。

---

### Task 9: 数据备份、导出与导入

**Files:**
- Create: `internal/store/data.go`
- Create: `internal/store/data_test.go`
- Create: `internal/daemon/data.go`
- Create: `internal/daemon/data_test.go`
- Modify: `internal/daemon/server.go`

- [ ] **Step 1: 写失败测试**

- 备份生成文件且可打开；
- 导出 JSON 含 projects/memories/tasks，不含 token；
- 导入预览返回新增/合并计数；
- apply 后记忆按去重键合并、任务按来源/目标合并；
- 失败时原库不变。

- [ ] **Step 2: 备份**

`Store.Backup(ctx, dir) (path,error)`：

```go
_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, filepath.Join(dir, name))
```

保留最近 10 份。

- [ ] **Step 3: 导出 JSON**

定义 `DataExport` 结构，查询项目、记忆、任务+步骤+摘要；写临时文件；HTTP 下载后删除。

- [ ] **Step 4: 导入预览/应用**

- 上传文件存 `DataDir/imports/<token>.json`，10 分钟过期。
- 预览解析并统计。
- apply 前备份；事务写入；冲突按 spec 合并；失败回滚。

- [ ] **Step 5: 测试**

Run:

```bash
go test ./internal/store ./internal/daemon -run 'Backup|Export|Import' -v
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 10: 设置页与 MCP 教程页交互

**Files:**
- Modify: `internal/daemon/webui/settings.html`
- Modify: `internal/daemon/webui/mcp.html`
- Modify: `internal/daemon/webui/app.js`
- Modify: `internal/daemon/webui/app.css`
- Modify: `internal/daemon/webui/layout.html`

- [ ] **Step 1: 设置页四分区**

- 左侧导航按钮切换右侧 section，不刷新页面。
- 每个分区独立读取/保存。
- 服务分区保存后调用 `/v1/settings/service`；如返回 `new_port`，跳转新端口。
- LLM 分区保存/测试；Key 显示掩码。
- 扫描分区保存后提示重载状态。
- 数据分区显示大小、最近备份、备份按钮、导出链接、导入文件选择与预览确认。

- [ ] **Step 2: MCP 页**

- 调 `/v1/mcp/snippets` 填充四个 tab 的 `<pre><code>`。
- 复制按钮调用 clipboard；失败显示文本框。
- token 掩码，但复制内容含真实 token。
- 提示端口修改后重新复制配置。

- [ ] **Step 3: 导航**

主导航增加“MCP”“设置”；`/settings` 左侧分区在窄屏变横向标签。

- [ ] **Step 4: 验证**

```bash
node --check internal/daemon/webui/app.js
go test ./internal/daemon -v
```

浏览器实测：复制、四分区保存、端口重启后自动跳转、备份/导出/导入预览、LLM 测试。

- [ ] **Step 5: 汇报并提交**

经确认后提交。

---

### Task 11: 文档与全量验收

**Files:**
- Modify: `docs/usage.md`
- Modify: `docs/superpowers/specs/2026-10-31-settings-mcp-llm-design.md`（如有偏差）

- [ ] **Step 1: 更新文档**

补充：

- 任务筛选参数与页面操作。
- MCP 教程与三种已验证配置、通用示例。
- 设置四分区、端口修改与自动 relink、托盘行为。
- LLM 配置、DPAPI 密钥、异步摘要与失败回退。
- 备份/导出/导入规则与风险。

- [ ] **Step 2: 全量验证**

```bash
go test ./...
go vet ./...
node --check internal/daemon/webui/app.js
```

- [ ] **Step 3: 桌面验收**

- 任务筛选 URL 保留。
- MCP 配置复制可直接使用。
- 改端口后自动 relink + 页面跳转；重启应用仍生效。
- 托盘关闭行为。
- 未配置 LLM 全功能可用；配置 fake/Ollama 后摘要异步出现。
- 备份、导出、导入预览与合并。
- 截图首页、任务筛选、MCP、设置四分区、摘要展示。

- [ ] **Step 4: 汇报并提交**

经用户确认后提交。

---

## 计划自检

- Spec 覆盖：任务筛选（Task 2/3）、MCP 教程（Task 4/10）、设置页（Task 1/5/6/10）、LLM 摘要（Task 7/8）、数据（Task 9/10）、文档验收（Task 11）。
- 类型一致：`LLMConfig`、`ServerConfig`、`DesktopConfig`、`MCPConfig`、`summary`、`TaskFilter`、`SettingsView` 在各任务中复用。
- 未使用子 agent；每个提交点都要求先向用户汇报，遵守“代码不自动 commit”。
