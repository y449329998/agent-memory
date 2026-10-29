# 项目控制台与记忆编辑实施计划

> **For agentic workers:** 本计划按任务拆分，每个任务包含失败的测试、最小实现、验证命令与提交。**本项目禁止调用子 agent**（用户硬性规则），只能使用 inline execution（`superpowers:executing-plans`），每个 Task 完成后停下来汇报。步骤用 `- [ ]` 跟踪。

**Goal:** 把单页改成「首页项目卡片 → `#/p/<key>` 项目控制台」，分区展示项目概况、项目记忆、用户级记忆、任务卡、交接简报、来源与扫描、最近会话；记忆可点开查看与编辑正文/类型。

**Architecture:** 复用现有 daemon 与单文件页面。store 增加 `UpdateMemory`、`ListProjectSources`、列表 `offset`、项目 `task_count`；daemon 增加更新/会话/来源过滤接口；`index.html` 整体重写为哈希路由的两个视图 + 共用记忆列表组件 + 编辑抽屉，仍为单文件、零外部依赖、全部动态内容走 `textContent`。

**Tech Stack:** Go 1.22.6、`modernc.org/sqlite`、标准库 `net/http`、原生 HTML/CSS/JS（无框架、无 CDN）。

**上游 spec:** `docs/superpowers/specs/2026-10-29-project-console-design.md`

---

## 开工约定

- 从 `master` 新建分支 `feat/project-console`；不开 worktree。
- **不要调用子 agent**：检索、实现、验证全部由主 agent 完成。
- Windows + `core.autocrlf=true`：不要用 `gofmt -l .` 判断格式；用 `git show :<path> | gofmt -d`。
- 前端仍是 `internal/daemon/index.html` 单文件，通过 `//go:embed` 提供；不新增 JS/CSS 文件、不引入依赖。
- 所有动态文本必须用 `textContent` 或 `createElement` + `textContent`；页面里不允许出现 `innerHTML`。
- 重写页面时保留现有脚本顶部的通用件：token 解析与清理、`api()`、`el()`、`clear()`、`toast()`、`busy()`、`fmtTime()`、`typeBadge()`、`statusBadge()`、`renderTask()`、`loadHealth()`、`setStats()/loadStats()`、统计下钻抽屉、开机自启设置。只替换页面结构与项目/记忆相关逻辑。
- 不改数据库结构；本计划只新增查询/更新方法与接口。
- 提交信息用中文；每个 Task 结束提交一次。
- 测试统一用 `go test <pkg> -count=1`，不要依赖缓存。
- 验收前如需重启桌面应用：先托盘「停止服务并退出」或 `taskkill //F //IM agent-memory-app.exe`，再 `go build -ldflags "-H windowsgui" -o agent-memory-app.exe ./cmd/agent-memory-app` 并启动。

---

## 文件结构

```
internal/domain/errors.go          + ErrInvalidMemoryType
internal/store/memory.go           + UpdateMemory；MemoryFilter.Offset
internal/store/memory_update_test.go（新建）
internal/store/memory_list_test.go（新建）
internal/store/source.go           + ListProjectSources
internal/store/source_project_test.go（新建）
internal/store/project.go          + ProjectSummary.TaskCount
internal/store/project_summary_test.go（新建）
internal/daemon/server.go          + 路由 /v1/memory/update、/v1/sessions；/v1/memory offset；/v1/projects task_count
internal/daemon/web.go             + handleUpdateMemory、handleListSessions；handleListSources 项目过滤 + failed
internal/daemon/memory_update_test.go（新建）
internal/daemon/sources_sessions_test.go（新建）
internal/daemon/index.html         整体重写
internal/daemon/index_test.go      契约断言重写
docs/usage.md                      Web UI 章节更新
```

---

## Task 1: 记忆更新（store + domain）

**Files:**
- Modify: `internal/domain/errors.go`
- Modify: `internal/store/memory.go`
- Create: `internal/store/memory_update_test.go`

- [ ] **Step 1: 写失败的 store 测试**

```go
package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestUpdateMemory_TextTypeAndFTS(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/upd", "upd", "", `F:\upd`, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "旧文本 部署脚本",
		SourceAgent: "qoder", SourcePath: "/a.md",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	merged, err := s.UpdateMemory(ctx, id, "新文本 发布流程", domain.TypeConstraint, now.Add(time.Minute))
	if err != nil || merged != 0 {
		t.Fatalf("merged=%d err=%v", merged, err)
	}
	m, err := s.MemoryByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "新文本 发布流程" || m.Type != domain.TypeConstraint {
		t.Fatalf("text=%q type=%q", m.Text, m.Type)
	}
	hits, err := NewFTS(s).Search(ctx, domain.SearchQuery{Text: "发布流程", ProjectID: pid, Scope: domain.ScopeProject, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Memory.ID != id {
		t.Fatalf("FTS after update = %+v", hits)
	}
}

func TestUpdateMemory_MergesIntoSameContent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/merge", "merge", "", `F:\merge`, now)
	a, _, _ := s.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "内容甲", SourceAgent: "qoder", SourcePath: "/a.md"}, now)
	b, _, _ := s.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "内容乙", SourceAgent: "trae", SourcePath: "/b.md"}, now)

	merged, err := s.UpdateMemory(ctx, a, "内容乙", domain.TypeFact, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if merged != b {
		t.Fatalf("merged = %d, want %d", merged, b)
	}
	old, _ := s.MemoryByID(ctx, a)
	if !old.Stale {
		t.Fatal("edited duplicate should be marked stale")
	}
	target, _ := s.MemoryByID(ctx, b)
	if len(target.Sources) != 2 {
		t.Fatalf("target sources = %d, want 2", len(target.Sources))
	}
}

func TestUpdateMemory_RejectsInvalidInput(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/bad", "bad", "", `F:\bad`, now)
	id, _, _ := s.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "合法内容"}, now)

	if _, err := s.UpdateMemory(ctx, id, "   ", domain.TypeFact, now); !errors.Is(err, domain.ErrEmptyMemory) {
		t.Fatalf("empty text err = %v", err)
	}
	if _, err := s.UpdateMemory(ctx, id, "内容", domain.MemoryType("bogus"), now); !errors.Is(err, domain.ErrInvalidMemoryType) {
		t.Fatalf("invalid type err = %v", err)
	}
	if _, err := s.UpdateMemory(ctx, 99999, "内容", domain.TypeFact, now); !errors.Is(err, domain.ErrMemoryNotFound) {
		t.Fatalf("missing id err = %v", err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run TestUpdateMemory -v`
Expected: FAIL（`UpdateMemory`、`ErrInvalidMemoryType` 未定义）

- [ ] **Step 3: 加 domain 错误**

`internal/domain/errors.go` 追加：

```go
// ErrInvalidMemoryType 拒绝不在枚举内的记忆类型。
ErrInvalidMemoryType = errors.New("invalid memory type")
```

- [ ] **Step 4: 实现 UpdateMemory**

`internal/store/memory.go` 追加（放在 `UpsertMemory` 之后）：

```go
// UpdateMemory 编辑已有条目的正文与类型。若改后与同范围/项目/类型下的
// 另一条内容完全相同，则把来源并入那条、把本条标过时，并返回被合并的 id；
// 否则原地更新并重建 FTS。范围、所属项目、首次发现时间和过时状态都不变。
func (s *Store) UpdateMemory(ctx context.Context, id int64, text string, typ domain.MemoryType, at time.Time) (int64, error) {
	old, err := s.MemoryByID(ctx, id)
	if err != nil {
		return 0, err
	}
	canon := normalize.CanonicalText(text)
	if canon == "" {
		return 0, domain.ErrEmptyMemory
	}
	display := normalize.DisplayText(text)
	hash := normalize.Hash(canon)
	if typ == "" {
		typ = old.Type
	}
	if !domain.ValidMemoryType(typ) {
		return 0, domain.ErrInvalidMemoryType
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var other int64
	err = tx.QueryRowContext(ctx, `
