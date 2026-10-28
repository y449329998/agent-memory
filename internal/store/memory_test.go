package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// rowCount 用通用原语跑一条测试专用聚合 SQL，不为统计单独在生产代码里加方法。
func rowCount(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	n, err := s.QueryInt(context.Background(), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

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

// assertStale 是三个 stale 场景共用的断言：直接查库，避免依赖 MemoryByID 的额外查询。
func assertStale(t *testing.T, s *Store, id int64, want bool) {
	t.Helper()
	got := rowCount(t, s, `SELECT stale FROM memory_item WHERE id = ?`, id) != 0
	if got != want {
		t.Fatalf("memory %d stale=%v want %v", id, got, want)
	}
}

// TestMarkStaleForRun_StalesSingleSourceItemThatDisappeared 场景一：
// 条目只有一个来源且本轮没再见到 → 过时；其他 agent 的条目不受波及。
func TestMarkStaleForRun_StalesSingleSourceItemThatDisappeared(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)

	kept, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "保持存在的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	gone, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/gone.md`, "只在消失文件里出现的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.UpsertMemory(ctx, 0, draft("trae", `/t/x.md`, "属于 trae 的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.SourceState{
		{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"},
		{Agent: "qoder", Kind: "memory", Path: "/q/gone.md", ContentHash: "h2"},
		{Agent: "trae", Kind: "memory", Path: "/t/x.md", ContentHash: "h3"},
	} {
		if err := s.PutSource(ctx, st, run1.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	// 第二轮只见到 a.md
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, run2.Unix()); err != nil {
		t.Fatal(err)
	}
	staled, err := s.MarkStaleForRun(ctx, []string{"qoder"}, run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 1 {
		t.Fatalf("want 1 staled, got %d", staled)
	}
	assertStale(t, s, kept, false)
	assertStale(t, s, other, false)
	assertStale(t, s, gone, true)

	// 重新出现应撤销 stale
	if _, created, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/gone.md`, "只在消失文件里出现的条目"), run2); err != nil || created {
		t.Fatalf("reappear should merge, created=%v err=%v", created, err)
	}
	assertStale(t, s, gone, false)
}

// TestMarkStaleForRun_KeepsItemWhenAnotherSourceOfSameAgentWasSeen 场景二：
// 同一 agent 的两个源支撑同一条目，只有一个源本轮见过 → 保持新鲜。
func TestMarkStaleForRun_KeepsItemWhenAnotherSourceOfSameAgentWasSeen(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)

	id, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "两个文件都写了的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	if merged, created, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/b.md`, "两个文件都写了的条目"), run1); err != nil || created || merged != id {
		t.Fatalf("same agent second source should merge: id=%d created=%v err=%v", merged, created, err)
	}
	for _, p := range []string{"/q/a.md", "/q/b.md"} {
		if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: p, ContentHash: "h"}, run1.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	// b.md 消失，只剩 a.md 露面
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h"}, run2.Unix()); err != nil {
		t.Fatal(err)
	}
	staled, err := s.MarkStaleForRun(ctx, []string{"qoder"}, run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 0 {
		t.Fatalf("want 0 staled while one source still seen, got %d", staled)
	}
	assertStale(t, s, id, false)
}

// TestMarkStaleForRun_LeavesSharedItemAloneWhenUnscannedAgentStillSupports 场景三：
// 跨 agent 共同支撑的条目，只扫了一个 agent（且它的文件消失）时不能被判过时；
// 来源全属于未扫描 agent 的条目同样不动。
func TestMarkStaleForRun_LeavesSharedItemAloneWhenUnscannedAgentStillSupports(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)

	shared, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "两个 agent 都记着的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	if merged, created, err := s.UpsertMemory(ctx, 0, draft("trae", `/t/a.md`, "两个 agent 都记着的条目"), run1); err != nil || created || merged != shared {
		t.Fatalf("cross-agent same text should merge: id=%d created=%v err=%v", merged, created, err)
	}
	traeOnly, _, err := s.UpsertMemory(ctx, 0, draft("trae", `/t/b.md`, "只属于 trae 的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	qoderSeen, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/seen.md`, "本轮仍然存在的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	qoderGone, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/gone.md`, "qoder 独有且已消失的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.SourceState{
		{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"},
		{Agent: "trae", Kind: "memory", Path: "/t/a.md", ContentHash: "h2"},
		{Agent: "trae", Kind: "memory", Path: "/t/b.md", ContentHash: "h3"},
		{Agent: "qoder", Kind: "memory", Path: "/q/seen.md", ContentHash: "h4"},
		{Agent: "qoder", Kind: "memory", Path: "/q/gone.md", ContentHash: "h5"},
	} {
		if err := s.PutSource(ctx, st, run1.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	// 只扫 qoder：seen.md 露面，a.md 消失，trae 的两个源完全没被扫过
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/seen.md", ContentHash: "h4"}, run2.Unix()); err != nil {
		t.Fatal(err)
	}
	staled, err := s.MarkStaleForRun(ctx, []string{"qoder"}, run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 1 {
		t.Fatalf("want only the qoder-only disappeared item staled, got %d", staled)
	}
	assertStale(t, s, shared, false)
	assertStale(t, s, traeOnly, false)
	assertStale(t, s, qoderSeen, false)
	assertStale(t, s, qoderGone, true)

	// 两个 agent 都扫、且 trae 仍见到 a.md：共同支撑的条目依然新鲜
	for _, p := range []string{"/t/a.md", "/t/b.md"} {
		if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: p, ContentHash: "h2"}, run2.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	staled, err = s.MarkStaleForRun(ctx, []string{"qoder", "trae"}, run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 0 {
		t.Fatalf("want 0 staled when trae still supports the shared item, got %d", staled)
	}
	assertStale(t, s, shared, false)

	// agents 为空时什么也不做
	if staled, err := s.MarkStaleForRun(ctx, nil, run2.Unix()); err != nil || staled != 0 {
		t.Fatalf("empty agents: staled=%d err=%v", staled, err)
	}
}

// TestUpsertMemory_ConcurrentAcrossTwoStores 锁住跨进程并发写：两个 Store 各持一条连接
// 打开同一个库（等价于 daemon 在扫、另一个 CLI 在写），8 个 goroutine 写同一条文本。
//
// 单连接场景被连接池天然串行化，抓不到问题；deferred 事务「先读后写」时，另一个进程
// 已经拿到写锁，升级事务会直接 SQLITE_BUSY（busy_timeout 对快照升级无效），
// 因此 DSN 必须用 _txlock=immediate 让 BEGIN 就排队拿写锁。
func TestUpsertMemory_ConcurrentAcrossTwoStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	stores := make([]*Store, 2)
	for i := range stores {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open store #%d: %v", i, err)
		}
		t.Cleanup(func() { s.Close() })
		stores[i] = s
	}
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	const perStore = 4
	const n = 2 * perStore

	var wg sync.WaitGroup
	ids := make([]int64, n)
	created := make([]bool, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], created[i], errs[i] = stores[i%2].UpsertMemory(ctx, 0,
				draft(fmt.Sprintf("agent%d", i), fmt.Sprintf("/m/%d.md", i), "两个连接并发写入的同一条文本"), now)
		}(i)
	}
	wg.Wait()

	winners := 0
	failed := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d (store %d): %v", i, i%2, errs[i])
			failed++
			continue
		}
		if ids[i] != ids[0] {
			t.Errorf("goroutine %d got id %d, want %d", i, ids[i], ids[0])
		}
		if created[i] {
			winners++
		}
	}
	if failed > 0 {
		t.Fatalf("%d/%d concurrent upserts failed", failed, n)
	}
	if winners != 1 {
		t.Fatalf("exactly one upsert should create the row, got %d", winners)
	}
	if got := rowCount(t, stores[0], `SELECT count(*) FROM memory_item`); got != 1 {
		t.Fatalf("memory_item = %d, want 1", got)
	}
	if got := rowCount(t, stores[0], `SELECT count(*) FROM memory_source WHERE memory_id = ?`, ids[0]); got != n {
		t.Fatalf("memory_source = %d, want %d", got, n)
	}
	if got := rowCount(t, stores[0], `SELECT count(*) FROM memory_fts WHERE rowid = ?`, ids[0]); got != 1 {
		t.Fatalf("memory_fts = %d, want 1", got)
	}
}

// TestMarkStaleForRun_OlderRunMarkerDoesNotHideNewerTouch 回归两个进程同时扫描：
// 旧轮次（runAt 更小）收尾时，另一个进程已经用更新的轮次 touch 过源。
// 「本轮见过」必须用 >= 判定，否则旧轮次会把新轮次刚见过的源当成没见到。
func TestMarkStaleForRun_OlderRunMarkerDoesNotHideNewerTouch(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	older := time.Unix(1700000000, 0)
	newer := older.Add(time.Minute)

	id, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "两个进程同时扫描的条目"), older)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, older.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TouchSource(ctx, "qoder", "memory", "/q/a.md", newer.Unix()); err != nil {
		t.Fatal(err)
	}

	staled, err := s.MarkStaleForRun(ctx, []string{"qoder"}, older.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 0 {
		t.Fatalf("older run must not stale a source touched by a newer run, staled %d", staled)
	}
	assertStale(t, s, id, false)
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

// TestMarkStaleSuperseded_StalesItemsReplacedInSameSource 场景一：同一个文件被重写后，
// 旧文本对应的条目在新一轮不再出现，应被置为过时，避免新旧两版并存。
func TestMarkStaleSuperseded_StalesItemsReplacedInSameSource(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)

	old, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "旧版本内容"), run1)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "新版本内容"), run2)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h2"}, run2.Unix()); err != nil {
		t.Fatal(err)
	}

	staled, err := s.MarkStaleSuperseded(ctx, "qoder", "memory", "/q/a.md", []int64{fresh}, run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 1 {
		t.Fatalf("staled = %d, want 1", staled)
	}
	assertStale(t, s, old, true)
	assertStale(t, s, fresh, false)

	// keepIDs 为空时同样工作：该源剩下的旧条目全部过时。
	legacy, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "更早的遗留内容"), run1)
	if err != nil {
		t.Fatal(err)
	}
	staled, err = s.MarkStaleSuperseded(ctx, "qoder", "memory", "/q/a.md", nil, run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 2 {
		t.Fatalf("empty keepIDs: staled = %d, want 2（该源支撑的新鲜条目全部过时）", staled)
	}
	assertStale(t, s, fresh, true)
	assertStale(t, s, legacy, true)
}

// TestMarkStaleSuperseded_LeavesItemSupportedByOtherFreshSource 场景二：条目同时被
// 另一个 agent 本轮刚见过的源支撑时不能判过时——旧文件里的文本还在别处生效。
func TestMarkStaleSuperseded_LeavesItemSupportedByOtherFreshSource(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)

	shared, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "两个 agent 共同支撑的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	if merged, created, err := s.UpsertMemory(ctx, 0, draft("trae", `/t/a.md`, "两个 agent 共同支撑的条目"), run2); err != nil || created || merged != shared {
		t.Fatalf("cross-agent merge failed: id=%d created=%v err=%v", merged, created, err)
	}
	// trae 的源本轮见过（last_seen_run >= runAt），qoder 的文件本轮被重写掉了这条。
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/a.md", ContentHash: "h1"}, run2.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h2"}, run2.Unix()); err != nil {
		t.Fatal(err)
	}

	staled, err := s.MarkStaleSuperseded(ctx, "qoder", "memory", "/q/a.md", nil, run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 0 {
		t.Fatalf("staled = %d, want 0（另一个 agent 的新鲜来源仍支撑该条目）", staled)
	}
	assertStale(t, s, shared, false)
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
