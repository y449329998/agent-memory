package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func draft(agent, path, text string) domain.MemoryDraft {
	return domain.MemoryDraft{
		Scope:       domain.ScopeUser,
		Type:        domain.TypePreference,
		Text:        text,
		SourceAgent: agent,
		SourcePath:  path,
		Quote:       text,
	}
}

// seedSource 只落一行 source 游标，供 stale 判定使用。
// Task 10 才会引入 Store.PutSource（含 prev_hash / changed_at 语义），
// 这里不提前实现生产代码，避免与那个任务的 TDD 步骤打架。
func seedSource(t *testing.T, s *Store, st domain.SourceState, runAt int64) {
	t.Helper()
	_, err := s.db.Exec(`
INSERT INTO source(agent, kind, path, content_hash, last_seen_run, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(agent, kind, path) DO UPDATE SET
  content_hash  = excluded.content_hash,
  last_seen_run = excluded.last_seen_run,
  updated_at    = excluded.updated_at`,
		st.Agent, st.Kind, st.Path, st.ContentHash, runAt, runAt)
	if err != nil {
		t.Fatalf("seed source %s:%s: %v", st.Agent, st.Path, err)
	}
}

func TestUpsertMemory_DedupesAcrossAgentsAndKeepsBothSources(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id1, created1, err := s.UpsertMemory(ctx, 0, draft("qoder", `/home/.qoder-cn/memory/a.md`, "Git 提交信息必须使用中文"), now)
	if err != nil || !created1 {
		t.Fatalf("first upsert id=%d created=%v err=%v", id1, created1, err)
	}

	// Trae 用了 bullet 前缀和不同空白，内容等价：应当合并成同一条
	id2, created2, err := s.UpsertMemory(ctx, 0, draft("trae", `/home/.trae-cn/memory/user_profile.md`, "-  Git 提交信息必须使用中文"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatalf("equivalent text must not create a second item")
	}
	if id1 != id2 {
		t.Fatalf("ids differ %d vs %d", id1, id2)
	}

	item, err := s.MemoryByID(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Sources) != 2 {
		t.Fatalf("want 2 sources, got %d: %+v", len(item.Sources), item.Sources)
	}
	if !item.LastSeenAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("last_seen_at not advanced: %v", item.LastSeenAt)
	}
	if item.FirstSeenAt.Equal(item.LastSeenAt) {
		t.Fatal("first_seen_at should stay at the original write")
	}
}

func TestUpsertMemory_TypeAndProjectParticipateInDedupe(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	text := "批量匹配时优先按索引关联 AI 返回结果"
	a := draft("qoder", `/a.md`, text)
	b := draft("trae", `/b.md`, text)
	b.Type = domain.TypeLesson
	if _, _, err := s.UpsertMemory(ctx, 0, a, now); err != nil {
		t.Fatal(err)
	}
	_, created, err := s.UpsertMemory(ctx, 0, b, now)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("different type should not dedupe into the same item")
	}

	c := draft("workbuddy", `/c.md`, text)
	c.Type = domain.TypeLesson
	if _, created, err := s.UpsertMemory(ctx, 42, c, now); err != nil {
		t.Fatal(err)
	} else if !created {
		t.Fatal("different project should not dedupe")
	}
}

func TestUpsertMemory_RejectsBlankText(t *testing.T) {
	s := openStore(t)
	_, _, err := s.UpsertMemory(context.Background(), 0, draft("qoder", `/a.md`, "   \n\t "), time.Unix(1, 0))
	if !errors.Is(err, domain.ErrEmptyMemory) {
		t.Fatalf("want ErrEmptyMemory, got %v", err)
	}
}

