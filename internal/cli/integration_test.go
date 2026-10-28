package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome 把用户主目录指向临时目录，并在那里放一个假的 Qoder home。
// Windows 下 os.UserHomeDir 读 USERPROFILE，Linux/macOS 读 HOME，两个都设。
// 返回的 repo 是项目记忆对应的真实工作目录，供 --project 使用。
func isolateHome(t *testing.T) (home, qoderHome, repo string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	qoderHome = filepath.Join(home, ".qoder-cn")
	mustWrite(t, filepath.Join(qoderHome, "memory", "user.md"),
		"---\nname: user\ndescription: 画像\nmetadata:\n  type: user\n---\n\n沟通用中文；语言偏好，提交信息也用中文。\n")
	mustWrite(t, filepath.Join(qoderHome, "memory", "MEMORY.md"), "- [画像](user.md) — 索引行\n")

	repo = filepath.Join(home, "code", "demo")
	mustMkdir(t, repo)
	slug := "demo"
	mustWrite(t, filepath.Join(qoderHome, "projects", slug, "s.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"directories\":[%q]}\n", repo))
	mustWrite(t, filepath.Join(qoderHome, "projects", slug, "memory", "state.md"),
		"---\nmetadata:\n  type: project\n---\n\n余额扣减需乐观锁。\n")
	return home, qoderHome, repo
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, out *bytes.Buffer, args ...string) int {
	t.Helper()
	code := Run(args, out, out)
	t.Logf("agent-memory %s -> %d\n%s", strings.Join(args, " "), code, out.String())
	return code
}

func TestEndToEnd_InitScanSearchAddDoctor(t *testing.T) {
	home, _, repo := isolateHome(t)
	var out bytes.Buffer

	if code := run(t, &out, "init"); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	cfgPath := filepath.Join(home, ".agent-memory", "config.toml")
	dbPath := filepath.Join(home, ".agent-memory", "memory.db")
	for _, p := range []string{cfgPath, dbPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s not created: %v", p, err)
		}
	}

	out.Reset()
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "created=2") {
		t.Fatalf("want created=2 (MEMORY.md 索引被跳过), got %q", out.String())
	}
	if !strings.Contains(out.String(), "units=2") {
		t.Fatalf("want units=2 (MEMORY.md 索引不应成为扫描单元), got %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "memory", "search", "偏好"); code != 0 {
		t.Fatalf("search exit %d", code)
	}
	if !strings.Contains(out.String(), "沟通用中文") {
		t.Fatalf("中文检索未命中: %q", out.String())
	}
	if !strings.Contains(out.String(), "qoder:") {
		t.Fatalf("来源 agent 未展示: %q", out.String())
	}
	if !strings.Contains(out.String(), "最后见到") {
		t.Fatalf("最后见到时间未展示: %q", out.String())
	}

	out.Reset()
	run(t, &out, "scan")
	if !strings.Contains(out.String(), "unchanged=2") {
		t.Fatalf("第二次扫描应全部 unchanged, got %q", out.String())
	}

	// --full 先清扫描游标再重放：记忆仍在，所以是合并而不是新建
	out.Reset()
	if code := run(t, &out, "scan", "--full"); code != 0 {
		t.Fatalf("scan --full exit %d", code)
	}
	if !strings.Contains(out.String(), "已重置 2 个扫描游标") {
		t.Fatalf("scan --full should report reset cursors, got %q", out.String())
	}
	if !strings.Contains(out.String(), "created=0") || !strings.Contains(out.String(), "merged=2") {
		t.Fatalf("scan --full should replay as merged, got %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "memory", "add", "接口返回字段一律蛇形命名", "--type", "convention"); code != 0 {
		t.Fatalf("add exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "新建") {
		t.Fatalf("want 新建, got %q", out.String())
	}

	out.Reset()
	run(t, &out, "memory", "add", "接口返回字段一律蛇形命名", "--type", "convention")
	if !strings.Contains(out.String(), "合并到已有条目") {
		t.Fatalf("same text should merge, got %q", out.String())
	}

	// 不带 --project 只列用户级：user.md + CLI 写入的一条 = 2
	out.Reset()
	if code := run(t, &out, "memory", "list"); code != 0 {
		t.Fatalf("list exit %d", code)
	}
	if !strings.Contains(out.String(), "共 2 条") {
		t.Fatalf("want 共 2 条 (用户级), got %q", out.String())
	}

	// 给了项目则含用户级：上面 2 条 + 项目级 state.md = 3
	out.Reset()
	if code := run(t, &out, "memory", "list", "--project", repo); code != 0 {
		t.Fatalf("list --project exit %d", code)
	}
	if !strings.Contains(out.String(), "共 3 条") {
		t.Fatalf("want 共 3 条, got %q", out.String())
	}

	// purge 不带 --yes 只报数量，不删
	out.Reset()
	if code := run(t, &out, "memory", "purge"); code != 0 {
		t.Fatalf("purge exit %d", code)
	}
	if !strings.Contains(out.String(), "加 --yes 才会真删") {
		t.Fatalf("purge without --yes should only report, got %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	for _, want := range []string{"memory.db", "qoder:", "写入目标"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "未合并项目") {
		t.Fatalf("高置信项目不应被列为未合并:\n%s", out.String())
	}
}

func TestMemorySearch_EmptyQueryIsUsageError(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "search"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "usage: agent-memory memory search") {
		t.Fatalf("want usage, got %q", out.String())
	}
}

func TestMemoryAdd_ProjectScopeNeedsProject(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "add", "x", "--scope", "project"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "--project") {
		t.Fatalf("want hint about --project, got %q", out.String())
	}
}

func TestMemorySearch_ProjectScopeRequiresKnownProject(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "search", "中文", "--project", `Z:\nope`); code != 1 {
		t.Fatalf("unknown project should exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "unknown project") {
		t.Fatalf("want explanation, got %q", out.String())
	}
}

func TestScanRejectsUnknownAgent(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "scan", "--agent", "cursor"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "cursor") {
		t.Fatalf("want agent name in error, got %q", out.String())
	}
}

func TestMemoryAddRejectsBadType(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "add", "x", "--type", "vibes"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}

func TestMemoryWithoutSubcommandPrintsUsage(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "usage: agent-memory memory") {
		t.Fatalf("want usage, got %q", out.String())
	}
}

func TestHelpListsCommands(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "help"); code != 0 {
		t.Fatalf("help exit %d", code)
	}
	for _, want := range []string{"init", "memory", "scan", "version", "doctor"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, out.String())
		}
	}
}