SELECT id FROM memory_item
WHERE scope = ? AND project_id = ? AND type = ? AND text_norm_hash = ? AND id <> ?
LIMIT 1`, old.Scope, old.ProjectID, typ, hash, id).Scan(&other)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_source(memory_id, agent, path, quote)
SELECT ?, agent, path, quote FROM memory_source WHERE memory_id = ?`, other, id); err != nil {
			return 0, fmt.Errorf("merge memory sources: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE memory_item SET stale = 1, last_seen_at = ? WHERE id = ?`, at.Unix(), id); err != nil {
			return 0, fmt.Errorf("stale merged memory: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return other, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE memory_item SET type = ?, text = ?, text_norm_hash = ?, last_seen_at = ? WHERE id = ?`,
		typ, display, hash, at.Unix(), id); err != nil {
		return 0, fmt.Errorf("update memory: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE rowid = ?`, id); err != nil {
		return 0, fmt.Errorf("reindex memory %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts(rowid, text_seg) VALUES(?, ?)`,
		id, strings.Join(normalize.Segment(canon), " ")); err != nil {
		return 0, fmt.Errorf("reindex memory %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return 0, nil
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/store/ -run TestUpdateMemory -v`
Expected: PASS（3 个用例）

- [ ] **Step 6: 提交**

```bash
git add internal/domain/errors.go internal/store/memory.go internal/store/memory_update_test.go
git commit -m "feat: 记忆正文与类型可编辑，冲突时合并来源"
```

---

## Task 2: 记忆列表 offset

**Files:**
- Modify: `internal/store/memory.go`
- Create: `internal/store/memory_list_test.go`
- Modify: `internal/daemon/server.go`（`handleListMemory` 解析 offset）

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

func TestListMemoriesFiltered_Offset(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/off", "off", "", `F:\off`, base)
	for i, text := range []string{"第一条", "第二条", "第三条"} {
		if _, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: domain.ScopeProject, Type: domain.TypeFact, Text: text,
		}, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListMemoriesFiltered(ctx, MemoryFilter{ProjectID: pid, Scope: domain.ScopeProject, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "第二条" {
		t.Fatalf("offset=1 got %+v", got)
	}
	total, err := s.CountMemoriesFiltered(ctx, MemoryFilter{ProjectID: pid, Scope: domain.ScopeProject})
	if err != nil || total != 3 {
		t.Fatalf("count = %d err = %v; offset must not affect count", total, err)
	}
	empty, err := s.ListMemoriesFiltered(ctx, MemoryFilter{ProjectID: pid, Scope: domain.ScopeProject, Limit: 1, Offset: 10})
	if err != nil || len(empty) != 0 {
		t.Fatalf("offset beyond total = %+v err=%v", empty, err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run TestListMemoriesFiltered_Offset -v`
Expected: FAIL（`MemoryFilter` 没有 `Offset`）

- [ ] **Step 3: 实现 Offset**

`MemoryFilter` 加字段：

```go
type MemoryFilter struct {
	ProjectID   int64
	AllProjects bool
	Scope       domain.Scope
	Type        domain.MemoryType
	Stale       *bool
	Limit       int
	Offset      int
}
```

`ListMemoriesFiltered` 里把 limit 校验后追加：

```go
	if f.Offset < 0 {
		f.Offset = 0
	}
	q += ` ORDER BY last_seen_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)
```

（替换原来的 `q += ... LIMIT ?` / `args = append(args, f.Limit)` 两行；计数逻辑不动。）

- [ ] **Step 4: 跑 store 测试**

Run: `go test ./internal/store/ -run TestListMemoriesFiltered_Offset -v`
Expected: PASS

- [ ] **Step 5: 接口解析 offset**

`handleListMemory` 在 limit 解析之后加：

```go
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Offset = n
		}
	}
```

- [ ] **Step 6: 跑全量测试并提交**

Run: `go test ./internal/store/ ./internal/daemon/ -count=1`
Expected: PASS

```bash
git add internal/store/memory.go internal/store/memory_list_test.go internal/daemon/server.go
git commit -m "feat: 记忆列表支持 offset 分页"
```

---

## Task 3: 项目来源查询

**Files:**
- Modify: `internal/store/source.go`
- Create: `internal/store/source_project_test.go`

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

func TestListProjectSources_OnlyContributors(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pa, _ := s.EnsureProject(ctx, "f:/pa", "pa", "", `F:\pa`, now)
	pb, _ := s.EnsureProject(ctx, "f:/pb", "pb", "", `F:\pb`, now)

	if _, err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/a.md", ContentHash: "ha"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/b.md", ContentHash: "hb"}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/orphan.md", ContentHash: "ho"}, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "qoder", "memory", "/a.md", "parse failed", 4); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, pa, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "A 的记忆", SourceAgent: "qoder", SourcePath: "/a.md"}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, pb, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "B 的记忆", SourceAgent: "trae", SourcePath: "/b.md"}, now); err != nil {
		t.Fatal(err)
	}

	rows, total, failed, err := s.ListProjectSources(ctx, pa, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != "/a.md" || total != 1 || failed != 1 {
		t.Fatalf("rows=%+v total=%d failed=%d", rows, total, failed)
	}
	if rows[0].LastError != "parse failed" {
		t.Fatalf("last error = %q", rows[0].LastError)
	}

	rows, total, failed, err = s.ListProjectSources(ctx, pb, 50)
	if err != nil || len(rows) != 1 || rows[0].Path != "/b.md" || total != 1 || failed != 0 {
		t.Fatalf("pb rows=%+v total=%d failed=%d err=%v", rows, total, failed, err)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run TestListProjectSources -v`
Expected: FAIL（方法未定义）

- [ ] **Step 3: 实现查询**

`internal/store/source.go` 追加：

```go
// ListProjectSources 列出给该项目贡献过记忆的源文件（含已过时记忆贡献的源），
// 按最后扫描倒序。total 是去重后的源数，failed 是其中解析失败的数量。
func (s *Store) ListProjectSources(ctx context.Context, projectID int64, limit int) ([]SourceRow, int, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const from = `
FROM source s
JOIN memory_source ms ON ms.agent = s.agent AND ms.path = s.path
JOIN memory_item m ON m.id = ms.memory_id
WHERE m.project_id = ?`

	var total, failed int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT s.id)`+from, projectID).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT s.id)`+from+` AND s.last_error <> ''`, projectID).Scan(&failed); err != nil {
		return nil, 0, 0, err
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT s.agent, s.kind, s.path, s.content_hash, s.last_error, s.last_seen_run, s.updated_at, s.changed_at, s.size`+from+`
ORDER BY s.last_seen_run DESC, s.updated_at DESC, s.agent, s.path
LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	var out []SourceRow
	for rows.Next() {
		var (
			r        SourceRow
			updated  int64
			changed  int64
		)
		if err := rows.Scan(&r.Agent, &r.Kind, &r.Path, &r.ContentHash, &r.LastError,
			&r.LastSeenRun, &updated, &changed, &r.Size); err != nil {
			return nil, 0, 0, err
		}
		r.UpdatedAt = time.Unix(updated, 0)
		if changed > 0 {
			r.ChangedAt = time.Unix(changed, 0)
		}
		out = append(out, r)
	}
	return out, total, failed, rows.Err()
}
```

- [ ] **Step 4: 跑测试并提交**

Run: `go test ./internal/store/ -run TestListProjectSources -v`
Expected: PASS

```bash
git add internal/store/source.go internal/store/source_project_test.go
git commit -m "feat: 按项目查询贡献过记忆的来源文件"
```

---

## Task 4: 项目 task_count

**Files:**
- Modify: `internal/store/project.go`
- Create: `internal/store/project_summary_test.go`
- Modify: `internal/daemon/server.go`（`projectSummary` + JSON）

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

func TestListProjectSummaries_TaskCount(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/tasks", "tasks", "", `F:\tasks`, now)
	sid, err := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "s1", ProjectID: pid, Title: "会话", UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTaskCard(ctx, domain.TaskCard{SessionID: sid, ProjectID: pid, Goal: "目标", Status: domain.StatusDoing, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListProjectSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TaskCount != 1 {
		t.Fatalf("summaries = %+v", list)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/store/ -run TestListProjectSummaries_TaskCount -v`
Expected: FAIL（`TaskCount` 未定义）

- [ ] **Step 3: 实现 task_count**

`ProjectSummary` 加 `TaskCount int`；`ListProjectSummaries` 的 SQL 改成：

```sql
SELECT p.id, p.key, p.display_name, p.primary_path,
       COUNT(m.id),
       SUM(CASE WHEN m.stale = 1 THEN 1 ELSE 0 END),
       COALESCE(MAX(m.last_seen_at), 0),
       (SELECT COUNT(*) FROM task_card t WHERE t.project_id = p.id)
FROM project p
LEFT JOIN memory_item m ON m.project_id = p.id AND m.scope = 'project'
GROUP BY p.id
ORDER BY p.key
```

Scan 增加 `&ps.TaskCount`（放在 `lastSeen` 之后）。

- [ ] **Step 4: 跑 store 测试**

Run: `go test ./internal/store/ -run TestListProjectSummaries -v`
Expected: PASS

- [ ] **Step 5: 接口输出 task_count**

`projectSummary` 结构加 `TaskCount int \`json:"task_count"\``；`handleListProjects` 赋值 `TaskCount: p.TaskCount`。

- [ ] **Step 6: 跑测试并提交**

Run: `go test ./internal/store/ ./internal/daemon/ -count=1`
Expected: PASS

```bash
git add internal/store/project.go internal/store/project_summary_test.go internal/daemon/server.go
git commit -m "feat: 项目列表增加任务卡数量"
```

---

## Task 5: 更新接口

**Files:**
- Modify: `internal/daemon/server.go`
- Create: `internal/daemon/memory_update_test.go`

- [ ] **Step 1: 写失败的服务端测试**

```go
package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestUpdateMemoryEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := st.EnsureProject(ctx, "f:/api-upd", "api-upd", "", `F:\api-upd`, now)
	id, _, _ := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "原内容",
		SourceAgent: "qoder", SourcePath: "/a.md",
	}, now)

	// 跨站 Origin 必须拒绝；同源（Host 与 Origin 端口一致）放行。
	rec := webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": id, "text": "新内容"}, 
		func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: code = %d, want 403", rec.Code)
	}

	sameOrigin := func(r *http.Request) {
		r.Host = "127.0.0.1:47123"
		r.Header.Set("Origin", "http://127.0.0.1:47123")
	}
	rec = webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": id, "text": "新内容", "type": "constraint"}, sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: code = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID         int64 `json:"id"`
		MergedInto int64 `json:"merged_into"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != id || out.MergedInto != 0 {
		t.Fatalf("response = %+v", out)
	}
	m, _ := st.MemoryByID(ctx, id)
	if m.Text != "新内容" || m.Type != domain.TypeConstraint {
		t.Fatalf("stored = %+v", m)
	}

	rec = webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": id, "text": "  "}, sameOrigin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty text: code = %d, want 400", rec.Code)
	}
	rec = webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": 99999, "text": "x"}, sameOrigin)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id: code = %d, want 404", rec.Code)
	}
}
```

先确认 `writeAuth` 对缺少 Origin 的返回码：读 `internal/daemon/server.go` 的 `writeAuth`；若实际是 403 按上面断言，若是 400 改成实际值再跑。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/daemon/ -run TestUpdateMemoryEndpoint -v`
Expected: FAIL（404/405，路由未注册）

