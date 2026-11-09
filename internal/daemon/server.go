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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/ingest"
	"github.com/muyi99/agent-memory/internal/normalize"
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
	MCPBinary    string
	ConfigPath   string
	HomeDir      string
	ReloadScan   func(context.Context, config.Config) error
	ReloadLLM    func(config.Config) error
	// SummaryEnabled 报告 LLM 摘要是否就绪（启用且 base_url/model/key 齐全）。
	// 未注入或返回 false 时不创建摘要任务。
	SummaryEnabled func() bool
	// Scan 由 cmdServe 注入「扫一轮 + 刷新简报」的编排；未注入时 /v1/scan 回 501。
	Scan func(context.Context) (map[string]any, error)
	// Shutdown 由 cmdServe 注入；托盘「停止服务并退出」会调用它。
	Shutdown func()
}

// Server 是回环 HTTP 服务。它把 store 的能力摊成 JSON 接口，
// MCP 层与 CLI 都走这一层，保证「唯一写入口」这条边界只有一处。
type Server struct {
	cfg Config
	st  *store.Store
	mux *http.ServeMux

	runner *ingest.Runner

	listenMu   sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	portPath   string
	actualPort int
}

// SetRunner 供 serve 装配定向同步能力（task_sync）；未设置时该工具报错。
func (s *Server) SetRunner(r *ingest.Runner) { s.runner = r }

