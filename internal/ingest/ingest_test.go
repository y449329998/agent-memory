package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/agent/qoder"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeQoder 造一个与真实布局同构的假 home：
// 一条用户记忆 + 一个索引文件（应跳过）+ 两个各自带会话 JSONL 的项目。
func fakeQoder(t *testing.T) (home string, repoA string, repoB string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, ".qoder-cn")
	repoA = filepath.Join(root, "repos", "alpha")
	repoB = filepath.Join(root, "repos", "beta")
	write(t, filepath.Join(home, "memory", "user.md"), "---\nmetadata:\n  type: user\n---\n\n沟通用中文。\n")
	write(t, filepath.Join(home, "memory", "MEMORY.md"), "- 索引行\n")
	link(t, home, "RA", repoA, "a.jsonl", "---\nmetadata:\n  type: project\n---\n\nalpha 项目状态。\n")
	link(t, home, "RB", repoB, "b.jsonl", "---\nmetadata:\n  type: feedback\n---\n\n余额扣减需乐观锁。\n")
	return home, repoA, repoB
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// link 建一个 Qoder 项目目录：slug 目录 + 会话 JSONL（提供真实工作目录）+ memory 子目录
func link(t *testing.T, home, slug, realDir, sessionName, memory string) {
	t.Helper()
	dir := filepath.Join(home, "projects", slug)
	write(t, filepath.Join(dir, sessionName), fmt.Sprintf("{\"type\":\"workspace-directories\",\"directories\":[%q]}\n", realDir))
	write(t, filepath.Join(dir, "memory", "m.md"), memory)
}

// dump 把幂等判定所需的库内容拼成可比较字符串：记忆行 + 来源行。
func dump(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	items, err := s.QueryStrings(ctx, `
SELECT id || '|' || scope || '|' || project_id || '|' || type || '|' || text_norm_hash || '|' || stale || '|' || first_seen_at || '|' || last_seen_at
FROM memory_item ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := s.QueryStrings(ctx, `
SELECT memory_id || '|' || agent || '|' || path
FROM memory_source ORDER BY memory_id, agent, path`)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(items, "\n") + "\n#\n" + strings.Join(sources, "\n")
}

func mustInt(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	n, err := s.QueryInt(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestRun_ImportsMemoriesAndIsIdempotent(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	r := New(s, qoder.New(home))

	first, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", first.Errors)
	}
	// 4 个 md 里有一个是 MEMORY.md 索引，应导入 3 条
	if first.Units != 3 || first.Created != 3 {
		t.Fatalf("first run = %+v, want units=3 created=3", first)
	}

	before := dump(t, s)
	second, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != 0 || second.Merged != 0 {
		t.Fatalf("unchanged sources must not touch memory rows: %+v", second)
	}
	if second.Unchanged != 3 {
		t.Fatalf("unchanged = %d, want 3", second.Unchanged)
	}
	if second.Staled != 0 {
		t.Fatalf("unchanged sources must not be marked stale: %+v", second)
	}
	if after := dump(t, s); after != before {
		t.Fatalf("database changed between identical scans:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestRun_MergesTwoCheckoutsByGitRemote(t *testing.T) {
	home, repoA, repoB := fakeQoder(t)
	// 同一个仓库的两份 checkout：路径不同，origin 相同
	for _, dir := range []string{repoA, repoB} {
		write(t, filepath.Join(dir, ".git", "config"), "[remote \"origin\"]\n\turl = git@github.com:acme/mono.git\n")
	}
	s := openTempStore(t)
	if _, err := New(s, qoder.New(home)).Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project`); got != 1 {
		t.Fatalf("same remote should merge into 1 project, got %d", got)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project_path`); got != 2 {
		t.Fatalf("want 2 path mappings, got %d", got)
	}
}

func TestRun_LowConfidenceSlugStaysItsOwnProject(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	slug := "Z--some-project"
	write(t, filepath.Join(home, "memory", "u.md"), "用户级记忆\n")
	// 没有会话 JSONL：路径只能靠 slug 反解，不可信
	write(t, filepath.Join(home, "projects", slug, "memory", "m.md"), "项目级记忆\n")

	s := openTempStore(t)
	if _, err := New(s, qoder.New(home)).Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project_path`); got != 0 {
		t.Fatalf("low-confidence path must not be registered in project_path, got %d rows", got)
	}
	keys, err := s.QueryStrings(context.Background(), `SELECT key FROM project ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 || keys[0] != "qoder:"+slug {
		t.Fatalf("project keys = %v, want first %q", keys, "qoder:"+slug)
	}
}

func TestRun_MarksStaleWhenSourceFileDisappears(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	r := New(s, qoder.New(home))
	if _, err := r.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(home, "memory", "user.md")); err != nil {
		t.Fatal(err)
	}
	third, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if third.Staled != 1 {
		t.Fatalf("staled = %d, want 1", third.Staled)
	}
	// 只统计 qoder 支撑的过时条目，别的 agent 的不应被波及
	qoderStale := mustInt(t, s, `SELECT count(*) FROM memory_item
WHERE stale = 1 AND id IN (SELECT memory_id FROM memory_source WHERE agent = ?)`, "qoder")
	if qoderStale != 1 {
		t.Fatalf("qoder-sourced stale count = %d, want 1", qoderStale)
	}
}

func TestRun_OnlyAgentFilter(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	res, err := New(s, qoder.New(home)).Run(context.Background(), "trae")
	if err != nil {
		t.Fatal(err)
	}
	if res.Units != 0 {
		t.Fatalf("filtering to trae should import nothing, got %d units", res.Units)
	}
}

func TestRun_ContextCancelledStopsBeforeWriting(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(s, qoder.New(home)).Run(ctx, ""); err == nil {
		t.Fatal("want error from cancelled context")
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project_path`); got != 0 {
		t.Fatalf("cancelled run wrote state: %d project_path rows", got)
	}
}

