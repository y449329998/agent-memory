package extract

import (
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

const (
	maxGoalRunes      = 600
	maxNextRunes      = 400
	maxQuestionRunes  = 300
	maxItemRunes      = 200
	defaultSettleIdle = 5 * time.Minute
)

var questionMarkers = []string{"？", "?", "要不要", "需要确认", "是否", "哪种", "还是"}

// Settled 判断一场会话是否已经写完。判据是源文件 mtime 与当前时刻之差：
// JSONL 的 updated_at 只反映已解析事件，agent 中途停笔与文件停写在这里无法区分，
// 而 mtime 是「还在不在写」的真凭据。mtime 为零值时返回 false，宁可不出卡。
func Settled(mtime, now time.Time, idle time.Duration) bool {
	if mtime.IsZero() {
		return false
	}
	if idle <= 0 {
		idle = defaultSettleIdle
	}
	return now.Sub(mtime) >= idle
}

// TaskCard 从事件序列抽出交接所需的最小结构。
// 第二返回值 false 表示这场会话没有可交接的目标（比如只有工具调用）。
//
// 所有字段只由规则产出：LLM 不参与，避免它编造「改动了哪些文件」。
// M5 的可选增强只允许改写 Goal 与 NextSteps 两个自由文本字段。
func TaskCard(sess domain.Session, events []domain.SessionEvent, now time.Time) (domain.TaskCard, bool) {
	card := domain.TaskCard{
		SessionID:   sess.ID,
		ProjectID:   sess.ProjectID,
		Confidence:  "rule",
		GeneratedBy: "rule",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	var goals []string
	var lastAssistant string
	lastAssistantIdx := -1
	lastCommandIdx := -1
	var questions []string
	var errors []string
	filesSeen := map[string]bool{}
	cmdsSeen := map[string]bool{}
	errTail := 0

	for i, e := range events {
		switch e.Kind {
		case domain.KindUserText:
			if e.Text == "" {
				continue
			}
			if len(goals) == 0 {
				goals = append(goals, clip(e.Text, maxGoalRunes))
			} else if len(goals) < 4 {
				goals = append(goals, clip(e.Text, maxItemRunes))
			}
		case domain.KindAssistant:
			if e.Text == "" {
				continue
			}
			lastAssistant = e.Text
			lastAssistantIdx = i
			if isQuestion(e.Text) {
				questions = appendUnique(questions, clip(e.Text, maxQuestionRunes), 6)
			}
		case domain.KindToolEdit:
			for _, f := range e.Files {
				if !filesSeen[f] {
					filesSeen[f] = true
					card.Files = appendUnique(card.Files, f, 40)
				}
			}
		case domain.KindToolCommand:
			if e.Text == "" {
				continue
			}
			lastCommandIdx = i
			if !cmdsSeen[e.Text] {
				cmdsSeen[e.Text] = true
				card.Commands = appendUnique(card.Commands, e.Text, 20)
			}
		case domain.KindToolError:
			if e.Text == "" {
				continue
			}
			errors = appendUnique(errors, clip(e.Text, maxItemRunes), 10)
		}
	}

	if len(goals) == 0 {
		return domain.TaskCard{}, false
	}
	card.Goal = strings.Join(goals, " ｜ ")
	card.Errors = errors
	card.OpenQuestions = strings.Join(questions, " / ")

	// 状态判定只看事件尾部：连续失败且其后没有成功动作 → blocked；
	// 出现验证通过类结论 → done；其余 → doing。
	// 验证结论必须落在最后一次命令之后：更早的「测试通过」证明不了当前版本。
	errTail = trailingErrorRun(events)
	verification := ""
	if lastAssistantIdx > lastCommandIdx {
		verification = lastAssistant
	}
	switch {
	case errTail >= 2:
		card.Status = domain.StatusBlocked
		card.NextSteps = "未解决的报错：" + clip(errorsTail(errors, maxNextRunes), maxNextRunes)
	case verified(verification, card.Commands):
		card.Status = domain.StatusDone
		card.NextSteps = clip(lastAssistant, maxNextRunes)
	default:
		card.Status = domain.StatusDoing
		card.NextSteps = clip(lastAssistant, maxNextRunes)
	}

	decisions := decisionsFrom(events)
	card.Decisions = decisions
	return card, true
}

// trailingErrorRun 数事件序列末尾连续出现的报错。
// 中间被成功操作打断就不算阻塞——即使历史上出过错。
func trailingErrorRun(events []domain.SessionEvent) int {
	n := 0
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case domain.KindToolError:
			n++
		case domain.KindToolCommand, domain.KindToolEdit, domain.KindAssistant:
			return n
		}
	}
	return n
}

// verified 判定「最后一条命令之后，assistant 明确说了验证通过」。
// 规则很窄：只有末条 assistant 文本里出现通过类措辞，且这场会话确实跑过命令，
// 才算 done。光有「我改好了」不算——那是最容易骗人的话。
//
// 否定/失败措辞优先：同一段文本里出现「未验证 / 没通过 / 失败 / FAIL …」时，
// 即使也带了「通过」字样也不判 done。「测试通过率 60%，PASS 3 个，FAIL 2 个」
// 这类汇报最容易被误读成完成。
func verified(lastAssistant string, commands []string) bool {
	if len(commands) == 0 || lastAssistant == "" {
		return false
	}
	for _, marker := range []string{"未通过", "没有通过", "没通过", "没验证", "未验证", "失败", "FAIL", "报错", "超时"} {
		if strings.Contains(lastAssistant, marker) {
			return false
		}
	}
	for _, marker := range []string{"测试通过", "全部通过", "验证通过", "已通过测试", "PASS", "通过验证"} {
		if strings.Contains(lastAssistant, marker) {
			return true
		}
	}
	return false
}

// decisionsFrom 取「做过的事」作为决策：先命令后文件改动，各自限量。
// 真正的语义决策留给 M5 的 LLM 增强，规则层不假装理解。
func decisionsFrom(events []domain.SessionEvent) []string {
	var out []string
	for _, e := range events {
		if e.Kind == domain.KindToolCommand {
			out = appendUnique(out, "执行: "+clip(e.Text, 120), 8)
		}
	}
	for _, e := range events {
		if e.Kind == domain.KindToolEdit {
			for _, f := range e.Files {
				out = appendUnique(out, "改动: "+f, 16)
			}
		}
	}
	return out
}

func isQuestion(s string) bool {
	if len([]rune(s)) > 200 {
		return false
	}
	for _, m := range questionMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func errorsTail(errors []string, n int) string {
	if len(errors) == 0 {
		return "（无）"
	}
	start := len(errors) - 2
	if start < 0 {
		start = 0
	}
	return clip(strings.Join(errors[start:], " ｜ "), n)
}

func appendUnique(in []string, v string, cap int) []string {
	if v == "" {
		return in
	}
	for _, x := range in {
		if x == v {
			return in
		}
	}
	if cap > 0 && len(in) >= cap {
		return in
	}
	return append(in, v)
}

// clip 把文本压成单行并截到 n 个 rune 以内（含省略号）：上限说的是
// 字段总长，不能因为追加「…」反而多出一个 rune。
func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string(r[:n-1]) + "…"
}