func New(cfg Config, st *store.Store) *Server {
	s := &Server{cfg: cfg, st: st, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /{$}", s.handleHomePage)
	s.mux.HandleFunc("GET /p/{key...}", s.handleProjectPage)
	s.mux.HandleFunc("GET /m/{id}", s.handleEditPage)
	s.mux.HandleFunc("GET /memories", s.handleMemoriesPage)
	s.mux.HandleFunc("GET /sources", s.handleSourcesPage)
	s.mux.HandleFunc("GET /tasks", s.handleTasksPage)
	s.mux.HandleFunc("GET /t/new", s.handleTaskPage)
	s.mux.HandleFunc("GET /t/{id}", s.handleTaskPage)
	s.mux.HandleFunc("GET /mcp", s.handleMCPPage)
	s.mux.HandleFunc("GET /settings", s.handleSettingsPage)
	s.mux.HandleFunc("GET /assets/{file}", s.handleAsset)
	s.mux.HandleFunc("GET /v1/healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /v1/stats", s.auth(s.handleStats))
	s.mux.HandleFunc("GET /v1/projects", s.auth(s.handleListProjects))
	s.mux.HandleFunc("GET /v1/sources", s.auth(s.handleListSources))
	s.mux.HandleFunc("GET /v1/sessions", s.auth(s.handleListSessions))
	s.mux.HandleFunc("POST /v1/search", s.auth(s.handleSearch))
	s.mux.HandleFunc("POST /v1/memory", s.auth(s.handleAddMemory))
	s.mux.HandleFunc("GET /v1/memory", s.auth(s.handleListMemory))
	s.mux.HandleFunc("GET /v1/memory/item", s.auth(s.handleGetMemory))
	s.mux.HandleFunc("GET /v1/handoff", s.auth(s.handleHandoff))
	s.mux.HandleFunc("GET /v1/task", s.auth(s.handleListTasks))
	s.mux.HandleFunc("GET /v1/tasks", s.auth(s.handleListFormalTasks))
	s.mux.HandleFunc("GET /v1/tasks/item", s.auth(s.handleGetFormalTask))
	s.mux.HandleFunc("POST /v1/tasks", s.writeAuth(s.handleCreateTask))
	s.mux.HandleFunc("POST /v1/tasks/update", s.writeAuth(s.handleUpdateTask))
	s.mux.HandleFunc("POST /v1/tasks/merge", s.writeAuth(s.handleMergeTask))
	s.mux.HandleFunc("GET /v1/tasks/export", s.auth(s.handleExportTask))
	s.mux.HandleFunc("GET /v1/task/conversation", s.auth(s.handleTaskConversation))
	s.mux.HandleFunc("GET /v1/task/files", s.auth(s.handleTaskFiles))
	s.mux.HandleFunc("POST /v1/tasks/step/status", s.writeAuth(s.handleStepStatus))
	s.mux.HandleFunc("GET /v1/mcp/snippets", s.auth(s.handleMCPSnippets))
	s.mux.HandleFunc("GET /v1/settings", s.auth(s.handleSettingsGet))
	s.mux.HandleFunc("POST /v1/settings/service", s.writeAuth(s.handleSettingsService))
	s.mux.HandleFunc("POST /v1/settings/scan", s.writeAuth(s.handleSettingsScan))
	s.mux.HandleFunc("POST /v1/settings/llm", s.writeAuth(s.handleSettingsLLM))
	s.mux.HandleFunc("POST /v1/settings/llm/test", s.writeAuth(s.handleSettingsLLMTest))
	s.mux.HandleFunc("POST /v1/settings/llm/retry", s.writeAuth(s.handleSettingsLLMRetry))
	s.mux.HandleFunc("POST /v1/data/backup", s.writeAuth(s.handleDataBackup))
	s.mux.HandleFunc("GET /v1/data/export", s.auth(s.handleDataExport))
	s.mux.HandleFunc("POST /v1/data/import", s.writeAuth(s.handleDataImport))
	s.mux.HandleFunc("POST /v1/data/import/apply", s.writeAuth(s.handleDataImportApply))
	s.mux.HandleFunc("POST /v1/data/open", s.writeAuth(s.handleDataOpen))
	s.mux.HandleFunc("POST /v1/project/resolve", s.auth(s.handleResolveProject))
	s.mux.HandleFunc("POST /v1/scan", s.writeAuth(s.handleScan))
	s.mux.HandleFunc("POST /v1/handoff", s.writeAuth(s.handleHandoffWrite))
	s.mux.HandleFunc("POST /v1/memory/stale", s.writeAuth(s.handleSetStale))
	s.mux.HandleFunc("POST /v1/memory/update", s.writeAuth(s.handleUpdateMemory))
	s.mux.HandleFunc("POST /mcp", s.auth(s.handleMCP))
	s.mux.HandleFunc("POST /v1/shutdown", s.writeAuth(s.handleShutdown))
	s.mux.HandleFunc("GET /v1/desktop-settings", s.auth(s.handleDesktopSettingsGet))
	s.mux.HandleFunc("POST /v1/desktop-settings", s.writeAuth(s.handleDesktopSettingsSet))
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

type handlerFunc func(http.ResponseWriter, *http.Request)

const bearerPrefix = "Bearer "

// bearerToken 取 Authorization 头里的 Bearer token；页面路由与 auth 共用这一份解析。
func bearerToken(r *http.Request) string {
	got := r.Header.Get("Authorization")
	if len(got) <= len(bearerPrefix) || !strings.HasPrefix(got, bearerPrefix) {
		return ""
	}
	return got[len(bearerPrefix):]
}

// auth 校验 bearer token。只监听回环并不意味着安全：本机任何进程都能连 127.0.0.1，
// 而库里存的是用户偏好与项目约束，所以仍然要令牌，且比较走常数时间。
func (s *Server) auth(next handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) != 1 {
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

// projectSummary 是 /v1/projects 的对外形状：只暴露前端需要的字段，
// 用 snake_case 与其余接口保持一致，不把 git_remote 等内部信息带出去。
type projectSummary struct {
	ID           int64  `json:"id"`
	Key          string `json:"key"`
	DisplayName  string `json:"display_name"`
	PrimaryPath  string `json:"primary_path"`
	MemoryCount  int    `json:"memory_count"`
	StaleCount   int    `json:"stale_count"`
	LastMemoryAt string `json:"last_memory_at"`
	PathExists   bool   `json:"path_exists"`
	TaskCount    int    `json:"task_count"`
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	summaries, err := s.st.ListProjectSummaries(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	out := make([]projectSummary, 0, len(summaries))
	for _, p := range summaries {
		last := ""
		if !p.LastMemoryAt.IsZero() {
			last = p.LastMemoryAt.Format(time.RFC3339)
		}
		out = append(out, projectSummary{
			ID: p.ID, Key: p.Key, DisplayName: p.DisplayName, PrimaryPath: p.PrimaryPath,
			MemoryCount: p.MemoryCount, StaleCount: p.StaleCount, LastMemoryAt: last,
			PathExists: p.PrimaryPath != "" && dirExists(p.PrimaryPath), TaskCount: p.TaskCount,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out, "count": len(out)})
}

type searchRequest struct {
	Query        string `json:"query"`
	ProjectPath  string `json:"project_path"`
	ProjectKey   string `json:"project_key"`
	Type         string `json:"type"`
	Scope        string `json:"scope"`
	All          bool   `json:"all"`
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
		AllProjects:  req.All,
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
			"id":            h.Memory.ID,
			"scope":         h.Memory.Scope,
			"type":          h.Memory.Type,
			"text":          h.Memory.Text,
			"stale":         h.Memory.Stale,
			"summary":       h.Memory.Summary,
			"summary_state": h.Memory.SummaryState,
			"last_seen_at":  h.Memory.LastSeenAt.Format(time.RFC3339),
			"agents":        agents,
			"sources":       h.Memory.Sources,
			"score":         h.Score,
			"origin":        h.Origin,
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
	s.markMemorySummary(ctx, id, req.Text)
	return addResult{ID: id, Created: created, Scope: scope, Type: typ}, nil
}

func (s *Server) summaryEnabled() bool {
	return s.cfg.SummaryEnabled != nil && s.cfg.SummaryEnabled()
}

func (s *Server) markMemorySummary(ctx context.Context, id int64, text string) {
	if !s.summaryEnabled() {
		return
	}
	_ = s.st.MarkSummaryPending(ctx, "memory", id, normalize.Hash(normalize.CanonicalText(text)), time.Now())
}

func (s *Server) markTaskSummary(ctx context.Context, id int64) {
	if !s.summaryEnabled() {
		return
	}
	source, err := s.st.TaskSummarySource(ctx, id)
	if err != nil {
		return
	}
	_ = s.st.MarkSummaryPending(ctx, "task", id, normalize.Hash(source), time.Now())
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

type updateMemoryRequest struct {
	ID   int64  `json:"id"`
	Text string `json:"text"`
	Type string `json:"type"`
}

func (s *Server) handleUpdateMemory(w http.ResponseWriter, r *http.Request) {
	var req updateMemoryRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if req.ID <= 0 {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	merged, err := s.st.UpdateMemory(r.Context(), req.ID, req.Text, domain.MemoryType(req.Type), time.Now())
	switch {
	case errors.Is(err, domain.ErrMemoryNotFound):
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	case errors.Is(err, domain.ErrEmptyMemory), errors.Is(err, domain.ErrInvalidMemoryType):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case err != nil:
		httpError(w, err)
		return
	}
	target := req.ID
	if merged != 0 {
		target = merged
	}
	s.markMemorySummary(r.Context(), target, req.Text)
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "merged_into": merged})
}

func (s *Server) handleGetMemory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	m, err := s.st.MemoryByID(r.Context(), id)
	if errors.Is(err, domain.ErrMemoryNotFound) {
		http.Error(w, "memory not found", http.StatusNotFound)
		return
	}
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleListMemory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	filter := store.MemoryFilter{
		AllProjects: q.Get("all") == "1",
		Scope:       domain.Scope(q.Get("scope")),
		Type:        domain.MemoryType(q.Get("type")),
	}
	if v := q.Get("stale"); v == "1" || v == "0" {
		stale := v == "1"
		filter.Stale = &stale
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			filter.Offset = n
		}
	}
	ref := q.Get("project_path")
	if ref == "" {
		ref = q.Get("project_key")
	}
	if !filter.AllProjects && ref != "" {
		id, err := s.projectIDFromRequest(ctx, ref, ref)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		filter.ProjectID = id
	}

	total, err := s.st.CountMemoriesFiltered(ctx, filter)
	if err != nil {
		httpError(w, err)
		return
	}
	items, err := s.st.ListMemoriesFiltered(ctx, filter)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"memories": items, "count": len(items), "total": total})
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
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
			if limit > 50 {
				limit = 50
			}
		}
	}
	cards, err := s.st.ListTaskCards(r.Context(), projectID, r.URL.Query().Get("status"), limit)
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

