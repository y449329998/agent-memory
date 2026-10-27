# M2 会话导入、任务卡与交接简报 · 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Qoder 的会话 JSONL 导入成任务卡，并按项目合成一份可直接读给下一个 agent 的交接简报文件。

**Architecture:** 沿用 M1 的分层。`agent/qoder` 新增会话解析（只解析，不写库）；`store` 新增会话与任务卡仓储；`extract` 是纯规则函数；`compose` 是纯函数 Store → markdown；`surface` 负责原子写文件。简报内容全部来自库里已有的记忆与任务卡，不重新读 agent 文件。

**Tech Stack:** Go 1.22.6、`modernc.org/sqlite`（M1 已引入）。无新增依赖。

**前置:** `docs/superpowers/plans/2026-10-27-agent-memory-m1-core-store.md` 全部完成。本计划里的 `store.Store`、`domain.*`、`normalize.*`、`ingest.Runner` 均指 M1 已存在的实现。

---

## 开工约定

- M1 的 schema 已经把 `session`、`session_event`、`task_card`、`handoff` 四张表建好了，本计划**不写迁移**。
- 会话文本一律截断到 500 rune 存 `session_event.text`；完整 transcript 不入库（spec 决策：记忆 + 任务卡，不做全文检索）。
- 时间戳判定「会话已结束」用源文件 mtime，不用库里的 `updated_at`：JSONL 可能被 agent 回写，mtime 才是「还在写」的真凭据。
- 每个 Task 结束时提交一次，提交信息用中文。
- 所有测试用 `t.TempDir()`，不读真实 `~/.qoder-cn`。

---

## 文件结构

```
internal/domain/types.go                        新增 Session / SessionEvent / TaskCard
internal/agent/qoder/session.go                 JSONL → SessionUnit（新建）
internal/agent/qoder/adapter.go                 新增 ScanSessionUnits（修改）
internal/agent/adapter.go                       Adapter 接口增加 ScanSessionUnits（修改）
internal/ingest/ingest.go                       增加会话分支（修改）
internal/store/session.go                       会话与事件仓储（新建）
internal/store/taskcard.go                      任务卡仓储（新建）
internal/store/handoff.go                       简报仓储（新建）
internal/extract/extract.go                     规则抽取（新建，纯函数）
internal/compose/compose.go                     简报合成（新建，纯函数）
internal/surface/handoff_file.go                写 HANDOFF.md + .git/info/exclude（新建）
internal/config/config.go                       新增 handoff.budget_tokens（修改）
internal/cli/handoff.go                         handoff 命令（新建）
internal/cli/doctor.go                          会话统计（修改）
```

`extract` 与 `compose` 都不碰文件系统与数据库，是本项目里最纯的两个包——规则改动只需要跑单测就能验证。

---

## Task 17: 会话数据结构与适配器接口扩展

- [ ] **Step 1: 在 `internal/domain/types.go` 追加**

```go
type Session struct {
	ID          int64
	Agent       string
	ExternalID  string
	ProjectID   int64
	Title       string
	StartedAt   time.Time
	UpdatedAt   time.Time
	EventCount  int
	Path        string
	ProjectPath string
	ProjectSlug string
	Confidence  string
}

type EventKind string

const (
	KindUserText     EventKind = "user_text"
	KindAssistant    EventKind = "assistant_text"
	KindToolEdit     EventKind = "tool_edit"
	KindToolCommand  EventKind = "tool_command"
	KindToolRead     EventKind = "tool_read"
	KindToolError    EventKind = "tool_error"
	KindWorkspace    EventKind = "workspace"
	KindOther        EventKind = "other"
)

type SessionEvent struct {
	ID        int64
	SessionID int64
	Seq       int
	Role      string
	Kind      EventKind
	Text      string
	Tool      string
	Files     []string
	Ts        time.Time
}

type TaskCard struct {
	ID            int64
	SessionID     int64
	ProjectID     int64
	Goal          string
	Status        string
	Decisions     []string
	Files         []string
	Commands      []string
	Errors        []string
	OpenQuestions string
	NextSteps     string
	Confidence    string
	GeneratedBy   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const (
	StatusDoing   = "doing"
	StatusDone    = "done"
	StatusBlocked = "blocked"
	StatusUnknown = "unknown"
)
```

- [ ] **Step 2: 在 `internal/agent/adapter.go` 里加会话类型并扩展接口**

```go
// SessionUnit 是一个会话源文件及其解析结果。
type SessionUnit struct {
	Path    string
	Content []byte
	MTime   time.Time
	Size    int64
	// Session 的 ProjectPath/ProjectSlug/Confidence 由适配器填；ProjectID 由 ingest 解析。
	Session domain.Session
	Events  []SessionEvent
	Err     error
}

// SessionEvent 是适配器产出的会话事件，与 domain.SessionEvent 同构但不带 SessionID：
// 库里的行主键由 store 决定，适配器不该关心。
type SessionEvent struct {
	Seq   int
	Role  string
	Kind  domain.EventKind
	Text  string
	Tool  string
	Files []string
	Ts    time.Time
}

type Adapter interface {
	Name() string
	ScanMemoryUnits(ctx context.Context) ([]MemoryUnit, error)
	ScanSessionUnits(ctx context.Context) ([]SessionUnit, error)
}
```

`MTime` 是 `Settled` 判定的唯一依据（Task 20），必须在这里就带上——事件时间推不出「文件是否还在被写」。

- [ ] **Step 3: 编译确认接口扩展逼出了未实现的方法**

Run: `go build ./... 2>&1 | head -5`
Expected: 编译失败，`*qoder.Adapter does not implement agent.Adapter (missing method ScanSessionUnits)`

这一步是刻意的：先扩展接口，让编译器列出所有需要跟进的实现点，不留静默的半成品。

- [ ] **Step 4: 在 `internal/agent/qoder/adapter.go` 加占位实现（Task 18 替换）**

```go
func (a *Adapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	return nil, nil
}
```

- [ ] **Step 5: 编译并跑 M1 全部测试**

Run: `go build ./... && go test ./...`
Expected: PASS（M1 测试不依赖会话）

- [ ] **Step 6: 提交**

```bash
git add internal/domain/types.go internal/agent/adapter.go internal/agent/qoder/adapter.go
git commit -m "feat: 会话数据结构与适配器会话扫描接口"
```

---

## Task 18: Qoder 会话 JSONL 解析

真实格式（本机 `~/.qoder-cn/projects/<slug>/<uuid>.jsonl` 抽样确认）：

- 首行 `{"type":"workspace-directories","sessionId":"...","directories":["F:\\go\\agent-memory"]}`
- `{"type":"runtime-config","sessionId":"...","model":"...","timestamp":1793089562320}` ← 数字毫秒
- `{"type":"user","uuid":"...","timestamp":"2026-10-27T08:26:02.995Z","message":{"role":"user","content":"..."},"cwd":"F:\\go\\agent-memory","isSidechain":false,"gitBranch":"HEAD"}` ← 字符串时间
- `message.content` 既可能是字符串，也可能是 block 数组（`text` / `tool_use` / `tool_result`）

两种时间格式必须都吃下，否则整场会话的时间线是空的。

**Files:**
- Create: `internal/agent/qoder/session.go`
- Modify: `internal/agent/qoder/adapter.go`
- Test: `internal/agent/qoder/session_test.go`

- [ ] **Step 1: 写失败测试 `internal/agent/qoder/session_test.go`**

```go
package qoder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

func line(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

type userMsg struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	} `json:"message"`
	IsSidechain bool `json:"isSidechain"`
	SessionID   string `json:"sessionId"`
}

func TestParseSession_SkipsSidechainAndSkillInjection(t *testing.T) {
	base := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	var sb string
	sb += line(t, map[string]any{"type": "workspace-directories", "sessionId": "s1", "directories": []string{`F:\demo`}})

	skill := userMsg{Type: "user", Timestamp: base.Add(time.Minute).Format(time.RFC3339), SessionID: "s1"}
	skill.Message.Role = "user"
	skill.Message.Content = "Base directory for this skill: C:\\skills\\brainstorming\n\n# Brainstorming\n lots of prose"
	sb += line(t, skill)

	cmd := userMsg{Type: "user", Timestamp: base.Add(2 * time.Minute).Format(time.RFC3339), SessionID: "s1"}
	cmd.Message.Role = "user"
	cmd.Message.Content = "<command-message>brainstorming</command-message>\n<command-name>/brainstorming</command-name>"
	sb += line(t, cmd)

	side := userMsg{Type: "user", Timestamp: base.Add(3 * time.Minute).Format(time.RFC3339), SessionID: "s1", IsSidechain: true}
	side.Message.Role = "user"
	side.Message.Content = "子 agent 的输出不应成为目标"
	sb += line(t, side)

	real := userMsg{Type: "user", Timestamp: base.Add(4 * time.Minute).Format(time.RFC3339), SessionID: "s1"}
	real.Message.Role = "user"
	real.Message.Content = "给扣减余额加乐观锁"
	sb += line(t, real)

	events := parseEvents(t, sb)
	var texts []string
	for _, e := range events {
		if e.Kind == domain.KindUserText {
			texts = append(texts, e.Text)
		}
	}
	if len(texts) != 1 || texts[0] != "给扣减余额加乐观锁" {
		t.Fatalf("sidechain/skill/command 应全部丢弃, got %q", texts)
	}
}

func TestParseSession_ToolBlocksBecomeStructuredEvents(t *testing.T) {
	base := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	var sb string
	sb += line(t, map[string]any{"type": "workspace-directories", "sessionId": "s2", "directories": []string{`F:\demo`}})
	sb += line(t, map[string]any{
		"type": "assistant", "sessionId": "s2", "timestamp": base.Format(time.RFC3339),
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "先改 Service 再补测试"},
			map[string]any{"type": "tool_use", "name": "Edit", "input": map[string]any{"file_path": `F:\demo\svc.go`}},
			map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "go test ./..."}},
			map[string]any{"type": "tool_use", "name": "Read", "input": map[string]any{"file_path": `F:\demo\svc.go`}},
		}},
	})
	sb += line(t, map[string]any{
		"type": "user", "sessionId": "s2", "timestamp": base.Add(time.Minute).Format(time.RFC3339),
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "is_error": true, "content": "undefined: decrementWithLock"},
		}},
	})

	events := parseEvents(t, sb)
	kinds := map[domain.EventKind]int{}
	for _, e := range events {
		kinds[e.Kind]++
	}
	if kinds[domain.KindToolEdit] != 1 || kinds[domain.KindToolCommand] != 1 || kinds[domain.KindToolError] != 1 {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, e := range events {
		if e.Kind == domain.KindToolRead {
			t.Fatalf("read 事件不应入库（对交接没用，只放大体积）: %+v", e)
		}
	}
	var edited []string
	for _, e := range events {
		if e.Kind == domain.KindToolEdit {
			edited = e.Files
		}
	}
	if len(edited) != 1 || edited[0] != `F:\demo\svc.go` {
		t.Fatalf("edit files = %v", edited)
	}
}

func TestParseSession_MixedTimestampFormats(t *testing.T) {
	var sb string
	sb += line(t, map[string]any{"type": "workspace-directories", "sessionId": "s3", "directories": []string{`F:\demo`}})
	sb += line(t, map[string]any{"type": "user", "sessionId": "s3", "timestamp": float64(1793089562320),
		"message": map[string]any{"role": "user", "content": "毫秒时间戳"}})
	sb += line(t, map[string]any{"type": "user", "sessionId": "s3", "timestamp": "2026-10-27T09:00:00.500Z",
		"message": map[string]any{"role": "user", "content": "RFC3339 时间戳"}})

	events := parseEvents(t, sb)
	if len(events) < 2 {
		t.Fatalf("want 2 events, got %d", len(events))
	}
	first := events[0]
	if first.Ts.IsZero() {
		t.Fatal("毫秒时间戳没解析出来")
	}
	if first.Ts.UTC().Format("2006-01-02") != "2026-10-28" && first.Ts.UTC().Format("2006-01-02") != "2026-10-27" {
		t.Fatalf("epoch ms parsed wrong: %v", first.Ts)
	}
	if events[1].Ts.UTC().Hour() != 9 {
		t.Fatalf("RFC3339 parsed wrong: %v", events[1].Ts)
	}
}

func TestParseSession_TruncatesLongText(t *testing.T) {
	var sb string
	sb += line(t, map[string]any{"type": "workspace-directories", "sessionId": "s4", "directories": []string{`F:\demo`}})
	long := make([]rune, 2000)
	for i := range long {
		long[i] = '锁'
	}
	sb += line(t, map[string]any{"type": "user", "sessionId": "s4", "timestamp": "2026-10-27T09:00:00Z",
		"message": map[string]any{"role": "user", "content": string(long)}})

	events := parseEvents(t, sb)
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if n := len([]rune(events[len(events)-1].Text)); n > 501 {
		t.Fatalf("text not truncated: %d runes", n)
	}
}

func TestParseSession_GarbageLinesSkippedNotFatal(t *testing.T) {
	sb := line(t, map[string]any{"type": "workspace-directories", "sessionId": "s5", "directories": []string{`F:\demo`}})
	sb += "{\"type\":\"user\",\"broken\n"
	sb += line(t, map[string]any{"type": "user", "sessionId": "s5", "timestamp": "2026-10-27T09:00:00Z",
		"message": map[string]any{"role": "user", "content": "坏行之后的正常事件"}})

	events := parseEvents(t, sb)
	if len(events) == 0 {
		t.Fatal("garbage line should not abort the file")
	}
}

func TestScanSessionUnits_EndToEndFile(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	repo := filepath.Join(root, "code", "demo")
	mustMkdir(t, repo)
	write(t, filepath.Join(home, "memory", "u.md"), "用户级\n")
	slug := "demo"
	write(t, filepath.Join(home, "projects", slug, "s1.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repo)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"加乐观锁\"}}\n")

	a := New(home)
	units, err := a.ScanSessionUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d units", len(units))
	}
	u := units[0]
	if u.Session.ExternalID != "s1" {
		t.Fatalf("external id = %q", u.Session.ExternalID)
	}
	if u.Session.Title != "加乐观锁" {
		t.Fatalf("title = %q", u.Session.Title)
	}
	if filepath.Clean(u.Session.ProjectPath) != filepath.Clean(repo) || u.Session.Confidence != "high" {
		t.Fatalf("project path/confidence = %q/%q", u.Session.ProjectPath, u.Session.Confidence)
	}
	if u.Session.EventCount != len(u.Events) || u.Session.EventCount == 0 {
		t.Fatalf("event count = %d, events = %d", u.Session.EventCount, len(u.Events))
	}
	if u.Session.StartedAt.After(u.Session.UpdatedAt) {
		t.Fatalf("started %v after updated %v", u.Session.StartedAt, u.Session.UpdatedAt)
	}
	if u.Size != int64(len(u.Content)) {
		t.Fatal("size not recorded")
	}
}

func TestScanSessionUnits_SessionWithoutWorkspaceDirIsSkipped(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	write(t, filepath.Join(home, "memory", "u.md"), "x\n")
	// 没有 workspace-directories 行：项目无法归属，宁可跳过也不猜
	write(t, filepath.Join(home, "projects", "orphan", "s.jsonl"),
		"{\"type\":\"user\",\"sessionId\":\"s\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"无归属\"}}\n")

	units, err := New(home).ScanSessionUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].Err == nil {
		t.Fatalf("want one unit carrying an explanatory error, got %+v", units)
	}
	if _, statErr := os.Stat(filepath.Join(home, "projects", "orphan", "s.jsonl")); statErr != nil {
		t.Fatal("fixture missing")
	}
}

// parseEvents 是测试入口：把 JSONL 文本解析成事件，避开文件读写。
func parseEvents(t *testing.T, content string) []agent.SessionEvent {
	t.Helper()
	sess, events, err := parseSessionJSONL("/x/s.jsonl", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if sess.ExternalID == "" {
		t.Fatal("session id not parsed")
	}
	return events
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/qoder/ -run 'TestParseSession|TestScanSessionUnits' -v`
Expected: FAIL，`undefined: parseSessionJSONL`

