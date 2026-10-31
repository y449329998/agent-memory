package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
