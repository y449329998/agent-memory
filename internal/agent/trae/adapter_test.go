package trae

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

type fakeHome struct {
	home    string
	storage string
	realA   string
	slugA   string
	loose   string
}

// buildFakeHome 造一个和真实 Trae 同构的假 home：
// 用户级记忆、两个窗口的项目记忆、未映射项目、用户规则，以及 workspaceStorage。
func buildFakeHome(t *testing.T) fakeHome {
	t.Helper()
	root := t.TempDir()
	f := fakeHome{
		home:    filepath.Join(root, ".trae-cn"),
		storage: filepath.Join(root, "workspaceStorage"),
		realA:   filepath.Join(root, "repo", "fresh3-api"),
		loose:   "-x-loose-project",
	}
	realB := filepath.Join(root, "repo", "second")
	mustMkdirAll(t, f.realA)
	mustMkdirAll(t, realB)
	f.slugA = slugForPath(f.realA)

	writeWorkspace(t, f.storage, "hash-a", f.realA)
	writeWorkspace(t, f.storage, "hash-b", realB)

	writeFile(t, filepath.Join(f.home, "memory", "user_profile.md"), `## User Preferences
- 中文沟通
- 提交信息用中文

## Tech Stack
- Go 1.22
`)
	writeFile(t, filepath.Join(f.home, "memory", "MEMORY.md"), "- [用户画像](user_profile.md)\n")

	writeFile(t, filepath.Join(f.home, "memory", "projects", f.slugA, "project_memory.md"), `## Hard Constraints
- 约束一

## Lessons Learned
- 教训一
`)
	writeFile(t, filepath.Join(f.home, "memory", "projects", f.slugA, "MEMORY.md"), "索引不是记忆\n")

	// 同一项目的第二个窗口：--p2-<hash> 去掉后与 slugA 相同。
	writeFile(t, filepath.Join(f.home, "memory", "projects", f.slugA+"--p2-deadbeef", "project_memory.md"),
		"## Engineering Conventions\n- 约定一\n")

	// 没有 workspace 映射的项目。
	writeFile(t, filepath.Join(f.home, "memory", "projects", f.loose, "project_memory.md"),
		"## Hard Constraints\n- 孤立约束\n")

	// 只有分节、没有 bullet：不应产出 unit。
	writeFile(t, filepath.Join(f.home, "memory", "projects", slugForPath(realB), "project_memory.md"),
		"## Hard Constraints\n\n")

	writeFile(t, filepath.Join(f.home, "user_rules", "rule-1.md"), "规则一\n")
	writeFile(t, filepath.Join(f.home, "user_rules", "rule-2.md"), "规则二第一行\n规则二第二行\n")

	return f
}

func scanFake(t *testing.T, f fakeHome) []agent.MemoryUnit {
	t.Helper()
	a := New(f.home)
	a.WorkspaceRoots = []string{f.storage}
	units, err := a.ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return units
}

func TestAdapter_ScanMemoryUnits(t *testing.T) {
	f := buildFakeHome(t)
	units := scanFake(t, f)

	wantPaths := []string{
		filepath.Join(f.home, "memory", "user_profile.md"),
		filepath.Join(f.home, "memory", "projects", f.slugA, "project_memory.md"),
		filepath.Join(f.home, "memory", "projects", f.slugA+"--p2-deadbeef", "project_memory.md"),
		filepath.Join(f.home, "memory", "projects", f.loose, "project_memory.md"),
		filepath.Join(f.home, "user_rules", "rule-1.md"),
		filepath.Join(f.home, "user_rules", "rule-2.md"),
	}
	if len(units) != len(wantPaths) {
		t.Fatalf("got %d units, want %d: %+v", len(units), len(wantPaths), units)
	}
	for i, want := range wantPaths {
		if filepath.Clean(units[i].Path) != filepath.Clean(want) {
			t.Fatalf("unit %d path = %q, want %q", i, units[i].Path, want)
		}
	}

	total := 0
	for _, u := range units {
		if u.Err != nil {
			t.Fatalf("%s: unexpected error: %v", u.Path, u.Err)
		}
		if u.Size != int64(len(u.Content)) || u.MTime.IsZero() {
			t.Fatalf("%s: size/mtime not populated", u.Path)
		}
		for _, d := range u.Drafts {
			if d.SourceAgent != "trae" {
				t.Fatalf("%s: agent = %q", u.Path, d.SourceAgent)
			}
		}
		total += len(u.Drafts)
	}
	if total != 9 {
		t.Fatalf("got %d drafts, want 9", total)
	}

	user := findUnit(t, units, filepath.Join(f.home, "memory", "user_profile.md"))
	if len(user.Drafts) != 3 {
		t.Fatalf("user drafts = %d", len(user.Drafts))
	}
	wantUserTypes := []domain.MemoryType{domain.TypePreference, domain.TypePreference, domain.TypeFact}
	for i, want := range wantUserTypes {
		d := user.Drafts[i]
		if d.Type != want || d.Scope != domain.ScopeUser || d.ProjectPath != "" || d.ProjectSlug != "" ||
			d.Confidence != "high" {
			t.Fatalf("user draft %d wrong: %+v", i, d)
		}
	}

	project := findUnit(t, units, filepath.Join(f.home, "memory", "projects", f.slugA, "project_memory.md"))
	if len(project.Drafts) != 2 {
		t.Fatalf("project drafts = %d", len(project.Drafts))
	}
	if project.Drafts[0].Type != domain.TypeConstraint || project.Drafts[1].Type != domain.TypeLesson {
		t.Fatalf("project types = %q / %q", project.Drafts[0].Type, project.Drafts[1].Type)
	}
	for _, d := range project.Drafts {
		if d.Scope != domain.ScopeProject || d.Confidence != "high" || d.ProjectSlug != f.slugA ||
			!samePath(d.ProjectPath, f.realA) {
			t.Fatalf("project draft wrong: %+v", d)
		}
	}

	window := findUnit(t, units, filepath.Join(f.home, "memory", "projects", f.slugA+"--p2-deadbeef", "project_memory.md"))
	if len(window.Drafts) != 1 || window.Drafts[0].Type != domain.TypeConvention {
		t.Fatalf("window draft wrong: %+v", window.Drafts)
	}
	if window.Drafts[0].ProjectSlug != f.slugA || window.Drafts[0].Confidence != "high" ||
		!samePath(window.Drafts[0].ProjectPath, f.realA) {
		t.Fatalf("window draft not merged into same project: %+v", window.Drafts[0])
	}

	loose := findUnit(t, units, filepath.Join(f.home, "memory", "projects", f.loose, "project_memory.md"))
	if len(loose.Drafts) != 1 {
		t.Fatalf("loose drafts = %d", len(loose.Drafts))
	}
	if d := loose.Drafts[0]; d.Confidence != "low" || d.ProjectPath != "" || d.ProjectSlug != f.loose {
		t.Fatalf("unmapped project must be low confidence: %+v", d)
	}

	rule1 := findUnit(t, units, filepath.Join(f.home, "user_rules", "rule-1.md"))
	rule2 := findUnit(t, units, filepath.Join(f.home, "user_rules", "rule-2.md"))
	for _, u := range []agent.MemoryUnit{rule1, rule2} {
		d := u.Drafts[0]
		if d.Type != domain.TypePreference || d.Scope != domain.ScopeUser || d.ProjectPath != "" ||
			d.Confidence != "high" || !samePath(d.SourcePath, u.Path) {
			t.Fatalf("rule draft wrong: %+v", d)
		}
	}
	if !strings.Contains(rule2.Drafts[0].Text, "\n") {
		t.Fatalf("multi-line rule lost newline: %q", rule2.Drafts[0].Text)
	}
}

