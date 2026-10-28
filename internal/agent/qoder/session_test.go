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
	IsSidechain bool   `json:"isSidechain"`
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
	mustMkdirAll(t, repo)
	writeFile(t, filepath.Join(home, "memory", "u.md"), "用户级\n")
	slug := "demo"
	writeFile(t, filepath.Join(home, "projects", slug, "s1.jsonl"),
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
	writeFile(t, filepath.Join(home, "memory", "u.md"), "x\n")
	// 没有 workspace-directories 行：项目无法归属，宁可跳过也不猜
	writeFile(t, filepath.Join(home, "projects", "orphan", "s.jsonl"),
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
