# 跨 Agent 任务接力 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans. 本项目明确禁止调用子 agent，所有任务必须在当前会话内 inline 执行；每个 Task 结束后先向用户汇报，确认后再提交。

**Goal:** 让任务从“会话摘要卡”升级为可跨会话持续更新、可由 MCP/桌面端编辑、能带步骤和对话片段交接的正式任务。

**Architecture:** 保留 `task_card` 作为会话摘要，新增 `task/task_step/task_snippet/task_session` 四张表。扫描把摘要卡同步成正式任务，WorkBuddy Todo 导入为步骤；MCP/HTTP/桌面端读写正式任务；Handoff 与导出 Prompt 从正式任务生成。

**Tech Stack:** Go 1.22、modernc SQLite、net/http、MCP JSON-RPC、WebView2 多页面 Web UI、原生 JavaScript。

---

## 文件结构

- `internal/domain/task.go`：正式任务、步骤、片段、会话关联的数据类型与状态常量。
- `internal/store/schema.go`：新增四张表及索引。
- `internal/store/task.go`：正式任务 CRUD、步骤、片段、归档、合并、关联。
- `internal/store/task_backfill.go`：旧 `task_card` 一次性回填。
- `internal/store/task_test.go`：Store 层测试。
- `internal/agent/adapter.go`：`SessionUnit` 增加步骤草稿。
- `internal/agent/workbuddy/session.go`：解析 `tasks/<session>/*.json`。
- `internal/agent/workbuddy/session_test.go`：Todo 解析测试。
- `internal/ingest/tasks.go`：会话同步为正式任务的编排。
- `internal/ingest/ingest.go`：在会话导入/未变分支调用同步。
- `internal/ingest/ingest_test.go`：同步、挂接、人工保护测试。
- `internal/compose/compose.go`：Handoff 使用正式任务，保留卡片兜底。
- `internal/compose/task_export.go`：交接 Prompt 渲染。
- `internal/compose/task_export_test.go`：导出内容测试。
- `internal/daemon/tasks.go`：`/v1/tasks` 处理器与 Handoff 刷新。
- `internal/daemon/server.go`：新路由。
- `internal/daemon/tasks_test.go`：HTTP 接口测试。
- `internal/daemon/mcp.go`：MCP 任务工具实现。
- `internal/mcp/tools.go`：工具 schema 与 ToolAPI 扩展。
- `internal/mcp/protocol_test.go`：MCP 工具测试与 stub 更新。
- `internal/daemon/webui.go`：任务页面路由与模板注册。
- `internal/daemon/webui/tasks.html`：任务列表/归档页。
- `internal/daemon/webui/task.html`：任务详情/新建页。
- `internal/daemon/webui/app.js`：任务列表、详情、步骤编辑、复制、合并。
- `internal/daemon/webui/app.css`：任务页与步骤样式。
- `internal/daemon/webui/project.html`：项目页任务区改为正式任务。
- `docs/usage.md`：使用说明与已知限制。

---

### Task 1: 领域类型与数据库表

**Files:**
- Create: `internal/domain/task.go`
- Modify: `internal/store/schema.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: 写失败测试，确认新表缺失**

在 `internal/store/store_test.go` 的 `TestOpen_CreatesAllSpecTables` 的 `want` 中加入：

```go
"task", "task_step", "task_snippet", "task_session",
```

- [ ] **Step 2: 运行测试确认失败**

Run:

```bash
go test ./internal/store -run TestOpen_CreatesAllSpecTables -v
```

Expected: FAIL，提示 `table "task" missing`。

- [ ] **Step 3: 新建领域类型**

创建 `internal/domain/task.go`：

```go
package domain

import "time"

type Task struct {
	ID               int64
	ProjectID        int64
	Goal             string
	Status           string
	OwnerAgent       string
	ClaimedAt        time.Time
	NextSteps        string
	Decisions        []string
	Files            []string
	Commands         []string
	Errors           []string
	OpenQuestions    string
	SourceAgent      string
	SourcePath       string
	ManualEditedAt   time.Time
	CompletedAt      time.Time
	ArchivedAt       time.Time
	MergedIntoTaskID int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
	Steps            []TaskStep
}

