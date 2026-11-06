package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

type SearchInput struct {
	Query        string `json:"query"`
	ProjectPath  string `json:"project_path"`
	Type         string `json:"type"`
	Scope        string `json:"scope"`
	IncludeStale bool   `json:"include_stale"`
	Limit        int    `json:"limit"`
}

type AddInput struct {
	Text        string `json:"text"`
	Type        string `json:"type"`
	Scope       string `json:"scope"`
	ProjectPath string `json:"project_path"`
	Supersedes  int64  `json:"supersedes"`
}

type ListInput struct {
	ProjectPath  string `json:"project_path"`
	IncludeStale bool   `json:"include_stale"`
	Limit        int    `json:"limit"`
}

type HandoffInput struct {
	ProjectPath string `json:"project_path"`
}

type TaskInput struct {
	ProjectPath     string `json:"project_path"`
	Status          string `json:"status"`
	Kind            string `json:"kind"`
	IncludeArchived bool   `json:"include_archived"`
	Limit           int    `json:"limit"`
}

type TaskStepInput struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"active_form"`
	Status      string `json:"status"`
}

type AddTaskInput struct {
	ProjectPath string          `json:"project_path"`
	Goal        string          `json:"goal"`
	Status      string          `json:"status"`
	NextSteps   string          `json:"next_steps"`
	OwnerAgent  string          `json:"owner_agent"`
	Steps       []TaskStepInput `json:"steps"`
}

type UpdateTaskInput struct {
	ID         int64            `json:"id"`
	Goal       *string          `json:"goal"`
	Status     *string          `json:"status"`
	NextSteps  *string          `json:"next_steps"`
	OwnerAgent *string          `json:"owner_agent"`
	Archived   *bool            `json:"archived"`
	Steps      *[]TaskStepInput `json:"steps"`
}

// ToolAPI 是 MCP 层与共享记忆之间的全部接触面。
// daemon.Server 直接实现它（spec §8.1 列的七个工具都从这里出入）。
type ToolAPI interface {
	Search(ctx context.Context, in SearchInput) (any, error)
	Add(ctx context.Context, in AddInput) (any, error)
	List(ctx context.Context, in ListInput) (any, error)
	Handoff(ctx context.Context, in HandoffInput) (any, error)
	Tasks(ctx context.Context, in TaskInput) (any, error)
	TaskGet(ctx context.Context, id int64) (any, error)
	AddTask(ctx context.Context, in AddTaskInput) (any, error)
	UpdateTask(ctx context.Context, in UpdateTaskInput) (any, error)
	ExportTask(ctx context.Context, id int64) (any, error)
	ResolveProject(ctx context.Context, path string) (any, error)
	Stats(ctx context.Context) (any, error)
}

type Server struct {
	api ToolAPI
}

func NewServer(api ToolAPI) *Server { return &Server{api: api} }

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func (s *Server) toolDefinitions() []toolDef {
	return []toolDef{
		{"memory_search", "在跨 agent 共享记忆里检索。中文两字词也能命中；返回条目文本、类型、来源 agent 与最后见到时间。",
			objectSchema(map[string]any{
				"query":         map[string]any{"type": "string", "description": "关键词或短语"},
				"project_path":  map[string]any{"type": "string", "description": "项目绝对路径；省略则只查全局记忆"},
				"type":          map[string]any{"type": "string", "enum": []string{"preference", "constraint", "convention", "lesson", "fact", "goal"}},
				"scope":         map[string]any{"type": "string", "enum": []string{"user", "project"}},
				"include_stale": map[string]any{"type": "boolean"},
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
			}, "query")},
		{"memory_add", "写入共享记忆。别的 agent 下次读简报时就会看到。文本会先去重合并。",
			objectSchema(map[string]any{
				"text":         map[string]any{"type": "string"},
				"type":         map[string]any{"type": "string", "enum": []string{"preference", "constraint", "convention", "lesson", "fact", "goal"}},
				"scope":        map[string]any{"type": "string", "enum": []string{"user", "project"}},
				"project_path": map[string]any{"type": "string"},
				"supersedes":   map[string]any{"type": "integer", "description": "被取代的旧条目 id；只标过时，不删除"},
			}, "text", "type")},
		{"memory_list", "列出共享记忆条目（不做相关性排序）。",
			objectSchema(map[string]any{
				"project_path":  map[string]any{"type": "string"},
				"include_stale": map[string]any{"type": "boolean"},
				"limit":         map[string]any{"type": "integer"},
			})},
		{"handoff_get", "取本项目的交接简报 markdown：在做什么、做到哪、下一步、硬约束。切换 agent 时先读它。",
			objectSchema(map[string]any{
				"project_path": map[string]any{"type": "string"},
			})},
		{"task_list", "列出任务。kind=session 是一次 agent 对话产出的对话任务（含步骤），kind=formal 是跨对话整理的合并任务；不传 kind 返回全部。",
			objectSchema(map[string]any{
				"project_path": map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string", "enum": []string{"doing", "done", "blocked", "unknown"}},
				"kind":         map[string]any{"type": "string", "enum": []string{"session", "formal"}},
				"limit":        map[string]any{"type": "integer"},
			})},
		{"task_get", "按 id 取单张任务卡的全部内容。task_list 之后按需展开细节用这个，别把整份简报复述一遍。",
			objectSchema(map[string]any{"id": map[string]any{"type": "integer"}}, "id")},
		{"task_add", "新建正式任务，可带步骤。",
			objectSchema(map[string]any{
				"project_path": map[string]any{"type": "string"},
				"goal":         map[string]any{"type": "string"},
				"status":       map[string]any{"type": "string", "enum": []string{"doing", "done", "blocked", "unknown"}},
				"next_steps":   map[string]any{"type": "string"},
				"owner_agent":  map[string]any{"type": "string"},
				"steps":        map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, "project_path", "goal")},
		{"task_update", "更新任务、认领、归档或整组替换步骤。",
			objectSchema(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"goal":        map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string", "enum": []string{"doing", "done", "blocked", "unknown"}},
				"next_steps":  map[string]any{"type": "string"},
				"owner_agent": map[string]any{"type": "string"},
				"archived":    map[string]any{"type": "boolean"},
				"steps":       map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			}, "id")},
		{"task_export", "导出交接 Prompt，带同一个 task_id。",
			objectSchema(map[string]any{"id": map[string]any{"type": "integer"}}, "id")},
		{"project_resolve", "把任意目录路径解析成共享库里的 project_key，用来确认两个 agent 看的是同一个项目。",
			objectSchema(map[string]any{"path": map[string]any{"type": "string"}}, "path")},
		{"stats", "共享库状态：项目数、记忆条数、来源与失败项。诊断用。",
			objectSchema(map[string]any{})},
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("bad tools/call params: %w", err)
	}

	result, err := func() (any, error) {
		switch p.Name {
		case "memory_search":
			var in SearchInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Search(ctx, in)
		case "memory_add":
			var in AddInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Add(ctx, in)
		case "memory_list":
			var in ListInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.List(ctx, in)
		case "handoff_get":
			var in HandoffInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Handoff(ctx, in)
		case "task_list":
			var in TaskInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.Tasks(ctx, in)
		case "task_get":
			var in struct {
				ID int64 `json:"id"`
			}
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			if in.ID <= 0 {
				return nil, fmt.Errorf("id must be a positive integer")
			}
			return s.api.TaskGet(ctx, in.ID)
		case "task_add":
			var in AddTaskInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.AddTask(ctx, in)
		case "task_update":
			var in UpdateTaskInput
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			if in.ID <= 0 {
				return nil, fmt.Errorf("id must be a positive integer")
			}
			return s.api.UpdateTask(ctx, in)
		case "task_export":
			var in struct {
				ID int64 `json:"id"`
			}
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			if in.ID <= 0 {
				return nil, fmt.Errorf("id must be a positive integer")
			}
			return s.api.ExportTask(ctx, in.ID)
		case "project_resolve":
			var in struct {
				Path string `json:"path"`
			}
			if err := bind(p.Arguments, &in); err != nil {
				return nil, err
			}
			return s.api.ResolveProject(ctx, in.Path)
		case "stats":
			return s.api.Stats(ctx)
		default:
			return nil, nil
		}
	}()
	if err != nil {
		return toolError(err.Error()), nil
	}
	if result == nil {
		return toolError(fmt.Sprintf("unknown tool: %s", p.Name)), nil
	}
	return toolText(result), nil
}

func bind(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("bad arguments: %w", err)
	}
	return nil
}

// toolText 把结果序列化成 text block：模型读的是文本，
// 结构化数据用 json 字符串承载最稳妥，也避免依赖服务端 structuredContent 支持。
func toolText(v any) map[string]any {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(err.Error())
	}
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": string(body)}},
		"isError": false,
	}
}

func toolError(message string) map[string]any {
	return map[string]any{
		"content": []any{map[string]any{"type": "text", "text": message}},
		"isError": true,
	}
}