// faultyAdapter 在正常扫描结果后追加一个读失败的源。
// 适配器会把目录当成非记忆文件跳过，无法用「建一个 bad.md 目录」制造 ReadFile 失败，
// 所以这里显式注入一个带 Err 的 unit 来验证「单源失败不中断」。
type faultyAdapter struct {
	inner agent.Adapter
	path  string
}

func (a faultyAdapter) Name() string { return a.inner.Name() }

func (a faultyAdapter) ScanMemoryUnits(ctx context.Context) ([]agent.MemoryUnit, error) {
	units, err := a.inner.ScanMemoryUnits(ctx)
	return append(units, agent.MemoryUnit{Path: a.path, Err: errors.New("read: boom")}), err
}

func (a faultyAdapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	return a.inner.ScanSessionUnits(ctx)
}

func TestRun_RecordsSourceErrorAndKeepsOthers(t *testing.T) {
	home, _, _ := fakeQoder(t)
	bad := filepath.Join(home, "projects", "RA", "memory", "bad.md")
	s := openTempStore(t)
	res, err := New(s, faultyAdapter{inner: qoder.New(home), path: bad}).Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("want a recorded error for the unreadable source")
	}
	if res.Created != 3 {
		t.Fatalf("healthy files should still import, created=%d want 3", res.Created)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM source WHERE last_error <> ''`); got == 0 {
		t.Fatal("source last_error not persisted")
	}
}

// badDraftAdapter 返回一个解析成功但草稿为空的 unit，用来验证 UpsertMemory 失败时
// 不能推进源哈希——否则下一轮会判 Unchanged，这条记忆永远不再重试。
type badDraftAdapter struct{}

func (badDraftAdapter) Name() string { return "qoder" }

func (badDraftAdapter) ScanMemoryUnits(ctx context.Context) ([]agent.MemoryUnit, error) {
	const path = "/virtual/empty.md"
	return []agent.MemoryUnit{{
		Path:    path,
		Content: []byte("x"),
		Drafts: []domain.MemoryDraft{{
			Scope:       domain.ScopeUser,
			Type:        domain.TypeFact,
			Text:        "   ",
			SourceAgent: "qoder",
			SourcePath:  path,
		}},
	}}, nil
}

func (badDraftAdapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	return nil, nil
}

func TestRun_DraftFailureDoesNotAdvanceSourceHash(t *testing.T) {
	s := openTempStore(t)
	r := New(s, badDraftAdapter{})
	for i := 0; i < 2; i++ {
		res, err := r.Run(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Errors) == 0 {
			t.Fatalf("run %d: empty draft must be reported as an error", i+1)
		}
		if res.Created != 0 {
			t.Fatalf("run %d: created = %d, want 0", i+1, res.Created)
		}
		if _, seen, err := s.SourceHash(context.Background(), "qoder", "memory", "/virtual/empty.md"); err != nil {
			t.Fatal(err)
		} else if seen {
			t.Fatalf("run %d: source hash advanced despite upsert failure; next run would skip the retry", i+1)
		}
	}
}

// TestRun_TimestampsUseWallClockNotRunMarker 回归轮次标记与时间戳混用：
// 同一秒内连跑 3 轮，runMarker 会给到 now+1、now+2，但 first_seen_at / changed_at
// 必须来自墙钟，不得落在未来；last_seen_run 仍须严格递增。
func TestRun_TimestampsUseWallClockNotRunMarker(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	r := New(s, qoder.New(home))
	user := filepath.Join(home, "memory", "user.md")

	var seenRuns []int
	for i := 1; i <= 3; i++ {
		// 每轮都改内容：迫使这轮写入新条目（first_seen_at）并记录 changed_at
		write(t, user, fmt.Sprintf("---\nmetadata:\n  type: user\n---\n\n第 %d 轮的墙钟内容。\n", i))
		if _, err := r.Run(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		seenRuns = append(seenRuns, mustInt(t, s,
			`SELECT last_seen_run FROM source WHERE agent = 'qoder' AND kind = 'memory' AND path = ?`, user))
	}
	wall := time.Now().Unix()
	if seenRuns[0] >= seenRuns[1] || seenRuns[1] >= seenRuns[2] {
		t.Fatalf("last_seen_run must strictly increase, got %v", seenRuns)
	}

	firstSeen := int64(mustInt(t, s, `SELECT MAX(first_seen_at) FROM memory_item`))
	changedAt := int64(mustInt(t, s,
		`SELECT MAX(changed_at) FROM source WHERE agent = 'qoder' AND path = ?`, user))
	t.Logf("run3: last_seen_run=%d first_seen_at=%d changed_at=%d wall=%d",
		seenRuns[2], firstSeen, changedAt, wall)
	if firstSeen > wall+1 {
		t.Fatalf("first_seen_at = %d is in the future (wall=%d)", firstSeen, wall)
	}
	if changedAt > wall+1 {
		t.Fatalf("changed_at = %d is in the future (wall=%d)", changedAt, wall)
	}
}

func TestResult_StringIsReadable(t *testing.T) {
	got := Result{Units: 4, Created: 2, Merged: 1, Unchanged: 1, Staled: 0}.String()
	for _, want := range []string{"units=4", "created=2", "merged=1", "unchanged=1", "staled=0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestValidateAgent(t *testing.T) {
	if err := ValidateAgent([]string{"qoder"}, "qoder"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAgent([]string{"qoder"}, ""); err != nil {
		t.Fatal(err)
	}
	err := ValidateAgent([]string{"qoder", "trae"}, "cursor")
	if err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("want unknown-agent error, got %v", err)
	}
}

// TestRun_ImportsSessionsAndCardsWhenSettled 覆盖「一轮扫描同时导入记忆与会话」：
// 已写完的会话出任务卡，还在写的只导入会话与事件。
func TestRun_ImportsSessionsAndCardsWhenSettled(t *testing.T) {
	home, repoA, repoB := fakeQoder(t)
	// fixture 自带两个只含 workspace 行的占位会话，这里换成带事件的真会话。
	write(t, filepath.Join(home, "projects", "RA", "a.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repoA)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"给扣减加乐观锁\"}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:01:00Z\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"name\":\"Edit\",\"input\":{\"file_path\":\"F:\\\\a.go\"}}]}}\n")
	write(t, filepath.Join(home, "projects", "RB", "b.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s2\",\"directories\":[%q]}\n", repoB)+
			"{\"type\":\"user\",\"sessionId\":\"s2\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"还在写的会话\"}}\n")

	// 「已经写完」用文件 mtime 表达：RA 推到 1 小时前，RB 保持当前时间。
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(home, "projects", "RA", "a.jsonl"), settled, settled); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now()
	if err := os.Chtimes(filepath.Join(home, "projects", "RB", "b.jsonl"), fresh, fresh); err != nil {
		t.Fatal(err)
	}

	s := openTempStore(t)
	r := New(s, qoder.New(home))
	r.SettleIdle = 5 * time.Minute
	res, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Units != 3 || res.Created != 3 {
		t.Fatalf("memories must import in the same scan: units=%d created=%d", res.Units, res.Created)
	}
	if res.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", res.Sessions)
	}
	if res.Cards != 1 {
		t.Fatalf("cards = %d, want 1（未写完的会话不该出卡）", res.Cards)
	}
	if res.SkippedUnsettled != 1 {
		t.Fatalf("skipped = %d, want 1", res.SkippedUnsettled)
	}
	t.Logf("first scan: %s", res.String())
	t.Logf("task_card rows = %d", mustInt(t, s, `SELECT count(*) FROM task_card`))

	goal, err := s.QueryStrings(context.Background(), `SELECT goal FROM task_card`)
	if err != nil {
		t.Fatal(err)
	}
	if len(goal) != 1 || goal[0] != "给扣减加乐观锁" {
		t.Fatalf("goals = %v", goal)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM session_event WHERE kind = 'tool_edit'`); got != 1 {
		t.Fatalf("edit events = %d, want 1", got)
	}

	// 第二次扫描：内容没变的会话整批跳过，卡片不重复产出，事件也不重写。
	second, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionUnchanged != 2 || second.Cards != 0 {
		t.Fatalf("second run = %+v, want unchanged sessions and no new cards", second)
	}
	if second.SkippedUnsettled != 0 {
		t.Fatalf("unchanged sessions must not reach the settle check: %+v", second)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM session_event`); got != 3 {
		t.Fatalf("events rewritten on unchanged scan: %d rows, want 3", got)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM task_card`); got != 1 {
		t.Fatalf("task cards duplicated: %d rows, want 1", got)
	}
	t.Logf("second scan: %s", second.String())
}

