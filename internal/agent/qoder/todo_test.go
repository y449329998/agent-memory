package qoder

import (
	"strconv"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

func createLine(callID, subject, activeForm string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + callID + `","name":"TaskCreate","input":{"subject":"` + subject + `","activeForm":"` + activeForm + `"}}]}}`
}

func createdResultLine(callID, id, subject string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + callID + `","content":"Task #` + id + ` created successfully: ` + subject + `"}]}}`
}

func updateLine(id, status string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-u` + id + `","name":"TaskUpdate","input":{"taskId":"` + id + `","status":"` + status + `"}}]}}`
}

func TestParseTodosMergesCreatesAndUpdates(t *testing.T) {
	content := strings.Join([]string{
		createLine("call-a", "写接口", "正在写接口"),
		createdResultLine("call-a", "1", "写接口"),
		createLine("call-b", "补测试", ""),
		createdResultLine("call-b", "2", "补测试"),
		updateLine("1", "in_progress"),
		updateLine("2", "in_progress"),
		updateLine("1", "completed"),
	}, "\n")
	steps := parseTodos([]byte(content))
	if len(steps) != 2 {
		t.Fatalf("want 2 steps got %d: %+v", len(steps), steps)
	}
	byKey := map[string]agent.TaskStepDraft{}
	for _, s := range steps {
		byKey[s.SourceKey] = s
	}
	if byKey["qt:1"].Status != domain.StepDone || byKey["qt:1"].Subject != "写接口" {
		t.Fatalf("status-only update must keep subject and take last status: %+v", byKey["qt:1"])
	}
	if byKey["qt:1"].ActiveForm != "正在写接口" {
		t.Fatalf("activeForm lost: %+v", byKey["qt:1"])
	}
	if byKey["qt:2"].Status != domain.StepDoing {
		t.Fatalf("step2 = %+v", byKey["qt:2"])
	}
	if steps[0].SourceKey != "qt:1" || steps[1].SourceKey != "qt:2" {
		t.Fatalf("steps should keep create order: %+v", steps)
	}
}

func TestParseTodosSkipsUncreatedAndBad(t *testing.T) {
	content := strings.Join([]string{
		createLine("call-x", "没有结果的创建", ""),
		updateLine("9", "completed"), // 没有 create 的更新，丢弃
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"TaskCreate","input":{ broken`,
	}, "\n")
	if steps := parseTodos([]byte(content)); len(steps) != 0 {
		t.Fatalf("want 0 got %+v", steps)
	}
}

func TestParseTodosUpdateBeforeResult(t *testing.T) {
	// 结果行缺失（会话被截断）但 update 带 subject：仍能建步骤
	content := strings.Join([]string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-z","name":"TaskUpdate","input":{"taskId":"5","status":"in_progress","subject":"补丁任务"}}]}}`,
	}, "\n")
	steps := parseTodos([]byte(content))
	if len(steps) != 1 || steps[0].SourceKey != "qt:5" || steps[0].Status != domain.StepDoing {
		t.Fatalf("update-only fallback failed: %+v", steps)
	}
}

func TestParseTodosNoTodoReturnsNil(t *testing.T) {
	if steps := parseTodos([]byte(`{"type":"user","message":{"role":"user","content":"hi"}}`)); steps != nil {
		t.Fatalf("want nil got %+v", steps)
	}
}

func TestParseTodosCapsAt100(t *testing.T) {
	var lines []string
	for i := 1; i <= 150; i++ {
		id := strconv.Itoa(i)
		lines = append(lines, createLine("call-"+id, "步骤"+id, ""))
		lines = append(lines, createdResultLine("call-"+id, id, "步骤"+id))
	}
	steps := parseTodos([]byte(strings.Join(lines, "\n")))
	if len(steps) != 100 {
		t.Fatalf("want cap 100 got %d", len(steps))
	}
	if steps[0].SourceKey != "qt:51" || steps[99].SourceKey != "qt:150" {
		t.Fatalf("cap should keep the latest 100: %s..%s", steps[0].SourceKey, steps[99].SourceKey)
	}
}
