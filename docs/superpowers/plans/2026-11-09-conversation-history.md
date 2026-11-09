# 完整对话存储与检索 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 新增 `session_message` 完整对话表（自动增量更新），MCP 提供 `conversation_search` / `conversation_get`，任务详情「加载更早」改读库。

**Architecture:** 在 adapter 层加 `ParseTranscript`，ingest 在会话导入后整场重写消息；store 提供替换/分页/FTS 检索；daemon 端点与 MCP 工具共用 store 查询。

**Tech Stack:** Go 1.26、SQLite FTS5（`unicode61`，中文二元组复用 `normalize.QueryTokens`）。

**Spec:** `docs/superpowers/specs/2026-11-09-conversation-history-design.md`

---

### Task 1: 类型与 Qoder 完整对话解析

**Files:**
- Modify: `internal/agent/adapter.go`
- Create: `internal/agent/qoder/transcript.go`
- Test: `internal/agent/qoder/transcript_test.go`

- [ ] **Step 1: 写失败测试**

```go
package qoder

import (
	"strings"
	"testing"
)

func TestParseTranscriptKeepsFullTextAndToolSummary(t *testing.T) {
	long := strings.Repeat("长", 3000)
	content := strings.Join([]string{
		`{"type":"user","timestamp":"2026-11-09T09:00:00Z","message":{"role":"user","content":"` + long + `"}}`,
		`{"type":"assistant","timestamp":"2026-11-09T09:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"好的"},{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","timestamp":"2026-11-09T09:00:02Z","message":{"role":"user","content":[{"type":"tool_result","is_error":true,"content":"FAIL boom"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-09T09:00:03Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}`,
	}, "\n")
	msgs, err := parseTranscript([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("want 4 msgs got %d: %+v", len(msgs), msgs)
	}
	if len([]rune(msgs[0].Text)) != 3000 {
		t.Fatalf("user text must be full, got %d runes", len([]rune(msgs[0].Text)))
	}
	if msgs[1].Kind != "text" || msgs[2].Kind != "tool" || msgs[2].Tool != "Bash" || msgs[2].Text != "go test ./..." {
		t.Fatalf("assistant blocks wrong: %+v", msgs[1:3])
	}
	if msgs[3].Kind != "tool_error" || !strings.Contains(msgs[3].Text, "FAIL boom") {
		t.Fatalf("error result wrong: %+v", msgs[3])
	}
}
```

- [ ] **Step 2: 跑红** `go test ./internal/agent/qoder/ -run TestParseTranscript`

- [ ] **Step 3: 实现**

在 `internal/agent/adapter.go` 加：

```go
// TranscriptMessage 是一整场会话里的一条完整对话消息。
type TranscriptMessage struct {
	Seq       int
	Role      string
	Kind      string // text / tool / tool_error
	Text      string
	Tool      string
	Files     []string
	Ts        time.Time
	Truncated bool
}

// TranscriptParser 是适配器的可选能力：整场会话 → 完整消息。
type TranscriptParser interface {
	ParseTranscript(path string) ([]TranscriptMessage, error)
}
```

`internal/agent/qoder/transcript.go`：

```go
package qoder

import (
	"os"

	"github.com/muyi99/agent-memory/internal/agent"
)

const (
	transcriptMaxText = 100000
	transcriptMaxTool = 2000
	transcriptMaxErr  = 4000
)

// ParseTranscript 整场会话 → 完整消息；消息全文不截断（单条 100k 上限）。
func (a *Adapter) ParseTranscript(path string) ([]agent.TranscriptMessage, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseTranscript(content)
}

func parseTranscript(content []byte) ([]agent.TranscriptMessage, error) {
	lines := splitJSONLines(content)
	var out []agent.TranscriptMessage
	seq := 0
	for _, l := range lines {
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		role := l.Message.Role
		if role == "" {
			role = l.Type
		}
		var blocks []contentBlock
		if jsonUnmarshal(l.Message.Content, &blocks) != nil {
			var s string
			if jsonUnmarshal(l.Message.Content, &s) == nil && s != "" {
				seq++
				out = append(out, agent.TranscriptMessage{Seq: seq, Role: role, Kind: "text", Text: clipText(s, transcriptMaxText), Ts: parseTimestamp(l.Timestamp), Truncated: len([]rune(s)) > transcriptMaxText})
			}
			continue
		}
		for _, b := range blocks {
			ts := parseTimestamp(l.Timestamp)
			switch b.Type {
			case "text":
				if b.Text == "" {
					continue
				}
				seq++
				out = append(out, agent.TranscriptMessage{Seq: seq, Role: role, Kind: "text", Text: clipText(b.Text, transcriptMaxText), Ts: ts, Truncated: len([]rune(b.Text)) > transcriptMaxText})
			case "tool_use":
				seq++
				out = append(out, agent.TranscriptMessage{Seq: seq, Role: role, Kind: "tool", Tool: b.Name, Text: clipText(toolSummary(b.Name, b.Input), transcriptMaxTool), Files: filesFromInput(b.Input), Ts: ts})
			case "tool_result":
				if !b.IsError {
					continue
				}
				seq++
				out = append(out, agent.TranscriptMessage{Seq: seq, Role: role, Kind: "tool_error", Text: clipText(textFromContent(b.Content), transcriptMaxErr), Ts: ts, Truncated: len([]rune(textFromContent(b.Content))) > transcriptMaxErr})
			}
		}
	}
	return out, nil
}

func toolSummary(name string, input []byte) string {
	switch name {
	case "Bash":
		return commandFromInput(input)
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		if files := filesFromInput(input); len(files) > 0 {
			return strings.Join(files, ", ")
		}
	}
	return clipText(string(input), transcriptMaxTool)
}
```

（`splitJSONLines`、`jsonUnmarshal` 用 `bufio.Scanner`/`json.Unmarshal` 实现；`clipText` 与 `clipRunes` 相同。）

- [ ] **Step 4: 跑绿 + commit**

---

### Task 2: WorkBuddy 完整对话解析

**Files:** Create `internal/agent/workbuddy/transcript.go`，Test 同名 `_test.go`

- [ ] **Step 1: 测试**：fixture 三行 `message`(user 文本) / `function_call`(name=run_command, arguments=`"{\"command\":\"ls\"}"` 字符串) / `function_call_result`(status=failed, output=`"boom"`)，断言 3 条、kind 与文本正确。
- [ ] **Step 2: 实现 `func (a *Adapter) ParseTranscript(path string) ([]agent.TranscriptMessage, error)`**：流式扫描，`message` → text 全文；`function_call` → kind=tool、text=arguments 解析出的 command 或原始 JSON 截 2000；`function_call_result` 且 `status != "" && status != "completed"` → kind=tool_error、output 截 4000。
- [ ] **Step 3: 跑绿 + commit**

---

### Task 3: schema v4 + 消息仓储

**Files:** Modify `internal/store/schema.go`、`store.go`；Create `internal/store/message.go`、`message_test.go`

- [ ] **Step 1: 测试**（替换、截断、FTS、分页）

```go
func TestReplaceMessagesAndPage(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/msg", "msg", "", `F:\msg`, now)
	sid, _ := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "m1", ProjectID: pid, Path: `C:\m1.jsonl`, UpdatedAt: now})
	msgs := []domain.SessionMessage{
		{SessionID: sid, Seq: 1, Role: "user", Kind: "text", Text: "给扣减加乐观锁", Ts: now},
		{SessionID: sid, Seq: 2, Role: "assistant", Kind: "text", Text: "好了", Ts: now.Add(time.Minute)},
		{SessionID: sid, Seq: 3, Role: "assistant", Kind: "tool", Tool: "Bash", Text: "go test ./...", Ts: now.Add(2 * time.Minute)},
	}
	if err := s.ReplaceMessages(ctx, sid, msgs); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceMessages(ctx, sid, msgs[:1]); err != nil { // 会话被截断
		t.Fatal(err)
	}
	page, next, err := s.MessagesForSession(ctx, sid, 0, 10)
	if err != nil || len(page) != 1 || next != 0 {
		t.Fatalf("truncate failed: %v %d %d", err, len(page), next)
	}
	_ = s.ReplaceMessages(ctx, sid, msgs)
	hits, err := s.SearchMessages(ctx, "乐观锁", 0, 0, 10)
	if err != nil || len(hits) != 1 || hits[0].Seq != 1 {
		t.Fatalf("fts miss: %v %+v", err, hits)
	}
}
```

- [ ] **Step 2: schema v4**：`schemaVersion = "4"`；表与 FTS 见 spec；`store.Open` 允许 `ver == "3"`（3→4 只建表）。加 `domain.SessionMessage` 类型。
- [ ] **Step 3: 实现 `message.go`**

```go
func (s *Store) ReplaceMessages(ctx context.Context, sessionID int64, in []domain.SessionMessage) error
func (s *Store) HasMessages(ctx context.Context, sessionID int64) (bool, error)
func (s *Store) MessagesForSession(ctx context.Context, sessionID, beforeSeq int64, limit int) ([]domain.SessionMessage, int64, error)
func (s *Store) MainSessionForTask(ctx context.Context, taskID int64) (int64, error) // task.session_id，fallback 最近关联
func (s *Store) SearchMessages(ctx context.Context, query string, projectID, taskID int64, limit int) ([]domain.MessageHit, error)
```

ReplaceMessages：事务内 `DELETE FROM session_message WHERE session_id=?` + `DELETE FROM session_message_fts WHERE rowid IN (...)`（先查旧 id）+ 插入 + FTS 插入（text_seg = `strings.Join(normalize.Segment(text), " ")`）。
MessagesForSession：`seq < beforeSeq`（beforeSeq=0 不限）倒序 limit 再反转；`next` = 本页最小 seq - 1（若到头 0）。
SearchMessages：`session_message_fts MATCH normalize.MatchExpr(...)` join session_message/session，可选 project/task 过滤，bm25 排序。

- [ ] **Step 4: 跑绿 + commit**

---

### Task 4: ingest 写入完整消息

**Files:** Modify `internal/ingest/ingest.go`、`sync.go`；Test `internal/ingest/message_test.go`

- [ ] **Step 1: 测试**：fakeQoder 写入带长文本的会话 → `Run` → `HasMessages(sessID)` true 且条数 = 2。
- [ ] **Step 2: 实现**：会话导入（changed 路径与 SyncSession）在 `ReplaceEvents` 后：

```go
if tp, ok := a.(agent.TranscriptParser); ok {
	if msgs, err := tp.ParseTranscript(u.Path); err == nil {
		storeMsgs := make([]domain.SessionMessage, 0, len(msgs))
		for _, m := range msgs {
			storeMsgs = append(storeMsgs, domain.SessionMessage{SessionID: sessID, Seq: m.Seq, Role: m.Role, Kind: m.Kind, Text: m.Text, Tool: m.Tool, Files: m.Files, Ts: m.Ts, Truncated: m.Truncated})
		}
		if err := r.store.ReplaceMessages(ctx, sessID, storeMsgs); err != nil { return res, err }
	}
}
```

（SyncSession 同样处理；解析失败只记录不阻断。）
- [ ] **Step 3: 跑绿 + commit**

---

### Task 5: 端点改读库 + MCP 两个工具

**Files:** Modify `internal/daemon/conversation.go`、`mcp.go`、`server.go`、`internal/mcp/tools.go`；Test `internal/daemon/conversation_test.go`、`mcp_sync_test.go`

- [ ] **Step 1: 测试**：`GET /v1/task/conversation?task_id=&before_seq=` 在有消息时 `source=db` 并分页；`conversation_search` / `conversation_get` 经 `Server` 方法返回命中。
- [ ] **Step 2: 端点**：先 `MainSessionForTask` → `MessagesForSession`，返回 `{source:"db", messages, next}`；无消息时回退现有 tailreader 逻辑（`source:"file"`/`store_only`）。
- [ ] **Step 3: MCP**：`ToolAPI` 加 `ConversationSearch(ctx, ConversationSearchInput)`、`ConversationGet(ctx, ConversationGetInput)`；tools.go 注册 `conversation_search`（`query` 必填，`project_path`/`task_id`/`limit` 可选）与 `conversation_get`（`task_id` 必填，`before_seq`/`limit` 可选）；daemon 实现（搜索时 project_path→projectID）。
- [ ] **Step 4: 跑绿 + commit**

---

### Task 6: UI 改游标 + 文档 + 真实验收

**Files:** Modify `internal/daemon/webui/app.js`、docs、README

- [ ] **Step 1: app.js**：`loadConversation` 改用 `before_seq=`，游标取响应 `next`；`source==='db'` 时不再隐藏「加载更早」。
- [ ] **Step 2: 文档**：usage.md（schema 4、14 工具、两个新工具、完整对话存储与粒度）、README（14 工具）、client-guide（最近对话读库）。
- [ ] **Step 3: 真实验收**：重启应用 → `task_sync` 本场会话 → `conversation_search` 命中已知词、`conversation_get` 翻页到 seq 1；库增量与耗时记录；源文件改名后 UI 仍能翻更早。
- [ ] **Step 4: 全量测试 + vet + 交叉编译 + commit**
