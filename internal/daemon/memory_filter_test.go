package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestMemoryListFilters(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	mk := func(scope domain.Scope, pid int64, typ domain.MemoryType, text string) int64 {
		t.Helper()
		id, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: scope, Type: typ, Text: text, SourceAgent: "test", SourcePath: "/t.md"}, now)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk(domain.ScopeUser, 0, domain.TypePreference, "用户偏好")
	staleID := mk(domain.ScopeProject, 3, domain.TypeFact, "项目事实")
	if err := st.SetStaleByID(ctx, staleID, true); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		url  string
		want int
	}{
		{"/v1/memory?all=1", 2},
		{"/v1/memory?all=1&stale=1", 1},
		{"/v1/memory?all=1&stale=0", 1},
		{"/v1/memory?all=1&type=preference", 1},
		{"/v1/memory", 1},
	}
	for _, c := range cases {
		rec := webDo(t, s, http.MethodGet, c.url, webToken, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s -> %d body=%s", c.url, rec.Code, rec.Body.String())
		}
		var out struct {
			Count int `json:"count"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Count != c.want || out.Total != c.want {
			t.Fatalf("%s count=%d total=%d want %d", c.url, out.Count, out.Total, c.want)
		}
	}
}
