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
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func newTestServer(t *testing.T, mutate ...func(*Config)) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := Config{Token: "test-token-0123456789abcdef", DataDir: t.TempDir()}
	for _, m := range mutate {
		m(&cfg)
	}
	s := New(cfg, st)
	return s, st
}

func doJSON(t *testing.T, s *Server, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var payload bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&payload).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &payload)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestHealthzNeedsNoToken(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/v1/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestAuthRequiredAndTimingSafe(t *testing.T) {
	s, _ := newTestServer(t)
	for _, tok := range []string{"", "wrong", "test-token-0123456789abcde"} {
		rec := doJSON(t, s, http.MethodGet, "/v1/stats", tok, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q should be rejected, got %d", tok, rec.Code)
		}
	}
	rec := doJSON(t, s, http.MethodGet, "/v1/stats", "test-token-0123456789abcdef", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token rejected: %d", rec.Code)
	}
}

func TestSearchEndpointUsesSharedStore(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	_, _, err := st.UpsertMemory(ctx, 0, domain.MemoryDraft{
		Scope: domain.ScopeUser, Type: domain.TypePreference, Text: "用户偏好：沟通用中文",
		SourceAgent: "cli", SourcePath: "cli",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/v1/search", "test-token-0123456789abcdef",
		map[string]any{"query": "偏好", "limit": 5})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Hits []struct {
			Text   string   `json:"text"`
			Agents []string `json:"agents"`
			Score  float64  `json:"score"`
			Origin string   `json:"origin"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || !strings.Contains(out.Hits[0].Text, "沟通用中文") {
		t.Fatalf("hits = %+v", out.Hits)
	}
	if out.Hits[0].Origin != "fts" || len(out.Hits[0].Agents) != 1 || out.Hits[0].Agents[0] != "cli" {
		t.Fatalf("hit metadata wrong: %+v", out.Hits[0])
	}
}

// TestSearchEndpointResolvesProjectKey 回归：只传 project_key 时曾被当成空引用，
// 项目级记忆一条都检索不到（随后还可能退化成全库检索）。
func TestSearchEndpointResolvesProjectKey(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	projectID, err := st.EnsureProject(ctx, "f:/go/demo", "demo", "git@github.com:foo/demo.git", "F:/go/demo", now)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = st.UpsertMemory(ctx, projectID, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeConstraint,
		Text: "本项目专用的中文约束", SourceAgent: "cli", SourcePath: "cli",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/v1/search", "test-token-0123456789abcdef",
		map[string]any{"query": "专用", "project_key": "f:/go/demo", "limit": 5})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Hits []struct {
			Scope string `json:"scope"`
			Text  string `json:"text"`
		} `json:"hits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 1 || out.Hits[0].Scope != "project" || !strings.Contains(out.Hits[0].Text, "专用") {
		t.Fatalf("project_key 应能命中该项目级记忆, hits = %+v", out.Hits)
	}

	// 不带任何项目引用时只看用户级：这条项目级记忆不得漏出。
	rec = doJSON(t, s, http.MethodPost, "/v1/search", "test-token-0123456789abcdef",
		map[string]any{"query": "专用", "limit": 5})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	out.Hits = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Hits) != 0 {
		t.Fatalf("省略项目参数不应返回项目级记忆, hits = %+v", out.Hits)
	}
}

func TestAddEndpointValidatesInput(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": "  ", "type": "preference"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank text should be 400, got %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": "接口字段一律蛇形", "type": "vibes"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad type should be 400, got %d", rec.Code)
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": "接口字段一律蛇形", "type": "convention", "scope": "user"})
	if rec.Code != http.StatusOK {
		t.Fatalf("valid add should succeed, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAddEndpointRejectsEmptyAndUnknownFields(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef", nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "empty request body") {
		t.Fatalf("empty body: code = %d body = %q", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": "接口字段一律蛇形", "type": "fact", "scope": "user", "bogus": 1})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("unknown field: code = %d body = %q", rec.Code, rec.Body.String())
	}
}

func TestBodySizeLimit(t *testing.T) {
	s, _ := newTestServer(t)
	huge := strings.Repeat("x", maxBodyBytes+1)
	rec := doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{"text": huge, "type": "fact", "scope": "user"})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code = %d, want 413", rec.Code)
	}
}

func TestHandoffEndpointReturnsNotFoundForUnknownProject(t *testing.T) {
	s, _ := newTestServer(t)
	rec := doJSON(t, s, http.MethodGet, "/v1/handoff?project_path=Z%3A%5Cnope", "test-token-0123456789abcdef", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rec.Code)
	}
}

// writeGitRemote 在 dir 下造一个最小 .git/config，供 GitRemoteFromConfig 读取。
func writeGitRemote(t *testing.T, dir, url string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = " + url + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAddProjectMemoryReusesRemoteKeyedProject 回归：HTTP/MCP 写入曾直接用空 remote 调
// ProjectIDForPath，绕过扫描登记的路径映射，把同一个仓库拆成两个项目。
func TestAddProjectMemoryReusesRemoteKeyedProject(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "repo")
	writeGitRemote(t, dir, "git@github.com:foo/bar.git")

	// 模拟扫描：按 remote 建项目并登记路径映射。
	scanned, _, err := st.ProjectIDForPath(ctx, "qoder", "bar", dir, "git@github.com:foo/bar.git", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, http.MethodPost, "/v1/memory", "test-token-0123456789abcdef",
		map[string]any{
			"text": "外部写入应复用扫描出来的项目", "type": "convention",
			"scope": "project", "project_path": dir, "agent": "mcp",
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	n, err := st.QueryInt(ctx, `SELECT count(*) FROM project`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		keys, _ := st.QueryStrings(ctx, `SELECT key FROM project ORDER BY key`)
		t.Fatalf("project rows = %d %v, want 1", n, keys)
	}

	got, err := st.QueryInt(ctx, `SELECT project_id FROM memory_item WHERE id = ?`, out.ID)
	if err != nil {
		t.Fatal(err)
	}
	if int64(got) != scanned {
		t.Fatalf("memory project_id = %d, want scanned remote project %d", got, scanned)
	}
	p, ok, err := st.ProjectByKey(ctx, "github.com/foo/bar")
	if err != nil || !ok {
		t.Fatalf("remote-keyed project missing: ok=%v err=%v", ok, err)
	}
	if p.ID != scanned {
		t.Fatalf("remote project id = %d, want %d", p.ID, scanned)
	}
}
