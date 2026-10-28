package ingest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/agent/qoder"
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
