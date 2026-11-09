package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type recordingAPI struct {
	lastSearch SearchInput
	lastAdd    AddInput
	searchOut  any
	err        error
}

func (a *recordingAPI) Search(ctx context.Context, in SearchInput) (any, error) {
	a.lastSearch = in
	if a.err != nil {
		return nil, a.err
	}
	if a.searchOut != nil {
		return a.searchOut, nil
	}
	return map[string]any{"hits": []any{}, "count": 0}, nil
}
func (a *recordingAPI) Add(ctx context.Context, in AddInput) (any, error) {
	a.lastAdd = in
	return map[string]any{"id": 42, "created": true}, nil
}
func (a *recordingAPI) List(ctx context.Context, in ListInput) (any, error) {
	return map[string]any{"memories": []any{}, "count": 0}, nil
}
func (a *recordingAPI) Handoff(ctx context.Context, in HandoffInput) (any, error) {
	return map[string]any{"body": "# 简报"}, nil
}
func (a *recordingAPI) Tasks(ctx context.Context, in TaskInput) (any, error) {
	return map[string]any{"cards": []any{}, "count": 0}, nil
}
func (a *recordingAPI) TaskGet(ctx context.Context, id int64) (any, error) {
	return map[string]any{"id": id, "goal": "给扣减加乐观锁"}, nil
}
func (a *recordingAPI) AddTask(ctx context.Context, in AddTaskInput) (any, error) {
	return map[string]any{"id": 7}, nil
}
func (a *recordingAPI) UpdateTask(ctx context.Context, in UpdateTaskInput) (any, error) {
	return map[string]any{"id": in.ID}, nil
}
func (a *recordingAPI) SyncTask(ctx context.Context, in SyncTaskInput) (any, error) {
	return map[string]any{"task_id": 7, "settled": false}, nil
}
func (a *recordingAPI) ConversationSearch(ctx context.Context, in ConversationSearchInput) (any, error) {
	return map[string]any{"hits": []any{}, "count": 0}, nil
}
func (a *recordingAPI) ConversationGet(ctx context.Context, in ConversationGetInput) (any, error) {
	return map[string]any{"messages": []any{}, "next": 0}, nil
}
func (a *recordingAPI) ExportTask(ctx context.Context, id int64) (any, error) {
	return map[string]any{"markdown": "task_id: 7"}, nil
}
func (a *recordingAPI) ResolveProject(ctx context.Context, path string) (any, error) {
	return map[string]any{"known": true, "project_key": "f:/go/demo"}, nil
}
func (a *recordingAPI) Stats(ctx context.Context) (any, error) {
	return map[string]any{"memories": 3}, nil
}

func callWith(t *testing.T, api ToolAPI, tool string, args map[string]any) map[string]any {
	t.Helper()
	s := NewServer(api)
	params, _ := json.Marshal(map[string]any{"name": tool, "arguments": args})
	// json.RawMessage 而不是 []byte：后者在 map[string]any 里会被 base64 编码。
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": json.RawMessage(params)})
	resp, ok := s.Handle(context.Background(), raw)
	if !ok {
		t.Fatal("no response")
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func firstText(t *testing.T, out map[string]any) string {
	t.Helper()
	if out["error"] != nil {
		t.Fatalf("jsonrpc error: %v", out["error"])
	}
	res := out["result"].(map[string]any)
	return res["content"].([]any)[0].(map[string]any)["text"].(string)
}

func TestMemorySearchPassesArgumentsThrough(t *testing.T) {
	api := &recordingAPI{}
	callWith(t, api, "memory_search", map[string]any{
		"query": "偏好", "project_path": `F:\go\demo`, "type": "preference", "limit": 3,
	})
	if api.lastSearch.Query != "偏好" || api.lastSearch.ProjectPath != `F:\go\demo` ||
		api.lastSearch.Type != "preference" || api.lastSearch.Limit != 3 {
		t.Fatalf("args not forwarded: %+v", api.lastSearch)
	}
}

func TestToolErrorBecomesResultNotJSONRPCError(t *testing.T) {
	api := &recordingAPI{err: context.DeadlineExceeded}
	out := callWith(t, api, "memory_search", map[string]any{"query": "x"})
	if out["error"] != nil {
		t.Fatalf("backend failure must not be a jsonrpc error: %v", out["error"])
	}
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatal("want isError=true so the agent can see and retry")
	}
	if !strings.Contains(firstText(t, out), "deadline") {
		t.Fatalf("error text = %q", firstText(t, out))
	}
}

func TestUnknownArgumentIsToolError(t *testing.T) {
	api := &recordingAPI{}
	out := callWith(t, api, "memory_search", map[string]any{"query": "x", "no_such_field": 1})
	if out["result"].(map[string]any)["isError"] != true {
		t.Fatalf("want tool error for unknown argument: %v", out)
	}
}

func TestStatsNeedsNoArguments(t *testing.T) {
	api := &recordingAPI{}
	out := callWith(t, api, "stats", nil)
	if out["error"] != nil {
		t.Fatalf("error: %v", out["error"])
	}
	if !strings.Contains(firstText(t, out), "memories") {
		t.Fatalf("text = %q", firstText(t, out))
	}
}

func TestTaskGetRejectsNonPositiveID(t *testing.T) {
	api := &recordingAPI{}
	out := callWith(t, api, "task_get", map[string]any{"id": 0})
	if out["result"].(map[string]any)["isError"] != true {
		t.Fatalf("id=0 should be a tool error: %v", out)
	}
}

func TestTaskGetForwardsID(t *testing.T) {
	api := &recordingAPI{}
	out := callWith(t, api, "task_get", map[string]any{"id": 7})
	if out["result"].(map[string]any)["isError"] != false {
		t.Fatalf("task_get failed: %v", out)
	}
	if text := firstText(t, out); !strings.Contains(text, `"id": 7`) {
		t.Fatalf("id not forwarded, text = %q", text)
	}
}

func TestToolDescriptionsArePresentAndNonTrivial(t *testing.T) {
	s := NewServer(&recordingAPI{})
	tools := s.toolDefinitions()
	if len(tools) != 14 {
		t.Fatalf("want 14 tools, got %d", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if len([]rune(tool.Description)) < 10 {
			t.Fatalf("tool %s description too thin to guide an agent: %q", tool.Name, tool.Description)
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
	}
}
