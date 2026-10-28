package trae

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSlugForPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{`C:\Users\Admin\Desktop\go-model`, "-c-users-admin-desktop-go-model"},
		{`D:\company\nuonuo-fresh3-api`, "-d-company-nuonuo-fresh3-api"},
		{`D:/company/nuonuo-ai`, "-d-company-nuonuo-ai"},
	}
	for _, c := range cases {
		if got := slugForPath(c.path); got != c.want {
			t.Fatalf("slugForPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestSlugKeyStripsWindowSuffix(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"-d-company-nuonuo-fresh3-api--p2-e4cebb9f4d6c5ac0fb11", "-d-company-nuonuo-fresh3-api"},
		{"-D-Company--P2-ABC", "-d-company"},
		{"-d-company-nuonuo-fresh3-api", "-d-company-nuonuo-fresh3-api"},
	}
	for _, c := range cases {
		if got := slugKey(c.name); got != c.want {
			t.Fatalf("slugKey(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWorkspaceFolderDecodesFileURI(t *testing.T) {
	cases := []struct {
		data string
		want string
		ok   bool
	}{
		{`{"folder":"file:///d%3A/company/nuonuo-ai"}`, `D:\company\nuonuo-ai`, true},
		{`{"folder":"file:///c%3A/Users/Admin/Desktop/go%20model"}`, `C:\Users\Admin\Desktop\go model`, true},
		{`{"workspace":"file:///c%3A/x"}`, "", false},
		{`{"folder":""}`, "", false},
		{`{"folder":"https://example.com/x"}`, "", false},
		{`not json`, "", false},
	}
	for _, c := range cases {
		got, ok := workspaceFolder([]byte(c.data))
		if ok != c.ok {
			t.Fatalf("workspaceFolder(%s) ok = %v, want %v", c.data, ok, c.ok)
		}
		if ok && !samePath(got, c.want) {
			t.Fatalf("workspaceFolder(%s) = %q, want %q", c.data, got, c.want)
		}
	}
}

func TestWorkspaceIndexAndProjectLocation(t *testing.T) {
	storage := t.TempDir()
	writeFile(t, filepath.Join(storage, "hash-a", "workspace.json"),
		`{"folder":"file:///d%3A/company/nuonuo-fresh3-api"}`)
	writeFile(t, filepath.Join(storage, "hash-b", "workspace.json"),
		`{"folder":"file:///c%3A/Users/Admin/Desktop/go-model"}`)
	writeFile(t, filepath.Join(storage, "hash-c", "workspace.json"),
		`{"workspace":"file:///c%3A/not-a-folder"}`)

	idx := workspaceIndex([]string{storage})

	path, conf := projectLocation("-d-company-nuonuo-fresh3-api--p2-e4cebb9f4d6c5ac0fb11", idx)
	if conf != "high" || !samePath(path, `D:\company\nuonuo-fresh3-api`) {
		t.Fatalf("window dir not resolved: path=%q conf=%q", path, conf)
	}

	path, conf = projectLocation("-c-Users-Admin-Desktop-go-model", idx)
	if conf != "high" || !samePath(path, `C:\Users\Admin\Desktop\go-model`) {
		t.Fatalf("case-insensitive slug lookup failed: path=%q conf=%q", path, conf)
	}

	path, conf = projectLocation("-d-company", idx)
	if conf != "low" || path != "" {
		t.Fatalf("unmapped project must be low with empty path: path=%q conf=%q", path, conf)
	}
}

func TestWorkspaceIndexPrefersFirstRoot(t *testing.T) {
	cn := t.TempDir()
	plain := t.TempDir()
	writeFile(t, filepath.Join(cn, "h1", "workspace.json"),
		`{"folder":"file:///d%3A/company/nuonuo-fresh3-api"}`)
	writeFile(t, filepath.Join(plain, "h1", "workspace.json"),
		`{"folder":"file:///e%3A/other/nuonuo-fresh3-api"}`)

	idx := workspaceIndex([]string{cn, plain})
	path, conf := projectLocation("-d-company-nuonuo-fresh3-api", idx)
	if conf != "high" || !samePath(path, `D:\company\nuonuo-fresh3-api`) {
		t.Fatalf("CN root should win: path=%q conf=%q", path, conf)
	}
}

func TestWorkspaceIndexMissingRootIsEmpty(t *testing.T) {
	idx := workspaceIndex([]string{filepath.Join(t.TempDir(), "nope")})
	if len(idx) != 0 {
		t.Fatalf("missing root produced %d entries", len(idx))
	}
	if _, conf := projectLocation("-d-company", idx); conf != "low" {
		t.Fatalf("confidence = %q", conf)
	}
}

func TestDiscoverHomePrefersTraeCN(t *testing.T) {
	base := t.TempDir()
	mustMkdirAll(t, filepath.Join(base, ".trae", "memory"))
	mustMkdirAll(t, filepath.Join(base, ".trae-cn", "user_rules"))

	got, ok := DiscoverHome(base)
	if !ok || filepath.Base(got) != ".trae-cn" {
		t.Fatalf("got %q ok=%v, want .trae-cn", got, ok)
	}
}

func TestDiscoverHomeFallsBackToPlainTrae(t *testing.T) {
	base := t.TempDir()
	mustMkdirAll(t, filepath.Join(base, ".trae", "memory"))
	if got, ok := DiscoverHome(base); !ok || filepath.Base(got) != ".trae" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestDiscoverHomeIgnoresEmptyLookalike(t *testing.T) {
	base := t.TempDir()
	mustMkdirAll(t, filepath.Join(base, ".trae-cn"))
	if _, ok := DiscoverHome(base); ok {
		t.Fatal("empty lookalike dir must not count as a Trae home")
	}
}

// samePath 跨平台比较路径：大小写不敏感，分隔符统一成 /。
func samePath(a, b string) bool {
	norm := func(p string) string {
		return strings.ToLower(strings.ReplaceAll(filepath.Clean(p), `\`, "/"))
	}
	return norm(a) == norm(b)
}
