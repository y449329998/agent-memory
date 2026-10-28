package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// isolateHomeWithRepo 把用户主目录与 Qoder home 指向同一个临时目录。
// 项目记忆必须放在 <slug>/memory/ 下——那是 Qoder 的真实布局，
// 放错位置扫描器看不到它，测试会假绿。
func isolateHomeWithRepo(t *testing.T) (home, qoderHome string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	qoderHome = filepath.Join(home, ".qoder-cn")
	mustMkdir(t, filepath.Join(qoderHome, "memory"))
	mustMkdir(t, filepath.Join(qoderHome, "projects", "demo", "memory"))
	return home, qoderHome
}

func write(t *testing.T, path, content string) {
	t.Helper()
	mustWrite(t, path, content)
}

// seedProjectWithSession 准备一份完整的扫描素材：用户偏好、项目硬约束、
// 一场已停写一小时的会话（mtime 决定是否出卡）。
func seedProjectWithSession(t *testing.T, home, qoderHome string) (projectDir, slug string) {
	t.Helper()
	projectDir = filepath.Join(home, "code", "demo")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	slug = "demo"
	write(t, filepath.Join(qoderHome, "memory", "user.md"),
		"---\nmetadata:\n  type: user\n---\n\n沟通用中文。\n")
	write(t, filepath.Join(qoderHome, "projects", slug, "memory", "m.md"),
		"---\nmetadata:\n  type: feedback\n---\n\n余额扣减需条件更新。\n")
	session := filepath.Join(qoderHome, "projects", slug, "s1.jsonl")
	write(t, session,
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[%q]}\n", projectDir)+
			"{\"type\":\"user\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"给扣减加乐观锁\"}}\n"+
			"{\"type\":\"assistant\",\"sessionId\":\"s1\",\"timestamp\":\"2026-10-27T09:01:00Z\",\"message\":{\"role\":\"assistant\",\"content\":\"未解决：字段类型不一致\"}}\n")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(session, old, old); err != nil {
		t.Fatal(err)
	}
	return projectDir, slug
}

func TestHandoff_WritesFileIntoProjectAndIsReused(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	// scan 也会自动刷新简报；先清掉，确保下面验证的是 handoff 自身的写入路径
	if err := os.RemoveAll(filepath.Join(projectDir, ".agent-memory")); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir); code != 0 {
		t.Fatalf("handoff exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "已更新") {
		t.Fatalf("first handoff should report 已更新, got %q", out.String())
	}
	path := filepath.Join(projectDir, ".agent-memory", "HANDOFF.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("handoff file not written: %v", err)
	}
	text := string(body)
	for _, want := range []string{"## 用户偏好", "## 项目硬约束与约定", "给扣减加乐观锁", "沟通用中文", "余额扣减需条件更新"} {
		if !strings.Contains(text, want) {
			t.Fatalf("brief missing %q:\n%s", want, text)
		}
	}

	// 第二次生成内容不变：mtime 不应被刷新（避免 fsnotify 空转）
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before := st.ModTime()
	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir); code != 0 {
		t.Fatalf("second handoff exit %d", code)
	}
	if !strings.Contains(out.String(), "未变化") {
		t.Fatalf("want 未变化 report, got %q", out.String())
	}
	st2, _ := os.Stat(path)
	if !st2.ModTime().Equal(before) {
		t.Fatalf("file rewritten with identical content")
	}
}

func TestHandoff_AddsProjectDirToGitExclude(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)
	gitInfo := filepath.Join(projectDir, ".git", "info")
	if err := os.MkdirAll(filepath.Join(projectDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	// scan 已写过简报与 exclude；清掉两者，验证 handoff 在内容变化时自行维护
	if err := os.RemoveAll(filepath.Join(projectDir, ".agent-memory")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(gitInfo, "exclude")); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir); code != 0 {
		t.Fatalf("handoff exit %d: %s", code, out.String())
	}
	data, err := os.ReadFile(filepath.Join(gitInfo, "exclude"))
	if err != nil {
		t.Fatalf("exclude not maintained: %v", err)
	}
	if !strings.Contains(string(data), "/.agent-memory/") {
		t.Fatalf("exclude = %q", data)
	}
}

func TestHandoff_PrintToStdoutWithoutWriting(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	// scan 会自动落盘；清掉后 --print 不得把它重新建出来
	if err := os.RemoveAll(filepath.Join(projectDir, ".agent-memory")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir, "--print"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "## 项目概况") {
		t.Fatalf("print mode should emit the brief:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".agent-memory")); !os.IsNotExist(err) {
		t.Fatal("--print must not touch the project directory")
	}
}

func TestHandoff_UnknownProjectAsksForScan(t *testing.T) {
	isolateHomeWithRepo(t)
	var out bytes.Buffer
	code := run(t, &out, "handoff", "--project", filepath.Join(t.TempDir(), "never-scanned"))
	if code != 1 {
		t.Fatalf("unknown project should exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "不认识这个项目") || !strings.Contains(out.String(), "scan") {
		t.Fatalf("error should tell the user to scan first:\n%s", out.String())
	}
}

func TestHandoff_BudgetFlagOverridesConfig(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}

	// 显式 --budget 覆盖配置预算，足够小会触发裁剪
	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir, "--budget", "1"); code != 0 {
		t.Fatalf("handoff --budget 1 exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "budget=1") || !strings.Contains(out.String(), "truncated=true") {
		t.Fatalf("--budget should override config, got %q", out.String())
	}

	// 0 表示用配置值（缺省 1500）
	out.Reset()
	if code := run(t, &out, "handoff", "--project", projectDir, "--budget", "0"); code != 0 {
		t.Fatalf("handoff --budget 0 exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "budget=1500") {
		t.Fatalf("--budget 0 should fall back to configured budget, got %q", out.String())
	}
}

func TestScanRefreshesHandoffsAndSkipsMissingProjects(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	projectDir, _ := seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	path := filepath.Join(projectDir, ".agent-memory", "HANDOFF.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("scan should refresh the handoff automatically: %v", err)
	}
	if !strings.Contains(string(body), "给扣减加乐观锁") {
		t.Fatalf("scan-generated brief lost the task card:\n%s", body)
	}
	if !strings.Contains(out.String(), "已更新") {
		t.Fatalf("scan should report the refreshed handoff, got %q", out.String())
	}

	// 项目目录消失后只跳过：库里留着项目记录，但不去磁盘上重建目录
	if err := os.RemoveAll(projectDir); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("second scan exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("refreshHandoffs must not recreate a missing project dir: %v", err)
	}
}

func TestDoctorReportsSessionsAndCards(t *testing.T) {
	home, qoderHome := isolateHomeWithRepo(t)
	seedProjectWithSession(t, home, qoderHome)

	var out bytes.Buffer
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	for _, want := range []string{"会话", "任务卡"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor missing %q:\n%s", want, out.String())
		}
	}
}
