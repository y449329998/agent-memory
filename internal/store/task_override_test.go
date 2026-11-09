package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func overrideFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/ov", "ov", "", `F:\ov`, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "覆盖测试", CreatedAt: now, UpdatedAt: now,
		Steps: []domain.TaskStep{
			{Subject: "稳定步骤", SourceKey: "qt:1", Status: domain.StepPending, CreatedAt: now, UpdatedAt: now},
			{Subject: "反推步骤", SourceKey: "cmd:0", Status: domain.StepPending, CreatedAt: now, UpdatedAt: now},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s, id
}

func TestOverrideOverlayAndOrphan(t *testing.T) {
	s, id := overrideFixture(t)
	ctx := context.Background()
	now := time.Unix(1700000100, 0)
	if err := s.SetStepOverride(ctx, id, "qt:1", domain.StepDone, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStepOverride(ctx, id, "qt:999", domain.StepDone, now); err != nil {
		t.Fatal(err)
	}
	d, ok, err := s.TaskDetail(ctx, id)
	if err != nil || !ok {
		t.Fatal(err)
	}
	var stable, derived string
	for _, st := range d.Steps {
		if st.SourceKey == "qt:1" {
			stable = st.Status
		}
		if st.SourceKey == "cmd:0" {
			derived = st.Status
		}
	}
	if stable != domain.StepDone {
		t.Fatalf("stable step should be overridden to done, got %s", stable)
	}
	if derived != domain.StepPending {
		t.Fatalf("derived step must NOT be overridden, got %s", derived)
	}
}

func TestOverrideRejectsInvalidStatus(t *testing.T) {
	s, id := overrideFixture(t)
	if err := s.SetStepOverride(context.Background(), id, "qt:1", "bogus", time.Now()); err == nil {
		t.Fatal("want error for invalid status")
	}
}

func TestOverrideRejectsDerivedKey(t *testing.T) {
	s, id := overrideFixture(t)
	if err := s.SetStepOverride(context.Background(), id, "cmd:0", domain.StepDone, time.Now()); err == nil {
		t.Fatal("derived key must not accept override")
	}
}

func TestPruneStaleOverrides(t *testing.T) {
	s, id := overrideFixture(t)
	ctx := context.Background()
	now := time.Now()
	_ = s.SetStepOverride(ctx, id, "qt:1", domain.StepDone, now)
	_ = s.SetStepOverride(ctx, id, "qt:999", domain.StepDone, now)
	if err := s.PruneStaleOverrides(ctx, id, []string{"qt:1", "cmd:0"}); err != nil {
		t.Fatal(err)
	}
	ov, err := s.stepOverrides(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := ov["qt:999"]; exists {
		t.Fatal("orphan override should be pruned")
	}
	if ov["qt:1"] != domain.StepDone {
		t.Fatal("alive override should survive prune")
	}
}
