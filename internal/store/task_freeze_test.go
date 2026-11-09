package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestUpdateTaskLayeredFreeze(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/fz", "fz", "", `F:\fz`, now)
	id, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Kind: domain.TaskKindSession, SessionID: 1, Goal: "g", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	status := domain.StatusBlocked
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, Status: &status}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ := s.TaskByID(ctx, id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("status-only update must NOT freeze")
	}
	next := "n"
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, NextSteps: &next}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ = s.TaskByID(ctx, id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("next_steps update must NOT freeze")
	}
	owner := "qoder"
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, OwnerAgent: &owner}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ = s.TaskByID(ctx, id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("owner update must NOT freeze")
	}
	goal := "g2"
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, Goal: &goal}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ = s.TaskByID(ctx, id)
	if tk.ManualEditedAt.IsZero() {
		t.Fatal("goal update MUST freeze")
	}
}

func TestUpdateTaskStepsFreeze(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/fz2", "fz2", "", `F:\fz2`, now)
	id, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Goal: "g", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	steps := []domain.TaskStep{{Subject: "s", Status: domain.StepPending, CreatedAt: now, UpdatedAt: now}}
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: id, Steps: &steps}, now); err != nil {
		t.Fatal(err)
	}
	tk, _, _ := s.TaskByID(ctx, id)
	if tk.ManualEditedAt.IsZero() {
		t.Fatal("steps text edit MUST freeze")
	}
}

func TestTaskFilesAggregates(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/tf", "tf", "", `F:\tf`, now)
	sid, err := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "tf1", ProjectID: pid, Path: `C:\tf1.jsonl`, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Kind: domain.TaskKindSession, SessionID: sid, Goal: "g", CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkTaskSession(ctx, id, sid, "source", now); err != nil {
		t.Fatal(err)
	}
	evs := []domain.SessionEvent{
		{SessionID: sid, Seq: 1, Kind: domain.KindToolEdit, Files: []string{"a.go"}, Ts: now},
		{SessionID: sid, Seq: 2, Kind: domain.KindToolEdit, Files: []string{"a.go", "b.go"}, Ts: now.Add(time.Minute)},
	}
	if err := s.ReplaceEvents(ctx, sid, evs); err != nil {
		t.Fatal(err)
	}
	files, err := s.TaskFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("want 2 files got %+v", files)
	}
	byPath := map[string]TaskFileStat{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if byPath["a.go"].Count != 2 || byPath["b.go"].Count != 1 {
		t.Fatalf("counts wrong: %+v", byPath)
	}
	if !byPath["a.go"].LastTs.Equal(now.Add(time.Minute)) {
		t.Fatalf("last ts wrong: %+v", byPath["a.go"])
	}
	if files[0].Path != "a.go" {
		t.Fatalf("should sort by last ts desc: %+v", files)
	}
}
