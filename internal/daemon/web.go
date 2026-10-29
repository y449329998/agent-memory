package daemon

import (
	"crypto/subtle"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
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

// handleListSources 列出扫描源，供「源 / 失败源」抽屉使用。
func (s *Server) handleListSources(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	ref := r.URL.Query().Get("project_path")
	if ref == "" {
		ref = r.URL.Query().Get("project_key")
	}
	if ref != "" {
		id, err := s.projectIDFromRequest(r.Context(), ref, ref)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		rows, total, failed, err := s.st.ListProjectSources(r.Context(), id, limit)
		if err != nil {
			httpError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"sources": rows, "count": len(rows), "total": total, "failed": failed,
		})
		return
	}
	agent := r.URL.Query().Get("agent")
	failedOnly := r.URL.Query().Get("failed") == "1"
	total, err := s.st.CountSources(r.Context(), agent, failedOnly)
	if err != nil {
		httpError(w, err)
		return
	}
	failed, err := s.st.CountSources(r.Context(), agent, true)
	if err != nil {
		httpError(w, err)
		return
	}
	rows, err := s.st.ListSources(r.Context(), agent, failedOnly, limit)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": rows, "count": len(rows), "total": total, "failed": failed})
}

// handleShutdown 受理托盘「停止服务并退出」：先应答再异步取消服务上下文。
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	if s.cfg.Shutdown != nil {
		go s.cfg.Shutdown()
	}
}

type desktopSettings struct {
	Autostart bool   `json:"autostart"`
	Command   string `json:"command,omitempty"`
}

// handleDesktopSettingsGet 读取开机自启状态（注册表 + 桌面应用写入的路径）。
func (s *Server) handleDesktopSettingsGet(w http.ResponseWriter, r *http.Request) {
	enabled, command := desktopAutostartState()
	writeJSON(w, http.StatusOK, desktopSettings{Autostart: enabled, Command: command})
}

// handleDesktopSettingsSet 写/删 HKCU Run。没有 desktop.json 时返回 400，
// 提示先去打开一次桌面应用——否则写进去的自启命令是空的。
func (s *Server) handleDesktopSettingsSet(w http.ResponseWriter, r *http.Request) {
	var req desktopSettings
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	appPath, ok := desktopAppPathFromFile(s.cfg.DataDir)
	if !ok {
		http.Error(w, "还没有打开过桌面应用：先运行一次 agent-memory-app.exe", http.StatusBadRequest)
		return
	}
	if err := desktopSetAutostart(req.Autostart, appPath); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, desktopSettings{Autostart: req.Autostart})
}
