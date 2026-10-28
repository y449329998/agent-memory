package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestListSources_OrdersByUpdatedAndFiltersFailed(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	base := time.Unix(1700000000, 0)

	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/ok.md", ContentHash: "h1", Size: 10}, base.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/bad.md", ContentHash: "h2", Size: 20}, base.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "trae", "memory", "/t/bad.md", "parse: bad", base.Add(2*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListSources(ctx, "", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 sources, got %+v", all)
	}
	if all[0].Path != "/t/bad.md" {
		t.Fatalf("want newest first, got %+v", all)
	}
	if all[0].LastError != "parse: bad" {
		t.Fatalf("last_error not carried: %+v", all[0])
	}
	if all[0].Size != 20 {
		t.Fatalf("size not carried: %+v", all[0])
	}

	failed, err := s.ListSources(ctx, "", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].Agent != "trae" {
		t.Fatalf("failed filter wrong: %+v", failed)
	}

	byAgent, err := s.ListSources(ctx, "qoder", false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(byAgent) != 1 || byAgent[0].Path != "/q/ok.md" {
		t.Fatalf("agent filter wrong: %+v", byAgent)
	}

	limited, err := s.ListSources(ctx, "", false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Path != "/t/bad.md" {
		t.Fatalf("limit wrong: %+v", limited)
	}
}