func TestAdapter_ScanOrderIsDeterministic(t *testing.T) {
	f := buildFakeHome(t)
	first := scanFake(t, f)
	second := scanFake(t, f)
	if len(first) != len(second) {
		t.Fatalf("unit count changed between scans: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Path != second[i].Path {
			t.Fatalf("unit %d path changed: %q vs %q", i, first[i].Path, second[i].Path)
		}
	}
}

// TestAdapter_ReadFailureIsolated: 一个读不出来的 .md 只登记错误 unit，
// 同目录与其他目录的源照常扫描。用同名目录模拟读失败，跨平台稳定。
func TestAdapter_ReadFailureIsolated(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".trae-cn")
	mustMkdirAll(t, filepath.Join(home, "memory", "broken.md"))
	writeFile(t, filepath.Join(home, "memory", "user_profile.md"), "## User Preferences\n- 正常\n")
	writeFile(t, filepath.Join(home, "user_rules", "rule-1.md"), "规则\n")

	a := New(home)
	a.WorkspaceRoots = []string{filepath.Join(root, "no-storage")}
	units, err := a.ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 3 {
		t.Fatalf("got %d units, want 3: %+v", len(units), units)
	}
	if units[0].Err == nil || !strings.HasSuffix(units[0].Path, "broken.md") {
		t.Fatalf("broken.md should yield an error unit: %+v", units[0])
	}
	if len(units[0].Drafts) != 0 {
		t.Fatalf("error unit must not carry drafts: %+v", units[0].Drafts)
	}
	if units[1].Err != nil || units[1].Drafts[0].Text != "正常" {
		t.Fatalf("good file hurt by bad neighbor: %+v", units[1])
	}
	if units[2].Err != nil || units[2].Drafts[0].Text != "规则" {
		t.Fatalf("rules not scanned after error: %+v", units[2])
	}
}

// TestAdapter_UnreadableMemoryRootPropagates: memory 根读失败不能被吞成空扫描，
// 否则 Runner 会把该 agent 当成本轮扫过，把上一轮的记忆全部误标 stale。
func TestAdapter_UnreadableMemoryRootPropagates(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".trae-cn")
	writeFile(t, filepath.Join(home, "memory"), "not a dir")

	if _, err := New(home).ScanMemoryUnits(context.Background()); err == nil {
		t.Fatal("unreadable memory root must surface an error")
	}
}

func TestAdapter_SessionUnitsEmpty(t *testing.T) {
	units, err := New("").ScanSessionUnits(context.Background())
	if err != nil || len(units) != 0 {
		t.Fatalf("units=%v err=%v", units, err)
	}
}

func findUnit(t *testing.T, units []agent.MemoryUnit, path string) agent.MemoryUnit {
	t.Helper()
	for _, u := range units {
		if filepath.Clean(u.Path) == filepath.Clean(path) {
			return u
		}
	}
	t.Fatalf("unit %q not found", path)
	return agent.MemoryUnit{}
}

func writeWorkspace(t *testing.T, root, hash, folder string) {
	t.Helper()
	slash := filepath.ToSlash(folder)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	uri := "file://" + strings.ReplaceAll(slash, ":", "%3A")
	writeFile(t, filepath.Join(root, hash, "workspace.json"), fmt.Sprintf(`{"folder":%q}`, uri))
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
