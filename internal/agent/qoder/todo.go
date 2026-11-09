package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// todo 是 Qoder 会话里 TaskCreate/TaskUpdate 归并后的一条 Todo。
type todo struct {
	SourceKey  string
	Subject    string
	ActiveForm string
	Status     string
	order      int
}

type todoInput struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

// parseTodos 从会话 JSONL 归并 TaskCreate/TaskUpdate：按 Todo id 取最后一次状态；
// 只改 status 的 update 保留 create 时的 subject。缺 id / 无 create / 坏 JSON 都跳过。
// 整场没有 Todo 调用时返回 nil，交给 ingest 的反推兜底。
func parseTodos(content []byte) []agent.TaskStepDraft {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	merged := map[string]*todo{}
	order := 0
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var l jsonlLine
		if json.Unmarshal(raw, &l) != nil || l.Type != "assistant" {
			continue
		}
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || (b.Name != "TaskCreate" && b.Name != "TaskUpdate") {
				continue
			}
			var in todoInput
			if json.Unmarshal(b.Input, &in) != nil || in.ID == "" {
				continue
			}
			key := "qt:" + in.ID
			cur, ok := merged[key]
			if !ok {
				if b.Name != "TaskCreate" || strings.TrimSpace(in.Subject) == "" {
					continue // 没有 create 的 update 不凭空造步骤
				}
				order++
				cur = &todo{SourceKey: key, Subject: in.Subject, ActiveForm: in.ActiveForm, Status: domain.StepPending, order: order}
				merged[key] = cur
			}
			if in.Subject != "" {
				cur.Subject = in.Subject
			}
			if in.ActiveForm != "" {
				cur.ActiveForm = in.ActiveForm
			}
			if in.Status != "" {
				cur.Status = mapTodoStatus(in.Status)
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}
	list := make([]*todo, 0, len(merged))
	for _, t := range merged {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })
	out := make([]agent.TaskStepDraft, 0, len(list))
	for _, t := range list {
		out = append(out, agent.TaskStepDraft{SourceKey: t.SourceKey, Subject: t.Subject, ActiveForm: t.ActiveForm, Status: t.Status})
	}
	return out
}

func mapTodoStatus(s string) string {
	switch s {
	case "completed", "done":
		return domain.StepDone
	case "in_progress", "doing":
		return domain.StepDoing
	case "blocked":
		return domain.StepBlocked
	case "skipped", "cancelled":
		return domain.StepSkipped
	default:
		return domain.StepPending
	}
}
