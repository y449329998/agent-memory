package daemon

import (
	"context"
	"testing"

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
