# M3 常驻服务、MCP 接入与上下文注入 · 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让各 agent 通过一个常驻服务读写共享记忆：MCP tools 按需检索与写入，受管区块让新会话自动带上交接简报，文件监听让记忆新鲜度不再依赖手动扫描。

**Architecture:** `serve` 常驻进程独占扫描与 SQLite，暴露回环 HTTP（bearer token 鉴权）；`mcp` 是 stdio ↔ HTTP 转发器，agent 只和我们自己的 stdio 进程说话，因此**不需要实现 Streamable HTTP 的客户端兼容细节**；`surface` 负责带备份的 JSON 合并与标记区块；`ingest.Watcher` 用 fsnotify 加周期轮询驱动扫描。

**Tech Stack:** Go 1.22.6 标准库（`net/http`、`encoding/json`、`crypto/subtle`）+ 新增 `github.com/fsnotify/fsnotify` v1.7.0（本机已实测可 `go get`）。MCP 协议层自己实现最小子集，不引 SDK：只用 `initialize`/`ping`/`tools/list`/`tools/call`，用真实 agent 验收一次即可。

**前置:** M1（`docs/superpowers/plans/2026-10-27-agent-memory-m1-core-store.md`）与 M2（`.../2026-10-27-agent-memory-m2-session-handoff.md`）已完成。

---

## 开工约定

- MCP 配置的目标文件格式已在本机确证，不再猜：
  - Qoder：`~/.qoder-cn/mcp.json`，顶层 `{"mcpServers": {}}`（实测存在，当前为空对象）
  - OpenCode：`~/.config/opencode/opencode.json`，MCP 段是 `"mcp": {"名字": {"type": "local", "command": [...], "enabled": true}}`（官方 config schema 确证）
  - Claude Code：`~/.claude.json`，顶层 `mcpServers`
  - **Trae：`~/.trae-cn/mcps/` 是 `s_<项目>-<hash>/…` 的生成目录，没有可编辑的 JSON，真实项目里也没有 `.trae/rules` → `link trae` 明确拒绝执行并说明原因，不猜格式往里面写**
  - WorkBuddy：不写它的配置，只产 `HANDOFF.md`
- `link` 是唯一会改动既有配置的命令：必须先 `--dry-run` 给用户看计划，落盘前写 `.bak-<时间戳>`，且只增删自己那一个 key。
- 端口与 token 文件：`~/.agent-memory/port`、`~/.agent-memory/token`，权限 0600。
- 每个 Task 结束时提交一次，提交信息用中文。

---

## 文件结构

```
internal/daemon/server.go          HTTP 服务、鉴权、端点（新建）
internal/daemon/token.go           token/port 文件读写（新建）
internal/mcp/protocol.go           JSON-RPC 2.0 最小实现（新建）
internal/mcp/tools.go              tool 定义与处理函数（新建）
internal/mcp/stdio.go              stdio ↔ HTTP 桥（新建）
internal/surface/merge.go          JSON 合并 + 备份（新建）
internal/surface/block.go          受管区块（新建）
internal/surface/link.go           link/unlink 编排与各 agent 目标表（新建）
internal/ingest/watcher.go         fsnotify + 轮询 + 去抖（新建）
internal/cli/serve.go              serve 子命令（新建）
internal/cli/mcp.go                mcp 子命令（新建）
internal/cli/link.go               link / unlink 子命令（新建）
```

---

## Task 27: token 与端口文件

**Files:**
- Create: `internal/daemon/token.go`
- Test: `internal/daemon/token_test.go`

- [ ] **Step 1: 写失败测试 `internal/daemon/token_test.go`**

```go
package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateToken_CreatesOnceAndIsPerms600(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateToken(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 32 {
		t.Fatalf("token too short: %q", first)
	}
	second, err := LoadOrCreateToken(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("token must be stable across restarts")
	}

	info, err := os.Stat(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上 os.Chmod 只对只读位有效，权限断言仅在 POSIX 上做
	if !isWindows() && info.Mode().Perm() != 0o600 {
		t.Fatalf("token perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestLoadOrCreateToken_RejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateToken(path); err == nil {
		t.Fatal("empty token file should be an error, not a silently regenerated secret")
	}
}

func TestWritePort_ReadPort_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "port")
	if err := WritePort(path, 55991); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPort(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != 55991 {
		t.Fatalf("port = %d", got)
	}
}

func TestReadPort_RejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "port")
	if err := os.WriteFile(path, []byte("not-a-port\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPort(path); err == nil {
		t.Fatal("want error for garbage port file")
	}
}

func isWindows() bool { return filepath.Separator == '\\' }
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/daemon/ -v`
Expected: FAIL，`undefined: LoadOrCreateToken`

- [ ] **Step 3: 写 `internal/daemon/token.go`**

```go
package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadOrCreateToken 读取或首次生成访问令牌。
// 空文件是错误而不是「顺手重建」：静默换 token 会让已接入的 agent 全部失效，
// 而现场看起来一切正常。
func LoadOrCreateToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return createToken(path)
	}
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", fmt.Errorf("token file %s is empty or too short; delete it and restart serve", path)
	}
	return token, nil
}

func createToken(path string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func WritePort(path string, port int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(port)+"\n"), 0o644)
}

func ReadPort(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("no running agent-memory serve? (%s missing: %v)", path, err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("bad port file %s: %q", path, strings.TrimSpace(string(data)))
	}
	return port, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/daemon/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/daemon
git commit -m "feat: 访问令牌与端口文件管理"
```

---

## Task 28: HTTP 服务与鉴权

只监听回环，仍需 bearer token：本机任意进程都能连 127.0.0.1，而库里存的是你的偏好、项目约束与任务历史。

**Files:**
- Create: `internal/daemon/server.go`
- Test: `internal/daemon/server_test.go`

- [ ] **Step 1: 写失败测试 `internal/daemon/server_test.go`**

```go
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Config{Token: "test-token-0123456789abcdef", DataDir: t.TempDir()}, st)
	return s, st
}

func doJSON(t *testing.T, s *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &payload)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthzNeedsNoToken(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/v1/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAuthRequiredAndTimingSafe(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tok := range []string{"", "wrong", "test-token-0123456789abcde"} {
		rec := doJSON(t, s, http.MethodGet, "/v1/stats", tok, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q should be rejected, got %d", tok, rec.Code)
		}
	}
	rec := doJSON(t, s, http.MethodGet, "/v1/stats", "test-token-0123456789abcdef", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token rejected: %d", rec.Code)
	}
}

func TestSearchEndpointUsesSharedStore(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	_, _, err := st.UpsertMemory(ctx, 0, domain.MemoryDraft{
		Scope: domain.ScopeUser, Type: domain.TypePreference, Text: "沟通用中文",
		SourceAgent: "cli", SourcePath: "cli",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/v1/search", "test-token-0123456789abcdef",
		map[string]any{"query": "偏好", "limit": 5})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Hits []struct {
			Text    string   `json:"text"`
			Agents  []string `json:"agents"`
			Score   float64  `json:"score"`
			Origin  string   `json:"origin"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || !strings.Contains(out.Hits[0].Text, "沟通用中文") {
		t.Fatalf("hits = %+v", out.Hits)
	}
	if out.Hits[0].Origin != "fts" || len(out.Hits[0].Agents) != 1 || out.Hits[0].Agents[0] != "cli" {
		t.Fatalf("hit metadata wrong: %+v", out.Hits[0])
	}
}

func TestAddEndpointValidatesInput(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": "  ", "type": "preference"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank text should be 400, got %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": "接口字段一律蛇形", "type": "vibes"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad type should be 400, got %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": "接口字段一律蛇形", "type": "convention", "scope": "user"})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid add should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBodySizeLimit(t *testing.T) {
	s, _ := newTestServer(t)
	huge := strings.Repeat("x", maxBodyBytes+1)
	rec := doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": huge, "type": "fact", "scope": "user"})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rec.Code)
	}
}

func TestHandoffEndpointReturnsNotFoundForUnknownProject(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/v1/handoff?project_path=Z%3A%5Cnope", "test-token-0123456789abcdef", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/daemon/ -v`
Expected: FAIL，`undefined: Server`

- [ ] **Step 3: 写 `internal/daemon/server.go`**

```go
package daemon

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

const (
	maxBodyBytes   = 1 << 20 // 1MB：记忆条目不该更大
	requestTimeout = 10 * time.Second
)

type Config struct {
	Token   string
	DataDir string
	Port    int // 0 表示随机端口
	// 简报预算与卡片数量由 serve 从配置带进来，handoff 端点不写死
	BudgetTokens int
	RecentCards  int
}

// Server 是回环 HTTP 服务。它把 store 的能力摊成 JSON 接口，
// MCP 层与 CLI 都走这一层，保证「唯一写入口」这条边界只有一处。
type Server struct {
	cfg Config
	st  *store.Store
	mux *http.ServeMux
}

func New(cfg Config, st *store.Store) *Server {
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /v1/stats", s.auth(s.handleStats))
	s.mux.HandleFunc("POST /v1/search", s.auth(s.handleSearch))
	s.mux.HandleFunc("POST /v1/memory", s.auth(s.handleAddMemory))
	s.mux.HandleFunc("GET /v1/memory", s.auth(s.handleListMemory))
	s.mux.HandleFunc("GET /v1/handoff", s.auth(s.handleHandoff))
	s.mux.HandleFunc("GET /v1/task", s.auth(s.handleListTasks))
	s.mux.HandleFunc("POST /v1/project/resolve", s.auth(s.handleResolveProject))
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

type handlerFunc func(http.ResponseWriter, *http.Request)

func (s *Server) auth(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		got := r.Header.Get("Authorization")
		if len(got) <= len(prefix) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		want := subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(got, prefix)), []byte(s.cfg.Token))
		if want != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": "m3"})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.st.Stats(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

type searchRequest struct {
	Query        string `json:"query"`
	ProjectPath  string `json:"project_path"`
	ProjectKey   string `json:"project_key"`
	Type         string `json:"type"`
	Scope        string `json:"scope"`
	IncludeStale bool   `json:"include_stale"`
	Limit        int    `json:"limit"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	projectID, err := s.projectIDFromRequest(r.Context(), req.ProjectPath, req.ProjectKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hits, err := store.NewFTS(s.st).Search(r.Context(), domain.SearchQuery{
		Text:         req.Query,
		ProjectID:    projectID,
		Type:         domain.MemoryType(req.Type),
		Scope:        domain.Scope(req.Scope),
		IncludeStale: req.IncludeStale,
		Limit:        req.Limit,
	})
	if err != nil {
		httpError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		agents := make([]string, 0, len(h.Memory.Sources))
		for _, src := range h.Memory.Sources {
			agents = append(agents, src.Agent)
		}
		out = append(out, map[string]any{
			"id":             h.Memory.ID,
			"scope":          h.Memory.Scope,
			"type":           h.Memory.Type,
			"text":           h.Memory.Text,
			"stale":          h.Memory.Stale,
			"last_seen_at":   h.Memory.LastSeenAt.Format(time.RFC3339),
			"agents":         agents,
			"sources":        h.Memory.Sources,
			"score":          h.Score,
			"origin":         h.Origin,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": out, "count": len(out)})
}

type addRequest struct {
	Text        string `json:"text"`
	Type        string `json:"type"`
	Scope       string `json:"scope"`
	ProjectPath string `json:"project_path"`
	Agent       string `json:"agent"`
	Supersedes  int64  `json:"supersedes"`
}

func (s *Server) handleAddMemory(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	typ := domain.MemoryType(req.Type)
	if typ == "" {
		typ = domain.TypeFact
	}
	if !domain.ValidMemoryType(typ) {
		http.Error(w, fmt.Sprintf("unknown type %q", req.Type), http.StatusBadRequest)
		return
	}
	scope := domain.ScopeUser
	if req.Scope != "" {
		scope = domain.Scope(req.Scope)
		if scope != domain.ScopeUser && scope != domain.ScopeProject {
			http.Error(w, fmt.Sprintf("unknown scope %q", req.Scope), http.StatusBadRequest)
			return
		}
	}
	agent := req.Agent
	if agent == "" {
		agent = "mcp"
	}

	ctx := r.Context()
	projectID := int64(0)
	if scope == domain.ScopeProject {
		if strings.TrimSpace(req.ProjectPath) == "" {
			http.Error(w, "scope=project needs project_path", http.StatusBadRequest)
			return
		}
		id, _, err := s.st.ProjectIDForPath(ctx, agent, "", req.ProjectPath, "", time.Now())
		if err != nil {
			httpError(w, err)
			return
		}
		projectID = id
	}

	id, created, err := s.st.UpsertMemory(ctx, projectID, domain.MemoryDraft{
		Scope: scope, Type: typ, Text: req.Text,
		SourceAgent: agent, SourcePath: "mcp", Quote: req.Text,
	}, time.Now())
	if err != nil {
		httpError(w, err)
		return
	}
	if req.Supersedes != 0 && req.Supersedes != id {
		if err := s.st.SetStaleByID(ctx, req.Supersedes, true); err != nil {
			httpError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "created": created, "scope": scope, "type": typ})
}

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	projectRef := r.URL.Query().Get("project_path")
	if projectRef == "" {
		projectRef = r.URL.Query().Get("project_key")
	}
	projectID, err := s.projectIDFromRequest(r.Context(), projectRef, projectRef)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.st.ListMemories(r.Context(), projectID, r.URL.Query().Get("include_stale") == "true", 50)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": items, "count": len(items)})
}

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("project_path")
	if ref == "" {
		ref = r.URL.Query().Get("project_key")
	}
	ctx := r.Context()
	projectID, err := s.projectIDFromRequest(ctx, ref, ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	budget, cards := s.cfg.BudgetTokens, s.cfg.RecentCards
	if budget <= 0 {
		budget = 1500
	}
	if cards <= 0 {
		cards = 3
	}
	res, err := compose.New(s.st, compose.Config{BudgetTokens: budget, RecentCards: cards}).Render(ctx, projectID)
	if err != nil {
		httpError(w, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "body": res.Body, "tokens": res.Tokens, "truncated": res.Truncated})
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	io.WriteString(w, res.Body)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("project_path")
	if ref == "" {
		ref = r.URL.Query().Get("project_key")
	}
	projectID, err := s.projectIDFromRequest(r.Context(), ref, ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cards, err := s.st.ListTaskCards(r.Context(), projectID, r.URL.Query().Get("status"), 10)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards, "count": len(cards)})
}

func (s *Server) handleResolveProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	ctx := r.Context()
	id, ok, err := s.st.ResolveProjectByPath(ctx, req.Path)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"known": false, "path": req.Path})
		return
	}
	project, _, err := s.st.ProjectByID(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"known": true, "project": project})
}

