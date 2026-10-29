package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

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
