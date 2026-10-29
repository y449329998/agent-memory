package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestUpdateMemory_TextTypeAndFTS(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/upd", "upd", "", `F:\upd`, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "旧文本 部署脚本",
		SourceAgent: "qoder", SourcePath: "/a.md",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	merged, err := s.UpdateMemory(ctx, id, "新文本 发布流程", domain.TypeConstraint, now.Add(time.Minute))
	if err != nil || merged != 0 {
		t.Fatalf("merged=%d err=%v", merged, err)
	}
	m, err := s.MemoryByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "新文本 发布流程" || m.Type != domain.TypeConstraint {
		t.Fatalf("text=%q type=%q", m.Text, m.Type)
	}
	hits, err := NewFTS(s).Search(ctx, domain.SearchQuery{Text: "发布流程", ProjectID: pid, Scope: domain.ScopeProject, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Memory.ID != id {
		t.Fatalf("FTS after update = %+v", hits)
	}
}

func TestUpdateMemory_MergesIntoSameContent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/merge", "merge", "", `F:\merge`, now)
	a, _, _ := s.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "内容甲", SourceAgent: "qoder", SourcePath: "/a.md"}, now)
	b, _, _ := s.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "内容乙", SourceAgent: "trae", SourcePath: "/b.md"}, now)

	merged, err := s.UpdateMemory(ctx, a, "内容乙", domain.TypeFact, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if merged != b {
		t.Fatalf("merged = %d, want %d", merged, b)
	}
	old, _ := s.MemoryByID(ctx, a)
	if !old.Stale {
		t.Fatal("edited duplicate should be marked stale")
	}
	target, _ := s.MemoryByID(ctx, b)
	if len(target.Sources) != 2 {
		t.Fatalf("target sources = %d, want 2", len(target.Sources))
	}
}

func TestUpdateMemory_RejectsInvalidInput(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/bad", "bad", "", `F:\bad`, now)
	id, _, _ := s.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "合法内容"}, now)

	if _, err := s.UpdateMemory(ctx, id, "   ", domain.TypeFact, now); !errors.Is(err, domain.ErrEmptyMemory) {
		t.Fatalf("empty text err = %v", err)
	}
	if _, err := s.UpdateMemory(ctx, id, "内容", domain.MemoryType("bogus"), now); !errors.Is(err, domain.ErrInvalidMemoryType) {
		t.Fatalf("invalid type err = %v", err)
	}
	if _, err := s.UpdateMemory(ctx, 99999, "内容", domain.TypeFact, now); !errors.Is(err, domain.ErrMemoryNotFound) {
		t.Fatalf("missing id err = %v", err)
	}
}
