package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
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

func TestDataBackupExportAndImport(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Now()
	pid, err := st.EnsureProject(ctx, "f:/export-demo", "export-demo", "", `F:\export-demo`, now)
	if err != nil {
		t.Fatal(err)
	}
	memID, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeConstraint,
		Text: "导出时要保留约束原文", SourceAgent: "test", SourcePath: "test",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSummaryPending(ctx, "memory", memID, "h1", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NextSummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSummary(ctx, "memory", memID, "h1", "导出摘要", "fake", now); err != nil {
		t.Fatal(err)
	}
	taskID, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "导出任务", Status: domain.StatusDoing, NextSteps: "写导出",
		CreatedAt: now, UpdatedAt: now,
		Steps: []domain.TaskStep{{Subject: "写导出", Status: domain.StepDoing}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// 对话任务是「目标机扫一遍就能重建」的东西，不该出现在导出里。
	if _, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Kind: domain.TaskKindSession, SessionID: 4242, Goal: "对话任务不进导出",
		Status: domain.StatusDoing, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	source, _ := st.TaskSummarySource(ctx, taskID)
	if err := st.MarkSummaryPending(ctx, "task", taskID, source, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NextSummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSummary(ctx, "task", taskID, source, "任务导出摘要", "fake", now); err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodPost, "/v1/data/backup", webToken, map[string]any{}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("backup code=%d body=%s", rec.Code, rec.Body.String())
	}
	var backup struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &backup); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(backup.Path); err != nil || st.Size() == 0 || backup.Size == 0 {
		t.Fatalf("backup file missing: %+v err=%v", backup, err)
	}

	rec = webDo(t, s, http.MethodGet, "/v1/data/export", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("export code=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), webToken) {
		t.Fatal("export must not contain the access token")
	}
	var doc store.DataExport
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Projects) != 1 || len(doc.Memories) != 1 || len(doc.Tasks) != 1 {
		t.Fatalf("export = %+v", doc)
	}
	if strings.Contains(rec.Body.String(), "对话任务不进导出") {
		t.Fatal("导出应只含合并任务，对话任务由目标机扫描重建")
	}
	if doc.Memories[0].Summary != "导出摘要" || doc.Tasks[0].Summary != "任务导出摘要" || len(doc.Tasks[0].Steps) != 1 {
		t.Fatalf("summary/steps missing: %+v / %+v", doc.Memories[0], doc.Tasks[0])
	}

	newDoc := store.DataExport{
		FormatVersion: 1,
		GeneratedAt:   now.UTC().Format(time.RFC3339),
		Projects:      []store.ProjectExport{{Key: "f:/import-demo", DisplayName: "import-demo", PrimaryPath: `F:\import-demo`}},
		Memories:      []store.MemoryExport{{Scope: "project", ProjectKey: "f:/import-demo", Type: "fact", Text: "导入进来的事实", Summary: "导入摘要"}},
		Tasks:         []store.TaskExport{{ProjectKey: "f:/import-demo", Goal: "导入任务", Status: "doing", OwnerAgent: "claude", NextSteps: "继续做", Summary: "导入任务摘要", Steps: []store.StepExport{{Subject: "继续做", Status: "doing"}}}},
	}
	raw, _ := json.Marshal(newDoc)

	importToken := uploadImport(t, s, raw)
	rec = webDo(t, s, http.MethodPost, "/v1/data/import/apply", webToken, map[string]any{"import_token": importToken}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply code=%d body=%s", rec.Code, rec.Body.String())
	}
	var applied store.ImportPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Projects != 1 || applied.Memories != 1 || applied.Tasks != 1 || len(applied.Errors) != 0 {
		t.Fatalf("applied = %+v", applied)
	}
	pid2, ok, err := st.ProjectByKey(ctx, "f:/import-demo")
	if err != nil || !ok {
		t.Fatalf("imported project ok=%v err=%v", ok, err)
	}
	memories, err := st.ListMemories(ctx, pid2.ID, false, 10)
	if err != nil || len(memories) != 1 || memories[0].Summary != "导入摘要" || memories[0].SummaryState != "ready" {
		t.Fatalf("memories=%+v err=%v", memories, err)
	}
	tasks, err := st.ListTasks(ctx, store.TaskFilter{ProjectID: pid2.ID, Limit: 10})
	if err != nil || len(tasks) != 1 || tasks[0].Goal != "导入任务" || tasks[0].Summary != "导入任务摘要" {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
}

func uploadImport(t *testing.T, s *Server, raw []byte) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "export.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/data/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+webToken)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Token string `json:"import_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Token == "" {
		t.Fatal("missing import token")
	}
	return out.Token
}

func TestImportRejectsBadTokenAndFormat(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()

	rec := webDo(t, s, http.MethodPost, "/v1/data/import/apply", webToken, map[string]any{"import_token": "../evil"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("path traversal token code=%d body=%s", rec.Code, rec.Body.String())
	}

	bad, _ := json.Marshal(map[string]any{"format_version": 2})
	if _, err := store.PreviewImport(bad); err == nil {
		t.Fatal("format_version 2 should be rejected")
	}
	before, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Memories != 0 || before.Projects != 0 {
		t.Fatalf("bad import must not write: %+v", before)
	}
}

func TestImportApplyExpiredToken(t *testing.T) {
	s, _ := newTestServer(t)
	dir := filepath.Join(s.cfg.DataDir, "imports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 32)
	path := filepath.Join(dir, token+".json")
	if err := os.WriteFile(path, []byte(`{"format_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	rec := webDo(t, s, http.MethodPost, "/v1/data/import/apply", webToken, map[string]any{"import_token": token}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired token code=%d body=%s", rec.Code, rec.Body.String())
	}
}
