package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// Qoder 的 Todo 两步走：TaskCreate 没有 id，id 在 tool_result 的
// 「Task #157 created successfully」里返回；TaskUpdate 用 taskId 改状态。
// 所以要先记 create 的 tool_use_id，再等对应 tool_result 拿到数字 id。

// todo 是归并后的一条 Todo。
type todo struct {
	ID         string
	Subject    string
	ActiveForm string
	Status     string
	order      int
}

type todoInput struct {
	ID         string `json:"id"`
	TaskID     string `json:"taskId"`
	Subject    string `json:"subject"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status"`
}

var createdID = regexp.MustCompile(`Task #(\d+)`)

// parseTodos 从会话 JSONL 归并 TaskCreate/TaskUpdate：
// create 的 tool_use 先暂存，等 tool_result 给出数字 id 后落成步骤；
// TaskUpdate 按 taskId 覆盖状态（只改 status 时保留 create 的 subject）。
// 整场没有 Todo 时返回 nil，交给 ingest 的反推兜底。
func parseTodos(content []byte) []agent.TaskStepDraft {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)

	type pending struct {
		subject    string
		activeForm string
		order      int
	}
	pendingByCall := map[string]pending{}
	todos := map[string]*todo{}
	order := 0

	apply := func(t *todo, in todoInput) {
		if in.Subject != "" {
			t.Subject = in.Subject
		}
		if in.ActiveForm != "" {
			t.ActiveForm = in.ActiveForm
		}
		if in.Status != "" {
			t.Status = mapTodoStatus(in.Status)
		}
	}

	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var l jsonlLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		if l.Type != "assistant" && l.Type != "user" {
			continue
		}
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				switch b.Name {
				case "TaskCreate":
					var in todoInput
					if json.Unmarshal(b.Input, &in) != nil || strings.TrimSpace(in.Subject) == "" {
						continue
					}
					order++
					pendingByCall[b.ID] = pending{subject: in.Subject, activeForm: in.ActiveForm, order: order}
				case "TaskUpdate":
					var in todoInput
					if json.Unmarshal(b.Input, &in) != nil {
						continue
					}
					id := in.TaskID
					if id == "" {
						id = in.ID
					}
					if id == "" {
						continue
					}
					t, ok := todos[id]
					if !ok {
						// update 先于 create 的结果出现时，用 update 里带的字段兜底建步骤
						if strings.TrimSpace(in.Subject) == "" {
							continue
						}
						order++
						t = &todo{ID: id, Subject: in.Subject, Status: domain.StepPending, order: order}
						todos[id] = t
					}
					apply(t, in)
				}
			case "tool_result":
				p, ok := pendingByCall[b.ToolUseID]
				if !ok {
					continue
				}
				delete(pendingByCall, b.ToolUseID)
				text := textFromContent(b.Content)
				m := createdID.FindStringSubmatch(text)
				if m == nil {
					continue
				}
				id := m[1]
				t, exists := todos[id]
				if !exists {
					t = &todo{ID: id, Subject: p.subject, ActiveForm: p.activeForm, Status: domain.StepPending, order: p.order}
					todos[id] = t
				}
			}
		}
	}
	if len(todos) == 0 {
		return nil
	}
	list := make([]*todo, 0, len(todos))
	for _, t := range todos {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].order != list[j].order {
			return list[i].order < list[j].order
		}
		a, _ := strconv.Atoi(list[i].ID)
		b, _ := strconv.Atoi(list[j].ID)
		return a < b
	})
	// task_step 有 100 条上限；超长会话保留最近创建的 100 条（最新的才是当前进度）
	if len(list) > 100 {
		list = list[len(list)-100:]
	}
	out := make([]agent.TaskStepDraft, 0, len(list))
	for _, t := range list {
		out = append(out, agent.TaskStepDraft{
			SourceKey: "qt:" + t.ID, Subject: t.Subject, ActiveForm: t.ActiveForm, Status: t.Status,
		})
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
