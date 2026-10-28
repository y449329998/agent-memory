package qoder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverHome_PrefersQoderCNAgent(t *testing.T) {
	base := t.TempDir()
	mk(t, filepath.Join(base, ".qoder", "projects"))
	mk(t, filepath.Join(base, ".qoder-cn", "memory"))

	got, ok := DiscoverHome(base)
	if !ok {
		t.Fatal("not found")
	}
	if filepath.Base(got) != ".qoder-cn" {
		t.Fatalf("got %q, want .qoder-cn first", got)
	}
}

func TestDiscoverHome_FallsBackToPlainQoder(t *testing.T) {
	base := t.TempDir()
	mk(t, filepath.Join(base, ".qoder", "memory"))
	if got, ok := DiscoverHome(base); !ok || filepath.Base(got) != ".qoder" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestDiscoverHome_IgnoresEmptyLookalikeDirs(t *testing.T) {
	base := t.TempDir()
	mk(t, filepath.Join(base, ".qoder")) // 存在但没有 memory/projects
	if _, ok := DiscoverHome(base); ok {
		t.Fatal("empty lookalike dir must not count as a Qoder home")
	}
}

func TestProjectPathFromSessions_HighConfidenceFromJSONL(t *testing.T) {
	dir := t.TempDir()
	// 目标目录必须真实存在才可信
	target := filepath.Join(t.TempDir(), "repo")
	mk(t, target)

	content := "{\"type\":\"runtime-config\",\"sessionId\":\"s1\"}\n" +
		"{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[\"" + jsonEscape(target) + "\"]}\n" +
		"{\"type\":\"user\",\"sessionId\":\"s1\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, conf := ProjectPathFromSessions(dir)
	if conf != "high" {
		t.Fatalf("confidence = %q, want high", conf)
	}
	if filepath.Clean(got) != filepath.Clean(target) {
		t.Fatalf("got %q want %q", got, target)
	}
}

func TestProjectPathFromSessions_SkipsHugeLinesBeyondBuffer(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "repo2")
	mk(t, target)
	// 技能正文之类会把 workspace-directories 行挤到很后面；行本身也可能极长
	long := make([]byte, 3*1024*1024)
	for i := range long {
		long[i] = 'x'
	}
	content := "{\"type\":\"user\",\"content\":\"" + string(long) + "\"}\n" +
		"{\"type\":\"workspace-directories\",\"directories\":[\"" + jsonEscape(target) + "\"]}\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, conf := ProjectPathFromSessions(dir)
	if conf != "high" || filepath.Clean(got) != filepath.Clean(target) {
		t.Fatalf("3MB 行不应被丢弃: got %q conf=%q", got, conf)
	}
}

// TestProjectPathFromSessions_FindsWorkspaceLineBeyondOldLimit 回归 I6：
// 旧实现在第 500 行截断，导致这里只反解出 low；而会话解析是全文扫描，
// 同一条会话的 ProjectPath 却是 high，记忆与会话被拆到两个项目。
func TestProjectPathFromSessions_FindsWorkspaceLineBeyondOldLimit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "repo601")
	mk(t, target)

	var b strings.Builder
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "{\"type\":\"user\",\"sessionId\":\"s1\",\"index\":%d}\n", i)
	}
	b.WriteString("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[\"" + jsonEscape(target) + "\"]}\n")
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	got, conf := ProjectPathFromSessions(dir)
	if conf != "high" || filepath.Clean(got) != filepath.Clean(target) {
		t.Fatalf("workspace 行在第 601 行也必须命中: got %q conf=%q", got, conf)
	}
}

func TestProjectPathFromSessions_FallsBackToSlugWithLowConfidence(t *testing.T) {
	dir := t.TempDir()
	slug := "E--company-nuonuo-fresh3-api"
	project := filepath.Join(dir, slug)
	mk(t, project)

	got, conf := ProjectPathFromSessions(project)
	if conf != "low" {
		t.Fatalf("no session data means low confidence, got %q", conf)
	}
	// 反解只能用于展示：项目名里的连字符与层级分隔符无法区分，这里必然解错
	if filepath.Clean(got) == filepath.Clean(`E:\company\nuonuo-fresh3-api`) {
		t.Fatal("slug decode unexpectedly correct — the test asserts it is unreliable")
	}
}

func TestDecodeSlug(t *testing.T) {
	got, ok := decodeSlug("F--go-agent-memory")
	if !ok || got == "" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if _, ok := decodeSlug("novalue"); ok {
		t.Fatal("slug without -- should not decode")
	}
}

func mk(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func jsonEscape(s string) string {
	out := make([]rune, 0, len(s)+4)
	for _, r := range s {
		switch r {
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
