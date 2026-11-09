# 任务详情工作台 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把任务详情页改成「对话优先 + 真 Todo + 状态覆盖层」的工作台，并让 MCP agent 能完整更新对话任务。

**Architecture:** 沿 adapter → ingest → store → daemon → webui/compose 分层。扫描只写「源 → 步骤文本/源状态」，人工状态进独立覆盖表，读取视图在 store 汇合叠加；对话区库优先、按需按字节窗口回溯源 jsonl；MCP 写入语义分层（状态类不冻结、文本类才冻结）并新增 `task_sync`。

**Tech Stack:** Go 1.26（纯 Go SQLite `modernc.org/sqlite`，`CGO_ENABLED=0`）、原生 JS Web UI（无框架）、`go test`。

**Spec:** `docs/superpowers/specs/2026-11-09-task-detail-workbench-design.md`

**约定：** 所有 Go 代码 `gofmt` 风格（tab 缩进）。Windows 工作区 CRLF，`gofmt -l .` 有假阳性，检查格式用 `git show :<file> | gofmt -d`。每个 Task 结束 commit。运行测试统一 `go test ./... -count=1`。

---

## File Structure

| 文件 | 责任 | 动作 |
| --- | --- | --- |
| `internal/tailreader/tailreader.go` | 按字节窗口从文件尾回溯读 JSONL，产出一页消息 + 下一偏移 | Create |
| `internal/tailreader/tailreader_test.go` | 窗口/截断/偏移失效测试 | Create |
| `internal/agent/qoder/todo.go` | 归并 TaskCreate/TaskUpdate 为 Todo 步骤 | Create |
| `internal/agent/qoder/todo_test.go` | 五类 fixture 测试 | Create |
| `internal/agent/qoder/session.go` | ScanSessionUnits 填 `u.Steps`；新增 `ParseSessionFile` | Modify |
| `internal/store/schema.go` | `task_step_override` 表 + `schemaVersion="3"` | Modify |
| `internal/store/store.go` | Open 允许 ver=="2"（2→3 只建新表） | Modify |
| `internal/store/task_override.go` | 覆盖读写 + prune + 叠加 | Create |
| `internal/store/task_override_test.go` | 叠加/孤儿/prune/非法值 | Create |
| `internal/store/task.go` | UpdateTask 分层 freeze；TaskDetail/ListTasks 叠加；TaskFiles 聚合 | Modify |
| `internal/ingest/tasks.go` | 同步后 prune 覆盖 | Modify |
| `internal/ingest/sync.go` | `Runner.SyncSession` 定向同步（绕过静默/哈希） | Create |
| `internal/daemon/conversation.go` | `/v1/task/conversation`、`/v1/task/files`、`/v1/tasks/step/status` | Create |
| `internal/daemon/tasks.go` | export 增加 `mode=brief|full` | Modify |
| `internal/daemon/server.go` | 注册新路由 | Modify |
| `internal/daemon/mcp.go` | UpdateTask 路由 step_status；新增 SyncTask | Modify |
| `internal/mcp/tools.go` | TaskStepInput.SourceKey、UpdateTaskInput.StepStatus、SyncTaskInput、task_sync、描述 | Modify |
| `internal/compose/task_export.go` | `RenderTaskHandoffBrief` + 保留 `RenderTaskExport` | Modify |
| `internal/daemon/webui/task.html` | 任务头 + tab bar + 四 pane | Modify |
| `internal/daemon/webui/app.js` | initTask 接四 tab + 回溯 + 覆盖 + 文件 + 交接 | Modify |
| `internal/daemon/webui/app.css` | tab/pane/进度条样式 | Modify |
| docs | usage / client-guide / README 同步 | Modify |

---

### Task 1: tailreader 包

**Files:**
- Create: `internal/tailreader/tailreader.go`
- Test: `internal/tailreader/tailreader_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tailreader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func line(role, text string) string {
	return `{"type":"` + role + `","message":{"role":"` + role + `","content":"` + text + `"}}`
}

func TestPageReadsTailWithoutWholeFile(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, line("user", strings.Repeat("x", 200)))
	}
	p := writeLines(t, lines)
	msgs, next, err := Page(p, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("want 5 msgs got %d", len(msgs))
	}
	if next <= 0 {
		t.Fatalf("want next>0 for a file bigger than window, got %d", next)
	}
	// 第二页应比第一页更早，且不重叠
	older, next2, err := Page(p, next, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 5 || next2 >= next {
		t.Fatalf("older page wrong: n=%d next2=%d next=%d", len(older), next2, next)
	}
}

func TestPageDropsPartialFirstLine(t *testing.T) {
	p := writeLines(t, []string{line("user", "aaa"), line("user", "bbb"), line("user", "ccc")})
	st, _ := os.Stat(p)
	// before 落在第二行中间：首行不完整应被丢弃
	msgs, _, err := Page(p, st.Size()-3, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "aaa") {
			t.Fatalf("partial first line leaked: %+v", m)
		}
	}
}

func TestPageTruncatesHugeLine(t *testing.T) {
	p := writeLines(t, []string{line("user", strings.Repeat("y", 2*1024*1024))})
	msgs, _, err := Page(p, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || !msgs[0].Truncated {
		t.Fatalf("huge line should be truncated: %+v", msgs)
	}
}

func TestPageMissingFile(t *testing.T) {
	_, _, err := Page(filepath.Join(t.TempDir(), "nope.jsonl"), 0, 5)
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestPageReachedStart(t *testing.T) {
	p := writeLines(t, []string{line("user", "only")})
	_, next, err := Page(p, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if next != 0 {
		t.Fatalf("small file should report next=0, got %d", next)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailreader/ -count=1`
Expected: FAIL / build error `undefined: Page`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package tailreader 按字节窗口从 JSONL 文件尾往前读，只读不整份解析。
// 用于任务详情页「加载更早」：44MB 的会话也只读几 MB。
package tailreader

import (
	"bytes"
	"encoding/json"
	"os"
	"time"
)

const (
	windowBytes = 2 * 1024 * 1024
	maxLine     = 1 * 1024 * 1024
)

// Message 是一条回溯出来的对话消息。Truncated 表示单行超过 maxLine 被截断。
type Message struct {
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	Tool      string    `json:"tool"`
	Ts        time.Time `json:"ts"`
	Truncated bool      `json:"truncated"`
}

type jsonlLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

// Page 读 [before-window, before) 窗口，返回窗口内最多 limit 条消息（时间正序）
// 与下一次回溯的偏移 next（=窗口起点；到文件头时为 0）。
// before<=0 或超过文件大小时视为文件尾。文件不存在返回 error，由调用方回退。
func Page(path string, before int64, limit int) ([]Message, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := st.Size()
	if before <= 0 || before > size {
		before = size
	}
	start := before - windowBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, before-start)
	if _, err := f.ReadAt(buf, start); err != nil && err.Error() != "EOF" {
		return nil, 0, err
	}
	lines := bytes.Split(buf, []byte("\n"))
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // 窗口起点可能切在半行，丢弃不完整首行
	}
	var all []Message
	for _, raw := range lines {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		trunc := false
		if len(raw) > maxLine {
			raw = raw[:maxLine]
			trunc = true
		}
		var l jsonlLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		role := l.Message.Role
		if role == "" {
			role = l.Type
		}
		ts, _ := time.Parse(time.RFC3339, l.Timestamp)
		for _, m := range messagesFromContent(role, l.Message.Content) {
			m.Ts = ts
			m.Truncated = trunc
			all = append(all, m)
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	next := start
	return all, next, nil
}

func messagesFromContent(role string, raw json.RawMessage) []Message {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []Message{{Role: role, Kind: "text", Text: s}}
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []Message
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, Message{Role: role, Kind: "text", Text: b.Text})
			}
		case "tool_use":
			out = append(out, Message{Role: role, Kind: "tool", Tool: b.Name})
		}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tailreader/ -count=1`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tailreader/
git commit -m "feat: tailreader 按字节窗口回溯读会话 JSONL"
```