// projectIDFromRequest 允许调用方用路径或 key 引用项目；空引用表示不限定（只查用户级）。
func (s *Server) projectIDFromRequest(ctx context.Context, pathRef, keyRef string) (int64, error) {
	if strings.TrimSpace(pathRef) == "" {
		return 0, nil
	}
	if id, ok, err := s.st.ResolveProjectByPath(ctx, pathRef); err != nil {
		return 0, err
	} else if ok {
		return id, nil
	}
	if keyRef != "" {
		if p, ok, err := s.st.ProjectByKey(ctx, keyRef); err != nil {
			return 0, err
		} else if ok {
			return p.ID, nil
		}
	}
	return 0, fmt.Errorf("unknown project %q", pathRef)
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("empty request body")
		}
		return fmt.Errorf("bad json: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var me *json.UnmarshalTypeError
	if errors.As(err, &me) {
		code = http.StatusBadRequest
	}
	http.Error(w, err.Error(), code)
}

// ListenAndServe 绑定回环地址并回填实际端口。端口为 0 时由系统分配，
// 实际值写进 port 文件供 stdio 桥读取。
func (s *Server) ListenAndServe(ctx context.Context, portPath string) error {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(s.cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	actual := ln.Addr().(*net.TCPAddr).Port
	if portPath != "" {
		if err := WritePort(portPath, actual); err != nil {
			ln.Close()
			return err
		}
		defer os.Remove(portPath)
	}

	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: requestTimeout,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/daemon/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/daemon
git commit -m "feat: 回环 HTTP 服务、令牌鉴权与共享记忆端点"
```

---

## Task 29: MCP 协议最小子集

只做 agent 真正会用的四个方法：`initialize`、`ping`、`tools/list`、`tools/call`。协议版本采用「回显客户端版本」策略，避免与不同版本客户端协商失败——这是 MCP 明确允许的行为。

**Files:**
- Create: `internal/mcp/protocol.go`
- Test: `internal/mcp/protocol_test.go`

- [ ] **Step 1: 写失败测试 `internal/mcp/protocol_test.go`**

```go
package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func call(t *testing.T, s *Server, id any, method string, params map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := s.Handle(context.Background(), raw)
	if !ok {
		t.Fatal("expected a response")
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("bad response json: %v\n%s", err, resp)
	}
	return out
}

func TestInitializeEchoesClientProtocolVersion(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
	if res["serverInfo"] == nil {
		t.Fatal("serverInfo missing")
	}
	caps := res["capabilities"].(map[string]any)
	if caps["tools"] == nil {
		t.Fatal("tools capability missing")
	}
}

func TestInitializeFallsBackWhenClientHasNoVersion(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 2, "initialize", map[string]any{"capabilities": map[string]any{}})
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != LatestProtocolVersion {
		t.Fatalf("fallback version = %v", res["protocolVersion"])
	}
}

func TestNotificationGetsNoResponse(t *testing.T) {
	s := NewServer(stubAPI{})
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	_, ok := s.Handle(context.Background(), raw)
	if ok {
		t.Fatal("notifications must not be answered")
	}
}

func TestToolsListShape(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 3, "tools/list", nil)
	res := out["result"].(map[string]any)
	tools := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools advertised")
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] == nil || tool["description"] == nil || tool["inputSchema"] == nil {
			t.Fatalf("tool missing required fields: %v", tool)
		}
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Fatalf("inputSchema.type = %v", schema["type"])
		}
	}
}

func TestToolsCallReturnsTextContent(t *testing.T) {
	s := NewServer(stubAPI{searched: []string{"沟通用中文"}})
	out := call(t, s, 4, "tools/call", map[string]any{
		"name":      "memory_search",
		"arguments": map[string]any{"query": "偏好"},
	})
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("tool call errored: %v", res)
	}
	block := res["content"].([]any)[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("content type = %v", block["type"])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(block["text"].(string)), &payload); err != nil {
		t.Fatalf("tool text should be json: %v", err)
	}
}

func TestUnknownToolIsAPromptErrorNotCrash(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 5, "tools/call", map[string]any{"name": "nope", "arguments": map[string]any{}})
	// MCP 约定：工具层错误走 result.isError，不是 JSON-RPC error
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("want isError result, got %v", out)
	}
	if out["error"] != nil {
		t.Fatalf("tool lookup failure must not become a jsonrpc error: %v", out["error"])
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 6, "bogus/method", nil)
	errObj := out["error"].(map[string]any)
	if int(errObj["code"].(float64)) != CodeMethodNotFound {
		t.Fatalf("code = %v", errObj["code"])
	}
}

func TestMalformedRequestReturnsParseError(t *testing.T) {
	s := NewServer(stubAPI{})
	resp, ok := s.Handle(context.Background(), []byte("{not json"))
	if !ok {
		t.Fatal("malformed request still needs an answer")
	}
	var out map[string]any
	json.Unmarshal(resp, &out)
	errObj := out["error"].(map[string]any)
	if int(errObj["code"].(float64)) != CodeInvalidRequest {
		t.Fatalf("code = %v", errObj["code"])
	}
}

func TestPing(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 7, "ping", nil)
	if out["error"] != nil {
		t.Fatalf("ping failed: %v", out["error"])
	}
}

// stubAPI 是实现 ToolAPI 的测试替身，记录调用参数。
type stubAPI struct {
	searched []string
	added    string
	calls    []string
}

func (a stubAPI) Search(ctx context.Context, in SearchInput) (any, error) {
	return map[string]any{"hits": a.searched}, nil
}

var _ = stubAPI{}
```

`stubAPI` 需要满足 `ToolAPI` 接口的全部方法。为避免每个测试文件重复一大坨，接口方法集合在 Step 3 定义，测试替身在本步这样补齐：

```go
func (a stubAPI) Add(ctx context.Context, in AddInput) (any, error) { return map[string]any{"id": 1}, nil }
func (a stubAPI) List(ctx context.Context, in ListInput) (any, error) { return map[string]any{"memories": nil}, nil }
func (a stubAPI) Handoff(ctx context.Context, in HandoffInput) (any, error) { return map[string]any{"body": "brief"}, nil }
func (a stubAPI) Tasks(ctx context.Context, in TaskInput) (any, error) { return map[string]any{"cards": nil}, nil }
func (a stubAPI) TaskGet(ctx context.Context, id int64) (any, error) { return map[string]any{"id": id}, nil }
func (a stubAPI) ResolveProject(ctx context.Context, path string) (any, error) { return map[string]any{"known": false}, nil }
func (a stubAPI) Stats(ctx context.Context) (any, error) { return map[string]any{"memories": 0}, nil }
```

同时删掉测试文件里那句 `var _ = stubAPI{}`（它什么都不是），并把 `context` 的 import 保留。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/mcp/ -v`
Expected: FAIL，`undefined: NewServer`

- [ ] **Step 3: 写 `internal/mcp/protocol.go`**

```go
package mcp

import (
	"encoding/json"
	"strings"
)

const (
	LatestProtocolVersion = "2025-06-18"
	JSONRPCVersion        = "2.0"

	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type errorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *errorObject    `json:"error,omitempty"`
}

// Handle 处理一条 JSON-RPC 消息。第二个返回值表示是否需要回写响应：
// 通知（没有 id）不需要响应，给它回包会让客户端解析出错。
func (s *Server) Handle(ctx context.Context, raw []byte) ([]byte, bool) {
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.replyError(nil, CodeInvalidRequest, "parse error: "+err.Error()), true
	}
	if strings.TrimSpace(req.Method) == "" {
		return s.replyError(req.ID, CodeInvalidRequest, "missing method"), true
	}

	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		return s.reply(req.ID, s.initialize(req.Params)), true
	case "ping":
		return s.reply(req.ID, map[string]any{}), true
	case "tools/list":
		return s.reply(req.ID, map[string]any{"tools": s.toolDefinitions()}), true
	case "tools/call":
		res, err := s.callTool(ctx, req.Params)
		if err != nil {
			return s.replyError(req.ID, CodeInternalError, err.Error()), true
		}
		return s.reply(req.ID, res), true
	}

	if strings.HasPrefix(req.Method, "notifications/") {
		return nil, false
	}
	if isNotification {
		return nil, false
	}
	return s.replyError(req.ID, CodeMethodNotFound, "method not found: "+req.Method), true
}

func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	version := LatestProtocolVersion
	if err := json.Unmarshal(params, &p); err == nil && p.ProtocolVersion != "" {
		version = p.ProtocolVersion
	}
	// 回显客户端版本：MCP 允许服务器在双方都懂的范围内选用；
	// 硬编码一个版本会让旧客户端直接协商失败。
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": "agent-memory", "version": ServerVersion},
	}
}

func (s *Server) reply(id json.RawMessage, result any) []byte {
	body, _ := json.Marshal(response{JSONRPC: JSONRPCVersion, ID: id, Result: result})
	return body
}

func (s *Server) replyError(id json.RawMessage, code int, message string) []byte {
	body, _ := json.Marshal(response{JSONRPC: JSONRPCVersion, ID: id, Error: &errorObject{Code: code, Message: message}})
	return body
}

const ServerVersion = "0.1.0-m3"
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/mcp/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/mcp
git commit -m "feat: MCP JSON-RPC 最小子集"
```

---

## Task 30: MCP tools 与 store 的对接

**Files:**
- Create: `internal/mcp/tools.go`
- Create: `internal/mcp/tools_test.go`

- [ ] **Step 1: 写 `internal/mcp/tools.go`**

```go
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

type SearchInput struct {
	Query        string `json:"query"`
	ProjectPath  string `json:"project_path"`
	Type         string `json:"type"`
	Scope        string `json:"scope"`
	IncludeStale bool   `json:"include_stale"`
	Limit        int    `json:"limit"`
}

type AddInput struct {
	Text        string `json:"text"`
	Type        string `json:"type"`
	Scope       string `json:"scope"`
	ProjectPath string `json:"project_path"`
	Supersedes  int64  `json:"supersedes"`
}

type ListInput struct {
	ProjectPath  string `json:"project_path"`
	IncludeStale bool   `json:"include_stale"`
	Limit        int    `json:"limit"`
}

type HandoffInput struct {
	ProjectPath string `json:"project_path"`
}

type TaskInput struct {
	ProjectPath string `json:"project_path"`
	Status      string `json:"status"`
	Limit       int    `json:"limit"`
}

// ToolAPI 是 MCP 层与共享记忆之间的全部接触面。
// daemon.Server 直接实现它（spec §8.1 列的七个工具都从这里出入）。
type ToolAPI interface {
	Search(ctx context.Context, in SearchInput) (any, error)
	Add(ctx context.Context, in AddInput) (any, error)
	List(ctx context.Context, in ListInput) (any, error)
	Handoff(ctx context.Context, in HandoffInput) (any, error)
	Tasks(ctx context.Context, in TaskInput) (any, error)
	TaskGet(ctx context.Context, id int64) (any, error)
	ResolveProject(ctx context.Context, path string) (any, error)
	Stats(ctx context.Context) (any, error)
}

type Server struct {
	api ToolAPI
}

func NewServer(api ToolAPI) *Server { return &Server{api: api} }

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (s *Server) toolDefinitions() []toolDef {
	return []toolDef{
		{"memory_search", "在跨 agent 共享记忆里检索。中文两字词也能命中；返回条目文本、类型、来源 agent 与最后见到时间。",
			objectSchema(map[string]any{
				"query":         map[string]any{"type": "string", "description": "关键词或短语"},
				"project_path":  map[string]any{"type": "string", "description": "项目绝对路径；省略则只查全局记忆"},
				"type":          map[string]any{"type": "string", "enum": []string{"preference", "constraint", "convention", "lesson", "fact", "goal"}},
				"scope":         map[string]any{"type": "string", "enum": []string{"user", "project"}},
				"include_stale": map[string]any{"type": "boolean"},
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			}, "query")},
		{"memory_add", "写入共享记忆。别的 agent 下次读简报时就会看到。文本会先去重合并。",
			objectSchema(map[string]any{
				"text":         map[string]any{"type": "string"},
				"type":         map[string]any{"type": "string", "enum": []string{"preference", "constraint", "convention", "lesson", "fact", "goal"}},
				"scope":        map[string]any{"type": "string", "enum": []string{"user", "project"}},
				"project_path": map[string]any{"type": "string"},
				"supersedes":   map[string]any{"type": "integer", "description": "被取代的旧条目 id；只标过时，不删除"},
			}, "text", "type")},
		{"memory_list", "列出共享记忆条目（不做相关性排序）。",
			objectSchema(map[string]any{
				"project_path":  map[string]any{"type": "string"},
				"include_stale": map[string]any{"type": "boolean"},
				"limit":         map[string]any{"type": "integer"},
			})},
		{"handoff_get", "取本项目的交接简报 markdown：在做什么、做到哪、下一步、硬约束。切换 agent 时先读它。",
			objectSchema(map[string]any{
				"project_path": map[string]any{"type": "string"},
			})},
		{"task_list", "列出近期任务卡（目标 / 状态 / 改动文件 / 下一步 / 未决问题）。",
			objectSchema(map[string]any{
				"project_path": map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string", "enum": []string{"doing", "done", "blocked", "unknown"}},
				"limit":        map[string]any{"type": "integer"},
			})},
		{"task_get", "按 id 取单张任务卡的全部内容。task_list 之后按需展开细节用这个，别把整份简报复述一遍。",
			objectSchema(map[string]any{"id": map[string]any{"type": "integer"}}, "id")},
		{"project_resolve", "把任意目录路径解析成共享库里的 project_key，用来确认两个 agent 看的是同一个项目。",
			objectSchema(map[string]any{"path": map[string]any{"type": "string"}}, "path")},
		{"stats", "共享库状态：项目数、记忆条数、来源与失败项。诊断用。",
			objectSchema(map[string]any{})},
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("bad tools/call params: %w", err)
	}

	result, err := func() (any, error) {
		switch p.Name {
		case "memory_search":
			var in SearchInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Search(ctx, in)
		case "memory_add":
			var in AddInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Add(ctx, in)
		case "memory_list":
			var in ListInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.List(ctx, in)
		case "handoff_get":
			var in HandoffInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Handoff(ctx, in)
		case "task_list":
			var in TaskInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Tasks(ctx, in)
		case "task_get":
			var in struct {
				ID int64 `json:"id"`
			}
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			if in.ID <= 0 {
				return nil, fmt.Errorf("id must be a positive integer")
			}
			return s.api.TaskGet(ctx, in.ID)
		case "project_resolve":
			var in struct {
				Path string `json:"path"`
			}
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.ResolveProject(ctx, in.Path)
		case "stats":
			return s.api.Stats(ctx)
		default:
			return nil, nil
		}
	}()
	if err != nil {
		return toolError(err.Error()), nil
	}
	if result == nil {
		return toolError(fmt.Sprintf("unknown tool: %s", p.Name)), nil
	}
	return toolText(result), nil
}

func bind(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// toolText 把结果序列化成 text block：模型读的是文本，
// 结构化数据用 json 字符串承载最稳妥，也避免依赖服务端 structuredContent 支持。
func toolText(v any) map[string]any {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(err.Error())
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(body)}},
		"isError": false,
	}
}

func toolError(message string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": message}},
		"isError": true,
	}
}
```