// TestUpsertMemory_KeepsDisplayCaseButDedupesCaseInsensitively 锁住展示与去重的分离：
// memory_item.text 保留原文大小写（供展示/交接简报），text_norm_hash 仍走小写归一化。
func TestUpsertMemory_KeepsDisplayCaseButDedupesCaseInsensitively(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id, created, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "Git commit messages 必须使用中文"), now)
	if err != nil || !created {
		t.Fatalf("first upsert id=%d created=%v err=%v", id, created, err)
	}
	item, err := s.MemoryByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if item.Text != "Git commit messages 必须使用中文" {
		t.Fatalf("display text lost original case: %q", item.Text)
	}

	// 仅大小写不同，去重必须合并；合并路径不改写首次写入的展示文本。
	id2, created2, err := s.UpsertMemory(ctx, 0, draft("trae", `/t/b.md`, "git COMMIT messages 必须使用中文"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if created2 || id2 != id {
		t.Fatalf("case-only difference must merge: id=%d created=%v want id=%d", id2, created2, id)
	}
	merged, err := s.MemoryByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Text != "Git commit messages 必须使用中文" {
		t.Fatalf("merge path must keep first display text, got %q", merged.Text)
	}
}

func TestUpsertMemory_WritesSegmentedFTSRow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/a.md`, "用户偏好 snake_case 命名"), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	var seg string
	if err := s.db.QueryRow(`SELECT text_seg FROM memory_fts WHERE rowid = ?`, id).Scan(&seg); err != nil {
		t.Fatal(err)
	}
	if seg == "" {
		t.Fatal("fts row missing segmented text")
	}
}

func TestMarkStaleExcept_OnlyStalesItemsWhoseAgentSourcesDisappeared(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)

	// qoder 的一条记忆，来自两个源文件
	kept, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "保持存在的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/gone.md`, "只在消失文件里出现的条目"), run1); err != nil {
		t.Fatal(err)
	}
	seedSource(t, s, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, run1.Unix())
	seedSource(t, s, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/gone.md", ContentHash: "h2"}, run1.Unix())
	// 另一 agent 单独支撑一条记忆，不应被 qoder 的扫描波及
	other, _, err := s.UpsertMemory(ctx, 0, draft("trae", `/t/x.md`, "属于 trae 的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	seedSource(t, s, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/x.md", ContentHash: "h3"}, run1.Unix())

	// 第二次扫描：a.md 仍在，gone.md 不再出现
	seedSource(t, s, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, run2.Unix())
	staled, err := s.MarkStaleExcept(ctx, "qoder", run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 1 {
		t.Fatalf("want 1 staled, got %d", staled)
	}

	assertStale := func(id int64, want bool) {
		t.Helper()
		item, err := s.MemoryByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Stale != want {
			t.Fatalf("memory %d stale=%v want %v (%s)", id, item.Stale, want, item.Text)
		}
	}
	assertStale(kept, false)
	assertStale(other, false)

	gone, err := s.db.Query(`SELECT id FROM memory_item WHERE text = '只在消失文件里出现的条目'`)
	if err != nil {
		t.Fatal(err)
	}
	var goneID int64
	for gone.Next() {
		gone.Scan(&goneID)
	}
	gone.Close()
	assertStale(goneID, true)

	// 重新出现应撤销 stale
	if _, created, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/gone.md`, "只在消失文件里出现的条目"), run2); err != nil || created {
		t.Fatalf("reappear should merge, created=%v err=%v", created, err)
	}
	assertStale(goneID, false)
}

func TestPurgeStale_RemovesFTSRowAndSources(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	kept, _, _ := s.UpsertMemory(ctx, 0, draft("qoder", `/a.md`, "长期有效"), now)
	doomed, _, _ := s.UpsertMemory(ctx, 0, draft("qoder", `/b.md`, "已过时"), now)
	if _, err := s.db.Exec(`UPDATE memory_item SET stale = 1 WHERE id = ?`, doomed); err != nil {
		t.Fatal(err)
	}

	n, err := s.PurgeStale(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if _, err := s.MemoryByID(ctx, doomed); !errors.Is(err, domain.ErrMemoryNotFound) {
		t.Fatalf("want ErrMemoryNotFound, got %v", err)
	}
	var fts, src int
	s.db.QueryRow(`SELECT count(*) FROM memory_fts WHERE rowid = ?`, doomed).Scan(&fts)
	s.db.QueryRow(`SELECT count(*) FROM memory_source WHERE memory_id = ?`, doomed).Scan(&src)
	if fts != 0 || src != 0 {
		t.Fatalf("leftover fts=%d sources=%d", fts, src)
	}
	if _, err := s.MemoryByID(ctx, kept); err != nil {
		t.Fatalf("non-stale memory removed: %v", err)
	}
}

func TestListMemories_ProjectFilterIncludesUserScope(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	userItem := draft("qoder", `/u.md`, "沟通语言用中文")
	if _, _, err := s.UpsertMemory(ctx, 0, userItem, now); err != nil {
		t.Fatal(err)
	}
	projItem := draft("qoder", `/p.md`, "该项目的扣减需乐观锁")
	projItem.Scope = domain.ScopeProject
	if _, _, err := s.UpsertMemory(ctx, 7, projItem, now); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListMemories(ctx, 0, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("project 0 listing should only see user scope, got %d", len(all))
	}
	mixed, err := s.ListMemories(ctx, 7, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed) != 2 {
		t.Fatalf("want project items + user items = 2, got %d", len(mixed))
	}
}
