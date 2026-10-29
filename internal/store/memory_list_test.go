package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestListMemoriesFiltered_Offset(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/off", "off", "", `F:\off`, base)
	for i, text := range []string{"第一条", "第二条", "第三条"} {
		if _, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: domain.ScopeProject, Type: domain.TypeFact, Text: text,
		}, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListMemoriesFiltered(ctx, MemoryFilter{ProjectID: pid, Scope: domain.ScopeProject, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "第二条" {
		t.Fatalf("offset=1 got %+v", got)
	}
	total, err := s.CountMemoriesFiltered(ctx, MemoryFilter{ProjectID: pid, Scope: domain.ScopeProject})
	if err != nil || total != 3 {
		t.Fatalf("count = %d err = %v; offset must not affect count", total, err)
	}
	empty, err := s.ListMemoriesFiltered(ctx, MemoryFilter{ProjectID: pid, Scope: domain.ScopeProject, Limit: 1, Offset: 10})
	if err != nil || len(empty) != 0 {
		t.Fatalf("offset beyond total = %+v err=%v", empty, err)
	}
}
