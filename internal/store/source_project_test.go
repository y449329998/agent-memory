package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestListProjectSources_OnlyContributors(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pa, _ := s.EnsureProject(ctx, "f:/pa", "pa", "", `F:\pa`, now)
	pb, _ := s.EnsureProject(ctx, "f:/pb", "pb", "", `F:\pb`, now)

	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/a.md", ContentHash: "ha"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/b.md", ContentHash: "hb"}, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/orphan.md", ContentHash: "ho"}, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "qoder", "memory", "/a.md", "parse failed", 4); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, pa, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "A 的记忆", SourceAgent: "qoder", SourcePath: "/a.md"}, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, pb, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "B 的记忆", SourceAgent: "trae", SourcePath: "/b.md"}, now); err != nil {
		t.Fatal(err)
	}

	rows, total, failed, err := s.ListProjectSources(ctx, pa, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != "/a.md" || total != 1 || failed != 1 {
		t.Fatalf("rows=%+v total=%d failed=%d", rows, total, failed)
	}
	if rows[0].LastError != "parse failed" {
		t.Fatalf("last error = %q", rows[0].LastError)
	}

	rows, total, failed, err = s.ListProjectSources(ctx, pb, 50)
	if err != nil || len(rows) != 1 || rows[0].Path != "/b.md" || total != 1 || failed != 0 {
		t.Fatalf("pb rows=%+v total=%d failed=%d err=%v", rows, total, failed, err)
	}
}
