package daemon

import (
	"crypto/subtle"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/store"
	"github.com/muyi99/agent-memory/internal/surface"
)

//go:embed index.html
var indexHTML []byte

// handleIndex 返回内嵌单页。token 只从 ?token= 或 Authorization 取，
// 页面本身不带 Bearer 也能加载；前端拿到后用它调 /v1/*。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		token = bearerToken(r)
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.Token)) != 1 {
		http.Error(w, "unauthorized: 浏览器打开 /?token=<token>", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

// writeAuth 是写接口的守卫：先校验 Bearer，再校验 Origin。
// 不设任何 CORS 响应头；浏览器跨站写请求一定带 Origin，非本机回环来源一律拒绝。
func (s *Server) writeAuth(next handlerFunc) handlerFunc {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		if !originAllowed(r) {
			http.Error(w, "cross-origin write rejected", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// originAllowed 允许没有 Origin 的请求（curl/CLI 与单元测试），
// 以及同源的浏览器请求。端口必须等于本次请求命中的 daemon 端口，
// 否则本机其他端口上的页面也能跨站写入。
func originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	if u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
		return false
	}
	reqPort := ""
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		reqPort = port
	}
	return u.Port() == reqPort
}

// handleScan 触发一轮扫描。编排由 cmdServe 注入，端点只负责鉴权与回包；
// 未注入说明当前 server 只服务不扫描，回 501 让调用方明确知道。
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Scan == nil {
		http.Error(w, "scan is not available on this server", http.StatusNotImplemented)
		return
	}
	res, err := s.cfg.Scan(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	if res == nil {
		res = map[string]any{}
	}
	writeJSON(w, http.StatusOK, res)
}

type handoffWriteRequest struct {
	ProjectPath string `json:"project_path"`
	Write       bool   `json:"write"`
}

// handleHandoffWrite 渲染简报；write=true 时按 CLI 的同一套落盘函数写入
// <project>/.agent-memory/HANDOFF.md 并把简报记进库里。write=false 只回正文。
func (s *Server) handleHandoffWrite(w http.ResponseWriter, r *http.Request) {
	var req handoffWriteRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	path := strings.TrimSpace(req.ProjectPath)
	if path == "" {
		http.Error(w, "project_path is required", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 与 CLI 一样，agent 自己的配置树永远不是写入目标。
	if surface.IsProtectedAgentDir(abs) {
		http.Error(w, fmt.Sprintf("拒绝写入 agent 原生目录 %s", abs), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	projectID, ok, err := s.st.ResolveProjectByPath(ctx, abs)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		http.Error(w, fmt.Sprintf("unknown project %q", abs), http.StatusNotFound)
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
	if !req.Write {
		writeJSON(w, http.StatusOK, map[string]any{"body": res.Body})
		return
	}

	filePath, changed, err := surface.WriteHandoff(abs, res.Body)
	if err != nil {
		httpError(w, err)
		return
	}
	if changed {
		if _, err := surface.EnsureGitExclude(abs, "/"+surface.DirName+"/"); err != nil {
			httpError(w, err)
			return
		}
	}
	if _, err := s.st.SaveHandoff(ctx, store.HandoffRow{
		ProjectID: projectID, Body: res.Body, ContentHash: res.ContentHash,
		BudgetTokens: budget, GeneratedAt: time.Now(),
	}); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"body": res.Body, "tokens": res.Tokens, "truncated": res.Truncated,
		"path": filePath, "changed": changed,
	})
}

type staleRequest struct {
	ID    int64 `json:"id"`
	Stale *bool `json:"stale"`
}

// handleSetStale 手动置/清单条记忆的过时标记，供前端纠错。
func (s *Server) handleSetStale(w http.ResponseWriter, r *http.Request) {
	var req staleRequest
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if req.Stale == nil {
		http.Error(w, "stale is required", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	n, err := s.st.QueryInt(ctx, `SELECT count(*) FROM memory_item WHERE id = ?`, req.ID)
	if err != nil {
		httpError(w, err)
		return
	}
	if n == 0 {
		http.Error(w, fmt.Sprintf("memory %d not found", req.ID), http.StatusNotFound)
		return
	}
	if err := s.st.SetStaleByID(ctx, req.ID, *req.Stale); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": req.ID, "stale": *req.Stale})
}