- [ ] **Step 3: 写 `internal/agent/qoder/session.go`**

```go
package qoder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

const (
	maxEventRunes = 500
	maxTitleRunes = 80
	// 技能正文与工具说明会被当成 user 消息灌进来，长度是明显特征；
	// 真实目标陈述极少超过这个长度。
	skillInjectionRunes = 4000
)

// 会话 JSONL 的行结构。timestamp 既可能是 RFC3339 字符串也可能是毫秒数字，
// message.content 既可能是字符串也可能是 block 数组——两者都要兼容。
type jsonlLine struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	SessionID   string          `json:"sessionId"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	IsSidechain bool            `json:"isSidechain"`
	Directories []string        `json:"directories"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	IsError  bool            `json:"is_error"`
}

func parseSessionJSONL(path string, content []byte) (domain.Session, []agent.SessionEvent, error) {
	sess := domain.Session{Agent: "qoder", Path: path}
	var events []agent.SessionEvent
	seq := 0

	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := scanner.Bytes()
		if len(raw) == 0 {
			continue
		}
		var l jsonlLine
		if err := json.Unmarshal(raw, &l); err != nil {
			// 单行坏 JSON 只丢这一行，不中断整个文件
			continue
		}
		ts := parseTimestamp(l.Timestamp)

		switch l.Type {
		case "workspace-directories":
			if len(l.Directories) > 0 {
				sess.ProjectPath = l.Directories[0]
				sess.Confidence = "high"
			}
			continue
		case "runtime-config", "queue-operation", "last-prompt", "ai-title":
			continue
		case "user", "assistant":
		default:
			continue
		}

		if l.SessionID != "" && sess.ExternalID == "" {
			sess.ExternalID = l.SessionID
		}
		if l.IsSidechain {
			continue
		}
		seq++
		ev := agent.SessionEvent{Seq: seq, Role: l.Message.Role, Ts: ts}
		applyContent(&ev, l.Message.Content)
		if ev.Kind == domain.KindOther && ev.Text == "" {
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return sess, nil, err
	}
	if sess.ExternalID == "" {
		sess.ExternalID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	for _, e := range events {
		if e.Ts.IsZero() {
			continue
		}
		if sess.StartedAt.IsZero() || e.Ts.Before(sess.StartedAt) {
			sess.StartedAt = e.Ts
		}
		if sess.UpdatedAt.IsZero() || e.Ts.After(sess.UpdatedAt) {
			sess.UpdatedAt = e.Ts
		}
		if sess.Title == "" && e.Kind == domain.KindUserText {
			sess.Title = clipRunes(e.Text, maxTitleRunes)
		}
	}
	sess.EventCount = len(events)
	return sess, events, nil
}

func applyContent(ev *agent.SessionEvent, raw json.RawMessage) {
	if len(raw) == 0 {
		ev.Kind = domain.KindOther
		return
	}
	// 字符串 content：普通用户消息或 assistant 纯文本
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		ev.Text = clipRunes(s, maxEventRunes)
		switch ev.Role {
		case "user":
			if isInjected(s) {
				ev.Kind = domain.KindOther
				ev.Text = ""
				return
			}
			ev.Kind = domain.KindUserText
		case "assistant":
			ev.Kind = domain.KindAssistant
		default:
			ev.Kind = domain.KindOther
		}
		return
	}

	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		ev.Kind = domain.KindOther
		return
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			ev.Text = clipRunes(b.Text, maxEventRunes)
			if ev.Role == "user" {
				if isInjected(b.Text) {
					ev.Kind = domain.KindOther
					ev.Text = ""
				} else {
					ev.Kind = domain.KindUserText
				}
			} else {
				ev.Kind = domain.KindAssistant
			}
		case "tool_use":
			ev.Tool = b.Name
			switch b.Name {
			case "Edit", "Write", "MultiEdit", "NotebookEdit":
				ev.Kind = domain.KindToolEdit
				ev.Files = filesFromInput(b.Input)
			case "Bash":
				ev.Kind = domain.KindToolCommand
				ev.Text = commandFromInput(b.Input)
			case "Read", "Glob", "Grep", "LS":
				// 读取类事件对交接没有信息量，丢掉，否则任务卡会被文件清单淹没
				ev.Kind = domain.KindOther
			default:
				ev.Kind = domain.KindOther
			}
		case "tool_result":
			if b.IsError {
				ev.Kind = domain.KindToolError
				ev.Text = clipRunes(textFromContent(b.Content), maxEventRunes)
			} else {
				ev.Kind = domain.KindOther
			}
		default:
			ev.Kind = domain.KindOther
		}
		if ev.Kind != domain.KindOther {
			return
		}
	}
}

func isInjected(s string) bool {
	if len([]rune(s)) > skillInjectionRunes {
		return true
	}
	for _, p := range []string{"<command-message>", "<command-name>", "<system-reminder>", "<local-command-caveat>"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return strings.Contains(s, "Base directory for this skill:")
}

func filesFromInput(raw json.RawMessage) []string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if v, ok := m[key].(string); ok && v != "" {
			out = append(out, v)
		}
	}
	if edits, ok := m["edits"].([]any); ok {
		for _, e := range edits {
			if em, ok := e.(map[string]any); ok {
				if v, ok := em["file_path"].(string); ok && v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func commandFromInput(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if cmd, ok := m["command"].(string); ok {
		return clipRunes(cmd, maxEventRunes)
	}
	return ""
}

func textFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// parseTimestamp 同时接受 "2026-10-27T08:26:02.995Z" 与 1793089562320。
// 认不出来时返回零值，让调用方自己决定要不要忽略这条事件。
func parseTimestamp(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00"} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC()
			}
		}
		return time.Time{}
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		if ms > 1_000_000_000_000 {
			return time.UnixMilli(ms).UTC()
		}
		return time.Unix(ms, 0).UTC()
	}
	return time.Time{}
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (a *Adapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	var out []agent.SessionUnit
	projectsDir := filepath.Join(a.Home, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		dir := filepath.Join(projectsDir, name)
		files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			return out, err
		}
		sort.Strings(files)
		for _, f := range files {
			u := agent.SessionUnit{Path: f}
			content, err := os.ReadFile(f)
			if err != nil {
				u.Err = err
				out = append(out, u)
				continue
			}
			u.Content = content
			u.Size = int64(len(content))
			if st, err := os.Stat(f); err == nil {
				u.MTime = st.ModTime()
			}
			sess, events, err := parseSessionJSONL(f, content)
			if err != nil {
				u.Err = err
				out = append(out, u)
				continue
			}
			sess.Agent = a.Name()
			sess.ProjectSlug = name
			if sess.ProjectPath == "" {
				// 猜不出项目归属就明确失败：把整场会话并到错误项目里，比丢掉它更糟
				u.Err = fmt.Errorf("no workspace-directories record in %s", f)
				out = append(out, u)
				continue
			}
			if sess.Confidence == "" {
				sess.Confidence = "high"
			}
			u.Session = sess
			u.Events = events
			out = append(out, u)
		}
	}
	return out, nil
}
```

`session.go` 顶部 import 需要 `"sort"`（`ScanSessionUnits` 用到），把 Task 18 Step 6 里那份 `ScanSessionUnits` 占位实现删掉——真正实现现在在这个文件里。

- [ ] **Step 4: 删掉 adapter.go 里的占位实现并在 import 里加 sort**

`adapter.go` 删除：

```go
func (a *Adapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	return nil, nil
}
```

`session.go` 的 import 块补 `"sort"`。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/agent/qoder/ -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/agent/qoder
git commit -m "feat: Qoder 会话 JSONL 解析为结构化事件"
```

---

## Task 19: 会话与事件仓储

一个会话的解析结果完全由源文件决定，所以重扫时整批替换事件，而不是增量对账——增量对账需要稳定事件 id，而 JSONL 只有 uuid，成本明显更高。

**Files:**
- Create: `internal/store/session.go`
- Test: `internal/store/session_test.go`

- [ ] **Step 1: 写失败测试 `internal/store/session_test.go`**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func sampleSession(external string) domain.Session {
	start := time.Unix(1700000000, 0)
	return domain.Session{
		Agent: "qoder", ExternalID: external, ProjectID: 5,
		Title: "加乐观锁", StartedAt: start, UpdatedAt: start.Add(time.Minute),
		EventCount: 2, Path: "/q/s1.jsonl",
	}
}

func sampleEvents() []domain.SessionEvent {
	t0 := time.Unix(1700000000, 0)
	return []domain.SessionEvent{
		{Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "加乐观锁", Ts: t0},
		{Seq: 2, Role: "assistant", Kind: domain.KindToolEdit, Text: "svc.go", Tool: "Edit", Files: []string{`F:\demo\svc.go`}, Ts: t0.Add(time.Minute)},
	}
}

func TestUpsertSession_CreatesThenUpdatesSameRow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	id, err := s.UpsertSession(ctx, sampleSession("s1"))
	if err != nil {
		t.Fatal(err)
	}
	updated := sampleSession("s1")
	updated.Title = "加乐观锁并补测试"
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Hour)
	id2, err := s.UpsertSession(ctx, updated)
	if err != nil {
		t.Fatal(err)
	}
	if id != id2 {
		t.Fatalf("same external id must reuse the row: %d vs %d", id, id2)
	}
	got, ok, err := s.SessionByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Title != "加乐观锁并补测试" {
		t.Fatalf("title = %q", got.Title)
	}
	if got.EventCount != 2 {
		t.Fatalf("event count = %d", got.EventCount)
	}
}

func TestReplaceEvents_ResetsSeqAndIsIdempotent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, err := s.UpsertSession(ctx, sampleSession("s1"))
	if err != nil {
		t.Fatal(err)
	}

	events := sampleEvents()
	if err := s.ReplaceEvents(ctx, id, events); err != nil {
		t.Fatal(err)
	}
	// 第二次带一个新增事件：旧事件不应残留
	extra := domain.SessionEvent{Seq: 3, Role: "assistant", Kind: domain.KindToolCommand, Text: "go test ./...", Tool: "Bash", Ts: time.Unix(1700003600, 0)}
	if err := s.ReplaceEvents(ctx, id, append(events, extra)); err != nil {
		t.Fatal(err)
	}
	list, err := s.EventsFor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("want 3 events, got %d", len(list))
	}
	for i, e := range list {
		if e.Seq != i+1 {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	if list[1].Files[0] != `F:\demo\svc.go` {
		t.Fatalf("files_json not round-tripped: %+v", list[1])
	}

	// 再跑一次完全相同的替换：行数不变
	if err := s.ReplaceEvents(ctx, id, append(events, extra)); err != nil {
		t.Fatal(err)
	}
	n, err := s.QueryInt(ctx, `SELECT count(*) FROM session_event`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("replace should not accumulate: %d", n)
	}
}

func TestListSessions_ByProjectAndRecency(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	a := sampleSession("sA")
	b := sampleSession("sB")
	b.ProjectID = 6
	c := sampleSession("sC")
	c.ProjectID = 5
	c.UpdatedAt = c.UpdatedAt.Add(2 * time.Hour)
	for _, sess := range []domain.Session{a, b, c} {
		if _, err := s.UpsertSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListSessions(ctx, 5, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions for project 5, got %d", len(got))
	}
	if got[0].ExternalID != "sC" {
		t.Fatalf("order should be most recent first, got %q", got[0].ExternalID)
	}
}

func TestUpsertSession_RejectsMissingExternalID(t *testing.T) {
	s := openStore(t)
	bad := sampleSession("")
	if _, err := s.UpsertSession(context.Background(), bad); err == nil {
		t.Fatal("want error for empty external id")
	}
}

func TestSessionStore_PathIsStored(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	dir := t.TempDir()
	sess := sampleSession("s1")
	sess.Path = filepath.Join(dir, "s1.jsonl")
	id, err := s.UpsertSession(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.SessionByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != sess.Path {
		t.Fatalf("path = %q want %q", got.Path, sess.Path)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run 'TestUpsertSession|TestReplaceEvents|TestListSessions|TestSessionStore' -v`
