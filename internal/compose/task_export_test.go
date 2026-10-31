package compose

import (
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestRenderTaskExport(t *testing.T) {
	task := domain.Task{
		ID: 42, Goal: "实现导出", Status: domain.StatusDoing, OwnerAgent: "qoder",
		NextSteps: "补测试", Files: []string{"app/Export.php"},
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone},
			{Subject: "补测试", Status: domain.StepDoing},
		},
	}
	snippets := []domain.TaskSnippet{{Role: "assistant", Kind: "assistant_text", Text: "测试通过", Ts: time.Now()}}
	project := domain.Project{Key: "e:/demo", DisplayName: "demo"}
	got := RenderTaskExport(task, snippets, project)
	for _, want := range []string{
		"task_id: 42", "实现导出", "[x] 写接口", "[~] 补测试",
		"补测试", "app/Export.php", "task_get", "task_update", "不要新建任务",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("export missing %q:\n%s", want, got)
		}
	}
}
