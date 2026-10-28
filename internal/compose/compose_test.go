package compose

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/extract"
	"github.com/muyi99/agent-memory/internal/store"
)

type fixture struct {
	st        *store.Store
	projectID int64
	ctx       context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	pid, err := st.EnsureProject(ctx, "f:/go/demo", "demo", "", `F:\go\demo`, now)
	if err != nil {
		t.Fatal(err)
	}

	add := func(scope domain.Scope, pid int64, typ domain.MemoryType, text, agent, path string, seen time.Time) {
		t.Helper()
		_, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: scope, Type: typ, Text: text, SourceAgent: agent, SourcePath: path, Quote: text,
		}, seen)
		if err != nil {
			t.Fatal(err)
		}
	}
	add(domain.ScopeUser, 0, domain.TypePreference, "沟通用中文，提交信息也用中文", "qoder", "/u1.md", now)
	add(domain.ScopeUser, 0, domain.TypePreference, "变量用 snake_case", "trae", "/u2.md", now.Add(time.Minute))
	add(domain.ScopeProject, pid, domain.TypeConstraint, "余额扣减需 WHERE balance >= cost 条件更新", "qoder", "/p1.md", now)
	add(domain.ScopeProject, pid, domain.TypeConvention, "ai_token_log.change_type：1=充值，-1=扣减", "trae", "/p2.md", now)
	add(domain.ScopeProject, pid, domain.TypeFact, "已废弃的旧写法", "qoder", "/p3.md", now)

	ids, err := st.QueryStrings(ctx, `SELECT CAST(id AS TEXT) FROM memory_item WHERE text = ?`, "已废弃的旧写法")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	staleID, err := strconv.ParseInt(ids[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetStaleByID(ctx, staleID, true); err != nil {
		t.Fatal(err)
	}

	// 另一个项目的记忆不能混进本项目简报。
	otherPID, err := st.EnsureProject(ctx, "f:/go/other", "other", "", `F:\go\other`, now)
	if err != nil {
		t.Fatal(err)
	}
	add(domain.ScopeProject, otherPID, domain.TypeConstraint, "别的项目才有的约束", "qoder", "/o1.md", now)

	// 两张任务卡：一新一旧，简报只应带最近 3 张里的前几张
	sid := seedSessionCard(t, st, ctx, "s-old", pid, now.Add(-time.Hour), domain.StatusDone,
		"接入余额校验", "无", []string{`F:\a.go`})
	_ = sid
	seedSessionCard(t, st, ctx, "s-new", pid, now, domain.StatusBlocked,
		"给扣减加乐观锁", "undefined: decrementWithLock", []string{`F:\svc.go`, `F:\dao.go`})

	return &fixture{st: st, projectID: pid, ctx: ctx}
}

func seedSessionCard(t *testing.T, st *store.Store, ctx context.Context, external string, pid int64, at time.Time, status, goal, next string, files []string) int64 {
	t.Helper()
	sid, err := st.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: external, ProjectID: pid, Title: goal,
		StartedAt: at.Add(-10 * time.Minute), UpdatedAt: at, EventCount: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	card := domain.TaskCard{SessionID: sid, ProjectID: pid, Goal: goal, Status: status,
		NextSteps: next, Files: files, Commands: []string{"go test ./..."}, GeneratedBy: "rule", Confidence: "rule"}
	if status == domain.StatusBlocked {
		card.Errors = []string{next}
	}
	if _, err := st.UpsertTaskCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	return sid
}

func render(t *testing.T, f *fixture, budget int) Result {
	t.Helper()
	c := New(f.st, Config{BudgetTokens: budget, RecentCards: 3})
	res, err := c.Render(f.ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRender_ContainsAllSixSectionsInFixedOrder(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	for _, want := range []string{
		"## 项目概况",
		"## 用户偏好",
		"## 项目硬约束与约定",
		"## 最近任务",
		"## 未决问题",
		"## 共享记忆入口",
	} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("missing section %q in:\n%s", want, res.Body)
		}
	}
	idx := func(s string) int { return strings.Index(res.Body, s) }
	if !(idx("## 项目概况") < idx("## 用户偏好") && idx("## 用户偏好") < idx("## 项目硬约束与约定") &&
		idx("## 项目硬约束与约定") < idx("## 最近任务") && idx("## 最近任务") < idx("## 未决问题") &&
		idx("## 未决问题") < idx("## 共享记忆入口")) {
		t.Fatalf("sections out of order:\n%s", res.Body)
	}
}

func TestRender_ExcludesStaleAndForeignProject(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	if strings.Contains(res.Body, "已废弃的旧写法") {
		t.Fatal("stale memory must not appear")
	}
	if strings.Contains(res.Body, "别的项目") {
		t.Fatal("foreign project memory leaked")
	}
}

func TestRender_CardsAreMostRecentFirst(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	if strings.Index(res.Body, "给扣减加乐观锁") > strings.Index(res.Body, "接入余额校验") {
		t.Fatalf("cards must be ordered by session recency:\n%s", res.Body)
	}
	if !strings.Contains(res.Body, "状态: blocked") {
		t.Fatalf("status missing:\n%s", res.Body)
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	f := newFixture(t)
	a := render(t, f, 1500)
	b := render(t, f, 1500)
	if a.Body != b.Body {
		t.Fatal("same store state must produce the same bytes")
	}
	if a.ContentHash != b.ContentHash {
		t.Fatal("hash must match content")
	}
}

func TestRender_BudgetTrimsCardDetailBeforeDroppingSections(t *testing.T) {
	// 预算刚好容不下降级后的卡片细节：优先砍细节，再砍卡片数量
	res := render(t, newFixture(t), 90)
	if !res.Truncated {
		t.Fatal("want truncated flag under a tight budget")
	}
	for _, mustKeep := range []string{"## 用户偏好", "## 项目硬约束与约定", "沟通用中文"} {
		if !strings.Contains(res.Body, mustKeep) {
			t.Fatalf("protected section dropped:\n%s", res.Body)
		}
	}
	if strings.Contains(res.Body, "F:\\svc.go") {
		t.Fatalf("card file list should be the first thing cut:\n%s", res.Body)
	}
	if res.Tokens > 90 {
		t.Fatalf("tokens=%d exceeds budget", res.Tokens)
	}
}

func TestRender_ExtremeBudgetStillKeepsPreferencesAndConstraints(t *testing.T) {
	res := render(t, newFixture(t), 5)
	if !strings.Contains(res.Body, "## 用户偏好") || !strings.Contains(res.Body, "## 项目硬约束与约定") {
		t.Fatalf("protected sections must survive any budget:\n%s", res.Body)
	}
	if !strings.Contains(res.Body, "## 共享记忆入口") {
		t.Fatalf("entry hint must survive:\n%s", res.Body)
	}
}

func TestRender_TightBudgetKeepsEveryProtectedItem(t *testing.T) {
	// 预算紧到放不下任务卡时，偏好与硬约束的每一条都必须还在，只允许去掉元信息。
	res := render(t, newFixture(t), 90)
	for _, want := range []string{
		"变量用 snake_case",
		"沟通用中文，提交信息也用中文",
		"余额扣减需 WHERE balance >= cost 条件更新",
		"ai_token_log.change_type：1=充值，-1=扣减",
	} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("protected item %q dropped:\n%s", want, res.Body)
		}
	}
	if strings.Contains(res.Body, "给扣减加乐观锁") {
		t.Fatalf("cards should be gone under this budget:\n%s", res.Body)
	}
	if res.Tokens > 90 || !res.Truncated {
		t.Fatalf("tokens=%d truncated=%v", res.Tokens, res.Truncated)
	}
}