- [ ] **Step 3: 实现 handler 与路由**

`internal/daemon/server.go` 追加：

```go
type updateMemoryRequest struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	Type string `json:"type"`
}

func (s *Server) handleUpdateMemory(w http.ResponseWriter, r *http.Request) {
	var req updateMemoryRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if req.ID <= 0 {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	merged, err := s.st.UpdateMemory(r.Context(), req.ID, req.Text, domain.MemoryType(req.Type), time.Now())
	switch {
	case errors.Is(err, domain.ErrMemoryNotFound):
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	case errors.Is(err, domain.ErrEmptyMemory), errors.Is(err, domain.ErrInvalidMemoryType):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "merged_into": merged})
}
```

路由注册（放在 `/v1/memory/stale` 旁边）：

```go
	s.mux.HandleFunc("POST /v1/memory/update", s.writeAuth(s.handleUpdateMemory))
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/daemon/ -run TestUpdateMemoryEndpoint -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/daemon/server.go internal/daemon/memory_update_test.go
git commit -m "feat: 新增记忆编辑接口"
```

---

## Task 6: 来源接口按项目过滤 + 失败计数

**Files:**
- Modify: `internal/daemon/web.go`（`handleListSources`）
- Create: `internal/daemon/sources_sessions_test.go`

- [ ] **Step 1: 写失败的服务端测试**

