# 桌面体验优化 · 阶段一（界面与下钻）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把桌面版首屏改成「打开即有内容、五个数字可下钻、文案可读、页面简约且有克制动效」，并补齐三个只读接口。

**Architecture:** 复用现有 daemon 与单文件页面。`store` 增加三个只读查询（源清单、带过滤的记忆列表、带计数的项目列表），`daemon` 暴露对应端点；`index.html` 整体重写为统计带 + 抽屉式明细 + 自动刷新，仍为单文件、零外部依赖、全部动态内容走 `textContent`。

**Tech Stack:** Go 1.22.6、`modernc.org/sqlite`、标准库 `net/http`、原生 HTML/CSS/JS（无框架、无 CDN）。

**上游 spec:** `docs/superpowers/specs/2026-10-28-desktop-ux-design.md`（3.1–3.8、4、8 验收 1–3、7）。阶段二（托盘、自启、快捷方式、图标）另行成计划。

---

## 开工约定

- 分支从 `master` 新建 `feat/desktop-ux-1`；不开 worktree。
- Windows + `core.autocrlf=true`：**不要**用 `gofmt -l .` 判断格式（会把所有文件误报）；用 `git show :<path> | gofmt -d`。
- 前端仍是 `internal/daemon/index.html` 单文件，通过 `//go:embed` 提供；不新增 JS 文件、不引入依赖。
- 所有动态文本必须用 `textContent` 或 `createElement` + `textContent`；页面里不允许出现 `innerHTML`。
- 提交信息用中文；每个任务结束提交一次。

---

## 文件结构

```
internal/store/source.go      + SourceRow / ListSources
internal/store/memory.go      + MemoryFilter / ListMemoriesFiltered
internal/store/project.go     + ProjectSummary / ListProjectSummaries
internal/daemon/server.go     注册 GET /v1/sources；扩展 /v1/memory、/v1/projects
internal/daemon/web.go        + handleListSources；handleListMemory 参数解析
internal/daemon/sources_test.go（新建）      store 查询测试
internal/daemon/web_test.go   端点测试（追加）
internal/daemon/index.html    整体重写
internal/daemon/index_test.go（新建）        页面内容与术语断言
docs/usage.md                 界面说明更新
```

---

## Task 1: 源清单查询与 `/v1/sources`

**Files:**
- Modify: `internal/store/source.go`
- Create: `internal/store/source_list_test.go`
- Modify: `internal/daemon/web.go`、`internal/daemon/server.go`
- Test: `internal/daemon/web_test.go`（追加）

