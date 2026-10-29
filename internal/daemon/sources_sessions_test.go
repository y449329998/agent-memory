package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestSourcesEndpoint_ProjectFilter(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, _ := st.EnsureProject(ctx, "f:/src", "src", "", `F:\src`, now)
	if err := st.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/a.md", ContentHash: "ha"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/b.md", ContentHash: "hb"}, 2); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "来源测试",
		SourceAgent: "qoder", SourcePath: "/a.md"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSourceError(ctx, "qoder", "memory", "/a.md", "boom", 3); err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodGet, "/v1/sources?project_key=f%3A%2Fsrc&limit=50", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Sources []struct {
			Path      string `json:"Path"`
			LastError string `json:"LastError"`
		} `json:"sources"`
		Total  int `json:"total"`
		Failed int `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Sources) != 1 || out.Sources[0].Path != "/a.md" || out.Total != 1 || out.Failed != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out.Sources[0].LastError != "boom" {
		t.Fatalf("last error = %q", out.Sources[0].LastError)
	}
}