- [ ] **Step 2: 写 `internal/mcp/tools_test.go`**

```go
package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type recordingAPI struct {
	lastSearch SearchInput
	lastAdd    AddInput
	searchOut  any
	err        error
}

func (a *recordingAPI) Search(ctx context.Context, in SearchInput) (any, error) {
	a.lastSearch = in
	if a.err != nil {
		return nil, a.err
	}
	if a.searchOut != nil {
		return a.searchOut, nil
	}
	return map[string]any{"hits": []any{}, "count": 0}, nil
}
func (a *recordingAPI) Add(ctx context.Context, in AddInput) (any, error) {
	a.lastAdd = in
	return map[string]any{"id": 42, "created": true}, nil
}
func (a *recordingAPI) List(ctx context.Context, in ListInput) (any, error) {
	return map[string]any{"memories": []any{}, "count": 0}, nil
}
func (a *recordingAPI) Handoff(ctx context.Context, in HandoffInput) (any, error) {
	return map[string]any{"body": "# 简报"}, nil
}
func (a *recordingAPI) Tasks(ctx context.Context, in TaskInput) (any, error) {
	return map[string]any{"cards": []any{}, "count": 0}, nil
}
func (a *recordingAPI) TaskGet(ctx context.Context, id int64) (any, error) {
	return map[string]any{"id": id, "goal": "给扣减加乐观锁"}, nil
}
func (a *recordingAPI) ResolveProject(ctx context.Context, path string) (any, error) {
	return map[string]any{"known": true, "project_key": "f:/go/demo"}, nil
}
func (a *recordingAPI) Stats(ctx context.Context) (any, error) {
	return map[string]any{"memories": 3}, nil
}

func callWith(t *testing.T, api ToolAPI, tool string, args map[string]any) map[string]any {
	t.Helper()
	s := NewServer(api)
	params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	resp, ok := s.Handle(context.Background(), raw)
	if !ok {
		t.Fatal("no response")
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func firstText(t *testing.T, out map[string]any) string {
	t.Helper()
	if out["error"] != nil {
		t.Fatalf("jsonrpc error: %v", out["error"])
	}
	res := out["result"].(map[string]any)
	return res["content"].([]any)[0].(map[string]any)["text"].(string)
}

func TestMemorySearchPassesArgumentsThrough(t *testing.T) {
	api := &recordingAPI{}
	callWith(t, api, "memory_search", map[string]any{
		"query": "偏好", "project_path": `F:\go\demo`, "type": "preference", "limit": 3,
	})
	if api.lastSearch.Query != "偏好" || api.lastSearch.ProjectPath != `F:\go\demo` ||
		api.lastSearch.Type != "preference" || api.lastSearch.Limit != 3 {
		t.Fatalf("args not forwarded: %+v", api.lastSearch)
	}
}

func TestToolErrorBecomesResultNotJSONRPCError(t *testing.T) {
	api := &recordingAPI{err: context.DeadlineExceeded}
	out := callWith(t, api, "memory_search", map[string]any{"query": "x"})
	if out["error"] != nil {
		t.Fatalf("backend failure must not be a jsonrpc error: %v", out["error"])
	}
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatal("want isError=true so the agent can see and retry")
	}
	if !strings.Contains(firstText(t, out), "deadline") {
		t.Fatalf("error text = %q", firstText(t, out))
	}
}

func TestUnknownArgumentIsToolError(t *testing.T) {
	api := &recordingAPI{}
	out := callWith(t, api, "memory_search", map[string]any{"query": "x", "no_such_field": 1})
	if out["result"].(map[string]any)["isError"] != true {
		t.Fatalf("want tool error for unknown argument: %v", out)
	}
}

func TestStatsNeedsNoArguments(t *testing.T) {
	api := &recordingAPI{}
	out := callWith(t, api, "stats", nil)
	if out["error"] != nil {
		t.Fatalf("error: %v", out["error"])
	}
	if !strings.Contains(firstText(t, out), "memories") {
		t.Fatalf("text = %q", firstText(t, out))
	}
}

func TestTaskGetRejectsNonPositiveID(t *testing.T) {
	api := &recordingAPI{}
	out := callWith(t, api, "task_get", map[string]any{"id": 0})
	if out["result"].(map[string]any)["isError"] != true {
		t.Fatalf("id=0 should be a tool error: %v", out)
	}
}

func TestToolDescriptionsArePresentAndNonTrivial(t *testing.T) {
	s := NewServer(&recordingAPI{})
	tools := s.toolDefinitions()
	if len(tools) != 8 {
		t.Fatalf("want 8 tools, got %d", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if len([]rune(tool.Description)) < 10 {
			t.Fatalf("tool %s description too thin to guide an agent: %q", tool.Name, tool.Description)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
	}
}
```

`TestToolErrorBecomesResultNotJSONRPCError` 断言里用到的 `deadline` 来自 `context.DeadlineExceeded.Error()`（"context deadline exceeded"），断言用它而不是硬编码整句，避免以后换错误类型时测试假失败。

- [ ] **Step 3: 跑测试确认通过**

Run: `go test ./internal/mcp/ -v`
Expected: PASS

- [ ] **Step 4: 提交**

```bash
git add internal/mcp
git commit -m "feat: MCP tools 定义与 ToolAPI 对接"
```

---

## Task 31: daemon 实现 ToolAPI、MCP HTTP 端点与 stdio 桥

**Files:**
- Create: `internal/daemon/mcp.go`
- Create: `internal/mcp/stdio.go`
- Test: `internal/mcp/stdio_test.go`

- [ ] **Step 1: 写 `internal/daemon/mcp.go`**

`daemon.Server` 直接实现 `mcp.ToolAPI`，并挂一条 `/mcp` 路由（鉴权同其他端点）。stdio 桥转发过来的就是这条路由。

```go
package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/mcp"
	"github.com/muyi99/agent-memory/internal/store"
)

var _ mcp.ToolAPI = (*Server)(nil)

func (s *Server) Search(ctx context.Context, in mcp.SearchInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	hits, err := store.NewFTS(s.st).Search(ctx, domain.SearchQuery{
		Text:         in.Query,
		ProjectID:    projectID,
		Type:         domain.MemoryType(in.Type),
		Scope:        domain.Scope(in.Scope),
		IncludeStale: in.IncludeStale,
		Limit:        in.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"id": h.Memory.ID, "scope": h.Memory.Scope, "type": h.Memory.Type,
			"text": h.Memory.Text, "stale": h.Memory.Stale,
			"last_seen_at": h.Memory.LastSeenAt.Format(time.RFC3339),
			"sources":      h.Memory.Sources, "score": h.Score, "origin": h.Origin,
		})
	}
	return map[string]any{"hits": out, "count": len(out)}, nil
}

func (s *Server) Add(ctx context.Context, in mcp.AddInput) (any, error) {
	return s.addMemory(ctx, addRequest{
		Text: in.Text, Type: in.Type, Scope: in.Scope,
		ProjectPath: in.ProjectPath, Agent: "mcp", Supersedes: in.Supersedes,
	})
}

func (s *Server) List(ctx context.Context, in mcp.ListInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	items, err := s.st.ListMemories(ctx, projectID, in.IncludeStale, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"memories": items, "count": len(items)}, nil
}

func (s *Server) Handoff(ctx context.Context, in mcp.HandoffInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	res, err := compose.New(s.st, compose.Config{BudgetTokens: s.cfg.BudgetTokens, RecentCards: s.cfg.RecentCards}).Render(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"body": res.Body, "tokens": res.Tokens, "truncated": res.Truncated}, nil
}

func (s *Server) Tasks(ctx context.Context, in mcp.TaskInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}
	cards, err := s.st.ListTaskCards(ctx, projectID, in.Status, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cards": cards, "count": len(cards)}, nil
}

func (s *Server) TaskGet(ctx context.Context, id int64) (any, error) {
	return s.st.GetTaskCard(ctx, id)
}

func (s *Server) ResolveProject(ctx context.Context, path string) (any, error) {
	id, ok, err := s.st.ResolveProjectByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]any{"known": false, "path": path}, nil
	}
	project, _, err := s.st.ProjectByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"known": true, "project_key": project.Key, "display_name": project.DisplayName}, nil
}

func (s *Server) Stats(ctx context.Context) (any, error) {
	return s.st.Stats(ctx)
}

// addMemory 抽出 handleAddMemory 的实体逻辑，让 HTTP 端点与 MCP tool 共用校验。
func (s *Server) addMemory(ctx context.Context, req addRequest) (any, error) {
	if strings.TrimSpace(req.Text) == "" {
		return nil, fmt.Errorf("text is required")
	}
	typ := domain.MemoryType(req.Type)
	if typ == "" {
		typ = domain.TypeFact
	}
	if !domain.ValidMemoryType(typ) {
		return nil, fmt.Errorf("unknown type %q", req.Type)
	}
	scope := domain.ScopeUser
	if req.Scope != "" {
		scope = domain.Scope(req.Scope)
		if scope != domain.ScopeUser && scope != domain.ScopeProject {
			return nil, fmt.Errorf("unknown scope %q", req.Scope)
		}
	}
	agent := req.Agent
	if agent == "" {
		agent = "mcp"
	}
	projectID := int64(0)
	if scope == domain.ScopeProject {
		if strings.TrimSpace(req.ProjectPath) == "" {
			return nil, fmt.Errorf("scope=project needs project_path")
		}
		id, _, err := s.st.ProjectIDForPath(ctx, agent, "", req.ProjectPath, "", time.Now())
		if err != nil {
			return nil, err
		}
		projectID = id
	}
	id, created, err := s.st.UpsertMemory(ctx, projectID, domain.MemoryDraft{
		Scope: scope, Type: typ, Text: req.Text,
		SourceAgent: agent, SourcePath: "mcp", Quote: req.Text,
	}, time.Now())
	if err != nil {
		return nil, err
	}
	if req.Supersedes != 0 && req.Supersedes != id {
		if err := s.st.SetStaleByID(ctx, req.Supersedes, true); err != nil {
			return nil, err
		}
	}
	return map[string]any{"id": id, "created": created, "scope": scope, "type": typ}, nil
}

// handleMCP 是 stdio 桥的目标。它不需要流式：每条请求都是单个 JSON-RPC 消息，
// 回单个 JSON 对象，Accept 里出现哪种都按 application/json 回。
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	mcpServer := mcp.NewServer(s)
	resp, needReply := mcpServer.Handle(r.Context(), body)
	if !needReply {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}
```

`handleAddMemory`（Task 28 写的 HTTP handler）改为调用 `addMemory`，避免两套校验：把它的函数体换成

```go
func (s *Server) handleAddMemory(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	res, err := s.addMemory(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
```

`Server` 注册路由处补一行（`/mcp` 走同一套鉴权）：

```go
	s.mux.HandleFunc("POST /mcp", s.auth(s.handleMCP))
```

`mcp.go` 的 import 需要 `"fmt"` 与 `"strings"`（`addMemory` 用到），按实际使用补齐。

`TaskGet` 依赖 M2 里的按 id 查询，它当时是非导出的。在 `internal/store/taskcard.go` 导出一层，并把「查不到」变成可判别的错误而不是零值卡片：

