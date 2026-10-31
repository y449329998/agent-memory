package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestCreateAndGetTaskWithSteps(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/demo", "demo", "", `F:\demo`, now)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "实现导出", Status: domain.StatusDoing,
		OwnerAgent: "qoder", NextSteps: "先写接口",
		SourceAgent: "qoder", SourcePath: `C:\sessions\a.jsonl`,
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone, SourceKey: "1"},
			{Subject: "补测试", Status: domain.StepDoing, SourceKey: "2"},
		},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.TaskByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("TaskByID ok=%v err=%v", ok, err)
	}
	if got.Goal != "实现导出" || got.OwnerAgent != "qoder" || len(got.Steps) != 2 {
		t.Fatalf("task = %+v", got)
	}
	if got.Steps[1].Status != domain.StepDoing || got.Steps[1].Seq != 2 {
		t.Fatalf("steps = %+v", got.Steps)
	}
	if got.ClaimedAt.IsZero() {
		t.Fatalf("claimed_at should be set for owner: %+v", got)
	}
}

func TestUpdateTaskReplacesStepsAndSetsManualEdit(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	id, err := s.CreateTask(ctx, domain.Task{
		Goal: "旧目标", Status: domain.StatusDoing,
		Steps:     []domain.TaskStep{{Subject: "旧步骤", Status: domain.StepPending}},
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	goal := "新目标"
	status := domain.StatusDone
	steps := []domain.TaskStep{
		{Subject: "新步骤一", Status: domain.StepDone},
		{Subject: "新步骤二", Status: domain.StepDone},
	}
	updated, err := s.UpdateTask(ctx, TaskUpdate{
		ID: id, Goal: &goal, Status: &status, Steps: &steps,
	}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Goal != goal || updated.Status != status {
		t.Fatalf("updated = %+v", updated)
	}
	if len(updated.Steps) != 2 || updated.Steps[0].Subject != "新步骤一" {
		t.Fatalf("steps = %+v", updated.Steps)
	}
	if updated.ManualEditedAt.IsZero() || updated.CompletedAt.IsZero() {
		t.Fatalf("manual/completed not set: %+v", updated)
	}
}

func TestTaskSessionAndSnippets(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/demo", "demo", "", `F:\demo`, now)
	sessID, err := s.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: "sess-1", ProjectID: pid, Title: "实现导出",
		Path: `C:\sessions\1.jsonl`, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceEvents(ctx, sessID, []domain.SessionEvent{
		{SessionID: sessID, Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "实现导出", Ts: now},
		{SessionID: sessID, Seq: 2, Role: "assistant", Kind: domain.KindToolCommand, Text: "go test ./...", Ts: now.Add(time.Minute)},
		{SessionID: sessID, Seq: 3, Role: "assistant", Kind: domain.KindAssistant, Text: "测试通过", Ts: now.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	taskID, err := s.CreateTask(ctx, domain.Task{ProjectID: pid, Goal: "实现导出", Status: domain.StatusDoing, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkTaskSession(ctx, taskID, sessID, "source", now); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.TaskBySession(ctx, sessID); err != nil || !ok || got.ID != taskID {
		t.Fatalf("TaskBySession ok=%v err=%v got=%+v", ok, err, got)
	}
	if err := s.RefreshTaskSnippets(ctx, taskID, now); err != nil {
		t.Fatal(err)
	}
	detail, ok, err := s.TaskDetail(ctx, taskID)
	if err != nil || !ok {
		t.Fatalf("TaskDetail ok=%v err=%v", ok, err)
	}
	if len(detail.Snippets) != 3 || detail.Snippets[2].Text != "测试通过" {
		t.Fatalf("snippets = %+v", detail.Snippets)
	}
	if len(detail.Sessions) != 1 || detail.Sessions[0].SessionID != sessID {
		t.Fatalf("sessions = %+v", detail.Sessions)
	}
}

func TestArchiveAndMergeTasks(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	old := now.Add(-31 * 24 * time.Hour)
	target, _ := s.CreateTask(ctx, domain.Task{Goal: "目标任务", Status: domain.StatusDoing, CreatedAt: now, UpdatedAt: now})
	if _, err := s.CreateTask(ctx, domain.Task{
		Goal: "重复任务", Status: domain.StatusDone, CompletedAt: old, CreatedAt: old, UpdatedAt: old,
	}); err != nil {
		t.Fatal(err)
	}
	source := int64(2)
	if n, err := s.ArchiveCompletedTasks(ctx, now.Add(-30*24*time.Hour), now); err != nil || n != 1 {
		t.Fatalf("archive n=%d err=%v", n, err)
	}
	got, _, _ := s.TaskByID(ctx, source)
	if got.ArchivedAt.IsZero() {
		t.Fatal("source should be archived")
	}
	// 给来源任务一个步骤，验证合并会追加步骤。
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: source, Steps: &[]domain.TaskStep{{Subject: "步骤A", Status: domain.StepDone}}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.MergeTasks(ctx, source, target, now); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.TaskByID(ctx, target)
	if len(got.Steps) != 1 || got.Steps[0].Subject != "步骤A" {
		t.Fatalf("merged steps = %+v", got.Steps)
	}
	src, _, _ := s.TaskByID(ctx, source)
	if src.MergedIntoTaskID != target {
		t.Fatalf("merged_into = %d", src.MergedIntoTaskID)
	}
}

func TestBackfillTasksFromCards(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/demo", "demo", "", `F:\demo`, now)
	sessID, _ := s.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "s1", ProjectID: pid, Title: "旧会话", UpdatedAt: now})
	cardID, err := s.UpsertTaskCard(ctx, domain.TaskCard{
		SessionID: sessID, ProjectID: pid, Goal: "旧目标", Status: domain.StatusDoing,
		NextSteps: "旧下一步", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM meta WHERE key = 'task_backfill_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillTasks(ctx); err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.TaskByID(ctx, cardID)
	if err != nil || !ok {
		t.Fatalf("backfill task ok=%v err=%v", ok, err)
	}
	if task.Goal != "旧目标" || task.SourceAgent != "qoder" {
		t.Fatalf("task = %+v", task)
	}
	linked, ok, err := s.TaskBySession(ctx, sessID)
	if err != nil || !ok || linked.ID != cardID {
		t.Fatalf("session link ok=%v err=%v got=%+v", ok, err, linked)
	}
}
