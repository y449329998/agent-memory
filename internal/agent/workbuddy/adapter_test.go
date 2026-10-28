package workbuddy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// buildFakeHome 造一个和真实 WorkBuddy 同构的假 home：MEMORY.md 分节带嵌套子项，
// 以及必须被忽略的 USER.md / SOUL.md / IDENTITY.md 与 app/sessions.json。
func buildFakeHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".workbuddy")
	memory := strings.Join([]string{
		"# 用户偏好",
		"",
		"## 代码风格（用户多次强调，务必遵守）",
		"- **查询构造器变量不要命名为 `$query`**：会与 `$request->query()` 混淆。",
		"  - **闭包内可以用**",
		"  - **闭包外 / 方法体一律用 `$list`**",
		"- 用 `when()` 替代 `if` 链",
		"",
		"## 工作流程",
		"- 实施前必须写计划",
		"- 计划执行方式用 Inline Execution",
		"",
		"## 硬约束",
		"- 代码绝不自动 commit",
		"",
		"## 教训",
		"- 遇到白屏先查 logcat",
		"",
		"## 其他",
		"- 普通事实",
		"",
	}, "\n")
	writeFile(t, filepath.Join(home, "MEMORY.md"), memory)
	writeFile(t, filepath.Join(home, "USER.md"), "---\nname: user\n---\n- 不应导入的 persona\n")
	writeFile(t, filepath.Join(home, "SOUL.md"), "- 不应导入的人格\n")
	writeFile(t, filepath.Join(home, "IDENTITY.md"), "- 不应导入的身份\n")
	writeFile(t, filepath.Join(home, "app", "sessions.json"), `{"sessions":[]}`)
	return home
}

func scan(t *testing.T, home string) []agent.MemoryUnit {
	t.Helper()
	units, err := New(home).ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return units
}

func TestAdapter_ScanMemoryUnits(t *testing.T) {
	home := buildFakeHome(t)
	units := scan(t, home)

	if len(units) != 1 {
		t.Fatalf("got %d units, want 1 (other .md files must be ignored): %+v", len(units), units)
	}
	u := units[0]
	wantPath := filepath.Join(home, "MEMORY.md")
	if filepath.Clean(u.Path) != filepath.Clean(wantPath) {
		t.Fatalf("unit path = %q, want %q", u.Path, wantPath)
	}
	if u.Err != nil {
		t.Fatalf("unexpected error: %v", u.Err)
	}
	if u.Size != int64(len(u.Content)) || u.MTime.IsZero() {
		t.Fatalf("size/mtime not populated: size=%d len=%d mtime=%v", u.Size, len(u.Content), u.MTime)
	}
	if len(u.Drafts) != 7 {
		t.Fatalf("got %d drafts, want 7: %+v", len(u.Drafts), u.Drafts)
	}

	wantTypes := []domain.MemoryType{
		domain.TypePreference, domain.TypePreference,
		domain.TypeConvention, domain.TypeConvention,
		domain.TypeConstraint,
		domain.TypeLesson,
		domain.TypeFact,
	}
	for i, d := range u.Drafts {
		if d.Type != wantTypes[i] {
			t.Fatalf("draft %d type = %q, want %q", i, d.Type, wantTypes[i])
		}
		if d.Scope != domain.ScopeUser || d.ProjectPath != "" || d.ProjectSlug != "" {
			t.Fatalf("draft %d must be user scope without project: %+v", i, d)
		}
		if d.SourceAgent != "workbuddy" || d.Confidence != "high" ||
			filepath.Clean(d.SourcePath) != filepath.Clean(u.Path) {
			t.Fatalf("draft %d metadata wrong: %+v", i, d)
		}
	}
	if !strings.Contains(u.Drafts[0].Text, "\n**闭包内可以用**") {
		t.Fatalf("nested bullet not merged into parent: %q", u.Drafts[0].Text)
	}
	for _, d := range u.Drafts {
		if strings.Contains(d.Text, "不应导入") {
			t.Fatalf("persona file leaked into drafts: %+v", d)
		}
	}
}

