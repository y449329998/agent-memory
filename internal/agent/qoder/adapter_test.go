package qoder

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

// buildFakeHome 造一个和真实布局同构的假 Qoder 目录。
// 返回 home，以及三个便于断言的路径：真实项目目录、项目 slug、两个记忆文件路径。
func buildFakeHome(t *testing.T) (home string, projectRealDir string, userMemoryPath string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, ".qoder-cn")
	projectRealDir = filepath.Join(root, "repo", "fresh3-api")
	mustMkdirAll(t, projectRealDir)
	mustMkdirAll(t, filepath.Join(home, "memory"))
	slug := "X--repo-fresh3-api"
	mustMkdirAll(t, filepath.Join(home, "projects", slug, "memory"))

	session := "{\"type\":\"workspace-directories\",\"sessionId\":\"a1\",\"directories\":[\"" + jsonEscape(projectRealDir) + "\"]}\n"
	writeFile(t, filepath.Join(home, "projects", slug, "a1.jsonl"), session)

	userMemory := `---
name: user-profile
description: 用户画像
metadata:
  type: user
---

用户主要维护 Laravel 后端，中文沟通。
`
	writeFile(t, filepath.Join(home, "memory", "user-profile.md"), userMemory)
	// 索引文件必须被跳过
	writeFile(t, filepath.Join(home, "memory", "MEMORY.md"), "- [用户画像](user-profile.md) — 索引\n")

	projectMemory := `---
name: initiative
description: 项目状态
metadata:
  type: project
---

M1 已批准，等实施计划。
`
	writeFile(t, filepath.Join(home, "projects", slug, "memory", "initiative.md"), projectMemory)

	return home, projectRealDir, filepath.Join(home, "memory", "user-profile.md")
}

func TestAdapter_ScanMemoryUnits(t *testing.T) {
	home, projectRealDir, _ := buildFakeHome(t)
	a := New(home)
	units, err := a.ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("want 2 units (MEMORY.md 被跳过), got %d", len(units))
	}

	var sawUser, sawProject bool
	for _, u := range units {
		if u.Err != nil {
			t.Fatalf("unexpected unit error for %s: %v", u.Path, u.Err)
		}
		if len(u.Drafts) != 1 {
			t.Fatalf("%s: want 1 draft, got %d", u.Path, len(u.Drafts))
		}
		if u.Size != int64(len(u.Content)) || u.MTime.IsZero() {
			t.Fatalf("%s: size/mtime not populated", u.Path)
		}
		d := u.Drafts[0]
		if d.SourceAgent != "qoder" {
			t.Fatalf("agent = %q", d.SourceAgent)
		}
		switch d.Scope {
		case domain.ScopeUser:
			sawUser = true
			if d.Type != domain.TypePreference || d.ProjectPath != "" {
				t.Fatalf("user draft wrong: %+v", d)
			}
		case domain.ScopeProject:
			sawProject = true
			if d.Type != domain.TypeFact {
				t.Fatalf("project draft type = %q", d.Type)
			}
			if filepath.Clean(d.ProjectPath) != filepath.Clean(projectRealDir) {
				t.Fatalf("project path not resolved: %q vs %q", d.ProjectPath, projectRealDir)
			}
			if d.ProjectSlug != "X--repo-fresh3-api" || d.Confidence != "high" {
				t.Fatalf("slug/confidence: %q / %q", d.ProjectSlug, d.Confidence)
			}
		default:
			t.Fatalf("unexpected scope %q", d.Scope)
		}
	}
	if !sawUser || !sawProject {
		t.Fatalf("want both scopes, user=%v project=%v", sawUser, sawProject)
	}
}

func TestAdapter_ProjectWithoutSessionsKeepsLowConfidence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	slug := "Y--loose-project"
	mustMkdirAll(t, filepath.Join(home, "memory"))
	mustMkdirAll(t, filepath.Join(home, "projects", slug, "memory"))
	writeFile(t, filepath.Join(home, "projects", slug, "memory", "m.md"), "只有一行正文\n")

	units, err := New(home).ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d units", len(units))
	}
	d := units[0].Drafts[0]
	if d.Confidence != "low" || d.ProjectSlug != slug {
		t.Fatalf("confidence=%q slug=%q", d.Confidence, d.ProjectSlug)
	}
}

func TestAdapter_SkipsProjectDirsWithoutMemoryDir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	mustMkdirAll(t, filepath.Join(home, "memory"))
	mustMkdirAll(t, filepath.Join(home, "projects", "Z--nothing"))
	writeFile(t, filepath.Join(home, "memory", "a.md"), "一条用户记忆\n")

	units, err := New(home).ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("want only the user unit, got %d", len(units))
	}
}

func mustMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
