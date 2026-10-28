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

// TestSessionIDByExternal_FindsAndReportsMiss 钉住 agent 隔离：同名外部 id
// 属于不同 agent 时不能互相命中，缺失时返回 ok=false 而不是错误。
func TestSessionIDByExternal_FindsAndReportsMiss(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, err := s.UpsertSession(ctx, sampleSession("s1"))
	if err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.SessionIDByExternal(ctx, "qoder", "s1")
	if err != nil || !ok || got != id {
		t.Fatalf("got id=%d ok=%v err=%v, want id=%d ok=true", got, ok, err, id)
	}
	if _, ok, err := s.SessionIDByExternal(ctx, "qoder", "missing"); err != nil || ok {
		t.Fatalf("missing session: ok=%v err=%v, want ok=false no error", ok, err)
	}
	if _, ok, err := s.SessionIDByExternal(ctx, "trae", "s1"); err != nil || ok {
		t.Fatalf("other agent must not match: ok=%v err=%v", ok, err)
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

// TestUpsertSession_ZeroTimesUseZeroSentinel 钉住零值时间的落库形态：
// 零值 time.Time 必须写成 0（而不是 Unix 的 -62135596800），读回时还原为零值。
func TestUpsertSession_ZeroTimesUseZeroSentinel(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	sess := sampleSession("s1")
	sess.StartedAt = time.Time{}
	sess.UpdatedAt = time.Time{}
	id, err := s.UpsertSession(ctx, sess)
	if err != nil {
		t.Fatal(err)
	}

	var started, updated int64
	if err := s.db.QueryRow(`SELECT started_at, updated_at FROM session WHERE id = ?`, id).Scan(&started, &updated); err != nil {
		t.Fatal(err)
	}
	if started != 0 {
		t.Fatalf("started_at = %d, want 0 sentinel", started)
	}
	if updated != 0 {
		t.Fatalf("updated_at = %d, want 0 sentinel", updated)
	}

	got, ok, err := s.SessionByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !got.StartedAt.IsZero() {
		t.Fatalf("StartedAt = %v, want zero time", got.StartedAt)
	}
	if !got.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt = %v, want zero time", got.UpdatedAt)
	}
}

// TestUpsertSession_BackfillsStartedAtFromZeroSentinel 验证 ON CONFLICT 的 CASE：
// 先以零值写入，再带真实时间 upsert 同一 external_id，started_at 必须被补上。
func TestUpsertSession_BackfillsStartedAtFromZeroSentinel(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	empty := sampleSession("s1")
	empty.StartedAt = time.Time{}
	empty.UpdatedAt = time.Time{}
	if _, err := s.UpsertSession(ctx, empty); err != nil {
		t.Fatal(err)
	}

	real := sampleSession("s1")
	id, err := s.UpsertSession(ctx, real)
	if err != nil {
		t.Fatal(err)
	}
	var started int64
	if err := s.db.QueryRow(`SELECT started_at FROM session WHERE id = ?`, id).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != real.StartedAt.Unix() {
		t.Fatalf("started_at = %d, want %d (CASE should backfill)", started, real.StartedAt.Unix())
	}
	got, ok, err := s.SessionByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !got.StartedAt.Equal(real.StartedAt) {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, real.StartedAt)
	}
}

// TestReplaceEvents_ZeroTsUsesZeroSentinel 钉住零 Ts 事件的落库与读回：
// ts 列写 0，读回零值 time.Time，使下游 IsZero 判断可靠。
func TestReplaceEvents_ZeroTsUsesZeroSentinel(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, err := s.UpsertSession(ctx, sampleSession("s1"))
	if err != nil {
		t.Fatal(err)
	}
	events := []domain.SessionEvent{
		{Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "hi"},
	}
	if err := s.ReplaceEvents(ctx, id, events); err != nil {
		t.Fatal(err)
	}
	var ts int64
	if err := s.db.QueryRow(`SELECT ts FROM session_event WHERE session_id = ? AND seq = 1`, id).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	if ts != 0 {
		t.Fatalf("ts = %d, want 0 sentinel", ts)
	}
	list, err := s.EventsFor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].Ts.IsZero() {
		t.Fatalf("zero ts should round-trip as zero time, got %+v", list)
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
