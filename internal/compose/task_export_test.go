package compose

import (
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestHandoffBriefIsShort(t *testing.T) {
	task := domain.Task{
		ID: 7, Goal: "给扣减加乐观锁", Status: domain.StatusBlocked,
		NextSteps: "先复现超卖", Files: []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go"},
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone},
			{Subject: "补测试", Status: domain.StepBlocked},
		},
	}
	project := domain.Project{DisplayName: "demo", Key: "f:/demo"}
	md := RenderTaskHandoffBrief(task, project)
	lines := strings.Split(strings.TrimSpace(md), "\n")
	if len(lines) > 30 {
		t.Fatalf("brief must be <=30 lines, got %d:\n%s", len(lines), md)
	}
	for _, want := range []string{"给扣减加乐观锁", "补测试", "task_get", "task_update"} {
		if !strings.Contains(md, want) {
			t.Fatalf("brief missing %q", want)
		}
	}
	if strings.Contains(md, "i.go") {
		t.Fatal("brief should cap files at 8")
	}
	if strings.Contains(md, "写接口") {
		t.Fatal("brief should only list unfinished steps")
	}
}
