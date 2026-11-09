package ingest

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/agent/qoder"
)

func TestRunWritesFullTranscript(t *testing.T) {
	home, repo, _ := fakeQoder(t)
	write(t, filepath.Join(home, "projects", "RA", "a.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repo)+
			`{"type":"user","sessionId":"s1","timestamp":"2026-11-09T09:00:00Z","message":{"role":"user","content":"给扣减加乐观锁"}}`+"\n"+
			`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-09T09:01:00Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"F:\\a.go"}}]}}`+"\n")
	s := openTempStore(t)
	r := New(s, qoder.New(home))
	r.SettleIdle = 5 * time.Minute
	if _, err := r.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sid, ok, err := s.SessionIDByExternal(ctx, "qoder", "s1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	has, err := s.HasMessages(ctx, sid)
	if err != nil || !has {
		t.Fatalf("transcript not written: has=%v err=%v", has, err)
	}
	msgs, _, err := s.MessagesForSession(ctx, sid, 0, 10)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("want 2 messages got %d err=%v", len(msgs), err)
	}
	if msgs[0].Text != "给扣减加乐观锁" || msgs[1].Kind != "tool" || msgs[1].Text != "F:\\a.go" {
		t.Fatalf("messages wrong: %+v", msgs)
	}
}
