package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/agent/qoder"
)

func TestSyncSessionBypassesSettle(t *testing.T) {
	home, repo, _ := fakeQoder(t)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home, "projects", "RA", "live.jsonl")
	write(t, p,
		fmt.Sprintf(`{"type":"workspace-directories","sessionId":"live","directories":[%q]}`, repo)+"\n"+
			`{"type":"user","sessionId":"live","timestamp":"2026-11-09T09:00:00Z","message":{"role":"user","content":"正在写的会话"}}`+"\n"+
			`{"type":"assistant","sessionId":"live","timestamp":"2026-11-09T09:01:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"TaskCreate","input":{"subject":"第一步","activeForm":"正在做第一步"}}]}}`+"\n"+
			`{"type":"user","sessionId":"live","timestamp":"2026-11-09T09:01:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"Task #1 created successfully: 第一步"}]}}`+"\n")
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
}

func TestSyncSessionMissingFile(t *testing.T) {
	s := openTempStore(t)
	r := New(s, qoder.New(t.TempDir()))
	if _, _, err := r.SyncSession(context.Background(), "qoder", filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestSyncSessionUnknownAgent(t *testing.T) {
	s := openTempStore(t)
	r := New(s, qoder.New(t.TempDir()))
	if _, _, err := r.SyncSession(context.Background(), "nope", "x.jsonl"); err == nil {
		t.Fatal("want error for unknown agent")
	}
}
