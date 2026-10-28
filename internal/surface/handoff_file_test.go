package surface

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteHandoff_CreatesFileAndReportsChange(t *testing.T) {
	dir := t.TempDir()
	path, changed, err := WriteHandoff(dir, "# 交接简报\n\n目标：加乐观锁\n")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".agent-memory", "HANDOFF.md")
	if path != want {
		t.Fatalf("path = %q want %q", path, want)
	}
	if !changed {
		t.Fatal("first write should report a change")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "加乐观锁") {
		t.Fatalf("body = %q", body)
	}
}

func TestWriteHandoff_IdenticalContentDoesNotTouchMTime(t *testing.T) {
	dir := t.TempDir()
	path, _, err := WriteHandoff(dir, "同一份内容\n")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := st.ModTime()
	// 显式把 mtime 推到过去，验证「内容相同就不重写」
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	_, _, err = WriteHandoff(dir, "同一份内容\n")
	if err != nil {
		t.Fatal(err)
	}
	st2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !st2.ModTime().Equal(old) {
		t.Fatalf("mtime moved from %v to %v: identical content must not rewrite", old, st2.ModTime())
	}
	_ = before
}

func TestWriteHandoff_LeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := WriteHandoff(dir, "第一版\n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := WriteHandoff(dir, "第二版\n"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".agent-memory"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "HANDOFF.md" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("temp files left behind: %v", names)
	}
}

func TestEnsureGitExclude(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	added, err := EnsureGitExclude(dir, "/.agent-memory/")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first call should add the entry")
	}
	data, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/.agent-memory/") {
		t.Fatalf("exclude content = %q", data)
	}

	// 再调用不重复追加
	added, err = EnsureGitExclude(dir, "/.agent-memory/")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("second call should be a no-op")
	}
	data2, _ := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if strings.Count(string(data2), "/.agent-memory/") != 1 {
		t.Fatalf("entry duplicated: %q", data2)
	}
}

func TestEnsureGitExclude_KeepsExistingLines(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "exclude"), []byte("/vendor/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureGitExclude(dir, "/.agent-memory/"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if !strings.Contains(string(data), "/vendor/") || !strings.Contains(string(data), "/.agent-memory/") {
		t.Fatalf("existing entries damaged: %q", data)
	}
}

func TestEnsureGitExclude_NonRepoIsSilentNoop(t *testing.T) {
	added, err := EnsureGitExclude(t.TempDir(), "/.agent-memory/")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("non-repo directory should not report an addition")
	}
}