// TestRun_SessionProjectMatchesMemoryProject 验证会话与记忆共用同一条归属逻辑：
// 同一个项目目录下的两份源必须落到同一个 project_id。
func TestRun_SessionProjectMatchesMemoryProject(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	repo := filepath.Join(root, "repos", "alpha")
	write(t, filepath.Join(home, "projects", "RA", "memory", "m.md"),
		"---\nmetadata:\n  type: project\n---\n\nalpha 项目状态。\n")
	write(t, filepath.Join(home, "projects", "RA", "s1.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repo)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"目标\"}}\n")
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(home, "projects", "RA", "s1.jsonl"), settled, settled); err != nil {
		t.Fatal(err)
	}

	s := openTempStore(t)
	if _, err := New(s, qoder.New(home)).Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QueryStrings(context.Background(), `
SELECT DISTINCT (SELECT key FROM project WHERE id = m.project_id)
FROM memory_item m WHERE m.scope = 'project' AND m.project_id <> 0`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("project keys from memory = %v, want exactly one", rows)
	}
	shared, err := s.QueryStrings(context.Background(), `
SELECT DISTINCT s.project_id FROM session s
JOIN memory_item m ON m.project_id = s.project_id
WHERE m.scope = 'project' AND m.project_id <> 0`)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 1 {
		t.Fatalf("session and memory must share a project, got %v", shared)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM session WHERE project_id = 0`); got != 0 {
		t.Fatalf("%d sessions left without a project", got)
	}
	sessPID := mustInt(t, s, `SELECT project_id FROM session WHERE external_id = 's1'`)
	memPID := mustInt(t, s, `SELECT DISTINCT project_id FROM memory_item WHERE scope = 'project' AND project_id <> 0`)
	t.Logf("memory project keys=%v session project_id=%d memory project_id=%d same=%v",
		rows, sessPID, memPID, sessPID == memPID)
}

func TestRun_SessionSourceErrorIsRecorded(t *testing.T) {
	home, _, _ := fakeQoder(t)
	// 无 workspace-directories 的会话：适配器给出带 Err 的单元，ingest 记错但不中断
	write(t, filepath.Join(home, "projects", "RA", "orphan.jsonl"),
		"{\"type\":\"user\",\"sessionId\":\"o\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"x\"}}\n")
	s := openTempStore(t)
	res, err := New(s, qoder.New(home)).Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions < 1 || len(res.Errors) == 0 {
		t.Fatalf("want a recorded session error: %+v", res)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM source WHERE kind = 'session' AND last_error <> ''`); got == 0 {
		t.Fatal("session error not persisted")
	}
	// 坏源不能挡住同目录与其他目录的健康会话
	if got := mustInt(t, s, `SELECT count(*) FROM session`); got != 2 {
		t.Fatalf("healthy sessions = %d, want 2", got)
	}
}

