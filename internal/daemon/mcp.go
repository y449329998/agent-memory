package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/mcp"
	"github.com/muyi99/agent-memory/internal/store"
)

// Server 直接实现 mcp.ToolAPI：MCP 工具与 HTTP 端点共用同一份存储与校验。
var _ mcp.ToolAPI = (*Server)(nil)

func (s *Server) Search(ctx context.Context, in mcp.SearchInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	hits, err := store.NewFTS(s.st).Search(ctx, domain.SearchQuery{
		Text:         in.Query,
		ProjectID:    projectID,
		Type:         domain.MemoryType(in.Type),
		Scope:        domain.Scope(in.Scope),
		IncludeStale: in.IncludeStale,
		Limit:        in.Limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		out = append(out, map[string]any{
			"id": h.Memory.ID, "scope": h.Memory.Scope, "type": h.Memory.Type,
			"text": h.Memory.Text, "stale": h.Memory.Stale,
			"summary": h.Memory.Summary, "summary_state": h.Memory.SummaryState,
			"last_seen_at": h.Memory.LastSeenAt.Format(time.RFC3339),
			"sources":      h.Memory.Sources, "score": h.Score, "origin": h.Origin,
		})
	}
	return map[string]any{"hits": out, "count": len(out)}, nil
}

func (s *Server) Add(ctx context.Context, in mcp.AddInput) (any, error) {
	res, err := s.addMemory(ctx, addRequest{
		Text: in.Text, Type: in.Type, Scope: in.Scope,
		ProjectPath: in.ProjectPath, Agent: "mcp", Supersedes: in.Supersedes,
	})
	if err != nil {
		return nil, err
	}
	state := ""
	if row, ok, err := s.st.SummaryFor(ctx, "memory", res.ID); err == nil && ok {
		state = row.State
	}
	return map[string]any{
		"id": res.ID, "created": res.Created, "scope": res.Scope, "type": res.Type,
		"summary_state": state,
	}, nil
}

func (s *Server) List(ctx context.Context, in mcp.ListInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	items, err := s.st.ListMemories(ctx, projectID, in.IncludeStale, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"memories": items, "count": len(items)}, nil
}

func (s *Server) Handoff(ctx context.Context, in mcp.HandoffInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	budget, cards := s.cfg.BudgetTokens, s.cfg.RecentCards
	if budget <= 0 {
		budget = 1500
	}
	if cards <= 0 {
		cards = 3
	}
	res, err := compose.New(s.st, compose.Config{BudgetTokens: budget, RecentCards: cards}).Render(ctx, projectID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"body": res.Body, "tokens": res.Tokens, "truncated": res.Truncated}, nil
}

func (s *Server) Tasks(ctx context.Context, in mcp.TaskInput) (any, error) {
	projectID, err := s.projectIDFromRequest(ctx, in.ProjectPath, in.ProjectPath)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}
	kind := in.Kind
	switch kind {
	case domain.TaskKindSession, domain.TaskKindFormal:
	default:
		kind = ""
	}
	tasks, err := s.st.ListTasks(ctx, store.TaskFilter{
		ProjectID: projectID, Status: in.Status, Kind: kind, IncludeArchived: in.IncludeArchived, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"tasks": tasks, "cards": tasks, "count": len(tasks)}, nil
}

func (s *Server) TaskGet(ctx context.Context, id int64) (any, error) {
	detail, ok, err := s.st.TaskDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrTaskCardNotFound
	}
	return detail, nil
}

func (s *Server) AddTask(ctx context.Context, in mcp.AddTaskInput) (any, error) {
	now := time.Now()
	projectID, err := s.taskProjectID(ctx, in.ProjectPath, "mcp", now)
	if err != nil {
		return nil, err
	}
	steps := make([]domain.TaskStep, 0, len(in.Steps))
	for _, step := range in.Steps {
		steps = append(steps, domain.TaskStep{
			Subject: step.Subject, Description: step.Description,
			ActiveForm: step.ActiveForm, Status: step.Status,
			CreatedAt: now, UpdatedAt: now,
		})
	}
	id, err := s.st.CreateTask(ctx, domain.Task{
		ProjectID: projectID, Goal: in.Goal, Status: in.Status,
		NextSteps: in.NextSteps, OwnerAgent: in.OwnerAgent,
		SourceAgent: "mcp", ManualEditedAt: now, CreatedAt: now, UpdatedAt: now, Steps: steps,
	})
	if err != nil {
		return nil, err
	}
	s.markTaskSummary(ctx, id)
	s.refreshTaskHandoff(ctx, projectID)
	return map[string]any{"id": id}, nil
}

