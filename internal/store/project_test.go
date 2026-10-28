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
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_path`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 row after normalized dedupe, got %d", n)
	}
}