Expected: FAIL，`s.UpsertSession undefined`

- [ ] **Step 3: 写 `internal/store/session.go`**

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func (s *Store) UpsertSession(ctx context.Context, sess domain.Session) (int64, error) {
	if sess.ExternalID == "" {
		return 0, errors.New("session external id is empty")
	}
	if sess.Agent == "" {
		return 0, errors.New("session agent is empty")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session(agent, external_id, project_id, title, started_at, updated_at, event_count, path)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(agent, external_id) DO UPDATE SET
  project_id = excluded.project_id,
  title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE session.title END,
  started_at = CASE WHEN session.started_at = 0 THEN excluded.started_at ELSE session.started_at END,
  updated_at = excluded.updated_at,
  event_count = excluded.event_count,
  path = excluded.path`,
		sess.Agent, sess.ExternalID, sess.ProjectID, sess.Title,
		sess.StartedAt.Unix(), sess.UpdatedAt.Unix(), sess.EventCount, sess.Path)
	if err != nil {
		return 0, fmt.Errorf("upsert session %s/%s: %w", sess.Agent, sess.ExternalID, err)
	}

	// 走 ON CONFLICT DO UPDATE 时 last_insert_rowid() 不指向被更新的那行，必须回查。
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM session WHERE agent = ? AND external_id = ?`,
		sess.Agent, sess.ExternalID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) SessionByID(ctx context.Context, id int64) (domain.Session, bool, error) {
	var (
		sess       domain.Session
		started    int64
		updated    int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, agent, external_id, project_id, title, started_at, updated_at, event_count, path
FROM session WHERE id = ?`, id).Scan(
		&sess.ID, &sess.Agent, &sess.ExternalID, &sess.ProjectID, &sess.Title,
		&started, &updated, &sess.EventCount, &sess.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, err
	}
	sess.StartedAt = time.Unix(started, 0)
	sess.UpdatedAt = time.Unix(updated, 0)
	return sess, true, nil
}

// ReplaceEvents 整批替换一个会话的事件。会话内容完全由源文件决定，
// 重扫时全删全插比对增量对账简单，且 JSONL 没有可用的稳定事件主键。
func (s *Store) ReplaceEvents(ctx context.Context, sessionID int64, events []domain.SessionEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM session_event WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	for i, e := range events {
		files, err := json.Marshal(e.Files)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO session_event(session_id, seq, role, kind, text, tool, files_json, ts)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID, i+1, e.Role, string(e.Kind), e.Text, e.Tool, string(files), e.Ts.Unix()); err != nil {
			return fmt.Errorf("insert event %d: %w", i+1, err)
		}
	}
	return tx.Commit()
}

func (s *Store) EventsFor(ctx context.Context, sessionID int64) ([]domain.SessionEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, seq, role, kind, text, tool, files_json, ts
FROM session_event WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SessionEvent
	for rows.Next() {
		var (
			e        domain.SessionEvent
			kind     string
			filesStr string
			ts       int64
		)
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Role, &kind, &e.Text, &e.Tool, &filesStr, &ts); err != nil {
			return nil, err
		}
		e.Kind = domain.EventKind(kind)
		e.Ts = time.Unix(ts, 0)
		if filesStr != "" && filesStr != "[]" {
			if err := json.Unmarshal([]byte(filesStr), &e.Files); err != nil {
				return nil, fmt.Errorf("event %d files_json: %w", e.ID, err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListSessions(ctx context.Context, projectID int64, limit int) ([]domain.Session, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM session WHERE project_id = ? ORDER BY updated_at DESC, id LIMIT ?`, projectID, limit)
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

	out := make([]domain.Session, 0, len(ids))
	for _, id := range ids {
		sess, ok, err := s.SessionByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, sess)
		}
	}
	return out, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/session.go internal/store/session_test.go
git commit -m "feat: 会话与事件仓储"
```

---

## Task 20: 任务卡规则抽取

`extract` 是纯函数包：输入会话 + 事件，输出任务卡。规则集合与判定顺序都在这一节写清楚，任何改动都必须体现在单测表格里——这是交接质量的唯一防线。

**Files:**
- Create: `internal/extract/extract.go`
- Test: `internal/extract/extract_test.go`

- [ ] **Step 1: 写失败测试 `internal/extract/extract_test.go`**

```go
package extract

import (
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func ev(seq int, kind domain.EventKind, text string, files ...string) domain.SessionEvent {
	base := time.Unix(1700000000, 0).Add(time.Duration(seq) * time.Minute)
	role := "assistant"
	if kind == domain.KindUserText {
		role = "user"
	}
	return domain.SessionEvent{Seq: seq, Role: role, Kind: kind, Text: text, Files: files, Ts: base}
}

func TestTaskCard_GoalIsFirstRealUserTextAndCorrectionsAppend(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "给扣减余额加乐观锁"),
		ev(2, domain.KindAssistant, "先看 Service"),
		ev(3, domain.KindUserText, "另外，失败时不要扣减余额"),
		ev(4, domain.KindToolEdit, "svc.go", `F:\a\svc.go`),
	}
	card, ok := TaskCard(domain.Session{ID: 1, ProjectID: 7}, events, time.Now())
	if !ok {
		t.Fatal("want a card")
	}
	if !strings.Contains(card.Goal, "给扣减余额加乐观锁") {
		t.Fatalf("goal = %q", card.Goal)
	}
	if !strings.Contains(card.Goal, "另外，失败时不要扣减余额") {
		t.Fatalf("later user turns must be captured: %q", card.Goal)
	}
	if card.ProjectID != 7 {
		t.Fatalf("project id = %d", card.ProjectID)
	}
	if len(card.Files) != 1 || card.Files[0] != `F:\a\svc.go` {
		t.Fatalf("files = %v", card.Files)
	}
}

func TestTaskCard_StatusBlockedWhenTrailingErrorsUnresolved(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "修复扣减逻辑"),
		ev(2, domain.KindToolCommand, "go test ./...", `F:\a`),
		ev(3, domain.KindToolError, "undefined: decrementWithLock"),
		ev(4, domain.KindToolError, "FAIL\tpkg/dao"),
	}
	card, ok := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if !ok {
		t.Fatal("want a card")
	}
	if card.Status != domain.StatusBlocked {
		t.Fatalf("status = %q want blocked", card.Status)
	}
	if len(card.Errors) != 2 {
		t.Fatalf("errors = %v", card.Errors)
	}
	if !strings.Contains(card.NextSteps, "undefined: decrementWithLock") {
		t.Fatalf("next steps must name the blocker: %q", card.NextSteps)
	}
}

func TestTaskCard_StatusDoneOnVerificationAfterSuccess(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "修复扣减逻辑"),
		ev(2, domain.KindToolError, "FAIL: balance over-drawn"),
		ev(3, domain.KindToolEdit, "svc.go", `F:\a\svc.go`),
		ev(4, domain.KindToolCommand, "go test ./... ok"),
		ev(5, domain.KindAssistant, "测试通过，扣减已用条件更新"),
	}
	card, ok := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if !ok {
		t.Fatal("want a card")
	}
	if card.Status != domain.StatusDone {
		t.Fatalf("status = %q want done", card.Status)
	}
	if !strings.Contains(card.OpenQuestions, "") && card.OpenQuestions != "" {
		t.Fatalf("no open question expected, got %q", card.OpenQuestions)
	}
}

func TestTaskCard_StatusDoingWhenWorkRemains(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "实现导出功能"),
		ev(2, domain.KindAssistant, "先加接口，稍后补测试"),
	}
	card, _ := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if card.Status != domain.StatusDoing {
		t.Fatalf("status = %q want doing", card.Status)
	}
	if !strings.Contains(card.NextSteps, "先加接口，稍后补测试") {
		t.Fatalf("next steps = %q", card.NextSteps)
	}
}

func TestTaskCard_NoUserTextYieldsNoCard(t *testing.T) {
	events := []domain.SessionEvent{
		ev(2, domain.KindToolEdit, "svc.go", `F:\a\svc.go`),
	}
	if _, ok := TaskCard(domain.Session{ID: 1}, events, time.Now()); ok {
		t.Fatal("a session without a stated goal should not produce a card")
	}
}

func TestTaskCard_CollectsCommandsAndDedupesFiles(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "跑测试"),
		ev(2, domain.KindToolCommand, "go test ./..."),
		ev(3, domain.KindToolCommand, "go test ./..."),
		ev(4, domain.KindToolEdit, "a.go", `F:\a.go`),
		ev(5, domain.KindToolEdit, "a.go again", `F:\a.go`),
		ev(6, domain.KindToolEdit, "b.go", `F:\b.go`),
	}
	card, _ := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if len(card.Commands) != 1 {
		t.Fatalf("commands must dedupe: %v", card.Commands)
	}
	if len(card.Files) != 2 {
		t.Fatalf("files must dedupe keeping order: %v", card.Files)
	}
}

func TestTaskCard_QuestionsExtractedFromAssistantText(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "重构扣减"),
		ev(2, domain.KindAssistant, "要不要把余额校验放到事务内？"),
		ev(3, domain.KindAssistant, "这里需要确认口径：是否含运费"),
	}
	card, _ := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if !strings.Contains(card.OpenQuestions, "余额校验") || !strings.Contains(card.OpenQuestions, "运费") {
		t.Fatalf("open questions = %q", card.OpenQuestions)
	}
}

func TestTaskCard_GeneratedByAndConfidence(t *testing.T) {
	card, _ := TaskCard(domain.Session{ID: 1}, []domain.SessionEvent{ev(1, domain.KindUserText, "x")}, time.Now())
	if card.GeneratedBy != "rule" || card.Confidence != "rule" {
		t.Fatalf("generated_by=%q confidence=%q", card.GeneratedBy, card.Confidence)
	}
}

func TestSettled_UsesFileMTimeNotEventTime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	if Settled(now.Add(-2*time.Minute), now, time.Minute*5) {
		t.Fatal("a session still being written must not be settled")
	}
	if !Settled(now.Add(-6*time.Minute), now, time.Minute*5) {
		t.Fatal("idle beyond threshold must count as settled")
	}
	if Settled(time.Time{}, now, time.Minute*5) {
		t.Fatal("zero mtime must not be treated as settled")
	}
}

