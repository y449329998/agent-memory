package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func seedSession(t *testing.T, s *Store, external string, projectID int64, updated time.Time) int64 {
	t.Helper()
	id, err := s.UpsertSession(context.Background(), domain.Session{
		Agent: "qoder", ExternalID: external, ProjectID: projectID,
		Title: external, StartedAt: updated.Add(-time.Hour), UpdatedAt: updated,
		EventCount: 3, Path: "/q/" + external + ".jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUpsertTaskCard_OneCardPerSessionReplaceable(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	sid := seedSession(t, s, "s1", 3, now)

	if _, err := s.TaskCardBySession(ctx, sid); err == nil {
		t.Fatal("missing task card must return an error")
	}

	card := domain.TaskCard{SessionID: sid, ProjectID: 3, Goal: "加乐观锁", Status: domain.StatusDoing,
		Files: []string{`F:\a.go`}, Commands: []string{"go test"}, GeneratedBy: "rule", Confidence: "rule"}
	id, err := s.UpsertTaskCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}
	card.ID = id
	card.Status = domain.StatusDone
	card.NextSteps = "无"
	id2, err := s.UpsertTaskCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}
	if id != id2 {
		t.Fatalf("same session must reuse card: %d vs %d", id, id2)
	}

	var n int
	n, err = s.QueryInt(ctx, `SELECT count(*) FROM task_card`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 card, got %d", n)
	}

	got, err := s.TaskCardBySession(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusDone || len(got.Files) != 1 || got.Files[0] != `F:\a.go` {
		t.Fatalf("round trip wrong: %+v", got)
	}
	if got.Goal != "加乐观锁" || got.Commands[0] != "go test" {
		t.Fatalf("round trip wrong: %+v", got)
	}
}

func TestListTaskCards_OrdersBySessionRecency(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	base := time.Unix(1700000000, 0)
	old := seedSession(t, s, "old", 3, base)
	fresh := seedSession(t, s, "fresh", 3, base.Add(time.Hour))
	other := seedSession(t, s, "other", 9, base.Add(2*time.Hour))

	for _, pair := range []struct {
		sid    int64
		goal   string
		status string
	}{
		{old, "旧任务", domain.StatusDone},
		{fresh, "新任务", domain.StatusBlocked},
		{other, "别的项目", domain.StatusDoing},
	} {
		if _, err := s.UpsertTaskCard(ctx, domain.TaskCard{SessionID: pair.sid, ProjectID: 0,
			Goal: pair.goal, Status: pair.status, GeneratedBy: "rule"}); err != nil {
			t.Fatal(err)
		}
	}
	// ProjectID 在卡上冗余存一份，这里按会话归属重算
	if _, err := s.db.Exec(`UPDATE task_card SET project_id = (SELECT project_id FROM session WHERE session.id = task_card.session_id)`); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListTaskCards(ctx, 3, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 cards for project 3, got %d", len(got))
	}
	if got[0].Goal != "新任务" || got[1].Goal != "旧任务" {
		t.Fatalf("order wrong, got %q then %q", got[0].Goal, got[1].Goal)
	}

	blocked, err := s.ListTaskCards(ctx, 3, domain.StatusBlocked, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked) != 1 || blocked[0].Status != domain.StatusBlocked {
		t.Fatalf("status filter leaked: %+v", blocked)
	}
}

func TestListTaskCards_CarriesSessionTitle(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	sid := seedSession(t, s, "s9", 3, now)
	if _, err := s.UpsertTaskCard(ctx, domain.TaskCard{SessionID: sid, ProjectID: 3, Goal: "g", Status: domain.StatusDoing}); err != nil {
		t.Fatal(err)
	}
	// 空切片必须落成 []，不能是 null。
	var filesJSON, commandsJSON string
	if err := s.db.QueryRow(`SELECT files_json, commands_json FROM task_card WHERE session_id = ?`, sid).Scan(&filesJSON, &commandsJSON); err != nil {
		t.Fatal(err)
	}
	if filesJSON != "[]" || commandsJSON != "[]" {
		t.Fatalf("empty lists stored as files_json=%q commands_json=%q, want []", filesJSON, commandsJSON)
	}
	cards, err := s.ListTaskCards(ctx, 3, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || !strings.Contains(cards[0].Goal, "g") {
		t.Fatalf("cards = %+v", cards)
	}
	sess, ok, err := s.SessionByID(ctx, cards[0].SessionID)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if sess.Title != "s9" {
		t.Fatalf("title = %q", sess.Title)
	}
}
