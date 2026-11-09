package ingest

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

// syncTaskFromSession 让一次对话对应一条对话任务：会话扫到就建，内容变了就更新。
//
// 更新只覆盖没被手工编辑过的任务；手工编辑说明用户在界面或 MCP 里改过目标与步骤，
// 源文件不能把它冲掉。跨对话的合并任务（kind=formal）由人或 agent 显式创建，
// 扫描不会再把会话塞进「同项目同 agent 的进行中任务」。
func (r *Runner) syncTaskFromSession(ctx context.Context, agentName string, sess domain.Session, sessID int64, card *domain.TaskCard, steps []agent.TaskStepDraft, at time.Time) error {
	task, ok, err := r.store.TaskByKindSession(ctx, sessID)
	if err != nil {
		return err
	}
	derived := pickSteps(toDomainSteps(steps, at), stepsFromCard(card, at))
	if !ok {
		goal := sess.Title
		if card != nil && card.Goal != "" {
			goal = card.Goal
		}
		if goal == "" && len(derived) == 0 {
			return nil
		}
		status := domain.StatusDoing
		switch {
		case card != nil:
			status = card.Status
		case len(steps) > 0:
			status = deriveTaskStatus(steps)
		}
		taskID, err := r.store.CreateTask(ctx, domain.Task{
			ProjectID: sess.ProjectID, Kind: domain.TaskKindSession, SessionID: sessID,
			Goal: goal, Status: status, SourceAgent: agentName, SourcePath: sess.Path,
			CreatedAt: at, UpdatedAt: at, Steps: derived,
		})
		if err != nil {
			return err
		}
		if err := r.store.LinkTaskSession(ctx, taskID, sessID, "source", at); err != nil {
			return err
		}
		if err := r.store.RefreshTaskSnippets(ctx, taskID, at); err != nil {
			return err
		}
		r.queueTaskSummary(ctx, taskID, at)
		return nil
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
		updated.Steps = derived
		if err := r.store.UpdateTaskFromSource(ctx, updated, at); err != nil {
			return err
		}
	}
	if err := r.store.RefreshTaskSnippets(ctx, task.ID, at); err != nil {
		return err
	}
	keys := make([]string, 0, len(derived))
	for _, st := range derived {
		keys = append(keys, st.SourceKey)
	}
	if err := r.store.PruneStaleOverrides(ctx, task.ID, keys); err != nil {
		return err
	}
	r.queueTaskSummary(ctx, task.ID, at)
	return nil
}

// pickSteps 优先用 agent 自己维护的 Todo（那是真实计划），没有才退回从任务卡
// 反推的步骤。两者不混排：混排会让同一件事在列表里出现两遍。
func pickSteps(adapter, derived []domain.TaskStep) []domain.TaskStep {
	if len(adapter) > 0 {
		return adapter
	}
	return derived
}

// stepsFromCard 把卡片里已经发生的命令与改动记成已完成步骤，下一步与未决问题
// 记成待办。上限 12 条，避免一次长对话刷出一屏流水账。
func stepsFromCard(card *domain.TaskCard, at time.Time) []domain.TaskStep {
	if card == nil {
		return nil
	}
	const maxSteps = 12
	out := make([]domain.TaskStep, 0, maxSteps)
	add := func(subject, status, key string) {
		if len(out) >= maxSteps {
			return
		}
		subject = strings.TrimLeft(strings.Join(strings.Fields(subject), " "), "-* \t")
		subject = strings.TrimLeft(subject, "0123456789.) 、")
		if subject == "" {
			return
		}
		if runes := []rune(subject); len(runes) > 90 {
			subject = string(runes[:90]) + "…"
		}
		out = append(out, domain.TaskStep{
			Subject: subject, Status: status, SourceKey: key + ":" + strconv.Itoa(len(out)),
			CreatedAt: at, UpdatedAt: at,
		})
	}
	for _, c := range card.Commands {
		add("执行 "+c, domain.StepDone, "cmd")
	}
	for _, f := range card.Files {
		add("改动 "+f, domain.StepDone, "file")
	}
	for _, e := range card.Errors {
		add("待处理报错 "+e, domain.StepBlocked, "err")
	}
	for _, line := range strings.Split(card.NextSteps, "\n") {
		add(line, domain.StepPending, "next")
	}
	for _, line := range strings.Split(card.OpenQuestions, "\n") {
		add(line, domain.StepPending, "ask")
	}
	return out
}

func (r *Runner) queueTaskSummary(ctx context.Context, taskID int64, at time.Time) {
	if !r.Summarize {
		return
	}
	source, err := r.store.TaskSummarySource(ctx, taskID)
	if err != nil {
		return
	}
	_ = r.store.MarkSummaryPending(ctx, "task", taskID, normalize.Hash(source), at)
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
