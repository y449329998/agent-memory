package workbuddy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func buildSessionHome(t *testing.T, cwd string) (home, id string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), ".workbuddy")
	id = "11111111-2222-3333-4444-555555555555"
	lines := []string{
		fmt.Sprintf(`{"type":"ai-title","timestamp":1790000000000,"aiTitle":"修复订单统计","cwd":%q}`, cwd),
		`{"type":"session-meta","timestamp":1790000000000,"sessionId":"11111111-2222-3333-4444-555555555555"}`,
		`{"type":"message","role":"user","timestamp":1790000001000,"content":[{"type":"input_text","text":"<system-reminder data-role=\"user-context\">注入的技能说明</system-reminder>"}]}`,
		`{"type":"message","role":"user","timestamp":1790000002000,"content":[{"type":"input_text","text":"修复订单统计"}]}`,
		`{"type":"function_call","timestamp":1790000003000,"name":"Grep","arguments":"{\"pattern\":\"订单\",\"path\":\"app\"}"}`,
		`{"type":"function_call","timestamp":1790000004000,"name":"Bash","arguments":"{\"command\":\"go test ./...\"}"}`,
		`{"type":"function_call","timestamp":1790000005000,"name":"Edit","arguments":"{\"file_path\":\"app/Order.php\"}"}`,
		`{"type":"function_call_result","timestamp":1790000006000,"name":"Bash","status":"incomplete","output":{"type":"text","text":"command failed"}}`,
		`{"type":"message","role":"assistant","timestamp":1790000007000,"content":[{"type":"output_text","text":"已修复并测试通过"}]}`,
	}
	path := filepath.Join(home, "projects", "demo", id+".jsonl")
	writeFile(t, path, strings.Join(lines, "\n")+"\n")
	writeFile(t, filepath.Join(home, "tasks", id, "1.json"), `{"subject":"修复订单统计","status":"completed"}`)
	return home, id
}

func TestScanSessionsParsesWorkBuddyJSONL(t *testing.T) {
	cwd := t.TempDir()
	home, id := buildSessionHome(t, cwd)

	units, err := New(home).ScanSessionUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1: %+v", len(units), units)
	}
	u := units[0]
	if u.Err != nil {
		t.Fatalf("unexpected error: %v", u.Err)
	}
	if u.Content != nil {
		t.Fatalf("WorkBuddy sessions must stream, not keep content")
	}
	if u.Hash == "" {
		t.Fatal("hash not populated")
	}
	if u.Session.Agent != "workbuddy" || u.Session.ExternalID != id {
		t.Fatalf("session identity wrong: %+v", u.Session)
	}
	if u.Session.ProjectSlug != "demo" || u.Session.ProjectPath != cwd || u.Session.Confidence != "high" {
		t.Fatalf("project attribution wrong: %+v", u.Session)
	}
	if u.Session.Title != "修复订单统计" {
		t.Fatalf("title = %q, want ai-title", u.Session.Title)
	}
	if u.Session.StartedAt.IsZero() || u.Session.UpdatedAt.IsZero() {
		t.Fatalf("time range not populated: %+v", u.Session)
	}
	if u.Session.EventCount != len(u.Events) {
		t.Fatalf("event count = %d, events = %d", u.Session.EventCount, len(u.Events))
	}

	var sawUser, sawCommand, sawEdit, sawError, sawAssistant bool
	for _, e := range u.Events {
		switch e.Kind {
		case domain.KindUserText:
			if strings.Contains(e.Text, "system-reminder") {
				t.Fatalf("injected context leaked into goal: %q", e.Text)
			}
			if e.Text == "修复订单统计" {
				sawUser = true
			}
		case domain.KindToolCommand:
			sawCommand = e.Text == "go test ./..."
		case domain.KindToolEdit:
			sawEdit = len(e.Files) == 1 && e.Files[0] == "app/Order.php"
		case domain.KindToolError:
			sawError = e.Text == "command failed"
		case domain.KindAssistant:
			sawAssistant = e.Text == "已修复并测试通过"
		}
	}
	if !sawUser || !sawCommand || !sawEdit || !sawError || !sawAssistant {
		t.Fatalf("missing events: user=%v cmd=%v edit=%v err=%v assistant=%v (%+v)",
			sawUser, sawCommand, sawEdit, sawError, sawAssistant, u.Events)
	}
}