- [ ] **Step 1: 写失败的 store 测试**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestListSources_OrdersByUpdatedAndFiltersFailed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := time.Unix(1700000000, 0)

	if err := s.PutSource(ctx, domainSource("qoder", "memory", "/q/ok.md", "h1"), base.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domainSource("trae", "memory", "/t/bad.md", "h2"), base.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "trae", "memory", "/t/bad.md", "parse: bad", base.Add(2*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListSources(ctx, "", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Path != "/t/bad.md" {
		t.Fatalf("want newest first, got %+v", all)
	}
	if all[0].LastError != "parse: bad" {
		t.Fatalf("last_error not carried: %+v", all[0])
	}

	failed, err := s.ListSources(ctx, "", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].Agent != "trae" {
		t.Fatalf("failed filter wrong: %+v", failed)
	}

	byAgent, err := s.ListSources(ctx, "qoder", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 || byAgent[0].Path != "/q/ok.md" {
		t.Fatalf("agent filter wrong: %+v", byAgent)
	}
}

func domainSource(agent, kind, path, hash string) domain.SourceState {
	return domain.SourceState{Agent: agent, Kind: kind, Path: path, ContentHash: hash, Size: 10}
}
```

（`domainSource` 里用到 `domain`，文件 import 补 `"github.com/muyi99/agent-memory/internal/domain"`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestListSources -v`
Expected: FAIL，`s.ListSources undefined`

- [ ] **Step 3: 在 `internal/store/source.go` 追加实现**

```go
// SourceRow 是源清单的一行，供 Web UI 的「源」抽屉展示。
type SourceRow struct {
	Agent       string
	Kind        string
	Path        string
	ContentHash string
	LastError   string
	LastSeenRun int64
	UpdatedAt   time.Time
	ChangedAt   time.Time
	Size        int64
}

// ListSources 按最近更新时间倒序列出扫描源。failedOnly 只返回解析失败的源；
// agent 为空表示不过滤。只读浏览用，不参与扫描决策。
func (s *Store) ListSources(ctx context.Context, agent string, failedOnly bool, limit int) ([]SourceRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT agent, kind, path, content_hash, last_error, last_seen_run, updated_at, changed_at, size
FROM source WHERE 1=1`
	args := []any{}
	if agent != "" {
		q += ` AND agent = ?`
		args = append(args, agent)
	}
	if failedOnly {
		q += ` AND last_error <> ''`
	}
	q += ` ORDER BY updated_at DESC, agent, path LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SourceRow
	for rows.Next() {
		var (
			r                   SourceRow
			updatedAt, changedAt int64
		)
		if err := rows.Scan(&r.Agent, &r.Kind, &r.Path, &r.ContentHash, &r.LastError,
			&r.LastSeenRun, &updatedAt, &changedAt, &r.Size); err != nil {
			return nil, err
		}
		r.UpdatedAt = time.Unix(updatedAt, 0)
		if changedAt != 0 {
			r.ChangedAt = time.Unix(changedAt, 0)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 跑 store 测试**

Run: `go test ./internal/store/ -run TestListSources -v`
Expected: PASS

- [ ] **Step 5: 写失败的端点测试（追加到 `internal/daemon/web_test.go`）**

```go
func TestSourcesEndpoint(t *testing.T) {
	s, st := newWebServer(t)
	ctx := context.Background()
	if err := st.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, 100); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSourceError(ctx, "trae", "memory", "/t/b.md", "bad", 200); err != nil {
		t.Fatal(err)
	}

	rec := doWebJSON(t, s, http.MethodGet, "/v1/sources", "token-012345678901234567890123456789", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Sources []struct {
			Agent     string `json:"agent"`
			Path      string `json:"path"`
			LastError string `json:"last_error"`
		} `json:"sources"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 2 {
		t.Fatalf("count=%d", out.Count)
	}

	rec = doWebJSON(t, s, http.MethodGet, "/v1/sources?failed=1", "token-012345678901234567890123456789", nil)
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Count != 1 || out.Sources[0].Agent != "trae" {
		t.Fatalf("failed filter: %+v", out)
	}
}
```

（`newWebServer` 与 `doWebJSON` 已存在于 `web_test.go`；若名字不同，按现有 helper 调整调用，不要新造第二套。）

- [ ] **Step 6: 跑端点测试确认失败**

Run: `go test ./internal/daemon/ -run TestSourcesEndpoint -v`
Expected: FAIL，404

- [ ] **Step 7: 实现 handler 与路由**

`internal/daemon/web.go` 追加：

```go
// handleListSources 列出扫描源，供「源 / 失败源」抽屉使用。
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := s.st.ListSources(r.Context(), r.URL.Query().Get("agent"),
		r.URL.Query().Get("failed") == "1", limit)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": rows, "count": len(rows)})
}
```

`internal/daemon/server.go` 路由表加一行（`s.auth` 与其它只读端点一致）：

```go
	s.mux.HandleFunc("GET /v1/sources", s.auth(s.handleListSources))
```

import 补 `"strconv"`。

- [ ] **Step 8: 跑测试**

Run: `go test ./internal/daemon/ -run TestSourcesEndpoint -v && go test ./... -count=1`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add internal/store/source.go internal/store/source_list_test.go internal/daemon/web.go internal/daemon/server.go internal/daemon/web_test.go
git commit -m "feat: 源清单查询与 /v1/sources 端点"
```

---

## Task 2: `/v1/memory` 支持跨项目、仅过时、按类型

**Files:**
- Modify: `internal/store/memory.go`
- Test: `internal/store/memory_filter_test.go`（新建）
- Modify: `internal/daemon/server.go`（handleListMemory）

- [ ] **Step 1: 写失败的 store 测试**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestListMemoriesFiltered(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	add := func(scope domain.Scope, pid int64, typ domain.MemoryType, text string) int64 {
		t.Helper()
		id, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: scope, Type: typ, Text: text, SourceAgent: "test", SourcePath: "/t.md",
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	userPref := add(domain.ScopeUser, 0, domain.TypePreference, "用户偏好甲")
	projC := add(domain.ScopeProject, 3, domain.TypeConstraint, "项目约束乙")
	projP := add(domain.ScopeProject, 3, domain.TypePreference, "项目偏好丙")
	if err := s.SetStaleByID(ctx, projP, true); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListMemoriesFiltered(ctx, MemoryFilter{AllProjects: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all=%d", len(all))
	}

	onlyStale := true
	stale, err := s.ListMemoriesFiltered(ctx, MemoryFilter{AllProjects: true, Stale: &onlyStale, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].ID != projP {
		t.Fatalf("stale filter: %+v", stale)
	}

	byType, err := s.ListMemoriesFiltered(ctx, MemoryFilter{AllProjects: true, Type: domain.TypeConstraint, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(byType) != 1 || byType[0].ID != projC {
		t.Fatalf("type filter: %+v", byType)
	}

	projectScoped, err := s.ListMemoriesFiltered(ctx, MemoryFilter{ProjectID: 3, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(projectScoped) != 3 { // 项目 3 的两条 + 用户级一条
		t.Fatalf("project scope: %+v", projectScoped)
	}

	userOnly, err := s.ListMemoriesFiltered(ctx, MemoryFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(userOnly) != 1 || userOnly[0].ID != userPref {
		t.Fatalf("user only: %+v", userOnly)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestListMemoriesFiltered -v`
Expected: FAIL，`undefined: MemoryFilter`

- [ ] **Step 3: 在 `internal/store/memory.go` 追加实现**

```go
// MemoryFilter 是只读浏览用的过滤条件。与 SearchQuery 不同，它不做相关性排序，
// 只按最近变更排；AllProjects 显式表示跨项目浏览，避免与「省略项目 = 用户级」混淆。
type MemoryFilter struct {
	ProjectID   int64
	AllProjects bool
	Scope       domain.Scope
	Type        domain.MemoryType
	Stale       *bool
	Limit       int
}

func (s *Store) ListMemoriesFiltered(ctx context.Context, f MemoryFilter) ([]domain.MemoryItem, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q := `SELECT id FROM memory_item WHERE 1=1`
	args := []any{}
	switch {
	case f.AllProjects:
	case f.ProjectID > 0:
		q += ` AND (project_id = ? OR scope = ?)`
		args = append(args, f.ProjectID, string(domain.ScopeUser))
	default:
		q += ` AND scope = ?`
		args = append(args, string(domain.ScopeUser))
	}
	if f.Scope != "" {
		q += ` AND scope = ?`
		args = append(args, string(f.Scope))
	}
	if f.Type != "" {
		q += ` AND type = ?`
		args = append(args, string(f.Type))
	}
	if f.Stale != nil {
		if *f.Stale {
			q += ` AND stale = 1`
		} else {
			q += ` AND stale = 0`
		}
	}
	q += ` ORDER BY last_seen_at DESC, id DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]domain.MemoryItem, 0, len(ids))
	for _, id := range ids {
		m, err := s.MemoryByID(ctx, id)
		if errors.Is(err, domain.ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
```

- [ ] **Step 4: 跑 store 测试**

Run: `go test ./internal/store/ -run TestListMemoriesFiltered -v`
Expected: PASS

- [ ] **Step 5: 扩展 handler 并补端点测试**

`internal/daemon/server.go` 的 `handleListMemory` 改为：

```go
func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	filter := store.MemoryFilter{
		AllProjects: q.Get("all") == "1",
		Scope:       domain.Scope(q.Get("scope")),
		Type:        domain.MemoryType(q.Get("type")),
	}
	if v := q.Get("stale"); v == "1" || v == "0" {
		stale := v == "1"
		filter.Stale = &stale
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	ref := q.Get("project_path")
	if ref == "" {
		ref = q.Get("project_key")
	}
	if !filter.AllProjects && ref != "" {
		id, err := s.projectIDFromRequest(ctx, ref, ref)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filter.ProjectID = id
	}

	items, err := s.st.ListMemoriesFiltered(ctx, filter)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": items, "count": len(items)})
}
```

追加端点测试：

```go
func TestMemoryListFilters(t *testing.T) {
	s, st := newWebServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	mk := func(scope domain.Scope, pid int64, typ domain.MemoryType, text string) int64 {
		id, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: scope, Type: typ, Text: text, SourceAgent: "test", SourcePath: "/t.md"}, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk(domain.ScopeUser, 0, domain.TypePreference, "用户偏好")
	staleID := mk(domain.ScopeProject, 3, domain.TypeFact, "项目事实")
	if err := st.SetStaleByID(ctx, staleID, true); err != nil {
		t.Fatal(err)
	}
	pid, err := st.EnsureProject(ctx, "f:/p", "p", "", `F:\p`, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkProjectPath(ctx, pid, "test", "", `F:\p`, "high"); err != nil {
		t.Fatal(err)
	}
	_ = pid

	token := "token-012345678901234567890123456789"
	cases := []struct {
		url  string
		want int
	}{
		{"/v1/memory?all=1", 2},
		{"/v1/memory?all=1&stale=1", 1},
		{"/v1/memory?all=1&type=preference", 1},
		{"/v1/memory", 1},
	}
	for _, c := range cases {
		rec := doWebJSON(t, s, http.MethodGet, c.url, token, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s -> %d", c.url, rec.Code)
		}
		var out struct{ Count int `json:"count"` }
		json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Count != c.want {
			t.Fatalf("%s count=%d want %d", c.url, out.Count, c.want)
		}
	}
}
```

- [ ] **Step 6: 跑测试并提交**

Run: `go test ./internal/store/ ./internal/daemon/ -count=1 && go test ./... -count=1`
Expected: PASS

```bash
git add internal/store/memory.go internal/store/memory_filter_test.go internal/daemon/server.go internal/daemon/web_test.go
git commit -m "feat: /v1/memory 支持跨项目、仅过时与按类型过滤"
```

---

## Task 3: `/v1/projects` 带记忆计数

**Files:**
- Modify: `internal/store/project.go`
- Test: `internal/store/project_summary_test.go`（新建）
- Modify: `internal/daemon/server.go`

- [ ] **Step 1: 写失败的 store 测试**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestListProjectSummaries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	a, err := s.EnsureProject(ctx, "a/repo", "a", "", `F:\a`, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsureProject(ctx, "b/repo", "b", "", `F:\b`, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, a, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "甲", SourceAgent: "t", SourcePath: "/t.md"}, now); err != nil {
		t.Fatal(err)
	}
	staleID, _, err := s.UpsertMemory(ctx, a, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "乙", SourceAgent: "t", SourcePath: "/t.md"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStaleByID(ctx, staleID, true); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListProjectSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "a/repo" {
		t.Fatalf("order/size: %+v", got)
	}
	if got[0].MemoryCount != 2 || got[0].StaleCount != 1 {
		t.Fatalf("counts: %+v", got[0])
	}
	if got[1].ID != b || got[1].MemoryCount != 0 {
		t.Fatalf("empty project: %+v", got[1])
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestListProjectSummaries -v`
Expected: FAIL，`undefined: ListProjectSummaries`

- [ ] **Step 3: 实现**

```go
// ProjectSummary 是项目带记忆计数的只读视图，供项目下拉与「项目」抽屉使用。
type ProjectSummary struct {
	ID           int64
	Key          string
	DisplayName  string
	PrimaryPath  string
	MemoryCount  int
	StaleCount   int
	LastMemoryAt time.Time
}

func (s *Store) ListProjectSummaries(ctx context.Context) ([]ProjectSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.key, p.display_name, p.primary_path,
       COUNT(m.id),
       SUM(CASE WHEN m.stale = 1 THEN 1 ELSE 0 END),
       COALESCE(MAX(m.last_seen_at), 0)
FROM project p
LEFT JOIN memory_item m ON m.project_id = p.id AND m.scope = 'project'
GROUP BY p.id
ORDER BY p.key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ProjectSummary
	for rows.Next() {
		var (
			ps       ProjectSummary
			lastSeen int64
			stale    sql.NullInt64
		)
		if err := rows.Scan(&ps.ID, &ps.Key, &ps.DisplayName, &ps.PrimaryPath,
			&ps.MemoryCount, &stale, &lastSeen); err != nil {
			return nil, err
		}
		if stale.Valid {
			ps.StaleCount = int(stale.Int64)
		}
		if lastSeen != 0 {
			ps.LastMemoryAt = time.Unix(lastSeen, 0)
		}
		out = append(out, ps)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 跑 store 测试**

Run: `go test ./internal/store/ -run TestListProjectSummaries -v`
Expected: PASS

- [ ] **Step 5: 扩展端点与测试**

`handleListProjects` 改为输出 `memory_count`、`stale_count`、`last_memory_at`：

```go
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	summaries, err := s.st.ListProjectSummaries(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(summaries))
	for _, p := range summaries {
		out = append(out, map[string]any{
			"id": p.ID, "key": p.Key, "display_name": p.DisplayName,
			"primary_path": p.PrimaryPath,
			"memory_count": p.MemoryCount, "stale_count": p.StaleCount,
			"last_memory_at": p.LastMemoryAt.Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "count": len(out)})
}
```

端点测试追加（沿用 TestMemoryListFilters 的 fixture 方式）：

```go
func TestProjectsEndpointCounts(t *testing.T) {
	s, st := newWebServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := st.EnsureProject(ctx, "f:/counted", "counted", "", `F:\counted`, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "一条", SourceAgent: "t", SourcePath: "/t.md"}, now); err != nil {
		t.Fatal(err)
	}
	rec := doWebJSON(t, s, http.MethodGet, "/v1/projects", "token-012345678901234567890123456789", nil)
	var out struct {
		Projects []struct {
			Key         string `json:"key"`
			MemoryCount int    `json:"memory_count"`
		} `json:"projects"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Projects) != 1 || out.Projects[0].MemoryCount != 1 {
		t.Fatalf("%+v", out.Projects)
	}
}
```

- [ ] **Step 6: 跑测试并提交**

Run: `go test ./internal/store/ ./internal/daemon/ -count=1 && go test ./... -count=1`
Expected: PASS

```bash
git add internal/store/project.go internal/store/project_summary_test.go internal/daemon/server.go internal/daemon/web_test.go
git commit -m "feat: 项目列表带记忆计数与最近活动"
```

---

## Task 4: 页面重写（视觉 + 下钻 + 自动刷新）

这是本阶段的主体。**完整替换** `internal/daemon/index.html`，保留现有元素 ID 契约（`project-select`、`search-input`、`task-list`、`handoff-body` 等），新增抽屉与统计带。

**Files:**
- Rewrite: `internal/daemon/index.html`
- Create: `internal/daemon/index_test.go`

- [ ] **Step 1: 写失败的页面契约测试**

```go
package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestIndexPage_Contract(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)

	mustHave := []string{
		`id="stat-band"`, `data-drawer="projects"`, `data-drawer="memories"`,
		`data-drawer="stale"`, `data-drawer="sources"`, `data-drawer="failed"`,
		`id="drawer"`, `id="drawer-close"`, `id="project-select"`,
		`id="search-input"`, `id="task-list"`, `id="handoff-body"`,
		`立即同步`, `生成交接简报`, `不再使用`, `恢复使用`,
		`prefers-reduced-motion`, `localStorage`, `30_000`, `focus`,
	}
	for _, want := range mustHave {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	mustNotHave := []string{
		`写入 HANDOFF.md`, `标过时`, `复活<`, `得分 `, `project_key`, `tokens`,
		`innerHTML`, `读取概览`, `写入目标`,
	}
	for _, bad := range mustNotHave {
		if strings.Contains(page, bad) {
			t.Fatalf("page still contains legacy copy %q", bad)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/daemon/ -run TestIndexPage_Contract -v`
Expected: FAIL（旧页面缺 `stat-band`、含旧文案）

- [ ] **Step 3: 重写 `internal/daemon/index.html`**

按下面的结构与规则实现（这是完整文件，不是片段；所有动态内容用 `textContent`）：

**HTML 结构**

```html
<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>agent-memory</title>
<style>/* 见下方 CSS 变量与规则 */</style>
</head>
<body>
<header class="topbar">
  <div class="brand">agent-memory <span id="health-dot" class="dot dot-unknown"></span><span id="health-text" class="muted">检查中</span></div>
  <div class="actions">
    <label class="sr-only" for="project-select">项目</label>
    <select id="project-select"><option value="">全部/用户级</option></select>
    <button id="refresh-btn" class="btn ghost">刷新</button>
    <button id="sync-btn" class="btn primary">立即同步</button>
  </div>
</header>

<main>
  <section id="stat-band" class="stat-band">
    <button class="stat" data-drawer="projects"><span id="stat-projects" class="num">0</span><span class="label">项目</span></button>
    <button class="stat" data-drawer="memories"><span id="stat-memories" class="num">0</span><span class="label">记忆</span></button>
    <button class="stat" data-drawer="stale"><span id="stat-stale" class="num">0</span><span class="label">过时</span></button>
    <button class="stat" data-drawer="sources"><span id="stat-sources" class="num">0</span><span class="label">源</span></button>
    <button class="stat" data-drawer="failed"><span id="stat-failed" class="num">0</span><span class="label">失败源</span></button>
  </section>
  <p id="sources-line" class="sources-line">记忆来源：—</p>

  <section class="card">
    <form id="search-form" class="row">
      <input id="search-input" type="search" placeholder="搜索记忆，例如：部署、约束" autocomplete="off">
      <select id="search-type">…类型选项…</select>
      <label class="check"><input id="search-stale" type="checkbox"> 包含过时</label>
      <button id="search-btn" class="btn primary" type="submit">搜索</button>
    </form>
    <p id="search-status" class="muted">未选择项目时只搜索用户级记忆。</p>
    <ul id="search-results" class="list"></ul>
  </section>

  <div class="grid">
    <section class="card">
      <h2>任务卡</h2>
      <p id="task-status" class="muted">选择具体项目后显示任务卡。</p>
      <ul id="task-list" class="list"></ul>
    </section>
    <section class="card">
      <h2>新增记忆</h2>
      <form id="add-form">
        <textarea id="add-text" rows="5" placeholder="写一条可复用的偏好、约束或事实…"></textarea>
        <div class="row">
          <select id="add-type">…</select>
          <select id="add-scope">…</select>
          <button id="add-btn" class="btn primary" type="submit">保存</button>
        </div>
      </form>
      <p id="add-hint" class="muted">用户级记忆对所有项目生效。</p>
    </section>
  </div>

  <section class="card">
    <h2>交接简报</h2>
    <div class="row">
      <button id="handoff-preview" class="btn ghost">预览</button>
      <button id="handoff-write" class="btn primary">生成交接简报</button>
      <span id="handoff-meta" class="muted">选择具体项目后可预览简报。</span>
    </div>
    <pre id="handoff-body" class="preview">无</pre>
  </section>

  <footer class="foot">
    <details><summary>文件位置</summary><p id="footer-target" class="mono">未选择项目</p></details>
  </footer>
</main>

<div id="drawer-backdrop" class="backdrop" hidden></div>
<aside id="drawer" class="drawer" hidden role="dialog" aria-modal="true" aria-labelledby="drawer-title">
  <header><h2 id="drawer-title">明细</h2><button id="drawer-close" class="btn ghost" aria-label="关闭">关闭</button></header>
  <div id="drawer-filters" class="row"></div>
  <div id="drawer-body" class="drawer-body"></div>
</aside>

<div id="toasts" class="toasts"></div>
<script>/* 见下方 JS 规则 */</script>
</body>
</html>
```

**CSS 规则（写入 `<style>`）**

```css
:root{
  --bg:#fafafa; --card:#fff; --text:#18181b; --muted:#6b7280; --faint:#a1a1aa;
  --line:#e4e4e7; --accent:#4f46e5; --accent-weak:#eef2ff;
  --danger:#dc2626; --ok:#16a34a; --radius:12px;
  --shadow:0 1px 2px rgba(0,0,0,.04),0 6px 20px rgba(0,0,0,.04);
}
@media (prefers-color-scheme: dark){
  :root{--bg:#111113;--card:#18181b;--text:#f4f4f5;--muted:#a1a1aa;--faint:#71717a;--line:#27272a;--accent:#818cf8;--accent-weak:#1e1b4b;--shadow:none}
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);font:15px/1.55 system-ui,-apple-system,"Segoe UI","Microsoft YaHei",sans-serif}
main{max-width:1040px;margin:0 auto;padding:20px 20px 48px;display:grid;gap:20px}
.topbar{position:sticky;top:0;z-index:20;display:flex;justify-content:space-between;align-items:center;gap:12px;
  padding:12px 20px;backdrop-filter:blur(8px);background:color-mix(in srgb,var(--bg) 82%,transparent);border-bottom:1px solid var(--line)}
.brand{font-weight:600;display:flex;align-items:center;gap:8px}
.actions{display:flex;gap:8px;align-items:center}
select,input,textarea,button{font:inherit;color:inherit}
select,input,textarea{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:8px 10px}
select:focus-visible,input:focus-visible,textarea:focus-visible,button:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.btn{border:1px solid var(--line);background:var(--card);border-radius:10px;padding:8px 14px;cursor:pointer;
  transition:transform .16s ease,box-shadow .16s ease,background .16s ease}
.btn:hover{transform:translateY(-1px);box-shadow:var(--shadow)}
.btn:active{transform:scale(.98)}
.btn.primary{background:var(--accent);border-color:var(--accent);color:#fff}
.btn.ghost{background:transparent}
.btn[disabled]{opacity:.55;cursor:not-allowed;transform:none;box-shadow:none}
.stat-band{display:grid;grid-template-columns:repeat(5,1fr);background:var(--card);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);overflow:hidden}
.stat{appearance:none;background:none;border:0;border-right:1px solid var(--line);padding:16px 18px;text-align:left;cursor:pointer;
  display:flex;flex-direction:column;gap:4px;transition:background .16s ease}
.stat:last-child{border-right:0}
.stat:hover{background:var(--accent-weak)}
.stat .num{font-size:26px;font-weight:600;font-variant-numeric:tabular-nums}
.stat .label{color:var(--muted);font-size:13px}
.sources-line{color:var(--muted);font-size:13px;margin:-8px 2px 0}
.card{background:var(--card);border:1px solid var(--line);border-radius:var(--radius);box-shadow:var(--shadow);padding:18px}
.card h2{font-size:15px;margin:0 0 12px}
.row{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.grid{display:grid;grid-template-columns:1fr 1fr;gap:20px}
@media (max-width:880px){.grid{grid-template-columns:1fr}.stat-band{grid-template-columns:1fr 1fr}.stat:nth-child(2n){border-right:0}}
.list{list-style:none;margin:12px 0 0;padding:0;display:grid;gap:10px}
.muted{color:var(--muted);font-size:13px}
.mono{font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px;word-break:break-all}
.preview{white-space:pre-wrap;background:var(--bg);border:1px solid var(--line);border-radius:10px;padding:14px;max-height:420px;overflow:auto;font-size:13px}
.backdrop{position:fixed;inset:0;background:rgba(0,0,0,.35);opacity:0;transition:opacity .15s ease}
.backdrop.open{opacity:1}
.drawer{position:fixed;top:0;right:0;height:100%;width:min(560px,92vw);background:var(--card);border-left:1px solid var(--line);
  transform:translateX(8px);opacity:0;transition:transform .2s cubic-bezier(.2,0,0,1),opacity .2s;display:flex;flex-direction:column;z-index:40}
.drawer.open{transform:none;opacity:1}
.drawer header{display:flex;justify-content:space-between;align-items:center;padding:16px 18px;border-bottom:1px solid var(--line)}
.drawer-body{padding:14px 18px;overflow:auto}
.toasts{position:fixed;right:16px;bottom:16px;display:grid;gap:8px;z-index:60}
.toast{background:var(--card);border:1px solid var(--line);border-left:3px solid var(--accent);border-radius:10px;box-shadow:var(--shadow);
  padding:10px 12px;max-width:360px;font-size:13px;animation:toast-in .2s ease-out}
.toast.error{border-left-color:var(--danger)}
@keyframes toast-in{from{transform:translateY(8px);opacity:0}to{transform:none;opacity:1}}
.fade-in{animation:fade-in .22s ease-out both}
@keyframes fade-in{from{transform:translateY(8px);opacity:0}to{transform:none;opacity:1}}
@media (prefers-reduced-motion: reduce){
  *{animation:none!important;transition:none!important}
  .drawer{transform:none}
}
```

**JS 规则（写入 `<script>`）**

1. **token**：沿用现有逻辑（从 `?token=` 取、存 `sessionStorage`、`history.replaceState` 抹除），key 改为 `agent-memory-token`。
2. **api(path, opts)**：带 `Authorization: Bearer`；非 2xx 抛错（401 给「访问令牌无效」文案）。
3. **状态与加载**：
   - `loadProjects()` 调 `/v1/projects`，填充下拉；每个 option 文本 = 项目名，`title` = 路径 + 记忆数；无任务卡的项目在文本后加「（仅记忆）」（用 `project.memory_count` 与 `/v1/task` 结果对比太贵，简化为：缓存任务卡查询结果，无则加后缀）。
   - **自动选项目**：`localStorage['am-project']` 有效则用之；否则选 `last_memory_at` 最大者；再否则选第一项；最后才 `''`。
   - 选中后并行 `loadStats()`、`loadTasks()`、`loadHandoff()`（预览，`GET /v1/handoff?format=json`）。
4. **自动刷新**：`setInterval` 30 秒调用 `refreshLive()`（stats + tasks；抽屉打开时跳过）；`window.addEventListener('focus', refreshLive)`；`sync` 完成后立即刷新。刷新失败时**保留上一次数据**，只在页内 toast 提示「刷新失败」，不清空列表与数字。
5. **数字动画**：`animateNum(el, to)`——从当前值到目标值 200ms，`requestAnimationFrame`；`prefers-reduced-motion` 时直接赋值。
6. **抽屉**：
   - `openDrawer(kind)`：设置 `hidden=false`，下一帧加 `open`；按 kind 渲染；记录触发元素；`document.body.style.overflow='hidden'`。
   - `closeDrawer()`：移除 `open`，`transitionend` 后 `hidden=true`；恢复焦点与滚动。
   - 支持 ESC、点遮罩、关闭按钮；`role=dialog` 已在 HTML 上。
   - 渲染：
     - projects → 列表（名称、路径、记忆数、最近活动），点击切换项目并关闭；
     - memories → 调 `/v1/memory?all=1&limit=200`（可加 `&stale=1`、`&type=`），每行显示类型、范围、文本、来源、最近时间；按钮「不再使用/恢复使用」调 `POST /v1/memory/stale`；
     - stale → 同上，固定 `stale=1`；
     - sources → `/v1/sources?limit=200`，每行 agent、kind、路径、最后扫描、大小、失败原因（红色）；
     - failed → `/v1/sources?failed=1`。
   - 抽屉内筛选行：记忆类给「全部/仅过时/仅新鲜」与类型下拉；源类给「全部/仅失败」。
7. **文案与结构**：
   - 按钮：「立即同步」「预览」「生成交接简报」「不再使用/恢复使用」；
   - 简报正文里不显示 token 数，改成「约 N 字」（按 `body.length` 估算，仅显示在 meta）；
   - 项目标识、给 agent 的提示放在 `details` 里默认折叠；
   - `sources-line`：`记忆来源：Qoder · Trae · WorkBuddy`（按 `stats.memories_by_agent` 的固定顺序拼，缺失则跳过）。
8. **空状态与骨架**：首次加载在列表容器放 3 条 `.skeleton`（高度 44px、`background:linear-gradient` 动画，reduced-motion 时静态）；空结果显示一句说明 + 如果是无数据则给「立即同步」按钮。
9. **安全**：不使用 `innerHTML`；所有文本走 `textContent`；URL 参数用 `encodeURIComponent`。

- [ ] **Step 4: 跑页面契约测试**

Run: `go test ./internal/daemon/ -run TestIndexPage_Contract -v`
Expected: PASS

- [ ] **Step 5: 跑全量测试并构建**

Run: `go test ./... -count=1 && go build -o agent-memory.exe ./cmd/agent-memory`
Expected: PASS + 构建成功

- [ ] **Step 6: 提交**

```bash
git add internal/daemon/index.html internal/daemon/index_test.go
git commit -m "feat: 重写桌面页面（统计带下钻、白话文案、自动刷新与动效）"
```

---

## Task 5: 浏览器实测与文档

- [ ] **Step 1: 启动服务并打开页面**

```bash
./agent-memory.exe serve --port 47123
# 浏览器打开 http://127.0.0.1:47123/?token=$(cat ~/.agent-memory/token)
```

- [ ] **Step 2: 逐项验收（把结果记下来）**

1. 打开 3 秒内自动选中项目，任务卡与简报有内容；
2. 点五个数字各自打开抽屉，数字与明细条数一致；
3. 抽屉：ESC 关闭、点遮罩关闭、关闭按钮关闭；焦点回到卡片；
4. 「不再使用」后该条从默认列表消失，勾选「包含过时」可再看到并「恢复使用」；
5. 「立即同步」完成后数字与任务卡自动更新，无整页闪烁；
6. 窄屏（DevTools 880px 以下）单列、无横向滚动；
7. 系统深色模式下颜色正确；
8. 开启「减少动态效果」（DevTools Rendering → Emulate CSS prefers-reduced-motion）后无位移动画。

- [ ] **Step 3: 截图四张**（浅色/深色 × 宽屏/窄屏）保存到 `docs/superpowers/specs/assets/desktop-ux-1/`，文件名 `light-wide.png`、`dark-wide.png`、`light-narrow.png`、`dark-narrow.png`。

- [ ] **Step 4: 更新 `docs/usage.md`**

在「10. Web UI」一节把功能描述换成新页面：统计带可下钻、四个抽屉、白话按钮、自动刷新；并补一句「记忆来源一行显示当前库中有哪些 agent 的数据」。

- [ ] **Step 5: 提交**

```bash
git add docs/usage.md docs/superpowers/specs/assets/desktop-ux-1
git commit -m "docs: 更新 Web UI 说明并附改造后截图"
```

---

## 完成标准（阶段一）

1. `go test ./... -count=1`、`go vet ./...`、`go build ./...` 全绿。
2. `TestIndexPage_Contract` 通过：五个 `data-drawer`、抽屉元素、白话文案齐全，旧术语与 `innerHTML` 不再出现。
3. 五个数字的明细条数与数字一致；空数据有说明。
4. 页面重做后：自动选中项目、30 秒自动刷新、动效总时长 <300ms、减少动效时无位移。
5. 四张截图已提交，`docs/usage.md` 与实现一致。
