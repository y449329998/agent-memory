package workbuddy

import (
	"strings"
	"testing"
)

func TestParseTranscriptWorkBuddy(t *testing.T) {
	content := strings.Join([]string{
		`{"type":"message","role":"user","timestamp":"2026-11-09T09:00:00Z","content":"把按钮改大"}`,
		`{"type":"message","role":"assistant","timestamp":"2026-11-09T09:00:01Z","content":[{"type":"text","text":"好的"}]}`,
		`{"type":"function_call","name":"Bash","timestamp":"2026-11-09T09:00:02Z","arguments":"{\"command\":\"go test ./...\"}"}`,
		`{"type":"function_call_result","name":"Bash","status":"failed","timestamp":"2026-11-09T09:00:03Z","output":"FAIL boom"}`,
	}, "\n")
	msgs, err := parseTranscript(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 4 {
		t.Fatalf("want 4 got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Kind != "text" || msgs[0].Text != "把按钮改大" || msgs[0].Role != "user" {
		t.Fatalf("user msg wrong: %+v", msgs[0])
	}
	if msgs[1].Kind != "text" || msgs[1].Text != "好的" {
		t.Fatalf("assistant msg wrong: %+v", msgs[1])
	}
	if msgs[2].Kind != "tool" || msgs[2].Tool != "Bash" || msgs[2].Text != "go test ./..." {
		t.Fatalf("tool summary wrong: %+v", msgs[2])
	}
	if msgs[3].Kind != "tool_error" || !strings.Contains(msgs[3].Text, "FAIL boom") {
		t.Fatalf("tool error wrong: %+v", msgs[3])
	}
}