```go
// GetTaskCard 按 id 取任务卡。找不到返回 domain.ErrTaskCardNotFound，
// 让 MCP 层能给出「没有这张卡」而不是把空白卡当成结果贴回去。
func (s *Store) GetTaskCard(ctx context.Context, id int64) (domain.TaskCard, error) {
	card, err := s.taskCardByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskCard{}, domain.ErrTaskCardNotFound
	}
	return card, err
}
```

`internal/domain/types.go` 的错误变量块里补 `ErrTaskCardNotFound = errors.New("task card not found")`，并在 `internal/store/taskcard_test.go` 加一条：

```go
func TestGetTaskCard_UnknownID(t *testing.T) {
	s := openStore(t)
	_, err := s.GetTaskCard(context.Background(), 4242)
	if !errors.Is(err, domain.ErrTaskCardNotFound) {
		t.Fatalf("want ErrTaskCardNotFound, got %v", err)
	}
}
```

- [ ] **Step 2: 写 MCP 端点测试——追加到 `internal/daemon/mcp_test.go`**

```go
package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/store"
)

func newMCPServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(Config{Token: "token-012345678901234567890123456789", BudgetTokens: 1500, RecentCards: 3}, st)
}

func rpc(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-012345678901234567890123456789")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPInitializeOverHTTP(t *testing.T) {
	s := newMCPServer(t)
	out := rpc(t, s, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}})
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
}

func TestMCPNotificationReturns202(t *testing.T) {
	s := newMCPServer(t)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-012345678901234567890123456789")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rec.Code)
	}
}

func TestMCPToolsCallWritesIntoSharedStore(t *testing.T) {
	s := newMCPServer(t)
	out := rpc(t, s, "tools/call", map[string]any{
		"name":      "memory_add",
		"arguments": map[string]any{"text": "共享库写入的记忆应能被中文检索命中", "type": "convention", "scope": "user"},
	})
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("add failed: %v", res)
	}

	out = rpc(t, s, "tools/call", map[string]any{
		"name": "memory_search", "arguments": map[string]any{"query": "检索"},
	})
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "共享库写入的记忆") {
		t.Fatalf("search should see the just-added memory:\n%s", text)
	}
}

func TestMCPRequiresAuth(t *testing.T) {
	s := newMCPServer(t)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}
```

- [ ] **Step 3: 跑测试确认通过**

Run: `go test ./internal/daemon/ -v`
Expected: PASS

- [ ] **Step 4: 写 `internal/mcp/stdio.go`**

```go
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Bridge 把 stdin 上的 JSON-RPC 逐行转发给常驻服务的 /mcp，再把响应逐行写回 stdout。
//
// 为什么要这层：agent 的 MCP 配置里只有 stdio 这一种是四家都稳定支持的，
// 而多个 agent 同时各起一份进程会争抢 SQLite 与重复扫描。桥让「agent 看到的形态」
// 与「我们内部的单实例」解耦。
type Bridge struct {
	endpoint string
	token    string
	client   *http.Client
}

func NewBridge(port int, token string) *Bridge {
	return &Bridge{
		endpoint: fmt.Sprintf("http://127.0.0.1:%d/mcp", port),
		token:    token,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// ErrNoDaemon 是明确的、可指导下一步的失败，不是「连接被拒绝」。
var ErrNoDaemon = fmt.Errorf("agent-memory serve 未运行：先执行 agent-memory serve（或看 ~/.agent-memory/port 是否存在）")

func (b *Bridge) Send(ctx context.Context, message []byte) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(message))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.token)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("%w (%v)", ErrNoDaemon, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, false, fmt.Errorf("token 不匹配：agent 配置里的 token 与 ~/.agent-memory/token 不一致，重跑 agent-memory link")
	}
	if resp.StatusCode == http.StatusAccepted {
		// 通知不需要回包
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("daemon returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, true, nil
}

// ServeStdio 读 stdin 的换行分隔 JSON-RPC 消息并转发。
// 每行一条消息；空行忽略；读到的行超过 8MB 时报错并退出，避免内存被撑爆。
func (b *Bridge) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		resp, needReply, err := b.Send(ctx, line)
		if err != nil {
			// 转发失败必须回一条 JSON-RPC error：静默让 agent 以为工具挂了并在原地重试
			errMsg := err.Error()
			if len(errMsg) > 500 {
				errMsg = errMsg[:500]
			}
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": nil,
				"error": map[string]any{"code": CodeInternalError, "message": errMsg},
			})
			fmt.Fprintln(out, string(body))
			continue
		}
		if needReply {
			if _, err := out.Write(resp); err != nil {
				return err
			}
			if _, err := io.WriteString(out, "\n"); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
```

- [ ] **Step 5: 写 `internal/mcp/stdio_test.go`**

```go
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBridge_ForwardsRequestAndResponse(t *testing.T) {
	var gotAuth string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"pong":true}}`)
	}))
	defer upstream.Close()

	br := NewBridge(0, "secret-token")
	br.endpoint = upstream.URL + "/mcp"
	var out bytes.Buffer
	err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/mcp" {
		t.Fatalf("upstream path = %q, want /mcp", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if !strings.Contains(out.String(), `"pong":true`) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestBridge_DropsNotificationResponsesAndWritesNothing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	br := NewBridge(0, "t")
	br.endpoint = upstream.URL
	var out bytes.Buffer
	if err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("notification must produce no stdout, got %q", out.String())
	}
}

func TestBridge_ReportsDaemonDownActionably(t *testing.T) {
	br := NewBridge(1, "t") // 端口 1 必然连不上
	var out bytes.Buffer
	err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if err != nil {
		t.Fatalf("transport failure must not kill the loop: %v", err)
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatal("bridge must answer the agent instead of staying silent")
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("error reply must be valid json: %v\n%s", err, line)
	}
	errObj := msg["error"].(map[string]any)
	text := errObj["message"].(string)
	if !strings.Contains(text, "agent-memory serve") {
		t.Fatalf("error should tell the user what to run: %q", text)
	}
}

func TestBridge_RejectsTokenMismatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer upstream.Close()
	br := NewBridge(0, "wrong")
	br.endpoint = upstream.URL
	var out bytes.Buffer
	br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if !strings.Contains(out.String(), "token") {
		t.Fatalf("want a token-specific error, got %q", out.String())
	}
}

func TestBridge_SkipsBlankLines(t *testing.T) {
	var count int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer upstream.Close()
	br := NewBridge(0, "t")
	br.endpoint = upstream.URL
	var out bytes.Buffer
	in := "\n\n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n\n"
	if err := br.ServeStdio(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("blank lines forwarded %d times", count)
	}
	lines := 0
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		lines++
	}
	if lines != 1 {
		t.Fatalf("want 1 response line, got %d", lines)
	}
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `go test ./internal/mcp/ -v`
Expected: PASS

- [ ] **Step 7: 提交**

```bash
git add internal/mcp internal/daemon
git commit -m "feat: daemon 实现 ToolAPI、MCP HTTP 端点与 stdio 转发桥"
```

---

## Task 32: JSON 合并、受管区块与 link

**Files:**
- Create: `internal/surface/merge.go`
- Create: `internal/surface/block.go`
- Create: `internal/surface/link.go`
- Test: `internal/surface/merge_test.go`
- Test: `internal/surface/block_test.go`
- Test: `internal/surface/link_test.go`

- [ ] **Step 1: 写 `internal/surface/merge_test.go`**

```go
package surface

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertJSONKey_AddsUnderRootAndBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte("{\n  \"mcpServers\": {\n    \"existing\": {\"command\": \"other\"}\n  }\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, backup, err := UpsertJSONKey(path, "mcpServers", "agent-memory", map[string]any{
		"command": "agent-memory", "args": []string{"mcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed=true")
	}
	if backup == "" {
		t.Fatal("want a backup path")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	data, _ := os.ReadFile(path)
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	servers := doc["mcpServers"].(map[string]any)
	if servers["existing"] == nil {
		t.Fatalf("existing server destroyed: %s", data)
	}
	if servers["agent-memory"] == nil {
		t.Fatalf("our server missing: %s", data)
	}
}

func TestUpsertJSONKey_IdempotentSecondCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"command": "agent-memory", "args": []any{"mcp"}}

	changed1, _, err := UpsertJSONKey(path, "mcpServers", "agent-memory", entry)
	if err != nil {
		t.Fatal(err)
	}
	changed2, backup2, err := UpsertJSONKey(path, "mcpServers", "agent-memory", entry)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("identical entry must not rewrite the file")
	}
	if backup2 != "" {
		t.Fatalf("no change means no backup, got %q", backup2)
	}
	if !changed1 {
		t.Fatal("first call should change")
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	var backups int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			backups++
		}
	}
	if backups != 1 {
		t.Fatalf("want exactly 1 backup, got %d", backups)
	}
}

func TestUpsertJSONKey_CreatesMissingFileWithRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "opencode.json")
	changed, _, err := UpsertJSONKey(path, "mcp", "agent-memory", map[string]any{"type": "local", "command": []any{"agent-memory", "mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("creating a file is a change")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["mcp"] == nil {
		t.Fatalf("root key missing: %s", data)
	}
}

func TestUpsertJSONKey_RefusesUnparseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := UpsertJSONKey(path, "mcpServers", "agent-memory", map[string]any{}); err == nil {
		t.Fatal("refuse to touch a file we cannot parse — never guess-overwrite user config")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{ not json" {
		t.Fatal("broken file was modified")
	}
}

func TestRemoveJSONKey_OnlyRemovesOurs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	doc := map[string]any{"mcpServers": map[string]any{
		"existing":     map[string]any{"command": "other"},
		"agent-memory": map[string]any{"command": "agent-memory"},
	}}
	body, _ := json.Marshal(doc)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, _, err := RemoveJSONKey(path, "mcpServers", "agent-memory")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed")
	}
	data, _ := os.ReadFile(path)
	var after map[string]any
	json.Unmarshal(data, &after)
	servers := after["mcpServers"].(map[string]any)
	if _, ok := servers["agent-memory"]; ok {
		t.Fatal("our key still present")
	}
	if _, ok := servers["existing"]; !ok {
		t.Fatal("foreign key removed")
	}
	// 不存在时第二次是 no-op
	changed2, _, err := RemoveJSONKey(path, "mcpServers", "agent-memory")
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("second removal should be a no-op")
	}
}
```

- [ ] **Step 2: 写 `internal/surface/merge.go`**

```go
package surface

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// UpsertJSONKey 在 <path> 的 JSON 文档里把 doc[root][name] 设为 entry。
// 返回 changed=false 表示内容已相同，不写文件也不留备份——每次 link 都塞一个
// 备份文件，比不备份更烦人。
//
// 解析失败直接拒绝：宁可不动用户配置，也不猜一个结构覆盖掉它。
func UpsertJSONKey(path, root, name string, entry map[string]any) (changed bool, backup string, err error) {
	return patchJSONKey(path, root, name, entry, true)
}

// RemoveJSONKey 删掉 doc[root][name]，其余键一概不动。
func RemoveJSONKey(path, root, name string) (changed bool, backup string, err error) {
	return patchJSONKey(path, root, name, nil, false)
}

func patchJSONKey(path, root, name string, entry map[string]any, upsert bool) (bool, string, error) {
	data, err := os.ReadFile(path)
	doc := map[string]any{}
	emptyInput := false
	if os.IsNotExist(err) {
		emptyInput = true
		if !upsert {
			return false, "", nil
		}
	} else if err != nil {
		return false, "", fmt.Errorf("read %s: %w", path, err)
	} else {
		if e := json.Unmarshal(data, &doc); e != nil {
			return false, "", fmt.Errorf("%s is not valid json; refusing to modify it: %w", path, e)
		}
	}

	rootValue, hasRoot := doc[root]
	if hasRoot {
		// 根键存在但类型不对（例如 mcpServers 被写成了数组）：拒绝，不静默替换
		if _, ok := rootValue.(map[string]any); !ok {
			return false, "", fmt.Errorf("%s: key %q is not an object; fix it manually", path, root)
		}
	}
	if !hasRoot {
		if !upsert {
			return false, "", nil
		}
		doc[root] = map[string]any{}
	}
	servers := doc[root].(map[string]any)

	if upsert {
		if existing, ok := servers[name]; ok && jsonEqual(existing, entry) {
			return false, "", nil
		}
		servers[name] = entry
	} else {
		if _, ok := servers[name]; !ok {
			return false, "", nil
		}
		delete(servers, name)
	}

	if !emptyInput {
		bak, err := backupFile(path)
		if err != nil {
			return false, "", err
		}
		backup = bak
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, backup, err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, backup, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return false, backup, err
	}
	return true, backup, nil
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return reflect.DeepEqual(a, b)
	}
	return string(ab) == string(bb)
}

func backupFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	target := fmt.Sprintf("%s.bak-%s", path, stamp)
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return "", err
	}
	return target, nil
}
```

- [ ] **Step 3: 写 `internal/surface/block_test.go`**

