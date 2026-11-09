package compose

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
	"github.com/muyi99/agent-memory/internal/store"
)

const (
	defaultBudgetTokens = 4000
	defaultRecentCards  = 3
)

type Config struct {
	BudgetTokens int
	RecentCards  int
}

type Result struct {
	Body        string
	Tokens      int
	Truncated   bool
	ContentHash string
}

// Composer 把库状态渲染成交接简报。它是纯函数：不读 agent 文件、不写磁盘。
type Composer struct {
	st  *store.Store
	cfg Config
}

func New(st *store.Store, cfg Config) *Composer {
	if cfg.BudgetTokens <= 0 {
		cfg.BudgetTokens = defaultBudgetTokens
	}
	if cfg.RecentCards <= 0 {
		cfg.RecentCards = defaultRecentCards
	}
	return &Composer{st: st, cfg: cfg}
}

// EstimateTokens 粗估 token 数：一个汉字算一个，其余非空白字符四个算一个。
// 简报语料以中文为主，这个估计足够稳定，且不需要引分词器。
func EstimateTokens(s string) int {
	var han, other int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.IsSpace(r):
		default:
			other++
		}
	}
	return han + other/4 + 1
}

// recentTasks 先取人工整理过的合并任务；一条都没有才退回最近的对话任务。
// 对话任务数量多且未经整理，直接进简报会把「进行中的任务」挤满碎片。
func (c *Composer) recentTasks(ctx context.Context, projectID int64) ([]domain.Task, error) {
	tasks, err := c.st.ListTasks(ctx, store.TaskFilter{
		ProjectID: projectID, Kind: domain.TaskKindFormal, Limit: c.cfg.RecentCards,
	})
	if err != nil || len(tasks) > 0 {
		return tasks, err
	}
	return c.st.ListTasks(ctx, store.TaskFilter{
		ProjectID: projectID, Kind: domain.TaskKindSession, Limit: c.cfg.RecentCards,
	})
}

// Render 生成简报。裁剪顺序是刻意的：先砍任务卡细节，再减卡片数量，最后截任务卡与未决问题；
// 用户偏好与项目硬约束任何情况下都不砍——那是「不用从头描述」的最低保障。
// 同样的库状态必须产出逐字节相同的正文，否则每次扫描都会重写 HANDOFF.md。
func (c *Composer) Render(ctx context.Context, projectID int64) (Result, error) {
	project, ok, err := c.st.ProjectByID(ctx, projectID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("project %d not found", projectID)
	}

	cards, err := c.st.ListTaskCards(ctx, projectID, "", c.cfg.RecentCards)
	if err != nil {
		return Result{}, err
	}
	cardSessions := make(map[int64]domain.Session, len(cards))
	for _, card := range cards {
		sess, ok, err := c.st.SessionByID(ctx, card.SessionID)
		if err != nil {
			return Result{}, err
		}
		if ok {
			cardSessions[card.ID] = sess
		}
	}

	userItems, err := c.st.RecentMemoriesByType(ctx, 0, domain.ScopeUser, []domain.MemoryType{
		domain.TypePreference, domain.TypeConvention, domain.TypeConstraint, domain.TypeLesson, domain.TypeFact,
	}, 40)
	if err != nil {
		return Result{}, err
	}
	projectItems, err := c.st.RecentMemoriesByType(ctx, projectID, domain.ScopeProject, []domain.MemoryType{
		domain.TypeConstraint, domain.TypeConvention, domain.TypeLesson,
	}, 40)
	if err != nil {
		return Result{}, err
	}

	tasks, err := c.recentTasks(ctx, projectID)
	if err != nil {
		return Result{}, err
	}
	taskCount := len(cards)
	if len(tasks) > 0 {
		taskCount = len(tasks)
	}
	overview := renderOverview(project, len(userItems), len(projectItems), taskCount)
	prefs := renderList("## 用户偏好", userItems)
	constraints := renderList("## 项目硬约束与约定", projectItems)
	entry := renderEntry(project.Key)

	type attempt struct {
		cards     string
		questions string
		truncated bool
	}
	var attempts []attempt
	if len(tasks) > 0 {
		taskQuestions := renderTaskQuestions(tasks)
		attempts = append(attempts,
			attempt{cards: section("## 进行中的任务", renderTasks(tasks, cardDetailFull)), questions: taskQuestions},
			attempt{cards: section("## 进行中的任务", renderTasks(tasks, cardDetailLean)), questions: taskQuestions, truncated: true},
		)
		for _, ask := range []int{2, 1} {
			if len(tasks) == 0 {
				break
			}
			shown := tasks
			if ask < len(tasks) {
				shown = tasks[:ask]
			}
			attempts = append(attempts, attempt{
				cards:     section("## 进行中的任务", renderTasks(shown, cardDetailLean)),
				questions: renderTaskQuestions(shown),
				truncated: true,
			})
		}
		attempts = append(attempts, attempt{
			cards:     section("## 进行中的任务", ""),
			questions: "## 未决问题\n\n（超预算，已省略）\n",
			truncated: true,
		})
	} else {
		// 没有合并任务时保留旧任务卡，兼容尚未回填/尚未同步的库。
		full := renderCards(cards, cardSessions, cardDetailFull)
		lean := renderCards(cards, cardSessions, cardDetailLean)
		attempts = []attempt{
			{cards: section("## 最近任务", full), questions: renderQuestions(cards)},
			{cards: section("## 最近任务", lean), questions: renderQuestions(cards), truncated: true},
		}
		for _, ask := range []int{2, 1} {
			if len(cards) == 0 {
				break
			}
			shown := cards
			if ask < len(cards) {
				shown = cards[:ask]
			}
			attempts = append(attempts, attempt{
				cards:     section("## 最近任务", renderCards(shown, cardSessions, cardDetailLean)),
				questions: renderQuestions(shown),
				truncated: true,
			})
		}
		attempts = append(attempts, attempt{
			cards:     section("## 最近任务", ""),
			questions: "## 未决问题\n\n（超预算，已省略）\n",
			truncated: true,
		})
	}

	var (
		body      string
		truncated bool
	)
	for _, a := range attempts {
		candidate := strings.Join([]string{overview, prefs, constraints, a.cards, a.questions, entry}, "\n")
		if EstimateTokens(candidate) <= c.cfg.BudgetTokens {
			body, truncated = candidate, a.truncated
			break
		}
	}
	if body == "" {
		// 连「没有任务卡」的一档都超预算：进入压缩档。
		// 只去掉受保护清单的元信息与任务卡整段，条目本身一条不丢。
		body = strings.Join([]string{
			renderOverviewCompact(project),
			renderListCompact("## 用户偏好", userItems),
			renderListCompact("## 项目硬约束与约定", projectItems),
			renderTasksCompact(tasks),
			renderEntryCompact(),
		}, "\n")
		truncated = true
	}

	final := strings.TrimRight(body, "\n") + "\n"
	return Result{
		Body:        final,
		Tokens:      EstimateTokens(final),
		Truncated:   truncated,
		ContentHash: normalize.Hash(final),
	}, nil
}

