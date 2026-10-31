package ingest

import (
	"context"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

func (r *Runner) syncTaskFromSession(ctx context.Context, agentName string, sess domain.Session, sessID int64, card *domain.TaskCard, steps []agent.TaskStepDraft, at time.Time) error {
	task, ok, err := r.store.TaskBySession(ctx, sessID)
	if err != nil {
		return err
	}
	if !ok {
		candidate, found, err := r.store.FindAttachableTask(ctx, sess.ProjectID, agentName)
		if err != nil {
			return err
		}
		if found {
			task = candidate
			ok = true
		}
	}
	if !ok {
		if card == nil && len(steps) == 0 {
			return nil
		}
		goal := sess.Title
		if card != nil && card.Goal != "" {
			goal = card.Goal
		}
		if goal == "" {
			return nil
		}
		status := domain.StatusDoing
		if card != nil {
			status = card.Status
		} else {
			status = deriveTaskStatus(steps)
		}
		taskID, err := r.store.CreateTask(ctx, domain.Task{
			ProjectID: sess.ProjectID, Goal: goal, Status: status,
			SourceAgent: agentName, SourcePath: sess.Path,
			CreatedAt: at, UpdatedAt: at,
			Steps: toDomainSteps(steps, at),
		})
		if err != nil {
			return err
		}
		if err := r.store.LinkTaskSession(ctx, taskID, sessID, "source", at); err != nil {
			return err
		}
		return r.store.RefreshTaskSnippets(ctx, taskID, at)
	}
	if err := r.store.LinkTaskSession(ctx, task.ID, sessID, "continuation", at); err != nil {
		return err
	}
	if task.ManualEditedAt.IsZero() && (card != nil || len(steps) > 0) {
		updated := task
		if card != nil {
			updated.Goal = card.Goal
			updated.Status = card.Status
			updated.NextSteps = card.NextSteps
			updated.Decisions = card.Decisions
			updated.Files = card.Files
			updated.Commands = card.Commands
			updated.Errors = card.Errors
			updated.OpenQuestions = card.OpenQuestions
		}
		updated.Steps = toDomainSteps(steps, at)
		if err := r.store.UpdateTaskFromSource(ctx, updated, at); err != nil {
			return err
		}
	}
	return r.store.RefreshTaskSnippets(ctx, task.ID, at)
}

func deriveTaskStatus(steps []agent.TaskStepDraft) string {
	if len(steps) == 0 {
		return domain.StatusDoing
	}
	allDone := true
	for _, step := range steps {
		if step.Status == domain.StepBlocked {
			return domain.StatusBlocked
		}
		if step.Status != domain.StepDone && step.Status != domain.StepSkipped {
			allDone = false
		}
	}
	if allDone {
		return domain.StatusDone
	}
	return domain.StatusDoing
}

func toDomainSteps(steps []agent.TaskStepDraft, at time.Time) []domain.TaskStep {
	out := make([]domain.TaskStep, 0, len(steps))
	for _, step := range steps {
		out = append(out, domain.TaskStep{
			Subject: step.Subject, Description: step.Description,
			ActiveForm: step.ActiveForm, Status: step.Status,
			SourceKey: step.SourceKey, CreatedAt: at, UpdatedAt: at,
		})
	}
	return out
}