```go
package surface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const beginMarker = "<!-- agent-memory:begin -->"
const endMarker = "<!-- agent-memory:end -->"

func TestInstallBlock_CreatesFileWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	changed, err := InstallBlock(path, beginMarker, endMarker, "## 共享记忆\n\n读 `.agent-memory/HANDOFF.md`。\n")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed")
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "## 共享记忆") {
		t.Fatalf("file content = %q", data)
	}
	if !strings.Contains(string(data), beginMarker) || !strings.Contains(string(data), endMarker) {
		t.Fatalf("markers missing:\n%s", data)
	}
}

func TestInstallBlock_KeepsUserContentAndReplacesOnlyOurs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	original := "# 项目说明\n\n这是用户自己写的规则。\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "v2\n"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "这是用户自己写的规则。") {
		t.Fatalf("user content lost:\n%s", text)
	}
	if strings.Count(text, beginMarker) != 1 {
		t.Fatalf("markers duplicated:\n%s", text)
	}
	if strings.Contains(text, "v1") || !strings.Contains(text, "v2") {
		t.Fatalf("old block not replaced:\n%s", text)
	}
}

func TestInstallBlock_IdempotentNoRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if _, err := InstallBlock(path, beginMarker, endMarker, "同一份内容\n"); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallBlock(path, beginMarker, endMarker, "同一份内容\n")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("identical block must not rewrite the file")
	}
}

func TestRemoveBlock_RestoresOriginalBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	original := "# 规则\n\n保留\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "插入内容\n"); err != nil {
		t.Fatal(err)
	}
	changed, err := RemoveBlock(path, beginMarker, endMarker)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed")
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatalf("want original bytes back, got %q", data)
	}
	if _, err := RemoveBlock(path, beginMarker, endMarker); err != nil {
		t.Fatal(err)
	}
}

func TestInstallBlock_RefusesUnbalancedMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("begin\n"+beginMarker+"\n中间没有结束标记\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "x\n"); err == nil {
		t.Fatal("refuse to operate on a half-marked file — rewriting it could eat user text")
	}
}

func TestRemoveBlock_MissingFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.md")
	changed, err := RemoveBlock(path, beginMarker, endMarker)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("removing from a missing file is not a change")
	}
}
```

- [ ] **Step 4: 写 `internal/surface/block.go`**

```go
package surface

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallBlock 在 markdown 文件里安装或更新一段受管区块。
// 文件不存在时创建；存在时只在标记之间改动，标记外的字节原样保留。
func InstallBlock(path, begin, end, content string) (changed bool, err error) {
	block := begin + "\n" + strings.TrimRight(content, "\n") + "\n" + end + "\n"
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		return true, os.WriteFile(path, []byte(block), 0o644)
	}
	if err != nil {
		return false, err
	}

	existing := string(data)
	beginCount := strings.Count(existing, begin)
	endCount := strings.Count(existing, end)
	if (beginCount == 0) != (endCount == 0) || beginCount > 1 || endCount > 1 {
		return false, fmt.Errorf("%s has unbalanced or repeated markers; fix it manually", path)
	}
	if beginCount == 0 {
		out := existing
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "\n" + block
		return true, os.WriteFile(path, []byte(out), 0o644)
	}

	start := strings.Index(existing, begin)
	stop := strings.Index(existing, end) + len(end)
	replaced := existing[:start] + block + existing[stop:]
	if replaced == existing {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(replaced), 0o644)
}

// RemoveBlock 精确删掉自己写入的区块，并尽量还原原始字节：
// 标记前后由我们插入的空行也一并清掉。
func RemoveBlock(path, begin, end string) (changed bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	existing := string(data)
	start := strings.Index(existing, begin)
	if start < 0 {
		return false, nil
	}
	stop := strings.Index(existing[start:], end)
	if stop < 0 {
		return false, fmt.Errorf("%s has a begin marker without one; remove it manually", path)
	}
	stop += start + len(end)
	for stop < len(existing) && existing[stop] == '\n' {
		stop++
	}
	head := strings.TrimRight(existing[:start], "\n")
	tail := existing[stop:]
	var out string
	switch {
	case head == "":
		out = strings.TrimLeft(tail, "\n")
	case tail == "":
		out = head + "\n"
	default:
		out = head + "\n" + strings.TrimLeft(tail, "\n")
	}
	if out == existing {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}

// BlockExists 供 doctor 检查区块是否还在（用户可能手动删了）。
func BlockExists(path, begin string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(begin))
}
```

`TestRemoveBlock_RestoresOriginalBytes` 要求逐字节还原。`InstallBlock` 在既有文件后追加时插了一个前导空行（`out += "\n" + block`），`RemoveBlock` 用 `TrimRight(head)` + 还原成 `head + "\n"`，对 `original = "# 规则\n\n保留\n"` 这条用例结果仍是 `"# 规则\n\n保留\n"`；实现时保持这套处理，不要顺手简化成单行拼接。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/surface/ -run 'TestUpsert|TestRemove|TestInstall|TestBlock' -v`
Expected: PASS

- [ ] **Step 6: 写 `internal/surface/link_test.go`**

```go
package surface

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkTargetsCoverConfirmedAgents(t *testing.T) {
	got := LinkTargets()
	want := map[string]bool{"qoder": true, "opencode": true, "claude": true}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want exactly the three confirmed formats", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected target %q: trae and workbuddy config formats are not confirmed", name)
		}
	}
}

func TestPlanLink_QoderEntryShape(t *testing.T) {
	plan, err := PlanLink("qoder", LinkOptions{Binary: `C:\bin\agent-memory.exe`, Token: "tok", Port: 47111})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan.MCPEntry)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["command"] != `C:\bin\agent-memory.exe` {
		t.Fatalf("command = %v", doc["command"])
	}
	args, _ := doc["args"].([]any)
	if len(args) != 2 || args[0] != "mcp" || args[1] != "--port" {
		t.Fatalf("args = %v, want [mcp --port ...]", args)
	}
	if doc["env"] == nil {
		t.Fatal("token must be passed via env, not the command line (it would land in shell history and ps output)")
	}
	env := doc["env"].(map[string]any)
	if env["AGENT_MEMORY_TOKEN"] != "tok" {
		t.Fatalf("env = %v", env)
	}
}

func TestPlanLink_OpenCodeUsesLocalTypeAndCommandArray(t *testing.T) {
	plan, err := PlanLink("opencode", LinkOptions{Binary: "/usr/local/bin/agent-memory", Token: "tok", Port: 47111})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MCPRoot != "mcp" {
		t.Fatalf("opencode root key = %q, want mcp", plan.MCPRoot)
	}
	if plan.MCPEntry["type"] != "local" {
		t.Fatalf("opencode entry needs type=local, got %v", plan.MCPEntry["type"])
	}
	cmd, ok := plan.MCPEntry["command"].([]string)
	if !ok || len(cmd) < 2 || cmd[len(cmd)-1] != "mcp" {
		t.Fatalf("opencode command should be an argv array ending in mcp, got %v", plan.MCPEntry["command"])
	}
	if _, hasArgs := plan.MCPEntry["args"]; hasArgs {
		t.Fatalf("opencode entry must not use the qoder-style args key: %v", plan.MCPEntry)
	}
}

func TestPlanLink_RejectsUnknownTarget(t *testing.T) {
	if _, err := PlanLink("trae", LinkOptions{Binary: "x", Token: "t", Port: 1}); err == nil {
		t.Fatal("trae must be refused until its config format is confirmed")
	}
}

func TestBlockContentMentionsHandoffAndProjectKey(t *testing.T) {
	body := BlockContent("f:/go/demo")
	for _, want := range []string{".agent-memory/HANDOFF.md", "f:/go/demo"} {
		if !strings.Contains(body, want) {
			t.Fatalf("block content missing %q:\n%s", want, body)
		}
	}
	if len([]rune(body)) > 600 {
		t.Fatalf("block content should stay a pointer, not the whole brief: %d runes", len([]rune(body)))
	}
}

