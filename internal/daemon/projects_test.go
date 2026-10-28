package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestProjectsEndpointCounts(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := st.EnsureProject(ctx, "f:/counted", "counted", "", `F:\counted`, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "一条", SourceAgent: "t", SourcePath: "/t.md"}, now); err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodGet, "/v1/projects", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
	var out struct {
		Projects []struct {
			Key          string `json:"key"`
			MemoryCount  int    `json:"memory_count"`
			StaleCount   int    `json:"stale_count"`
			LastMemoryAt string `json:"last_memory_at"`
			PathExists   bool   `json:"path_exists"`
		} `json:"projects"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || out.Projects[0].MemoryCount != 1 || out.Projects[0].StaleCount != 0 {
		t.Fatalf("%+v", out)
	}
	if out.Projects[0].LastMemoryAt == "" {
		t.Fatalf("last_memory_at missing: %+v", out.Projects[0])
	}
	// 测试里的项目路径是虚构的，必须报告为不存在，前端据此跳过自动选中。
	if out.Projects[0].PathExists {
		t.Fatalf("fictional path must not be reported as existing: %+v", out.Projects[0])
	}
}