func (s *Server) UpdateTask(ctx context.Context, in mcp.UpdateTaskInput) (any, error) {
	now := time.Now()
	for key, status := range in.StepStatus {
		if err := s.st.SetStepOverride(ctx, in.ID, key, status, now); err != nil {
			return nil, err
		}
	}
	var steps *[]domain.TaskStep
	if in.Steps != nil {
		converted := make([]domain.TaskStep, 0, len(*in.Steps))
		for _, step := range *in.Steps {
			converted = append(converted, domain.TaskStep{
				Subject: step.Subject, Description: step.Description,
				ActiveForm: step.ActiveForm, Status: step.Status, SourceKey: step.SourceKey,
			})
		}
		steps = &converted
	}
	changed := in.Goal != nil || in.Status != nil || in.NextSteps != nil || in.OwnerAgent != nil || in.Archived != nil || steps != nil
	if !changed {
		detail, ok, err := s.st.TaskDetail(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, domain.ErrTaskCardNotFound
		}
		return detail, nil
	}
	task, err := s.st.UpdateTask(ctx, store.TaskUpdate{
		ID: in.ID, Goal: in.Goal, Status: in.Status, NextSteps: in.NextSteps,
		OwnerAgent: in.OwnerAgent, Archived: in.Archived, Steps: steps,
	}, now)
	if err != nil {
		return nil, err
	}
	s.markTaskSummary(ctx, task.ID)
	s.refreshTaskHandoff(ctx, task.ProjectID)
	return task, nil
}

// SyncTask 定向同步一个会话成对话任务，绕过静默窗口与哈希门控。
func (s *Server) SyncTask(ctx context.Context, in mcp.SyncTaskInput) (any, error) {
	if s.runner == nil {
		return nil, fmt.Errorf("runner not configured")
	}
	path := in.SessionID
	if path == "" {
		p, err := s.latestSessionPath(ctx, in.ProjectPath)
		if err != nil {
			return nil, err
		}
		path = p
	}
	id, settled, err := s.runner.SyncSession(ctx, agentFromPath(path), path)
	if err != nil {
		return nil, err
	}
	detail, _, err := s.st.TaskDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task_id": id, "settled": settled, "session_path": path, "task": detail}, nil
}

// latestSessionPath 取该项目下最近更新的会话文件路径。
func (s *Server) latestSessionPath(ctx context.Context, projectPath string) (string, error) {
	pid, ok, err := s.st.ResolveProjectByPath(ctx, projectPath)
	if err != nil || !ok {
		return "", fmt.Errorf("unknown project %q", projectPath)
	}
	sessions, err := s.st.ListSessions(ctx, pid, 1)
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("该项目下没有可同步的会话")
	}
	return sessions[0].Path, nil
}

// agentFromPath 按目录判断会话归属：.workbuddy → workbuddy，其余 → qoder。
func agentFromPath(path string) string {
	if strings.Contains(filepath.ToSlash(path), ".workbuddy") {
		return "workbuddy"
	}
	return "qoder"
}

func (s *Server) ExportTask(ctx context.Context, id int64) (any, error) {
	detail, ok, err := s.st.TaskDetail(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrTaskCardNotFound
	}
	project, ok, err := s.st.ProjectByID(ctx, detail.ProjectID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, domain.ErrTaskCardNotFound
	}
	return map[string]any{"markdown": compose.RenderTaskExport(detail.Task, detail.Snippets, project)}, nil
}

func (s *Server) ResolveProject(ctx context.Context, path string) (any, error) {
	id, ok, err := s.st.ResolveProjectByPath(ctx, path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]any{"known": false, "path": path}, nil
	}
	project, _, err := s.st.ProjectByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"known": true, "project_key": project.Key, "display_name": project.DisplayName}, nil
}

func (s *Server) Stats(ctx context.Context) (any, error) {
	return s.st.Stats(ctx)
}

// handleMCP 是 stdio 桥的目标。它不需要流式：每条请求都是单个 JSON-RPC 消息，
// 回单个 JSON 对象，Accept 里出现哪种都按 application/json 回。
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "read failed", http.StatusBadRequest)
		return
	}
	mcpServer := mcp.NewServer(s)
	resp, needReply := mcpServer.Handle(r.Context(), body)
	if !needReply {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(resp)
}
