package daemon

import (
	"context"
	"io"
	"net/http"
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
	return map[string]any{"id": res.ID, "created": res.Created, "scope": res.Scope, "type": res.Type}, nil
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
	cards, err := s.st.ListTaskCards(ctx, projectID, in.Status, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"cards": cards, "count": len(cards)}, nil
}

func (s *Server) TaskGet(ctx context.Context, id int64) (any, error) {
	card, err := s.st.GetTaskCard(ctx, id)
	if err != nil {
		return nil, err
	}
	return card, nil
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