func TestScanSessionsCachesUnchangedFiles(t *testing.T) {
	cwd := t.TempDir()
	home, _ := buildSessionHome(t, cwd)
	a := New(home)

	first, err := a.ScanSessionUnits(context.Background())
	if err != nil || len(first) != 1 {
		t.Fatalf("first scan: %v %+v", err, first)
	}
	if len(first[0].Events) == 0 {
		t.Fatal("first scan should parse events")
	}

	second, err := a.ScanSessionUnits(context.Background())
	if err != nil || len(second) != 1 {
		t.Fatalf("second scan: %v %+v", err, second)
	}
	if second[0].Hash != first[0].Hash {
		t.Fatalf("hash changed: %q vs %q", first[0].Hash, second[0].Hash)
	}
	if len(second[0].Events) != len(first[0].Events) {
		t.Fatalf("cached events = %d, want %d", len(second[0].Events), len(first[0].Events))
	}
	if second[0].Session.Title != "修复订单统计" {
		t.Fatalf("cached session lost title: %+v", second[0].Session)
	}

	// 内容变化后必须重新解析并得到新哈希。
	path := first[0].Path
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","role":"assistant","timestamp":1790000008000,"content":[{"type":"output_text","text":"补充说明"}]}` + "\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	third, err := a.ScanSessionUnits(context.Background())
	if err != nil || len(third) != 1 {
		t.Fatalf("third scan: %v %+v", err, third)
	}
	if third[0].Hash == first[0].Hash {
		t.Fatal("hash did not change after append")
	}
	if len(third[0].Events) == 0 {
		t.Fatal("changed file should be parsed again")
	}
}

func TestMessageEventsCleansSkillPathAndImageRefs(t *testing.T) {
	raw := json.RawMessage(`[{"type":"input_text","text":"@skill:\"C:\\\\Users\\\\Admin\\\\.workbuddy\\\\skills\\\\brainstorming\\\\SKILL.md\" 修复订单统计 @image#1:Clipboard_Screenshot.png"}]`)
	events := messageEvents("user", time.Now(), raw)
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1: %+v", len(events), events)
	}
	if events[0].Text != "修复订单统计" {
		t.Fatalf("text = %q, want cleaned goal", events[0].Text)
	}
}

func TestMessageEventsDropsContinuationInstruction(t *testing.T) {
	raw := json.RawMessage(`[{"type":"input_text","text":"Please continue with the conversation based on the summarized context above. Maintain the same level of detail."}]`)
	if events := messageEvents("user", time.Now(), raw); len(events) != 0 {
		t.Fatalf("continuation instruction must be ignored: %+v", events)
	}
}

func TestScanSessionsUsesSummaryQueryAsGoal(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".workbuddy")
	id := "bbbbbbbb-0000-0000-0000-000000000001"
	cwd := t.TempDir()
	lines := []string{
		fmt.Sprintf(`{"type":"ai-title","timestamp":1790000000000,"aiTitle":"绩效方案调整","cwd":%q}`, cwd),
		`{"type":"message","role":"user","timestamp":1790000001000,"content":[{"type":"input_text","text":"<cb_summary>Summary of the conversation so far: <previous_user_message><user_query>看一下业务绩效相关的代码和最近修改记录</user_query></previous_user_message></cb_summary>"}]}`,
	}
	writeFile(t, filepath.Join(home, "projects", "demo", id+".jsonl"), strings.Join(lines, "\n")+"\n")
	mustMkdirAll(t, filepath.Join(home, "tasks", id))

	units, err := New(home).ScanSessionUnits(context.Background())
	if err != nil || len(units) != 1 {
		t.Fatalf("scan: %v %+v", err, units)
	}
	var goal string
	for _, e := range units[0].Events {
		if e.Kind == domain.KindUserText {
			goal = e.Text
		}
	}
	if goal != "看一下业务绩效相关的代码和最近修改记录" {
		t.Fatalf("goal = %q, want summary user_query", goal)
	}
}