```go
package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestSourcesEndpoint_ProjectFilter(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := st.EnsureProject(ctx, "f:/src", "src", "", `F:\src`, now)
	if _, err := st.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/a.md", ContentHash: "ha"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/b.md", ContentHash: "hb"}, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "来源测试",
		SourceAgent: "qoder", SourcePath: "/a.md"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSourceError(ctx, "qoder", "memory", "/a.md", "boom", 3); err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodGet, "/v1/sources?project_key=f%3A%2Fsrc&limit=50", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Sources []struct {
			Path      string `json:"Path"`
			LastError string `json:"LastError"`
		} `json:"sources"`
		Total  int `json:"total"`
		Failed int `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 1 || out.Sources[0].Path != "/a.md" || out.Total != 1 || out.Failed != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out.Sources[0].LastError != "boom" {
		t.Fatalf("last error = %q", out.Sources[0].LastError)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/daemon/ -run TestSourcesEndpoint_ProjectFilter -v`
Expected: FAIL（返回全部源 / 没有 failed 字段）

- [ ] **Step 3: 实现项目分支**

`handleListSources` 改为：

```go
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	ref := r.URL.Query().Get("project_path")
	if ref == "" {
		ref = r.URL.Query().Get("project_key")
	}
	if ref != "" {
		id, err := s.projectIDFromRequest(r.Context(), ref, ref)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		rows, total, failed, err := s.st.ListProjectSources(r.Context(), id, limit)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sources": rows, "count": len(rows), "total": total, "failed": failed,
		})
		return
	}
	agent := r.URL.Query().Get("agent")
	failedOnly := r.URL.Query().Get("failed") == "1"
	total, err := s.st.CountSources(r.Context(), agent, failedOnly)
	if err != nil {
		httpError(w, err)
		return
	}
	failed, err := s.st.CountSources(r.Context(), agent, true)
	if err != nil {
		httpError(w, err)
		return
	}
	rows, err := s.st.ListSources(r.Context(), agent, failedOnly, limit)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": rows, "count": len(rows), "total": total, "failed": failed})
}
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/daemon/ -run TestSourcesEndpoint -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/daemon/web.go internal/daemon/sources_sessions_test.go
git commit -m "feat: 来源接口支持按项目过滤并返回失败数"
```

---

## Task 7: 会话接口

**Files:**
- Modify: `internal/daemon/server.go`
- Modify: `internal/daemon/sources_sessions_test.go`（追加）

- [ ] **Step 1: 写失败的服务端测试**

追加：

```go
func TestSessionsEndpoint_ProjectScope(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := st.EnsureProject(ctx, "f:/sess", "sess", "", `F:\sess`, now)
	if _, err := st.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "s1", ProjectID: pid, Title: "加单功能", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "s2", ProjectID: 0, Title: "别处", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodGet, "/v1/sessions?project_key=f%3A%2Fsess&limit=20", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Sessions []struct {
			Agent string `json:"agent"`
			Title string `json:"title"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sessions) != 1 || out.Sessions[0].Agent != "qoder" || out.Sessions[0].Title != "加单功能" {
		t.Fatalf("sessions = %+v", out.Sessions)
	}

	rec = webDo(t, s, http.MethodGet, "/v1/sessions", webToken, nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing project: code = %d, want 400", rec.Code)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/daemon/ -run TestSessionsEndpoint -v`
Expected: FAIL（404/405）

- [ ] **Step 3: 实现 handler 与路由**

```go
func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("project_path")
	if ref == "" {
		ref = r.URL.Query().Get("project_key")
	}
	if ref == "" {
		http.Error(w, "project_key is required", http.StatusBadRequest)
		return
	}
	id, err := s.projectIDFromRequest(r.Context(), ref, ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	sessions, err := s.st.ListSessions(r.Context(), id, limit)
	if err != nil {
		httpError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, map[string]any{
			"agent": sess.Agent, "external_id": sess.ExternalID, "title": sess.Title,
			"updated_at": sess.UpdatedAt.Format(time.RFC3339), "event_count": sess.EventCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out, "count": len(out)})
}
```

路由（放在 `/v1/sources` 旁）：

```go
	s.mux.HandleFunc("GET /v1/sessions", s.auth(s.handleListSessions))
```

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/daemon/ -run TestSessionsEndpoint -v`
Expected: PASS

- [ ] **Step 5: 跑全量并提交**

Run: `go test ./internal/store/ ./internal/daemon/ -count=1`
Expected: PASS

```bash
git add internal/daemon/server.go internal/daemon/sources_sessions_test.go
git commit -m "feat: 新增项目会话列表接口"
```

---

## Task 8: 页面骨架、路由与样式

**Files:**
- Rewrite: `internal/daemon/index.html`
- Rewrite: `internal/daemon/index_test.go`

- [ ] **Step 1: 重写页面契约测试**

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
		`id="home-view"`, `id="project-view"`, `id="project-grid"`,
		`id="home-user-list"`, `id="project-mem-list"`, `id="project-user-list"`,
		`id="edit-drawer"`, `id="memory-text"`, `id="memory-type"`,
		`id="memory-save"`, `id="memory-stale"`, `id="edit-close"`,
		`id="project-nav"`, `id="sec-sources"`, `id="sec-sessions"`, `id="source-list"`, `id="session-list"`,
		`id="task-list"`, `id="handoff-body"`, `id="stat-band"`,
		`id="drawer"`, `id="drawer-close"`, `id="toasts"`, `id="autostart"`,
		`#/p/`, `/v1/memory/update`, `offset`, `立即同步`, `生成交接简报`,
		`用户级记忆`, `包含过时`, `prefers-reduced-motion`,
		`localStorage`, `30000`, `focus`, `pointer-events:none`,
	}
	for _, want := range mustHave {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	mustNotHave := []string{
		`innerHTML`, `id="project-select"`, `写入 HANDOFF.md`, `标过时`,
		`复活<`, `得分 `, `读取概览`, `写入目标`, `tokens`,
	}
	for _, bad := range mustNotHave {
		if strings.Contains(page, bad) {
			t.Fatalf("page still contains legacy copy %q", bad)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/daemon/ -run TestIndexPage_Contract -v`
Expected: FAIL（旧页面没有 `home-view`，仍有 `project-select`）

- [ ] **Step 3: 写页面骨架（HTML）**

HTML 结构如下（完整替换 `<body>` 内容；`<head>` 的样式在下一步补）：

```html
<body>
<header class="topbar">
  <a class="brand" href="#/">agent-memory</a>
  <span class="brand-status"><span id="health-dot" class="dot"></span><span id="health-text" class="muted">检查中</span></span>
  <div class="actions">
    <button id="refresh-btn" class="btn ghost">刷新</button>
    <button id="sync-btn" class="btn primary">立即同步</button>
  </div>
</header>

<main id="app">
  <section id="home-view">
    <section id="stat-band" class="stat-band">
      <button class="stat" data-drawer="projects"><span id="stat-projects" class="num">0</span><span class="label">项目</span></button>
      <button class="stat" data-drawer="memories"><span id="stat-memories" class="num">0</span><span class="label">记忆</span></button>
      <button class="stat" data-drawer="stale"><span id="stat-stale" class="num">0</span><span class="label">过时</span></button>
      <button class="stat" data-drawer="sources"><span id="stat-sources" class="num">0</span><span class="label">源</span></button>
      <button class="stat" data-drawer="failed"><span id="stat-failed" class="num">0</span><span class="label">失败源</span></button>
    </section>
    <p id="sources-line" class="sources-line">记忆来源：—</p>

    <section class="card">
      <div class="section-head"><h2>项目</h2><span id="project-count" class="muted"></span></div>
      <div id="project-grid" class="project-grid"></div>
      <p id="project-empty" class="muted" hidden>还没有项目，扫描后会自动出现。</p>
    </section>

    <section class="card">
      <div class="section-head"><h2>用户级记忆</h2><span id="home-user-total" class="muted"></span></div>
      <div class="row filters">
        <input id="home-user-search" type="search" placeholder="在用户级记忆里搜索" autocomplete="off">
        <select id="home-user-type" aria-label="类型"></select>
        <label class="check"><input id="home-user-stale" type="checkbox"> 包含过时</label>
      </div>
      <ul id="home-user-list" class="list"></ul>
      <button id="home-user-more" class="btn ghost small" hidden>显示更多</button>
      <details class="add-box">
        <summary>新增用户级记忆</summary>
        <form id="home-add-form">
          <textarea id="home-add-text" rows="4" placeholder="写一条对所有项目生效的偏好、约束或事实…"></textarea>
          <div class="row"><select id="home-add-type" aria-label="类型"></select><button class="btn primary" type="submit">保存</button></div>
        </form>
      </details>
    </section>
  </section>

  <section id="project-view" hidden>
    <div class="project-head">
      <button id="back-btn" class="btn ghost">← 返回</button>
      <div class="project-title-box"><h1 id="project-title"></h1><p id="project-path" class="mono"></p></div>
    </div>
    <nav id="project-nav" class="anchor-nav" aria-label="项目页导航">
      <button data-scroll="sec-overview">概要</button>
      <button data-scroll="sec-project-memory">项目记忆</button>
      <button data-scroll="sec-user-memory">用户记忆</button>
      <button data-scroll="sec-tasks">任务</button>
      <button data-scroll="sec-handoff">简报</button>
      <button data-scroll="sec-sources">来源</button>
      <button data-scroll="sec-sessions">会话</button>
    </nav>

    <section id="sec-overview" class="card">
      <h2>项目概况</h2>
      <div class="overview-grid">
        <div><span class="label">路径</span><span id="ov-path" class="mono"></span></div>
        <div><span class="label">项目记忆</span><span id="ov-mem" class="num-sm">0</span></div>
        <div><span class="label">用户级记忆</span><span id="ov-user-mem" class="num-sm">0</span></div>
        <div><span class="label">任务卡</span><span id="ov-tasks" class="num-sm">0</span></div>
        <div><span class="label">最后活动</span><span id="ov-last"></span></div>
        <div><span class="label">来源</span><span id="ov-sources"></span></div>
        <div><span class="label">记忆来源</span><span id="ov-agents"></span></div>
      </div>
      <p id="ov-warning" class="notice" hidden></p>
    </section>

    <section id="sec-project-memory" class="card">
      <div class="section-head"><h2>项目记忆</h2><span id="project-mem-total" class="muted"></span>
        <button id="project-add-toggle" class="btn small primary">新增项目记忆</button></div>
      <div class="row filters">
        <input id="project-mem-search" type="search" placeholder="在项目记忆里搜索" autocomplete="off">
        <select id="project-mem-type" aria-label="类型"></select>
        <label class="check"><input id="project-mem-stale" type="checkbox"> 包含过时</label>
      </div>
      <form id="project-add-form" class="add-inline" hidden>
        <textarea id="project-add-text" rows="3" placeholder="写一条只属于这个项目的记忆…"></textarea>
        <div class="row"><select id="project-add-type" aria-label="类型"></select><button class="btn primary" type="submit">保存</button></div>
      </form>
      <ul id="project-mem-list" class="list"></ul>
      <button id="project-mem-more" class="btn ghost small" hidden>显示更多</button>
    </section>

    <section id="sec-user-memory" class="card">
      <div class="section-head"><h2>用户级记忆</h2><span class="muted">对所有项目生效</span><span id="project-user-total" class="muted"></span></div>
      <div class="row filters">
        <input id="project-user-search" type="search" placeholder="在用户级记忆里搜索" autocomplete="off">
        <select id="project-user-type" aria-label="类型"></select>
        <label class="check"><input id="project-user-stale" type="checkbox"> 包含过时</label>
      </div>
      <ul id="project-user-list" class="list"></ul>
      <button id="project-user-more" class="btn ghost small" hidden>显示更多</button>
    </section>

    <section id="sec-tasks" class="card"><h2>任务卡</h2><p id="task-status" class="muted"></p><ul id="task-list" class="list"></ul></section>

    <section id="sec-handoff" class="card">
      <h2>交接简报</h2>
      <div class="row"><button id="handoff-preview" class="btn ghost">预览</button><button id="handoff-write" class="btn primary">生成交接简报</button><span id="handoff-meta" class="muted"></span></div>
      <pre id="handoff-body" class="preview">无</pre>
    </section>

    <section id="sec-sources" class="card">
      <div class="section-head"><h2>来源与扫描</h2><span id="source-summary" class="muted"></span><button id="source-sync" class="btn small ghost">立即同步</button></div>
      <ul id="source-list" class="list"></ul>
    </section>

    <section id="sec-sessions" class="card"><h2>最近会话</h2><ul id="session-list" class="list"></ul><p id="session-empty" class="muted" hidden>目前只有 Qoder 会话会被导入；Trae / WorkBuddy 没有本地会话正文。</p></section>
  </section>
</main>

<div id="drawer-backdrop" class="backdrop" hidden></div>
<aside id="drawer" class="drawer" hidden role="dialog" aria-modal="true" aria-labelledby="drawer-title">
  <header><h2 id="drawer-title">明细</h2><button id="drawer-close" class="btn ghost">关闭</button></header>
  <div id="drawer-filters" class="filters"></div>
  <div id="drawer-body" class="drawer-body"></div>
</aside>

<div id="edit-backdrop" class="backdrop" hidden></div>
<aside id="edit-drawer" class="drawer" hidden role="dialog" aria-modal="true" aria-labelledby="edit-title">
  <header><h2 id="edit-title">编辑记忆</h2><button id="edit-close" class="btn ghost">关闭</button></header>
  <div class="drawer-body edit-body">
    <label class="field"><span>正文</span><textarea id="memory-text" rows="8"></textarea></label>
    <label class="field"><span>类型</span><select id="memory-type" aria-label="类型"></select></label>
    <p id="memory-meta" class="muted"></p>
    <p id="memory-warning" class="notice" hidden></p>
    <div class="row"><button id="memory-save" class="btn primary">保存</button><button id="memory-cancel" class="btn ghost">取消</button><button id="memory-stale" class="btn ghost">不再使用</button></div>
  </div>
</aside>

<div id="toasts" class="toasts" aria-live="polite"></div>
<footer class="foot">
  <details><summary>设置</summary>
    <label class="check"><input id="autostart" type="checkbox"> 开机自动运行</label>
    <p id="autostart-hint" class="muted"></p>
  </details>
</footer>
<script>/* 见下一步与后续任务 */</script>
</body>
```

- [ ] **Step 4: 写样式**

在现有 CSS 变量基础上追加（保留现有 `.stat-band/.drawer/.toast/.list/.item` 与 `pointer-events:none` 等规则；删除所有 `#project-select` 规则）：

```css
.project-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(250px,1fr));gap:12px}
.project-card{display:flex;flex-direction:column;gap:6px;text-align:left;padding:14px 16px;background:var(--card);
  border:1px solid var(--line);border-radius:var(--radius);cursor:pointer}
.project-card:hover{background:var(--accent-weak);transform:none;box-shadow:var(--shadow)}
.project-card .name{font-weight:600}
.project-card .path{color:var(--muted);font-size:12px;word-break:break-all}
.project-card .meta{display:flex;gap:8px;flex-wrap:wrap;color:var(--muted);font-size:12px}
.project-card.bad{border-color:var(--danger)}
.section-head{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin-bottom:10px}
.section-head h2{margin:0}
.filters{margin:0 0 8px}
.item-click{cursor:pointer}
.item-click:hover{border-color:var(--accent)}
.item-click:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.anchor-nav{position:sticky;top:56px;z-index:10;display:flex;gap:6px;overflow:auto;padding:6px 0;
  background:color-mix(in srgb,var(--bg) 88%,transparent);backdrop-filter:blur(6px)}
.anchor-nav button{border:0;background:none;color:var(--muted);padding:6px 10px;border-radius:8px;white-space:nowrap}
.anchor-nav button:hover{background:var(--accent-weak);color:var(--text);transform:none;box-shadow:none}
.project-head{display:flex;gap:12px;align-items:flex-start;margin:4px 0 8px}
.project-title-box h1{font-size:20px;margin:0}
.overview-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(160px,1fr));gap:12px}
.overview-grid .label{display:block;color:var(--muted);font-size:12px}
.overview-grid .num-sm{font-size:20px;font-weight:600}
.notice{border-left:3px solid var(--danger);padding:8px 10px;background:var(--card);color:var(--text);font-size:13px}
.add-box{margin-top:12px}
.add-box summary{cursor:pointer;color:var(--muted)}
.add-inline{margin:8px 0}
.edit-body .field{display:block;margin-bottom:12px}
.edit-body .field span{display:block;color:var(--muted);font-size:13px;margin-bottom:4px}
.edit-body textarea{width:100%}
.foot{max-width:1040px;margin:0 auto;padding:0 20px 32px;color:var(--muted);font-size:13px}
@media (max-width:880px){.project-head{flex-direction:column}.anchor-nav{top:0}}
```

- [ ] **Step 5: 实现路由与基础渲染**

脚本骨架（后续任务补全细节，但这一步必须能跑通首页/项目页切换）：

```js
var state = { token: '', projects: [], stats: null, view: 'home', project: null,
  userHome: null, projectMem: null, projectUser: null, edit: null, editOriginal: null };

function projectLabel(p) { return (p && (p.display_name || p.key)) || '—'; }
function findProject(key) { return state.projects.filter(function (p) { return p.key === key; })[0] || null; }

function route() {
  var hash = location.hash || '#/';
  var m = /^#\/p\/(.+)$/.exec(hash);
  if (m) {
    var key = decodeURIComponent(m[1]);
    var p = findProject(key);
    if (!p) {
      if (!state.projects.length) { return loadProjects().then(route); }
      toast('error', '项目不存在或已移除');
      location.hash = '#/';
      return;
    }
    showProject(p);
    return;
  }
  showHome();
}
function showHome() {
  state.view = 'home';
  $('home-view').hidden = false;
  $('project-view').hidden = true;
  window.scrollTo(0, 0);
  refreshHome();
}
function showProject(p) {
  state.view = 'project';
  state.project = p;
  $('home-view').hidden = true;
  $('project-view').hidden = false;
  $('project-title').textContent = projectLabel(p);
  $('project-path').textContent = p.primary_path || p.key;
  window.scrollTo(0, 0);
  renderOverview(p);
}
window.addEventListener('hashchange', route);
```

本步还需要两个最小实现，Task 10/11 会替换/扩展它们：

```js
function loadProjects() {
  return api('/v1/projects').then(function (d) { state.projects = d.projects || []; });
}
function renderOverview(p) {
  $('ov-path').textContent = p.primary_path || '（未登记路径）';
  $('ov-mem').textContent = p.memory_count || 0;
  $('ov-tasks').textContent = p.task_count || 0;
  $('ov-last').textContent = p.last_memory_at ? fmtTime(p.last_memory_at) : '—';
  $('ov-warning').hidden = p.path_exists !== false;
  if (p.path_exists === false) $('ov-warning').textContent = '项目路径不存在：项目级写入与生成简报已禁用。';
}
// Task 11 替换为完整版本
function refreshHome() {
  return Promise.all([loadStats().catch(function () {}), loadProjects()]);
}
```

同时做三件收尾：

1. **删除旧页面逻辑**：项目下拉（`project-select`）、`applySelection/updateAddHint/updateHandoffControls/updateFooter`、全局搜索表单（`search-form` 等）、旧的 `loadTasks/loadHandoff/refreshAll`；保留通用件（见开工约定）。
2. **重写 `bind()`**：只保留 `refresh-btn`（刷新当前视图）、`sync-btn`（同步后刷新当前视图）、`back-btn`、`#project-nav` 按钮、`edit-close` 与统计下钻的事件；其余表单事件在后续 Task 补。
3. **统计下钻项目点击**：`renderProjects()` 里把原来的 `project-select.value = p.key; applySelection();` 改成 `location.hash = '#/p/' + encodeURIComponent(p.key); closeDrawer();`。

`init()` 先写成：

```js
function init() {
  bind();
  bindAutostart();
  if (!state.token) { showNoToken(); return; }
  loadHealth();
  loadAutostart();
  loadProjects().then(route);
}
init();
```

- [ ] **Step 6: 跑契约测试**

Run: `go test ./internal/daemon/ -run TestIndexPage_Contract -v`
Expected: PASS

- [ ] **Step 7: 启动服务并确认两个视图可切换**

页面验证统一在桌面应用里做（页面是内嵌的，只有重建 exe 才会更新）：按 Task 12 Step 3 停止应用、重新构建并启动，然后确认：默认首页可见、地址加 `#/p/<项目key>` 能切到项目视图、返回按钮回到首页、控制台无报错。

- [ ] **Step 8: 提交**

```bash
git add internal/daemon/index.html internal/daemon/index_test.go
git commit -m "feat: 项目控制台页面骨架与哈希路由"
```

---

## Task 9: 记忆列表组件与编辑抽屉

**Files:**
- Modify: `internal/daemon/index.html`

- [ ] **Step 1: 实现类型下拉与记忆列表组件**

在脚本中加入：

```js
var TYPES = [
  ['preference', '偏好'], ['constraint', '约束'], ['convention', '约定'],
  ['lesson', '教训'], ['fact', '事实'], ['goal', '目标']
];
function fillTypeSelect(sel, withAll) {
  if (!sel) return;
  if (withAll) { var all = el('option', '', '全部类型'); all.value = ''; sel.appendChild(all); }
  TYPES.forEach(function (t) { var o = el('option', '', t[1]); o.value = t[0]; sel.appendChild(o); });
}
function normalizeHit(h) {
  return { ID: h.id, Scope: h.scope, Type: h.type, Text: h.text, Stale: h.stale,
    LastSeenAt: h.last_seen_at, Sources: h.sources || [] };
}
function memoryItem(m, onOpen) {
  var li = el('li', 'item item-click');
  li.tabIndex = 0;
  var head = el('div', 'item-head');
  head.appendChild(typeBadge(m.Type));
  head.appendChild(el('span', 'badge', m.Scope === 'user' ? '用户级' : '项目级'));
  if (m.Stale) head.appendChild(el('span', 'badge stale', '过时'));
  li.appendChild(head);
  var text = (m.Text || '').trim();
  li.appendChild(el('p', 'item-text', text.length > 180 ? text.slice(0, 180) + '…' : text));
  var meta = el('div', 'item-meta');
  var agents = (m.Sources || []).map(function (s) { return s.Agent; });
  meta.appendChild(el('span', '', '来源：' + (agents.join(' · ') || '—')));
  meta.appendChild(el('span', '', '最后变更：' + fmtTime(m.LastSeenAt)));
  li.appendChild(meta);
  function open() { onOpen(m); }
  li.addEventListener('click', open);
  li.addEventListener('keydown', function (e) { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); open(); } });
  return li;
}

function memoryView(cfg) {
  var st = { items: [], total: 0, offset: 0, query: '', loading: false };
  function projectKey() { return state.project ? state.project.key : ''; }
  function params() {
    var p = ['limit=50', 'offset=' + st.offset];
    if (cfg.scope === 'project') { p.push('scope=project', 'project_key=' + encodeURIComponent(projectKey())); }
    else { p.push('scope=user'); }
    if ($(cfg.type).value) p.push('type=' + encodeURIComponent($(cfg.type).value));
    if ($(cfg.stale).checked) p.push('stale=1');
    return p.join('&');
  }
  function render() {
    var list = $(cfg.list);
    clear(list);
    if (!st.items.length) { list.appendChild(el('li', 'muted', st.query ? '没有匹配的记忆。' : '还没有记忆。')); return; }
    st.items.forEach(function (m) { list.appendChild(memoryItem(m, openEdit)); });
    $(cfg.total).textContent = '共 ' + st.total + ' 条' + (st.items.length < st.total ? '，已显示 ' + st.items.length + ' 条' : '');
    $(cfg.more).hidden = st.query !== '' || st.items.length >= st.total;
  }
  function load() {
    if (st.loading) return Promise.resolve();
    st.loading = true;
    var p;
    if (st.query) {
      p = api('/v1/search', { method: 'POST', body: JSON.stringify({
        query: st.query, scope: cfg.scope,
        project_key: cfg.scope === 'project' ? projectKey() : '',
        type: $(cfg.type).value, include_stale: $(cfg.stale).checked, limit: 50
      }) }).then(function (d) {
        var items = (d.hits || []).map(normalizeHit);
        st.items = items; st.total = items.length; render();
      });
    } else {
      p = api('/v1/memory?' + params()).then(function (d) {
        var items = d.memories || [];
        st.items = st.offset === 0 ? items : st.items.concat(items);
        st.total = typeof d.total === 'number' ? d.total : st.items.length;
        render();
      });
    }
    return p.catch(function (e) {
      $(cfg.list).textContent = '';
      $(cfg.list).appendChild(el('li', 'muted', '加载失败：' + e.message));
      throw e;
    }).then(function () { st.loading = false; });
  }
  function reload(keepOffset) {
    if (!keepOffset) st.offset = 0;
    return load();
  }
  var timer = null;
  $(cfg.search).addEventListener('input', function () {
    clearTimeout(timer);
    timer = setTimeout(function () { st.query = $(cfg.search).value.trim(); reload(false); }, 300);
  });
  $(cfg.type).addEventListener('change', function () { reload(false); });
  $(cfg.stale).addEventListener('change', function () { reload(false); });
  $(cfg.more).addEventListener('click', function () { st.offset += 50; load(); });
  return { reload: reload };
}
```

- [ ] **Step 2: 实现编辑抽屉**

```js
function openEdit(m) {
  state.edit = m;
  state.editOriginal = { text: m.Text || '', type: m.Type || 'fact' };
  $('memory-text').value = m.Text || '';
  $('memory-type').value = m.Type || 'fact';
  var parts = [];
  parts.push(m.Scope === 'user' ? '用户级（对所有项目生效）' : ('项目级 · ' + (state.project ? projectLabel(state.project) : ''));
  if (m.Sources && m.Sources.length) {
    parts.push('来源：' + m.Sources.map(function (s) { return s.Agent + ' · ' + s.Path; }).join('；'));
  }
  parts.push('首次：' + fmtTime(m.FirstSeenAt) + ' · 最后变更：' + fmtTime(m.LastSeenAt));
  $('memory-meta').textContent = parts.join('\n');
  $('memory-warning').hidden = !(m.Sources && m.Sources.length);
  if (!(m.Sources && m.Sources.length)) $('memory-warning').textContent = '';
  else $('memory-warning').textContent = '这条记忆来自 agent 文件，源文件变化后编辑可能被覆盖。';
  $('memory-stale').textContent = m.Stale ? '恢复使用' : '不再使用';
  $('edit-drawer').hidden = false;
  $('edit-backdrop').hidden = false;
  requestAnimationFrame(function () {
    $('edit-drawer').classList.add('open');
    $('edit-backdrop').classList.add('open');
  });
  $('memory-text').focus();
}
function editDirty() {
  if (!state.edit) return false;
  return $('memory-text').value !== state.editOriginal.text || $('memory-type').value !== state.editOriginal.type;
}
function closeEdit(force) {
  if (!force && editDirty() && !confirm('放弃未保存的修改？')) return;
  $('edit-drawer').classList.remove('open');
  $('edit-backdrop').classList.remove('open');
  setTimeout(function () {
    if (state.edit) return;
    $('edit-drawer').hidden = true;
    $('edit-backdrop').hidden = true;
  }, 200);
  state.edit = null;
  state.editOriginal = null;
}
function refreshMemoryViews() {
  if (state.userHome) state.userHome.reload(false).catch(function () {});
  if (state.view === 'project') {
    if (state.projectMem) state.projectMem.reload(false).catch(function () {});
    if (state.projectUser) state.projectUser.reload(false).catch(function () {});
  }
  loadStats().catch(function () {});
}
function saveEdit() {
  if (!state.edit) return;
  var m = state.edit;
  busy($('memory-save'), '保存', function () {
    return api('/v1/memory/update', { method: 'POST', body: JSON.stringify({
      id: m.ID, text: $('memory-text').value, type: $('memory-type').value
    }) }).then(function (d) {
      closeEdit(true);
      toast('info', d.merged_into ? '内容与 #' + d.merged_into + ' 相同，已合并' : '已保存');
      refreshMemoryViews();
    });
  });
}
function toggleEditStale() {
  if (!state.edit) return;
  var m = state.edit, want = !m.Stale;
  busy($('memory-stale'), want ? '恢复' : '标记', function () {
    return api('/v1/memory/stale', { method: 'POST', body: JSON.stringify({ id: m.ID, stale: want }) })
      .then(function () {
        m.Stale = want;
        $('memory-stale').textContent = want ? '恢复使用' : '不再使用';
        toast('info', want ? '已恢复使用' : '已标记为不再使用');
        refreshMemoryViews();
      });
  });
}
```

绑定（放进 `bind()`）：

```js
  $('edit-close').addEventListener('click', function () { closeEdit(false); });
  $('memory-cancel').addEventListener('click', function () { closeEdit(false); });
  $('edit-backdrop').addEventListener('click', function () { closeEdit(false); });
  $('memory-save').addEventListener('click', saveEdit);
  $('memory-stale').addEventListener('click', toggleEditStale);
  document.addEventListener('keydown', function (e) {
    if (!state.edit) return;
    if (e.key === 'Escape') closeEdit(false);
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); saveEdit(); }
  });
```

- [ ] **Step 3: 首页接入用户级记忆**

放在 `init()` 里、`loadProjects().then(route)` 之前：

```js
  fillTypeSelect($('home-user-type'), true);
  state.userHome = memoryView({
    scope: 'user', list: 'home-user-list', total: 'home-user-total',
    search: 'home-user-search', type: 'home-user-type', stale: 'home-user-stale', more: 'home-user-more'
  });
  state.userHome.reload(true).catch(function (e) { toast('error', '读取用户级记忆失败：' + e.message); });
```

Task 11 再补新增表单与首页其余接线。重建桌面应用后确认：首页用户级记忆列表能出现、能点开抽屉、能保存。

- [ ] **Step 4: 跑契约测试与手动验证**

Run: `go test ./internal/daemon/ -run TestIndexPage_Contract -v`
Expected: PASS

用 Task 8 的临时服务打开页面，手动验证：点一条用户级记忆 → 抽屉显示正文/类型/来源 → 改正文 → 保存 → 提示“已保存” → 列表与搜索结果更新；改回原内容不应报错。

- [ ] **Step 5: 提交**

```bash
git add internal/daemon/index.html
git commit -m "feat: 记忆列表组件与编辑抽屉"
```

---

## Task 10: 项目页七个区块

**Files:**
- Modify: `internal/daemon/index.html`

- [ ] **Step 1: 概况与项目/用户记忆视图**

实现 `renderOverview(p)`：

```js
function renderOverview(p) {
  $('ov-path').textContent = p.primary_path || '（未登记路径）';
  $('ov-mem').textContent = p.memory_count || 0;
  $('ov-tasks').textContent = p.task_count || 0;
  $('ov-last').textContent = p.last_memory_at ? fmtTime(p.last_memory_at) : '—';
  $('ov-agents').textContent = '—';
  $('ov-sources').textContent = '—';
  $('ov-user-mem').textContent = '…';
  $('ov-warning').hidden = p.path_exists !== false;
  if (p.path_exists === false) $('ov-warning').textContent = '项目路径不存在：项目级写入与生成简报已禁用，请先移动或重新登记项目。';
  $('project-add-toggle').disabled = p.path_exists === false || !p.primary_path;
  $('handoff-write').disabled = p.path_exists === false || !p.primary_path;
  api('/v1/memory?scope=user&limit=1').then(function (d) { $('ov-user-mem').textContent = d.total || 0; }, function () { $('ov-user-mem').textContent = '—'; });
}
```

在 `showProject` 中创建两个记忆视图（只创建一次）：

```js
function ensureProjectViews() {
  if (!state.projectMem) {
    fillTypeSelect($('project-mem-type'), true);
    fillTypeSelect($('project-user-type'), true);
    state.projectMem = memoryView({ scope: 'project', list: 'project-mem-list', total: 'project-mem-total', search: 'project-mem-search', type: 'project-mem-type', stale: 'project-mem-stale', more: 'project-mem-more' });
    state.projectUser = memoryView({ scope: 'user', list: 'project-user-list', total: 'project-user-total', search: 'project-user-search', type: 'project-user-type', stale: 'project-user-stale', more: 'project-user-more' });
  }
}
```

`showProject` 末尾调用 `ensureProjectViews(); state.projectMem.reload(true); state.projectUser.reload(true);` 并加载任务/简报/来源/会话。

- [ ] **Step 2: 任务卡与简报**

把现有 `loadTasks`/`loadHandoff` 改成固定使用 `state.project`：

```js
function loadProjectTasks() {
  var list = $('task-list'), status = $('task-status');
  clear(list);
  status.textContent = '加载中…';
  return api('/v1/task?project_key=' + encodeURIComponent(state.project.key) + '&limit=10').then(function (d) {
    var cards = d.cards || [];
    clear(list);
    if (!cards.length) { status.textContent = '这个项目还没有任务卡。'; list.appendChild(el('li', 'muted', 'Qoder 的会话会提炼成任务卡；Trae / WorkBuddy 目前只同步记忆。')); return; }
    status.textContent = '共 ' + cards.length + ' 张';
    cards.forEach(function (c) { list.appendChild(renderTask(c)); });
  }, function (e) { status.textContent = '加载失败：' + e.message; throw e; });
}
```

`loadProjectHandoff()` 的完整实现：

```js
function loadProjectHandoff() {
  if (!state.project) { $('handoff-body').textContent = '无'; return Promise.resolve(); }
  $('handoff-meta').textContent = '加载中…';
  return api('/v1/handoff?project_key=' + encodeURIComponent(state.project.key) + '&format=json').then(function (d) {
    var body = d.body || '';
    $('handoff-body').textContent = body || '无';
    $('handoff-meta').textContent = '约 ' + body.length + ' 字' + (d.truncated ? ' · 已按预算精简' : '');
  }, function (e) {
    $('handoff-meta').textContent = '加载失败：' + e.message;
    throw e;
  });
}
```

`handoff-preview`/`handoff-write` 的事件里改用 `state.project`：

```js
  $('handoff-preview').addEventListener('click', function () {
    busy(this, '预览', function () { return loadProjectHandoff().then(function () { toast('info', '简报已更新'); }); });
  });
  $('handoff-write').addEventListener('click', function () {
    if (!state.project || !state.project.primary_path) { toast('error', '请先选择一个有路径的项目'); return; }
    busy(this, '生成', function () {
      return api('/v1/handoff', { method: 'POST', body: JSON.stringify({ project_path: state.project.primary_path, write: true }) })
        .then(function (d) { $('handoff-body').textContent = d.body || '无'; $('handoff-meta').textContent = '约 ' + ((d.body || '').length) + ' 字' + (d.changed ? ' · 已更新' : ' · 内容无变化'); toast('info', d.changed ? '已更新项目里的交接简报' : '内容无变化，未重写'); });
    });
  });
```

- [ ] **Step 3: 来源与扫描**

```js
function loadProjectSources() {
  var list = $('source-list');
  clear(list);
  list.appendChild(el('li', 'muted', '加载中…'));
  return api('/v1/sources?project_key=' + encodeURIComponent(state.project.key) + '&limit=100').then(function (d) {
    var rows = d.sources || [];
    clear(list);
    var latest = '';
    var agents = {};
    rows.forEach(function (r) {
      if (r.UpdatedAt > latest) latest = r.UpdatedAt;
      agents[r.Agent] = 1;
      var li = el('li', 'item');
      var head = el('div', 'item-head');
      head.appendChild(el('span', 'badge', r.Agent));
      head.appendChild(el('span', 'badge', r.Kind === 'session' ? '会话' : '记忆'));
      if (r.LastError) head.appendChild(el('span', 'badge blocked', '失败'));
      li.appendChild(head);
      li.appendChild(el('div', 'mono', r.Path || ''));
      li.appendChild(el('div', 'item-meta', '最后扫描：' + fmtTime(r.UpdatedAt) + ' · 大小：' + (r.Size || 0) + ' 字节'));
      if (r.LastError) li.appendChild(el('p', 'item-text', '原因：' + r.LastError));
      list.appendChild(li);
    });
    if (!rows.length) list.appendChild(el('li', 'muted', '还没有给这个项目贡献过记忆的来源文件。'));
    $('source-summary').textContent = '最后同步：' + (latest ? fmtTime(latest) : '—') + ' · 失败 ' + (d.failed || 0) + ' 个 · 共 ' + (d.total || 0) + ' 个';
    $('ov-sources').textContent = (d.total || 0) + ' 个（失败 ' + (d.failed || 0) + '）';
    $('ov-agents').textContent = Object.keys(agents).join(' · ') || '—';
  }, function (e) {
    clear(list);
    list.appendChild(el('li', 'muted', '来源加载失败：' + e.message));
  });
}
```

`source-sync` 绑定：

```js
  $('source-sync').addEventListener('click', function () {
    busy(this, '同步', function () { return syncNow(); });
  });
```

- [ ] **Step 4: 会话**

```js
function loadProjectSessions() {
  var list = $('session-list');
  clear(list);
  api('/v1/sessions?project_key=' + encodeURIComponent(state.project.key) + '&limit=20').then(function (d) {
    var rows = d.sessions || [];
    clear(list);
    $('session-empty').hidden = rows.length > 0;
    rows.forEach(function (s) {
      var li = el('li', 'item');
      var head = el('div', 'item-head');
      head.appendChild(el('span', 'badge', s.agent));
      head.appendChild(el('strong', '', s.title || '（无标题会话）'));
      li.appendChild(head);
      li.appendChild(el('div', 'item-meta', '最近活动：' + fmtTime(s.updated_at) + ' · 事件 ' + (s.event_count || 0)));
      list.appendChild(li);
    });
  }, function (e) {
    clear(list);
    list.appendChild(el('li', 'muted', '会话加载失败：' + e.message));
  });
}
```

- [ ] **Step 5: 导航与返回**

```js
  $('back-btn').addEventListener('click', function () { location.hash = '#/'; });
  Array.prototype.forEach.call(document.querySelectorAll('#project-nav button'), function (btn) {
    btn.addEventListener('click', function () {
      var target = document.getElementById(btn.getAttribute('data-scroll'));
      if (target) target.scrollIntoView({ behavior: 'smooth', block: 'start' });
    });
  });
```

- [ ] **Step 6: 验证并提交**

Run: `go test ./internal/daemon/ -run TestIndexPage_Contract -v`
Expected: PASS

临时服务手动验证：进入项目页 → 七个区块都有内容或空状态；切换项目后数据更新；返回首页正常。

```bash
git add internal/daemon/index.html
git commit -m "feat: 项目页概况、记忆、任务、简报、来源与会话"
```

---

## Task 11: 首页项目卡片、新增记忆、自动刷新与收尾

**Files:**
- Modify: `internal/daemon/index.html`

- [ ] **Step 1: 项目卡片**

```js
// 替换 Task 8 的最小版本：加载后同时渲染首页卡片。
function loadProjects() {
  return api('/v1/projects').then(function (d) {
    state.projects = d.projects || [];
    renderProjectCards();
  });
}
function renderProjectCards() {
  var grid = $('project-grid');
  clear(grid);
  var list = state.projects.slice().sort(function (a, b) {
    if (!!b.path_exists !== !!a.path_exists) return a.path_exists ? -1 : 1;
    var ta = a.last_memory_at || '', tb = b.last_memory_at || '';
    if (ta !== tb) return tb.localeCompare(ta);
    return projectLabel(a).localeCompare(projectLabel(b));
  });
  list.forEach(function (p) {
    var card = el('button', 'project-card' + (p.path_exists === false ? ' bad' : ''));
    card.type = 'button';
    card.appendChild(el('span', 'name', projectLabel(p)));
    card.appendChild(el('span', 'path mono', p.primary_path || p.key));
    var meta = el('div', 'meta');
    meta.appendChild(el('span', '', '记忆 ' + (p.memory_count || 0)));
    meta.appendChild(el('span', '', p.task_count ? '任务卡 ' + p.task_count : '仅记忆'));
    meta.appendChild(el('span', '', p.path_exists === false ? '路径不存在' : '最后活动 ' + (p.last_memory_at ? fmtTime(p.last_memory_at) : '—')));
    card.appendChild(meta);
    card.addEventListener('click', function () { location.hash = '#/p/' + encodeURIComponent(p.key); });
    grid.appendChild(card);
  });
  $('project-count').textContent = list.length ? '共 ' + list.length + ' 个' : '';
  $('project-empty').hidden = list.length > 0;
}
```

- [ ] **Step 2: 首页新增用户级记忆**

```js
function bindAddForms() {
  fillTypeSelect($('home-add-type'), false);
  fillTypeSelect($('project-add-type'), false);
  $('home-add-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var btn = this.querySelector('button');
    busy(btn, '保存', function () {
      var text = $('home-add-text').value.trim();
      if (!text) { toast('error', '请输入记忆内容'); return Promise.resolve(); }
      return api('/v1/memory', { method: 'POST', body: JSON.stringify({
        text: text, type: $('home-add-type').value, scope: 'user', agent: 'web'
      }) }).then(function (d) {
        $('home-add-text').value = '';
        toast('info', d.created ? '已保存 #' + d.id : '相同内容已存在 #' + d.id);
        return state.userHome.reload(true);
      }).then(loadStats);
    });
  });
  $('project-add-toggle').addEventListener('click', function () {
    var form = $('project-add-form');
    form.hidden = !form.hidden;
    if (!form.hidden) $('project-add-text').focus();
  });
  $('project-add-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var btn = this.querySelector('button');
    busy(btn, '保存', function () {
      if (!state.project || !state.project.primary_path) { toast('error', '该项目没有可写路径'); return Promise.resolve(); }
      var text = $('project-add-text').value.trim();
      if (!text) { toast('error', '请输入记忆内容'); return Promise.resolve(); }
      return api('/v1/memory', { method: 'POST', body: JSON.stringify({
        text: text, type: $('project-add-type').value, scope: 'project',
        project_path: state.project.primary_path, agent: 'web'
      }) }).then(function (d) {
        $('project-add-text').value = '';
        toast('info', d.created ? '已保存 #' + d.id : '相同内容已存在 #' + d.id);
        return state.projectMem.reload(true);
      }).then(loadStats);
    });
  });
}
```

- [ ] **Step 3: 自动刷新与初始化**

```js
function refreshHome() {
  return Promise.all([
    loadStats().catch(function (e) { toast('error', '刷新统计失败：' + e.message); }),
    loadProjects().catch(function (e) { toast('error', '刷新项目失败：' + e.message); })
  ]).then(function () {
    if (state.userHome) return state.userHome.reload(false);
  });
}
function refreshProject() {
  if (!state.project) return Promise.resolve();
  var p = findProject(state.project.key);
  if (p) { state.project = p; renderOverview(p); }
  return Promise.all([
    state.projectMem ? state.projectMem.reload(false) : Promise.resolve(),
    state.projectUser ? state.projectUser.reload(false) : Promise.resolve(),
    loadProjectTasks().catch(function () {}),
    loadProjectHandoff().catch(function () {}),
    loadProjectSources(),
    loadProjectSessions()
  ]);
}
function refreshLive() {
  if (document.hidden || state.edit) return;
  if (state.view === 'project') refreshProject(); else refreshHome();
}
// syncNow 里原来的 refreshAll() 改成刷新当前视图
// return (state.view === 'project' ? refreshProject() : refreshHome());
```

`init()` 末尾：

```js
  bindAddForms();
  loadProjects().then(function () {
    route();
    setInterval(refreshLive, 30000);
    window.addEventListener('focus', refreshLive);
  });
```

删除旧的 `setInterval(refreshLive, 30000)`、`window.addEventListener('focus', ...)` 重复绑定，以及旧的项目下拉/`applySelection`/`loadSearchForm` 等逻辑。

- [ ] **Step 4: 手动回归**

临时服务验证：

- 首页项目卡片数量、排序、点击进入项目页；
- 用户级记忆新增后列表更新；
- 项目页新增项目记忆后列表更新；
- 30 秒/聚焦刷新不打断编辑抽屉；
- 统计下钻抽屉仍可用；
- 无 `innerHTML`。

- [ ] **Step 5: 提交**

```bash
git add internal/daemon/index.html
git commit -m "feat: 首页项目卡片、新增入口与自动刷新"
```

---

## Task 12: 文档、全量测试与真机验收

**Files:**
- Modify: `docs/usage.md`
- Maybe `README.md`（Web UI 一句描述）

- [ ] **Step 1: 更新文档**

`docs/usage.md` 的 Web UI 章节改为：

- 首页：项目卡片、统计下钻、用户级记忆与新增；
- 项目页：地址 `#/p/<项目key>`、七个区块、返回；
- 记忆：点开查看来源与时间，编辑正文/类型；冲突自动合并；导入记忆的覆盖提示；
- 同步：自动扫描（启动 + 文件监听 + 30 秒轮询）与手动「立即同步」；MCP 是 AI 主动写库的通道，不是同步前置条件；
- 已知限制：Trae/WorkBuddy 无会话正文；不做硬删除。

- [ ] **Step 2: 全量测试与静态检查**

```bash
go build ./...
go vet ./...
go test ./... -count=1
git diff --check
```

Expected: 全部通过；格式检查无输出（`gofmt` 只对 Go 文件有意义，这里只用于确认没有误改 Go 文件）。

- [ ] **Step 3: 重启桌面应用**

```bash
taskkill //F //IM agent-memory-app.exe
go build -ldflags "-H windowsgui" -o agent-memory-app.exe ./cmd/agent-memory-app
powershell -NoProfile -Command "Start-Process -FilePath 'F:\go\agent-memory\agent-memory-app.exe'"
```

等 healthz 200 后打开窗口。

- [ ] **Step 4: 真机验收**

用 company（154 条）和 nuonuo-fresh3-api（21 条）走一遍：

1. 首页项目卡片可点击，进入 `#/p/<key>`；
2. 项目页七个区块都有数据或空状态；
3. 点开一条记忆 → 抽屉显示正文、类型、来源、时间；
4. 修改一条导入记忆的正文并保存 → 列表/搜索更新；再改回原内容；
5. 编辑成另一条的相同内容 → 提示“已合并”，不产生重复；
6. 标过时/恢复；
7. 来源区块显示最后同步与失败原因，「立即同步」可用；
8. 会话：Qoder 项目有列表，Trae 项目显示空状态说明；
9. 路径不存在的项目写操作禁用；
10. 返回首页、刷新、后退正常；统计下钻、托盘、窗口记忆无回归。

用真实鼠标（UI Automation 定位 + `mouse_event`）完成点击验证，并截图留档到 `docs/superpowers/specs/assets/project-console/`。

- [ ] **Step 5: 提交**

```bash
git add docs/usage.md README.md docs/superpowers/specs/assets/project-console
git commit -m "docs: 项目控制台与记忆编辑使用说明"
```

---

## 自审清单（写完计划后执行）

- [ ] 对照 spec 第 2–14 节：每个“目标/区块/接口/验收”都能指到具体 Task。
- [ ] 检查占位符：无 TBD/TODO、“类似 Task N”、未定义函数名。
- [ ] 类型/命名一致：`memoryView.reload`、`state.projectMem`、`ListProjectSources`、`TaskCount`、`merged_into` 在各 Task 中一致。
- [ ] 每个 Task 都有“先失败、再实现、再通过、再提交”的顺序。
- [ ] 所有新接口都有鉴权测试；写接口有 Origin 测试。
