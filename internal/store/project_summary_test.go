package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestListProjectSummaries(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	a, err := s.EnsureProject(ctx, "a/repo", "a", "", `F:\a`, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.EnsureProject(ctx, "b/repo", "b", "", `F:\b`, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, a, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "甲", SourceAgent: "t", SourcePath: "/t.md"}, now); err != nil {
		t.Fatal(err)
	}
	staleID, _, err := s.UpsertMemory(ctx, a, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "乙", SourceAgent: "t", SourcePath: "/t.md"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStaleByID(ctx, staleID, true); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListProjectSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "a/repo" {
		t.Fatalf("order/size: %+v", got)
	}
	if got[0].MemoryCount != 2 || got[0].StaleCount != 1 {
		t.Fatalf("counts: %+v", got[0])
	}
	if got[0].LastMemoryAt.IsZero() {
		t.Fatalf("last memory at not set: %+v", got[0])
	}
	if got[1].ID != b || got[1].MemoryCount != 0 {
		t.Fatalf("empty project: %+v", got[1])
	}
}

func TestListProjectSummaries_TaskCount(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/tasks", "tasks", "", `F:\tasks`, now)
	sid, err := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "s1", ProjectID: pid, Title: "会话", UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTaskCard(ctx, domain.TaskCard{SessionID: sid, ProjectID: pid, Goal: "目标", Status: domain.StatusDoing, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	list, err := s.ListProjectSummaries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TaskCount != 1 {
		t.Fatalf("summaries = %+v", list)
	}
}
