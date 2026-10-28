package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestListMemoriesFiltered(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	add := func(scope domain.Scope, pid int64, typ domain.MemoryType, text string) int64 {
		t.Helper()
		id, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: scope, Type: typ, Text: text, SourceAgent: "test", SourcePath: "/t.md",
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	userPref := add(domain.ScopeUser, 0, domain.TypePreference, "用户偏好甲")
	projC := add(domain.ScopeProject, 3, domain.TypeConstraint, "项目约束乙")
	projP := add(domain.ScopeProject, 3, domain.TypePreference, "项目偏好丙")
	if err := s.SetStaleByID(ctx, projP, true); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListMemoriesFiltered(ctx, MemoryFilter{AllProjects: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all=%d", len(all))
	}

	onlyStale := true
	stale, err := s.ListMemoriesFiltered(ctx, MemoryFilter{AllProjects: true, Stale: &onlyStale, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].ID != projP {
		t.Fatalf("stale filter: %+v", stale)
	}

	byType, err := s.ListMemoriesFiltered(ctx, MemoryFilter{AllProjects: true, Type: domain.TypeConstraint, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(byType) != 1 || byType[0].ID != projC {
		t.Fatalf("type filter: %+v", byType)
	}

	projectScoped, err := s.ListMemoriesFiltered(ctx, MemoryFilter{ProjectID: 3, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(projectScoped) != 3 {
		t.Fatalf("project scope: %+v", projectScoped)
	}

	userOnly, err := s.ListMemoriesFiltered(ctx, MemoryFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(userOnly) != 1 || userOnly[0].ID != userPref {
		t.Fatalf("user only: %+v", userOnly)
	}
}