func TestTaskCard_GoalIsTruncated(t *testing.T) {
	long := strings.Repeat("锁", 3000)
	card, _ := TaskCard(domain.Session{ID: 1}, []domain.SessionEvent{ev(1, domain.KindUserText, long)}, time.Now())
	if len([]rune(card.Goal)) > 600 {
		t.Fatalf("goal not truncated: %d runes", len([]rune(card.Goal)))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/extract/ -v`
Expected: FAIL，`undefined: TaskCard`

- [ ] **Step 3: 写 `internal/extract/extract.go`**

```go
package extract

import (
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

const (
	maxGoalRunes      = 600
	maxNextRunes      = 400
	maxQuestionRunes  = 300
	maxItemRunes      = 200
	defaultSettleIdle = 5 * time.Minute
)

var questionMarkers = []string{"？", "?", "要不要", "需要确认", "是否", "哪种", "还是"}

// Settled 判断一场会话是否已经写完。判据是源文件 mtime 与当前时刻之差：
// JSONL 的 updated_at 只反映已解析事件，agent 中途停笔与文件停写在这里无法区分，
// 而 mtime 是「还在不在写」的真凭据。mtime 为零值时返回 false，宁可不出卡。
func Settled(mtime, now time.Time, idle time.Duration) bool {
	if mtime.IsZero() {
		return false
	}
	if idle <= 0 {
		idle = defaultSettleIdle
	}
	return now.Sub(mtime) >= idle
}

// TaskCard 从事件序列抽出交接所需的最小结构。
// 第二返回值 false 表示这场会话没有可交接的目标（比如只有工具调用）。
//
// 所有字段只由规则产出：LLM 不参与，避免它编造「改动了哪些文件」。
// M5 的可选增强只允许改写 Goal 与 NextSteps 两个自由文本字段。
func TaskCard(sess domain.Session, events []domain.SessionEvent, now time.Time) (domain.TaskCard, bool) {
	card := domain.TaskCard{
		SessionID:   sess.ID,
		ProjectID:   sess.ProjectID,
		Confidence:  "rule",
		GeneratedBy: "rule",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	var goals []string
	var lastAssistant string
	var questions []string
	var errors []string
	filesSeen := map[string]bool{}
	cmdsSeen := map[string]bool{}
	errTail := 0

	for _, e := range events {
		switch e.Kind {
		case domain.KindUserText:
			if e.Text == "" {
				continue
			}
			if len(goals) == 0 {
				goals = append(goals, clip(e.Text, maxGoalRunes))
			} else if len(goals) < 4 {
				goals = append(goals, clip(e.Text, maxItemRunes))
			}
		case domain.KindAssistant:
			if e.Text == "" {
				continue
			}
			lastAssistant = e.Text
			if isQuestion(e.Text) {
				questions = appendUnique(questions, clip(e.Text, maxQuestionRunes), 6)
			}
		case domain.KindToolEdit:
			for _, f := range e.Files {
				if !filesSeen[f] {
					filesSeen[f] = true
					card.Files = appendUnique(card.Files, f, 40)
				}
			}
		case domain.KindToolCommand:
			if e.Text == "" {
				continue
			}
			if !cmdsSeen[e.Text] {
				cmdsSeen[e.Text] = true
				card.Commands = appendUnique(card.Commands, e.Text, 20)
			}
		case domain.KindToolError:
			if e.Text == "" {
				continue
			}
			errors = appendUnique(errors, clip(e.Text, maxItemRunes), 10)
		}
	}

	if len(goals) == 0 {
		return domain.TaskCard{}, false
	}
	card.Goal = strings.Join(goals, " ｜ ")
	card.Errors = errors
	card.OpenQuestions = strings.Join(questions, " / ")

	// 状态判定只看事件尾部：连续失败且其后没有成功动作 → blocked；
	// 出现验证通过类结论 → done；其余 → doing。
	errTail = trailingErrorRun(events)
	switch {
	case errTail >= 2:
		card.Status = domain.StatusBlocked
		card.NextSteps = "未解决的报错：" + clip(errorsTail(errors, maxNextRunes), maxNextRunes)
	case verified(lastAssistant, card.Commands):
		card.Status = domain.StatusDone
		card.NextSteps = clip(lastAssistant, maxNextRunes)
	default:
		card.Status = domain.StatusDoing
		card.NextSteps = clip(lastAssistant, maxNextRunes)
	}

	decisions := decisionsFrom(events)
	card.Decisions = decisions
	return card, true
}

// trailingErrorRun 数事件序列末尾连续出现的报错。
// 中间被成功操作打断就不算阻塞——即使历史上出过错。
func trailingErrorRun(events []domain.SessionEvent) int {
	n := 0
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case domain.KindToolError:
			n++
		case domain.KindToolCommand, domain.KindToolEdit, domain.KindAssistant:
			return n
		}
	}
	return n
}

// verified 判定「有验证且通过了」。规则很窄：只有末条 assistant 文本里
// 出现通过类措辞，且这场会话确实跑过命令，才算 done。
// 光有「我改好了」不算——那是最容易骗人的话。
func verified(lastAssistant string, commands []string) bool {
	if len(commands) == 0 {
		return false
	}
	for _, marker := range []string{"测试通过", "全部通过", "验证通过", "已通过测试", "PASS", "通过验证"} {
		if strings.Contains(lastAssistant, marker) {
			return true
		}
	}
	return false
}

// decisionsFrom 取「做过的事」作为决策：先命令后文件改动，各自限量。
// 真正的语义决策留给 M5 的 LLM 增强，规则层不假装理解。
func decisionsFrom(events []domain.SessionEvent) []string {
	var out []string
	for _, e := range events {
		if e.Kind == domain.KindToolCommand {
			out = appendUnique(out, "执行: "+clip(e.Text, 120), 8)
		}
	}
	for _, e := range events {
		if e.Kind == domain.KindToolEdit {
			for _, f := range e.Files {
				out = appendUnique(out, "改动: "+f, 16)
			}
		}
	}
	return out
}

func isQuestion(s string) bool {
	if len([]rune(s)) > 200 {
		return false
	}
	for _, m := range questionMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func errorsTail(errors []string, n int) string {
	if len(errors) == 0 {
		return "（无）"
	}
	start := len(errors) - 2
	if start < 0 {
		start = 0
	}
	return clip(strings.Join(errors[start:], " ｜ "), n)
}

func appendUnique(in []string, v string, cap int) []string {
	if v == "" {
		return in
	}
	for _, x := range in {
		if x == v {
			return in
		}
	}
	if cap > 0 && len(in) >= cap {
		return in
	}
	return append(in, v)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/extract/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/extract
git commit -m "feat: 会话事件到任务卡的规则抽取"
```

---

## Task 21: 任务卡仓储

**Files:**
- Create: `internal/store/taskcard.go`
- Test: `internal/store/taskcard_test.go`

- [ ] **Step 1: 写失败测试 `internal/store/taskcard_test.go`**

```go
package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func seedSession(t *testing.T, s *Store, external string, projectID int64, updated time.Time) int64 {
	t.Helper()
	id, err := s.UpsertSession(context.Background(), domain.Session{
		Agent: "qoder", ExternalID: external, ProjectID: projectID,
		Title: external, StartedAt: updated.Add(-time.Hour), UpdatedAt: updated,
		EventCount: 3, Path: "/q/" + external + ".jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUpsertTaskCard_OneCardPerSessionReplaceable(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	sid := seedSession(t, s, "s1", 3, now)

	card := domain.TaskCard{SessionID: sid, ProjectID: 3, Goal: "加乐观锁", Status: domain.StatusDoing,
		Files: []string{`F:\a.go`}, Commands: []string{"go test"}, GeneratedBy: "rule", Confidence: "rule"}
	id, err := s.UpsertTaskCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}
	card.ID = id
	card.Status = domain.StatusDone
	card.NextSteps = "无"
	id2, err := s.UpsertTaskCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}
	if id != id2 {
		t.Fatalf("same session must reuse card: %d vs %d", id, id2)
	}

	var n int
	n, err = s.QueryInt(ctx, `SELECT count(*) FROM task_card`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 card, got %d", n)
	}

	got, err := s.TaskCardBySession(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusDone || len(got.Files) != 1 || got.Files[0] != `F:\a.go` {
		t.Fatalf("round trip wrong: %+v", got)
	}
	if got.Goal != "加乐观锁" || got.Commands[0] != "go test" {
		t.Fatalf("round trip wrong: %+v", got)
	}
}

func TestListTaskCards_OrdersBySessionRecency(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	old := seedSession(t, s, "old", 3, base)
	fresh := seedSession(t, s, "fresh", 3, base.Add(time.Hour))
	other := seedSession(t, s, "other", 9, base.Add(2*time.Hour))

	for _, pair := range []struct {
		sid   int64
		goal  string
		status string
	}{
		{old, "旧任务", domain.StatusDone},
		{fresh, "新任务", domain.StatusBlocked},
		{other, "别的项目", domain.StatusDoing},
	} {
		if _, err := s.UpsertTaskCard(ctx, domain.TaskCard{SessionID: pair.sid, ProjectID: 0,
			Goal: pair.goal, Status: pair.status, GeneratedBy: "rule"}); err != nil {
			t.Fatal(err)
		}
	}
	// ProjectID 在卡上冗余存一份，这里按会话归属重算
	if _, err := s.db.Exec(`UPDATE task_card SET project_id = (SELECT project_id FROM session WHERE session.id = task_card.session_id)`); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListTaskCards(ctx, 3, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 cards for project 3, got %d", len(got))
	}
	if got[0].Goal != "新任务" {
		t.Fatalf("order wrong, first = %q", got[0].Goal)
	}

	blocked, err := s.ListTaskCards(ctx, 3, domain.StatusBlocked, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Status != domain.StatusBlocked {
		t.Fatalf("status filter leaked: %+v", blocked)
	}
}

func TestListTaskCards_CarriesSessionTitle(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	sid := seedSession(t, s, "s9", 3, now)
	if _, err := s.UpsertTaskCard(ctx, domain.TaskCard{SessionID: sid, ProjectID: 3, Goal: "g", Status: domain.StatusDoing}); err != nil {
		t.Fatal(err)
	}
	cards, err := s.ListTaskCards(ctx, 3, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || !strings.Contains(cards[0].Goal, "g") {
		t.Fatalf("cards = %+v", cards)
	}
	sess, ok, err := s.SessionByID(ctx, cards[0].SessionID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if sess.Title != "s9" {
		t.Fatalf("title = %q", sess.Title)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestUpsertTaskCard -v`
Expected: FAIL，`s.UpsertTaskCard undefined`

- [ ] **Step 3: 写 `internal/store/taskcard.go`**

```go
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func (s *Store) UpsertTaskCard(ctx context.Context, card domain.TaskCard) (int64, error) {
	if card.SessionID == 0 {
		return 0, errors.New("task card needs a session id")
	}
	decisions, err := json.Marshal(card.Decisions)
	if err != nil {
		return 0, err
	}
	files, err := json.Marshal(card.Files)
	if err != nil {
		return 0, err
	}
	commands, err := json.Marshal(card.Commands)
	if err != nil {
		return 0, err
	}
	errs, err := json.Marshal(card.Errors)
	if err != nil {
		return 0, err
	}
	if card.Status == "" {
		card.Status = domain.StatusUnknown
	}
	if card.GeneratedBy == "" {
		card.GeneratedBy = "rule"
	}

	res, err := s.db.ExecContext(ctx, `
INSERT INTO task_card(session_id, project_id, goal, status, decisions_json, files_json, commands_json,
  errors_json, open_questions, next_steps, confidence, generated_by, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
  project_id = excluded.project_id,
  goal = excluded.goal,
  status = excluded.status,
  decisions_json = excluded.decisions_json,
  files_json = excluded.files_json,
  commands_json = excluded.commands_json,
  errors_json = excluded.errors_json,
  open_questions = excluded.open_questions,
  next_steps = excluded.next_steps,
  confidence = excluded.confidence,
  generated_by = excluded.generated_by,
  updated_at = excluded.updated_at`,
		card.SessionID, card.ProjectID, card.Goal, card.Status,
		string(decisions), string(files), string(commands), string(errs),
		card.OpenQuestions, card.NextSteps, card.Confidence, card.GeneratedBy,
		card.CreatedAt.Unix(), card.UpdatedAt.Unix())
	if err != nil {
		return 0, fmt.Errorf("upsert task card for session %d: %w", card.SessionID, err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM task_card WHERE session_id = ?`, card.SessionID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) TaskCardBySession(ctx context.Context, sessionID int64) (domain.TaskCard, error) {
	var (
		card      domain.TaskCard
		decisions string
		files     string
		commands  string
		errs      string
		created   int64
		updated   int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, session_id, project_id, goal, status, decisions_json, files_json, commands_json,
  errors_json, open_questions, next_steps, confidence, generated_by, created_at, updated_at
FROM task_card WHERE session_id = ?`, sessionID).Scan(
		&card.ID, &card.SessionID, &card.ProjectID, &card.Goal, &card.Status,
		&decisions, &files, &commands, &errs,
		&card.OpenQuestions, &card.NextSteps, &card.Confidence, &card.GeneratedBy, &created, &updated)
	if err != nil {
		return domain.TaskCard{}, err
	}
	card.Decisions, card.Files, card.Commands, card.Errors, err = decodeLists(decisions, files, commands, errs)
	if err != nil {
		return domain.TaskCard{}, err
	}
	card.CreatedAt = time.Unix(created, 0)
	card.UpdatedAt = time.Unix(updated, 0)
	return card, nil
}

// ListTaskCards 按项目取任务卡，顺序跟随所属会话的最近活动时间。
// 简报要的是「最近在做什么」，不是「卡片最后被改的时间」——两者在重扫时会错开。
func (s *Store) ListTaskCards(ctx context.Context, projectID int64, status string, limit int) ([]domain.TaskCard, error) {
	if limit <= 0 || limit > 100 {
		limit = 5
	}
	q := `
SELECT t.id FROM task_card t
JOIN session se ON se.id = t.session_id
WHERE t.project_id = ?`
	args := []any{projectID}
	if status != "" {
		q += ` AND t.status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY se.updated_at DESC, t.id DESC LIMIT ?`
	args = append(args, limit)

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

	out := make([]domain.TaskCard, 0, len(ids))
	for _, id := range ids {
		card, err := s.taskCardByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, nil
}

func (s *Store) taskCardByID(ctx context.Context, id int64) (domain.TaskCard, error) {
	var (
		card      domain.TaskCard
		decisions string
		files     string
		commands  string
		errs      string
		created   int64
		updated   int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, session_id, project_id, goal, status, decisions_json, files_json, commands_json,
  errors_json, open_questions, next_steps, confidence, generated_by, created_at, updated_at
FROM task_card WHERE id = ?`, id).Scan(
		&card.ID, &card.SessionID, &card.ProjectID, &card.Goal, &card.Status,
		&decisions, &files, &commands, &errs,
		&card.OpenQuestions, &card.NextSteps, &card.Confidence, &card.GeneratedBy, &created, &updated)
	if err != nil {
		return domain.TaskCard{}, err
	}
	card.Decisions, card.Files, card.Commands, card.Errors, err = decodeLists(decisions, files, commands, errs)
	if err != nil {
		return domain.TaskCard{}, err
	}
	card.CreatedAt = time.Unix(created, 0)
	card.UpdatedAt = time.Unix(updated, 0)
	return card, nil
}

func decodeLists(decisions, files, commands, errs string) ([]string, []string, []string, []string, error) {
	var d, f, c, e []string
	for _, item := range []struct {
		raw  string
		dest *[]string
	}{{decisions, &d}, {files, &f}, {commands, &c}, {errs, &e}} {
		if item.raw == "" {
			continue
		}
		if err := json.Unmarshal([]byte(item.raw), item.dest); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return d, f, c, e, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/taskcard.go internal/store/taskcard_test.go
git commit -m "feat: 任务卡仓储"
```

---

## Task 22: 交接简报合成

Composer 是纯函数：库里已有的记忆 + 任务卡 → markdown。它不读任何 agent 文件，也不写任何文件。排序规则完全固定，同样的库状态必须产出逐字节相同的简报，否则每次扫描都会重写 `HANDOFF.md`。

**Files:**
- Create: `internal/compose/compose.go`
- Modify: `internal/store/project.go`（追加 `ProjectByID`）
- Modify: `internal/store/handoff.go`（新建，简报仓储）
- Test: `internal/compose/compose_test.go`

- [ ] **Step 1: 在 `internal/store/project.go` 追加 `ProjectByID`**

```go
func (s *Store) ProjectByID(ctx context.Context, id int64) (domain.Project, bool, error) {
	var (
		p         domain.Project
		created   int64
		updated   int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, key, display_name, git_remote, primary_path, created_at, updated_at
FROM project WHERE id = ?`, id).Scan(
		&p.ID, &p.Key, &p.DisplayName, &p.GitRemote, &p.PrimaryPath, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, false, nil
	}
	if err != nil {
		return domain.Project{}, false, err
	}
	p.CreatedAt = time.Unix(created, 0)
	p.UpdatedAt = time.Unix(updated, 0)
	return p, true, nil
}
```

- [ ] **Step 2: 写 `internal/store/handoff.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type HandoffRow struct {
	ProjectID    int64
	Body         string
	ContentHash  string
	BudgetTokens int
	GeneratedAt  time.Time
}

// SaveHandoff 每个项目只保留最近一版简报。内容没变时返回 changed=false，
// 调用方据此决定要不要重写磁盘文件——重写会刷新 mtime，白耗一次监听事件。
func (s *Store) SaveHandoff(ctx context.Context, row HandoffRow) (changed bool, err error) {
	var existing string
	err = s.db.QueryRowContext(ctx, `SELECT content_hash FROM handoff WHERE project_id = ?`, row.ProjectID).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		changed = true
	case err != nil:
		return false, err
	default:
		changed = existing != row.ContentHash
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO handoff(project_id, body, content_hash, budget_tokens, generated_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(project_id) DO UPDATE SET
  body = excluded.body,
  content_hash = excluded.content_hash,
  budget_tokens = excluded.budget_tokens,
  generated_at = excluded.generated_at`,
		row.ProjectID, row.Body, row.ContentHash, row.BudgetTokens, row.GeneratedAt.Unix())
	if err != nil {
		return false, err
	}
	return changed, nil
}

func (s *Store) LastHandoff(ctx context.Context, projectID int64) (HandoffRow, bool, error) {
	var (
		row     HandoffRow
		generated int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT project_id, body, content_hash, budget_tokens, generated_at
FROM handoff WHERE project_id = ?`, projectID).Scan(
		&row.ProjectID, &row.Body, &row.ContentHash, &row.BudgetTokens, &generated)
	if errors.Is(err, sql.ErrNoRows) {
		return HandoffRow{}, false, nil
	}
	if err != nil {
		return HandoffRow{}, false, err
	}
	row.GeneratedAt = time.Unix(generated, 0)
	return row, true, nil
}
```

- [ ] **Step 3: 写 `internal/store/handoff_test.go`**

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/normalize"
)

func TestSaveHandoff_KeepsOnePerProjectAndDetectsNoChange(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/x", "x", "", `F:\x`, now)
	if err != nil {
		t.Fatal(err)
	}
	body := "# 交接简报\n\n内容"
	hash := normalize.Hash(body)

	changed, err := s.SaveHandoff(ctx, HandoffRow{ProjectID: pid, Body: body, ContentHash: hash, BudgetTokens: 1500, GeneratedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first save should count as a change")
	}

	changed, err = s.SaveHandoff(ctx, HandoffRow{ProjectID: pid, Body: body, ContentHash: hash, BudgetTokens: 1500, GeneratedAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("identical content must not count as a change")
	}

	row, ok, err := s.LastHandoff(ctx, pid)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if row.Body != body || row.BudgetTokens != 1500 {
		t.Fatalf("row = %+v", row)
	}
	n, err := s.QueryInt(ctx, `SELECT count(*) FROM handoff`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 row per project, got %d", n)
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -run 'TestSaveHandoff' -v`
Expected: PASS

- [ ] **Step 5: 写失败测试 `internal/compose/compose_test.go`**

```go
package compose

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/extract"
	"github.com/muyi99/agent-memory/internal/store"
)

type fixture struct {
	st        *store.Store
	projectID int64
	ctx       context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	pid, err := st.EnsureProject(ctx, "f:/go/demo", "demo", "", `F:\go\demo`, now)
	if err != nil {
		t.Fatal(err)
	}

	add := func(scope domain.Scope, pid int64, typ domain.MemoryType, text, agent, path string, seen time.Time) {
		t.Helper()
		_, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: scope, Type: typ, Text: text, SourceAgent: agent, SourcePath: path, Quote: text,
		}, seen)
		if err != nil {
			t.Fatal(err)
		}
	}
	add(domain.ScopeUser, 0, domain.TypePreference, "沟通用中文，提交信息也用中文", "qoder", "/u1.md", now)
	add(domain.ScopeUser, 0, domain.TypePreference, "变量用 snake_case", "trae", "/u2.md", now.Add(time.Minute))
	add(domain.ScopeProject, pid, domain.TypeConstraint, "余额扣减需 WHERE balance >= cost 条件更新", "qoder", "/p1.md", now)
	add(domain.ScopeProject, pid, domain.TypeConvention, "ai_token_log.change_type：1=充值，-1=扣减", "trae", "/p2.md", now)
	add(domain.ScopeProject, pid, domain.TypeFact, "已废弃的旧写法", "qoder", "/p3.md", now)
	if _, err := st.db.Exec(`UPDATE memory_item SET stale = 1 WHERE text = ?`, "已废弃的旧写法"); err != nil {
		t.Fatal(err)
	}

	// 两张任务卡：一新一旧，简报只应带最近 3 张里的前几张
	sid := seedSessionCard(t, st, ctx, "s-old", pid, now.Add(-time.Hour), domain.StatusDone,
		"接入余额校验", "无", []string{`F:\a.go`})
	_ = sid
	seedSessionCard(t, st, ctx, "s-new", pid, now, domain.StatusBlocked,
		"给扣减加乐观锁", "undefined: decrementWithLock", []string{`F:\svc.go`, `F:\dao.go`})

	return &fixture{st: st, projectID: pid, ctx: ctx}
}

func seedSessionCard(t *testing.T, st *store.Store, ctx context.Context, external string, pid int64, at time.Time, status, goal, next string, files []string) int64 {
	t.Helper()
	sid, err := st.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: external, ProjectID: pid, Title: goal,
		StartedAt: at.Add(-10 * time.Minute), UpdatedAt: at, EventCount: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	card := domain.TaskCard{SessionID: sid, ProjectID: pid, Goal: goal, Status: status,
		NextSteps: next, Files: files, Commands: []string{"go test ./..."}, GeneratedBy: "rule", Confidence: "rule"}
	if status == domain.StatusBlocked {
		card.Errors = []string{next}
	}
	if _, err := st.UpsertTaskCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	return sid
}

func render(t *testing.T, f *fixture, budget int) Result {
	t.Helper()
	c := New(f.st, Config{BudgetTokens: budget, RecentCards: 3})
	res, err := c.Render(f.ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRender_ContainsAllSixSectionsInFixedOrder(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	for _, want := range []string{
		"## 项目概况",
		"## 用户偏好",
		"## 项目硬约束与约定",
		"## 最近任务",
		"## 未决问题",
		"## 共享记忆入口",
	} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("missing section %q in:\n%s", want, res.Body)
		}
	}
	idx := func(s string) int { return strings.Index(res.Body, s) }
	if !(idx("## 项目概况") < idx("## 用户偏好") && idx("## 用户偏好") < idx("## 项目硬约束与约定") &&
		idx("## 项目硬约束与约定") < idx("## 最近任务") && idx("## 最近任务") < idx("## 未决问题") &&
		idx("## 未决问题") < idx("## 共享记忆入口")) {
		t.Fatalf("sections out of order:\n%s", res.Body)
	}
}

func TestRender_ExcludesStaleAndForeignProject(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	if strings.Contains(res.Body, "已废弃的旧写法") {
		t.Fatal("stale memory must not appear")
	}
	if strings.Contains(res.Body, "别的项目") {
		t.Fatal("foreign project memory leaked")
	}
}

func TestRender_CardsAreMostRecentFirst(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	if strings.Index(res.Body, "给扣减加乐观锁") > strings.Index(res.Body, "接入余额校验") {
		t.Fatalf("cards must be ordered by session recency:\n%s", res.Body)
	}
	if !strings.Contains(res.Body, "状态: blocked") {
		t.Fatalf("status missing:\n%s", res.Body)
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	f := newFixture(t)
	a := render(t, f, 1500)
	b := render(t, f, 1500)
	if a.Body != b.Body {
		t.Fatal("same store state must produce the same bytes")
	}
	if a.ContentHash != b.ContentHash {
		t.Fatal("hash must match content")
	}
}

func TestRender_BudgetTrimsCardDetailBeforeDroppingSections(t *testing.T) {
	// 预算刚好容不下降级后的卡片细节：优先砍细节，再砍卡片数量
	res := render(t, newFixture(t), 90)
	if !res.Truncated {
		t.Fatal("want truncated flag under a tight budget")
	}
	for _, mustKeep := range []string{"## 用户偏好", "## 项目硬约束与约定", "沟通用中文"} {
		if !strings.Contains(res.Body, mustKeep) {
			t.Fatalf("protected section dropped:\n%s", res.Body)
		}
	}
	if strings.Contains(res.Body, "F:\\svc.go") {
		t.Fatalf("card file list should be the first thing cut:\n%s", res.Body)
	}
	if res.Tokens > 90 {
		t.Fatalf("tokens=%d exceeds budget", res.Tokens)
	}
}

func TestRender_ExtremeBudgetStillKeepsPreferencesAndConstraints(t *testing.T) {
	res := render(t, newFixture(t), 5)
	if !strings.Contains(res.Body, "## 用户偏好") || !strings.Contains(res.Body, "## 项目硬约束与约定") {
		t.Fatalf("protected sections must survive any budget:\n%s", res.Body)
	}
	if !strings.Contains(res.Body, "## 共享记忆入口") {
		t.Fatalf("entry hint must survive:\n%s", res.Body)
	}
}

func TestEstimateTokens_CountsCJKAsOnePerChar(t *testing.T) {
	if got := EstimateTokens("中文中文中文"); got < 5 {
		t.Fatalf("cjk undercounted: %d", got)
	}
	if got := EstimateTokens("hello hello hello"); got > 6 {
		t.Fatalf("ascii overcounted: %d", got)
	}
}

func TestRender_UsesExtractorRulesEndToEnd(t *testing.T) {
	// 简报里的任务卡字段必须与 extract 的产出一致，防止 compose 自己编内容
	f := newFixture(t)
	events := []domain.SessionEvent{
		{Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "写个新目标"},
		{Seq: 2, Role: "assistant", Kind: domain.KindAssistant, Text: "先不动"},
	}
	card, ok := extract.TaskCard(domain.Session{ID: 99, ProjectID: f.projectID}, events, time.Unix(1700000100, 0))
	if !ok {
		t.Fatal("want a card")
	}
	if _, err := f.st.UpsertSession(f.ctx, domain.Session{Agent: "qoder", ExternalID: "s-x",
		ProjectID: f.projectID, Title: card.Goal, StartedAt: time.Unix(1700000100, 0), UpdatedAt: time.Unix(1700000100, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.UpsertTaskCard(f.ctx, card); err != nil {
		t.Fatal(err)
	}
	res := render(t, f, 1500)
	if !strings.Contains(res.Body, "写个新目标") {
		t.Fatalf("extracted goal missing from brief:\n%s", res.Body)
	}
}
```

`compose_test.go` 在 `store` 包外，访问不到非导出的 `db` 字段，所以标过时这件事要有正式入口。在 `internal/store/memory.go` 追加：

```go
// SetStaleByID 置或清条目的过时标记。MCP 的 supersedes、doctor 与测试都走这里；
// 直接对外暴露 *sql.DB 会让「唯一写入口」这条边界失效。
func (s *Store) SetStaleByID(ctx context.Context, id int64, stale bool) error {
	flag := 0
	if stale {
		flag = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE memory_item SET stale = ? WHERE id = ?`, flag, id)
	return err
}
```

`newFixture` 里把那段直接执行 SQL 的代码换成：

```go
	ids, err := st.QueryStrings(ctx, `SELECT CAST(id AS TEXT) FROM memory_item WHERE text = ?`, "已废弃的旧写法")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	staleID, err := strconv.ParseInt(ids[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetStaleByID(ctx, staleID, true); err != nil {
		t.Fatal(err)
	}
```

测试 import 块相应加 `"strconv"`，去掉不再需要的东西。

- [ ] **Step 6: 跑测试确认失败**

Run: `go test ./internal/compose/ -v`
Expected: FAIL，`undefined: New`

- [ ] **Step 7: 写 `internal/compose/compose.go`**

```go
package compose

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
	"github.com/muyi99/agent-memory/internal/store"
)

const (
	defaultBudgetTokens = 1500
	defaultRecentCards  = 3
)

type Config struct {
	BudgetTokens int
	RecentCards  int
}

type Result struct {
	Body        string
	Tokens      int
	Truncated   bool
	ContentHash string
}

// Composer 把库状态渲染成交接简报。它是纯函数：不读 agent 文件、不写磁盘。
type Composer struct {
	st  *store.Store
	cfg Config
}

func New(st *store.Store, cfg Config) *Composer {
	if cfg.BudgetTokens <= 0 {
		cfg.BudgetTokens = defaultBudgetTokens
	}
	if cfg.RecentCards <= 0 {
		cfg.RecentCards = defaultRecentCards
	}
	return &Composer{st: st, cfg: cfg}
}

// EstimateTokens 粗估 token 数：一个汉字算一个，其余非空白字符四个算一个。
// 简报语料以中文为主，这个估计足够稳定，且不需要引分词器。
func EstimateTokens(s string) int {
	var han, other int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.IsSpace(r):
		default:
			other++
		}
	}
	return han + other/4 + 1
}

// Render 生成简报。裁剪顺序是刻意的：先砍任务卡细节，再减卡片数量，最后截未决问题；
// 用户偏好与项目硬约束任何情况下都不砍——那是「不用从头描述」的最低保障。
func (c *Composer) Render(ctx context.Context, projectID int64) (Result, error) {
	project, ok, err := c.st.ProjectByID(ctx, projectID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("project %d not found", projectID)
	}

	cards, err := c.st.ListTaskCards(ctx, projectID, "", c.cfg.RecentCards)
	if err != nil {
		return Result{}, err
	}
	cardSessions := make(map[int64]domain.Session, len(cards))
	for _, card := range cards {
		sess, ok, err := c.st.SessionByID(ctx, card.SessionID)
		if err != nil {
			return Result{}, err
		}
		if ok {
			cardSessions[card.ID] = sess
		}
	}

	userItems, err := c.st.RecentMemoriesByType(ctx, 0, domain.ScopeUser, []domain.MemoryType{
		domain.TypePreference, domain.TypeConvention, domain.TypeConstraint, domain.TypeLesson, domain.TypeFact,
	}, 40)
	if err != nil {
		return Result{}, err
	}
	projectItems, err := c.st.RecentMemoriesByType(ctx, projectID, domain.ScopeProject, []domain.MemoryType{
		domain.TypeConstraint, domain.TypeConvention, domain.TypeLesson,
	}, 40)
	if err != nil {
		return Result{}, err
	}

	overview := renderOverview(project, len(userItems), len(projectItems), len(cards))
	prefs := renderList("## 用户偏好", userItems)
	constraints := renderList("## 项目硬约束与约定", projectItems)
	entry := renderEntry(project.Key)

	// 三档卡片渲染，从详细到骨架，预算不够就整档替换
	full := renderCards(cards, cardSessions, cardDetailFull)
	lean := renderCards(cards, cardSessions, cardDetailLean)

	var (
		body      string
		truncated bool
	)
	for _, attempt := range []struct {
		cards string
		ask   int
		drop  int
	}{
		{cards: full, ask: len(cards), drop: 0},
		{cards: lean, ask: len(cards), drop: 0},
		{cards: lean, ask: 2, drop: 0},
		{cards: lean, ask: 1, drop: 0},
	} {
		shown := attempt.cards
		if attempt.ask < len(cards) {
			shown = renderCards(cards[:attempt.ask], cardSessions, cardDetailLean)
		}
		questions := renderQuestions(cards[:minInt(attempt.ask, len(cards))])
		candidate := strings.Join([]string{overview, prefs, constraints, section("## 最近任务", shown), questions, entry}, "\n")
		if EstimateTokens(candidate) <= c.cfg.BudgetTokens {
			body = candidate
			truncated = attempt.ask < len(cards) || shown != full
			break
		}
		if body == "" {
			// 连最省的一档都超预算：保留受保护段落，砍掉任务卡整段与未决问题
			body = strings.Join([]string{overview, prefs, constraints, section("## 最近任务", ""), "## 未决问题\n\n（超预算，已省略）\n", entry}, "\n")
			truncated = true
		}
	}

	return Result{
		Body:        strings.TrimRight(body, "\n") + "\n",
		Tokens:      EstimateTokens(body),
		Truncated:   truncated,
		ContentHash: normalize.Hash(body),
	}, nil
}

func renderOverview(p domain.Project, userCount, projectCount, cardCount int) string {
	var lines []string
	lines = append(lines, "## 项目概况", "")
	lines = append(lines, fmt.Sprintf("- project_key: `%s`", p.Key))
	if p.PrimaryPath != "" {
		lines = append(lines, fmt.Sprintf("- 路径: `%s`", p.PrimaryPath))
	}
	lines = append(lines, fmt.Sprintf("- 共享记忆: 全局 %d 条 / 本项目 %d 条；最近任务 %d 个", userCount, projectCount, cardCount))
	return strings.Join(lines, "\n") + "\n"
}

func section(title, body string) string {
	if strings.TrimSpace(body) == "" {
		return title + "\n\n（无）\n"
	}
	return title + "\n\n" + strings.TrimRight(body, "\n") + "\n"
}

func renderList(title string, items []domain.MemoryItem) string {
	if len(items) == 0 {
		return section(title, "")
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	for _, m := range items {
		agents := make([]string, 0, len(m.Sources))
		for _, s := range m.Sources {
			agents = append(agents, s.Agent)
		}
		fmt.Fprintf(&b, "- (%s, 来自 %s) %s\n", m.Type, strings.Join(agents, "+"), oneLine(m.Text))
	}
	return b.String()
}

type detailLevel int

const (
	cardDetailFull detailLevel = iota
	cardDetailLean
)

func renderCards(cards []domain.TaskCard, sessions map[int64]domain.Session, level detailLevel) string {
	var b strings.Builder
	for i, card := range cards {
		sess := sessions[card.ID]
		when := sess.UpdatedAt
		if when.IsZero() {
			when = card.UpdatedAt
		}
		fmt.Fprintf(&b, "### %d. %s\n", i+1, oneLine(card.Goal))
		fmt.Fprintf(&b, "- 状态: %s ｜ 会话: `%s` ｜ 最近活动: %s\n", card.Status, oneLine(sess.ExternalID), when.Format(time.RFC3339))
		if level == cardDetailFull && len(card.Decisions) > 0 {
			fmt.Fprintf(&b, "- 关键决策: %s\n", strings.Join(head(card.Decisions, 5), "; "))
		}
		if level == cardDetailFull && len(card.Files) > 0 {
			fmt.Fprintf(&b, "- 改动文件: %s\n", strings.Join(head(card.Files, 8), ", "))
		}
		if len(card.Commands) > 0 {
			fmt.Fprintf(&b, "- 执行命令: %s\n", strings.Join(head(card.Commands, 4), " && "))
		}
		if card.NextSteps != "" {
			fmt.Fprintf(&b, "- 下一步: %s\n", oneLine(card.NextSteps))
		}
		if len(card.Errors) > 0 {
			fmt.Fprintf(&b, "- 未解决报错: %s\n", oneLine(head(card.Errors, 2)[0]))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderQuestions(cards []domain.TaskCard) string {
	var qs []string
	for _, card := range cards {
		if card.OpenQuestions == "" {
			continue
		}
		for _, q := range strings.Split(card.OpenQuestions, " / ") {
			if q = strings.TrimSpace(q); q != "" {
				qs = append(qs, fmt.Sprintf("- [%s] %s", card.Status, oneLine(q)))
			}
		}
	}
	return section("## 未决问题", strings.Join(qs, "\n"))
}

func renderEntry(projectKey string) string {
	return strings.Join([]string{
		"## 共享记忆入口",
		"",
		"接上下文时优先读这份简报，再决定要不要按需检索：",
		"",
		"- `agent-memory memory search \"<关键词>\" --project " + projectKey + "`",
		"- MCP tools: `memory_search` / `memory_add` / `handoff_get` / `task_list`（M3 起可用）",
		"- 学到的新约定请用 `memory_add` 写回共享库，别只留在本 agent 的记忆里",
		"",
	}, "\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func head(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

`RecentMemoriesByType` 是 M1 `RecentMemories` 的加强版（要同时按 scope 与一组类型取），在 `internal/store/search.go` 里加：

```go
// RecentMemoriesByType 按 scope 与一组类型取最近见过的条目。
// 简报需要「全局偏好」与「本项目约束」两组，各自单独取，互不挤占额度。
func (s *Store) RecentMemoriesByType(ctx context.Context, projectID int64, scope domain.Scope, types []domain.MemoryType, limit int) ([]domain.MemoryItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	q := `SELECT id FROM memory_item WHERE stale = 0 AND scope = ?`
	args := []any{string(scope)}
	if scope == domain.ScopeProject {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if len(types) > 0 {
		placeholders := strings.Repeat("?,", len(types))
		q += ` AND type IN (` + strings.TrimSuffix(placeholders, ",") + `)`
		for _, t := range types {
			args = append(args, string(t))
		}
	}
	q += ` ORDER BY last_seen_at DESC, id LIMIT ?`
	args = append(args, limit)

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

（`internal/store/search.go` 的 import 里需要 `"strings"`，已有。）

- [ ] **Step 8: 跑测试确认通过**

Run: `go test ./internal/compose/ -v && go test ./...`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add internal/compose internal/store
git commit -m "feat: 交接简报合成与预算裁剪"
```

---

## Task 23: 简报落盘与 .git/info/exclude

写入只有两处，都要可重入且不破坏既有内容。

**Files:**
- Create: `internal/surface/handoff_file.go`
- Test: `internal/surface/handoff_file_test.go`

- [ ] **Step 1: 写失败测试 `internal/surface/handoff_file_test.go`**

```go
package surface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteHandoff_CreatesFileAndReportsChange(t *testing.T) {
	dir := t.TempDir()
	path, changed, err := WriteHandoff(dir, "# 交接简报\n\n目标：加乐观锁\n")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".agent-memory", "HANDOFF.md")
	if path != want {
		t.Fatalf("path = %q want %q", path, want)
	}
	if !changed {
		t.Fatal("first write should report a change")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "加乐观锁") {
		t.Fatalf("body = %q", body)
	}
}

func TestWriteHandoff_IdenticalContentDoesNotTouchMTime(t *testing.T) {
	dir := t.TempDir()
	path, _, err := WriteHandoff(dir, "同一份内容\n")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := st.ModTime()
	// 显式把 mtime 推到过去，验证「内容相同就不重写」
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	_, _, err = WriteHandoff(dir, "同一份内容\n")
	if err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.ModTime().Equal(old) {
		t.Fatalf("mtime moved from %v to %v: identical content must not rewrite", old, st2.ModTime())
	}
}

func TestWriteHandoff_LeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := WriteHandoff(dir, "第一版\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WriteHandoff(dir, "第二版\n"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".agent-memory"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "HANDOFF.md" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp files left behind: %v", names)
	}
}

func TestEnsureGitExclude(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureGitExclude(dir, "/.agent-memory/")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first call should add the entry")
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/.agent-memory/") {
		t.Fatalf("exclude content = %q", data)
	}

	// 再调用不重复追加
	added, err = EnsureGitExclude(dir, "/.agent-memory/")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("second call should be a no-op")
	}
	data2, _ := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if strings.Count(string(data2), "/.agent-memory/") != 1 {
		t.Fatalf("entry duplicated: %q", data2)
	}
}

func TestEnsureGitExclude_KeepsExistingLines(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "exclude"), []byte("/vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureGitExclude(dir, "/.agent-memory/"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if !strings.Contains(string(data), "/vendor/") || !strings.Contains(string(data), "/.agent-memory/") {
		t.Fatalf("existing entries damaged: %q", data)
	}
}

func TestEnsureGitExclude_NonRepoIsSilentNoop(t *testing.T) {
	added, err := EnsureGitExclude(t.TempDir(), "/.agent-memory/")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("non-repo directory should not report an addition")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/surface/ -v`
Expected: FAIL，`undefined: WriteHandoff`

- [ ] **Step 3: 写 `internal/surface/handoff_file.go`**

```go
package surface

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DirName 是项目内的输出目录名，M3 的受管区块也指向它。
	DirName  = ".agent-memory"
	FileName = "HANDOFF.md"
)

// WriteHandoff 把简报正文写进 <projectDir>/.agent-memory/HANDOFF.md。
// 内容相同则完全不碰磁盘：重写会刷新 mtime，白耗一次 fsnotify 事件。
func WriteHandoff(projectDir, body string) (path string, changed bool, err error) {
	dir := filepath.Join(projectDir, DirName)
	path = filepath.Join(dir, FileName)

	if existing, err := os.ReadFile(path); err == nil && string(existing) == body {
		return path, false, nil
	} else if err != nil && !os.IsNotExist(err) {
		return path, false, err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, false, fmt.Errorf("create %s: %w", dir, err)
	}

	// 临时文件 + rename：读者不会看到半份简报，崩了也不留残缺正文
	tmp, err := os.CreateTemp(dir, ".handoff-*.tmp")
	if err != nil {
		return path, false, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return path, false, err
	}
	if err := tmp.Close(); err != nil {
		return path, false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return path, false, fmt.Errorf("publish handoff file: %w", err)
	}
	return path, true, nil
}

// EnsureGitExclude 把条目加进 .git/info/exclude。
// 选它而不是 .gitignore：只影响本机，不会往用户提交的仓库文件里塞内容。
func EnsureGitExclude(projectDir, entry string) (added bool, err error) {
	gitDir := filepath.Join(projectDir, ".git")
	if st, err := os.Stat(gitDir); err != nil || !st.IsDir() {
		return false, nil
	}
	infoDir := filepath.Join(gitDir, "info")
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return false, err
	}
	path := filepath.Join(infoDir, "exclude")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if containsLine(string(existing), entry) {
		return false, nil
	}
	out := string(existing)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += entry + "\n"
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func containsLine(text, entry string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/surface/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/surface
git commit -m "feat: 交接简报原子落盘与 .git/info/exclude 维护"
```

---

## Task 24: 扫描编排接入会话与配置项

**Files:**
- Modify: `internal/ingest/ingest.go`
- Modify: `internal/ingest/ingest_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试——在 `internal/ingest/ingest_test.go` 追加**

```go
func TestRun_ImportsSessionsAndCardsWhenSettled(t *testing.T) {
	home, repoA, repoB := fakeQoder(t)
	// 真实会话文件：一条用户目标 + 一次编辑 + 一条报错
	write(t, filepath.Join(home, "projects", "RA", "s1.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repoA)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"给扣减加乐观锁\"}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:01:00Z\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"name\":\"Edit\",\"input\":{\"file_path\":\"F:\\\\a.go\"}}]}}\n")
	write(t, filepath.Join(home, "projects", "RB", "s2.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s2\",\"directories\":[%q]}\n", repoB)+
			"{\"type\":\"user\",\"sessionId\":\"s2\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"还在写的会话\"}}\n")

	// 让 RA 的会话文件"已经写完"，RB 的"还在写"
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(home, "projects", "RA", "s1.jsonl"), settled, settled); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now()
	if err := os.Chtimes(filepath.Join(home, "projects", "RB", "s2.jsonl"), fresh, fresh); err != nil {
		t.Fatal(err)
	}

	s := openTempStore(t)
	r := New(s, qoder.New(home))
	r.SettleIdle = 5 * time.Minute
	res, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", res.Sessions)
	}
	if res.Cards != 1 {
		t.Fatalf("cards = %d, want 1（未写完的会话不该出卡）", res.Cards)
	}
	if res.SkippedUnsettled != 1 {
		t.Fatalf("skipped = %d, want 1", res.SkippedUnsettled)
	}

	goal, err := s.QueryStrings(context.Background(), `SELECT goal FROM task_card`)
	if err != nil {
		t.Fatal(err)
	}
	if len(goal) != 1 || goal[0] != "给扣减加乐观锁" {
		t.Fatalf("goals = %v", goal)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM session_event WHERE kind = 'tool_edit'`); got != 1 {
		t.Fatalf("edit events = %d, want 1", got)
	}

	// 第二次扫描：内容没变的会话整批跳过，卡片不重复产出
	second, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionUnchanged != 2 || second.Cards != 0 {
		t.Fatalf("second run = %+v, want unchanged sessions and no new cards", second)
	}
}

func TestRun_SessionProjectMatchesMemoryProject(t *testing.T) {
	home, repoA, _ := fakeQoder(t)
	// 同一个 slug 目录下既有记忆也有会话：两者必须归到同一个 project_id
	write(t, filepath.Join(home, "projects", "RA", "s1.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repoA)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"目标\"}}\n")
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(home, "projects", "RA", "s1.jsonl"), settled, settled); err != nil {
		t.Fatal(err)
	}

	s := openTempStore(t)
	if _, err := New(s, qoder.New(home)).Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryStrings(context.Background(), `
SELECT DISTINCT (SELECT key FROM project WHERE id = m.project_id)
FROM memory_item m WHERE m.scope = 'project' AND m.project_id <> 0`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("project keys from memory = %v, want exactly one", rows)
	}
	shared, err := s.QueryStrings(context.Background(), `
SELECT DISTINCT s.project_id FROM session s
JOIN memory_item m ON m.project_id = s.project_id
WHERE m.scope = 'project' AND m.project_id <> 0`)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 1 {
		t.Fatalf("session and memory must share a project, got %v", shared)
	}
}

func TestRun_SessionSourceErrorIsRecorded(t *testing.T) {
	home, repoA, _ := fakeQoder(t)
	// 无 workspace-directories 的会话：适配器给出带 Err 的单元，ingest 记错但不中断
	write(t, filepath.Join(home, "projects", "RA", "orphan.jsonl"),
		"{\"type\":\"user\",\"sessionId\":\"o\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"x\"}}\n")
	s := openTempStore(t)
	res, err := New(s, qoder.New(home)).Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions < 1 || len(res.Errors) == 0 {
		t.Fatalf("want a recorded session error: %+v", res)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM source WHERE kind = 'session' AND last_error <> ''`); got == 0 {
		t.Fatal("session error not persisted")
	}
	_ = repoA
}

func TestResult_StringCoversSessionCounts(t *testing.T) {
	got := Result{Units: 1, Created: 1, Sessions: 2, Cards: 1, SkippedUnsettled: 1}.String()
	for _, want := range []string{"sessions=2", "cards=1", "unsettled=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ingest/ -v`
Expected: FAIL，`res.Sessions undefined`

- [ ] **Step 3: 改 `internal/ingest/ingest.go`**

`Result` 增加会话字段，`String` 一并覆盖：

```go
type Result struct {
	Units     int
	Created   int
	Merged    int
	Unchanged int
	Staled    int

	Sessions          int
	SessionUnchanged  int
	Cards             int
	SkippedUnsettled  int

	Errors []string
}

func (r Result) String() string {
	base := fmt.Sprintf("units=%d created=%d merged=%d unchanged=%d staled=%d sessions=%d sessions_unchanged=%d cards=%d unsettled=%d",
		r.Units, r.Created, r.Merged, r.Unchanged, r.Staled,
		r.Sessions, r.SessionUnchanged, r.Cards, r.SkippedUnsettled)
	if len(r.Errors) == 0 {
		return base
	}
	return base + fmt.Sprintf(" errors=%d", len(r.Errors))
}
```

`Runner` 增加可调的空闲阈值（测试与 M3 的 watch 周期都用它）：

```go
type Runner struct {
	store      *store.Store
	adapters   []agent.Adapter
	RemoteReader func(dir string) (string, bool, error)
	// SettleIdle 是「会话算写完」的空闲阈值，默认 5 分钟。
	SettleIdle time.Duration
}

func New(st *store.Store, adapters ...agent.Adapter) *Runner {
	return &Runner{
		store:        st,
		adapters:     adapters,
		RemoteReader: normalize.GitRemoteFromConfig,
		SettleIdle:   5 * time.Minute,
	}
}
```

把 `projectFor` 换成按参数解析的版本（记忆与会话共用一条归属逻辑）：

```go
func (r *Runner) projectForDraft(ctx context.Context, d domain.MemoryDraft, at time.Time) (int64, error) {
	if d.Scope != domain.ScopeProject {
		return 0, nil
	}
	return r.resolveProject(ctx, d.SourceAgent, d.ProjectSlug, d.ProjectPath, d.Confidence, at)
}

// resolveProject 见 Task 13 的 projectFor：低置信保持独立项目，
// 高置信先按路径查已有项目，再读 git remote 建或合并。
func (r *Runner) resolveProject(ctx context.Context, agent, slug, path, confidence string, at time.Time) (int64, error) {
	if confidence != "high" || path == "" {
		key := agent + ":" + slug
		return r.store.EnsureProject(ctx, key, slug, "", "", at)
	}
	if id, ok, err := r.store.ResolveProjectByPath(ctx, path); err != nil {
		return 0, err
	} else if ok {
		return id, nil
	}
	remote, _, err := r.RemoteReader(path)
	if err != nil {
		remote = ""
	}
	id, _, err := r.store.ProjectIDForPath(ctx, agent, slug, path, remote, at)
	return id, err
}
```

M1 的 `Run` 里那句 `projectID, err := r.projectFor(ctx, u.Drafts, at)` 改成调用 `r.projectForDraft(ctx, u.Drafts, at)`——语义完全一致，只是现在同一份归属逻辑也被会话分支复用。原 `projectFor` 的函数体删掉，不要留两个版本。

在 `Run` 的适配器循环里，记忆处理之后追加会话处理：

```go
		sUnits, err := a.ScanSessionUnits(ctx)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s sessions: %v", a.Name(), err))
			continue
		}
		for _, u := range sUnits {
			res.Sessions++
			if err := ctx.Err(); err != nil {
				return res, err
			}

			if u.Err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", u.Path, u.Err))
				if err := r.store.MarkSourceError(ctx, a.Name(), "session", u.Path, u.Err.Error(), runAt); err != nil {
					return res, err
				}
				continue
			}

			hash := normalize.HashBytes(u.Content)
			prev, seen, err := r.store.SourceHash(ctx, a.Name(), "session", u.Path)
			if err != nil {
				return res, err
			}
			if seen && prev == hash {
				res.SessionUnchanged++
				if err := r.store.TouchSource(ctx, a.Name(), "session", u.Path, runAt); err != nil {
					return res, err
				}
				continue
			}

			projectID, err := r.resolveProject(ctx, a.Name(), u.Session.ProjectSlug, u.Session.ProjectPath, u.Session.Confidence, at)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: project: %v", u.Path, err))
				continue
			}

			sess := u.Session
			sess.Agent = a.Name()
			sess.ProjectID = projectID
			sessID, err := r.store.UpsertSession(ctx, sess)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", u.Path, err))
				continue
			}

			events := agent.EventsWithSession(sessID, u.Events)
			if err := r.store.ReplaceEvents(ctx, sessID, events); err != nil {
				return res, err
			}

			if extract.Settled(u.MTime, time.Now(), r.SettleIdle) {
				card, ok := extract.TaskCard(sess, events, time.Now())
				if ok {
					card.SessionID = sessID
					card.ProjectID = projectID
					if _, err := r.store.UpsertTaskCard(ctx, card); err != nil {
						res.Errors = append(res.Errors, fmt.Sprintf("%s: card: %v", u.Path, err))
					} else {
						res.Cards++
					}
				}
			} else {
				res.SkippedUnsettled++
			}

			if err := r.store.PutSource(ctx, domain.SourceState{
				Agent: a.Name(), Kind: "session", Path: u.Path,
				ContentHash: hash, MTime: u.MTime, Size: u.Size,
			}, runAt); err != nil {
				return res, err
			}
		}
```

`ingest.go` 的 import 追加 `"github.com/muyi99/agent-memory/internal/extract"` 与 `"time"`。

- [ ] **Step 4: 把 `agent.SessionEvent` 转成 `domain.SessionEvent`**

`agent.SessionEvent` 故意没有 `SessionID`（Task 17 的决定：适配器不关心库主键），所以需要一个转换函数。放在 `agent` 包里而不是 `domain`：`agent` 已经 import `domain`，反向放会成环。

在 `internal/agent/adapter.go` 追加：

```go
// EventsWithSession 给适配器产出的事件补上库主键。
// 转换只在这里发生：适配器不知道 id，store 不知道适配器结构。
func EventsWithSession(sessionID int64, in []SessionEvent) []domain.SessionEvent {
	out := make([]domain.SessionEvent, 0, len(in))
	for _, e := range in {
		out = append(out, domain.SessionEvent{
			SessionID: sessionID,
			Seq:       e.Seq,
			Role:      e.Role,
			Kind:      e.Kind,
			Text:      e.Text,
			Tool:      e.Tool,
			Files:     e.Files,
			Ts:        e.Ts,
		})
	}
	return out
}
```

于是 `ingest.go` 里的事件转换段替换为：

```go
			events := agent.EventsWithSession(sessID, u.Events)
			if err := r.store.ReplaceEvents(ctx, sessID, events); err != nil {
				return res, err
			}
```

`extract.TaskCard(sess, events, ...)` 收的也是这份 `[]domain.SessionEvent`。

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/... -v 2>&1 | tail -30`
Expected: PASS

- [ ] **Step 6: 配置追加简报参数——先在 `internal/config/config_test.go` 加失败测试**

```go
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
}
```

- [ ] **Step 7: 实现配置段**

```go
type HandoffConfig struct {
	BudgetTokens int           `toml:"budget_tokens"`
	RecentCards  int           `toml:"recent_cards"`
	SettleIdle   time.Duration `toml:"settle_idle"`
}
```

`Config` 结构加 `Handoff HandoffConfig \`toml:"handoff"\``；`Default` 里给 `Handoff{BudgetTokens: 1500, RecentCards: 3, SettleIdle: 5 * time.Minute}`；`Load` 末尾补零值兜底：

```go
	if c.Handoff.BudgetTokens <= 0 {
		c.Handoff.BudgetTokens = Default(userHome).Handoff.BudgetTokens
	}
	if c.Handoff.RecentCards <= 0 {
		c.Handoff.RecentCards = Default(userHome).Handoff.RecentCards
	}
	if c.Handoff.SettleIdle <= 0 {
		c.Handoff.SettleIdle = Default(userHome).Handoff.SettleIdle
	}
```

- [ ] **Step 8: 跑测试确认通过**

Run: `go test ./internal/config/ -v`
Expected: PASS

时长的 TOML 写法已在本机实测确认：`BurntSushi/toml` v1.3.2 原生处理 `time.Duration`——`Encode` 写成 `"5m"` 这样的字符串（encode.go:249），`Decode` 接受字符串时长，整数则按纳秒解释（decode.go:82）。所以配置文件里直接写：

```toml
[handoff]
budget_tokens = 1500
recent_cards = 3
settle_idle = "5m"
```

`TestSaveThenLoad_RoundTrips` 里对 `SettleIdle` 的断言可以照 `Scan.Interval` 的既有写法直接比 `time.Duration`。

- [ ] **Step 9: 提交**

```bash
git add internal/ingest internal/domain internal/agent internal/config
git commit -m "feat: 扫描接入会话与任务卡，新增简报配置段"
```

---

## Task 25: handoff 命令与端到端验收

**Files:**
- Create: `internal/cli/handoff.go`
- Modify: `internal/cli/doctor.go`
- Create: `internal/cli/handoff_test.go`

- [ ] **Step 1: 写失败测试 `internal/cli/handoff_test.go`**

```go
package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedProjectWithSession(t *testing.T, home, qoderHome string) (projectDir, slug string) {
	t.Helper()
	projectDir = filepath.Join(home, "code", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	slug = "demo"
	write(t, filepath.Join(qoderHome, "memory", "user.md"),
		"---\nmetadata:\n  type: user\n---\n\n沟通用中文。\n")
	write(t, filepath.Join(qoderHome, "projects", slug, "memory", "m.md"),
		"---\nmetadata:\n  type: feedback\n---\n\n余额扣减需条件更新。\n")
	session := filepath.Join(qoderHome, "projects", slug, "s1.jsonl")
	write(t, session,
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", projectDir)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"给扣减加乐观锁\"}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:01:00Z\",\"message\":{\"role\":\"assistant\",\"content\":\"未解决：字段类型不一致\"}}\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(session, old, old); err != nil {
		t.Fatal(err)
	}
	return projectDir, slug
}

func TestHandoff_WritesFileIntoProjectAndIsReused(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}

	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir); code != 0 {
		t.Fatalf("handoff exit %d: %s", code, out.String())
	}
	path := filepath.Join(projectDir, ".agent-memory", "HANDOFF.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("handoff file not written: %v", err)
	}
	text := string(body)
	for _, want := range []string{"## 用户偏好", "## 项目硬约束与约定", "给扣减加乐观锁", "沟通用中文", "余额扣减需条件更新"} {
		if !strings.Contains(text, want) {
			t.Fatalf("brief missing %q:\n%s", want, text)
		}
	}

	// 第二次生成内容不变：mtime 不应被刷新（避免 fsnotify 空转）
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := st.ModTime()
	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir); code != 0 {
		t.Fatalf("second handoff exit %d", code)
	}
	if !strings.Contains(out.String(), "未变化") {
		t.Fatalf("want 未变化 report, got %q", out.String())
	}
	st2, _ := os.Stat(path)
	if !st2.ModTime().Equal(before) {
		t.Fatalf("file rewritten with identical content")
	}
}

func TestHandoff_AddsProjectDirToGitExclude(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)
	gitInfo := filepath.Join(projectDir, ".git", "info")
	if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	run(t, &out, "scan")
	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir); code != 0 {
		t.Fatalf("handoff exit %d: %s", code, out.String())
	}
	data, err := os.ReadFile(filepath.Join(gitInfo, "exclude"))
	if err != nil {
		t.Fatalf("exclude not maintained: %v", err)
	}
	if !strings.Contains(string(data), "/.agent-memory/") {
		t.Fatalf("exclude = %q", data)
	}
}

func TestHandoff_PrintToStdoutWithoutWriting(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	run(t, &out, "scan")
	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir, "--print"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "## 项目概况") {
		t.Fatalf("print mode should emit the brief:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".agent-memory", "HANDOFF.md")); !os.IsNotExist(err) {
		t.Fatal("--print must not touch the project directory")
	}
}

func TestHandoff_UnknownProjectAsksForScan(t *testing.T) {
	isolateHomeWithRepo(t)
	var out bytes.Buffer
	code := run(t, &out, "handoff", "--project", filepath.Join(t.TempDir(), "never-scanned"))
	if code == 0 {
		t.Fatal("want failure for an unknown project")
	}
	if !strings.Contains(out.String(), "scan") {
		t.Fatalf("error should tell the user to scan first:\n%s", out.String())
	}
}

func TestDoctorReportsSessionsAndCards(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	run(t, &out, "scan")
	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	for _, want := range []string{"会话", "任务卡"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor missing %q:\n%s", want, out.String())
		}
	}
}
```

测试用到两个新辅助函数。`write(t, path, content)` 在 M1 的 cli 集成测试里没有（M1 用的是 `mustWrite`），本任务把 `isolateHome` 扩成带仓库参数的版本，并复用 `mustWrite` 写文件：

```go
// internal/cli/handoff_test.go 顶部补上（isolateHomeWithRepo 在 isolation 目录里
// 放一个真实的 git 仓库目录，Qoder home 与它同级）
func isolateHomeWithRepo(t *testing.T) (home, qoderHome string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	qoderHome = filepath.Join(home, ".qoder-cn")
	mustMkdir(t, filepath.Join(qoderHome, "memory"))
	mustMkdir(t, filepath.Join(qoderHome, "projects", "demo", "memory"))
	return home, qoderHome
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mustWrite(t, path, content)
}
```

`mustMkdir` 与 `mustWrite` 在 M1 的 `integration_test.go` 里已存在，直接复用。项目记忆必须放在 `<slug>/memory/` 下——那是 Qoder 的真实布局，fixture 放错位置扫描器看不到它，测试会假绿。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run 'TestHandoff|TestDoctor' -v`
Expected: FAIL，`unknown command "handoff"` 之类的断言失败

- [ ] **Step 3: 写 `internal/cli/handoff.go`**

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/store"
	"github.com/muyi99/agent-memory/internal/surface"
)

func init() {
	register(command{name: "handoff", desc: "生成/打印项目交接简报", run: cmdHandoff})
}

func cmdHandoff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectRef := fs.String("project", "", "项目绝对路径，默认当前工作目录")
	printOnly := fs.Bool("print", false, "只打印到标准输出，不写项目文件")
	budget := fs.Int("budget", 0, "简报 token 预算，0 表示用配置里的值")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	target := *projectRef
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return reportError(stderr, err)
		}
		target = cwd
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return reportError(stderr, err)
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	projectID, ok, err := e.st.ResolveProjectByPath(ctx, abs)
	if err != nil {
		return reportError(stderr, err)
	}
	if !ok {
		fmt.Fprintf(stderr, "error: 不认识这个项目: %s\n先跑 agent-memory scan，或确认该目录里有 agent 记录\n", abs)
		return 1
	}

	cfg := compose.Config{BudgetTokens: e.cfg.Handoff.BudgetTokens, RecentCards: e.cfg.Handoff.RecentCards}
	if *budget > 0 {
		cfg.BudgetTokens = *budget
	}
	res, err := compose.New(e.st, cfg).Render(ctx, projectID)
	if err != nil {
		return reportError(stderr, err)
	}

	if *printOnly {
		fmt.Fprint(stdout, res.Body)
		return 0
	}

	path, changed, err := surface.WriteHandoff(abs, res.Body)
	if err != nil {
		return reportError(stderr, err)
	}
	if changed {
		if _, err := surface.EnsureGitExclude(abs, "/"+surface.DirName+"/"); err != nil {
			return reportError(stderr, err)
		}
	}
	if _, err := e.st.SaveHandoff(ctx, store.HandoffRow{
		ProjectID: projectID, Body: res.Body, ContentHash: res.ContentHash,
		BudgetTokens: cfg.BudgetTokens, GeneratedAt: time.Now(),
	}); err != nil {
		return reportError(stderr, err)
	}

	state := "已更新"
	if !changed {
		state = "未变化"
	}
	fmt.Fprintf(stdout, "%s %s tokens=%d budget=%d truncated=%v\n", state, path, res.Tokens, cfg.BudgetTokens, res.Truncated)
	return 0
}
```

`handoff` 只读库、只写项目的 `.agent-memory/`，不碰任何 agent 原生文件——这条边界和 `scan` 一样。

- [ ] **Step 4: 让 `scan` 之后自动生成简报**

`scan` 结束时对每个「有任务卡或项目级记忆」的项目刷新一次 `HANDOFF.md`，这是「切 agent 不用重新描述」的落地条件——不靠用户记得手动跑 `handoff`。在 `internal/cli/scan.go` 的 `res` 打印之前插入：

```go
		handoffReport := refreshHandoffs(ctx, e, *agentFilter)
		for _, line := range handoffReport {
			fmt.Fprintf(stdout, "  %s\n", line)
		}
```

并在 `internal/cli/handoff.go` 里实现它：

```go
// refreshHandoffs 给每个有内容的项目重写简报。
// 单个项目失败只记录，不影响其他项目——一个坏路径不该让整次扫描少交付。
func refreshHandoffs(ctx context.Context, e *env, agentFilter string) []string {
	ids, err := e.st.ProjectIDsForHandoff(ctx)
	if err != nil {
		return []string{"handoff: " + err.Error()}
	}
	cfg := compose.Config{BudgetTokens: e.cfg.Handoff.BudgetTokens, RecentCards: e.cfg.Handoff.RecentCards}
	var report []string
	for _, id := range ids {
		project, ok, err := e.st.ProjectByID(ctx, id)
		if err != nil || !ok || project.PrimaryPath == "" {
			continue
		}
		if _, err := os.Stat(project.PrimaryPath); err != nil {
			// 目录已不存在：库里留着项目记录，但不去磁盘上建目录
			continue
		}
		res, err := compose.New(e.st, cfg).Render(ctx, id)
		if err != nil {
			report = append(report, fmt.Sprintf("handoff %s: %v", project.Key, err))
			continue
		}
		path, changed, err := surface.WriteHandoff(project.PrimaryPath, res.Body)
		if err != nil {
			report = append(report, fmt.Sprintf("handoff %s: %v", project.Key, err))
			continue
		}
		if changed {
			if _, err := surface.EnsureGitExclude(project.PrimaryPath, "/"+surface.DirName+"/"); err != nil {
				report = append(report, fmt.Sprintf("handoff %s: exclude: %v", project.Key, err))
			}
		}
		if _, err := e.st.SaveHandoff(ctx, store.HandoffRow{
			ProjectID: id, Body: res.Body, ContentHash: res.ContentHash,
			BudgetTokens: cfg.BudgetTokens, GeneratedAt: time.Now(),
		}); err != nil {
			report = append(report, fmt.Sprintf("handoff %s: %v", project.Key, err))
			continue
		}
		if changed {
			report = append(report, fmt.Sprintf("handoff %s 已更新 %s tokens=%d", project.Key, path, res.Tokens))
		}
	}
	return report
}
```

import 追加 `"time"`。`internal/store/project.go` 里加查询：

```go
// ProjectIDsForHandoff 列出值得一份简报的项目：有项目级记忆或有任务卡的那些，
// 按 id 升序保证多次运行产出顺序一致。
func (s *Store) ProjectIDsForHandoff(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT id FROM (
  SELECT project_id AS id FROM memory_item WHERE scope = 'project' AND project_id <> 0 AND stale = 0
  UNION SELECT project_id FROM task_card WHERE project_id <> 0
) ORDER BY id`)
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

- [ ] **Step 5: doctor 增加会话与任务卡统计**

`internal/cli/doctor.go` 的统计段之后追加：

```go
	sessions, err := e.st.QueryInt(ctx, `SELECT count(*) FROM session`)
	if err != nil {
		return reportError(stderr, err)
	}
	cards, err := e.st.QueryInt(ctx, `SELECT count(*) FROM task_card`)
	if err != nil {
		return reportError(stderr, err)
	}
	handoffs, err := e.st.QueryInt(ctx, `SELECT count(*) FROM handoff`)
	if err != nil {
		return reportError(stderr, err)
	}
	byStatus, err := e.st.QueryStrings(ctx, `SELECT status || '=' || COUNT(*) FROM task_card GROUP BY status ORDER BY status`)
	if err != nil {
		return reportError(stderr, err)
	}
	fmt.Fprintf(stdout, "会话 %d，任务卡 %d，简报 %d（状态: %v）\n", sessions, cards, handoffs, byStatus)
```

- [ ] **Step 6: 跑全部测试**

Run: `go test ./... 2>&1 | tail -20`
Expected: PASS

- [ ] **Step 7: 静态检查与手工冒烟**

Run: `gofmt -l . && go vet ./...`
Expected: 无输出

Run: `go run ./cmd/agent-memory help`
Expected: 命令列表里出现 `handoff`

- [ ] **Step 8: 提交**

```bash
git add internal/cli internal/store/project.go
git commit -m "feat: handoff 命令、扫描后自动刷新简报与 doctor 会话统计"
```

---

## Task 26: 真实数据验收

这一步会读真实的 `~/.qoder-cn` 与真实项目目录，并且会往项目里写 `.agent-memory/HANDOFF.md`。执行前确认你愿意在那个目录留下这个子目录（它会被加进 `.git/info/exclude`，不进仓库）。

- [ ] **Step 1: 记录原生文件基线**

```bash
find ~/.qoder-cn -name '*.md' -print0 2>/dev/null | xargs -0 md5sum > /tmp/m2-before-md.txt
find ~/.qoder-cn -name '*.jsonl' -print0 2>/dev/null | xargs -0 md5sum > /tmp/m2-before-jsonl.txt
wc -l /tmp/m2-before-md.txt /tmp/m2-before-jsonl.txt
```

- [ ] **Step 2: 全量扫描并观察会话导入**

```bash
go run ./cmd/agent-memory scan
go run ./cmd/agent-memory scan
go run ./cmd/agent-memory doctor
```

Expected: 第二次 `scan` 里 `unchanged=` 与 `sessions_unchanged=` 都覆盖到全部源；`doctor` 打印出「会话 N，任务卡 M」且 N>0。

- [ ] **Step 3: 挑一个真实项目看简报**

```bash
go run ./cmd/agent-memory handoff --project "E:\\company\\nuonuo-fresh3-api" --print
```

Expected: 六个小节齐全；「最近任务」里能看到真实的 goal；预算内且 `truncated=false`。这是整个 M1+M2 的判定性输出——内容你自己认不认得。

- [ ] **Step 4: 落盘并二次确认不重写**

```bash
go run ./cmd/agent-memory handoff --project "E:\\company\\nuonuo-fresh3-api"
ls -la "E:/company/nuonuo-fresh3-api/.agent-memory"
go run ./cmd/agent-memory handoff --project "E:\\company\\nuonuo-fresh3-api"
```

Expected: 第二次输出含「未变化」，且 `.git/info/exclude` 里有 `/.agent-memory/` 恰好一行。

- [ ] **Step 5: 确认原生文件未被改动**

```bash
find ~/.qoder-cn -name '*.md' -print0 2>/dev/null | xargs -0 md5sum > /tmp/m2-after-md.txt
find ~/.qoder-cn -name '*.jsonl' -print0 2>/dev/null | xargs -0 md5sum > /tmp/m2-after-jsonl.txt
diff /tmp/m2-before-md.txt /tmp/m2-after-md.txt && diff /tmp/m2-before-jsonl.txt /tmp/m2-after-jsonl.txt && echo NATIVE_UNTOUCHED
```

Expected: `NATIVE_UNTOUCHED`

- [ ] **Step 6: 更新 README 并提交**

在 `README.md` 的「现在能做什么」段落追加：

```markdown
- 导入 Qoder 会话历史，提炼成任务卡（目标 / 状态 / 改动文件 / 下一步 / 未决问题）
- 每个项目生成 `.agent-memory/HANDOFF.md`：切到别的 agent 时先读它，不用重新描述任务
- `agent-memory handoff --project <路径> --print` 随时重看简报
```

```bash
git add README.md
git commit -m "docs: README 补充任务卡与交接简报用法"
```

---

## 完成标准（M2）

1. `go test ./...`、`go vet ./...`、`gofmt -l .` 全干净。
2. 未写完的会话（mtime 在阈值内）不产出任务卡；写完的产出，且第二次扫描不重复生成。
3. 简报对同一库状态是逐字节确定的：连续两次 `handoff` 第二次报「未变化」。
4. 预算收紧时按「卡片细节 → 卡片数量 → 未决问题」的顺序裁剪，用户偏好与项目硬约束永不被砍。
5. 真实项目里 `HANDOFF.md` 的内容你自己认可——认可它就是 M3 的交付形态。

<!-- APPEND-HERE -->