// projectIDFromRequest 允许调用方用路径或 key 引用项目；两者都空表示不限定（只查用户级）。
// 只给 key 时必须走 ProjectByKey：调用方（MCP 与 /v1/search）经常手里只有 key。
func (s *Server) projectIDFromRequest(ctx context.Context, pathRef, keyRef string) (int64, error) {
	pathRef = strings.TrimSpace(pathRef)
	keyRef = strings.TrimSpace(keyRef)
	if pathRef == "" && keyRef == "" {
		return 0, nil
	}
	if pathRef != "" {
		if id, ok, err := s.st.ResolveProjectByPath(ctx, pathRef); err != nil {
			return 0, err
		} else if ok {
			return id, nil
		}
	}
	if keyRef != "" {
		if p, ok, err := s.st.ProjectByKey(ctx, keyRef); err != nil {
			return 0, err
		} else if ok {
			return p.ID, nil
		}
	}
	ref := pathRef
	if ref == "" {
		ref = keyRef
	}
	return 0, fmt.Errorf("unknown project %q", ref)
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
	s.listenMu.Lock()
	s.portPath = portPath
	s.listenMu.Unlock()
	if err := s.bind(ctx, s.cfg.Port); err != nil {
		return err
	}
	<-ctx.Done()
	s.listenMu.Lock()
	srv := s.httpServer
	s.listenMu.Unlock()
	if srv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}
	if portPath != "" {
		_ = os.Remove(portPath)
	}
	return nil
}

func (s *Server) listening() bool {
	s.listenMu.Lock()
	defer s.listenMu.Unlock()
	return s.listener != nil
}

// Rebind 在同一进程内切换监听端口；新端口起不来时旧监听器保持不变。
func (s *Server) Rebind(ctx context.Context, port int) error {
	return s.bind(ctx, port)
}

func (s *Server) bind(ctx context.Context, port int) error {
	addr := net.JoinHostPort("127.0.0.1", fmt.Sprint(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", addr, err)
	}
	actual := ln.Addr().(*net.TCPAddr).Port
	s.listenMu.Lock()
	portPath := s.portPath
	s.listenMu.Unlock()
	if portPath != "" {
		if err := WritePort(portPath, actual); err != nil {
			ln.Close()
			return err
		}
	}
	srv := &http.Server{Handler: s.mux, ReadHeaderTimeout: requestTimeout}
	go func() { _ = srv.Serve(ln) }()

	s.listenMu.Lock()
	old := s.httpServer
	s.httpServer = srv
	s.listener = ln
	s.actualPort = actual
	s.listenMu.Unlock()
	if old != nil {
		// 调用方通常正运行在旧 server 的 handler 里（设置页改端口）；
		// 同步 Shutdown 会等当前 handler 返回，形成自等。这里异步收尾。
		go func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = old.Shutdown(shutCtx)
		}()
	}
	return nil
}

func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
