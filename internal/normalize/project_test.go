package normalize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectKey_PrefersGitRemote(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/Foo/Bar.git",
		"git@github.com:Foo/Bar.git",
		"ssh://git@github.com/Foo/Bar.git",
	} {
		got := ProjectKey(remote, `D:\somewhere\else\bar`)
		if got != "github.com/foo/bar" {
			t.Fatalf("remote %q -> %q, want github.com/foo/bar", remote, got)
		}
	}
}

func TestProjectKey_FallsBackToAbsolutePath(t *testing.T) {
	got := ProjectKey("", filepath.FromSlash("F:/go/Agent-Memory/"))
	want := "f:/go/agent-memory"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestProjectKey_BothEmpty(t *testing.T) {
	if got := ProjectKey("", ""); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestProjectKey_CollapsesDuplicateSeparators(t *testing.T) {
	a := ProjectKey("", `C:\a\\b`)
	b := ProjectKey("", `c:/a/b`)
	if a != b || a != "c:/a/b" {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestGitRemoteFromConfig(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = git@github.com:Foo/Bar.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[remote \"upstream\"]\n\turl = https://github.com/up/repo.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := GitRemoteFromConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "git@github.com:Foo/Bar.git" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestGitRemoteFromConfig_NoRepo(t *testing.T) {
	_, ok, err := GitRemoteFromConfig(t.TempDir())
	if err != nil {
		t.Fatalf("missing repo is not an error: %v", err)
	}
	if ok {
		t.Fatal("want ok=false when no .git/config")
	}
}

func TestProjectKey_NormalizesPortsAcrossTransports(t *testing.T) {
	cases := map[string]string{
		"ssh://git@github.com:22/Foo/Bar.git": "github.com/foo/bar",
		"ssh://git@host:2222/a/b.git":         "host/a/b",
		"https://host:2222/a/b.git":           "host/a/b",
		"https://[::1]:8443/a/b.git":          "[::1]/a/b",
		"https://user:pass@host/a/b.git":      "host/a/b",
	}
	for in, want := range cases {
		if got := ProjectKey(in, ""); got != want {
			t.Fatalf("ProjectKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectKey_TwoDistinctReposNeverCollideOnPort(t *testing.T) {
	// 去掉端口后，同一 host 上的不同仓库仍必须是不同 key
	a := ProjectKey("ssh://git@host:2222/team/a.git", "")
	b := ProjectKey("https://host/team/b.git", "")
	if a == b {
		t.Fatalf("distinct repos collided: %q", a)
	}
	if a != "host/team/a" || b != "host/team/b" {
		t.Fatalf("a=%q b=%q", a, b)
	}
}