func TestScanSessionsFallsBackToTitleWithoutUserText(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".workbuddy")
	id := "bbbbbbbb-0000-0000-0000-000000000002"
	cwd := t.TempDir()
	lines := []string{
		fmt.Sprintf(`{"type":"ai-title","timestamp":1790000000000,"aiTitle":"修复生产环境菜单问题","cwd":%q}`, cwd),
		`{"type":"message","role":"user","timestamp":1790000001000,"content":[{"type":"input_text","text":"<image_local_path>C:\\tmp\\a.png</image_local_path>"}]}`,
	}
	writeFile(t, filepath.Join(home, "projects", "demo", id+".jsonl"), strings.Join(lines, "\n")+"\n")
	mustMkdirAll(t, filepath.Join(home, "tasks", id))

	units, err := New(home).ScanSessionUnits(context.Background())
	if err != nil || len(units) != 1 {
		t.Fatalf("scan: %v %+v", err, units)
	}
	u := units[0]
	if u.Session.Title != "修复生产环境菜单问题" {
		t.Fatalf("title = %q", u.Session.Title)
	}
	if len(u.Events) != 1 || u.Events[0].Kind != domain.KindUserText || u.Events[0].Text != u.Session.Title {
		t.Fatalf("expected title fallback goal, got %+v", u.Events)
	}
}

func TestScanSessionsSkipsSessionsWithoutTasksOrCwd(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".workbuddy")
	live := t.TempDir()

	// 有任务清单但没有 cwd：不导入，避免建出无路径项目。
	noCwd := "aaaaaaaa-0000-0000-0000-000000000001"
	writeFile(t, filepath.Join(home, "projects", "demo", noCwd+".jsonl"),
		`{"type":"message","role":"user","timestamp":1790000000000,"content":[{"type":"input_text","text":"目标"}]}`+"\n")
	mustMkdirAll(t, filepath.Join(home, "tasks", noCwd))

	// cwd 指向已不存在的工作区：适配器照样返回，是否有历史路径映射由 ingest 判断。
	gone := "aaaaaaaa-0000-0000-0000-000000000002"
	goneCwd := filepath.Join(t.TempDir(), "gone")
	writeFile(t, filepath.Join(home, "projects", "demo", gone+".jsonl"),
		fmt.Sprintf(`{"type":"message","role":"user","timestamp":1790000000000,"cwd":%q,"content":[{"type":"input_text","text":"目标"}]}`, goneCwd)+"\n")
	mustMkdirAll(t, filepath.Join(home, "tasks", gone))

	// 没有任务清单的会话：不导入。
	noTasks := "aaaaaaaa-0000-0000-0000-000000000003"
	writeFile(t, filepath.Join(home, "projects", "demo", noTasks+".jsonl"),
		fmt.Sprintf(`{"type":"message","role":"user","timestamp":1790000000000,"cwd":%q,"content":[{"type":"input_text","text":"闲聊"}]}`, live)+"\n")

	units, err := New(home).ScanSessionUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d units, want only the gone-workspace session: %+v", len(units), units)
	}
	if units[0].Session.ExternalID != gone || units[0].Session.ProjectPath != goneCwd {
		t.Fatalf("unexpected unit: %+v", units[0].Session)
	}
}

func TestScanSessionsImportsTodoSteps(t *testing.T) {
	cwd := t.TempDir()
	home, id := buildSessionHome(t, cwd)
	writeFile(t, filepath.Join(home, "tasks", id, "2.json"),
		`{"subject":"补测试","description":"覆盖导出","activeForm":"正在补测试","status":"in_progress"}`)
	writeFile(t, filepath.Join(home, "tasks", id, "1.json"),
		`{"subject":"写接口","status":"completed"}`)

	units, err := New(home).ScanSessionUnits(context.Background())
	if err != nil || len(units) != 1 {
		t.Fatalf("scan: %v %+v", err, units)
	}
	steps := units[0].Steps
	if len(steps) != 2 {
		t.Fatalf("steps = %+v", steps)
	}
	if steps[0].SourceKey != "1" || steps[0].Subject != "写接口" || steps[0].Status != "done" {
		t.Fatalf("step 0 = %+v", steps[0])
	}
	if steps[1].SourceKey != "2" || steps[1].Status != "doing" {
		t.Fatalf("step 1 = %+v", steps[1])
	}
}
