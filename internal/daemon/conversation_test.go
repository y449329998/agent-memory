package daemon

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// seedSessionTask 建一个带稳定 key 步骤、源文件不存在的对话任务。
func seedSessionTask(t *testing.T, st *store.Store) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	pid, err := st.EnsureProject(ctx, "f:/conv", "conv", "", `F:\conv`, now)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := st.UpsertSession(ctx, domain.Session{Agent: "qoder", ExternalID: "conv1", ProjectID: pid, Path: `C:\nonexistent\conv1.jsonl`, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateTask(ctx, domain.Task{
		ProjectID: pid, Kind: domain.TaskKindSession, SessionID: sid, Goal: "g",
		SourcePath: `C:\nonexistent\conv1.jsonl`, CreatedAt: now, UpdatedAt: now,
		Steps: []domain.TaskStep{{Subject: "s", SourceKey: "qt:1", Status: domain.StepPending, CreatedAt: now, UpdatedAt: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkTaskSession(ctx, id, sid, "source", now); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConversationStoreOnlyFallback(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	rec := webDo(t, s, http.MethodGet, "/v1/task/conversation?task_id="+itoa(id)+"&before=1", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "store_only") {
		t.Fatalf("missing source file should fall back to store_only: %s", rec.Body.String())
	}
}

func TestConversationFirstPageFromStore(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	rec := webDo(t, s, http.MethodGet, "/v1/task/conversation?task_id="+itoa(id), webToken, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"source":"store"`) {
		t.Fatalf("first page should come from store: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestStepStatusOverrideDoesNotFreeze(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	sameOrigin := func(r *http.Request) {
		r.Host = "127.0.0.1:47123"
		r.Header.Set("Origin", "http://127.0.0.1:47123")
	}
	rec := webDo(t, s, http.MethodPost, "/v1/tasks/step/status", webToken,
		map[string]any{"task_id": id, "source_key": "qt:1", "status": "done"}, sameOrigin)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	tk, _, _ := st.TaskByID(context.Background(), id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("step status override must not freeze")
	}
	d, _, _ := st.TaskDetail(context.Background(), id)
	if d.Steps[0].Status != domain.StepDone {
		t.Fatalf("override should be visible in read view: %+v", d.Steps)
	}
}

func TestTaskFilesEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	rec := webDo(t, s, http.MethodGet, "/v1/task/files?task_id="+itoa(id), webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
}
