package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestUpdateMemoryEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := st.EnsureProject(ctx, "f:/api-upd", "api-upd", "", `F:\api-upd`, now)
	id, _, _ := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "原内容",
		SourceAgent: "qoder", SourcePath: "/a.md",
	}, now)

	// 跨站 Origin 必须拒绝；同源（Host 与 Origin 端口一致）放行。
	rec := webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": id, "text": "新内容"},
		func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") })
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: code = %d, want 403", rec.Code)
	}

	sameOrigin := func(r *http.Request) {
		r.Host = "127.0.0.1:47123"
		r.Header.Set("Origin", "http://127.0.0.1:47123")
	}
	rec = webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": id, "text": "新内容", "type": "constraint"}, sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: code = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID         int64 `json:"id"`
		MergedInto int64 `json:"merged_into"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != id || out.MergedInto != 0 {
		t.Fatalf("response = %+v", out)
	}
	m, _ := st.MemoryByID(ctx, id)
	if m.Text != "新内容" || m.Type != domain.TypeConstraint {
		t.Fatalf("stored = %+v", m)
	}

	rec = webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": id, "text": "  "}, sameOrigin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty text: code = %d, want 400", rec.Code)
	}
	rec = webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{"id": 99999, "text": "x"}, sameOrigin)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id: code = %d, want 404", rec.Code)
	}
}
