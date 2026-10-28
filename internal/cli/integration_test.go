package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/agent/trae"
	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/store"
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

// runIn 与 run 相同，用于 stdin 敏感的子命令（mcp）。
// 缺 port 文件时 cmdMCP 在接触 stdin 之前就会失败，所以这里不需要注入输入流；
// 单独留一个名字，是为了让测试表达「即使没给 stdin 也不能挂住」。
func runIn(t *testing.T, out *bytes.Buffer, args ...string) int {
	t.Helper()
	return run(t, out, args...)
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
	if !strings.Contains(out.String(), "已重置 3 个扫描游标") {
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

// TestEndToEnd_ThreeAgentsScanAndDoctor：三家都装假数据时，scan 应按
// qoder → trae → workbuddy 顺序导入，doctor 同时给出三家发现结果。
func TestEndToEnd_ThreeAgentsScanAndDoctor(t *testing.T) {
	home, _, _ := isolateHome(t)

	traeHome := filepath.Join(home, ".trae-cn")
	mustWrite(t, filepath.Join(traeHome, "memory", "user_profile.md"),
		"# 用户偏好\n\n- 注释只留必要部分。\n")
	mustWrite(t, filepath.Join(traeHome, "memory", "projects", "-x-demo", "project_memory.md"),
		"# 硬约束\n\n- 金额字段一律用整数分。\n")

	workBuddyHome := filepath.Join(home, ".workbuddy")
	mustWrite(t, filepath.Join(workBuddyHome, "MEMORY.md"), "# 偏好\n\n- 提交信息用中文。\n")

	var out bytes.Buffer
	if code := run(t, &out, "init"); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	out.Reset()
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	// qoder 2（user.md + state.md）+ trae 2（user_profile + 项目记忆）+ workbuddy 1
	for _, want := range []string{"units=5", "created=5"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("scan missing %q: %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "! ") {
		t.Fatalf("scan should have no per-source errors: %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	for _, want := range []string{
		"按 agent 的记忆条数: map[qoder:2 trae:2 workbuddy:1]",
		"  qoder: " + filepath.Join(home, ".qoder-cn"),
		"  trae: " + traeHome,
		"（workspace 根 2 个）",
		"  workbuddy: " + workBuddyHome,
		"trae:-x-demo", // 无 workspace 索引时项目记忆标为低置信独立项目
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor missing %q:\n%s", want, out.String())
		}
	}
}

func TestMemorySearch_EmptyQueryIsUsageError(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "search"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "usage: agent-memory memory search <关键词> [--]") {
		t.Fatalf("want usage with -- escape hint, got %q", out.String())
	}
}

// TestMemoryAdd_DoubleDashAllowsLeadingDashText 回归：`--` 必须交给 flag.Parse，
// 否则标准逃逸失效，以 '-' 开头的记忆文本会被当成未定义 flag。
func TestMemoryAdd_DoubleDashAllowsLeadingDashText(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "add", "--", "-5% 扣减阈值"); code != 0 {
		t.Fatalf("add with -- should exit 0, got %d: %s", code, out.String())
	}

	out.Reset()
	if code := run(t, &out, "memory", "search", "--", "-5%"); code != 0 {
		t.Fatalf("search with -- should exit 0, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "扣减阈值") {
		t.Fatalf("search should hit the dashed memory, got %q", out.String())
	}
}

func TestMemoryAdd_UnknownFlagAfterTextExits2(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "add", "文本", "--bogus"); code != 2 {
		t.Fatalf("want exit 2, got %d: %s", code, out.String())
	}
}

// TestMemoryAdd_ReusesScannedProjectByPath 回归：add --project 命中已扫描的
// git-remote 项目时必须复用，而不是再建一个按路径命名的第二个项目。
func TestMemoryAdd_ReusesScannedProjectByPath(t *testing.T) {
	home, _, repo := isolateHome(t)
	mustWrite(t, filepath.Join(repo, ".git", "config"),
		"[remote \"origin\"]\n\turl = git@example.com:acme/demo.git\n")

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}

	out.Reset()
	if code := run(t, &out, "memory", "add", "扣减阈值需配置化", "--scope", "project", "--project", repo); code != 0 {
		t.Fatalf("add --project exit %d: %s", code, out.String())
	}

	st, err := store.Open(filepath.Join(home, ".agent-memory", "memory.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	n, err := st.QueryInt(ctx, `SELECT count(*) FROM project`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		keys, _ := st.QueryStrings(ctx, `SELECT key FROM project ORDER BY key`)
		t.Fatalf("project rows = %d %v, want 1 (add must reuse the scanned project)", n, keys)
	}

	projectID, err := st.QueryInt(ctx, `SELECT id FROM project WHERE key = 'example.com/acme/demo'`)
	if err != nil || projectID == 0 {
		t.Fatalf("remote-keyed project not found: id=%d err=%v", projectID, err)
	}
	shared, err := st.QueryInt(ctx,
		`SELECT count(*) FROM memory_item WHERE scope = 'project' AND project_id = ?`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if shared != 2 {
		t.Fatalf("project-scope memories under project_id %d = %d, want 2 (scan + CLI add)", projectID, shared)
	}
	t.Logf("project rows=%d, key=example.com/acme/demo project_id=%d, project-scope memories=%d", n, projectID, shared)
}

func TestDoctor_BadSinceIsUsageErrorWithNoPartialReport(t *testing.T) {
	isolateHome(t)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"doctor", "--since", "bogus"}, &stdout, &stderr); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("doctor printed a partial report before the usage error: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "bad --since") {
		t.Fatalf("want bad --since explanation, got %q", stderr.String())
	}
}

