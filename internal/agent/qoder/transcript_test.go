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
	if len(msgs) != 5 {
		t.Fatalf("want 5 msgs got %d: %+v", len(msgs), msgs)
	}
	if len([]rune(msgs[0].Text)) != 3000 {
		t.Fatalf("user text must be full, got %d runes", len([]rune(msgs[0].Text)))
	}
	if msgs[1].Kind != "text" || msgs[1].Text != "好的" {
		t.Fatalf("assistant text wrong: %+v", msgs[1])
	}
	if msgs[2].Kind != "tool" || msgs[2].Tool != "Bash" || msgs[2].Text != "go test ./..." {
		t.Fatalf("bash summary wrong: %+v", msgs[2])
	}
	if msgs[3].Kind != "tool_error" || !strings.Contains(msgs[3].Text, "FAIL boom") {
		t.Fatalf("error result wrong: %+v", msgs[3])
	}
	if msgs[4].Kind != "tool" || msgs[4].Text != "a.go" {
		t.Fatalf("read summary should be file path: %+v", msgs[4])
	}
}

func TestParseTranscriptTruncatesHugeText(t *testing.T) {
	huge := strings.Repeat("x", transcriptMaxText+10)
	content := `{"type":"user","message":{"role":"user","content":"` + huge + `"}}`
	msgs, err := parseTranscript([]byte(content))
	if err != nil || len(msgs) != 1 || !msgs[0].Truncated {
		t.Fatalf("huge text should be truncated: %v %+v", err, msgs)
	}
}
