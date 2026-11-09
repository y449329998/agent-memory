package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/mcp"
)

func TestMCPStepStatusDoesNotFreeze(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	if _, err := s.UpdateTask(context.Background(), mcp.UpdateTaskInput{ID: id, StepStatus: map[string]string{"qt:1": "done"}}); err != nil {
		t.Fatal(err)
	}
	tk, _, _ := st.TaskByID(context.Background(), id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("step_status must not freeze")
	}
	d, _, _ := st.TaskDetail(context.Background(), id)
	if d.Steps[0].Status != "done" {
		t.Fatalf("override not visible: %+v", d.Steps)
	}
}

func TestMCPStatusOnlyDoesNotFreeze(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	status := "blocked"
	if _, err := s.UpdateTask(context.Background(), mcp.UpdateTaskInput{ID: id, Status: &status}); err != nil {
		t.Fatal(err)
	}
	tk, _, _ := st.TaskByID(context.Background(), id)
	if !tk.ManualEditedAt.IsZero() {
		t.Fatal("status must not freeze")
	}
}

func TestMCPSyncTaskUnknownProject(t *testing.T) {
	s, _ := newTestServer(t)
	if _, err := s.SyncTask(context.Background(), mcp.SyncTaskInput{ProjectPath: t.TempDir()}); err == nil {
		t.Fatal("want error when project has no sessions")
	}
}

func TestMCPConversationSearchAndGet(t *testing.T) {
	s, st := newTestServer(t)
	id := seedSessionTask(t, st)
	ctx := context.Background()
	sid, err := st.MainSessionForTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceMessages(ctx, sid, []domain.SessionMessage{
		{SessionID: sid, Seq: 1, Role: "user", Kind: "text", Text: "给扣减加乐观锁", Ts: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.ConversationSearch(ctx, mcp.ConversationSearchInput{Query: "乐观锁"})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := out.(map[string]any); !ok || m["count"].(int) != 1 {
		t.Fatalf("search should hit once: %+v", out)
	}
	out, err = s.ConversationGet(ctx, mcp.ConversationGetInput{TaskID: id})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := out.(map[string]any); !ok || m["session_id"].(int64) != sid {
		t.Fatalf("get wrong session: %+v", out)
	}
}
