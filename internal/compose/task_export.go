package compose

import (
	"fmt"
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
)

func RenderTaskExport(task domain.Task, snippets []domain.TaskSnippet, project domain.Project) string {
	var b strings.Builder
	b.WriteString("# 接手任务\n\n")
	fmt.Fprintf(&b, "- task_id: %d\n", task.ID)
	fmt.Fprintf(&b, "- 项目: %s (`%s`)\n", project.DisplayName, project.Key)
	fmt.Fprintf(&b, "- 目标: %s\n", oneLine(task.Goal))
	fmt.Fprintf(&b, "- 状态: %s\n", task.Status)
	fmt.Fprintf(&b, "- owner: %s\n", task.OwnerAgent)
	if summary := readyTaskSummary(task); summary != "" {
		fmt.Fprintf(&b, "- 摘要: %s\n", oneLine(summary))
	}
	b.WriteString("\n")
	b.WriteString("## 步骤\n\n")
	for _, step := range task.Steps {
		mark := "[ ]"
		switch step.Status {
		case domain.StepDone, domain.StepSkipped:
			mark = "[x]"
		case domain.StepDoing:
			mark = "[~]"
		case domain.StepBlocked:
			mark = "[!]"
		}
		fmt.Fprintf(&b, "- %s %s\n", mark, oneLine(step.Subject))
	}
	if task.NextSteps != "" {
		fmt.Fprintf(&b, "\n## 下一步\n\n%s\n", task.NextSteps)
	}
	if len(task.Files) > 0 {
		fmt.Fprintf(&b, "\n## 改动文件\n\n- %s\n", strings.Join(task.Files, "\n- "))
	}
	if len(task.Commands) > 0 {
		fmt.Fprintf(&b, "\n## 执行命令\n\n- %s\n", strings.Join(task.Commands, "\n- "))
	}
	if len(task.Errors) > 0 {
		fmt.Fprintf(&b, "\n## 未解决报错\n\n- %s\n", strings.Join(task.Errors, "\n- "))
	}
	if len(snippets) > 0 {
		b.WriteString("\n## 最近对话片段\n\n")
		start := len(snippets) - 10
		if start < 0 {
			start = 0
		}
		for _, snippet := range snippets[start:] {
			fmt.Fprintf(&b, "- [%s] %s\n", snippet.Role, oneLine(snippet.Text))
		}
	}
	b.WriteString("\n## 交接要求\n\n")
	b.WriteString("1. 先调用 `task_get` 读取最新任务。\n")
	b.WriteString("2. 用 `task_update` 更新同一个 task_id，不要新建任务。\n")
	b.WriteString("3. 完成后把状态改为 done，并在下一步里写清验证结果。\n")
	return b.String()
}

// RenderTaskHandoffBrief 极简接手版：五块、<=30 行，凸出重点不啰嗦。
func RenderTaskHandoffBrief(task domain.Task, project domain.Project) string {
	var b strings.Builder
	b.WriteString("# 接手任务\n\n")
	fmt.Fprintf(&b, "- 目标: %s\n", oneLine(task.Goal))
	fmt.Fprintf(&b, "- 项目: %s (`%s`) · task_id %d · 状态 %s\n", project.DisplayName, project.Key, task.ID, task.Status)
	if task.NextSteps != "" {
		fmt.Fprintf(&b, "- 现在到哪/卡在哪: %s\n", clipPlain(task.NextSteps, 120))
	}
	b.WriteString("\n## 未完成步骤\n\n")
	n := 0
	for _, st := range task.Steps {
		if st.Status == domain.StepDone || st.Status == domain.StepSkipped {
			continue
		}
		mark := "[ ]"
		if st.Status == domain.StepBlocked {
			mark = "[!]"
		} else if st.Status == domain.StepDoing {
			mark = "[~]"
		}
		fmt.Fprintf(&b, "- %s %s\n", mark, oneLine(st.Subject))
		n++
		if n >= 10 {
			break
		}
	}
	if n == 0 {
		b.WriteString("- （无未完成步骤）\n")
	}
	if len(task.Files) > 0 {
		b.WriteString("\n## 关键文件\n\n")
		for _, f := range head(task.Files, 8) {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	b.WriteString("\n## 交接要求\n\n")
	b.WriteString("1. 先 `task_get` 读最新任务。\n")
	b.WriteString("2. 用 `task_update` 更新同一个 task_id，不要新建。\n")
	b.WriteString("3. 完成后改 done 并在下一步写清验证结果。\n")
	return b.String()
}
