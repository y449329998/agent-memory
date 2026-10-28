package surface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const beginMarker = "<!-- agent-memory:begin -->"
const endMarker = "<!-- agent-memory:end -->"

func TestInstallBlock_CreatesFileWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	changed, err := InstallBlock(path, beginMarker, endMarker, "## 共享记忆\n\n读 `.agent-memory/HANDOFF.md`。\n")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed")
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), beginMarker) {
		t.Fatalf("managed block should open with the begin marker, got %q", data)
	}
	if !strings.Contains(string(data), "## 共享记忆") {
		t.Fatalf("file content = %q", data)
	}
	if !strings.Contains(string(data), beginMarker) || !strings.Contains(string(data), endMarker) {
		t.Fatalf("markers missing:\n%s", data)
	}
}

func TestInstallBlock_KeepsUserContentAndReplacesOnlyOurs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	original := "# 项目说明\n\n这是用户自己写的规则。\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "v1\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "v2\n"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if !strings.Contains(text, "这是用户自己写的规则。") {
		t.Fatalf("user content lost:\n%s", text)
	}
	if strings.Count(text, beginMarker) != 1 {
		t.Fatalf("markers duplicated:\n%s", text)
	}
	if strings.Contains(text, "v1") || !strings.Contains(text, "v2") {
		t.Fatalf("old block not replaced:\n%s", text)
	}
}

func TestInstallBlock_IdempotentNoRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if _, err := InstallBlock(path, beginMarker, endMarker, "同一份内容\n"); err != nil {
		t.Fatal(err)
	}
	changed, err := InstallBlock(path, beginMarker, endMarker, "同一份内容\n")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("identical block must not rewrite the file")
	}
}

func TestRemoveBlock_RestoresOriginalBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	original := "# 规则\n\n保留\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "插入内容\n"); err != nil {
		t.Fatal(err)
	}
	changed, err := RemoveBlock(path, beginMarker, endMarker)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed")
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatalf("want original bytes back, got %q", data)
	}
	if _, err := RemoveBlock(path, beginMarker, endMarker); err != nil {
		t.Fatal(err)
	}
}

func TestInstallBlock_RefusesUnbalancedMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	if err := os.WriteFile(path, []byte("begin\n"+beginMarker+"\n中间没有结束标记\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "x\n"); err == nil {
		t.Fatal("refuse to operate on a half-marked file — rewriting it could eat user text")
	}
}

func TestInstallBlock_RefusesRepeatedMarkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	original := beginMarker + "\nA\n" + endMarker + "\n" + beginMarker + "\nB\n" + endMarker + "\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallBlock(path, beginMarker, endMarker, "x\n"); err == nil {
		t.Fatal("duplicated blocks are ambiguous; refuse instead of guessing which one is ours")
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("file changed despite refusal: %s", data)
	}
}

func TestRemoveBlock_MissingFileIsNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.md")
	changed, err := RemoveBlock(path, beginMarker, endMarker)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("removing from a missing file is not a change")
	}
}
