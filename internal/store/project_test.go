package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/normalize"
)

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
