package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := New(Config{Token: "test-token-0123456789abcdef", DataDir: t.TempDir()}, st)
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