---

### Task 2: Qoder 真 Todo 解析

**Files:**
- Create: `internal/agent/qoder/todo.go`
- Create: `internal/agent/qoder/todo_test.go`
- Modify: `internal/agent/qoder/session.go` (ScanSessionUnits 填 Steps；新增 ParseSessionFile)

- [ ] **Step 1: Write the failing test**

```go
package qoder

import (
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func todoLine(name, input string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"` + name + `","input":` + input + `}]}}`
}

func TestParseTodosMergesUpdates(t *testing.T) {
	content := strings.Join([]string{
		todoLine("TaskCreate", `{"id":"1","subject":"写接口","activeForm":"正在写接口"}`),
		todoLine("TaskCreate", `{"id":"2","subject":"补测试"}`),
		todoLine("TaskUpdate", `{"id":"1","status":"done"}`),
		todoLine("TaskUpdate", `{"id":"2","status":"doing"}`),
		todoLine("TaskUpdate", `{"id":"1","status":"blocked"}`), // 最后一次覆盖
	}, "\n")
	steps := parseTodos([]byte(content))
	if len(steps) != 2 {
		t.Fatalf("want 2 steps got %d: %+v", len(steps), steps)
	}
	byKey := map[string]todo{}
	for _, s := range steps {
		byKey[s.SourceKey] = s
	}
	if byKey["qt:1"].Status != domain.StepBlocked || byKey["qt:1"].Subject != "写接口" {
		t.Fatalf("status-only update must keep subject and take last status: %+v", byKey["qt:1"])
	}
	if byKey["qt:2"].Status != domain.StepDoing {
		t.Fatalf("step2 = %+v", byKey["qt:2"])
	}
}