func TestEstimateTokens_CountsCJKAsOnePerChar(t *testing.T) {
	if got := EstimateTokens("中文中文中文"); got < 5 {
		t.Fatalf("cjk undercounted: %d", got)
	}
	if got := EstimateTokens("hello hello hello"); got > 6 {
		t.Fatalf("ascii overcounted: %d", got)
	}
}

func TestRender_UsesExtractorRulesEndToEnd(t *testing.T) {
	// 简报里的任务卡字段必须与 extract 的产出一致，防止 compose 自己编内容
	f := newFixture(t)
	events := []domain.SessionEvent{
		{Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "写个新目标"},
		{Seq: 2, Role: "assistant", Kind: domain.KindAssistant, Text: "先不动"},
	}
	card, ok := extract.TaskCard(domain.Session{ID: 99, ProjectID: f.projectID}, events, time.Unix(1700000100, 0))
	if !ok {
		t.Fatal("want a card")
	}
	sid, err := f.st.UpsertSession(f.ctx, domain.Session{Agent: "qoder", ExternalID: "s-x",
		ProjectID: f.projectID, Title: card.Goal, StartedAt: time.Unix(1700000100, 0), UpdatedAt: time.Unix(1700000100, 0)})
	if err != nil {
		t.Fatal(err)
	}
	// extract 的输入会话 id 是占位值，落库前必须换成真实会话 id，否则外键拒绝插入。
	card.SessionID = sid
	if _, err := f.st.UpsertTaskCard(f.ctx, card); err != nil {
		t.Fatal(err)
	}
	res := render(t, f, 1500)
	if !strings.Contains(res.Body, "写个新目标") {
		t.Fatalf("extracted goal missing from brief:\n%s", res.Body)
	}
}