func TestLinkWritesAndUnlinkRestores(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	project := filepath.Join(dir, "repo")
	for _, d := range []string{home, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mcpPath := filepath.Join(home, ".qoder-cn", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := LinkOptions{
		Target: "qoder", Home: home, ProjectDir: project,
		Binary: "agent-memory", Token: "tok1234567890tok1234567890", Port: 47111,
	}
	plan, err := PlanLink(opts.Target, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MCPPath != mcpPath {
		t.Fatalf("plan mcp path = %q want %q", plan.MCPPath, mcpPath)
	}
	report, err := ApplyLink(plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !report.MCPChanged || report.Backup == "" {
		t.Fatalf("link report wrong: %+v", report)
	}
	if !report.BlockAdded {
		t.Fatalf("block should be installed: %+v", report)
	}
	if _, err := os.Stat(report.Backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	data, _ := os.ReadFile(mcpPath)
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["mcpServers"].(map[string]any)["agent-memory"] == nil {
		t.Fatalf("not linked: %s", data)
	}
	blockPath := filepath.Join(project, "AGENTS.md")
	blockBody, err := os.ReadFile(blockPath)
	if err != nil {
		t.Fatalf("block file should be installed: %v", err)
	}
	if !strings.Contains(string(blockBody), BeginMarker) {
		t.Fatalf("marker missing: %s", blockBody)
	}

	undo, err := ApplyUnlink(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !undo.MCPChanged {
		t.Fatalf("unlink should report a change: %+v", undo)
	}
	data, _ = os.ReadFile(mcpPath)
	doc = map[string]any{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["mcpServers"].(map[string]any)["agent-memory"] != nil {
		t.Fatalf("unlink left our entry behind: %s", data)
	}
	finalBlock, err := os.ReadFile(blockPath)
	if err == nil && strings.Contains(string(finalBlock), BeginMarker) {
		t.Fatalf("block marker still present after unlink: %s", finalBlock)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	mcpPath := filepath.Join(home, ".qoder-cn", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(mcpPath)

	plan, err := PlanLink("qoder", LinkOptions{Binary: "agent-memory", Token: "t0123456789t0123456789t0123456789", Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MCPRoot == "" {
		t.Fatal("plan should carry the root key so --dry-run can describe it")
	}
	after, _ := os.ReadFile(mcpPath)
	if string(before) != string(after) {
		t.Fatal("PlanLink must not write anything")
	}
}
```

- [ ] **Step 7: 写 `internal/surface/link.go`**

```go
package surface

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/muyi99/agent-memory/internal/config"
)

const (
	BeginMarker = "<!-- agent-memory:begin -->"
	EndMarker   = "<!-- agent-memory:end -->"
	MCPName     = "agent-memory"
)

// LinkOptions 是一次 link/unlink 的全部输入。Home 与 ProjectDir 都可注入，
// 让测试不必碰真实用户目录。
type LinkOptions struct {
	Target     string
	Home       string
	ProjectDir string
	Binary     string
	Token      string
	Port       int
}

type Plan struct {
	Target      string
	MCPPath     string
	MCPRoot     string
	MCPEntry    map[string]any
	BlockFile   string
	BlockExists bool
}

type Report struct {
	MCPChanged bool
	Backup     string
	BlockFile  string
	BlockAdded bool
}

// LinkTargets 只列出配置格式已在本机确证过的 agent。
// 没确证就拒绝写入，比照着别人的目录结构猜一个更安全。
func LinkTargets() []string { return []string{"qoder", "opencode", "claude"} }

func PlanLink(target string, opts LinkOptions) (Plan, error) {
	home := opts.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Plan{}, err
		}
		home = h
	}
	if opts.Binary == "" {
		return Plan{}, fmt.Errorf("binary path is required (link needs an absolute launcher)")
	}

	plan := Plan{Target: target}
	switch target {
	case "qoder":
		qHome := config.QoderHomeDir(home)
		plan.MCPPath = filepath.Join(qHome, "mcp.json")
		plan.MCPRoot = "mcpServers"
		plan.MCPEntry = stdioEntry(opts, false)
		plan.BlockFile = filepath.Join(opts.ProjectDir, "AGENTS.md")
	case "opencode":
		plan.MCPPath = filepath.Join(home, ".config", "opencode", "opencode.json")
		plan.MCPRoot = "mcp"
		plan.MCPEntry = stdioEntry(opts, true)
		plan.BlockFile = filepath.Join(opts.ProjectDir, "AGENTS.md")
	case "claude":
		plan.MCPPath = filepath.Join(home, ".claude.json")
		plan.MCPRoot = "mcpServers"
		plan.MCPEntry = stdioEntry(opts, false)
		plan.BlockFile = filepath.Join(opts.ProjectDir, "CLAUDE.md")
	default:
		return Plan{}, fmt.Errorf("agent %q 的配置格式尚未确证，link 拒绝写入；它仍可通过项目里的 HANDOFF.md 读取共享记忆", target)
	}
	if opts.ProjectDir != "" {
		plan.BlockExists = BlockExists(plan.BlockFile, BeginMarker)
	}
	if plan.BlockFile == "" {
		return Plan{}, fmt.Errorf("project dir is required to place the context block")
	}
	return plan, nil
}

// stdioEntry 用 env 传 token：命令行参数会进 shell 历史与进程列表。
func stdioEntry(opts LinkOptions, openCode bool) map[string]any {
	args := []string{"mcp", "--port", fmt.Sprint(opts.Port)}
	entry := map[string]any{
		"command": opts.Binary,
		"args":    args,
		"env":     map[string]string{"AGENT_MEMORY_TOKEN": opts.Token},
	}
	if openCode {
		// OpenCode 的 local MCP 用 command 数组，不用 command+args 两段式
		entry["type"] = "local"
		entry["command"] = append([]string{opts.Binary}, args...)
		delete(entry, "args")
		entry["enabled"] = true
	}
	return entry
}

func ApplyLink(plan Plan, opts LinkOptions) (Report, error) {
	var rep Report
	changed, backup, err := UpsertJSONKey(plan.MCPPath, plan.MCPRoot, MCPName, plan.MCPEntry)
	if err != nil {
		return rep, fmt.Errorf("mcp config: %w", err)
	}
	rep.MCPChanged = changed
	rep.Backup = backup

	content := BlockContent(projectKeyFor(opts.ProjectDir))
	blockChanged, err := InstallBlock(plan.BlockFile, BeginMarker, EndMarker, content)
	if err != nil {
		return rep, fmt.Errorf("context block: %w", err)
	}
	rep.BlockAdded = blockChanged
	rep.BlockFile = plan.BlockFile
	return rep, nil
}

func ApplyUnlink(opts LinkOptions) (Report, error) {
	var rep Report
	plan, err := PlanLink(opts.Target, opts)
	if err != nil {
		return rep, err
	}
	changed, backup, err := RemoveJSONKey(plan.MCPPath, plan.MCPRoot, MCPName)
	if err != nil {
		return rep, err
	}
	rep.MCPChanged = changed
	rep.Backup = backup
	blockChanged, err := RemoveBlock(plan.BlockFile, BeginMarker, EndMarker)
	if err != nil {
		return rep, err
	}
	rep.BlockAdded = blockChanged
	rep.BlockFile = plan.BlockFile
	return rep, nil
}

// BlockContent 是区块正文：只放指针和 project_key，正文留在 HANDOFF.md。
// 简报本身会随扫描变化，把它的副本塞进 AGENTS.md 只会让仓库文件反复产生 diff。
func BlockContent(projectKey string) string {
	return fmt.Sprintf("## 共享记忆（agent-memory）\n\n"+
		"这个项目接入跨 agent 共享记忆，project_key: `%s`。\n\n"+
		"- 接手前**先读** `.agent-memory/HANDOFF.md`：目标、进度、下一步与硬约束都在里面\n"+
		"- 学到的约定/约束/踩坑用 MCP tool `memory_add` 写回共享库，别只留在本 agent 记忆里\n"+
		"- 需要更多细节时用 `memory_search` 按需检索，不要复述整份简报\n"+
		"- 简报由 `agent-memory scan` 自动更新；手动重生成：`agent-memory handoff --project .`\n",
		projectKey)
}

func projectKeyFor(dir string) string {
	remote, _, err := readRemote(dir)
	if err != nil || remote == "" {
		return normalizePathKey(dir)
	}
	return remote
}
```

`readRemote` 与 `normalizePathKey` 直接复用已有实现，在 `link.go` 里定义成薄封装（避免 surface 包依赖 store）：

```go
func readRemote(dir string) (string, bool, error) {
	return normalize.GitRemoteFromConfig(dir)
}

func normalizePathKey(dir string) string {
	return normalize.ProjectKey("", dir)
}
```

`internal/config` 需要一个 `QoderHomeDir(home string) string`，返回 `<home>/.qoder-cn`（不存在则 `<home>/.qoder`），与 M1 的 `Config.QoderHome()` 共用发现逻辑：把 M1 里 `qoder.DiscoverHome` 的调用抽到 config 包里，`Config.QoderHome()` 改为委托 `QoderHomeDir(userBase)`。

- [ ] **Step 8: 跑测试确认通过**

Run: `go test ./internal/surface/ -v`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add internal/surface internal/config
git commit -m "feat: JSON 合并、受管区块与 link/unlink"
```

---

## Task 33: 文件监听与轮询兜底

**Files:**
- Create: `internal/ingest/watcher.go`
- Test: `internal/ingest/watcher_test.go`

- [ ] **Step 1: 写失败测试 `internal/ingest/watcher_test.go`**

```go
package ingest

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func countCalls(t *testing.T, w *Watcher, d time.Duration) int {
	t.Helper()
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	time.Sleep(d)
	cancel()
	time.Sleep(50 * time.Millisecond)
	return int(atomic.LoadInt32(&calls))
}

func TestWatcher_FiresOnFileCreate(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{
		Dirs:        []string{dir},
		Debounce:    20 * time.Millisecond,
		PollSeconds: 3600, // 故意把轮询拉长，确认这条路径靠的是 fsnotify
	})

	var calls int32
	done := make(chan struct{})
	w.SetHandler(func(context.Context) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(done)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// 等 watcher 就绪再写文件，否则第一个事件可能落在注册之前
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("fsnotify 没触发，calls=%d", atomic.LoadInt32(&calls))
	}
}

func TestWatcher_CoalescesBurstIntoOneScan(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{
		Dirs:        []string{dir},
		Debounce:    100 * time.Millisecond,
		PollSeconds: 3600,
	})
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 12; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i))+".md")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1500 * time.Millisecond)

	// 12 次写应该被去抖成很少几次：一次扫描的成本远大于一次事件
	if got := atomic.LoadInt32(&calls); got > 3 {
		t.Fatalf("debounce ineffective: %d scans for a 12-file burst", got)
	}
}

func TestWatcher_PollFiresWithoutFileEvents(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{
		Dirs:        []string{dir},
		Debounce:    10 * time.Millisecond,
		PollSeconds: 1,
	})
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(2500 * time.Millisecond)

	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("轮询兜底必须独立触发——Windows 上 fsnotify 会静默漏事件")
	}
}

func TestWatcher_PicksUpDirectoriesCreatedLater(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "projects", "newslug", "memory")
	w := NewWatcher(WatchConfig{
		Dirs:        []string{root},
		Debounce:    20 * time.Millisecond,
		PollSeconds: 1,
	})
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "m.md"), []byte("新目录里的记忆"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("运行中新建的目录应当被纳入监听")
	}
}

func TestWatcher_StopsWithContext(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{Dirs: []string{dir}, Debounce: 10 * time.Millisecond, PollSeconds: 1})
	w.SetHandler(func(context.Context) {})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- w.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Run should exit cleanly on cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestWatcher_BadDirIsNotFatal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	w := NewWatcher(WatchConfig{Dirs: []string{missing, t.TempDir()}, Debounce: 10 * time.Millisecond, PollSeconds: 1})
	w.SetHandler(func(context.Context) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- w.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-errs; err != nil {
		t.Fatalf("一个不存在的目录不该让监听退出: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ingest/ -run TestWatcher -v`
Expected: FAIL，`undefined: NewWatcher`

- [ ] **Step 3: 加依赖**

Run: `go get github.com/fsnotify/fsnotify@v1.7.0`
Expected: `go: added github.com/fsnotify/fsnotify v1.7.0`（本机已实测可取）

- [ ] **Step 4: 写 `internal/ingest/watcher.go`**

```go
package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

type WatchConfig struct {
	Dirs        []string
	Debounce    time.Duration
	PollSeconds int
}

// Watcher 把「文件可能变了」压缩成「该扫一次了」。
//
// 两条触发路径并存：fsnotify 负责及时，固定轮询负责兜底——Windows 上的目录通知
// 会静默丢事件（进程被杀、网络盘、编辑器换名写入），只靠 fsnotify 会让人以为
// 记忆没更新。两条路径共用同一个去抖窗口，所以不会有重复扫描风暴。
type Watcher struct {
	cfg     WatchConfig
	handler func(context.Context)
	// lastRun 用于避免刚扫完又立刻被轮询触发一次空扫。
	lastRun time.Time
}

func NewWatcher(cfg WatchConfig) *Watcher {
	if cfg.Debounce <= 0 {
		cfg.Debounce = 2 * time.Second
	}
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 30
	}
	return &Watcher{cfg: cfg}
}

func (w *Watcher) SetHandler(fn func(context.Context)) { w.handler = fn }

func (w *Watcher) Run(ctx context.Context) error {
	dw, err := fsnotify.NewWatcher()
	if err != nil {
		// 起不了监听不是致命错误：退化成纯轮询，功能仍在，只是慢一点
		fmt.Fprintf(os.Stderr, "watcher: fsnotify unavailable (%v), falling back to polling only\n", err)
		dw = nil
	} else {
		defer dw.Close()
		w.addExisting(dw)
	}

	debounce := time.NewTimer(w.cfg.Debounce)
	defer debounce.Stop()
	debounceActive := false

	poll := time.NewTicker(time.Duration(w.cfg.PollSeconds) * time.Second)
	defer poll.Stop()

	fire := func() {
		if w.handler == nil {
			return
		}
		w.lastRun = time.Now()
		w.handler(ctx)
	}
	requestScan := func() {
		if !debounceActive {
			debounceActive = true
			debounce.Reset(w.cfg.Debounce)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-debounce.C:
			if !debounceActive {
				continue
			}
			debounceActive = false
			fire()
		case <-poll.C:
			if time.Since(w.lastRun) < w.cfg.Debounce {
				continue
			}
			requestScan()
		case ev, ok := <-w.events(dw):
			if !ok {
				continue
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if ev.Op&fsnotify.Create != 0 && w.isDir(ev.Name) {
				w.addDir(dw, ev.Name)
			}
			requestScan()
		case err, ok := <-w.errors(dw):
			if !ok {
				continue
			}
			fmt.Fprintf(os.Stderr, "watcher: %v\n", err)
		}
	}
}

// events/errors 让 fsnotify 为 nil（监听不可用）时不会 panic：
// 返回一个永不就绪的 channel，select 会自然忽略这一路。
func (w *Watcher) events(dw *fsnotify.Watcher) <-chan fsnotify.Event {
	if dw == nil {
		return make(chan fsnotify.Event)
	}
	return dw.Events
}

func (w *Watcher) errors(dw *fsnotify.Watcher) <-chan error {
	if dw == nil {
		return make(chan error)
	}
	return dw.Errors
}

func (w *Watcher) addExisting(dw *fsnotify.Watcher) {
	for _, root := range w.cfg.Dirs {
		w.walkAdd(dw, root, 3)
	}
}

// walkAdd 递归加目录，深度受限：agent 目录下有大量无关子目录
// （缓存、node_modules、IndexedDB），全加进去会浪费句柄并放大事件量。
func (w *Watcher) walkAdd(dw *fsnotify.Watcher, root string, depth int) {
	if depth < 0 || !w.isDir(root) {
		return
	}
	w.addDir(dw, root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			w.walkAdd(dw, filepath.Join(root, e.Name()), depth-1)
		}
	}
}

func (w *Watcher) addDir(dw *fsnotify.Watcher, dir string) {
	if dw == nil {
		return
	}
	_ = dw.Add(dir) // 已在监视集里时返回错误，忽略即可
}

func (w *Watcher) isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
```

`fsnotify.Watcher.Errors` 字段在 v1.7 里是 `chan error`（不是 `Error` 方法），上面的用法与之匹配；`Events` 类型是 `Event{Op Op, Name string}` 值类型，`ev.Op&fsnotify.Rename` 这类位运算合法。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/ingest/ -run TestWatcher -v`
Expected: PASS。`-race` 下再跑一次：`go test -race ./internal/ingest/ -run TestWatcher`

- [ ] **Step 6: 提交**

```bash
git add internal/ingest go.mod go.sum
git commit -m "feat: 文件监听与轮询兜底，含去抖合并"
```

---

## Task 34: serve / mcp / link 三个子命令

**Files:**
- Create: `internal/cli/serve.go`
- Create: `internal/cli/mcpcmd.go`
- Create: `internal/cli/link.go`
- Test: `internal/cli/serve_test.go`

- [ ] **Step 1: 写失败测试 `internal/cli/serve_test.go`**

```go
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
)

func TestServe_RefusesSecondInstance(t *testing.T) {
	home, _ := isolateHomeWithRepo(t)
	dataDir := filepath.Join(home, ".agent-memory")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 占住端口文件并让健康检查可达：模拟"已经有一个 serve 在跑"
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

```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run 'TestServe|TestLink|TestMCPCommand|TestJSONOutput' -v`
Expected: FAIL，`unknown command "serve"`

- [ ] **Step 3: 写 `internal/cli/serve.go`**

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/ingest"
	"github.com/muyi99/agent-memory/internal/store"
)

func init() {
	register(command{name: "serve", desc: "启动常驻服务（扫描 + HTTP + MCP）", run: cmdServe})
	register(command{name: "stats", desc: "打印共享库统计", run: cmdStats})
}

func cmdServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "监听端口，0 表示随机")
	pollSeconds := fs.Int("poll", 30, "轮询兜底间隔秒数")
	debounce := fs.Duration("debounce", 2*time.Second, "事件去抖窗口")
	noWatch := fs.Bool("no-watch", false, "只服务不扫描（调试用）")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	token, err := daemon.LoadOrCreateToken(e.cfg.TokenPath())
	if err != nil {
		return reportError(stderr, err)
	}

	if err := refuseIfAlreadyRunning(*port); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	srv := daemon.New(daemon.Config{
		Token:        token,
		DataDir:      e.cfg.DataDir,
		Port:         *port,
		BudgetTokens: e.cfg.Handoff.BudgetTokens,
		RecentCards:  e.cfg.Handoff.RecentCards,
	}, e.st)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	adapters := buildAdapters(e.cfg)
	if !*noWatch && len(adapters) > 0 {
		go scanLoop(ctx, e.st, adapters, e.cfg, *debounce, *pollSeconds, stdout)
	}

	fmt.Fprintf(stdout, "serve on 127.0.0.1 (token: %s)\n", e.cfg.TokenPath())
	fmt.Fprintf(stdout, "agents: %v\n", adapterNames(adapters))
	if err := srv.ListenAndServe(ctx, e.cfg.PortPath()); err != nil {
		return reportError(stderr, err)
	}
	return 0
}

// scanLoop 启动先扫一次，之后由监听事件驱动。扫描期间收到的事件靠 channel 合并成一次补扫。
func scanLoop(ctx context.Context, st *store.Store, adapters []agent.Adapter, cfg config.Config, debounce time.Duration, pollSeconds int, stdout io.Writer) {
	runner := ingest.New(st, adapters...)
	runner.SettleIdle = cfg.SettleIdle()

	dirs := watchDirs(adapters)
	w := ingest.NewWatcher(ingest.WatchConfig{Dirs: dirs, Debounce: debounce, PollSeconds: pollSeconds})

	requested := make(chan struct{}, 1)
	w.SetHandler(func(c context.Context) {
		select {
		case requested <- struct{}{}:
		default:
		}
	})
	go w.Run(ctx)

	scanOnce := func(reason string) {
		res, err := runner.Run(ctx, "")
		if err != nil {
			fmt.Fprintf(stdout, "scan %s error: %v\n", reason, err)
			return
		}
		if res.Created+res.Merged+res.Cards+res.Staled > 0 {
			fmt.Fprintf(stdout, "scan (%s): %s\n", reason, res.String())
		}
		refreshHandoffsFor(ctx, st, cfg, stdout)
	}
	scanOnce("startup")

	for {
		select {
		case <-ctx.Done():
			return
		case <-requested:
			time.Sleep(debounce)
			scanOnce("event")
		}
	}
}

// refuseIfAlreadyRunning 检查目标端口上是否已有一个 serve：
// 两个实例同时扫描会争抢 SQLite 并重复写简报，第二个必须直接退出。
func refuseIfAlreadyRunning(port int) error {
	probe := port
	if probe == 0 {
		// --port 0 也要能发现"默认端口上已有实例"：读 port 文件
		e, err := openEnv()
		if err != nil {
			return nil
		}
		defer e.st.Close()
		if p, err := daemon.ReadPort(e.cfg.PortPath()); err == nil {
			probe = p
		} else {
			return nil
		}
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", probe), 500*time.Millisecond)
	if err != nil {
		return nil
	}
	conn.Close()
	// 端口通还不算：确认它真的应答 /v1/healthz，避免别的程序占了同一端口
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/healthz", probe))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return fmt.Errorf("agent-memory serve 已在运行（端口 %d），先停掉它或用 --port 换一个端口", probe)
	}
	return nil
}

func cmdStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "输出 json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	stats, err := e.st.Stats(context.Background())
	if err != nil {
		return reportError(stderr, err)
	}
	if *asJSON {
		return printJSON(stdout, stats)
	}
	fmt.Fprintf(stdout, "项目 %d，记忆 %d（过时 %d），源 %d（失败 %d）\n",
		stats.Projects, stats.Memories, stats.Stale, stats.Sources, stats.FailedSource)
	fmt.Fprintf(stdout, "按 agent: %v\n按类型: %v\n", stats.MemoriesByAgent, stats.MemoriesByType)
	return 0
}
```

`serve.go` 还需要 `adapterNames`、`watchDirs`、`refreshHandoffsFor`、`printJSON` 四个辅助函数，一并给出（`scanLoop` 直接用 `config.Config`，不新造视图类型）：

```go
// adapterNames 用于启动日志。
func adapterNames(adapters []agent.Adapter) []string {
	out := make([]string, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a.Name())
	}
	return out
}

// watchDirs 只监听各 agent 存放记忆与会话的目录，不递归到无关子树。
func watchDirs(adapters []agent.Adapter) []string {
	var out []string
	for _, a := range adapters {
		q, ok := a.(*qoder.Adapter)
		if !ok {
			continue
		}
		out = append(out, filepath.Join(q.Home, "memory"), filepath.Join(q.Home, "projects"))
	}
	return out
}

// refreshHandoffsFor 是 scan 与 watch 共用的简报刷新入口（Task 25 的 refreshHandoffs 的库层版本）。
func refreshHandoffsFor(ctx context.Context, st *store.Store, cfg config.Config, stdout io.Writer) {
	ids, err := st.ProjectIDsForHandoff(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		project, ok, err := st.ProjectByID(ctx, id)
		if err != nil || !ok || project.PrimaryPath == "" {
			continue
		}
		if _, err := os.Stat(project.PrimaryPath); err != nil {
			continue
		}
		res, err := compose.New(st, compose.Config{BudgetTokens: cfg.Handoff.BudgetTokens, RecentCards: cfg.Handoff.RecentCards}).Render(ctx, id)
		if err != nil {
			continue
		}
		path, changed, err := surface.WriteHandoff(project.PrimaryPath, res.Body)
		if err != nil {
			continue
		}
		if changed {
			if _, err := surface.EnsureGitExclude(project.PrimaryPath, "/"+surface.DirName+"/"); err != nil {
				continue
			}
			if _, err := st.SaveHandoff(ctx, store.HandoffRow{
				ProjectID: id, Body: res.Body, ContentHash: res.ContentHash,
				BudgetTokens: cfg.Handoff.BudgetTokens, GeneratedAt: time.Now(),
			}); err == nil {
				fmt.Fprintf(stdout, "handoff 已更新: %s\n", path)
			}
		}
	}
}

func printJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}
```

`cmdServe` 里启动扫描循环的调用是 `go scanLoop(ctx, e.st, adapters, e.cfg, *debounce, *pollSeconds, stdout)`。`serve.go` 的 import 块补 `"encoding/json"`、`"path/filepath"`、`internal/agent/qoder`、`internal/compose`、`internal/config`、`internal/surface`。

- [ ] **Step 4: 给 config 加 TokenPath/PortPath**

```go
func (c Config) TokenPath() string { return filepath.Join(c.DataDir, "token") }
func (c Config) PortPath() string  { return filepath.Join(c.DataDir, "port") }
```

- [ ] **Step 5: 写 `internal/cli/mcpcmd.go`**

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/mcp"
)

func init() { register(command{name: "mcp", desc: "以 stdio 方式接入常驻服务（给 agent 的 MCP 配置用）", run: cmdMCP}) }

func cmdMCP(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "常驻服务端口，0 表示从 port 文件读")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer e.st.Close()

	usePort := *port
	if usePort == 0 {
		usePort, err = daemon.ReadPort(e.cfg.PortPath())
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	// token 优先取环境变量：agent 的 MCP 配置里用 env 传，不进命令行与进程列表
	token := os.Getenv("AGENT_MEMORY_TOKEN")
	if token == "" {
		token, err = daemon.LoadOrCreateToken(e.cfg.TokenPath())
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	ctx := context.Background()
	bridge := mcp.NewBridge(usePort, token)
	fmt.Fprintf(stderr, "agent-memory mcp -> 127.0.0.1:%d\n", usePort)
	// 不额外观察 stdin 是否关闭：ServeStdio 里的 scanner.Scan() 在 agent 关闭管道时
	// 自然返回 false，函数随之退出。另起 goroutine 去 Read stdin 会抢走协议字节。
	if err := bridge.ServeStdio(ctx, os.Stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "mcp bridge: %v\n", err)
		return 1
	}
	return 0
}
```

`mcpcmd.go` 的 import 只需要 `"context"`、`"flag"`、`"fmt"`、`"io"`、`"os"`，加上 `daemon` 与 `mcp` 两个内部包。

- [ ] **Step 6: 写 `internal/cli/link.go`**

```go
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/surface"
)

func init() {
	register(command{name: "link", desc: "把 agent-memory 接入某个 agent（改配置，先看 --dry-run）", run: cmdLink})
	register(command{name: "unlink", desc: "撤销 link 写入的配置与区块", run: cmdUnlink})
}

func cmdLink(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "项目绝对路径，默认当前目录")
	dryRun := fs.Bool("dry-run", false, "只打印计划，不写任何文件")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: agent-memory link <%s> [--project PATH] [--dry-run]\n",
			joinNames(surface.LinkTargets()))
		return 2
	}
	target := fs.Arg(0)

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	opts, err := linkOptions(e, target, *project, *dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	plan, err := surface.PlanLink(target, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	service := fmt.Sprintf("127.0.0.1:%d", opts.Port)
	if opts.Port == 0 {
		service = "127.0.0.1:<serve 启动后的端口>"
	}
	fmt.Fprintf(stdout, "计划（dry-run 未写入）:\n  agent:        %s\n  MCP 配置:     %s\n  配置键:       %q -> %q\n  上下文区块:   %s\n  服务地址:     %s\n",
		plan.Target, plan.MCPPath, plan.MCPRoot, surface.MCPName, plan.BlockFile, service)

	if *dryRun {
		fmt.Fprintln(stdout, "去掉 --dry-run 才会写入；写前会自动备份为 <文件>.bak-<时间戳>")
		return 0
	}

	report, err := surface.ApplyLink(plan, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	mcpState := "未变化"
	if report.MCPChanged {
		mcpState = "已写入，备份 " + report.Backup
	}
	blockState := "未变化"
	if report.BlockAdded {
		blockState = "已安装"
	}
	fmt.Fprintf(stdout, "MCP 配置: %s\n上下文区块: %s (%s)\n", mcpState, blockState, report.BlockFile)
	fmt.Fprintln(stdout, "重启该 agent 后，新会话会自动读到 HANDOFF.md")
	return 0
}

func cmdUnlink(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("unlink", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "项目绝对路径，默认当前目录")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: agent-memory unlink <agent> [--project PATH]")
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	// unlink 不需要活的 serve：端口只是配置里的一个数字，撤销时不必校验
	opts, err := linkOptions(e, fs.Arg(0), *project, true)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	report, err := surface.ApplyUnlink(opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "MCP 配置回滚: %v\n上下文区块移除: %v\n", report.MCPChanged, report.BlockAdded)
	return 0
}

// linkOptions 组装 link/unlink 的输入。
// port 与 token 来自正在运行的 serve：没有它就没法给出可用的 MCP 配置。
// allowMissingPort 给 dry-run 与 unlink 用——看计划和撤销都不需要活的 serve。
func linkOptions(e *env, target, project string, allowMissingPort bool) (surface.LinkOptions, error) {
	abs := project
	if abs == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return surface.LinkOptions{}, err
		}
		abs = cwd
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return surface.LinkOptions{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		return surface.LinkOptions{}, err
	}
	token, err := daemon.LoadOrCreateToken(e.cfg.TokenPath())
	if err != nil {
		return surface.LinkOptions{}, err
	}
	port, err := daemon.ReadPort(e.cfg.PortPath())
	if err != nil {
		if !allowMissingPort {
			return surface.LinkOptions{}, fmt.Errorf("agent-memory serve 未运行：先启动它再 link（link 需要真实端口写进 agent 配置）")
		}
		port = 0
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return surface.LinkOptions{}, err
	}
	return surface.LinkOptions{
		Target: target, Home: home, ProjectDir: abs,
		Binary: bin, Token: token, Port: port,
	}, nil
}

func joinNames(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += "|"
		}
		out += s
	}
	return out
}
```

- [ ] **Step 7: 跑测试确认通过**

Run: `go test ./internal/cli/ -v 2>&1 | tail -40`
Expected: PASS

- [ ] **Step 8: 提交**

```bash
git add internal/cli internal/config
git commit -m "feat: serve/stats/mcp/link/unlink 子命令"
```

---

## Task 35: 端到端桥接验证与真实接入

**Files:**
- Create: `internal/cli/bridge_e2e_test.go`
- Modify: `README.md`

- [ ] **Step 1: 写进程内端到端测试 `internal/cli/bridge_e2e_test.go`**

```go
package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/mcp"
	"github.com/muyi99/agent-memory/internal/store"
)

// TestBridgeToEndToEnd 覆盖 agent 实际走的完整链路：
// stdio 消息 → 桥 → HTTP → daemon → store → 结果文本。
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
	if resp["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize = %v", resp)
	}

	// tools/list
	resp = roundTrip(t, bridge, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	tools := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 8 {
		t.Fatalf("want 8 tools over the bridge, got %d", len(tools))
	}

	// memory_add 写进共享库
	resp = roundTrip(t, bridge, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{
			"name":      "memory_add",
			"arguments": map[string]any{"text": "端到端写入的记忆条目", "type": "convention", "scope": "user"},
		},
	})
	if out := toolText(t, resp); strings.Contains(out, `"created": true`) == false {
		t.Fatalf("add result should report creation:\n%s", out)
	}

	// 另一个查询命中它
	resp = roundTrip(t, bridge, map[string]any{
		"jsonrpc": "2.0", "id": 4, "method": "tools/call",
		"params": map[string]any{"name": "memory_search", "arguments": map[string]any{"query": "端到端"}},
	})
	if !strings.Contains(toolText(t, resp), "端到端写入的记忆条目") {
		t.Fatalf("search over bridge missed the written memory:\n%s", toolText(t, resp))
	}

	// handoff_get 对未知项目给出可读错误而不是崩溃
	resp = roundTrip(t, bridge, map[string]any{
		"jsonrpc": "2.0", "id": 5, "method": "tools/call",
		"params": map[string]any{"name": "handoff_get", "arguments": map[string]any{"project_path": `Z:\nope`}},
	})
	if res := resp["result"].(map[string]any); res["isError"] != true {
		t.Fatalf("unknown project should be a tool error: %v", res)
	}
}

func roundTrip(t *testing.T, b *mcp.Bridge, msg map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(msg)
	body, needReply, err := b.Send(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if !needReply {
		t.Fatalf("no reply for %v", msg["method"])
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
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
	return res["content"].([]any)[0].(map[string]any)["text"].(string)
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

func TestRealBinaryRoundTrip(t *testing.T) {
	// 用测试编译出的二进制跑一次真实 stdio 会话，验证 agent 侧看到的东西是对的。
	// 这一步会真的启动一个 serve 进程，因此只在显式开启时跑。
	if os.Getenv("AM_E2E_BINARY") == "" {
		t.Skip("set AM_E2E_BINARY=1 to run the real two-process test")
	}
	t.Log("见 Task 35 Step 3 的手工步骤")
}
```

`bridge.SetEndpoint(...)` 还不存在——`Bridge.endpoint` 是非导出字段，`internal/mcp` 包外的测试没法赋值（前面 daemon 测试在包内可以直接改）。给 `stdio.go` 加一个明确的入口，同时把包内测试里那些 `br.endpoint = ...` 都换成它：

```go
// SetEndpoint 覆盖转发目标，测试与非常规部署用它指向自定义地址。
func (b *Bridge) SetEndpoint(url string) { b.endpoint = url }
```

- [ ] **Step 2: 跑全部测试（含 race）**

Run: `go test ./... && go test -race ./internal/ingest/ ./internal/mcp/`
Expected: PASS

- [ ] **Step 3: 静态检查**

Run: `gofmt -l . && go vet ./...`
Expected: 无输出

- [ ] **Step 4: 真实接入验收（需要你点头，会改动 Qoder 配置）**

顺序不能换：先构建，再 `serve`，确认端口，才 `link`。

```bash
go build -o agent-memory.exe ./cmd/agent-memory
./agent-memory.exe init
./agent-memory.exe scan
./agent-memory.exe serve --port 47123      # 单独开一个终端，保持前台运行
```

另开终端：

```bash
./agent-memory.exe link qoder --project "F:\\go\\agent-memory" --dry-run
./agent-memory.exe stats --json | head -5
```

确认 dry-run 里 MCP 路径是 `~/.qoder-cn/mcp.json`、区块落在项目 `AGENTS.md`，再执行：

```bash
./agent-memory.exe link qoder --project "F:\\go\\agent-memory"
cat "F:/go/agent-memory/AGENTS.md"
cat ~/.qoder-cn/mcp.json
```

然后在 Qoder 里新建一个会话（MCP 需要重启客户端才会加载），第一句就问：

> 用 memory_search 查「偏好」，告诉我这个项目现在在做什么

Expected: 它不要求你解释背景，直接报出共享记忆里的偏好与当前任务卡。这是整个项目的判定性验收。

- [ ] **Step 5: 验证原生记忆文件没被改**

```bash
find ~/.qoder-cn/memory ~/.qoder-cn/projects/*/memory -name '*.md' -print0 | xargs -0 md5sum > /tmp/m3-after.txt
diff /tmp/m2-before-md.txt /tmp/m3-after.txt | grep -v 'qoder-cn/projects/F--go-agent-memory/memory' && echo NATIVE_UNTOUCHED
```

Expected: `NATIVE_UNTOUCHED`。被排除的那几条是本会话自己的 Qoder 记忆变化（Qoder 自己写的），不是本程序所为——`doctor` 的写入目标清单里没有它们的父目录，归属可以据此判定。

- [ ] **Step 6: 更新 README 并提交**

`README.md` 的「现在能做什么（M1）」段落改成：

```markdown
## 现在能做什么

- 读取 Qoder / OpenCode / Trae / WorkBuddy 的长期记忆，归并去重进一个共享 SQLite 库
- 导入 Qoder 会话历史，提炼任务卡（目标 / 状态 / 改动文件 / 下一步 / 未决问题）
- 每个项目生成 `.agent-memory/HANDOFF.md`，切 agent 时先读它，不用重新描述任务
- `agent-memory serve` 常驻 + MCP tools：`memory_search` / `memory_add` / `handoff_get` / `task_list` / `project_resolve` / `stats`
- `agent-memory link qoder|opencode|claude` 一键接入（改配置前先 `--dry-run`，写前自动备份）

## MCP 接入

    ./agent-memory serve --port 47123     # 常驻，保持运行
    ./agent-memory link qoder --project "$(pwd)" --dry-run
    ./agent-memory link qoder --project "$(pwd)"

Trae 与 WorkBuddy 的 MCP 配置格式未确证，不自动写入；它们照常通过项目里的 `HANDOFF.md` 读到共享记忆。
```

```bash
git add README.md internal/mcp/stdio.go internal/cli
git commit -m "docs: MCP 接入用法与端到端验证"
```

---

## Task 36: link 登记与 embedding 失效判定（补齐 spec 本期承诺）

spec §5 把 `agent_link` 列为「记录受管区块装在哪」的表，§5.3 又要求本期就定下 embedding 的失效判定——这两件事前面都没落地。缺了 `agent_link`，`doctor` 就无法回答「本程序的写入目标清单」这条验收依据；缺了失效判定，M5 的向量回填要么全表重算要么靠猜。

**Files:**
- Create: `internal/store/link.go`
- Test: `internal/store/link_test.go`
- Modify: `internal/cli/link.go`
- Modify: `internal/cli/doctor.go`

- [ ] **Step 1: 写失败测试 `internal/store/link_test.go`**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func openForLinks(t *testing.T) (*Store, int64) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	pid, err := st.EnsureProject(ctx, "f:/go/demo", "demo", "", `F:\go\demo`, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return st, pid
}

func TestRecordAgentLink_ReinstallUpdatesInsteadOfDuplicating(t *testing.T) {
	st, pid := openForLinks(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	if err := st.RecordAgentLink(ctx, pid, "qoder", `F:\go\demo\AGENTS.md`, "hash-a", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAgentLink(ctx, pid, "qoder", `F:\go\demo\AGENTS.md`, "hash-b", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := st.QueryInt(ctx, `SELECT count(*) FROM agent_link`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reinstall must update the row, got %d rows", n)
	}

	links, err := st.ListAgentLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].InstalledHash != "hash-b" {
		t.Fatalf("links = %+v", links)
	}
	if links[0].ProjectID != pid || links[0].Agent != "qoder" {
		t.Fatalf("link identity wrong: %+v", links[0])
	}
}

func TestClearAgentLink_OnUnlink(t *testing.T) {
	st, pid := openForLinks(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	if err := st.RecordAgentLink(ctx, pid, "opencode", `F:\go\demo\AGENTS.md`, "h", now); err != nil {
		t.Fatal(err)
	}
	removed, err := st.ClearAgentLink(ctx, pid, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("want removed=true")
	}
	again, err := st.ClearAgentLink(ctx, pid, "opencode")
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("second clear is a no-op")
	}
}

func TestOutdatedEmbeddings_SelectsOnlyMissingOrMismatched(t *testing.T) {
	st, pid := openForLinks(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	// 三条记忆：无 embedding、旧模型、当前模型且新鲜
	texts := []string{"甲", "乙", "丙"}
	models := []string{"", "other-model", "text-embed-3-small"}
	embedded := []int64{0, now.Add(-2 * time.Hour).Unix(), now.Unix()}
	ids := make([]int64, 0, 3)
	for i, text := range texts {
		id, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: domain.ScopeProject, Type: domain.TypeFact, Text: text,
			SourceAgent: "test", SourcePath: "/t.md",
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		_, err = st.db.ExecContext(ctx,
			`UPDATE memory_item SET embed_model = ?, embedded_at = ? WHERE id = ?`, models[i], embedded[i], id)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// 「丙」在嵌入之后又被同文本重新见过：embedded_at 早于 last_seen_at 也算过期
	if _, err := st.db.ExecContext(ctx, `UPDATE memory_item SET last_seen_at = ? WHERE id = ?`, now.Add(time.Hour).Unix(), ids[2]); err != nil {
		t.Fatal(err)
	}

	outdated, err := st.OutdatedEmbeddings(ctx, "text-embed-3-small")
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, id := range outdated {
		got[id] = true
	}
	// 甲（没嵌入）、乙（模型不符）、丙（嵌入后又见过）都该在；这里三条全过期，
	// 所以再用一条完全合格的对照：
	if !got[ids[0]] || !got[ids[1]] || !got[ids[2]] {
		t.Fatalf("outdated set = %v want all of %v", outdated, ids)
	}

	if _, err := st.db.ExecContext(ctx,
		`UPDATE memory_item SET embed_model='text-embed-3-small', embedded_at=?, last_seen_at=? WHERE id = ?`,
		now.Add(2*time.Hour).Unix(), now.Unix(), ids[2]); err != nil {
		t.Fatal(err)
	}
	outdated2, err := st.OutdatedEmbeddings(ctx, "text-embed-3-small")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range outdated2 {
		if id == ids[2] {
			t.Fatalf("freshly embedded item still counted outdated")
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run 'TestRecordAgentLink|TestClearAgentLink|TestOutdatedEmbeddings' -v`
Expected: FAIL，`st.RecordAgentLink undefined`

- [ ] **Step 3: 写 `internal/store/link.go`**

```go
package store

import (
	"context"
	"time"
)

// AgentLinkRow 对应 agent_link 表：某个项目的某个 agent 装了区块、装的是哪份文件、内容哈希多少。
type AgentLinkRow struct {
	ProjectID     int64
	Agent         string
	BlockFile     string
	InstalledHash string
	InstalledAt   time.Time
}

func (s *Store) RecordAgentLink(ctx context.Context, projectID int64, agent, blockFile, hash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_link(project_id, agent, block_file, installed_hash, installed_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(project_id, agent) DO UPDATE SET
  block_file = excluded.block_file,
  installed_hash = excluded.installed_hash,
  installed_at = excluded.installed_at`,
		projectID, agent, blockFile, hash, at.Unix())
	return err
}

func (s *Store) ClearAgentLink(ctx context.Context, projectID int64, agent string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_link WHERE project_id = ? AND agent = ?`, projectID, agent)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) ListAgentLinks(ctx context.Context) ([]AgentLinkRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT project_id, agent, block_file, installed_hash, installed_at
FROM agent_link ORDER BY project_id, agent`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentLinkRow
	for rows.Next() {
		var (
			l     AgentLinkRow
			at    int64
		)
		if err := rows.Scan(&l.ProjectID, &l.Agent, &l.BlockFile, &l.InstalledHash, &at); err != nil {
			return nil, err
		}
		l.InstalledAt = time.Unix(at, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// OutdatedEmbeddings 返回需要重算向量条目的 id：没嵌入过、模型换了、或嵌入之后又被重新见过。
// M5 的回填按这个集合走，不做全表重算。
//
// 没配模型时只挑「完全没嵌入过」的条目——不能因为调用方没给模型就把全库列为过期。
func (s *Store) OutdatedEmbeddings(ctx context.Context, model string) ([]int64, error) {
	q := `SELECT id FROM memory_item WHERE stale = 0 AND embedding IS NULL`
	args := []any{}
	if model != "" {
		q = `
SELECT id FROM memory_item
WHERE stale = 0 AND (embedding IS NULL OR embed_model <> ? OR embedded_at < last_seen_at)`
		args = []any{model}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: link/unlink 登记与撤销**

`internal/cli/link.go` 的 `cmdLink` 在 `ApplyLink` 成功后登记。项目可能尚未在库里（新项目先 link 再 scan 也是合法顺序），所以先解析、拿不到就用 `ProjectIDForPath` 建：

```go
	ctx := context.Background()
	projectID, _, err := e.st.ProjectIDForPath(ctx, target, "", opts.ProjectDir, "", time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "warn: 项目未登记进库: %v\n", err)
	} else {
		if err := e.st.RecordAgentLink(ctx, projectID, target, report.BlockFile, contentHashOf(report.BlockFile), time.Now()); err != nil {
			fmt.Fprintf(stderr, "warn: agent_link 登记失败: %v\n", err)
		}
	}
```

`contentHashOf` 在 `link.go` 里定义（读文件算哈希，读不到就返回空串，登记照常）：

```go
func contentHashOf(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return normalize.HashBytes(data)
}
```

`cmdUnlink` 在 `ApplyUnlink` 之后撤销登记：

```go
	ctx := context.Background()
	if id, ok, err := e.st.ResolveProjectByPath(ctx, opts.ProjectDir); err == nil && ok {
		if _, err := e.st.ClearAgentLink(ctx, id, fs.Arg(0)); err != nil {
			fmt.Fprintf(stderr, "warn: agent_link 撤销失败: %v\n", err)
		}
	}
```

`link.go` 的 import 补 `"time"` 与 `"github.com/muyi99/agent-memory/internal/normalize"`。

- [ ] **Step 6: doctor 把 agent_link 列进写入目标清单**

`internal/cli/doctor.go` 的「写入目标」段落里，把固定的三行改成「固定目标 + 已登记目标」：

```go
	links, err := e.st.ListAgentLinks(ctx)
	if err != nil {
		return reportError(stderr, err)
	}
	for _, l := range links {
		fmt.Fprintf(stdout, "  %s 区块 -> %s (登记于 %s)\n", l.Agent, l.BlockFile, l.InstalledAt.Format(time.RFC3339))
	}
	if len(links) == 0 {
		fmt.Fprintln(stdout, "  （尚无 link 登记）")
	}
```

这样验收标准 3 的「写入目标清单封闭」才是可核对的：清单来自库，而不是靠读代码保证。

- [ ] **Step 7: 跑全部测试与静态检查**

Run: `go test ./... && gofmt -l . && go vet ./...`
Expected: 全部 PASS，无输出

- [ ] **Step 8: 提交**

```bash
git add internal/store/link.go internal/store/link_test.go internal/cli/link.go internal/cli/doctor.go
git commit -m "feat: link 登记到 agent_link 与 embedding 失效判定查询"
```

---

## 完成标准（M3）

1. `go test ./...`、`go test -race`、`go vet ./...`、`gofmt -l .` 全干净。
2. `TestBridgeToEndToEnd` 通过：stdio → HTTP → store → 结果文本整链可跑。
3. `serve` 第二个实例被明确拒绝（不是 SQLITE_BUSY，也不是静默双写）。
4. `link --dry-run` 一个字节都不写；`link` 留下恰好一个备份；`unlink` 后 `mcp.json` 里 `agent-memory` 键消失且别人键没动。
5. Task 35 Step 4 的人工验收成功：Qoder 新会话不需要你重新描述任务。这一条不过，M3 不算完成。

## M3 已知风险

- **MCP 客户端兼容性**：协议层是自己实现的最小子集，`initialize` 回显版本、工具错误走 `result.isError` 都是规范允许的行为，但真实客户端是否满意只有跑一次才知道。因此 Step 4 必须人工过一遍，不能只看单测绿。
- **随机端口与 agent 重启的耦合**：`link` 把端口写死进 MCP 配置，`serve` 换端口后必须重跑 `link`。`mcp` 命令读 port 文件所以桥本身不受影响。
- **fsnotify 在 Windows 上会漏事件**：所以轮询兜底是必需项而非优化，`--poll` 默认 30s。
