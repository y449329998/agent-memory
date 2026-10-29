package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/surface"
)

const webToken = "test-token-0123456789abcdef"

// webDo 发一个带 token 的请求；mutate 用来加 Origin 或改 Host 模拟浏览器。
func webDo(t *testing.T, s *Server, method, target, token string, body any, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, target, &payload)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestIndexPageTokenAuth(t *testing.T) {
	s, _ := newTestServer(t)

	rec := webDo(t, s, http.MethodGet, "/", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code = %d, want 401", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "浏览器打开") || !strings.Contains(body, "/?token=") {
		t.Fatalf("401 body should tell the user how to open the page: %q", body)
	}

	rec = webDo(t, s, http.MethodGet, "/?token=wrong", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: code = %d, want 401", rec.Code)
	}

	rec = webDo(t, s, http.MethodGet, "/?token="+webToken, "", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("query token: code = %d body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	page := rec.Body.String()
	if !strings.Contains(page, "用户级记忆") || !strings.Contains(page, "sessionStorage") {
		t.Fatalf("web ui page missing expected content: %q", page)
	}
	if strings.Contains(page, "<script src=") || strings.Contains(page, "<link rel=\"stylesheet\"") {
		t.Fatal("web ui must be a single self-contained page without external assets")
	}
	// 记忆正文来自外部文件，动态内容只能走 textContent；页面里不应出现 HTML 注入入口。
	if strings.Contains(page, "innerHTML") || strings.Contains(page, "insertAdjacentHTML") {
		t.Fatal("web ui must not use innerHTML/insertAdjacentHTML for dynamic content")
	}

	rec = webDo(t, s, http.MethodGet, "/", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer token: code = %d, want 200", rec.Code)
	}
}

func TestProjectsEndpointEmpty(t *testing.T) {
	s, _ := newTestServer(t)

	rec := webDo(t, s, http.MethodGet, "/v1/projects", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code = %d, want 401", rec.Code)
	}

	rec = webDo(t, s, http.MethodGet, "/v1/projects", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"projects":[]`) {
		t.Fatalf("empty db should return an empty array, got %s", got)
	}
	var out struct {
		Projects []projectSummary `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Projects == nil || len(out.Projects) != 0 {
		t.Fatalf("projects = %#v, want non-nil empty slice", out.Projects)
	}
}

func TestProjectsEndpointSortedByKey(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	// 故意先插 z 再插 a：返回顺序必须由 SQL 的 ORDER BY key 决定，而不是插入顺序。
	for _, p := range []struct{ key, name, path string }{
		{"z/repo", "Z 仓库", `F:\work\z`},
		{"a/repo", "A 仓库", `F:\work\a`},
	} {
		if _, err := st.EnsureProject(ctx, p.key, p.name, "", p.path, now); err != nil {
			t.Fatal(err)
		}
	}

	rec := webDo(t, s, http.MethodGet, "/v1/projects", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Projects []projectSummary `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Projects) != 2 {
		t.Fatalf("projects = %+v, want 2", out.Projects)
	}
	if out.Projects[0].Key != "a/repo" || out.Projects[1].Key != "z/repo" {
		t.Fatalf("projects not sorted by key: %+v", out.Projects)
	}
	if out.Projects[0].DisplayName != "A 仓库" || out.Projects[0].PrimaryPath != `F:\work\a` {
		t.Fatalf("project fields wrong: %+v", out.Projects[0])
	}
	if out.Projects[0].ID == 0 || out.Projects[1].ID == 0 {
		t.Fatalf("project ids missing: %+v", out.Projects)
	}
	if strings.Contains(rec.Body.String(), `"DisplayName"`) {
		t.Fatalf("response should use snake_case keys: %s", rec.Body.String())
	}
}

func TestScanEndpointWithoutCallback(t *testing.T) {
	s, _ := newTestServer(t)
	rec := webDo(t, s, http.MethodPost, "/v1/scan", webToken, nil, nil)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("code = %d, want 501", rec.Code)
	}
}

func TestScanEndpointRunsInjectedCallback(t *testing.T) {
	var calls int32
	s, _ := newTestServer(t, func(cfg *Config) {
		cfg.Scan = func(context.Context) (map[string]any, error) {
			atomic.AddInt32(&calls, 1)
			return map[string]any{"created": 7}, nil
		}
	})
	rec := webDo(t, s, http.MethodPost, "/v1/scan", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("scan callback calls = %d, want 1", got)
	}
	if !strings.Contains(rec.Body.String(), `"created":7`) {
		t.Fatalf("scan result not returned: %s", rec.Body.String())
	}
}

type handoffWriteResponse struct {
	Body      string `json:"body"`
	Tokens    int    `json:"tokens"`
	Truncated bool   `json:"truncated"`
	Path      string `json:"path"`
	Changed   bool   `json:"changed"`
}

func TestHandoffWriteEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Unix(1700000000, 0)
	projectID, _, err := st.ProjectIDForPath(ctx, "qoder", "demo", dir, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertMemory(ctx, projectID, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeConstraint,
		Text: "写接口必须带 token", SourceAgent: "qoder", SourcePath: "qoder",
	}, now); err != nil {
		t.Fatal(err)
	}
	handoffPath := filepath.Join(dir, surface.DirName, surface.FileName)

	rec := webDo(t, s, http.MethodPost, "/v1/handoff", webToken,
		map[string]any{"project_path": dir, "write": false}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("write=false: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var preview handoffWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Body, "写接口必须带 token") {
		t.Fatalf("preview body missing project memory: %q", preview.Body)
	}
	if preview.Path != "" {
		t.Fatalf("write=false must not report a path, got %q", preview.Path)
	}
	if _, err := os.Stat(handoffPath); !os.IsNotExist(err) {
		t.Fatalf("write=false must not touch disk: %v", err)
	}

	rec = webDo(t, s, http.MethodPost, "/v1/handoff", webToken,
		map[string]any{"project_path": dir, "write": true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("write=true: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var written handoffWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &written); err != nil {
		t.Fatal(err)
	}
	if written.Path != handoffPath || !written.Changed {
		t.Fatalf("write=true path = %q changed = %v, want %q true", written.Path, written.Changed, handoffPath)
	}
	fileBody, err := os.ReadFile(handoffPath)
	if err != nil {
		t.Fatalf("handoff file not written: %v", err)
	}
	if string(fileBody) != written.Body {
		t.Fatalf("file body differs from response body")
	}

	rec = webDo(t, s, http.MethodPost, "/v1/handoff", webToken,
		map[string]any{"project_path": dir, "write": true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("second write: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var again handoffWriteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &again); err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatal("unchanged body should report changed=false")
	}
}

func TestHandoffWriteEndpointUnknownProject(t *testing.T) {
	s, _ := newTestServer(t)
	rec := webDo(t, s, http.MethodPost, "/v1/handoff", webToken,
		map[string]any{"project_path": filepath.Join(t.TempDir(), "missing"), "write": true}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d body = %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestHandoffWriteEndpointRefusesAgentNativeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	s, _ := newTestServer(t)
	protected := filepath.Join(home, ".qoder-cn", "workspace")
	rec := webDo(t, s, http.MethodPost, "/v1/handoff", webToken,
		map[string]any{"project_path": protected, "write": true}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d body = %s, want 400", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "agent 原生目录") {
		t.Fatalf("400 body should explain the protected dir: %s", rec.Body.String())
	}
}

func TestStaleEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	id, _, err := st.UpsertMemory(ctx, 0, domain.MemoryDraft{
		Scope: domain.ScopeUser, Type: domain.TypeFact, Text: "可被手动标记过时的条目",
		SourceAgent: "cli", SourcePath: "cli",
	}, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodPost, "/v1/memory/stale", webToken,
		map[string]any{"id": id, "stale": true}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mark stale: code = %d body = %s", rec.Code, rec.Body.String())
	}
	var marked struct {
		ID    int64 `json:"id"`
		Stale bool  `json:"stale"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &marked); err != nil {
		t.Fatal(err)
	}
	if marked.ID != id || !marked.Stale {
		t.Fatalf("response = %+v, want id=%d stale=true", marked, id)
	}
	if n, err := st.QueryInt(ctx, `SELECT stale FROM memory_item WHERE id = ?`, id); err != nil || n != 1 {
		t.Fatalf("stale flag = %d err = %v, want 1", n, err)
	}

	rec = webDo(t, s, http.MethodPost, "/v1/memory/stale", webToken,
		map[string]any{"id": id, "stale": false}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revive: code = %d body = %s", rec.Code, rec.Body.String())
	}
	if n, err := st.QueryInt(ctx, `SELECT stale FROM memory_item WHERE id = ?`, id); err != nil || n != 0 {
		t.Fatalf("stale flag = %d err = %v, want 0", n, err)
	}

	rec = webDo(t, s, http.MethodPost, "/v1/memory/stale", webToken,
		map[string]any{"id": id + 999, "stale": true}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: code = %d body = %s, want 404", rec.Code, rec.Body.String())
	}
}

func TestWriteEndpointsRejectForeignOrigin(t *testing.T) {
	var calls int32
	s, _ := newTestServer(t, func(cfg *Config) {
		cfg.Scan = func(context.Context) (map[string]any, error) {
			atomic.AddInt32(&calls, 1)
			return map[string]any{}, nil
		}
	})
	dir := t.TempDir()
	foreign := func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }

	cases := []struct {
		path string
		body any
	}{
		{"/v1/scan", nil},
		{"/v1/handoff", map[string]any{"project_path": dir, "write": true}},
		{"/v1/memory/stale", map[string]any{"id": 1, "stale": true}},
	}
	for _, tc := range cases {
		rec := webDo(t, s, http.MethodPost, tc.path, webToken, tc.body, foreign)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s with foreign origin: code = %d body = %s, want 403", tc.path, rec.Code, rec.Body.String())
		}
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("foreign origin must not trigger scan, calls = %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, surface.DirName)); !os.IsNotExist(err) {
		t.Fatalf("foreign origin must not write handoff: %v", err)
	}
}

func TestWriteEndpointsAllowLoopbackOrigin(t *testing.T) {
	var calls int32
	s, _ := newTestServer(t, func(cfg *Config) {
		cfg.Scan = func(context.Context) (map[string]any, error) {
			atomic.AddInt32(&calls, 1)
			return map[string]any{}, nil
		}
	})
	origin := func(host, scheme string) func(*http.Request) {
		return func(r *http.Request) {
			r.Host = host
			r.Header.Set("Origin", scheme+"://"+host)
		}
	}

	rec := webDo(t, s, http.MethodPost, "/v1/scan", webToken, nil, origin("127.0.0.1:47123", "http"))
	if rec.Code != http.StatusOK {
		t.Fatalf("127.0.0.1 origin: code = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodPost, "/v1/scan", webToken, nil, origin("localhost:47123", "http"))
	if rec.Code != http.StatusOK {
		t.Fatalf("localhost origin: code = %d body = %s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodPost, "/v1/scan", webToken, nil, func(r *http.Request) {
		r.Host = "127.0.0.1:47123"
		r.Header.Set("Origin", "http://127.0.0.1:9999")
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("port mismatch: code = %d, want 403", rec.Code)
	}
	rec = webDo(t, s, http.MethodPost, "/v1/scan", webToken, nil, origin("127.0.0.1:47123", "https"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("https origin: code = %d, want 403", rec.Code)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("allowed origins should trigger exactly 2 scans, got %d", got)
	}
}
