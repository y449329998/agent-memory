package qoder

import (
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

func todoLine(name, input string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"` + name + `","input":` + input + `}]}}`
}

func TestParseTodosMergesUpdates(t *testing.T) {
	content := strings.Join([]string{
		todoLine("TaskCreate", `{"id":"1","subject":"写接口","activeForm":"正在写接口"}`),
		todoLine("TaskCreate", `{"id":"2","subject":"补测试"}`),
		todoLine("TaskUpdate", `{"id":"1","status":"done"}`),
		todoLine("TaskUpdate", `{"id":"2","status":"doing"}`),
		todoLine("TaskUpdate", `{"id":"1","status":"blocked"}`),
	}, "\n")
	steps := parseTodos([]byte(content))
	if len(steps) != 2 {
		t.Fatalf("want 2 steps got %d: %+v", len(steps), steps)
	}
	byKey := map[string]agent.TaskStepDraft{}
	for _, s := range steps {
		byKey[s.SourceKey] = s
	}
	if byKey["qt:1"].Status != domain.StepBlocked || byKey["qt:1"].Subject != "写接口" {
		t.Fatalf("status-only update must keep subject and take last status: %+v", byKey["qt:1"])
	}
	if byKey["qt:1"].ActiveForm != "正在写接口" {
		t.Fatalf("activeForm lost: %+v", byKey["qt:1"])
	}
	if byKey["qt:2"].Status != domain.StepDoing {
		t.Fatalf("step2 = %+v", byKey["qt:2"])
	}
	if steps[0].SourceKey != "qt:1" {
		t.Fatalf("steps should keep create order: %+v", steps)
	}
}

func TestParseTodosSkipsBadAndEmpty(t *testing.T) {
	content := strings.Join([]string{
		todoLine("TaskCreate", `{"subject":"无 id 丢弃"}`),
		todoLine("TaskUpdate", `{"id":"9","status":"done"}`),
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"TaskCreate","input":{ broken`,
	}, "\n")
	if steps := parseTodos([]byte(content)); len(steps) != 0 {
		t.Fatalf("want 0 got %+v", steps)
	}
}

func TestParseTodosNoTodoReturnsNil(t *testing.T) {
	if steps := parseTodos([]byte(`{"type":"user","message":{"role":"user","content":"hi"}}`)); steps != nil {
		t.Fatalf("want nil got %+v", steps)
	}
}