func renderOverview(p domain.Project, userCount, projectCount, cardCount int) string {
	var lines []string
	lines = append(lines, "## 项目概况", "")
	lines = append(lines, fmt.Sprintf("- project_key: `%s`", p.Key))
	if p.PrimaryPath != "" {
		lines = append(lines, fmt.Sprintf("- 路径: `%s`", p.PrimaryPath))
	}
	lines = append(lines, fmt.Sprintf("- 共享记忆: 全局 %d 条 / 本项目 %d 条；最近任务 %d 个", userCount, projectCount, cardCount))
	return strings.Join(lines, "\n") + "\n"
}

func section(title, body string) string {
	if strings.TrimSpace(body) == "" {
		return title + "\n\n（无）\n"
	}
	return title + "\n\n" + strings.TrimRight(body, "\n") + "\n"
}

func renderList(title string, items []domain.MemoryItem) string {
	if len(items) == 0 {
		return section(title, "")
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	for _, m := range items {
		agents := make([]string, 0, len(m.Sources))
		for _, s := range m.Sources {
			agents = append(agents, s.Agent)
		}
		fmt.Fprintf(&b, "- (%s, 来自 %s) %s\n", m.Type, strings.Join(agents, "+"), oneLine(m.Text))
	}
	return b.String()
}

// renderListCompact 去掉类型与来源等元信息，只保留条目原文。
// 仅在预算连完整清单都放不下时使用，保证「一条都不丢」。
func renderListCompact(title string, items []domain.MemoryItem) string {
	if len(items) == 0 {
		return section(title, "")
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n\n")
	for _, m := range items {
		fmt.Fprintf(&b, "- %s\n", oneLine(m.Text))
	}
	return b.String()
}

func renderOverviewCompact(p domain.Project) string {
	return fmt.Sprintf("## 项目概况\n\n- project_key: `%s`\n", p.Key)
}

type detailLevel int

const (
	cardDetailFull detailLevel = iota
	cardDetailLean
)

func renderCards(cards []domain.TaskCard, sessions map[int64]domain.Session, level detailLevel) string {
	var b strings.Builder
	for i, card := range cards {
		sess := sessions[card.ID]
		when := sess.UpdatedAt
		if when.IsZero() {
			when = card.UpdatedAt
		}
		fmt.Fprintf(&b, "### %d. %s\n", i+1, oneLine(card.Goal))
		fmt.Fprintf(&b, "- 状态: %s ｜ 会话: `%s` ｜ 最近活动: %s\n", card.Status, oneLine(sess.ExternalID), when.Format(time.RFC3339))
		if level == cardDetailFull && len(card.Decisions) > 0 {
			fmt.Fprintf(&b, "- 关键决策: %s\n", strings.Join(head(card.Decisions, 5), "; "))
		}
		if level == cardDetailFull && len(card.Files) > 0 {
			fmt.Fprintf(&b, "- 改动文件: %s\n", strings.Join(head(card.Files, 8), ", "))
		}
		if len(card.Commands) > 0 {
			fmt.Fprintf(&b, "- 执行命令: %s\n", strings.Join(head(card.Commands, 4), " && "))
		}
		if card.NextSteps != "" {
			fmt.Fprintf(&b, "- 下一步: %s\n", oneLine(card.NextSteps))
		}
		if len(card.Errors) > 0 {
			fmt.Fprintf(&b, "- 未解决报错: %s\n", oneLine(card.Errors[0]))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTasks(tasks []domain.Task, level detailLevel) string {
	var b strings.Builder
	for i, task := range tasks {
		done := 0
		current := ""
		for _, step := range task.Steps {
			if step.Status == domain.StepDone || step.Status == domain.StepSkipped {
				done++
			}
			if current == "" && (step.Status == domain.StepDoing || step.Status == domain.StepPending) {
				current = step.Subject
			}
		}
		fmt.Fprintf(&b, "### %d. %s\n", i+1, oneLine(task.Goal))
		fmt.Fprintf(&b, "- 状态: %s ｜ owner: %s ｜ 步骤: %d/%d", task.Status, task.OwnerAgent, done, len(task.Steps))
		if current != "" {
			fmt.Fprintf(&b, " ｜ 当前: %s", oneLine(current))
		}
		b.WriteString("\n")
		if summary := readyTaskSummary(task); summary != "" {
			fmt.Fprintf(&b, "- 摘要: %s\n", clipPlain(summary, 400))
		} else if task.NextSteps != "" {
			fmt.Fprintf(&b, "- 下一步: %s\n", oneLine(task.NextSteps))
		}
		if level == cardDetailFull && len(task.Files) > 0 {
			fmt.Fprintf(&b, "- 改动文件: %s\n", strings.Join(head(task.Files, 8), ", "))
		}
		if len(task.Commands) > 0 {
			fmt.Fprintf(&b, "- 执行命令: %s\n", strings.Join(head(task.Commands, 4), " && "))
		}
		if len(task.Errors) > 0 {
			fmt.Fprintf(&b, "- 未解决报错: %s\n", oneLine(task.Errors[0]))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderTasksCompact(tasks []domain.Task) string {
	if len(tasks) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 进行中的任务\n\n")
	for _, task := range tasks {
		fmt.Fprintf(&b, "- [%s] %s", task.Status, oneLine(task.Goal))
		if summary := readyTaskSummary(task); summary != "" {
			fmt.Fprintf(&b, " — %s", clipPlain(summary, 160))
		} else if task.NextSteps != "" {
			fmt.Fprintf(&b, " — 下一步: %s", clipPlain(task.NextSteps, 120))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func readyTaskSummary(task domain.Task) string {
	if task.SummaryState == "ready" {
		return strings.TrimSpace(task.Summary)
	}
	return ""
}

func clipPlain(s string, n int) string {
	line := oneLine(s)
	r := []rune(line)
	if len(r) <= n {
		return line
	}
	if n <= 0 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

func renderTaskQuestions(tasks []domain.Task) string {
	var qs []string
	for _, task := range tasks {
		if task.OpenQuestions == "" {
			continue
		}
		for _, q := range strings.Split(task.OpenQuestions, " / ") {
			if q = strings.TrimSpace(q); q != "" {
				qs = append(qs, fmt.Sprintf("- [%s] %s", task.Status, oneLine(q)))
			}
		}
	}
	return section("## 未决问题", strings.Join(qs, "\n"))
}

func renderQuestions(cards []domain.TaskCard) string {
	var qs []string
	for _, card := range cards {
		if card.OpenQuestions == "" {
			continue
		}
		for _, q := range strings.Split(card.OpenQuestions, " / ") {
			if q = strings.TrimSpace(q); q != "" {
				qs = append(qs, fmt.Sprintf("- [%s] %s", card.Status, oneLine(q)))
			}
		}
	}
	return section("## 未决问题", strings.Join(qs, "\n"))
}

func renderEntry(projectKey string) string {
	return strings.Join([]string{
		"## 共享记忆入口",
		"",
		"接上下文时优先读这份简报，再决定要不要按需检索：",
		"",
		"- `agent-memory memory search \"<关键词>\" --project " + projectKey + "`",
		"- MCP tools: `memory_search` / `memory_add` / `handoff_get` / `task_list`（M3 起可用）",
		"- 学到的新约定请用 `memory_add` 写回共享库，别只留在本 agent 的记忆里",
		"",
	}, "\n")
}

func renderEntryCompact() string {
	return "## 共享记忆入口\n\n- 检索用 `memory_search`\n"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
}

func head(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}