func TestAdapter_MissingOrEmptyMemoryYieldsNothing(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		home string
	}{
		{"missing home", filepath.Join(root, "missing")},
		{"empty home", filepath.Join(root, "empty")},
		{"no MEMORY.md", filepath.Join(root, "no-memory")},
		{"empty MEMORY.md", filepath.Join(root, "empty-file")},
	}
	mustMkdirAll(t, filepath.Join(root, "empty"))
	mustMkdirAll(t, filepath.Join(root, "no-memory"))
	writeFile(t, filepath.Join(root, "no-memory", "USER.md"), "- 只有 persona\n")
	writeFile(t, filepath.Join(root, "empty-file", "MEMORY.md"), "  \n\t\n")
	for _, c := range cases {
		if units := scan(t, c.home); len(units) != 0 {
			t.Fatalf("%s: got %+v, want no units", c.name, units)
		}
	}
	if units := scan(t, ""); len(units) != 0 {
		t.Fatalf("empty Home: got %+v", units)
	}
}

// TestAdapter_ReadFailureIsolated: MEMORY.md 读不出来时只登记一条错误 unit，
// ScanMemoryUnits 本身不失败。用同名目录模拟读失败，跨平台稳定。
func TestAdapter_ReadFailureIsolated(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".workbuddy")
	mustMkdirAll(t, filepath.Join(home, "MEMORY.md"))

	units := scan(t, home)
	if len(units) != 1 {
		t.Fatalf("got %d units, want 1", len(units))
	}
	if units[0].Err == nil || filepath.Base(units[0].Path) != "MEMORY.md" {
		t.Fatalf("MEMORY.md should yield an error unit: %+v", units[0])
	}
	if len(units[0].Drafts) != 0 {
		t.Fatalf("error unit must not carry drafts: %+v", units[0].Drafts)
	}
}

func TestAdapter_ScanOrderIsDeterministic(t *testing.T) {
	home := buildFakeHome(t)
	first := scan(t, home)
	second := scan(t, home)
	if len(first) != len(second) {
		t.Fatalf("unit count changed between scans: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Path != second[i].Path {
			t.Fatalf("unit %d path changed: %q vs %q", i, first[i].Path, second[i].Path)
		}
		if len(first[i].Drafts) != len(second[i].Drafts) {
			t.Fatalf("unit %d draft count changed", i)
		}
		for j := range first[i].Drafts {
			if first[i].Drafts[j].Text != second[i].Drafts[j].Text {
				t.Fatalf("unit %d draft %d text changed", i, j)
			}
		}
	}
}

func TestAdapter_SessionUnitsEmpty(t *testing.T) {
	units, err := New("").ScanSessionUnits(context.Background())
	if err != nil || len(units) != 0 {
		t.Fatalf("units=%v err=%v", units, err)
	}
}

func TestDiscoverHome(t *testing.T) {
	base := t.TempDir()

	withMemory := filepath.Join(base, "with-memory", ".workbuddy")
	mustMkdirAll(t, withMemory)
	writeFile(t, filepath.Join(withMemory, "MEMORY.md"), "- a\n")
	if got, ok := DiscoverHome(filepath.Join(base, "with-memory")); !ok || filepath.Clean(got) != filepath.Clean(withMemory) {
		t.Fatalf("MEMORY.md home: got %q ok=%v", got, ok)
	}

	withApp := filepath.Join(base, "with-app", ".workbuddy")
	mustMkdirAll(t, filepath.Join(withApp, "app"))
	if got, ok := DiscoverHome(filepath.Join(base, "with-app")); !ok || filepath.Clean(got) != filepath.Clean(withApp) {
		t.Fatalf("app home: got %q ok=%v", got, ok)
	}

	lookalike := filepath.Join(base, "lookalike", ".workbuddy")
	mustMkdirAll(t, lookalike)
	if _, ok := DiscoverHome(filepath.Join(base, "lookalike")); ok {
		t.Fatal("empty lookalike dir must not count as a WorkBuddy home")
	}
	if _, ok := DiscoverHome(filepath.Join(base, "missing")); ok {
		t.Fatal("missing dir must not count as a WorkBuddy home")
	}
	if _, ok := DiscoverHome(""); ok {
		t.Fatal("empty user home must not count as a WorkBuddy home")
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
