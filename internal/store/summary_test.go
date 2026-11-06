package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestSummaryQueueStates(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	pid, err := s.EnsureProject(ctx, "f:/summary", "summary", "", `F:\summary`, now)
	if err != nil {
		t.Fatal(err)
	}
	memID, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "摘要队列测试记忆",
		SourceAgent: "test", SourcePath: "test", Quote: "摘要队列测试记忆",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSummaryPending(ctx, "memory", memID, "hash-1", now); err != nil {
		t.Fatal(err)
	}
	job, ok, err := s.NextSummary(ctx, now)
	if err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if job.State != "processing" || job.Kind != "memory" || job.ID != memID {
		t.Fatalf("job = %+v", job)
	}
	if err := s.SaveSummary(ctx, "memory", memID, "hash-1", "AI 摘要", "qwen", now); err != nil {
		t.Fatal(err)
	}
	row, ok, err := s.SummaryFor(ctx, "memory", memID)
	if err != nil || !ok || row.Summary != "AI 摘要" || row.State != "ready" {
		t.Fatalf("row=%+v ok=%v err=%v", row, ok, err)
	}
	mem, err := s.MemoryByID(ctx, memID)
	if err != nil || mem.Summary != "AI 摘要" || mem.SummaryState != "ready" {
		t.Fatalf("memory summary not joined: %+v err=%v", mem, err)
	}
}

func TestSummaryFailedStaysUntilContentChanges(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	if err := s.MarkSummaryPending(ctx, "memory", 7, "hash-1", now); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.NextSummary(ctx, now); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if err := s.FailSummary(ctx, "memory", 7, "boom", now); err != nil {
		t.Fatal(err)
	}
	// 同一内容再次触发不应把 failed 重置成 pending。
	if err := s.MarkSummaryPending(ctx, "memory", 7, "hash-1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, _, err := s.SummaryFor(ctx, "memory", 7)
	if err != nil || row.State != "failed" || row.Attempts != 1 {
		t.Fatalf("row=%+v err=%v", row, err)
	}
	// 内容变化后重新排队，attempts 清零。
	if err := s.MarkSummaryPending(ctx, "memory", 7, "hash-2", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, _, _ = s.SummaryFor(ctx, "memory", 7)
	if row.State != "pending" || row.Attempts != 0 || row.Summary != "" {
		t.Fatalf("row=%+v", row)
	}
}

func TestRetryFailedSummaries(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	if err := s.MarkSummaryPending(ctx, "task", 3, "h", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NextSummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FailSummary(ctx, "task", 3, "boom", now); err != nil {
		t.Fatal(err)
	}
	n, err := s.RetryFailedSummaries(ctx, now.Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("retried=%d err=%v", n, err)
	}
	row, _, _ := s.SummaryFor(ctx, "task", 3)
	if row.State != "pending" || row.Attempts != 0 {
		t.Fatalf("row=%+v", row)
	}
}

func TestTaskByIDJoinsSummary(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := s.EnsureProject(ctx, "f:/sumtask", "sumtask", "", `F:\sumtask`, now)
	id, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "任务摘要", Status: domain.StatusDoing,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.TaskSummarySource(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSummaryPending(ctx, "task", id, source, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.NextSummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSummary(ctx, "task", id, source, "任务摘要正文", "qwen", now); err != nil {
		t.Fatal(err)
	}
	task, ok, err := s.TaskByID(ctx, id)
	if err != nil || !ok || task.Summary != "任务摘要正文" || task.SummaryState != "ready" {
		t.Fatalf("task=%+v ok=%v err=%v", task, ok, err)
	}
}
