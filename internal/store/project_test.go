package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/normalize"
)

// writeGitRemote 在 dir 下造一个最小 .git/config，供 GitRemoteFromConfig 读取。
func writeGitRemote(t *testing.T, dir, url string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = " + url + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureProject_IdempotentKeepsStableID(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id1, err := s.EnsureProject(ctx, "github.com/foo/bar", "bar", "git@github.com:Foo/Bar.git", `D:\x\bar`, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.EnsureProject(ctx, "github.com/foo/bar", "bar", "", `E:\other\bar`, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %d vs %d", id1, id2)
	}

	p, ok, err := s.ProjectByKey(ctx, "github.com/foo/bar")
	if err != nil || !ok {
		t.Fatalf("ProjectByKey ok=%v err=%v", ok, err)
	}
	if p.GitRemote == "" {
		t.Fatal("first write should keep git_remote")
	}
	if p.PrimaryPath != `D:\x\bar` {
		t.Fatalf("primary_path overwritten: %q", p.PrimaryPath)
	}
	if p.UpdatedAt.Equal(now) {
		t.Fatal("updated_at should advance on second write")
	}
}

func TestEnsureProject_FillsEmptyFieldsOnLaterWrite(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id, err := s.EnsureProject(ctx, "f:/go/agent-memory", "", "", `F:\go\agent-memory`, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureProject(ctx, "f:/go/agent-memory", "agent-memory", "git@github.com:muyi99/agent-memory.git", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	p, _, err := s.ProjectByKey(ctx, "f:/go/agent-memory")
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayName != "agent-memory" || p.GitRemote == "" {
		t.Fatalf("empty fields should be filled later: %+v", p)
	}
	if p.ID != id {
		t.Fatalf("id changed: %d vs %d", p.ID, id)
	}
}

func TestLinkProjectPathAndResolveByPath(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id, err := s.EnsureProject(ctx, "f:/go/hiprint", "hiprint", "", `F:\go-hiprint`, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, id, "qoder", "F--go-hiprint", `F:\go-hiprint`, "high"); err != nil {
		t.Fatal(err)
	}
	// 第二次链接同一项目、另一 agent 的写法：路径归一化后必须解析到同一 id
	if err := s.LinkProjectPath(ctx, id, "workbuddy", "", `f:/go-hiprint/`, "high"); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.ResolveProjectByPath(ctx, `F:\go-hiprint`)
	if err != nil || !ok {
		t.Fatalf("resolve ok=%v err=%v", ok, err)
	}
	if got != id {
		t.Fatalf("got project %d want %d", got, id)
	}

	missing, ok, err := s.ResolveProjectByPath(ctx, `Z:\nowhere`)
	if err != nil {
		t.Fatal(err)
	}
	if ok || missing != 0 {
		t.Fatalf("want (0,false), got (%d,%v)", missing, ok)
	}
}

func TestLinkProjectPath_DeduplicatesSameAgentPath(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	id, _ := s.EnsureProject(ctx, "e:/go/deploy", "", "", `E:\go-deploy`, now)

	key := normalize.ProjectKey("", filepath.FromSlash("E:/go-deploy"))
	if err := s.LinkProjectPath(ctx, id, "qoder", "E--go-deploy", `E:\go-deploy`, "high"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, id, "qoder", "E--go-deploy", key, "low"); err != nil {
		t.Fatal(err)
	}
	// 再次以 low + 空 slug 登记：既不能降级置信度，也不能把已登记的 slug 清空。
	if err := s.LinkProjectPath(ctx, id, "qoder", "", key, "low"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_path`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 row after normalized dedupe, got %d", n)
	}

	// low 置信度登记不得降级已存在的 high，也不得清空已登记的 slug。
	var gotConf, gotSlug string
	if err := s.db.QueryRow(`SELECT confidence, slug FROM project_path`).Scan(&gotConf, &gotSlug); err != nil {
		t.Fatal(err)
	}
	if gotConf != "high" {
		t.Fatalf("confidence downgraded to %q; low must never overwrite high", gotConf)
	}
	if gotSlug != "E--go-deploy" {
		t.Fatalf("slug cleared or overwritten: %q", gotSlug)
	}
}

// TestResolveProjectByPath_PrefersLatestLinkForSamePath 覆盖同一物理路径先后绑定
// 两个项目的情形：project_path 会有两行同 path、不同 project_id，解析必须确定性地
// 取最新登记（id 最大）的一行，而不是依赖扫描顺序。
func TestResolveProjectByPath_PrefersLatestLinkForSamePath(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	idA, err := s.EnsureProject(ctx, "e:/go/deploy", "deploy", "", `E:\go-deploy`, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, idA, "qoder", "E--go-deploy", `E:\go-deploy`, "high"); err != nil {
		t.Fatal(err)
	}

	// 项目 B 用更早的 at 登记（乱序扫描/回放），但登记顺序在后，id 更大。
	idB, err := s.EnsureProject(ctx, "e:/go/deploy-fork", "deploy-fork", "", `E:\go-deploy`, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if idB <= idA {
		t.Fatalf("project B id %d should be greater than A id %d", idB, idA)
	}
	if err := s.LinkProjectPath(ctx, idB, "qoder", "E--go-deploy", `E:\go-deploy`, "high"); err != nil {
		t.Fatal(err)
	}

	var rows int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_path WHERE path = ?`, normalize.ProjectKey("", `E:\go-deploy`)).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("want 2 rows for the same path (one per project), got %d", rows)
	}

	got, ok, err := s.ResolveProjectByPath(ctx, `E:\go-deploy`)
	if err != nil || !ok {
		t.Fatalf("resolve ok=%v err=%v", ok, err)
	}
	if got != idB {
		t.Fatalf("resolved project %d, want latest registration %d (A=%d)", got, idB, idA)
	}
}

// TestResolveOrCreateProject_PrefersExistingPathMapping 覆盖「同路径已有映射」：
// 即使目录里有 remote，已登记的映射也必须胜出，否则同一路径会被拆成两个项目。
func TestResolveOrCreateProject_PrefersExistingPathMapping(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	dir := filepath.Join(t.TempDir(), "repo")
	writeGitRemote(t, dir, "git@github.com:foo/bar.git")

	legacy, err := s.EnsureProject(ctx, "f:/legacy/repo", "repo", "", dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, legacy, "qoder", "F--legacy-repo", dir, "high"); err != nil {
		t.Fatal(err)
	}

	id, key, err := s.ResolveOrCreateProject(ctx, "mcp", "", dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if id != legacy || key != "f:/legacy/repo" {
		t.Fatalf("got (%d,%q), want existing mapping (%d,%q)", id, key, legacy, "f:/legacy/repo")
	}
	if n, err := s.QueryInt(ctx, `SELECT count(*) FROM project`); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("project rows = %d, want 1 (existing mapping must win)", n)
	}
}

// TestResolveOrCreateProject_UsesGitRemoteWhenUnmapped 覆盖「未映射但有 remote」：
// 新建项目必须按 remote 取 key，这样另一份 checkout 也会并进同一个项目。
func TestResolveOrCreateProject_UsesGitRemoteWhenUnmapped(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	dir := filepath.Join(t.TempDir(), "checkout-a")
	writeGitRemote(t, dir, "git@github.com:Foo/Bar.git")

	id, key, err := s.ResolveOrCreateProject(ctx, "mcp", "bar", dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if key != "github.com/foo/bar" {
		t.Fatalf("key = %q, want github.com/foo/bar", key)
	}
	p, ok, err := s.ProjectByKey(ctx, key)
	if err != nil || !ok {
		t.Fatalf("ProjectByKey ok=%v err=%v", ok, err)
	}
	if p.ID != id || p.GitRemote == "" {
		t.Fatalf("project not keyed by remote: %+v", p)
	}

	// 幂等：同一路径再解析一次不得新建。
	if again, _, err := s.ResolveOrCreateProject(ctx, "mcp", "bar", dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	} else if again != id {
		t.Fatalf("second resolve got %d, want %d", again, id)
	}

	// 同 remote 的另一份 checkout 也必须并进来。
	other := filepath.Join(t.TempDir(), "checkout-b")
	writeGitRemote(t, other, "https://github.com/foo/bar.git")
	if otherID, otherKey, err := s.ResolveOrCreateProject(ctx, "cli", "", other, now); err != nil {
		t.Fatal(err)
	} else if otherID != id || otherKey != key {
		t.Fatalf("second checkout got (%d,%q), want (%d,%q)", otherID, otherKey, id, key)
	}
	if n, err := s.QueryInt(ctx, `SELECT count(*) FROM project`); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("project rows = %d, want 1 (same remote must merge)", n)
	}
}

// TestListProjectDirs_ReturnsHighConfidenceInRegistrationOrder 回归 I4：
// 同一 remote 合并后项目有多份 checkout，简报必须能拿到每一份的路径。
// 只返回 high 置信度、去重，并按登记顺序（id）排列，保证写盘顺序稳定。
func TestListProjectDirs_ReturnsHighConfidenceInRegistrationOrder(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id, err := s.EnsureProject(ctx, "github.com/foo/bar", "bar", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	// 故意先登记 b 再登记 a：结果必须按登记顺序，而不是字典序。
	if err := s.LinkProjectPath(ctx, id, "qoder", "b", `F:\work\repo-b`, "high"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, id, "qoder", "a", `F:\work\repo-a`, "high"); err != nil {
		t.Fatal(err)
	}
	// 同一路径换 agent 重复登记：归一化后相同，只能出现一次。
	if err := s.LinkProjectPath(ctx, id, "claude", "", `f:/work/repo-b/`, "high"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, id, "workbuddy", "", `F:\work\low`, "low"); err != nil {
		t.Fatal(err)
	}
	other, err := s.EnsureProject(ctx, "github.com/foo/other", "other", "", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, other, "qoder", "o", `F:\work\elsewhere`, "high"); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListProjectDirs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"f:/work/repo-b", "f:/work/repo-a"}
	if !slices.Equal(got, want) {
		t.Fatalf("ListProjectDirs = %q, want %q", got, want)
	}
}

// TestResolveOrCreateProject_FallsBackToPathKey 覆盖「两者都没有」：
// 读不到 remote 时退化为路径 key，git_remote 留空，不报错。
func TestResolveOrCreateProject_FallsBackToPathKey(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	dir := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	id, key, err := s.ResolveOrCreateProject(ctx, "cli", "", dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := normalize.ProjectKey("", dir); key != want {
		t.Fatalf("key = %q, want path key %q", key, want)
	}
	p, ok, err := s.ProjectByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ProjectByID ok=%v err=%v", ok, err)
	}
	if p.GitRemote != "" {
		t.Fatalf("git_remote = %q, want empty", p.GitRemote)
	}
	if n, err := s.QueryInt(ctx, `SELECT count(*) FROM project`); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("project rows = %d, want 1", n)
	}
}
