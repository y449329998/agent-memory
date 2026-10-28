package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/store"
)

func newMCPServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(Config{Token: "token-012345678901234567890123456789", BudgetTokens: 1500, RecentCards: 3}, st)
}

func rpc(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-012345678901234567890123456789")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMCPInitializeOverHTTP(t *testing.T) {
	s := newMCPServer(t)
	out := rpc(t, s, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}})
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != "2025-03-26" {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
}

func TestMCPNotificationReturns202(t *testing.T) {
	s := newMCPServer(t)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-012345678901234567890123456789")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202", rec.Code)
	}
}

func TestMCPToolsCallWritesIntoSharedStore(t *testing.T) {
	s := newMCPServer(t)
	out := rpc(t, s, "tools/call", map[string]any{
		"name":      "memory_add",
		"arguments": map[string]any{"text": "共享库写入的记忆应能被中文检索命中", "type": "convention", "scope": "user"},
	})
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("add failed: %v", res)
	}

	out = rpc(t, s, "tools/call", map[string]any{
		"name": "memory_search", "arguments": map[string]any{"query": "检索"},
	})
	text := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "共享库写入的记忆") {
		t.Fatalf("search should see the just-added memory:\n%s", text)
	}
}

func TestMCPRequiresAuth(t *testing.T) {
	s := newMCPServer(t)
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}
