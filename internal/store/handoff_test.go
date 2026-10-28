package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/normalize"
)

func TestSaveHandoff_KeepsOnePerProjectAndDetectsNoChange(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/x", "x", "", `F:\x`, now)
	if err != nil {
		t.Fatal(err)
	}
	body := "# 交接简报\n\n内容"
	hash := normalize.Hash(body)

	changed, err := s.SaveHandoff(ctx, HandoffRow{ProjectID: pid, Body: body, ContentHash: hash, BudgetTokens: 1500, GeneratedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first save should count as a change")
	}

	changed, err = s.SaveHandoff(ctx, HandoffRow{ProjectID: pid, Body: body, ContentHash: hash, BudgetTokens: 1500, GeneratedAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("identical content must not count as a change")
	}

	row, ok, err := s.LastHandoff(ctx, pid)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if row.Body != body || row.BudgetTokens != 1500 {
		t.Fatalf("row = %+v", row)
	}
	n, err := s.QueryInt(ctx, `SELECT count(*) FROM handoff`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 row per project, got %d", n)
	}
}
