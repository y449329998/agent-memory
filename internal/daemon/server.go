package daemon

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

const (
	maxBodyBytes   = 1 << 20 // 1MB：记忆条目不该更大
	requestTimeout = 10 * time.Second
)

type Config struct {
	Token   string
	DataDir string
	Port    int // 0 表示随机端口
	// 简报预算与卡片数量由 serve 从配置带进来，handoff 端点不写死
	BudgetTokens int
	RecentCards  int
}

// Server 是回环 HTTP 服务。它把 store 的能力摊成 JSON 接口，
// MCP 层与 CLI 都走这一层，保证「唯一写入口」这条边界只有一处。
type Server struct {
	cfg Config
	st  *store.Store
	mux *http.ServeMux
}

func New(cfg Config, st *store.Store) *Server {
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /v1/stats", s.auth(s.handleStats))
	s.mux.HandleFunc("POST /v1/search", s.auth(s.handleSearch))
	s.mux.HandleFunc("POST /v1/memory", s.auth(s.handleAddMemory))
	s.mux.HandleFunc("GET /v1/memory", s.auth(s.handleListMemory))
	s.mux.HandleFunc("GET /v1/handoff", s.auth(s.handleHandoff))
	s.mux.HandleFunc("GET /v1/task", s.auth(s.handleListTasks))
	s.mux.HandleFunc("POST /v1/project/resolve", s.auth(s.handleResolveProject))
	s.mux.HandleFunc("POST /mcp", s.auth(s.handleMCP))
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

type handlerFunc func(http.ResponseWriter, *http.Request)

// auth 校验 bearer token。只监听回环并不意味着安全：本机任何进程都能连 127.0.0.1，
// 而库里存的是用户偏好与项目约束，所以仍然要令牌，且比较走常数时间。
func (s *Server) auth(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		got := r.Header.Get("Authorization")
		if len(got) <= len(prefix) || !strings.HasPrefix(got, prefix) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(s.cfg.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": "m3"})
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.st.Stats(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

type searchRequest struct {
	Query        string `json:"query"`
	ProjectPath  string `json:"project_path"`
	ProjectKey   string `json:"project_key"`
	Type         string `json:"type"`
	Scope        string `json:"scope"`
	IncludeStale bool   `json:"include_stale"`
	Limit        int    `json:"limit"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		http.Error(w, "query is required", http.StatusBadRequest)
		return
	}
	projectID, err := s.projectIDFromRequest(r.Context(), req.ProjectPath, req.ProjectKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	hits, err := store.NewFTS(s.st).Search(r.Context(), domain.SearchQuery{
		Text:         req.Query,
		ProjectID:    projectID,
		Type:         domain.MemoryType(req.Type),
		Scope:        domain.Scope(req.Scope),
		IncludeStale: req.IncludeStale,
		Limit:        req.Limit,
	})
	if err != nil {
		httpError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		agents := make([]string, 0, len(h.Memory.Sources))
		for _, src := range h.Memory.Sources {
			agents = append(agents, src.Agent)
		}
		out = append(out, map[string]any{
			"id":           h.Memory.ID,
			"scope":        h.Memory.Scope,
			"type":         h.Memory.Type,
			"text":         h.Memory.Text,
			"stale":        h.Memory.Stale,
			"last_seen_at": h.Memory.LastSeenAt.Format(time.RFC3339),
			"agents":       agents,
			"sources":      h.Memory.Sources,
			"score":        h.Score,
			"origin":       h.Origin,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hits": out, "count": len(out)})
}

type addRequest struct {
	Text        string `json:"text"`
	Type        string `json:"type"`
	Scope       string `json:"scope"`
	ProjectPath string `json:"project_path"`
	Agent       string `json:"agent"`
	Supersedes  int64  `json:"supersedes"`
}

type addResult struct {
	ID      int64
	Created bool
	Scope   domain.Scope
	Type    domain.MemoryType
}

// addMemory 是记忆的唯一写入口：HTTP 端点与 MCP 工具都调它，
// 这样校验规则只存在一份。返回的错误若是 clientError，调用方应回 400。
func (s *Server) addMemory(ctx context.Context, req addRequest) (addResult, error) {
	if strings.TrimSpace(req.Text) == "" {
		return addResult{}, badInput("text is required")
	}
	typ := domain.MemoryType(req.Type)
	if typ == "" {
		typ = domain.TypeFact
	}
	if !domain.ValidMemoryType(typ) {
		return addResult{}, badInput("unknown type %q", req.Type)
	}
	scope := domain.ScopeUser
	if req.Scope != "" {
		scope = domain.Scope(req.Scope)
		if scope != domain.ScopeUser && scope != domain.ScopeProject {
			return addResult{}, badInput("unknown scope %q", req.Scope)
		}
	}
	agent := req.Agent
	if agent == "" {
		agent = "mcp"
	}

	projectID := int64(0)
	if scope == domain.ScopeProject {
		if strings.TrimSpace(req.ProjectPath) == "" {
			return addResult{}, badInput("scope=project needs project_path")
		}
		// 与扫描共用同一套解析顺序（路径映射 → git remote）：直接按空 remote 建项目
		// 会把同一个仓库拆成「remote 项目」和「路径项目」两个。
		id, _, err := s.st.ResolveOrCreateProject(ctx, agent, "", req.ProjectPath, time.Now())
		if err != nil {
			return addResult{}, err
		}
		projectID = id
	}

	id, created, err := s.st.UpsertMemory(ctx, projectID, domain.MemoryDraft{
		Scope: scope, Type: typ, Text: req.Text,
		SourceAgent: agent, SourcePath: "mcp", Quote: req.Text,
	}, time.Now())
	if err != nil {
		return addResult{}, err
	}
	if req.Supersedes != 0 && req.Supersedes != id {
		if err := s.st.SetStaleByID(ctx, req.Supersedes, true); err != nil {
			return addResult{}, err
		}
	}
	return addResult{ID: id, Created: created, Scope: scope, Type: typ}, nil
}

func (s *Server) handleAddMemory(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	res, err := s.addMemory(r.Context(), req)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": res.ID, "created": res.Created, "scope": res.Scope, "type": res.Type,
	})
}

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	projectRef := r.URL.Query().Get("project_path")
	if projectRef == "" {
		projectRef = r.URL.Query().Get("project_key")
	}
	projectID, err := s.projectIDFromRequest(r.Context(), projectRef, projectRef)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	items, err := s.st.ListMemories(r.Context(), projectID, r.URL.Query().Get("include_stale") == "true", 50)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": items, "count": len(items)})
}

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("project_path")
	if ref == "" {
		ref = r.URL.Query().Get("project_key")
	}
	ctx := r.Context()
	projectID, err := s.projectIDFromRequest(ctx, ref, ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
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
		httpError(w, err)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		writeJSON(w, http.StatusOK, map[string]any{
			"project_id": projectID, "body": res.Body, "tokens": res.Tokens, "truncated": res.Truncated,
		})
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	io.WriteString(w, res.Body)
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("project_path")
	if ref == "" {
		ref = r.URL.Query().Get("project_key")
	}
	projectID, err := s.projectIDFromRequest(r.Context(), ref, ref)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cards, err := s.st.ListTaskCards(r.Context(), projectID, r.URL.Query().Get("status"), 10)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cards": cards, "count": len(cards)})
}

func (s *Server) handleResolveProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	ctx := r.Context()
	id, ok, err := s.st.ResolveProjectByPath(ctx, req.Path)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"known": false, "path": req.Path})
		return
	}
	project, _, err := s.st.ProjectByID(ctx, id)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"known": true, "project": project})
}

// projectIDFromRequest 允许调用方用路径或 key 引用项目；空引用表示不限定（只查用户级）。
func (s *Server) projectIDFromRequest(ctx context.Context, pathRef, keyRef string) (int64, error) {
	if strings.TrimSpace(pathRef) == "" {
		return 0, nil
	}
	if id, ok, err := s.st.ResolveProjectByPath(ctx, pathRef); err != nil {
		return 0, err
	} else if ok {
		return id, nil
	}
	if keyRef != "" {
		if p, ok, err := s.st.ProjectByKey(ctx, keyRef); err != nil {
			return 0, err
		} else if ok {
			return p.ID, nil
		}
	}
	return 0, fmt.Errorf("unknown project %q", pathRef)
}

// clientError 表示调用方输入有问题，httpError 据此回 400 而不是 500。
type clientError struct{ msg string }

func (e clientError) Error() string { return e.msg }

func badInput(format string, args ...any) error {
	return clientError{msg: fmt.Sprintf(format, args...)}
}

// errBodyTooLarge 单独成错误，好让 httpError 回 413 而不是 400。
var errBodyTooLarge = errors.New("request body exceeds 1MB limit")

func decodeJSON(r *http.Request, dst any) error {
	// 多读一个字节用来判定超限，而不是 LimitReader 截断后把「半个 JSON」
	// 当成语法错误报 500：1MB 上限是给客户端的明确规则，应回 413。
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return clientError{msg: fmt.Sprintf("read request body: %v", err)}
	}
	if len(data) > maxBodyBytes {
		return errBodyTooLarge
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return badInput("empty request body: send a JSON object")
		}
		return badInput("bad json: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	var ce clientError
	switch {
	case errors.Is(err, errBodyTooLarge):
		code = http.StatusRequestEntityTooLarge
	case errors.As(err, &ce):
		code = http.StatusBadRequest
	}
	http.Error(w, err.Error(), code)
}

// ListenAndServe 绑定回环地址并回填实际端口。端口为 0 时由系统分配，
// 实际值写进 port 文件供 stdio 桥读取。
func (s *Server) ListenAndServe(ctx context.Context, portPath string) error {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(s.cfg.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	actual := ln.Addr().(*net.TCPAddr).Port
	if portPath != "" {
		if err := WritePort(portPath, actual); err != nil {
			ln.Close()
			return err
		}
		defer os.Remove(portPath)
	}

	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: requestTimeout,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