func TestParseTodosSkipsBadAndEmpty(t *testing.T) {
	content := strings.Join([]string{
		todoLine("TaskCreate", `{"subject":"无 id 丢弃"}`),
		todoLine("TaskUpdate", `{"id":"9","status":"done"}`), // 无 create，丢弃
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"TaskCreate","input":{ broken`,
	}, "\n")
	if steps := parseTodos([]byte(content)); len(steps) != 0 {
		t.Fatalf("want 0 got %+v", steps)
	}
}

func TestParseTodosNoTodoReturnsNil(t *testing.T) {
	if steps := parseTodos([]byte(`{"type":"user","message":{"role":"user","content":"hi"}}`)); steps != nil {
		t.Fatalf("want nil got %+v", steps)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/qoder/ -run TestParseTodos -count=1`
Expected: FAIL `undefined: parseTodos` / `undefined: todo`.

- [ ] **Step 3: Write minimal implementation**

`internal/agent/qoder/todo.go`:

```go
package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// todo 是 Qoder 会话里 TaskCreate/TaskUpdate 归并后的一条 Todo。
type todo struct {
	SourceKey  string
	Subject    string
	ActiveForm string
	Status     string
	order      int
}

type todoInput struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

// parseTodos 从会话 JSONL 归并 TaskCreate/TaskUpdate：按 Todo id 取最后一次状态；
// 只改 status 的 update 保留 create 时的 subject。缺 id / 无 create / 坏 JSON 都跳过。
// 整场没有 Todo 调用时返回 nil，交给 ingest 的反推兜底。
func parseTodos(content []byte) []agent.TaskStepDraft {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	merged := map[string]*todo{}
	order := 0
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var l jsonlLine
		if json.Unmarshal(raw, &l) != nil || l.Type != "assistant" {
			continue
		}
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || (b.Name != "TaskCreate" && b.Name != "TaskUpdate") {
				continue
			}
			var in todoInput
			if json.Unmarshal(b.Input, &in) != nil || in.ID == "" {
				continue
			}
			key := "qt:" + in.ID
			cur, ok := merged[key]
			if !ok {
				if b.Name != "TaskCreate" || strings.TrimSpace(in.Subject) == "" {
					continue // 没有 create 的 update 不凭空造步骤
				}
				order++
				cur = &todo{SourceKey: key, Subject: in.Subject, ActiveForm: in.ActiveForm, Status: domain.StepPending, order: order}
				merged[key] = cur
			}
			if in.Subject != "" {
				cur.Subject = in.Subject
			}
			if in.ActiveForm != "" {
				cur.ActiveForm = in.ActiveForm
			}
			if in.Status != "" {
				cur.Status = mapTodoStatus(in.Status)
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	list := make([]*todo, 0, len(merged))
	for _, t := range merged {
		list = append(list, t)
	}
	// 按 create 顺序稳定输出
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].order < list[i].order {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	out := make([]agent.TaskStepDraft, 0, len(list))
	for _, t := range list {
		out = append(out, agent.TaskStepDraft{SourceKey: t.SourceKey, Subject: t.Subject, ActiveForm: t.ActiveForm, Status: t.Status})
	}
	return out
}

func mapTodoStatus(s string) string {
	switch s {
	case "completed", "done":
		return domain.StepDone
	case "in_progress", "doing":
		return domain.StepDoing
	case "blocked":
		return domain.StepBlocked
	case "skipped", "cancelled":
		return domain.StepSkipped
	default:
		return domain.StepPending
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/qoder/ -run TestParseTodos -count=1`
Expected: PASS (3 tests).

- [ ] **Step 5: Wire into ScanSessionUnits + add ParseSessionFile**

In `internal/agent/qoder/session.go`, inside `ScanSessionUnits` after `u.Events = events` add:

```go
			u.Steps = parseTodos(content)
```

And append a new exported method (used by ingest 定向同步):

```go
// ParseSessionFile 解析单个会话文件，供定向同步（task_sync）复用，不走目录扫描。
func (a *Adapter) ParseSessionFile(path string) (agent.SessionUnit, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return agent.SessionUnit{}, err
	}
	u := agent.SessionUnit{Path: path, Content: content, Size: int64(len(content))}
	if st, err := os.Stat(path); err == nil {
		u.MTime = st.ModTime()
	}
	sess, events, err := parseSessionJSONL(path, content)
	if err != nil {
		return u, err
	}
	sess.Agent = a.Name()
	u.Session = sess
	u.Events = events
	u.Steps = parseTodos(content)
	return u, nil
}
```

- [ ] **Step 6: Run package tests**

Run: `go test ./internal/agent/... -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/qoder/
git commit -m "feat: 解析 Qoder 会话 TaskCreate/TaskUpdate 为真 Todo 步骤"
```

---

### Task 3: schema v3 + 覆盖表 store

**Files:**
- Modify: `internal/store/schema.go` (add table + version)
- Modify: `internal/store/store.go` (allow ver=="2")
- Create: `internal/store/task_override.go`
- Create: `internal/store/task_override_test.go`

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func overrideFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/ov", "ov", "", `F:\ov`, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "覆盖测试", CreatedAt: now, UpdatedAt: now,
		Steps: []domain.TaskStep{
			{Subject: "稳定步骤", SourceKey: "qt:1", Status: domain.StepPending, CreatedAt: now, UpdatedAt: now},
			{Subject: "反推步骤", SourceKey: "cmd:0", Status: domain.StepPending, CreatedAt: now, UpdatedAt: now},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestOverrideOverlayAndOrphan(t *testing.T) {
	s, id := overrideFixture(t)
	ctx := context.Background()
	now := time.Unix(1700000100, 0)
	if err := s.SetStepOverride(ctx, id, "qt:1", domain.StepDone, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepOverride(ctx, id, "qt:999", domain.StepDone, now); err != nil { // 孤儿
		t.Fatal(err)
	}
	d, ok, err := s.TaskDetail(ctx, id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	var stable, derived string
	for _, st := range d.Steps {
		if st.SourceKey == "qt:1" {
			stable = st.Status
		}
		if st.SourceKey == "cmd:0" {
			derived = st.Status
		}
	}
	if stable != domain.StepDone {
		t.Fatalf("stable step should be overridden to done, got %s", stable)
	}
	if derived != domain.StepPending {
		t.Fatalf("derived step must NOT be overridden, got %s", derived)
	}
}

func TestOverrideRejectsInvalidStatus(t *testing.T) {
	s, id := overrideFixture(t)
	if err := s.SetStepOverride(context.Background(), id, "qt:1", "bogus", time.Now()); err == nil {
		t.Fatal("want error for invalid status")
	}
}

func TestPruneStaleOverrides(t *testing.T) {
	s, id := overrideFixture(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.SetStepOverride(ctx, id, "qt:1", domain.StepDone, now)
	_ = s.SetStepOverride(ctx, id, "qt:999", domain.StepDone, now)
	if err := s.PruneStaleOverrides(ctx, id, []string{"qt:1", "cmd:0"}); err != nil {
		t.Fatal(err)
	}
	ov, err := s.stepOverrides(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := ov["qt:999"]; exists {
		t.Fatal("orphan override should be pruned")
	}
	if ov["qt:1"] != domain.StepDone {
		t.Fatal("alive override should survive prune")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run TestOverride -count=1` and `-run TestPruneStale`
Expected: FAIL `undefined: SetStepOverride` etc.

- [ ] **Step 3: Add table + version + migration allowance**

In `internal/store/schema.go` change `const schemaVersion = "2"` → `"3"`, and append to `schemaSQL` (after `task_session` table):

```sql
CREATE TABLE IF NOT EXISTS task_step_override (
  task_id     INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  source_key  TEXT    NOT NULL,
  status      TEXT    NOT NULL,
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (task_id, source_key)
);
```

In `internal/store/store.go` Open, replace the `if ver == "1" { ... } else { ...error... }` block with:

```go
		if ver == "1" {
			if err := migrateV1(db); err != nil {
				db.Close()
				return nil, fmt.Errorf("升级库结构（schema_version 1→2）失败：%w", err)
			}
		} else if ver != "2" {
			db.Close()
			return nil, fmt.Errorf("旧版本库（schema_version %q < %s），需重建", ver, schemaVersion)
		}
		// ver=="2"→3 只新增 task_step_override 表，下面的 schemaSQL 会建，无需数据迁移。
```

- [ ] **Step 4: Write override store**

`internal/store/task_override.go`:

```go
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// SetStepOverride 记录人工对某个稳定 source_key 步骤的状态修正。
// 只接受稳定 key（非空且不以反推序号前缀开头）与合法状态；不触碰 manual_edited_at。
func (s *Store) SetStepOverride(ctx context.Context, taskID int64, sourceKey, status string, at time.Time) error {
	if sourceKey == "" || !stableSourceKey(sourceKey) {
		return fmt.Errorf("step override requires a stable source_key, got %q", sourceKey)
	}
	if !validStepStatus(status) {
		return fmt.Errorf("invalid step status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO task_step_override(task_id, source_key, status, updated_at) VALUES(?,?,?,?)
ON CONFLICT(task_id, source_key) DO UPDATE SET status=excluded.status, updated_at=excluded.updated_at`,
		taskID, sourceKey, status, at.Unix())
	return err
}

// stableSourceKey 反推步骤的 key 是 cmd:0/err:1 这类序号，内容一变就错位，不接受覆盖。
func stableSourceKey(k string) bool {
	for _, prefix := range []string{"cmd:", "err:", "next:", "file:"} {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			return false
		}
	}
	return true
}

func (s *Store) stepOverrides(ctx context.Context, taskID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_key, status FROM task_step_override WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// PruneStaleOverrides 删除该任务下已不存在于 task_step 的覆盖（agent 删了那条 todo）。
func (s *Store) PruneStaleOverrides(ctx context.Context, taskID int64, aliveKeys []string) error {
	alive := map[string]bool{}
	for _, k := range aliveKeys {
		alive[k] = true
	}
	ov, err := s.stepOverrides(ctx, taskID)
	if err != nil {
		return err
	}
	for k := range ov {
		if !alive[k] {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM task_step_override WHERE task_id = ? AND source_key = ?`, taskID, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyOverrides 把人工状态叠加到读取视图；只命中稳定 key，孤儿不叠加。
func applyOverrides(task *domain.Task, ov map[string]string) {
	if len(ov) == 0 {
		return
	}
	for i := range task.Steps {
		if v, ok := ov[task.Steps[i].SourceKey]; ok && stableSourceKey(task.Steps[i].SourceKey) {
			task.Steps[i].Status = v
		}
	}
}
```

- [ ] **Step 5: Overlay in read views**

In `internal/store/task.go` `TaskDetail`, after `detail := TaskDetail{Task: task}` and loading snippets/sessions, before `return`, add:

```go
	ov, err := s.stepOverrides(ctx, id)
	if err != nil {
		return TaskDetail{}, false, err
	}
	applyOverrides(&detail.Task, ov)
```

In `ListTasks`, inside the loop that appends each loaded `task` (the `if ok { out = append(out, task) }` block), change to:

```go
		if ok {
			ov, err := s.stepOverrides(ctx, id)
			if err != nil {
				return nil, err
			}
			applyOverrides(&task, ov)
			out = append(out, task)
		}
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/store/ -count=1`
Expected: PASS (including new override tests and existing migration tests).

- [ ] **Step 7: Commit**

```bash
git add internal/store/
git commit -m "feat: schema v3 步骤状态覆盖层（稳定 key 叠加、孤儿 prune）"
```

---

### Task 4: UpdateTask 分层 freeze + 任务文件聚合

**Files:**
- Modify: `internal/store/task.go`
- Test: `internal/store/task_freeze_test.go` (Create)

- [ ] **Step 1: Write the failing test**

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestUpdateTaskLayeredFreeze(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/fz", "fz", "", `F:\fz`, now)
	id, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Kind: domain.TaskKindSession, SessionID: 1, Goal: "g", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	status := domain.StatusBlocked
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, Status: &status}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ := s.TaskByID(ctx, id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("status-only update must NOT freeze")
	}
	next := "n"
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, NextSteps: &next}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ = s.TaskByID(ctx, id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("next_steps update must NOT freeze")
	}
	goal := "g2"
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, Goal: &goal}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ = s.TaskByID(ctx, id)
	if tk.ManualEditedAt.IsZero() {
		t.Fatal("goal update MUST freeze")
	}
}

func TestTaskFilesAggregates(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/tf", "tf", "", `F:\tf`, now)
	sid, err := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "tf1", ProjectID: pid, Path: `C:\tf1.jsonl`, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Kind: domain.TaskKindSession, SessionID: sid, Goal: "g", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkTaskSession(ctx, id, sid, "source", now); err != nil {
		t.Fatal(err)
	}
	evs := []domain.SessionEvent{
		{SessionID: sid, Seq: 1, Kind: domain.KindToolEdit, Files: []string{"a.go"}, Ts: now},
		{SessionID: sid, Seq: 2, Kind: domain.KindToolEdit, Files: []string{"a.go", "b.go"}, Ts: now.Add(time.Minute)},
	}
	if err := s.ReplaceEvents(ctx, sid, evs); err != nil {
		t.Fatal(err)
	}
	files, err := s.TaskFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files got %+v", files)
	}
	byPath := map[string]TaskFileStat{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if byPath["a.go"].Count != 2 || byPath["b.go"].Count != 1 {
		t.Fatalf("counts wrong: %+v", byPath)
	}
	if !byPath["a.go"].LastTs.Equal(now.Add(time.Minute)) {
		t.Fatalf("last ts wrong: %+v", byPath["a.go"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/store/ -run "TestUpdateTaskLayeredFreeze|TestTaskFilesAggregates" -count=1`
Expected: FAIL (freeze assertion + `undefined: TaskFiles`).

- [ ] **Step 3: Layer the freeze in UpdateTask**

In `internal/store/task.go` `UpdateTask`:
- Remove `manual_edited_at = ?, ` and the corresponding `now, ` argument from the `NextSteps`, `OwnerAgent`, `Status`, and both `Archived` UPDATE statements (keep `updated_at = ?`).
- Replace the final unconditional update:

```go
	freeze := in.Goal != nil || in.Steps != nil
	if freeze {
		if _, err := tx.ExecContext(ctx, `UPDATE task SET manual_edited_at = ?, updated_at = ? WHERE id = ?`, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE task SET updated_at = ? WHERE id = ?`, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
```

(The `Goal` branch keeps its own `manual_edited_at = ?`; that is fine and redundant-safe.)

- [ ] **Step 4: Add TaskFiles aggregation**

Append to `internal/store/task.go`:

```go
// TaskFileStat 是任务文件区的一行：路径 + 改动次数 + 最近改动时间 + 来源 agent。
type TaskFileStat struct {
	Path    string    `json:"path"`
	Count   int       `json:"count"`
	LastTs  time.Time `json:"last_ts"`
	Agent   string    `json:"agent"`
}

// TaskFiles 从关联会话的 tool_edit 事件按文件聚合；无关联会话时退回 task.Files。
func (s *Store) TaskFiles(ctx context.Context, taskID int64) ([]TaskFileStat, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.files_json, e.ts, se.agent
FROM session_event e
JOIN task_session ts ON ts.session_id = e.session_id
JOIN session se ON se.id = e.session_id
WHERE ts.task_id = ? AND e.kind = 'tool_edit'
ORDER BY e.ts`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agg := map[string]*TaskFileStat{}
	var order []string
	for rows.Next() {
		var filesJSON string
		var ts int64
		var agent string
		if err := rows.Scan(&filesJSON, &ts, &agent); err != nil {
			return nil, err
		}
		var list []string
		if json.Unmarshal([]byte(filesJSON), &list) != nil {
			continue
		}
		for _, p := range list {
			st, ok := agg[p]
			if !ok {
				st = &TaskFileStat{Path: p, Agent: agent}
				agg[p] = st
				order = append(order, p)
			}
			st.Count++
			if t := timeFromUnix(ts); t.After(st.LastTs) {
				st.LastTs = t
				st.Agent = agent
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(order) == 0 {
		task, ok, err := s.TaskByID(ctx, taskID)
		if err != nil || !ok {
			return nil, err
		}
		for _, p := range task.Files {
			order = append(order, p)
			agg[p] = &TaskFileStat{Path: p, Count: 1, Agent: task.SourceAgent}
		}
	}
	out := make([]TaskFileStat, 0, len(order))
	for _, p := range order {
		out = append(out, *agg[p])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastTs.After(out[j].LastTs) })
	return out, nil
}
```

Ensure `encoding/json` and `sort` are imported in task.go (they already are for other funcs; verify).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/store/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/store/
git commit -m "feat: UpdateTask 分层 freeze + 任务文件按事件聚合"
```

---

### Task 5: ingest 同步后 prune + 定向 SyncSession

**Files:**
- Modify: `internal/ingest/tasks.go`
- Create: `internal/ingest/sync.go`
- Test: `internal/ingest/sync_test.go`

- [ ] **Step 1: Write the failing test**

```go
package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/agent/qoder"
)

func TestSyncSessionBypassesSettle(t *testing.T) {
	home, repo, _ := fakeQoder(t)
	p := filepath.Join(home, "projects", "RA", "live.jsonl")
	write(t, p,
		`{"type":"workspace-directories","sessionId":"live","directories":["`+repo+`"]}`+"\n"+
			`{"type":"user","sessionId":"live","timestamp":"2026-11-09T09:00:00Z","message":{"role":"user","content":"正在写的会话"}}`+"\n"+
			`{"type":"assistant","sessionId":"live","timestamp":"2026-11-09T09:01:00Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"TaskCreate","input":{"id":"1","subject":"第一步"}}]}}`+"\n")
	// mtime = now → 未静默；普通 scan 不出卡，但 SyncSession 必须同步出任务
	s := openTempStore(t)
	r := New(s, qoder.New(home))
	r.SettleIdle = 5 * time.Minute
	id, settled, err := r.SyncSession(context.Background(), "qoder", p)
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("want a task id")
	}
	if settled {
		t.Fatal("live session should report settled=false")
	}
	tk, ok, err := s.TaskByID(context.Background(), id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if len(tk.Steps) != 1 || tk.Steps[0].SourceKey != "qt:1" {
		t.Fatalf("steps should come from real Todo: %+v", tk.Steps)
	}
	_ = os.Remove(p)
}

func TestSyncSessionMissingFile(t *testing.T) {
	s := openTempStore(t)
	r := New(s, qoder.New(t.TempDir()))
	if _, _, err := r.SyncSession(context.Background(), "qoder", filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("want error for missing file")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ingest/ -run TestSyncSession -count=1`
Expected: FAIL `r.SyncSession undefined`.

- [ ] **Step 3: Prune in syncTaskFromSession**

In `internal/ingest/tasks.go`, at the end of `syncTaskFromSession` (both the create branch return and the update path), before returning nil on the update path, add prune. Simplest: after `RefreshTaskSnippets` on the update path add:

```go
	keys := make([]string, 0, len(derived))
	for _, st := range derived {
		keys = append(keys, st.SourceKey)
	}
	if err := r.store.PruneStaleOverrides(ctx, task.ID, keys); err != nil {
		return err
	}
```

- [ ] **Step 4: Write SyncSession**

`internal/ingest/sync.go`:

```go
package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/extract"
)

// sessionParser 是适配器的可选能力：解析单个会话文件（定向同步用）。
type sessionParser interface {
	ParseSessionFile(path string) (agent.SessionUnit, error)
}

// SyncSession 定向同步一个会话文件，绕过静默窗口与哈希门控（task_sync 用）。
// 返回任务 id 与是否已静默；正在写的会话 settled=false。
func (r *Runner) SyncSession(ctx context.Context, agentName, path string) (int64, bool, error) {
	var parser sessionParser
	for _, a := range r.adapters {
		if a.Name() == agentName {
			p, ok := a.(sessionParser)
			if !ok {
				return 0, false, fmt.Errorf("agent %s 不支持定向同步", agentName)
			}
			parser = p
		}
	}
	if parser == nil {
		return 0, false, fmt.Errorf("unknown agent %q", agentName)
	}
	u, err := parser.ParseSessionFile(path)
	if err != nil {
		return 0, false, err
	}
	now := time.Now()
	projectID, err := r.resolveSessionProject(ctx, agentName, u.Session.ProjectSlug, u.Session.ProjectPath, u.Session.Confidence, now)
	if err != nil {
		return 0, false, err
	}
	if projectID == 0 {
		return 0, false, fmt.Errorf("无法确定会话 %s 的项目归属", path)
	}
	sess := u.Session
	sess.Agent = agentName
	sess.ProjectID = projectID
	sessID, err := r.store.UpsertSession(ctx, sess)
	if err != nil {
		return 0, false, err
	}
	if err := r.store.ReplaceEvents(ctx, sessID, agent.EventsWithSession(sessID, u.Events)); err != nil {
		return 0, false, err
	}
	settled := extract.Settled(u.MTime, now, r.SettleIdle)
	var card *domain.TaskCard
	if extracted, ok := extract.TaskCard(sess, agent.EventsWithSession(sessID, u.Events), now); ok {
		extracted.SessionID = sessID
		extracted.ProjectID = projectID
		if _, err := r.store.UpsertTaskCard(ctx, extracted); err == nil {
			card = &extracted
		}
	}
	if err := r.syncTaskFromSession(ctx, agentName, sess, sessID, card, u.Steps, now); err != nil {
		return 0, false, err
	}
	tk, ok, err := r.store.TaskByKindSession(ctx, sessID)
	if err != nil || !ok {
		return 0, settled, fmt.Errorf("同步后未找到对话任务")
	}
	return tk.ID, settled, nil
}
```

Verify `Runner` field name for adapters (`r.adapters`) and `resolveSessionProject` signature match existing code; adjust if the field is named differently (check `internal/ingest/ingest.go` struct).

- [ ] **Step 5: Run tests**

Run: `go test ./internal/ingest/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/ingest/
git commit -m "feat: 定向 SyncSession 绕过静默窗口 + 同步后 prune 覆盖"
```

---

### Task 6: daemon 对话/文件/步骤状态端点 + export mode

**Files:**
- Create: `internal/daemon/conversation.go`
- Modify: `internal/daemon/tasks.go` (export mode)
- Modify: `internal/daemon/server.go` (routes)
- Test: `internal/daemon/conversation_test.go`

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// seedSessionTask 建一个带稳定 key 步骤、源文件不存在的对话任务。
func seedSessionTask(t *testing.T, st *store.Store) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	pid, err := st.EnsureProject(ctx, "f:/conv", "conv", "", `F:\conv`, now)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "conv1", ProjectID: pid, Path: `C:\nonexistent\conv1.jsonl`, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Kind: domain.TaskKindSession, SessionID: sid, Goal: "g",
		SourcePath: `C:\nonexistent\conv1.jsonl`, CreatedAt: now, UpdatedAt: now,
		Steps: []domain.TaskStep{{Subject: "s", SourceKey: "qt:1", Status: domain.StepPending, CreatedAt: now, UpdatedAt: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkTaskSession(ctx, id, sid, "source", now); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConversationStoreOnlyFallback(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	rec := webDo(t, s, http.MethodGet, "/v1/task/conversation?task_id="+itoa(id)+"&before=1", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "store_only") {
		t.Fatalf("missing source file should fall back to store_only: %s", rec.Body.String())
	}
}

func TestStepStatusOverrideDoesNotFreeze(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	rec := webDo(t, s, http.MethodPost, "/v1/tasks/step/status", webToken,
		map[string]any{"task_id": id, "source_key": "qt:1", "status": "done"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	tk, _, _ := st.TaskByID(context.Background(), id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("step status override must not freeze")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run "TestConversation|TestStepStatus" -count=1`
Expected: FAIL (404 / undefined handlers).

- [ ] **Step 3: Write conversation.go**

```go
package daemon

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/muyi99/agent-memory/internal/tailreader"
)

func (s *Server) handleTaskConversation(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	detail, ok, err := s.st.TaskDetail(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	// 主来源会话：对话任务用自己那场；合并任务取最近关联那场
	path := detail.SourcePath
	if path == "" && len(detail.Sessions) > 0 {
		path = detail.Sessions[0].Path
	}
	if before == 0 {
		// 第一页用库里的片段
		writeJSON(w, http.StatusOK, map[string]any{
			"source": "store", "messages": detail.Snippets, "next": 0, "file_size": fileSize(path),
		})
		return
	}
	msgs, next, err := tailreader.Page(path, before, limit)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"source": "store_only", "messages": detail.Snippets, "next": 0,
			"warning": "源文件不可读，只显示库里最近 20 条",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": "file", "messages": msgs, "next": next})
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func (s *Server) handleTaskFiles(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	files, err := s.st.TaskFiles(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) handleStepStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID    int64  `json:"task_id"`
		SourceKey string `json:"source_key"`
		Status    string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if err := s.st.SetStepOverride(r.Context(), req.TaskID, req.SourceKey, req.Status, time.Now()); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 4: export mode + routes**

In `internal/daemon/tasks.go` `handleExportTask`, read `mode := r.URL.Query().Get("mode")`; choose renderer:

```go
	md := compose.RenderTaskExport(detail.Task, detail.Snippets, project)
	if mode == "brief" {
		md = compose.RenderTaskHandoffBrief(detail.Task, project)
	}
	writeJSON(w, http.StatusOK, map[string]any{"markdown": md})
```

In `internal/daemon/server.go` register:

```go
	s.mux.HandleFunc("GET /v1/task/conversation", s.auth(s.handleTaskConversation))
	s.mux.HandleFunc("GET /v1/task/files", s.auth(s.handleTaskFiles))
	s.mux.HandleFunc("POST /v1/tasks/step/status", s.writeAuth(s.handleStepStatus))
```

- [ ] **Step 5: Run tests**

Run: `go test ./internal/daemon/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/
git commit -m "feat: 对话回溯/任务文件/步骤状态端点 + export brief 模式"
```

---

### Task 7: compose 极简接手版

**Files:**
- Modify: `internal/compose/task_export.go`
- Test: `internal/compose/task_export_test.go`

- [ ] **Step 1: Write the failing test**

```go
package compose

import (
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestHandoffBriefIsShort(t *testing.T) {
	task := domain.Task{
		ID: 7, Goal: "给扣减加乐观锁", Status: domain.StatusBlocked,
		NextSteps: "先复现超卖", Files: []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go"},
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone},
			{Subject: "补测试", Status: domain.StepBlocked},
		},
	}
	project := domain.Project{DisplayName: "demo", Key: "f:/demo"}
	md := RenderTaskHandoffBrief(task, project)
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) > 30 {
		t.Fatalf("brief must be <=30 lines, got %d:\n%s", len(lines), md)
	}
	for _, want := range []string{"给扣减加乐观锁", "补测试", "task_get", "task_update"} {
		if !strings.Contains(md, want) {
			t.Fatalf("brief missing %q", want)
		}
	}
	if strings.Contains(md, "i.go") {
		t.Fatal("brief should cap files at 8")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/compose/ -run TestHandoffBrief -count=1`
Expected: FAIL `undefined: RenderTaskHandoffBrief`.

- [ ] **Step 3: Implement brief renderer**

Append to `internal/compose/task_export.go`:

```go
// RenderTaskHandoffBrief 极简接手版：五块、<=30 行，凸出重点不啰嗦。
func RenderTaskHandoffBrief(task domain.Task, project domain.Project) string {
	var b strings.Builder
	b.WriteString("# 接手任务\n\n")
	fmt.Fprintf(&b, "- 目标: %s\n", oneLine(task.Goal))
	fmt.Fprintf(&b, "- 项目: %s (`%s`) · task_id %d · 状态 %s\n", project.DisplayName, project.Key, task.ID, task.Status)
	if task.NextSteps != "" {
		fmt.Fprintf(&b, "- 现在到哪/卡在哪: %s\n", clipPlain(task.NextSteps, 120))
	}
	b.WriteString("\n## 未完成步骤\n\n")
	n := 0
	for _, st := range task.Steps {
		if st.Status == domain.StepDone || st.Status == domain.StepSkipped {
			continue
		}
		mark := "[ ]"
		if st.Status == domain.StepBlocked {
			mark = "[!]"
		} else if st.Status == domain.StepDoing {
			mark = "[~]"
		}
		fmt.Fprintf(&b, "- %s %s\n", mark, oneLine(st.Subject))
		n++
		if n >= 10 {
			break
		}
	}
	if n == 0 {
		b.WriteString("- （无未完成步骤）\n")
	}
	if len(task.Files) > 0 {
		b.WriteString("\n## 关键文件\n\n")
		for _, f := range head(task.Files, 8) {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	b.WriteString("\n## 交接要求\n\n")
	b.WriteString("1. 先 `task_get` 读最新任务。\n")
	b.WriteString("2. 用 `task_update` 更新同一个 task_id，不要新建。\n")
	b.WriteString("3. 完成后改 done 并在下一步写清验证结果。\n")
	return b.String()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/compose/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/compose/
git commit -m "feat: 极简接手版交接 Prompt（五块 <=30 行）"
```

---

### Task 8: MCP 完整更新（分层 + source_key + task_sync）

**Files:**
- Modify: `internal/mcp/tools.go`
- Modify: `internal/daemon/mcp.go`
- Test: `internal/daemon/mcp_sync_test.go`

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"context"
	"testing"

	"github.com/muyi99/agent-memory/internal/mcp"
)

func TestMCPStepStatusDoesNotFreeze(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	status := "done"
	_, err := s.UpdateTask(context.Background(), mcp.UpdateTaskInput{ID: id, StepStatus: map[string]string{"qt:1": status}})
	if err != nil {
		t.Fatal(err)
	}
	tk, _, _ := st.TaskByID(context.Background(), id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("step_status must not freeze")
	}
}

func TestMCPSyncTaskUnknownProject(t *testing.T) {
	s, _ := newTestServer(t)
	if _, err := s.SyncTask(context.Background(), mcp.SyncTaskInput{ProjectPath: t.TempDir()}); err == nil {
		t.Fatal("want error when project has no sessions")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run "TestMCPStepStatus|TestMCPSyncTask" -count=1`
Expected: FAIL (field/method undefined).

- [ ] **Step 3: Extend mcp types + tool defs**

In `internal/mcp/tools.go`:
- `TaskStepInput` add `SourceKey string \`json:"source_key"\``.
- `UpdateTaskInput` add `StepStatus map[string]string \`json:"step_status"\``.
- Add `type SyncTaskInput struct { ProjectPath string \`json:"project_path"\`; SessionID string \`json:"session_id"\` }`.
- `ToolAPI` add `SyncTask(ctx context.Context, in SyncTaskInput) (any, error)`.
- In `task_update` schema add `"step_status": map[string]any{"type":"object"}` and steps items property `source_key`.
- Add tool def `{"task_sync", "把指定会话立刻同步成对话任务（不等静默窗口）。不传 session_id 取该项目最近修改的会话。", objectSchema(map[string]any{"project_path": map[string]any{"type":"string"}, "session_id": map[string]any{"type":"string"}}, "project_path")}`.
- In dispatch add `case "task_sync": var in SyncTaskInput; bind; return s.api.SyncTask(ctx, in)`.
- Update `task_update` description to mention 分层语义与 step_status。

- [ ] **Step 4: daemon/mcp.go implement**

In `UpdateTask`, before calling `s.st.UpdateTask`, route step_status:

```go
	for key, status := range in.StepStatus {
		if err := s.st.SetStepOverride(ctx, in.ID, key, status, time.Now()); err != nil {
			return nil, err
		}
	}
```

and pass `SourceKey` through when converting `in.Steps` to `domain.TaskStep`. Only call `s.st.UpdateTask` if any of Goal/Status/NextSteps/OwnerAgent/Archived/Steps is non-nil; otherwise return the refreshed task.

Add to `internal/daemon/mcp.go` (Server needs an ingest Runner; add field + setter, wire in serve and newTestServer):

```go
// 在 Server struct 加字段： runner *ingest.Runner
// 加 setter：
func (s *Server) SetRunner(r *ingest.Runner) { s.runner = r }

func (s *Server) SyncTask(ctx context.Context, in mcp.SyncTaskInput) (any, error) {
	if s.runner == nil {
		return nil, fmt.Errorf("runner not configured")
	}
	path := in.SessionID
	if path == "" {
		p, err := s.latestSessionPath(ctx, in.ProjectPath)
		if err != nil {
			return nil, err
		}
		path = p
	}
	id, settled, err := s.runner.SyncSession(ctx, agentFromPath(path), path)
	if err != nil {
		return nil, err
	}
	detail, _, err := s.st.TaskDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "settled": settled, "session_path": path, "task": detail}, nil
}

// latestSessionPath 取该项目下最近更新的会话文件路径。
func (s *Server) latestSessionPath(ctx context.Context, projectPath string) (string, error) {
	pid, ok, err := s.st.ResolveProjectByPath(ctx, projectPath)
	if err != nil || !ok {
		return "", fmt.Errorf("unknown project %q", projectPath)
	}
	sessions, err := s.st.ListSessions(ctx, pid, 1)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("该项目下没有可同步的会话")
	}
	return sessions[0].Path, nil
}

// agentFromPath 按目录判断会话归属：.workbuddy → workbuddy，其余 → qoder。
func agentFromPath(path string) string {
	if strings.Contains(filepath.ToSlash(path), ".workbuddy") {
		return "workbuddy"
	}
	return "qoder"
}
```

`ListSessions` 按 `updated_at` 倒序返回（读 `internal/store/session.go:164` 确认排序），故 `sessions[0]` 即最近会话。serve 启动处（创建 ingest Runner 之后）调用 `srv.SetRunner(runner)`；`newTestServer` 里用 `ingest.New(st, qoder.New(t.TempDir()))` 设置 runner，使 SyncTask 测试可跑。`internal/daemon/mcp.go` 需 import `fmt`、`strings`、`path/filepath`、`internal/ingest`。

- [ ] **Step 5: Run tests**

Run: `go test ./internal/daemon/ ./internal/mcp/ -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/ internal/daemon/
git commit -m "feat: MCP 分层更新 + source_key + task_sync 定向同步"
```

---

### Task 9: Web UI 任务头 + 四 tab

**Files:**
- Modify: `internal/daemon/webui/task.html`
- Modify: `internal/daemon/webui/app.js` (initTask)
- Modify: `internal/daemon/webui/app.css`

- [ ] **Step 1: Replace task.html body**

Keep crumbs + kicker + title + ai-summary. Replace the form/sections with: task head form (goal/status/owner/next + actions, NO steps field), then a tab bar and four panes:

```html
  <form id="task-form" class="stack task-head">
    <label class="field" id="task-project-field"><span class="label">项目路径</span><input id="task-project" type="text"></label>
    <label class="field"><span class="label">目标</span><textarea id="task-goal" rows="2" required></textarea></label>
    <div class="row">
      <label class="field inline"><span class="label">状态</span><select id="task-status"></select></label>
      <label class="field inline"><span class="label">owner</span><input id="task-owner" type="text"></label>
    </div>
    <label class="field"><span class="label">下一步</span><textarea id="task-next" rows="2"></textarea></label>
    <p id="task-meta" class="meta"></p>
    <div class="actions">
      <button class="stamp primary" type="submit">保存</button>
      <button id="task-archive" class="stamp ghost" type="button">归档</button>
      <button id="task-merge" class="stamp ghost" type="button">合并到…</button>
      <a class="stamp ghost" href="/tasks">返回</a>
    </div>
  </form>
  <nav class="tab-bar" id="task-tabs" role="tablist">
    <button data-tab="conversation" class="tab active">最近对话</button>
    <button data-tab="steps" class="tab">进行中的 task</button>
    <button data-tab="files" class="tab">任务文件</button>
    <button data-tab="handoff" class="tab">任务交接</button>
  </nav>
  <section class="tab-pane" id="pane-conversation">
    <div id="conv-warn" class="warn" hidden></div>
    <div id="snippet-list" class="md"></div>
    <button id="conv-more" class="stamp small ghost" type="button">加载更早</button>
    <div class="section-head"><h2>来源会话</h2></div>
    <ul id="session-list" class="sources"></ul>
  </section>
  <section class="tab-pane" id="pane-steps" hidden>
    <div id="step-progress" class="meta"></div>
    <div id="task-steps" class="step-list"></div>
    <div class="actions">
      <button id="task-add-step" class="stamp small ghost" type="button">添加步骤</button>
      <button id="task-save-steps" class="stamp small" type="button">保存步骤（接管同步）</button>
    </div>
  </section>
  <section class="tab-pane" id="pane-files" hidden>
    <div class="actions"><button id="files-copy" class="stamp small ghost" type="button">复制全部路径</button></div>
    <div id="file-list"></div>
  </section>
  <section class="tab-pane" id="pane-handoff" hidden>
    <div class="actions"><button id="handoff-copy-brief" class="stamp small" type="button">复制接手 Prompt</button></div>
    <div id="handoff-brief" class="md preview-pane"></div>
    <details><summary>完整版（命令/报错/片段）</summary>
      <div class="actions"><button id="handoff-copy-full" class="stamp small ghost" type="button">复制完整版</button></div>
      <div id="handoff-full" class="md preview-pane"></div>
    </details>
  </section>
```

- [ ] **Step 2: Rework initTask in app.js**

Replace the whole `initTask()` with the following. It reuses existing helpers (`api/el/clear/renderMd/enhanceSelect/fmtTime/busy/clipText/plainPreview/paintSummary/isRealTime`). Keep the old `task-merge` click-handler block verbatim (merge interaction unchanged) inside the new function. Form submit now sends only goal/status/owner/next; step text edits go through `#task-save-steps` (freeze); stable-key status changes post to the override endpoint immediately.

```js
  function initTask() {
    const id = document.body.dataset.taskId || '';
    const isNew = !id;
    const form = $('task-form');
    const stepsBox = $('task-steps');
    const rows = [];
    if (!isNew) $('task-project-field').hidden = true;
    const statusSel = $('task-status');
    [['doing', '进行中'], ['blocked', '受阻'], ['done', '已完成'], ['unknown', '未知']].forEach(function (p) { const o = el('option', '', p[1]); o.value = p[0]; statusSel.appendChild(o); });
    statusSel.value = 'doing';
    let detail = null;

    function stableKey(k) { return !/^(cmd|err|next|file):/.test(k); }
    function paintProgress() {
      const done = rows.filter(function (r) { return r.status.value === 'done' || r.status.value === 'skipped'; }).length;
      const box = $('step-progress'); if (box) box.textContent = '进度 ' + done + '/' + rows.length;
    }
    function addStep(step) {
      const row = el('div', 'step-row');
      const status = el('select'); status.setAttribute('aria-label', '步骤状态');
      [['pending', '待办'], ['doing', '进行中'], ['done', '完成'], ['blocked', '受阻'], ['skipped', '跳过']].forEach(function (p) { const o = el('option', '', p[1]); o.value = p[0]; status.appendChild(o); });
      status.value = step.Status || step.status || 'pending';
      const subject = el('input'); subject.type = 'text'; subject.value = step.Subject || step.subject || ''; subject.placeholder = '步骤内容';
      const remove = el('button', 'step-remove', '×'); remove.type = 'button'; remove.title = '删除步骤';
      const entry = { row: row, status: status, subject: subject, sourceKey: step.SourceKey || step.source_key || '' };
      remove.addEventListener('click', function () { row.remove(); const i = rows.indexOf(entry); if (i >= 0) rows.splice(i, 1); paintProgress(); });
      status.addEventListener('change', async function () {
        if (!isNew && entry.sourceKey && stableKey(entry.sourceKey)) {
          try { await api('/v1/tasks/step/status', { method: 'POST', body: JSON.stringify({ task_id: +id, source_key: entry.sourceKey, status: status.value }) }); }
          catch (e) { alert('状态保存失败：' + e.message); }
        } else if (!isNew && entry.sourceKey) {
          status.title = '反推步骤不能单独改状态，请用「保存步骤」接管';
        }
        paintProgress();
      });
      rows.push(entry); row.appendChild(status); row.appendChild(subject); row.appendChild(remove); stepsBox.appendChild(row);
      enhanceSelect(status); paintProgress();
    }
    function readSteps() {
      return rows.filter(function (r) { return r.subject.value.trim() !== ''; }).map(function (r) { return { subject: r.subject.value.trim(), status: r.status.value, source_key: r.sourceKey }; });
    }
    $('task-add-step').addEventListener('click', function () { addStep({ Status: 'pending' }); });
    $('task-save-steps').addEventListener('click', async function () {
      if (!confirm('保存步骤文本后，这条任务将不再由扫描自动同步。继续？')) return;
      await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify({ id: +id, steps: readSteps() }) });
      load();
    });

    const tabs = [].slice.call(document.querySelectorAll('#task-tabs .tab'));
    function showTab(name) {
      tabs.forEach(function (t) { t.classList.toggle('active', t.dataset.tab === name); });
      ['conversation', 'steps', 'files', 'handoff'].forEach(function (p) { $('pane-' + p).hidden = (p !== name); });
      const u = new URL(location.href); u.searchParams.set('tab', name); history.replaceState(null, '', u);
      if (name === 'files') loadFiles();
      if (name === 'handoff') loadHandoff();
    }
    tabs.forEach(function (t) { t.addEventListener('click', function () { showTab(t.dataset.tab); }); });

    async function loadFiles() {
      const box = $('file-list'); clear(box);
      try {
        const d = await api('/v1/task/files?task_id=' + enc(id));
        (d.files || []).forEach(function (f) {
          const row = el('div', 'src-row');
          row.appendChild(el('span', 'path', f.path));
          row.appendChild(el('span', 'when', fmtTime(f.last_ts) + ' · ' + f.count + ' 次 · ' + (f.agent || '')));
          box.appendChild(row);
        });
        if (!(d.files || []).length) box.appendChild(el('p', 'empty', '这场对话没有文件改动'));
        $('files-copy').onclick = function () { navigator.clipboard.writeText((d.files || []).map(function (f) { return f.path; }).join('\n')); };
      } catch (e) { box.appendChild(el('p', 'empty', '加载失败：' + e.message)); }
    }
    async function loadHandoff() {
      try {
        const b = await api('/v1/tasks/export?id=' + enc(id) + '&mode=brief');
        renderMd($('handoff-brief'), b.markdown || '');
        $('handoff-copy-brief').onclick = function () { navigator.clipboard.writeText(b.markdown || ''); };
        const f = await api('/v1/tasks/export?id=' + enc(id) + '&mode=full');
        renderMd($('handoff-full'), f.markdown || '');
        $('handoff-copy-full').onclick = function () { navigator.clipboard.writeText(f.markdown || ''); };
      } catch (e) { renderMd($('handoff-brief'), '加载失败：' + e.message); }
    }

    let convNext = 0, convSize = 0;
    async function loadConversation(reset) {
      const box = $('snippet-list');
      if (reset) { clear(box); convNext = 0; }
      try {
        const d = await api('/v1/task/conversation?task_id=' + enc(id) + '&before=' + convNext + '&limit=20');
        if (d.warning) { $('conv-warn').hidden = false; $('conv-warn').textContent = d.warning; } else { $('conv-warn').hidden = true; }
        convSize = d.file_size || convSize;
        const frag = document.createDocumentFragment();
        (d.messages || []).forEach(function (m) {
          const wrap = el('div', 'snippet');
          wrap.appendChild(el('div', 'meta', (m.Role || m.role) + ' · ' + fmtTime(m.Ts || m.ts)));
          const body = el('div', 'md'); renderMd(body, m.Text || m.text || ('[' + (m.Tool || m.tool || '') + ']'));
          if (m.Truncated || m.truncated) body.appendChild(el('span', 'meta', ' …源文件里更长'));
          wrap.appendChild(body); frag.appendChild(wrap);
        });
        if (reset || convNext === 0) box.appendChild(frag); else box.insertBefore(frag, box.firstChild);
        convNext = (d.source === 'store') ? convSize : (d.next || 0);
        $('conv-more').hidden = !((d.source === 'file' && d.next > 0) || (d.source === 'store' && convSize > 0));
      } catch (e) { box.appendChild(el('p', 'empty', '加载失败：' + e.message)); }
    }
    $('conv-more').addEventListener('click', function () { loadConversation(false); });

    async function load() {
      if (isNew) { addStep({ Status: 'pending' }); return; }
      detail = await api('/v1/tasks/item?id=' + enc(id));
      $('task-id').textContent = '#' + detail.ID;
      $('task-title').textContent = clipText(plainPreview(detail.Goal || '任务'), 28);
      $('task-crumb').textContent = '#' + detail.ID;
      $('task-goal').value = detail.Goal || '';
      statusSel.value = detail.Status || 'doing';
      $('task-owner').value = detail.OwnerAgent || '';
      $('task-next').value = detail.NextSteps || '';
      clear(stepsBox); rows.length = 0;
      (detail.Steps || []).forEach(addStep);
      if (!rows.length) addStep({ Status: 'pending' });
      const kindName = (detail.Kind || 'formal') === 'session' ? '对话任务' : '合并任务';
      $('task-meta').textContent = kindName + ' · 来源 ' + (detail.SourceAgent || '手动') + ' · 最近更新 ' + fmtTime(detail.UpdatedAt) + (isRealTime(detail.ArchivedAt) ? ' · 已归档' : '');
      paintSummary('task-ai-summary', 'task-ai-summary-text', detail);
      $('task-archive').textContent = isRealTime(detail.ArchivedAt) ? '恢复' : '归档';
      const sb = $('session-list'); clear(sb);
      (detail.Sessions || []).forEach(function (s) { const li = el('li', 'src-row'); li.appendChild(el('span', 'agent', s.Agent || '')); li.appendChild(el('span', 'path', s.Title || s.Path || '')); li.appendChild(el('span', 'when', fmtTime(s.UpdatedAt))); sb.appendChild(li); });
      if (!(detail.Sessions || []).length) sb.appendChild(el('li', 'empty', '没有关联会话'));
      loadConversation(true);
    }

    form.addEventListener('submit', function (e) {
      e.preventDefault();
      const btn = form.querySelector('button[type="submit"]');
      busy(btn, '保存', async function () {
        const payload = { goal: $('task-goal').value.trim(), status: statusSel.value, owner_agent: $('task-owner').value.trim(), next_steps: $('task-next').value };
        if (isNew) {
          payload.project_path = $('task-project').value.trim(); payload.steps = readSteps();
          const d = await api('/v1/tasks', { method: 'POST', body: JSON.stringify(payload) });
          location.href = '/t/' + d.id; return;
        }
        payload.id = +id;
        await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify(payload) });
        load();
      });
    });
    $('task-archive').addEventListener('click', async function () {
      await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify({ id: +id, archived: !isRealTime(detail.ArchivedAt) }) });
      load();
    });
    // 旧 initTask 的 task-merge 点击处理逻辑原样搬入此处（合并交互不变）。

    load();
    const want = new URLSearchParams(location.search).get('tab') || 'conversation';
    showTab(['conversation', 'steps', 'files', 'handoff'].indexOf(want) >= 0 ? want : 'conversation');
  }
```

- [ ] **Step 3: Add CSS**

Append to `internal/daemon/webui/app.css`:

```css
.tab-bar { display: flex; gap: 8px; margin: 18px 0 12px; flex-wrap: wrap; }
.tab { background: transparent; border: 1px solid var(--line, #2a3441); color: var(--muted, #9aa7b4);
  border-radius: 10px; padding: 8px 14px; cursor: pointer; font: inherit; }
.tab.active { color: var(--fg, #e6edf3); border-color: var(--accent, #38bdf8); background: rgba(56,189,248,.08); }
.tab-pane[hidden] { display: none; }
.warn { border: 1px solid #b7791f; background: rgba(183,121,31,.12); color: #f0c674;
  border-radius: 10px; padding: 8px 12px; margin-bottom: 10px; }
#step-progress { margin-bottom: 8px; }
.task-head { border-bottom: 1px solid var(--line, #2a3441); padding-bottom: 14px; }
```

- [ ] **Step 4: Build + browser verify**

Run: `go build -o agent-memory-app.exe -ldflags "-H windowsgui" ./cmd/agent-memory-app`, restart app, open a session task detail, click through four tabs, click 加载更早 on the 44MB session, change a stable step status, copy brief.
Expected: tabs switch + URL updates; 回溯 returns older messages; status change persists without freezing; brief <=30 lines.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/webui/
git commit -m "feat: 任务详情页任务头 + 四 tab（对话/进度/文件/交接）"
```

---

### Task 10: 文档同步 + 全量验收

**Files:**
- Modify: `docs/usage.md`, `docs/client-guide.md`, `README.md`

- [ ] **Step 1: Update docs**

usage.md: MCP 工具数 11→12、`task_sync`、`task_update` 分层语义与 `step_status`/`source_key`、schema_version 3、详情页四区。client-guide §11/§12 重写为四区图解。README 工具列表加 `task_sync`、11→12。

- [ ] **Step 2: Re-shoot detail screenshots**

Use the existing CDP shoot tooling to capture the new detail page (four tabs) into `docs/images/client/`, update client-guide image references.

- [ ] **Step 3: Full verification**

Run: `go test ./... -count=1 && go vet ./...`
Run cross-builds: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/agent-memory` (and darwin).
Read-only boundary: md5 agent `*.jsonl`/`*.md` before/after `scan` + `task_sync` + 回溯, expect no diff.

- [ ] **Step 4: Commit**

```bash
git add docs/ README.md
git commit -m "docs: 任务详情工作台与 MCP 完整更新同步文档"
```

---

## Self-Review notes

- Spec coverage: 对话优先/四区(Task 9)、真 Todo(Task 2)、覆盖层(Task 3/4)、回溯(Task 1/6)、文件聚合(Task 4/6)、交接极简(Task 7)、MCP 分层+source_key+task_sync(Task 8)、prune(Task 5)、文档(Task 10)。无遗漏。
- Type consistency: `SetStepOverride/PruneStaleOverrides/stepOverrides/applyOverrides/TaskFiles/TaskFileStat/SyncSession/ParseSessionFile/RenderTaskHandoffBrief/SyncTaskInput/StepStatus/SourceKey/Page/Message` 在各 Task 间签名一致。
- 已核对的现有符号：`Runner.adapters`、`resolveSessionProject(ctx,agent,slug,path,confidence,at)`、`ResolveProjectByPath`、`ListSessions(ctx,projectID,limit)`、`openStore/openTempStore/fakeQoder/write/newTestServer/webDo/webToken` 均存在且签名匹配。
- 执行时仍需现场确认的两处（不改变对外接口）：① `ListSessions` 是否按 `updated_at` 倒序（若不是，在 `latestSessionPath` 里自行取 max）；② serve 装配 Runner 的具体位置（grep `ingest.New(` 于 `internal/cli`/`internal/daemon`），在其后加 `SetRunner`。
