package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func messageFixture(t *testing.T) (*Store, int64, int64) {
	t.Helper()
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/msg", "msg", "", `F:\msg`, now)
	sid, err := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "m1", ProjectID: pid, Path: `C:\m1.jsonl`, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	return s, sid, pid
}

func sampleMessages(sid int64) []domain.SessionMessage {
	now := time.Unix(1700000000, 0)
	return []domain.SessionMessage{
		{SessionID: sid, Seq: 1, Role: "user", Kind: "text", Text: "给扣减加乐观锁", Ts: now},
		{SessionID: sid, Seq: 2, Role: "assistant", Kind: "text", Text: "好了", Ts: now.Add(time.Minute)},
		{SessionID: sid, Seq: 3, Role: "assistant", Kind: "tool", Tool: "Bash", Text: "go test ./...", Ts: now.Add(2 * time.Minute)},
	}
}

func TestReplaceMessagesPageAndTruncate(t *testing.T) {
	s, sid, _ := messageFixture(t)
	ctx := context.Background()
	msgs := sampleMessages(sid)
	if err := s.ReplaceMessages(ctx, sid, msgs); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.HasMessages(ctx, sid); err != nil || !ok {
		t.Fatalf("has messages: %v %v", ok, err)
	}
	page, next, err := s.MessagesForSession(ctx, sid, 0, 2)
	if err != nil || len(page) != 2 || page[1].Seq != 3 || next != 1 {
		t.Fatalf("page1 wrong: %v %+v next=%d", err, page, next)
	}
	page, next, err = s.MessagesForSession(ctx, sid, next+1, 2)
	if err != nil || len(page) != 1 || page[0].Seq != 1 || next != 0 {
		t.Fatalf("page2 wrong: %v %+v next=%d", err, page, next)
	}
	// 会话被截断：只剩第一条，多余 seq 应被删掉
	if err := s.ReplaceMessages(ctx, sid, msgs[:1]); err != nil {
		t.Fatal(err)
	}
	all, _, err := s.MessagesForSession(ctx, sid, 0, 10)
	if err != nil || len(all) != 1 {
		t.Fatalf("truncate failed: %v %+v", err, all)
	}
}

func TestSearchMessagesFTS(t *testing.T) {
	s, sid, pid := messageFixture(t)
	ctx := context.Background()
	if err := s.ReplaceMessages(ctx, sid, sampleMessages(sid)); err != nil {
		t.Fatal(err)
	}
	hits, err := s.SearchMessages(ctx, "乐观锁", 0, 0, 10)
	if err != nil || len(hits) != 1 || hits[0].Seq != 1 || hits[0].Agent != "qoder" {
		t.Fatalf("fts miss: %v %+v", err, hits)
	}
	hits, err = s.SearchMessages(ctx, "乐观锁", pid, 0, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("project filter should hit: %v %+v", err, hits)
	}
	if hits, _ := s.SearchMessages(ctx, "乐观锁", pid+999, 0, 10); len(hits) != 0 {
		t.Fatalf("other project should miss: %+v", hits)
	}
}

func TestMainSessionForTask(t *testing.T) {
	s, sid, pid := messageFixture(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	id, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Kind: domain.TaskKindSession, SessionID: sid, Goal: "g", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.MainSessionForTask(ctx, id)
	if err != nil || got != sid {
		t.Fatalf("main session = %d err=%v want %d", got, err, sid)
	}
	formal, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Goal: "f", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkTaskSession(ctx, formal, sid, "source", now); err != nil {
		t.Fatal(err)
	}
	if got, err := s.MainSessionForTask(ctx, formal); err != nil || got != sid {
		t.Fatalf("formal fallback = %d err=%v", got, err)
	}
}
