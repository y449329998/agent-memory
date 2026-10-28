package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateToken_CreatesOnceAndIsPerms600(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateToken(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 32 {
		t.Fatalf("token too short: %q", first)
	}
	second, err := LoadOrCreateToken(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("token must be stable across restarts")
	}

	info, err := os.Stat(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	// Windows 上 os.Chmod 只对只读位有效，权限断言仅在 POSIX 上做
	if !isWindows() && info.Mode().Perm() != 0o600 {
		t.Fatalf("token perms = %v, want 0600", info.Mode().Perm())
	}
}

func TestLoadOrCreateToken_RejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateToken(path); err == nil {
		t.Fatal("empty token file should be an error, not a silently regenerated secret")
	}
}

func TestWritePort_ReadPort_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "port")
	if err := WritePort(path, 55991); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPort(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != 55991 {
		t.Fatalf("port = %d", got)
	}
}

func TestReadPort_RejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "port")
	if err := os.WriteFile(path, []byte("not-a-port\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPort(path); err == nil {
		t.Fatal("want error for garbage port file")
	}
}

func isWindows() bool { return filepath.Separator == '\\' }
