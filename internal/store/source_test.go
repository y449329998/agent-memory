package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestPutSource_RecordsHashAndDetectsChange(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)
	st := func(hash string) domain.SourceState {
		return domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: hash, MTime: run1, Size: 10}
	}

	if err := s.PutSource(ctx, st("h1"), run1.Unix()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.SourceHash(ctx, "qoder", "memory", "/q/a.md")
	if err != nil || !ok || got != "h1" {
		t.Fatalf("got %q ok=%v err=%v", got, ok, err)
	}

	// 同一 run 重复写：不应记为变更
	if err := s.PutSource(ctx, st("h1"), run1.Unix()); err != nil {
		t.Fatal(err)
	}
	var changed int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source WHERE changed_at <> 0`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("unchanged rescan should not record change, got %d", changed)
	}

	// 内容变化：prev_hash 记下旧哈希，changed_at 记下时间
	if err := s.PutSource(ctx, st("h2"), run2.Unix()); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetSource(ctx, "qoder", "memory", "/q/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if row.ContentHash != "h2" || row.PrevHash != "h1" {
		t.Fatalf("hash history wrong: %+v", row)
	}
	if !row.ChangedAt.Equal(run2) {
		t.Fatalf("changed_at = %v want %v", row.ChangedAt, run2)
	}
	if row.LastError != "" {
		t.Fatalf("successful put should clear last_error, got %q", row.LastError)
	}
}

func TestMarkSourceError_KeepsPreviousHash(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run := time.Unix(1700000000, 0)
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/x.md", ContentHash: "old"}, run.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "trae", "memory", "/t/x.md", "parse: bad frontmatter", run.Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetSource(ctx, "trae", "memory", "/t/x.md")
	if err != nil {
		t.Fatal(err)
	}
	if row.LastError == "" || row.ContentHash != "old" {
		t.Fatalf("want error recorded with hash kept: %+v", row)
	}
	// 错误路径不该推进 last_seen_run，否则 stale 判定会误判为「本轮见过」
	if row.LastSeenRun == run.Add(time.Minute).Unix() {
		t.Fatal("MarkSourceError must not mark the source as seen this run")
	}
}

func TestStats_CountsAndGroupings(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	for _, d := range []domain.MemoryDraft{
		{Scope: domain.ScopeUser, Type: domain.TypePreference, Text: "沟通用中文", SourceAgent: "qoder", SourcePath: "/q/a.md"},
		{Scope: domain.ScopeUser, Type: domain.TypeConstraint, Text: "余额扣减需乐观锁", SourceAgent: "trae", SourcePath: "/t/a.md"},
	} {
		if _, _, err := s.UpsertMemory(ctx, 0, d, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/a.md", ContentHash: "h2"}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "trae", "memory", "/t/bad.md", "boom", now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE memory_item SET stale = 1 WHERE type = 'constraint'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO project(key, created_at, updated_at) VALUES('p1', 1, 1)`); err != nil {
		t.Fatal(err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Memories != 2 || stats.Stale != 1 || stats.Projects != 1 {
		t.Fatalf("counts wrong: %+v", stats)
	}
	if stats.Sources != 3 {
		t.Fatalf("sources = %d, want 3 (含一个错误源)", stats.Sources)
	}
	if stats.FailedSource != 1 {
		t.Fatalf("failed = %d, want 1", stats.FailedSource)
	}
	if stats.MemoriesByAgent["qoder"] != 1 || stats.MemoriesByAgent["trae"] != 1 {
		t.Fatalf("by agent wrong: %v", stats.MemoriesByAgent)
	}
	if stats.MemoriesByType["preference"] != 1 || stats.MemoriesByType["constraint"] != 1 {
		t.Fatalf("by type wrong: %v", stats.MemoriesByType)
	}
}
