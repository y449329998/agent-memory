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

// TestReplaceEvents_EmptyFilesStoredAsJSONArray 钉住空 Files 的落库形态：
// 必须是 JSON 数组 []，不能是 null，否则读回方要额外兼容 null。
func TestReplaceEvents_EmptyFilesStoredAsJSONArray(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, err := s.UpsertSession(ctx, sampleSession("s1"))
	if err != nil {
		t.Fatal(err)
	}
	events := []domain.SessionEvent{
		{Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "hi", Ts: time.Unix(1700000000, 0)},
	}
	if err := s.ReplaceEvents(ctx, id, events); err != nil {
		t.Fatal(err)
	}
	raw, err := s.QueryStrings(ctx, `SELECT files_json FROM session_event WHERE session_id = ? ORDER BY seq`, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 1 || raw[0] != "[]" {
		t.Fatalf("empty files must be stored as [], got %v", raw)
	}
	list, err := s.EventsFor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || len(list[0].Files) != 0 {
		t.Fatalf("empty files should round-trip as empty, got %+v", list)
	}
}
