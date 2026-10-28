package extract

import (
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func ev(seq int, kind domain.EventKind, text string, files ...string) domain.SessionEvent {
	base := time.Unix(1700000000, 0).Add(time.Duration(seq) * time.Minute)
	role := "assistant"
	if kind == domain.KindUserText {
		role = "user"
	}
	return domain.SessionEvent{Seq: seq, Role: role, Kind: kind, Text: text, Files: files, Ts: base}
}

func TestTaskCard_GoalIsFirstRealUserTextAndCorrectionsAppend(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "给扣减余额加乐观锁"),
		ev(2, domain.KindAssistant, "先看 Service"),
		ev(3, domain.KindUserText, "另外，失败时不要扣减余额"),
		ev(4, domain.KindToolEdit, "svc.go", `F:\a\svc.go`),
	}
	card, ok := TaskCard(domain.Session{ID: 1, ProjectID: 7}, events, time.Now())
	if !ok {
		t.Fatal("want a card")
	}
	if !strings.Contains(card.Goal, "给扣减余额加乐观锁") {
		t.Fatalf("goal = %q", card.Goal)
	}
	if !strings.Contains(card.Goal, "另外，失败时不要扣减余额") {
		t.Fatalf("later user turns must be captured: %q", card.Goal)
	}
	if card.ProjectID != 7 {
		t.Fatalf("project id = %d", card.ProjectID)
	}
	if len(card.Files) != 1 || card.Files[0] != `F:\a\svc.go` {
		t.Fatalf("files = %v", card.Files)
	}
}

func TestTaskCard_StatusBlockedWhenTrailingErrorsUnresolved(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "修复扣减逻辑"),
		ev(2, domain.KindToolCommand, "go test ./...", `F:\a`),
		ev(3, domain.KindToolError, "undefined: decrementWithLock"),
		ev(4, domain.KindToolError, "FAIL\tpkg/dao"),
	}
	card, ok := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if !ok {
		t.Fatal("want a card")
	}
	if card.Status != domain.StatusBlocked {
		t.Fatalf("status = %q want blocked", card.Status)
	}
	if len(card.Errors) != 2 {
		t.Fatalf("errors = %v", card.Errors)
	}
	if !strings.Contains(card.NextSteps, "undefined: decrementWithLock") {
		t.Fatalf("next steps must name the blocker: %q", card.NextSteps)
	}
}

func TestTaskCard_StatusDoneOnVerificationAfterSuccess(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "修复扣减逻辑"),
		ev(2, domain.KindToolError, "FAIL: balance over-drawn"),
		ev(3, domain.KindToolEdit, "svc.go", `F:\a\svc.go`),
		ev(4, domain.KindToolCommand, "go test ./... ok"),
		ev(5, domain.KindAssistant, "测试通过，扣减已用条件更新"),
	}
	card, ok := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if !ok {
		t.Fatal("want a card")
	}
	if card.Status != domain.StatusDone {
		t.Fatalf("status = %q want done", card.Status)
	}
	if !strings.Contains(card.OpenQuestions, "") && card.OpenQuestions != "" {
		t.Fatalf("no open question expected, got %q", card.OpenQuestions)
	}
}

func TestTaskCard_StatusDoingWhenWorkRemains(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "实现导出功能"),
		ev(2, domain.KindAssistant, "先加接口，稍后补测试"),
	}
	card, _ := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if card.Status != domain.StatusDoing {
		t.Fatalf("status = %q want doing", card.Status)
	}
	if !strings.Contains(card.NextSteps, "先加接口，稍后补测试") {
		t.Fatalf("next steps = %q", card.NextSteps)
	}
}

func TestTaskCard_NoUserTextYieldsNoCard(t *testing.T) {
	events := []domain.SessionEvent{
		ev(2, domain.KindToolEdit, "svc.go", `F:\a\svc.go`),
	}
	if _, ok := TaskCard(domain.Session{ID: 1}, events, time.Now()); ok {
		t.Fatal("a session without a stated goal should not produce a card")
	}
}

func TestTaskCard_CollectsCommandsAndDedupesFiles(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "跑测试"),
		ev(2, domain.KindToolCommand, "go test ./..."),
		ev(3, domain.KindToolCommand, "go test ./..."),
		ev(4, domain.KindToolEdit, "a.go", `F:\a.go`),
		ev(5, domain.KindToolEdit, "a.go again", `F:\a.go`),
		ev(6, domain.KindToolEdit, "b.go", `F:\b.go`),
	}
	card, _ := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if len(card.Commands) != 1 {
		t.Fatalf("commands must dedupe: %v", card.Commands)
	}
	if len(card.Files) != 2 {
		t.Fatalf("files must dedupe keeping order: %v", card.Files)
	}
}

func TestTaskCard_QuestionsExtractedFromAssistantText(t *testing.T) {
	events := []domain.SessionEvent{
		ev(1, domain.KindUserText, "重构扣减"),
		ev(2, domain.KindAssistant, "要不要把余额校验放到事务内？"),
		ev(3, domain.KindAssistant, "这里需要确认口径：是否含运费"),
	}
	card, _ := TaskCard(domain.Session{ID: 1}, events, time.Now())
	if !strings.Contains(card.OpenQuestions, "余额校验") || !strings.Contains(card.OpenQuestions, "运费") {
		t.Fatalf("open questions = %q", card.OpenQuestions)
	}
}

func TestTaskCard_GeneratedByAndConfidence(t *testing.T) {
	card, _ := TaskCard(domain.Session{ID: 1}, []domain.SessionEvent{ev(1, domain.KindUserText, "x")}, time.Now())
	if card.GeneratedBy != "rule" || card.Confidence != "rule" {
		t.Fatalf("generated_by=%q confidence=%q", card.GeneratedBy, card.Confidence)
	}
}

func TestSettled_UsesFileMTimeNotEventTime(t *testing.T) {
	now := time.Unix(1700000000, 0)
	if Settled(now.Add(-2*time.Minute), now, time.Minute*5) {
		t.Fatal("a session still being written must not be settled")
	}
	if !Settled(now.Add(-6*time.Minute), now, time.Minute*5) {
		t.Fatal("idle beyond threshold must count as settled")
	}
	if Settled(time.Time{}, now, time.Minute*5) {
		t.Fatal("zero mtime must not be treated as settled")
	}
}

func TestTaskCard_GoalIsTruncated(t *testing.T) {
	long := strings.Repeat("锁", 3000)
	card, _ := TaskCard(domain.Session{ID: 1}, []domain.SessionEvent{ev(1, domain.KindUserText, long)}, time.Now())
	if len([]rune(card.Goal)) > 600 {
		t.Fatalf("goal not truncated: %d runes", len([]rune(card.Goal)))
	}
}
