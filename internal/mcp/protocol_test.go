package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func call(t *testing.T, s *Server, id any, method string, params map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := s.Handle(context.Background(), raw)
	if !ok {
		t.Fatal("expected a response")
	}
	var out map[string]any
	if err := json.Unmarshal(resp, &out); err != nil {
		t.Fatalf("bad response json: %v\n%s", err, resp)
	}
	return out
}

func TestInitializeEchoesClientProtocolVersion(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 1, "initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "0"},
	})
	res, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion = %v", res["protocolVersion"])
	}
	if res["serverInfo"] == nil {
		t.Fatal("serverInfo missing")
	}
	caps := res["capabilities"].(map[string]any)
	if caps["tools"] == nil {
		t.Fatal("tools capability missing")
	}
}

func TestInitializeFallsBackWhenClientHasNoVersion(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 2, "initialize", map[string]any{"capabilities": map[string]any{}})
	res := out["result"].(map[string]any)
	if res["protocolVersion"] != LatestProtocolVersion {
		t.Fatalf("fallback version = %v", res["protocolVersion"])
	}
}

func TestNotificationGetsNoResponse(t *testing.T) {
	s := NewServer(stubAPI{})
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	_, ok := s.Handle(context.Background(), raw)
	if ok {
		t.Fatal("notifications must not be answered")
	}
}

func TestToolsListShape(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 3, "tools/list", nil)
	res := out["result"].(map[string]any)
	tools := res["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools advertised")
	}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		if tool["name"] == nil || tool["description"] == nil || tool["inputSchema"] == nil {
			t.Fatalf("tool missing required fields: %v", tool)
		}
		schema := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Fatalf("inputSchema.type = %v", schema["type"])
		}
	}
}

func TestToolsCallReturnsTextContent(t *testing.T) {
	s := NewServer(stubAPI{searched: []string{"沟通用中文"}})
	out := call(t, s, 4, "tools/call", map[string]any{
		"name":      "memory_search",
		"arguments": map[string]any{"query": "偏好"},
	})
	res := out["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("tool call errored: %v", res)
	}
	block := res["content"].([]any)[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("content type = %v", block["type"])
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(block["text"].(string)), &payload); err != nil {
		t.Fatalf("tool text should be json: %v", err)
	}
}

func TestUnknownToolIsAPromptErrorNotCrash(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 5, "tools/call", map[string]any{"name": "nope", "arguments": map[string]any{}})
	// MCP 约定：工具层错误走 result.isError，不是 JSON-RPC error
	res := out["result"].(map[string]any)
	if res["isError"] != true {
		t.Fatalf("want isError result, got %v", out)
	}
	if out["error"] != nil {
		t.Fatalf("tool lookup failure must not become a jsonrpc error: %v", out["error"])
	}
}

func TestUnknownMethodReturnsMethodNotFound(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 6, "bogus/method", nil)
	errObj := out["error"].(map[string]any)
	if int(errObj["code"].(float64)) != CodeMethodNotFound {
		t.Fatalf("code = %v", errObj["code"])
	}
}

func TestMalformedRequestReturnsParseError(t *testing.T) {
	s := NewServer(stubAPI{})
	resp, ok := s.Handle(context.Background(), []byte("{not json"))
	if !ok {
		t.Fatal("malformed request still needs an answer")
	}
	var out map[string]any
	json.Unmarshal(resp, &out)
	errObj := out["error"].(map[string]any)
	if int(errObj["code"].(float64)) != CodeInvalidRequest {
		t.Fatalf("code = %v", errObj["code"])
	}
}

func TestPing(t *testing.T) {
	s := NewServer(stubAPI{})
	out := call(t, s, 7, "ping", nil)
	if out["error"] != nil {
		t.Fatalf("ping failed: %v", out["error"])
	}
}

// stubAPI 是实现 ToolAPI 的测试替身，记录调用参数。
type stubAPI struct {
	searched []string
	added    string
	calls    []string
}

func (a stubAPI) Search(ctx context.Context, in SearchInput) (any, error) {
	return map[string]any{"hits": a.searched}, nil
}

func (a stubAPI) Add(ctx context.Context, in AddInput) (any, error) {
	return map[string]any{"id": 1}, nil
}
func (a stubAPI) List(ctx context.Context, in ListInput) (any, error) {
	return map[string]any{"memories": nil}, nil
}
func (a stubAPI) Handoff(ctx context.Context, in HandoffInput) (any, error) {
	return map[string]any{"body": "brief"}, nil
}
func (a stubAPI) Tasks(ctx context.Context, in TaskInput) (any, error) {
	return map[string]any{"cards": nil}, nil
}
func (a stubAPI) TaskGet(ctx context.Context, id int64) (any, error) {
	return map[string]any{"id": id}, nil
}
func (a stubAPI) AddTask(ctx context.Context, in AddTaskInput) (any, error) {
	return map[string]any{"id": 1}, nil
}
func (a stubAPI) UpdateTask(ctx context.Context, in UpdateTaskInput) (any, error) {
	return map[string]any{"id": in.ID}, nil
}
func (a stubAPI) SyncTask(ctx context.Context, in SyncTaskInput) (any, error) {
	return map[string]any{"task_id": 1, "settled": false}, nil
}
func (a stubAPI) ExportTask(ctx context.Context, id int64) (any, error) {
	return map[string]any{"markdown": "task_id: 1"}, nil
}
func (a stubAPI) ResolveProject(ctx context.Context, path string) (any, error) {
	return map[string]any{"known": false}, nil
}
func (a stubAPI) Stats(ctx context.Context) (any, error) { return map[string]any{"memories": 0}, nil }