func TestInit_ExistingConfigIgnoresDataDirWithoutForce(t *testing.T) {
	home, _, _ := isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "init"); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	cfgPath := filepath.Join(home, ".agent-memory", "config.toml")
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(home, "elsewhere")
	out.Reset()
	if code := run(t, &out, "init", "--data-dir", other); code != 0 {
		t.Fatalf("init --data-dir exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "忽略") {
		t.Fatalf("output should explain --data-dir is ignored, got %q", out.String())
	}
	if !strings.Contains(out.String(), "数据目录: "+filepath.Join(home, ".agent-memory")) {
		t.Fatalf("should report the configured data dir, got %q", out.String())
	}
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("config changed without --force:\nbefore=%q\nafter=%q", before, after)
	}
	if _, err := os.Stat(filepath.Join(other, "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("--data-dir must not create a database when config is kept: err=%v", err)
	}

	// --force 才真正应用 --data-dir
	out.Reset()
	if code := run(t, &out, "init", "--data-dir", other, "--force"); code != 0 {
		t.Fatalf("init --force exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "数据目录: "+other) {
		t.Fatalf("--force should apply --data-dir, got %q", out.String())
	}
	if _, err := os.Stat(filepath.Join(other, "memory.db")); err != nil {
		t.Fatalf("--force should create the database at the new dir: %v", err)
	}
}

func TestDoctor_IgnoresDriveRootKeyAsUnmerged(t *testing.T) {
	home, _, _ := isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "init"); code != 0 {
		t.Fatalf("init exit %d", code)
	}

	st, err := store.Open(filepath.Join(home, ".agent-memory", "memory.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	now := time.Now()
	if _, err := st.EnsureProject(ctx, "f:", "f", "", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	if strings.Contains(out.String(), "未合并项目") {
		t.Fatalf("drive-root key must not be reported as unmerged:\n%s", out.String())
	}

	st, err = store.Open(filepath.Join(home, ".agent-memory", "memory.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if _, err := st.EnsureProject(ctx, "trae:demo", "demo", "", "", now); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	if !strings.Contains(out.String(), "  trae:demo") {
		t.Fatalf("agent:slug key should still be reported:\n%s", out.String())
	}
	if strings.Contains(out.String(), "\n  f:\n") {
		t.Fatalf("drive-root key must be filtered out:\n%s", out.String())
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

// TestMemoryAdd_SecondCheckoutReusesRemoteProject 回归：另一份未扫描过的 checkout
// 也必须先读 git remote 再决定是否新建，否则同一个仓库会出现两个项目。
func TestMemoryAdd_SecondCheckoutReusesRemoteProject(t *testing.T) {
	home, _, repo := isolateHome(t)
	const remote = "git@example.com:acme/demo.git"
	mustWrite(t, filepath.Join(repo, ".git", "config"), "[remote \"origin\"]\n\turl = "+remote+"\n")

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}

	// 同 remote 的第二份 checkout，库里还没有它的路径映射
	other := filepath.Join(home, "code", "demo-second")
	mustMkdir(t, other)
	mustWrite(t, filepath.Join(other, ".git", "config"), "[remote \"origin\"]\n\turl = "+remote+"\n")

	out.Reset()
	if code := run(t, &out, "memory", "add", "扣减阈值需配置化", "--scope", "project", "--project", other); code != 0 {
		t.Fatalf("add --project exit %d: %s", code, out.String())
	}

	st, err := store.Open(filepath.Join(home, ".agent-memory", "memory.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	n, err := st.QueryInt(ctx, `SELECT count(*) FROM project`)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		keys, _ := st.QueryStrings(ctx, `SELECT key FROM project ORDER BY key`)
		t.Fatalf("project rows = %d %v, want 1 (same remote must merge)", n, keys)
	}

	projectID, err := st.QueryInt(ctx, `SELECT id FROM project WHERE key = 'example.com/acme/demo'`)
	if err != nil || projectID == 0 {
		t.Fatalf("remote-keyed project not found: id=%d err=%v", projectID, err)
	}
	shared, err := st.QueryInt(ctx,
		`SELECT count(*) FROM memory_item WHERE scope = 'project' AND project_id = ?`, projectID)
	if err != nil {
		t.Fatal(err)
	}
	if shared != 2 {
		t.Fatalf("project-scope memories under project_id %d = %d, want 2 (scan + CLI add)", projectID, shared)
	}
}

// TestDoctor_ReportsSamePathMappedToMultipleProjects 覆盖历史脏数据：
// 同一 path 出现在两个 project_id 下时，doctor 必须给出可见告警（不自动合并）。
func TestDoctor_ReportsSamePathMappedToMultipleProjects(t *testing.T) {
	home, _, _ := isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "init"); code != 0 {
		t.Fatalf("init exit %d", code)
	}

	st, err := store.Open(filepath.Join(home, ".agent-memory", "memory.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	now := time.Now()
	remoteID, err := st.EnsureProject(ctx, "github.com/foo/bar", "bar", "git@github.com:foo/bar.git", `F:\go\dup`, now)
	if err != nil {
		t.Fatal(err)
	}
	pathID, err := st.EnsureProject(ctx, "f:/go/dup", "dup", "", `F:\go\dup`, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.LinkProjectPath(ctx, remoteID, "qoder", "F--go-dup", `F:\go\dup`, "high"); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkProjectPath(ctx, pathID, "mcp", "", `F:\go\dup`, "high"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	for _, want := range []string{"同一路径映射到多个项目", "f:/go/dup", "github.com/foo/bar"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor missing %q:\n%s", want, out.String())
		}
	}
}

// TestBuildAdapters_OrderAndTraeWorkspaceRoots 保证装配顺序稳定（决定扫描与报告顺序），
// 且 [trae] workspace_roots 真的传到适配器；禁用时整家消失但不打乱其余顺序。
func TestBuildAdapters_OrderAndTraeWorkspaceRoots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USERPROFILE", root)
	t.Setenv("HOME", root)
	mustMkdir(t, filepath.Join(root, ".qoder-cn", "memory"))
	mustMkdir(t, filepath.Join(root, ".trae-cn", "memory"))
	mustWrite(t, filepath.Join(root, ".workbuddy", "MEMORY.md"), "- x\n")

	cfg := config.Default(root)
	roots := []string{filepath.Join(root, "ws-cn"), filepath.Join(root, "ws")}
	cfg.Trae.WorkspaceRoots = roots

	adapters := buildAdapters(cfg)
	if got := adapterNames(adapters); !reflect.DeepEqual(got, []string{"qoder", "trae", "workbuddy"}) {
		t.Fatalf("adapter order = %v", got)
	}
	tr, ok := adapters[1].(*trae.Adapter)
	if !ok {
		t.Fatalf("second adapter is %T, want *trae.Adapter", adapters[1])
	}
	if !reflect.DeepEqual(tr.WorkspaceRoots, roots) {
		t.Fatalf("trae workspace roots = %v, want %v", tr.WorkspaceRoots, roots)
	}

	cfg.Trae.Disabled = true
	if got := adapterNames(buildAdapters(cfg)); !reflect.DeepEqual(got, []string{"qoder", "workbuddy"}) {
		t.Fatalf("disabled trae should be dropped, got %v", got)
	}
}