type TaskStep struct {
	ID          int64
	TaskID      int64
	Seq         int
	Subject     string
	Description string
	ActiveForm  string
	Status      string
	SourceKey   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type TaskSnippet struct {
	ID     int64
	TaskID int64
	Seq    int
	Role   string
	Kind   string
	Text   string
	Ts     time.Time
}

type TaskSession struct {
	TaskID    int64
	SessionID int64
	Role      string
	CreatedAt time.Time
	Agent     string
	Title     string
	Path      string
	UpdatedAt time.Time
}

const (
	StepPending = "pending"
	StepDoing   = "doing"
	StepDone    = "done"
	StepBlocked = "blocked"
	StepSkipped = "skipped"
)
```

- [ ] **Step 4: 在 schema 中新增表**

在 `internal/store/schema.go` 的 `schemaSQL` 末尾、索引之前加入：

```sql
CREATE TABLE IF NOT EXISTS task (
  id                  INTEGER PRIMARY KEY,
  project_id          INTEGER NOT NULL DEFAULT 0,
  goal                TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'doing',
  owner_agent         TEXT NOT NULL DEFAULT '',
  claimed_at          INTEGER NOT NULL DEFAULT 0,
  next_steps          TEXT NOT NULL DEFAULT '',
  decisions_json      TEXT NOT NULL DEFAULT '[]',
  files_json          TEXT NOT NULL DEFAULT '[]',
  commands_json       TEXT NOT NULL DEFAULT '[]',
  errors_json         TEXT NOT NULL DEFAULT '[]',
  open_questions      TEXT NOT NULL DEFAULT '',
  source_agent        TEXT NOT NULL DEFAULT '',
  source_path         TEXT NOT NULL DEFAULT '',
  manual_edited_at    INTEGER NOT NULL DEFAULT 0,
  completed_at        INTEGER NOT NULL DEFAULT 0,
  archived_at         INTEGER NOT NULL DEFAULT 0,
  merged_into_task_id INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS task_step (
  id          INTEGER PRIMARY KEY,
  task_id     INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  subject     TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  active_form TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending',
  source_key  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE(task_id, seq)
);

CREATE TABLE IF NOT EXISTS task_snippet (
  id      INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq     INTEGER NOT NULL,
  role    TEXT NOT NULL DEFAULT '',
  kind    TEXT NOT NULL DEFAULT '',
  text    TEXT NOT NULL DEFAULT '',
  ts      INTEGER NOT NULL DEFAULT 0,
  UNIQUE(task_id, seq)
);

CREATE TABLE IF NOT EXISTS task_session (
  task_id    INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  session_id INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'source',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(task_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_task_project ON task(project_id, archived_at, status);
CREATE INDEX IF NOT EXISTS idx_task_owner ON task(owner_agent, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_step_source ON task_step(task_id, source_key) WHERE source_key <> '';
CREATE INDEX IF NOT EXISTS idx_task_snippet_task ON task_snippet(task_id, seq);
```

- [ ] **Step 5: 运行测试确认通过**

Run:

```bash
go test ./internal/store -run TestOpen_CreatesAllSpecTables -v
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

向用户汇报“新增任务表与领域类型”。经确认后：

```bash
git add internal/domain/task.go internal/store/schema.go internal/store/store_test.go
git commit -m "feat: 增加正式任务数据模型"
```

---

### Task 2: 正式任务 CRUD 与步骤

**Files:**
- Create: `internal/store/task.go`
- Create: `internal/store/task_test.go`

- [ ] **Step 1: 写失败的 Store 测试**

创建 `internal/store/task_test.go`：

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestCreateAndGetTaskWithSteps(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/demo", "demo", "", `F:\demo`, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "实现导出", Status: domain.StatusDoing,
		OwnerAgent: "qoder", NextSteps: "先写接口",
		SourceAgent: "qoder", SourcePath: `C:\sessions\a.jsonl`,
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone, SourceKey: "1"},
			{Subject: "补测试", Status: domain.StepDoing, SourceKey: "2"},
		},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.TaskByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("TaskByID ok=%v err=%v", ok, err)
	}
	if got.Goal != "实现导出" || got.OwnerAgent != "qoder" || len(got.Steps) != 2 {
		t.Fatalf("task = %+v", got)
	}
	if got.Steps[1].Status != domain.StepDoing || got.Steps[1].Seq != 2 {
		t.Fatalf("steps = %+v", got.Steps)
	}
}

func TestUpdateTaskReplacesStepsAndSetsManualEdit(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	id, err := s.CreateTask(ctx, domain.Task{
		Goal: "旧目标", Status: domain.StatusDoing,
		Steps: []domain.TaskStep{{Subject: "旧步骤", Status: domain.StepPending}},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	goal := "新目标"
	status := domain.StatusDone
	steps := []domain.TaskStep{
		{Subject: "新步骤一", Status: domain.StepDone},
		{Subject: "新步骤二", Status: domain.StepDone},
	}
	updated, err := s.UpdateTask(ctx, TaskUpdate{
		ID: id, Goal: &goal, Status: &status, Steps: &steps,
	}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Goal != goal || updated.Status != status {
		t.Fatalf("updated = %+v", updated)
	}
	if len(updated.Steps) != 2 || updated.Steps[0].Subject != "新步骤一" {
		t.Fatalf("steps = %+v", updated.Steps)
	}
	if updated.ManualEditedAt.IsZero() || updated.CompletedAt.IsZero() {
		t.Fatalf("manual/completed not set: %+v", updated)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run:

```bash
go test ./internal/store -run 'TestCreateAndGetTaskWithSteps|TestUpdateTaskReplacesStepsAndSetsManualEdit' -v
```

Expected: FAIL，`CreateTask`/`TaskUpdate` 未定义。

- [ ] **Step 3: 实现 Store 类型与创建/读取**

创建 `internal/store/task.go`，先实现以下内容：

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

type TaskFilter struct {
	ProjectID       int64
	Status          string
	IncludeArchived bool
	Limit           int
	Offset          int
}

type TaskUpdate struct {
	ID         int64
	Goal       *string
	Status     *string
	NextSteps  *string
	OwnerAgent *string
	Archived   *bool
	Steps      *[]domain.TaskStep
}

func (s *Store) CreateTask(ctx context.Context, task domain.Task) (int64, error) {
	if task.Goal == "" {
		return 0, errors.New("task goal is empty")
	}
	if task.Status == "" {
		task.Status = domain.StatusDoing
	}
	if task.Status == domain.StatusDone && task.CompletedAt.IsZero() {
		task.CompletedAt = task.UpdatedAt
	}
	if task.Status != domain.StatusDone {
		task.CompletedAt = time.Time{}
	}
	if task.OwnerAgent != "" && task.ClaimedAt.IsZero() {
		task.ClaimedAt = task.UpdatedAt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
INSERT INTO task(project_id, goal, status, owner_agent, claimed_at, next_steps,
  decisions_json, files_json, commands_json, errors_json, open_questions,
  source_agent, source_path, manual_edited_at, completed_at, archived_at,
  created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, '[]', '[]', '[]', '[]', '', ?, ?, ?, ?, 0, ?, ?)`,
		task.ProjectID, task.Goal, task.Status, task.OwnerAgent, unixOrZero(task.ClaimedAt),
		task.NextSteps, task.SourceAgent, task.SourcePath,
		unixOrZero(task.ManualEditedAt), unixOrZero(task.CompletedAt),
		unixOrZero(task.CreatedAt), unixOrZero(task.UpdatedAt))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertSteps(ctx, tx, id, task.Steps); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func insertSteps(ctx context.Context, tx *sql.Tx, taskID int64, steps []domain.TaskStep) error {
	if len(steps) > 100 {
		return fmt.Errorf("task has too many steps: %d", len(steps))
	}
	for i, step := range steps {
		if strings.TrimSpace(step.Subject) == "" {
			return fmt.Errorf("task step %d has empty subject", i+1)
		}
		status := step.Status
		if status == "" {
			status = domain.StepPending
		}
		if !validStepStatus(status) {
			return fmt.Errorf("invalid step status %q", status)
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO task_step(task_id, seq, subject, description, active_form, status, source_key, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, i+1, step.Subject, step.Description, step.ActiveForm, status,
			step.SourceKey, unixOrZero(step.CreatedAt), unixOrZero(step.UpdatedAt))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) TaskByID(ctx context.Context, id int64) (domain.Task, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, goal, status, owner_agent, claimed_at, next_steps,
  decisions_json, files_json, commands_json, errors_json, open_questions,
  source_agent, source_path, manual_edited_at, completed_at, archived_at,
  merged_into_task_id, created_at, updated_at
FROM task WHERE id = ?`, id)
	task, err := scanTaskRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, false, nil
	}
	if err != nil {
		return domain.Task{}, false, err
	}
	if task.Steps, err = s.taskSteps(ctx, id); err != nil {
		return domain.Task{}, false, err
	}
	return task, true, nil
}

func scanTaskRow(row *sql.Row) (domain.Task, error) {
	var (
		task                           domain.Task
		decisions, files, cmds, errs   string
		claimed, manual, completed     int64
		archived, created, updated     int64
	)
	err := row.Scan(&task.ID, &task.ProjectID, &task.Goal, &task.Status, &task.OwnerAgent,
		&claimed, &task.NextSteps, &decisions, &files, &cmds, &errs, &task.OpenQuestions,
		&task.SourceAgent, &task.SourcePath, &manual, &completed, &archived,
		&task.MergedIntoTaskID, &created, &updated)
	if err != nil {
		return domain.Task{}, err
	}
	task.Decisions, task.Files, task.Commands, task.Errors, err = decodeLists(decisions, files, cmds, errs)
	if err != nil {
		return domain.Task{}, err
	}
	task.ClaimedAt = timeFromUnix(claimed)
	task.ManualEditedAt = timeFromUnix(manual)
	task.CompletedAt = timeFromUnix(completed)
	task.ArchivedAt = timeFromUnix(archived)
	task.CreatedAt = timeFromUnix(created)
	task.UpdatedAt = timeFromUnix(updated)
	return task, nil
}

func (s *Store) taskSteps(ctx context.Context, taskID int64) ([]domain.TaskStep, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, seq, subject, description, active_form, status, source_key, created_at, updated_at
FROM task_step WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskStep
	for rows.Next() {
		var (
			step              domain.TaskStep
			created, updated  int64
		)
		if err := rows.Scan(&step.ID, &step.TaskID, &step.Seq, &step.Subject, &step.Description,
			&step.ActiveForm, &step.Status, &step.SourceKey, &created, &updated); err != nil {
			return nil, err
		}
		step.CreatedAt = timeFromUnix(created)
		step.UpdatedAt = timeFromUnix(updated)
		out = append(out, step)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 实现列表与更新**

继续在 `internal/store/task.go` 添加：

```go
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]domain.Task, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 20
	}
	q := `SELECT id FROM task WHERE 1 = 1`
	args := []any{}
	if f.ProjectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if !f.IncludeArchived {
		q += ` AND archived_at = 0`
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]domain.Task, 0, len(ids))
	for _, id := range ids {
		task, ok, err := s.TaskByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, task)
		}
	}
	return out, nil
}

func (s *Store) UpdateTask(ctx context.Context, in TaskUpdate, at time.Time) (domain.Task, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	now := at.Unix()
	if in.Goal != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE task SET goal = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.Goal, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.NextSteps != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE task SET next_steps = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.NextSteps, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.OwnerAgent != nil {
		claimed := now
		if *in.OwnerAgent == "" {
			claimed = 0
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task SET owner_agent = ?, claimed_at = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.OwnerAgent, claimed, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.Status != nil {
		completed := int64(0)
		if *in.Status == domain.StatusDone {
			completed = now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task SET status = ?, completed_at = ?, archived_at = 0, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.Status, completed, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.Archived != nil {
		if *in.Archived {
			if _, err := tx.ExecContext(ctx, `UPDATE task SET archived_at = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
				now, now, now, in.ID); err != nil {
				return domain.Task{}, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE task SET archived_at = 0, completed_at = CASE WHEN status = 'done' THEN ? ELSE 0 END, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
				now, now, now, in.ID); err != nil {
				return domain.Task{}, err
			}
		}
	}
	if in.Steps != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_step WHERE task_id = ?`, in.ID); err != nil {
			return domain.Task{}, err
		}
		if err := insertSteps(ctx, tx, in.ID, *in.Steps); err != nil {
			return domain.Task{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task SET manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET manual_edited_at = ?, updated_at = ? WHERE id = ?`,
		now, now, in.ID); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	task, ok, err := s.TaskByID(ctx, in.ID)
	if err != nil || !ok {
		return domain.Task{}, fmt.Errorf("reload task %d: %w", in.ID, err)
	}
	return task, nil
}

func validTaskStatus(s string) bool {
	switch s {
	case domain.StatusDoing, domain.StatusDone, domain.StatusBlocked, domain.StatusUnknown:
		return true
	}
	return false
}

func validStepStatus(s string) bool {
	switch s {
	case domain.StepPending, domain.StepDoing, domain.StepDone, domain.StepBlocked, domain.StepSkipped:
		return true
	}
	return false
}
```

在 `CreateTask` 开头与 `UpdateTask` 的 `in.Status != nil` 分支使用：

```go
if !validTaskStatus(task.Status) {
	return 0, fmt.Errorf("invalid task status %q", task.Status)
}
```

```go
if in.Status != nil && !validTaskStatus(*in.Status) {
	return domain.Task{}, fmt.Errorf("invalid task status %q", *in.Status)
}
```

- [ ] **Step 5: 运行测试确认通过**

Run:

```bash
go test ./internal/store -run 'Task' -v
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经用户确认后：

```bash
git add internal/store/task.go internal/store/task_test.go
git commit -m "feat: 增加正式任务仓储"
```

---

### Task 3: 会话关联、片段、归档与合并

**Files:**
- Modify: `internal/store/task.go`
- Test: `internal/store/task_test.go`

- [ ] **Step 1: 写失败测试**

在 `internal/store/task_test.go` 增加：

```go
func TestTaskSessionAndSnippets(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/demo", "demo", "", `F:\demo`, now)
	sessID, err := s.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: "sess-1", ProjectID: pid, Title: "实现导出",
		Path: `C:\sessions\1.jsonl`, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceEvents(ctx, sessID, []domain.SessionEvent{
		{SessionID: sessID, Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "实现导出", Ts: now},
		{SessionID: sessID, Seq: 2, Role: "assistant", Kind: domain.KindToolCommand, Text: "go test ./...", Ts: now.Add(time.Minute)},
		{SessionID: sessID, Seq: 3, Role: "assistant", Kind: domain.KindAssistant, Text: "测试通过", Ts: now.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	taskID, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Goal: "实现导出", Status: domain.StatusDoing, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkTaskSession(ctx, taskID, sessID, "source", now); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.TaskBySession(ctx, sessID); err != nil || !ok || got.ID != taskID {
		t.Fatalf("TaskBySession ok=%v err=%v got=%+v", ok, err, got)
	}
	if err := s.RefreshTaskSnippets(ctx, taskID, now); err != nil {
		t.Fatal(err)
	}
	detail, ok, err := s.TaskDetail(ctx, taskID)
	if err != nil || !ok {
		t.Fatalf("TaskDetail ok=%v err=%v", ok, err)
	}
	if len(detail.Snippets) != 3 || detail.Snippets[2].Text != "测试通过" {
		t.Fatalf("snippets = %+v", detail.Snippets)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].SessionID != sessID {
		t.Fatalf("sessions = %+v", detail.Sessions)
	}
}

func TestArchiveAndMergeTasks(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	old := now.Add(-31 * 24 * time.Hour)
	target, _ := s.CreateTask(ctx, domain.Task{Goal: "目标任务", Status: domain.StatusDoing, CreatedAt: now, UpdatedAt: now})
	source, _ := s.CreateTask(ctx, domain.Task{
		Goal: "重复任务", Status: domain.StatusDone, CompletedAt: old, CreatedAt: old, UpdatedAt: old,
		Steps: []domain.TaskStep{{Subject: "步骤A", Status: domain.StepDone}},
	})
	if n, err := s.ArchiveCompletedTasks(ctx, now.Add(-30*24*time.Hour), now); err != nil || n != 1 {
		t.Fatalf("archive n=%d err=%v", n, err)
	}
	got, _, _ := s.TaskByID(ctx, source)
	if got.ArchivedAt.IsZero() {
		t.Fatal("source should be archived")
	}
	if err := s.MergeTasks(ctx, source, target, now); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.TaskByID(ctx, target)
	if len(got.Steps) != 1 || got.Steps[0].Subject != "步骤A" {
		t.Fatalf("merged steps = %+v", got.Steps)
	}
	src, _, _ := s.TaskByID(ctx, source)
	if src.MergedIntoTaskID != target {
		t.Fatalf("merged_into = %d", src.MergedIntoTaskID)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run:

```bash
go test ./internal/store -run 'TestTaskSessionAndSnippets|TestArchiveAndMergeTasks' -v
```

Expected: FAIL，方法未定义。

- [ ] **Step 3: 实现关联、详情与片段**

在 `internal/store/task.go` 添加：

```go
type TaskDetail struct {
	Task
	Snippets []domain.TaskSnippet
	Sessions []domain.TaskSession
}

func (s *Store) LinkTaskSession(ctx context.Context, taskID, sessionID int64, role string, at time.Time) error {
	if role == "" {
		role = "continuation"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO task_session(task_id, session_id, role, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(task_id, session_id) DO NOTHING`, taskID, sessionID, role, at.Unix())
	return err
}

func (s *Store) TaskBySession(ctx context.Context, sessionID int64) (domain.Task, bool, error) {
	var taskID int64
	err := s.db.QueryRowContext(ctx, `SELECT task_id FROM task_session WHERE session_id = ? ORDER BY created_at LIMIT 1`, sessionID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, false, nil
	}
	if err != nil {
		return domain.Task{}, false, err
	}
	return s.TaskByID(ctx, taskID)
}

func (s *Store) FindAttachableTask(ctx context.Context, projectID int64, agent string) (domain.Task, bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM task
WHERE project_id = ? AND archived_at = 0 AND status IN ('doing','blocked') AND owner_agent = ?
ORDER BY updated_at DESC, id DESC LIMIT 2`, projectID, agent)
	if err != nil {
		return domain.Task{}, false, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return domain.Task{}, false, err
		}
		ids = append(ids, id)
	}
	if len(ids) != 1 {
		return domain.Task{}, false, rows.Err()
	}
	return s.TaskByID(ctx, ids[0])
}

func (s *Store) TaskDetail(ctx context.Context, id int64) (TaskDetail, bool, error) {
	task, ok, err := s.TaskByID(ctx, id)
	if err != nil || !ok {
		return TaskDetail{}, ok, err
	}
	detail := TaskDetail{Task: task}
	detail.Snippets, err = s.taskSnippets(ctx, id)
	if err != nil {
		return TaskDetail{}, false, err
	}
	detail.Sessions, err = s.taskSessions(ctx, id)
	return detail, true, err
}

func (s *Store) taskSnippets(ctx context.Context, taskID int64) ([]domain.TaskSnippet, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, seq, role, kind, text, ts FROM task_snippet
WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskSnippet
	for rows.Next() {
		var item domain.TaskSnippet
		var ts int64
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Seq, &item.Role, &item.Kind, &item.Text, &ts); err != nil {
			return nil, err
		}
		item.Ts = timeFromUnix(ts)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) taskSessions(ctx context.Context, taskID int64) ([]domain.TaskSession, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT ts.task_id, ts.session_id, ts.role, ts.created_at, s.agent, s.title, s.path, s.updated_at
FROM task_session ts JOIN session s ON s.id = ts.session_id
WHERE ts.task_id = ? ORDER BY ts.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskSession
	for rows.Next() {
		var item domain.TaskSession
		var created, updated int64
		if err := rows.Scan(&item.TaskID, &item.SessionID, &item.Role, &created, &item.Agent, &item.Title, &item.Path, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt = timeFromUnix(created)
		item.UpdatedAt = timeFromUnix(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) RefreshTaskSnippets(ctx context.Context, taskID int64, at time.Time) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.role, e.kind, e.text, e.files_json, e.ts
FROM session_event e JOIN task_session ts ON ts.session_id = e.session_id
WHERE ts.task_id = ? AND e.kind IN ('user_text','assistant_text','tool_command','tool_error','tool_edit')
ORDER BY e.ts DESC, e.id DESC LIMIT 20`, taskID)
	if err != nil {
		return err
	}
	var snippets []domain.TaskSnippet
	for rows.Next() {
		var item domain.TaskSnippet
		var files string
		var tsInt int64
		if err := rows.Scan(&item.Role, &item.Kind, &item.Text, &files, &tsInt); err != nil {
			rows.Close()
			return err
		}
		item.Ts = timeFromUnix(tsInt)
		if item.Kind == "tool_edit" {
			var list []string
			if json.Unmarshal([]byte(files), &list) == nil && len(list) > 0 {
				item.Text = "改动: " + strings.Join(list, ", ")
			}
		}
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		snippets = append(snippets, item)
	}
	rows.Close()
	// 倒序取最近的，入库前翻回时间正序。
	for i, j := 0, len(snippets)-1; i < j; i, j = i+1, j-1 {
		snippets[i], snippets[j] = snippets[j], snippets[i]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_snippet WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for i, item := range snippets {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_snippet(task_id, seq, role, kind, text, ts) VALUES(?, ?, ?, ?, ?, ?)`,
			taskID, i+1, item.Role, item.Kind, item.Text, unixOrZero(item.Ts)); err != nil {
			return err
		}
	}
	return tx.Commit()
}
```

- [ ] **Step 4: 实现归档与合并**

继续添加：

```go
func (s *Store) ArchiveCompletedTasks(ctx context.Context, before, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE task SET archived_at = ?, updated_at = ?
WHERE archived_at = 0 AND status = 'done' AND completed_at > 0 AND completed_at <= ?`,
		at.Unix(), at.Unix(), before.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) MergeTasks(ctx context.Context, sourceID, targetID int64, at time.Time) error {
	if sourceID == targetID {
		return errors.New("cannot merge a task into itself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetArchived int64
	if err := tx.QueryRowContext(ctx, `SELECT archived_at FROM task WHERE id = ?`, targetID).Scan(&targetArchived); err != nil {
		return err
	}
	if targetArchived != 0 {
		return errors.New("target task is archived")
	}

	seen := map[string]bool{}
	count := 0
	nextSeq := 1
	rows, err := tx.QueryContext(ctx, `SELECT seq, source_key, subject FROM task_step WHERE task_id = ? ORDER BY seq`, targetID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var seq int
		var sourceKey, subject string
		if err := rows.Scan(&seq, &sourceKey, &subject); err != nil {
			rows.Close()
			return err
		}
		count++
		if seq >= nextSeq {
			nextSeq = seq + 1
		}
		key := sourceKey
		if key == "" {
			key = subject
		}
		seen[key] = true
	}
	rows.Close()

	rows, err = tx.QueryContext(ctx, `SELECT subject, description, active_form, status, source_key FROM task_step WHERE task_id = ? ORDER BY seq`, sourceID)
	if err != nil {
		return err
	}
	var merged []domain.TaskStep
	for rows.Next() && count < 100 {
		var step domain.TaskStep
		if err := rows.Scan(&step.Subject, &step.Description, &step.ActiveForm, &step.Status, &step.SourceKey); err != nil {
			rows.Close()
			return err
		}
		key := step.SourceKey
		if key == "" {
			key = step.Subject
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, step)
		count++
	}
	rows.Close()
	for i, step := range merged {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_step(task_id, seq, subject, description, active_form, status, source_key, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			targetID, nextSeq+i, step.Subject, step.Description, step.ActiveForm, step.Status,
			step.SourceKey, at.Unix(), at.Unix()); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_session(task_id, session_id, role, created_at)
SELECT ?, session_id, 'continuation', ? FROM task_session WHERE task_id = ?`, targetID, at.Unix(), sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_session WHERE task_id = ?`, sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET archived_at = ?, merged_into_task_id = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
		at.Unix(), targetID, at.Unix(), at.Unix(), sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET manual_edited_at = ?, updated_at = ? WHERE id = ?`,
		at.Unix(), at.Unix(), targetID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RefreshTaskSnippets(ctx, targetID, at)
}
```

- [ ] **Step 5: 运行测试并修复编译问题**

Run:

```bash
go test ./internal/store -run 'Task' -v
```

Expected: PASS。若 `strings`/`json` 未导入，补到 import。

- [ ] **Step 6: 汇报并提交**

经用户确认后提交。

---

### Task 4: 旧任务卡回填

**Files:**
- Create: `internal/store/task_backfill.go`
- Modify: `internal/store/store.go`
- Test: `internal/store/task_test.go`

- [ ] **Step 1: 写失败测试**

在 `internal/store/task_test.go` 增加：

```go
func TestBackfillTasksFromCards(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/demo", "demo", "", `F:\demo`, now)
	sessID, _ := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "s1", ProjectID: pid, Title: "旧会话", UpdatedAt: now})
	cardID, err := s.UpsertTaskCard(ctx, domain.TaskCard{
		SessionID: sessID, ProjectID: pid, Goal: "旧目标", Status: domain.StatusDoing,
		NextSteps: "旧下一步", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Open 时已经在空库上写过回填标记；测试里补一张卡后要清掉标记再回填。
	if _, err := s.db.Exec(`DELETE FROM meta WHERE key = 'task_backfill_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillTasks(ctx); err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.TaskByID(ctx, cardID)
	if err != nil || !ok {
		t.Fatalf("backfill task ok=%v err=%v", ok, err)
	}
	if task.Goal != "旧目标" || task.SourceAgent != "qoder" {
		t.Fatalf("task = %+v", task)
	}
	linked, ok, err := s.TaskBySession(ctx, sessID)
	if err != nil || !ok || linked.ID != cardID {
		t.Fatalf("session link ok=%v err=%v got=%+v", ok, err, linked)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/store -run TestBackfillTasksFromCards -v`

Expected: FAIL，`BackfillTasks` 未定义。

- [ ] **Step 3: 实现回填**

创建 `internal/store/task_backfill.go`：

```go
package store

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) BackfillTasks(ctx context.Context) error {
	var done string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'task_backfill_v1'`).Scan(&done)
	if err == nil && done == "1" {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.project_id, c.goal, c.status, c.next_steps,
       c.decisions_json, c.files_json, c.commands_json, c.errors_json, c.open_questions,
       c.created_at, c.updated_at, s.id, s.agent, s.path
FROM task_card c JOIN session s ON s.id = c.session_id
WHERE NOT EXISTS (SELECT 1 FROM task t WHERE t.id = c.id)`)
	if err != nil {
		return err
	}
	type row struct {
		cardID, projectID, sessionID int64
		goal, status, next             string
		decisions, files, commands     string
		errorsJSON, questions          string
		agent, path                    string
		created, updated               int64
	}
	var items []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.cardID, &r.projectID, &r.goal, &r.status, &r.next,
			&r.decisions, &r.files, &r.commands, &r.errorsJSON, &r.questions,
			&r.created, &r.updated, &r.sessionID, &r.agent, &r.path); err != nil {
			rows.Close()
			return err
		}
		items = append(items, r)
	}
	rows.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range items {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task(id, project_id, goal, status, next_steps, decisions_json, files_json,
  commands_json, errors_json, open_questions, source_agent, source_path, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.cardID, r.projectID, r.goal, r.status, r.next,
			r.decisions, r.files, r.commands, r.errorsJSON, r.questions,
			r.agent, r.path, r.created, r.updated); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_session(task_id, session_id, role, created_at) VALUES(?, ?, 'source', ?)`,
			r.cardID, r.sessionID, r.created); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('task_backfill_v1','1')
ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, r := range items {
		if err := s.RefreshTaskSnippets(ctx, r.cardID, time.Unix(r.updated, 0)); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: 在 Open 中调用回填**

修改 `internal/store/store.go` 中 `schemaSQL` 执行之后：

```go
	st := &Store{db: db, path: path}
	if err := st.BackfillTasks(context.Background()); err != nil {
		return fail(err)
	}
```

并在文件 import 加入 `context`，最后返回 `st` 而不是 `&Store{...}`。

- [ ] **Step 5: 运行测试**

Run:

```bash
go test ./internal/store -v
```

Expected: PASS，包括旧表和回填测试。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 5: WorkBuddy Todo 解析为步骤

**Files:**
- Modify: `internal/agent/adapter.go`
- Modify: `internal/agent/workbuddy/session.go`
- Modify: `internal/agent/workbuddy/adapter.go`
- Test: `internal/agent/workbuddy/session_test.go`

- [ ] **Step 1: 写失败测试**

在 `session_test.go` 增加：

```go
func TestScanSessionsImportsTodoSteps(t *testing.T) {
	cwd := t.TempDir()
	home, id := buildSessionHome(t, cwd)
	writeFile(t, filepath.Join(home, "tasks", id, "2.json"),
		`{"subject":"补测试","description":"覆盖导出","activeForm":"正在补测试","status":"in_progress"}`)
	writeFile(t, filepath.Join(home, "tasks", id, "1.json"),
		`{"subject":"写接口","status":"completed"}`)

	units, err := New(home).ScanSessionUnits(context.Background())
	if err != nil || len(units) != 1 {
		t.Fatalf("scan: %v %+v", err, units)
	}
	steps := units[0].Steps
	if len(steps) != 2 {
		t.Fatalf("steps = %+v", steps)
	}
	if steps[0].SourceKey != "1" || steps[0].Subject != "写接口" || steps[0].Status != "done" {
		t.Fatalf("step 0 = %+v", steps[0])
	}
	if steps[1].SourceKey != "2" || steps[1].Status != "doing" {
		t.Fatalf("step 1 = %+v", steps[1])
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/agent/workbuddy -run TestScanSessionsImportsTodoSteps -v`

Expected: FAIL，`Steps` 字段不存在。

- [ ] **Step 3: 增加步骤草稿类型**

修改 `internal/agent/adapter.go`，在 `SessionUnit` 增加：

```go
	Steps []TaskStepDraft
```

并新增：

```go
type TaskStepDraft struct {
	SourceKey   string
	Subject     string
	Description string
	ActiveForm  string
	Status      string
}
```

- [ ] **Step 4: 解析 tasks 目录**

在 `internal/agent/workbuddy/session.go` 增加：

```go
func readTaskSteps(home, sessionID string) []agent.TaskStepDraft {
	dir := filepath.Join(home, "tasks", sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		key  int
		file string
	}
	var files []item
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		files = append(files, item{key: n, file: filepath.Join(dir, e.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].key < files[j].key })
	var out []agent.TaskStepDraft
	for _, f := range files {
		raw, err := os.ReadFile(f.file)
		if err != nil {
			continue
		}
		var payload struct {
			Subject     string `json:"subject"`
			Description string `json:"description"`
			ActiveForm  string `json:"activeForm"`
			Status      string `json:"status"`
		}
		if json.Unmarshal(raw, &payload) != nil || strings.TrimSpace(payload.Subject) == "" {
			continue
		}
		status := domain.StepPending
		switch payload.Status {
		case "in_progress":
			status = domain.StepDoing
		case "completed":
			status = domain.StepDone
		}
		out = append(out, agent.TaskStepDraft{
			SourceKey: strings.TrimSuffix(filepath.Base(f.file), ".json"),
			Subject: payload.Subject, Description: payload.Description,
			ActiveForm: payload.ActiveForm, Status: status,
		})
		if len(out) == 100 {
			break
		}
	}
	return out
}
```

在 `scanSessions` 的 `parseSessionFile` 之后、缓存前：

```go
			steps := readTaskSteps(a.Home, externalID)
```

把 `steps` 放入 `SessionUnit` 和缓存；`sessionCacheEntry` 增加 `steps []agent.TaskStepDraft`，缓存命中时一并返回。

- [ ] **Step 5: 运行测试**

Run:

```bash
go test ./internal/agent/workbuddy -v
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 6: 扫描同步正式任务

**Files:**
- Create: `internal/ingest/tasks.go`
- Modify: `internal/ingest/ingest.go`
- Test: `internal/ingest/ingest_test.go`

- [ ] **Step 1: 写失败测试**

在 `internal/ingest/ingest_test.go` 增加一个静态适配器与测试：

```go
type taskAdapter struct{ units []agent.SessionUnit }
func (a taskAdapter) Name() string { return "workbuddy" }
func (a taskAdapter) ScanMemoryUnits(context.Context) ([]agent.MemoryUnit, error) { return nil, nil }
func (a taskAdapter) ScanSessionUnits(context.Context) ([]agent.SessionUnit, error) { return a.units, nil }

func TestRun_CreatesFormalTaskAndWorkBuddySteps(t *testing.T) {
	s := openTempStore(t)
	repo := t.TempDir()
	now := time.Now()
	u := agent.SessionUnit{
		Path: filepath.Join(t.TempDir(), "s.jsonl"),
		Session: domain.Session{
			Agent: "workbuddy", ExternalID: "s1", Title: "实现导出",
			ProjectPath: repo, Confidence: "high", UpdatedAt: now,
		},
		Steps: []agent.TaskStepDraft{
			{SourceKey: "1", Subject: "写接口", Status: domain.StepDone},
			{SourceKey: "2", Subject: "补测试", Status: domain.StepDoing},
		},
	}
	r := New(s, taskAdapter{units: []agent.SessionUnit{u}})
	r.SettleIdle = time.Nanosecond
	if _, err := r.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListTasks(context.Background(), store.TaskFilter{Limit: 10})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	if tasks[0].Goal != "实现导出" || len(tasks[0].Steps) != 2 {
		t.Fatalf("task = %+v", tasks[0])
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/ingest -run TestRun_CreatesFormalTaskAndWorkBuddySteps -v`

Expected: FAIL，没有正式任务。

- [ ] **Step 3: 实现同步函数**

创建 `internal/ingest/tasks.go`：

```go
package ingest

import (
	"context"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func (r *Runner) syncTaskFromSession(ctx context.Context, agentName string, sess domain.Session, sessID int64, card *domain.TaskCard, steps []agent.TaskStepDraft, at time.Time) error {
	task, ok, err := r.store.TaskBySession(ctx, sessID)
	if err != nil {
		return err
	}
	if !ok {
		candidate, found, err := r.store.FindAttachableTask(ctx, sess.ProjectID, agentName)
		if err != nil {
			return err
		}
		if found {
			task = candidate
			ok = true
		}
	}
	if !ok {
		if card == nil && len(steps) == 0 {
			return nil
		}
		goal := sess.Title
		if card != nil && card.Goal != "" {
			goal = card.Goal
		}
		if goal == "" {
			return nil
		}
		status := domain.StatusDoing
		if card != nil {
			status = card.Status
		} else {
			status = deriveTaskStatus(steps)
		}
		taskID, err := r.store.CreateTask(ctx, domain.Task{
			ProjectID: sess.ProjectID, Goal: goal, Status: status,
			SourceAgent: agentName, SourcePath: sess.Path,
			CreatedAt: at, UpdatedAt: at,
			Steps: toDomainSteps(steps, at),
		})
		if err != nil {
			return err
		}
		if err := r.store.LinkTaskSession(ctx, taskID, sessID, "source", at); err != nil {
			return err
		}
		return r.store.RefreshTaskSnippets(ctx, taskID, at)
	}
	if err := r.store.LinkTaskSession(ctx, task.ID, sessID, "continuation", at); err != nil {
		return err
	}
	if task.ManualEditedAt.IsZero() && (card != nil || len(steps) > 0) {
		updated := task
		if card != nil {
			updated.Goal = card.Goal
			updated.Status = card.Status
			updated.NextSteps = card.NextSteps
			updated.Decisions = card.Decisions
			updated.Files = card.Files
			updated.Commands = card.Commands
			updated.Errors = card.Errors
			updated.OpenQuestions = card.OpenQuestions
		}
		updated.Steps = toDomainSteps(steps, at)
		if err := r.store.UpdateTaskFromSource(ctx, updated, at); err != nil {
			return err
		}
	}
	return r.store.RefreshTaskSnippets(ctx, task.ID, at)
}

func deriveTaskStatus(steps []agent.TaskStepDraft) string {
	if len(steps) == 0 {
		return domain.StatusDoing
	}
	allDone := true
	for _, step := range steps {
		if step.Status == domain.StepBlocked {
			return domain.StatusBlocked
		}
		if step.Status != domain.StepDone && step.Status != domain.StepSkipped {
			allDone = false
		}
	}
	if allDone {
		return domain.StatusDone
	}
	return domain.StatusDoing
}

func toDomainSteps(steps []agent.TaskStepDraft, at time.Time) []domain.TaskStep {
	out := make([]domain.TaskStep, 0, len(steps))
	for _, step := range steps {
		out = append(out, domain.TaskStep{
			Subject: step.Subject, Description: step.Description,
			ActiveForm: step.ActiveForm, Status: step.Status,
			SourceKey: step.SourceKey, CreatedAt: at, UpdatedAt: at,
		})
	}
	return out
}
```

- [ ] **Step 4: 增加 Store 的自动同步更新**

在 `internal/store/task.go` 增加：

```go
func (s *Store) UpdateTaskFromSource(ctx context.Context, task domain.Task, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	decisions, _ := marshalList(task.Decisions)
	files, _ := marshalList(task.Files)
	commands, _ := marshalList(task.Commands)
	errorsJSON, _ := marshalList(task.Errors)
	if _, err := tx.ExecContext(ctx, `
UPDATE task SET goal = ?, status = ?, next_steps = ?, decisions_json = ?, files_json = ?,
  commands_json = ?, errors_json = ?, open_questions = ?,
  completed_at = CASE
    WHEN ? = 'done' AND completed_at = 0 THEN ?
    WHEN ? <> 'done' THEN 0
    ELSE completed_at
  END,
  archived_at = CASE WHEN ? <> 'done' THEN 0 ELSE archived_at END,
  updated_at = ?
WHERE id = ? AND manual_edited_at = 0`,
		task.Goal, task.Status, task.NextSteps, decisions, files, commands, errorsJSON,
		task.OpenQuestions, task.Status, at.Unix(), task.Status, task.Status,
		at.Unix(), task.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_step WHERE task_id = ?`, task.ID); err != nil {
		return err
	}
	if err := insertSteps(ctx, tx, task.ID, task.Steps); err != nil {
		return err
	}
	return tx.Commit()
}
```

- [ ] **Step 5: 在 ingest 两个分支调用**

在 `internal/ingest/ingest.go`：

- 会话未变分支，`ensureTaskCard` 之后读取 `u.Session` 已有卡：调用 `r.syncTaskFromSession(ctx, a.Name(), sess, sessID, card, u.Steps, time.Now())`。没有卡时传 `nil`。
- 会话导入分支，在 `ReplaceEvents`、卡片生成之后调用同一函数。
- 同步错误追加到 `res.Errors`，不中断整轮。
- 在 `Runner.Run` 的适配器循环结束后、`MarkStaleForRun` 之前调用：

```go
now := time.Now()
if _, err := r.store.ArchiveCompletedTasks(ctx, now.Add(-30*24*time.Hour), now); err != nil {
	res.Errors = append(res.Errors, fmt.Sprintf("task archive: %v", err))
}
```

- [ ] **Step 6: 运行测试**

Run:

```bash
go test ./internal/ingest -run 'TestRun_CreatesFormalTaskAndWorkBuddySteps' -v
go test ./internal/ingest -v
```

Expected: PASS。

- [ ] **Step 7: 汇报并提交**

经确认后提交。

---

### Task 7: Handoff 使用正式任务

**Files:**
- Modify: `internal/compose/compose.go`
- Modify: `internal/compose/compose_test.go`

- [ ] **Step 1: 写失败测试**

增加：

```go
func TestRenderIncludesFormalTaskSteps(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1700000000, 0)
	_, err := f.st.CreateTask(f.ctx, domain.Task{
		ProjectID: f.projectID, Goal: "实现导出", Status: domain.StatusDoing,
		OwnerAgent: "qoder", NextSteps: "补测试",
		CreatedAt: now, UpdatedAt: now,
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone},
			{Subject: "补测试", Status: domain.StepDoing},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(f.st, Config{}).Render(f.ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## 进行中的任务", "实现导出", "1/2", "补测试", "owner: qoder"} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("handoff missing %q:\n%s", want, res.Body)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/compose -run TestRenderIncludesFormalTaskSteps -v`

Expected: FAIL，简报没有正式任务区。

- [ ] **Step 3: 增加任务渲染**

在 `compose.go` 增加：

```go
func renderTasks(tasks []domain.Task, level detailLevel) string {
	var b strings.Builder
	for i, task := range tasks {
		done := 0
		current := ""
		for _, step := range task.Steps {
			if step.Status == domain.StepDone || step.Status == domain.StepSkipped {
				done++
			}
			if current == "" && (step.Status == domain.StepDoing || step.Status == domain.StepPending) {
				current = step.Subject
			}
		}
		fmt.Fprintf(&b, "### %d. %s\n", i+1, oneLine(task.Goal))
		fmt.Fprintf(&b, "- 状态: %s ｜ owner: %s ｜ 步骤: %d/%d", task.Status, task.OwnerAgent, done, len(task.Steps))
		if current != "" {
			fmt.Fprintf(&b, " ｜ 当前: %s", oneLine(current))
		}
		b.WriteString("\n")
		if task.NextSteps != "" {
			fmt.Fprintf(&b, "- 下一步: %s\n", oneLine(task.NextSteps))
		}
		if level == cardDetailFull && len(task.Files) > 0 {
			fmt.Fprintf(&b, "- 改动文件: %s\n", strings.Join(head(task.Files, 8), ", "))
		}
		if len(task.Commands) > 0 {
			fmt.Fprintf(&b, "- 执行命令: %s\n", strings.Join(head(task.Commands, 4), " && "))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
```

在 `Render` 中读取任务：

```go
	tasks, err := c.st.ListTasks(ctx, store.TaskFilter{ProjectID: projectID, Limit: 5})
	if err != nil {
		return Result{}, err
	}
	doneTasks, err := c.st.ListTasks(ctx, store.TaskFilter{ProjectID: projectID, Status: domain.StatusDone, Limit: c.cfg.RecentCards})
	if err != nil {
		return Result{}, err
	}
```

把 `overview` 的任务数改为 `len(tasks)`；把 `attempts` 中的任务卡段替换为：

```go
	taskFull := section("## 进行中的任务", renderTasks(tasks, cardDetailFull))
	taskLean := section("## 进行中的任务", renderTasks(tasks, cardDetailLean))
```

预算降级时依次使用 `taskFull`、`taskLean`、前 2 条、前 1 条、空；没有正式任务时回退现有 `renderCards`/`renderQuestions`。

- [ ] **Step 4: 更新现有 compose 测试**

现有测试若断言“最近任务”或任务卡字段，改为断言“进行中的任务”或加兼容回退。保留卡片兜底测试。

- [ ] **Step 5: 运行测试**

Run:

```bash
go test ./internal/compose -v
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 8: 交接 Prompt 导出

**Files:**
- Create: `internal/compose/task_export.go`
- Test: `internal/compose/task_export_test.go`

- [ ] **Step 1: 写失败测试**

```go
package compose

import (
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestRenderTaskExport(t *testing.T) {
	task := domain.Task{
		ID: 42, Goal: "实现导出", Status: domain.StatusDoing, OwnerAgent: "qoder",
		NextSteps: "补测试", Files: []string{"app/Export.php"},
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone},
			{Subject: "补测试", Status: domain.StepDoing},
		},
		Snippets: []domain.TaskSnippet{{Role: "assistant", Kind: "assistant_text", Text: "测试通过", Ts: time.Now()}},
	}
	project := domain.Project{Key: "e:/demo", DisplayName: "demo"}
	got := RenderTaskExport(task, project)
	for _, want := range []string{
		"task_id: 42", "实现导出", "[x] 写接口", "[~] 补测试",
		"补测试", "app/Export.php", "task_get", "task_update", "不要新建任务",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("export missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/compose -run TestRenderTaskExport -v`

Expected: FAIL，函数未定义。

- [ ] **Step 3: 实现导出渲染**

创建 `internal/compose/task_export.go`：

```go
package compose

import (
	"fmt"
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
)

func RenderTaskExport(task domain.Task, project domain.Project) string {
	var b strings.Builder
	b.WriteString("# 接手任务\n\n")
	fmt.Fprintf(&b, "- task_id: %d\n", task.ID)
	fmt.Fprintf(&b, "- 项目: %s (`%s`)\n", project.DisplayName, project.Key)
	fmt.Fprintf(&b, "- 目标: %s\n", oneLine(task.Goal))
	fmt.Fprintf(&b, "- 状态: %s\n", task.Status)
	fmt.Fprintf(&b, "- owner: %s\n\n", task.OwnerAgent)
	b.WriteString("## 步骤\n\n")
	for _, step := range task.Steps {
		mark := "[ ]"
		switch step.Status {
		case domain.StepDone, domain.StepSkipped:
			mark = "[x]"
		case domain.StepDoing:
			mark = "[~]"
		case domain.StepBlocked:
			mark = "[!]"
		}
		fmt.Fprintf(&b, "- %s %s\n", mark, oneLine(step.Subject))
	}
	if task.NextSteps != "" {
		fmt.Fprintf(&b, "\n## 下一步\n\n%s\n", task.NextSteps)
	}
	if len(task.Files) > 0 {
		fmt.Fprintf(&b, "\n## 改动文件\n\n- %s\n", strings.Join(task.Files, "\n- "))
	}
	if len(task.Snippets) > 0 {
		b.WriteString("\n## 最近对话片段\n\n")
		start := len(task.Snippets) - 10
		if start < 0 {
			start = 0
		}
		for _, snippet := range task.Snippets[start:] {
			fmt.Fprintf(&b, "- [%s] %s\n", snippet.Role, oneLine(snippet.Text))
		}
	}
	b.WriteString("\n## 交接要求\n\n")
	b.WriteString("1. 先调用 `task_get` 读取最新任务。\n")
	b.WriteString("2. 用 `task_update` 更新同一个 task_id，不要新建任务。\n")
	b.WriteString("3. 完成后把状态改为 done，并在下一步里写清验证结果。\n")
	return b.String()
}
```

- [ ] **Step 4: 运行测试**

Run: `go test ./internal/compose -run TestRenderTaskExport -v`

Expected: PASS。

- [ ] **Step 5: 汇报并提交**

经确认后提交。

---

### Task 9: HTTP 任务接口与 Handoff 刷新

**Files:**
- Create: `internal/daemon/tasks.go`
- Modify: `internal/daemon/server.go`
- Test: `internal/daemon/tasks_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/daemon/tasks_test.go`：

```go
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTaskCreateUpdateAndExport(t *testing.T) {
	s, st := newTestServer(t)
	now := time.Now()
	pid, _ := st.EnsureProject(context.Background(), "f:/task-demo", "task-demo", "", t.TempDir(), now)
	_ = pid
	rec := webDo(t, s, http.MethodPost, "/v1/tasks",
		webToken,
		map[string]any{"project_path": "f:/task-demo", "goal": "实现导出", "steps": []map[string]any{{"subject": "写接口"}}},
		sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := int64(created["id"].(float64))
	rec = webDo(t, s, http.MethodGet, "/v1/tasks?project_key=f%3A%2Ftask-demo", webToken, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "实现导出") {
		t.Fatalf("list code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodPost, "/v1/tasks/update", webToken,
		map[string]any{"id": id, "status": "done", "owner_agent": "claude"}, sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("update code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodGet, "/v1/tasks/export?id="+fmt.Sprint(id), webToken, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "task_id") {
		t.Fatalf("export code=%d body=%s", rec.Code, rec.Body.String())
	}
}
```

需要 `sameOrigin` 在测试文件定义：

```go
func sameOrigin(r *http.Request) {
	r.Header.Set("Origin", "http://"+r.Host)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/daemon -run TestTaskCreateUpdateAndExport -v`

Expected: FAIL，路由 404。

- [ ] **Step 3: 增加路由**

在 `server.go` 注册：

```go
	s.mux.HandleFunc("GET /tasks", s.handleTasksPage)
	s.mux.HandleFunc("GET /t/new", s.handleTaskPage)
	s.mux.HandleFunc("GET /t/{id}", s.handleTaskPage)
	s.mux.HandleFunc("GET /v1/tasks", s.auth(s.handleListFormalTasks))
	s.mux.HandleFunc("GET /v1/tasks/item", s.auth(s.handleGetFormalTask))
	s.mux.HandleFunc("POST /v1/tasks", s.writeAuth(s.handleCreateTask))
	s.mux.HandleFunc("POST /v1/tasks/update", s.writeAuth(s.handleUpdateTask))
	s.mux.HandleFunc("POST /v1/tasks/merge", s.writeAuth(s.handleMergeTask))
	s.mux.HandleFunc("GET /v1/tasks/export", s.auth(s.handleExportTask))
```

- [ ] **Step 4: 实现处理器**

创建 `internal/daemon/tasks.go`，包含：

```go
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
	"github.com/muyi99/agent-memory/internal/surface"
)

func (s *Server) taskProjectID(ctx context.Context, path, agent string, at time.Time) (int64, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, fmt.Errorf("project_path is required")
	}
	if id, err := s.projectIDFromRequest(ctx, path, ""); err == nil {
		return id, nil
	}
	id, _, err := s.st.ResolveOrCreateProject(ctx, agent, "", path, at)
	return id, err
}

type taskStepRequest struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"active_form"`
	Status      string `json:"status"`
}

type taskCreateRequest struct {
	ProjectPath string            `json:"project_path"`
	Goal        string            `json:"goal"`
	Status      string            `json:"status"`
	NextSteps   string            `json:"next_steps"`
	OwnerAgent  string            `json:"owner_agent"`
	Steps       []taskStepRequest `json:"steps"`
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req taskCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	now := time.Now()
	project, err := s.taskProjectID(r.Context(), req.ProjectPath, "web", now)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := s.st.CreateTask(r.Context(), domain.Task{
		ProjectID: project, Goal: req.Goal, Status: req.Status,
		NextSteps: req.NextSteps, OwnerAgent: req.OwnerAgent,
		SourceAgent: "web", ManualEditedAt: now, CreatedAt: now, UpdatedAt: now,
		Steps: convertSteps(req.Steps, now),
	})
	if err != nil {
		httpError(w, err)
		return
	}
	s.refreshTaskHandoff(r.Context(), project)
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func convertSteps(in []taskStepRequest, at time.Time) []domain.TaskStep {
	out := make([]domain.TaskStep, 0, len(in))
	for _, step := range in {
		out = append(out, domain.TaskStep{
			Subject: step.Subject, Description: step.Description,
			ActiveForm: step.ActiveForm, Status: step.Status,
			CreatedAt: at, UpdatedAt: at,
		})
	}
	return out
}
```

type taskUpdateRequest struct {
	ID         int64             `json:"id"`
	Goal       *string           `json:"goal"`
	Status     *string           `json:"status"`
	NextSteps  *string           `json:"next_steps"`
	OwnerAgent *string           `json:"owner_agent"`
	Archived   *bool             `json:"archived"`
	Steps      *[]taskStepRequest `json:"steps"`
}

func (s *Server) handleListFormalTasks(w http.ResponseWriter, r *http.Request) {
	projectID := int64(0)
	if key := r.URL.Query().Get("project_key"); key != "" {
		id, err := s.projectIDFromRequest(r.Context(), key, key)
		if err != nil {
			httpError(w, err)
			return
		}
		projectID = id
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	includeArchived := r.URL.Query().Get("include_archived") == "1"
	tasks, err := s.st.ListTasks(r.Context(), store.TaskFilter{
		ProjectID: projectID, Status: r.URL.Query().Get("status"),
		IncludeArchived: includeArchived, Limit: limit,
	})
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "count": len(tasks)})
}

func (s *Server) handleGetFormalTask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "bad task id", http.StatusBadRequest)
		return
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
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var req taskUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if req.ID <= 0 {
		http.Error(w, "id must be positive", http.StatusBadRequest)
		return
	}
	var steps *[]domain.TaskStep
	if req.Steps != nil {
		converted := convertSteps(*req.Steps, time.Now())
		steps = &converted
	}
	task, err := s.st.UpdateTask(r.Context(), store.TaskUpdate{
		ID: req.ID, Goal: req.Goal, Status: req.Status, NextSteps: req.NextSteps,
		OwnerAgent: req.OwnerAgent, Archived: req.Archived, Steps: steps,
	}, time.Now())
	if err != nil {
		httpError(w, err)
		return
	}
	s.refreshTaskHandoff(r.Context(), task.ProjectID)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleMergeTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceID int64 `json:"source_id"`
		TargetID int64 `json:"target_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if err := s.st.MergeTasks(r.Context(), req.SourceID, req.TargetID, time.Now()); err != nil {
		httpError(w, err)
		return
	}
	s.refreshTaskHandoff(r.Context(), req.TargetID)
	writeJSON(w, http.StatusOK, map[string]any{"source_id": req.SourceID, "target_id": req.TargetID})
}

func (s *Server) handleExportTask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "bad task id", http.StatusBadRequest)
		return
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
	project, ok, err := s.st.ProjectByID(r.Context(), detail.ProjectID)
	if err != nil || !ok {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"markdown": compose.RenderTaskExport(detail.Task, project)})
}

`refreshTaskHandoff` 使用：

```go
func (s *Server) refreshTaskHandoff(ctx context.Context, projectID int64) {
	project, ok, err := s.st.ProjectByID(ctx, projectID)
	if err != nil || !ok {
		return
	}
	res, err := compose.New(s.st, compose.Config{
		BudgetTokens: s.cfg.BudgetTokens, RecentCards: s.cfg.RecentCards,
	}).Render(ctx, projectID)
	if err != nil {
		return
	}
	dirs, err := s.st.ListProjectDirs(ctx, projectID)
	if err != nil {
		return
	}
	for _, key := range dirs {
		dir := filepath.FromSlash(key)
		if surface.IsProtectedAgentDir(dir) {
			continue
		}
		_, _, _ = surface.WriteHandoff(dir, res.Body)
	}
	if len(dirs) == 0 && project.PrimaryPath != "" && !surface.IsProtectedAgentDir(project.PrimaryPath) {
		_, _, _ = surface.WriteHandoff(project.PrimaryPath, res.Body)
	}
}
```

补 import `path/filepath`。

- [ ] **Step 5: 运行测试**

Run:

```bash
go test ./internal/daemon -run TestTaskCreateUpdateAndExport -v
go test ./internal/daemon -v
```

Expected: PASS。

- [ ] **Step 6: 汇报并提交**

经确认后提交。

---

### Task 10: MCP 任务工具

**Files:**
- Modify: `internal/mcp/tools.go`
- Modify: `internal/daemon/mcp.go`
- Modify: `internal/mcp/protocol_test.go`
- Test: `internal/mcp/tools_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/mcp/tools_test.go`：

```go
package mcp

import (
	"context"
	"testing"
)

type taskStub struct{ stubAPI }
func (taskStub) AddTask(ctx context.Context, in AddTaskInput) (any, error) {
	return map[string]any{"id": 7}, nil
}
func (taskStub) UpdateTask(ctx context.Context, in UpdateTaskInput) (any, error) {
	return map[string]any{"id": in.ID, "status": "done"}, nil
}
func (taskStub) ExportTask(ctx context.Context, id int64) (any, error) {
	return map[string]any{"markdown": "task_id: 7"}, nil
}

func TestTaskTools(t *testing.T) {
	s := NewServer(taskStub{})
	out := call(t, s, 1, "tools/call", map[string]any{
		"name": "task_add", "arguments": map[string]any{"project_path": "f:/demo", "goal": "写测试"},
	})
	if out["error"] != nil {
		t.Fatalf("task_add error: %v", out["error"])
	}
	out = call(t, s, 2, "tools/call", map[string]any{
		"name": "task_update", "arguments": map[string]any{"id": 7, "status": "done"},
	})
	if out["error"] != nil {
		t.Fatalf("task_update error: %v", out["error"])
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/mcp -run TestTaskTools -v`

Expected: FAIL，未知工具。

- [ ] **Step 3: 扩展 ToolAPI 与 schema**

在 `tools.go` 增加输入类型：

```go
type AddTaskInput struct {
	ProjectPath string     `json:"project_path"`
	Goal        string     `json:"goal"`
	Status      string     `json:"status"`
	NextSteps   string     `json:"next_steps"`
	OwnerAgent  string     `json:"owner_agent"`
	Steps       []TaskStepInput `json:"steps"`
}

type UpdateTaskInput struct {
	ID         int64      `json:"id"`
	Goal       *string    `json:"goal"`
	Status     *string    `json:"status"`
	NextSteps  *string    `json:"next_steps"`
	OwnerAgent *string    `json:"owner_agent"`
	Archived   *bool      `json:"archived"`
	Steps      *[]TaskStepInput `json:"steps"`
}

type TaskStepInput struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"active_form"`
	Status      string `json:"status"`
}
```

并把现有 `TaskInput` 改为：

```go
type TaskInput struct {
	ProjectPath     string `json:"project_path"`
	Status          string `json:"status"`
	IncludeArchived bool   `json:"include_archived"`
	Limit           int    `json:"limit"`
}
```

`ToolAPI` 增加：

```go
	AddTask(ctx context.Context, in AddTaskInput) (any, error)
	UpdateTask(ctx context.Context, in UpdateTaskInput) (any, error)
	ExportTask(ctx context.Context, id int64) (any, error)
```

在 `toolDefinitions` 增加：

```go
{"task_add", "新建正式任务，可带步骤。",
	objectSchema(map[string]any{
		"project_path": map[string]any{"type": "string"},
		"goal":         map[string]any{"type": "string"},
		"status":       map[string]any{"type": "string", "enum": []string{"doing", "done", "blocked", "unknown"}},
		"next_steps":   map[string]any{"type": "string"},
		"owner_agent":  map[string]any{"type": "string"},
		"steps":        map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
	}, "project_path", "goal")},
{"task_update", "更新任务、认领、归档或整组替换步骤。",
	objectSchema(map[string]any{
		"id":          map[string]any{"type": "integer"},
		"goal":        map[string]any{"type": "string"},
		"status":      map[string]any{"type": "string", "enum": []string{"doing", "done", "blocked", "unknown"}},
		"next_steps":  map[string]any{"type": "string"},
		"owner_agent": map[string]any{"type": "string"},
		"archived":    map[string]any{"type": "boolean"},
		"steps":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
	}, "id")},
{"task_export", "导出交接 Prompt，带同一个 task_id。",
	objectSchema(map[string]any{
		"id": map[string]any{"type": "integer"},
	}, "id")},
```

在 `callTool` 增加 3 个 case；同时把已有 `Tasks`/`TaskGet` 改为正式任务：

```go
case "task_add":
	var in AddTaskInput
	if err := bind(p.Arguments, &in); err != nil { return nil, err }
	return s.api.AddTask(ctx, in)
case "task_update":
	var in UpdateTaskInput
	if err := bind(p.Arguments, &in); err != nil { return nil, err }
	return s.api.UpdateTask(ctx, in)
case "task_export":
	var in struct{ ID int64 `json:"id"` }
	if err := bind(p.Arguments, &in); err != nil { return nil, err }
	if in.ID <= 0 { return nil, fmt.Errorf("id must be a positive integer") }
	return s.api.ExportTask(ctx, in.ID)
```

- [ ] **Step 4: daemon 实现**

在 `internal/daemon/mcp.go` 实现：

```go
func (s *Server) AddTask(ctx context.Context, in mcp.AddTaskInput) (any, error) {
	now := time.Now()
	projectID, err := s.taskProjectID(ctx, in.ProjectPath, "mcp", now)
	if err != nil {
		return nil, err
	}
	steps := make([]domain.TaskStep, 0, len(in.Steps))
	for _, step := range in.Steps {
		steps = append(steps, domain.TaskStep{
			Subject: step.Subject, Description: step.Description,
			ActiveForm: step.ActiveForm, Status: step.Status,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	id, err := s.st.CreateTask(ctx, domain.Task{
		ProjectID: projectID, Goal: in.Goal, Status: in.Status,
		NextSteps: in.NextSteps, OwnerAgent: in.OwnerAgent,
		SourceAgent: "mcp", ManualEditedAt: now, CreatedAt: now, UpdatedAt: now, Steps: steps,
	})
	if err != nil {
		return nil, err
	}
	s.refreshTaskHandoff(ctx, projectID)
	return map[string]any{"id": id}, nil
}

func (s *Server) UpdateTask(ctx context.Context, in mcp.UpdateTaskInput) (any, error) {
	steps := (*[]domain.TaskStep)(nil)
	if in.Steps != nil {
		converted := make([]domain.TaskStep, 0, len(*in.Steps))
		for _, step := range *in.Steps {
			converted = append(converted, domain.TaskStep{
				Subject: step.Subject, Description: step.Description,
				ActiveForm: step.ActiveForm, Status: step.Status,
			})
		}
		steps = &converted
	}
	task, err := s.st.UpdateTask(ctx, store.TaskUpdate{
		ID: in.ID, Goal: in.Goal, Status: in.Status, NextSteps: in.NextSteps,
		OwnerAgent: in.OwnerAgent, Archived: in.Archived, Steps: steps,
	}, time.Now())
	if err != nil {
		return nil, err
	}
	s.refreshTaskHandoff(ctx, task.ProjectID)
	return task, nil
}

func (s *Server) ExportTask(ctx context.Context, id int64) (any, error) {
	detail, ok, err := s.st.TaskDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrTaskCardNotFound
	}
	project, ok, err := s.st.ProjectByID(ctx, detail.ProjectID)
	if err != nil || !ok {
		return nil, fmt.Errorf("project %d not found", detail.ProjectID)
	}
	return map[string]any{"markdown": compose.RenderTaskExport(detail.Task, project)}, nil
}
```

同时修改已有的 `Tasks` 和 `TaskGet`：

```go
func (s *Server) Tasks(ctx context.Context, in mcp.TaskInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}
	tasks, err := s.st.ListTasks(ctx, store.TaskFilter{
		ProjectID: projectID, Status: in.Status, IncludeArchived: in.IncludeArchived, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"tasks": tasks, "cards": tasks, "count": len(tasks)}, nil
}

func (s *Server) TaskGet(ctx context.Context, id int64) (any, error) {
	detail, ok, err := s.st.TaskDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrTaskCardNotFound
	}
	return detail, nil
}
```

- [ ] **Step 5: 更新 stub**

在 `internal/mcp/protocol_test.go` 给 `stubAPI` 增加三个方法，返回空结果。

- [ ] **Step 6: 运行测试**

Run:

```bash
go test ./internal/mcp ./internal/daemon -v
```

Expected: PASS。

- [ ] **Step 7: 汇报并提交**

经确认后提交。

---

### Task 11: 任务页面骨架与路由

**Files:**
- Create: `internal/daemon/webui/tasks.html`
- Create: `internal/daemon/webui/task.html`
- Modify: `internal/daemon/webui.go`
- Modify: `internal/daemon/webui/layout.html`
- Modify: `internal/daemon/webui/app.css`
- Test: `internal/daemon/webui_test.go`

- [ ] **Step 1: 写页面契约测试**

在 `webui_test.go` 增加页面路由断言（沿用现有 `TestWebUIPages` 风格）：

```go
func TestTaskPagesRenderShell(t *testing.T) {
	s, _ := newTestServer(t)
	for _, path := range []string{"/tasks", "/t/new", "/t/1"} {
		rec := webDo(t, s, http.MethodGet, path, "", nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s code=%d", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `id="token-gate"`) {
			t.Fatalf("%s missing shell", path)
		}
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/daemon -run TestTaskPagesRenderShell -v`

Expected: FAIL，404。

- [ ] **Step 3: 增加模板注册与 pageData**

修改 `webui.go`：

```go
for _, name := range []string{"home", "project", "memories", "sources", "edit", "tasks", "task"} {
```

`pageData` 增加：

```go
	TaskID string
```

增加：

```go
func (s *Server) handleTasksPage(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "tasks", pageData{Title: "任务", View: "tasks"})
}

func (s *Server) handleTaskPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.renderPage(w, "task", pageData{Title: "任务详情", View: "task", TaskID: id})
}
```

- [ ] **Step 4: 新建模板**

`tasks.html`：

```html
{{define "content"}}
<nav class="crumbs" aria-label="面包屑"><a href="/">总览</a><span class="sep">/</span><span>任务</span></nav>
<header class="page-head">
  <p class="kicker">TASKS</p>
  <h1 class="display-sm">任务</h1>
  <p class="lede">跨会话持续更新；换 agent 时从同一个任务继续。</p>
</header>
<section class="section">
  <div class="section-head">
    <h2>任务列表</h2>
    <span id="tasks-meta" class="meta"></span>
    <a class="more" href="/t/new">新建任务 <span aria-hidden="true">→</span></a>
  </div>
  <div class="row"><label class="check"><input id="tasks-archived" type="checkbox"> 显示归档</label></div>
  <div id="task-cards" class="cards"></div>
</section>
{{end}}
```

`task.html`：

```html
{{define "content"}}
<nav class="crumbs" aria-label="面包屑"><a href="/">总览</a><span class="sep">/</span><a href="/tasks">任务</a><span class="sep">/</span><span id="task-crumb">详情</span></nav>
<article class="sheet wide rise">
  <p class="kicker">TASK <span id="task-id" class="mono"></span></p>
  <h1 class="display-sm" id="task-title">任务</h1>
  <form id="task-form" class="stack">
    <label class="field" id="task-project-field"><span class="label">项目路径</span><input id="task-project" type="text" placeholder="例如 F:\go\demo"></label>
    <label class="field"><span class="label">目标</span><textarea id="task-goal" rows="2" required></textarea></label>
    <div class="row">
      <label class="field inline"><span class="label">状态</span><select id="task-status"></select></label>
      <label class="field inline"><span class="label">owner</span><input id="task-owner" type="text" placeholder="例如 qoder / claude"></label>
    </div>
    <label class="field"><span class="label">下一步</span><textarea id="task-next" rows="3"></textarea></label>
    <div class="field"><span class="label">步骤</span><div id="task-steps" class="step-list"></div>
      <button id="task-add-step" class="stamp small ghost" type="button">添加步骤</button></div>
    <p id="task-meta" class="meta"></p>
    <div class="actions">
      <button class="stamp primary" type="submit">保存</button>
      <button id="task-archive" class="stamp ghost" type="button">归档</button>
      <button id="task-merge" class="stamp ghost" type="button">合并到…</button>
      <button id="task-copy" class="stamp ghost" type="button">复制交接 Prompt</button>
      <a class="stamp ghost" href="/tasks">返回</a>
    </div>
  </form>
  <section class="section" id="task-snippets">
    <div class="section-head"><h2>最近对话</h2></div>
    <div id="snippet-list" class="md"></div>
  </section>
  <section class="section" id="task-sessions">
    <div class="section-head"><h2>来源会话</h2></div>
    <ul id="session-list" class="sources"></ul>
  </section>
</article>
{{end}}
```

- [ ] **Step 5: 导航与基础样式**

在 `layout.html` 主导航增加：

```html
<a href="/tasks"{{if eq .View "tasks"}} aria-current="page"{{end}}{{if eq .View "task"}} aria-current="page"{{end}}>任务</a>
```

并把 `<body>` 标签补上任务 ID：

```html
<body data-view="{{.View}}"{{if .Key}} data-key="{{.Key}}"{{end}}{{if .MemoryID}} data-memory-id="{{.MemoryID}}"{{end}}{{if .TaskID}} data-task-id="{{.TaskID}}"{{end}}>
```

在 `app.css` 增加：

```css
.field[hidden] { display: none; }
.step-list { display: grid; gap: 8px; }
.step-row { display: grid; grid-template-columns: 28px minmax(0, 1fr) 140px 32px; gap: 8px; align-items: center; }
.step-row input[type="text"], .step-row select { width: 100%; }
.step-remove { color: var(--danger); background: transparent; border: 0; cursor: pointer; }
.task-link { font-family: var(--display); font-size: 17px; color: var(--text); text-decoration: none; }
.task-link:hover { color: var(--accent); }
.snippet { border-bottom: 1px solid var(--stroke); padding: 8px 0; }
.snippet:last-child { border-bottom: 0; }
@media (max-width: 980px) { .step-row { grid-template-columns: 24px minmax(0, 1fr) 110px 28px; } }
```

- [ ] **Step 6: 运行测试**

Run:

```bash
go test ./internal/daemon -run 'TestTaskPagesRenderShell|TestWebUIStaticContract' -v
```

Expected: PASS。

- [ ] **Step 7: 汇报并提交**

经确认后提交。

---

### Task 12: 任务页面交互

**Files:**
- Modify: `internal/daemon/webui/app.js`
- Modify: `internal/daemon/webui/project.html`
- Modify: `internal/daemon/webui/app.css`

- [ ] **Step 1: 实现任务列表**

在 `app.js` 增加：

```js
  function taskProgress(t) {
    const steps = t.Steps || t.steps || [];
    let done = 0;
    steps.forEach((s) => { if (s.Status === 'done' || s.Status === 'skipped' || s.status === 'done' || s.status === 'skipped') done++; });
    return steps.length ? done + '/' + steps.length : '无步骤';
  }

  async function initTasks() {
    const list = $('task-cards');
    const params = new URLSearchParams(location.search);
    const archived = params.get('archived') === '1';
    const projectKey = params.get('project_key') || '';
    $('tasks-archived').checked = archived;
    async function load() {
      clear(list);
      list.appendChild(el('p', 'empty', '载入中…'));
      try {
        const query = ['limit=100'];
        if (projectKey) query.push('project_key=' + enc(projectKey));
        if (archived) query.push('include_archived=1');
        const d = await api('/v1/tasks?' + query.join('&'));
        const tasks = d.tasks || [];
        clear(list);
        tasks.forEach((t) => {
          const card = el('article', 'card');
          const head = el('div', 'card-head');
          head.appendChild(el('strong', '', t.Goal || '（无目标）'));
          head.appendChild(statusStamp(t.Status));
          head.appendChild(el('span', 'stamp tiny', (t.OwnerAgent || '未认领')));
          card.appendChild(head);
          card.appendChild(el('div', 'meta', '步骤 ' + taskProgress(t) + ' · ' + (t.SourceAgent || '手动')));
          const a = el('a', 'more', '打开任务 →');
          a.href = '/t/' + t.ID;
          card.appendChild(a);
          list.appendChild(card);
        });
        if (!tasks.length) list.appendChild(el('p', 'empty', '还没有任务。'));
        $('tasks-meta').textContent = '共 ' + tasks.length + ' 条';
      } catch (e) { clear(list); list.appendChild(el('p', 'empty', '加载失败：' + e.message)); }
    }
    $('tasks-archived').addEventListener('change', () => {
      const qs = new URLSearchParams(location.search);
      if ($('tasks-archived').checked) qs.set('archived', '1');
      else qs.delete('archived');
      location.search = qs.toString();
    });
    load();
  }
```

- [ ] **Step 2: 任务详情/步骤编辑**

增加 `initTask`：

```js
  function initTask() {
    const id = document.body.dataset.taskId || '';
    const isNew = !id;
    const form = $('task-form');
    const stepsBox = $('task-steps');
    const rows = [];
    const projectField = $('task-project-field');
    if (!isNew) projectField.hidden = true;
    const statusSel = $('task-status');
    [['doing', '进行中'], ['blocked', '受阻'], ['done', '已完成'], ['unknown', '未知']].forEach(function (pair) {
      const opt = el('option', '', pair[1]); opt.value = pair[0]; statusSel.appendChild(opt);
    });
    statusSel.value = 'doing';

    function addStep(step) {
      const row = el('div', 'step-row');
      const status = el('select');
      [['pending', '待办'], ['doing', '进行中'], ['done', '完成'], ['blocked', '受阻'], ['skipped', '跳过']].forEach(function (pair) {
        const opt = el('option', '', pair[1]); opt.value = pair[0]; status.appendChild(opt);
      });
      status.value = step.Status || 'pending';
      const subject = el('input');
      subject.type = 'text';
      subject.value = step.Subject || '';
      subject.placeholder = '步骤内容';
      const remove = el('button', 'step-remove', '×');
      remove.type = 'button';
      remove.title = '删除步骤';
      remove.addEventListener('click', function () { row.remove(); const i = rows.indexOf(entry); if (i >= 0) rows.splice(i, 1); });
      const entry = { row: row, status: status, subject: subject };
      rows.push(entry);
      row.appendChild(status); row.appendChild(subject); row.appendChild(remove);
      stepsBox.appendChild(row);
    }
    function readSteps() {
      return rows.filter(function (r) { return r.subject.value.trim() !== ''; }).map(function (r) {
        return { subject: r.subject.value.trim(), status: r.status.value };
      });
    }
    function isRealTime(v) { return v && new Date(v).getFullYear() > 1; }

    $('task-add-step').addEventListener('click', function () { addStep({ Status: 'pending' }); });

    async function load() {
      if (isNew) { addStep({ Status: 'pending' }); return; }
      const d = await api('/v1/tasks/item?id=' + enc(id));
      $('task-id').textContent = '#' + d.ID;
      $('task-title').textContent = d.Goal || '任务';
      $('task-crumb').textContent = '#' + d.ID;
      $('task-goal').value = d.Goal || '';
      statusSel.value = d.Status || 'doing';
      $('task-owner').value = d.OwnerAgent || '';
      $('task-next').value = d.NextSteps || '';
      (d.Steps || []).forEach(addStep);
      if (!rows.length) addStep({ Status: 'pending' });
      $('task-meta').textContent = '来源 ' + (d.SourceAgent || '手动') + ' · 最近更新 ' + fmtTime(d.UpdatedAt) + (isRealTime(d.ArchivedAt) ? ' · 已归档' : '');
      $('task-archive').textContent = isRealTime(d.ArchivedAt) ? '恢复' : '归档';
      const snippetBox = $('snippet-list');
      clear(snippetBox);
      (d.Snippets || []).forEach(function (s) {
        const wrap = el('div', 'snippet');
        wrap.appendChild(el('div', 'meta', s.Role + ' · ' + fmtTime(s.Ts)));
        const body = el('div', 'md');
        renderMd(body, s.Text || '');
        wrap.appendChild(body);
        snippetBox.appendChild(wrap);
      });
      if (!(d.Snippets || []).length) snippetBox.appendChild(el('p', 'empty', '还没有对话片段。'));
      const sessionBox = $('session-list');
      clear(sessionBox);
      (d.Sessions || []).forEach(function (s) {
        const li = el('li', 'src-row');
        li.appendChild(el('span', 'agent', s.agent || s.Agent || ''));
        li.appendChild(el('span', 'path', s.title || s.Title || ''));
        li.appendChild(el('span', 'when', fmtTime(s.updated_at || s.UpdatedAt)));
        sessionBox.appendChild(li);
      });
      if (!(d.Sessions || []).length) sessionBox.appendChild(el('li', 'empty', '没有关联会话。'));
    }

    form.addEventListener('submit', function (e) {
      e.preventDefault();
      const btn = form.querySelector('button[type="submit"]');
      busy(btn, '保存', async function () {
        const payload = {
          goal: $('task-goal').value.trim(),
          status: statusSel.value,
          owner_agent: $('task-owner').value.trim(),
          next_steps: $('task-next').value,
          steps: readSteps()
        };
        if (!payload.goal) { toast('error', '请先写目标'); return; }
        if (isNew) {
          payload.project_path = $('task-project').value.trim();
          if (!payload.project_path) { toast('error', '请填写项目路径'); return; }
          const d = await api('/v1/tasks', { method: 'POST', body: JSON.stringify(payload) });
          location.href = '/t/' + d.id;
          return;
        }
        payload.id = Number(id);
        await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify(payload) });
        toast('info', '已保存');
        location.reload();
      });
    });

    $('task-archive').addEventListener('click', function () {
      if (isNew) return;
      const btn = this;
      busy(btn, '处理', async function () {
        const d = await api('/v1/tasks/item?id=' + enc(id));
        const archived = isRealTime(d.ArchivedAt);
        await api('/v1/tasks/update', { method: 'POST', body: JSON.stringify({ id: Number(id), archived: !archived }) });
        toast('info', archived ? '已恢复' : '已归档');
        location.reload();
      });
    });

    $('task-copy').addEventListener('click', async function () {
      if (isNew) return;
      const d = await api('/v1/tasks/export?id=' + enc(id));
      const markdown = d.markdown || '';
      try {
        await navigator.clipboard.writeText(markdown);
        toast('info', '交接 Prompt 已复制');
      } catch (e) {
        window.prompt('复制失败，请手动复制', markdown);
      }
    });

    $('task-merge').addEventListener('click', function () {
      if (isNew) return;
      const target = window.prompt('输入要合并到的目标任务 ID');
      if (!target) return;
      const targetID = Number(target);
      if (!targetID || targetID === Number(id)) { toast('error', '目标 ID 无效'); return; }
      const btn = this;
      busy(btn, '合并', async function () {
        await api('/v1/tasks/merge', { method: 'POST', body: JSON.stringify({ source_id: Number(id), target_id: targetID }) });
        toast('info', '已合并');
        location.href = '/t/' + targetID;
      });
    });

    load().catch(function (e) { toast('error', '任务加载失败：' + e.message); });
  }
```

- [ ] **Step 3: 接入 init 分发**

```js
    else if (view === 'tasks') initTasks();
    else if (view === 'task') initTask();
```

- [ ] **Step 4: 项目页任务区改为正式任务**

在 `project.html` 把 04 区块的文案改为“任务”，保留 `task-list` 容器；在 `app.js` 把 `loadTasks` 替换为：

```js
  async function loadTasks(key) {
    const box = $('task-list');
    clear(box);
    try {
      const d = await api('/v1/tasks?project_key=' + enc(key) + '&limit=20');
      const tasks = d.tasks || [];
      tasks.forEach((t) => {
        const card = el('article', 'card');
        const head = el('div', 'card-head');
        const title = el('a', 'task-link', t.Goal || '（无目标）');
        title.href = '/t/' + t.ID;
        head.appendChild(title);
        head.appendChild(statusStamp(t.Status));
        head.appendChild(el('span', 'stamp tiny', t.OwnerAgent || '未认领'));
        card.appendChild(head);
        card.appendChild(el('div', 'meta', '步骤 ' + taskProgress(t) + ' · ' + (t.SourceAgent || '手动')));
        if (t.NextSteps) {
          const md = el('div', 'md md-clamp card-md');
          renderMd(md, t.NextSteps);
          card.appendChild(md);
        }
        box.appendChild(card);
      });
      if (!tasks.length) box.appendChild(el('p', 'empty', '还没有任务。'));
      $('task-meta').textContent = tasks.length ? '共 ' + tasks.length + ' 个任务' : '';
    } catch (e) {
      box.appendChild(el('p', 'empty', '任务加载失败：' + e.message));
    }
  }
```

项目页增加“查看全部任务”“已归档”链接：

```html
<a class="more" href="/tasks?project_key={{.Key}}">查看全部 <span aria-hidden="true">→</span></a>
```

（若模板里不能直接放 key，使用现有项目页的 JS 填充链接，保持和其他“查看全部”入口一致。）

- [ ] **Step 5: 运行 JS 语法与页面测试**

Run:

```bash
node --check internal/daemon/webui/app.js
go test ./internal/daemon -v
```

Expected: PASS。

- [ ] **Step 6: 桌面端手工验收**

构建并启动，实际走：

1. 打开 `/tasks` 新建任务。
2. 添加两个步骤，勾选一个。
3. 保存后刷新，确认状态与步骤保留。
4. 设置 owner，保存。
5. 复制交接 Prompt。
6. 归档后从列表消失，归档视图可见并恢复。
7. 新建第二个重复任务，合并到第一个任务，确认来源任务归档、步骤并入。
8. 打开项目页，确认任务区显示正式任务。

- [ ] **Step 7: 汇报并提交**

经确认后提交。

---

### Task 13: 文档、全量验收与收尾

**Files:**
- Modify: `docs/usage.md`
- Modify: `docs/superpowers/specs/2026-10-31-cross-agent-task-continuation-design.md`（若实现与设计有偏差，回写）

- [ ] **Step 1: 更新使用说明**

在 `docs/usage.md` 增加：

- 任务模型说明：会话摘要卡与正式任务的区别。
- 新建/认领/步骤/归档/合并/复制交接 Prompt 的操作路径。
- MCP 新增 3 个工具与 `task_list`/`task_get` 语义变化。
- 30 天归档规则与恢复行为。
- 旧库自动回填说明。

- [ ] **Step 2: 全量测试**

Run:

```bash
go test ./...
go vet ./...
node --check internal/daemon/webui/app.js
```

Expected: 全部 PASS。

- [ ] **Step 3: 真实数据验收**

- 停止桌面端。
- 备份 `~/.agent-memory/memory.db` 与 WAL。
- 启动新版本，确认旧任务卡回填任务。
- 扫描 WorkBuddy，确认 Todo 步骤出现。
- 创建一条手动任务，认领、改步骤、归档、恢复、合并。
- 检查项目 `HANDOFF.md` 含“进行中的任务”和步骤进度。
- 截图任务列表、任务详情、Handoff。

- [ ] **Step 4: 重建桌面端并重启**

```bash
go build -ldflags "-H windowsgui" -o agent-memory-app.exe ./cmd/agent-memory-app
```

关闭旧进程后启动新 exe，确认窗口、托盘、刷新正常。

- [ ] **Step 5: 汇报并提交**

向用户汇报验证结果和截图，经确认后提交文档与最终代码。

---

## 计划自检

- 覆盖 spec：数据模型（Task 1）、Store 与生命周期（Task 2/3）、回填（Task 4）、WorkBuddy Todo（Task 5）、扫描同步（Task 6）、Handoff（Task 7）、导出（Task 8）、HTTP（Task 9）、MCP（Task 10）、桌面端（Task 11/12）、文档与验收（Task 13）。
- 所有任务都有明确文件、测试、运行命令与预期结果。
- 类型名称在各任务中一致：`domain.Task`、`domain.TaskStep`、`domain.TaskSnippet`、`domain.TaskSession`、`store.TaskUpdate`、`store.TaskFilter`、`agent.TaskStepDraft`。
- 未引入子 agent 执行；每个提交点都要求先向用户汇报，遵守“代码不自动 commit”。
