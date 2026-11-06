package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/mcp"
)

func TestTaskListKindAndSortParams(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Now()
	dir := t.TempDir()
	pid, err := st.EnsureProject(ctx, "f:/kind-demo", "kind-demo", "", dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Kind: domain.TaskKindSession, SessionID: 1, Goal: "对话里的活",
		CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
		Steps: []domain.TaskStep{{Subject: "半小时前补的步骤", Status: domain.StepDone, CreatedAt: now, UpdatedAt: now.Add(-30 * time.Minute)}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "整理好的活", CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		query string
		want  string
		deny  string
	}{
		{"kind=session", "对话里的活", "整理好的活"},
		{"kind=formal", "整理好的活", "对话里的活"},
		{"kind=%E4%B9%B1%E7%A0%81", "对话里的活", ""},
		{"", "对话里的活", ""},
	}
	for _, c := range cases {
		rec := webDo(t, s, http.MethodGet, "/v1/tasks?"+c.query, webToken, nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code=%d body=%s", c.query, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, c.want) {
			t.Fatalf("%s: 缺少 %q，body=%s", c.query, c.want, body)
		}
		if c.deny != "" && strings.Contains(body, c.deny) {
			t.Fatalf("%s: 不该出现 %q，body=%s", c.query, c.deny, body)
		}
	}

	// 默认按「最近有内容」：对话任务虽然本体两小时没动，仍是第一条；
	// sort=created 才让合并任务排前面。
	rec := webDo(t, s, http.MethodGet, "/v1/tasks", webToken, nil, nil)
	var listed struct {
		Tasks []struct {
			Goal string
			Kind string
		} `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 2 || listed.Tasks[0].Goal != "对话里的活" || listed.Tasks[0].Kind != "session" {
		t.Fatalf("默认排序 = %+v", listed.Tasks)
	}
	rec = webDo(t, s, http.MethodGet, "/v1/tasks?sort=created", webToken, nil, nil)
	listed.Tasks = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Tasks) != 2 || listed.Tasks[0].Goal != "整理好的活" {
		t.Fatalf("sort=created 排序 = %+v", listed.Tasks)
	}
}

// TestMCPTaskListKindFilter 覆盖 agent 侧 task_list 的 kind 语义。
func TestMCPTaskListKindFilter(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Now()
	dir := t.TempDir()
	pid, err := st.EnsureProject(ctx, "f:/mcp-kind", "mcp-kind", "", dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Kind: domain.TaskKindSession, SessionID: 5, Goal: "MCP 对话任务",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "MCP 合并任务", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		kind string
		want int
	}{{domain.TaskKindSession, 1}, {domain.TaskKindFormal, 1}, {"", 2}} {
		out, err := s.Tasks(ctx, mcp.TaskInput{ProjectPath: "f:/mcp-kind", Kind: c.kind, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		tasks, ok := out.(map[string]any)["tasks"].([]domain.Task)
		if !ok || len(tasks) != c.want {
			t.Fatalf("kind=%q 期望 %d 条，得到 %+v", c.kind, c.want, tasks)
		}
	}
}

func TestTaskCreateUpdateAndExport(t *testing.T) {
	s, st := newTestServer(t)
	now := time.Now()
	dir := t.TempDir()
	if _, err := st.EnsureProject(context.Background(), "f:/task-demo", "task-demo", "", dir, now); err != nil {
		t.Fatal(err)
	}
	rec := webDo(t, s, http.MethodPost, "/v1/tasks",
		webToken,
		map[string]any{"project_path": "f:/task-demo", "goal": "实现导出", "steps": []map[string]any{{"subject": "写接口"}}},
		nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := int64(created["id"].(float64))
	rec = webDo(t, s, http.MethodGet, "/v1/tasks?project_key=f%3A%2Ftask-demo", webToken, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "实现导出") {
		t.Fatalf("list code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodPost, "/v1/tasks/update", webToken,
		map[string]any{"id": id, "status": "done", "owner_agent": "claude"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodGet, "/v1/tasks/export?id="+fmt.Sprint(id), webToken, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "task_id") {
		t.Fatalf("export code=%d body=%s", rec.Code, rec.Body.String())
	}
}
