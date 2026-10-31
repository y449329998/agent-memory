package daemon

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
	"github.com/muyi99/agent-memory/internal/surface"
)

type taskStepRequest struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"active_form"`
	Status      string `json:"status"`
}

type taskCreateRequest struct {
	ProjectPath string            `json:"project_path"`
	Goal        string            `json:"goal"`
	Status      string            `json:"status"`
	NextSteps   string            `json:"next_steps"`
	OwnerAgent  string            `json:"owner_agent"`
	Steps       []taskStepRequest `json:"steps"`
}

type taskUpdateRequest struct {
	ID         int64              `json:"id"`
	Goal       *string            `json:"goal"`
	Status     *string            `json:"status"`
	NextSteps  *string            `json:"next_steps"`
	OwnerAgent *string            `json:"owner_agent"`
	Archived   *bool              `json:"archived"`
	Steps      *[]taskStepRequest `json:"steps"`
}

func convertSteps(in []taskStepRequest, at time.Time) []domain.TaskStep {
	out := make([]domain.TaskStep, 0, len(in))
	for _, step := range in {
		out = append(out, domain.TaskStep{
			Subject: step.Subject, Description: step.Description,
			ActiveForm: step.ActiveForm, Status: step.Status,
			CreatedAt: at, UpdatedAt: at,
		})
	}
	return out
}

func (s *Server) taskProjectID(ctx context.Context, path, agent string, at time.Time) (int64, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, clientError{msg: "project_path is required"}
	}
	if id, err := s.projectIDFromRequest(ctx, path, ""); err == nil {
		return id, nil
	}
	id, _, err := s.st.ResolveOrCreateProject(ctx, agent, "", path, at)
	return id, err
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var req taskCreateRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if strings.TrimSpace(req.Goal) == "" {
		http.Error(w, "goal is required", http.StatusBadRequest)
		return
	}
	now := time.Now()
	project, err := s.taskProjectID(r.Context(), req.ProjectPath, "web", now)
	if err != nil {
		httpError(w, err)
		return
	}
	id, err := s.st.CreateTask(r.Context(), domain.Task{
		ProjectID: project, Goal: req.Goal, Status: req.Status,
		NextSteps: req.NextSteps, OwnerAgent: req.OwnerAgent,
		SourceAgent: "web", ManualEditedAt: now, CreatedAt: now, UpdatedAt: now,
		Steps: convertSteps(req.Steps, now),
	})
	if err != nil {
		httpError(w, err)
		return
	}
	s.refreshTaskHandoff(r.Context(), project)
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (s *Server) handleListFormalTasks(w http.ResponseWriter, r *http.Request) {
	projectID := int64(0)
	if key := r.URL.Query().Get("project_key"); key != "" {
		id, err := s.projectIDFromRequest(r.Context(), key, key)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		projectID = id
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	includeArchived := r.URL.Query().Get("include_archived") == "1"
	tasks, err := s.st.ListTasks(r.Context(), store.TaskFilter{
		ProjectID: projectID, Status: r.URL.Query().Get("status"),
		IncludeArchived: includeArchived, Limit: limit,
	})
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "count": len(tasks)})
}

func (s *Server) handleGetFormalTask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "bad task id", http.StatusBadRequest)
		return
	}
	detail, ok, err := s.st.TaskDetail(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	var req taskUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if req.ID <= 0 {
		http.Error(w, "id must be positive", http.StatusBadRequest)
		return
	}
	var steps *[]domain.TaskStep
	if req.Steps != nil {
		converted := convertSteps(*req.Steps, time.Now())
		steps = &converted
	}
	task, err := s.st.UpdateTask(r.Context(), store.TaskUpdate{
		ID: req.ID, Goal: req.Goal, Status: req.Status, NextSteps: req.NextSteps,
		OwnerAgent: req.OwnerAgent, Archived: req.Archived, Steps: steps,
	}, time.Now())
	if err != nil {
		httpError(w, err)
		return
	}
	s.refreshTaskHandoff(r.Context(), task.ProjectID)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleMergeTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SourceID int64 `json:"source_id"`
		TargetID int64 `json:"target_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if err := s.st.MergeTasks(r.Context(), req.SourceID, req.TargetID, time.Now()); err != nil {
		httpError(w, err)
		return
	}
	s.refreshTaskHandoff(r.Context(), req.TargetID)
	writeJSON(w, http.StatusOK, map[string]any{"source_id": req.SourceID, "target_id": req.TargetID})
}

func (s *Server) handleExportTask(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "bad task id", http.StatusBadRequest)
		return
	}
	detail, ok, err := s.st.TaskDetail(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	project, ok, err := s.st.ProjectByID(r.Context(), detail.ProjectID)
	if err != nil || !ok {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"markdown": compose.RenderTaskExport(detail.Task, detail.Snippets, project)})
}

func (s *Server) refreshTaskHandoff(ctx context.Context, projectID int64) {
	project, ok, err := s.st.ProjectByID(ctx, projectID)
	if err != nil || !ok {
		return
	}
	res, err := compose.New(s.st, compose.Config{
		BudgetTokens: s.cfg.BudgetTokens, RecentCards: s.cfg.RecentCards,
	}).Render(ctx, projectID)
	if err != nil {
		return
	}
	dirs, err := s.st.ListProjectDirs(ctx, projectID)
	if err != nil {
		return
	}
	for _, key := range dirs {
		dir := filepath.FromSlash(key)
		if surface.IsProtectedAgentDir(dir) {
			continue
		}
		_, _, _ = surface.WriteHandoff(dir, res.Body)
	}
	if len(dirs) == 0 && project.PrimaryPath != "" && !surface.IsProtectedAgentDir(project.PrimaryPath) {
		_, _, _ = surface.WriteHandoff(project.PrimaryPath, res.Body)
	}
}