func TestResult_StringCoversSessionCounts(t *testing.T) {
	got := Result{Units: 1, Created: 1, Sessions: 2, SessionUnchanged: 3, Cards: 1, SkippedUnsettled: 1}.String()
	for _, want := range []string{"sessions=2", "sessions_unchanged=3", "cards=1", "unsettled=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

// TestRun_UnchangedSettledSessionGetsCardOnLaterScan 回归 Critical 缺陷：
// 会话首次被扫到时还在写（mtime 接近现在），只导入事件不出卡；等它静默后
// 内容不再变化，哈希未变的后续扫描必须补出任务卡，而不是永远跳过。
func TestRun_UnchangedSettledSessionGetsCardOnLaterScan(t *testing.T) {
	home, repoA, _ := fakeQoder(t)
	path := filepath.Join(home, "projects", "RA", "a.jsonl")
	write(t, path,
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repoA)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"给扣减加乐观锁\"}}\n")
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}

	s := openTempStore(t)
	r := New(s, qoder.New(home))
	r.SettleIdle = 5 * time.Minute

	first, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Cards != 0 {
		t.Fatalf("first scan: cards = %d, want 0（还在写的会话不该出卡）", first.Cards)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM task_card`); got != 0 {
		t.Fatalf("first scan created %d task cards, want 0", got)
	}
	t.Logf("first scan: %s", first.String())

	// 内容一字未改，只把 mtime 推回 1 小时前：文件已静默，应补卡。
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, settled, settled); err != nil {
		t.Fatal(err)
	}
	second, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionUnchanged < 1 {
		t.Fatalf("second scan: sessions_unchanged = %d, want >= 1", second.SessionUnchanged)
	}
	if second.Cards != 1 {
		t.Fatalf("second scan: cards = %d, want 1（静默后的未变会话应补出任务卡）", second.Cards)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM task_card`); got != 1 {
		t.Fatalf("second scan: task_card rows = %d, want 1", got)
	}
	t.Logf("second scan: %s", second.String())
	t.Logf("task_card rows after second scan = %d", mustInt(t, s, `SELECT count(*) FROM task_card`))

	third, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if third.Cards != 0 {
		t.Fatalf("third scan: cards = %d, want 0（已有卡不该重复产出）", third.Cards)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM task_card`); got != 1 {
		t.Fatalf("third scan: task_card rows = %d, want 1", got)
	}
	t.Logf("third scan: %s", third.String())
	t.Logf("task_card rows after third scan = %d", mustInt(t, s, `SELECT count(*) FROM task_card`))
}

// TestRun_ExistingCardIsNotOverwritten 钉住补卡路径的边界：已有卡时不能重写，
// 否则人工或后续 LLM 对 goal / next_steps 的修订会被规则结果冲掉。
func TestRun_ExistingCardIsNotOverwritten(t *testing.T) {
	home, repoA, _ := fakeQoder(t)
	path := filepath.Join(home, "projects", "RA", "a.jsonl")
	write(t, path,
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", repoA)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"原始目标\"}}\n")
	settled := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, settled, settled); err != nil {
		t.Fatal(err)
	}

	s := openTempStore(t)
	r := New(s, qoder.New(home))
	first, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Cards != 1 {
		t.Fatalf("first scan: cards = %d, want 1", first.Cards)
	}

	// 模拟人工/LLM 修订：改 goal 并把 updated_at 固定在一个可辨识的过去时刻。
	ctx := context.Background()
	sessID := int64(mustInt(t, s, `SELECT id FROM session WHERE agent = 'qoder' AND external_id = 's1'`))
	projectID := int64(mustInt(t, s, `SELECT project_id FROM session WHERE id = ?`, sessID))
	customAt := time.Unix(1700000000, 0)
	if _, err := s.UpsertTaskCard(ctx, domain.TaskCard{
		SessionID: sessID, ProjectID: projectID, Goal: "人工修订后的目标",
		Status: domain.StatusDoing, Confidence: "manual", GeneratedBy: "manual",
		CreatedAt: customAt, UpdatedAt: customAt,
	}); err != nil {
		t.Fatal(err)
	}

	// 内容仍然未变：这一轮会走未变分支，但已有卡必须原样保留。
	second, err := r.Run(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.SessionUnchanged < 1 {
		t.Fatalf("second scan: sessions_unchanged = %d, want >= 1", second.SessionUnchanged)
	}
	if second.Cards != 0 {
		t.Fatalf("second scan: cards = %d, want 0（已有卡不该被重写）", second.Cards)
	}
	goal, err := s.QueryStrings(ctx, `SELECT goal FROM task_card WHERE session_id = ?`, sessID)
	if err != nil {
		t.Fatal(err)
	}
	if len(goal) != 1 || goal[0] != "人工修订后的目标" {
		t.Fatalf("goal = %v, want 人工修订后的目标", goal)
	}
	if got := int64(mustInt(t, s, `SELECT updated_at FROM task_card WHERE session_id = ?`, sessID)); got != customAt.Unix() {
		t.Fatalf("updated_at = %d, want %d（已有卡被改写）", got, customAt.Unix())
	}
	t.Logf("second scan: %s", second.String())
}
