package workbuddy

import (
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func parse(t *testing.T, content string) []domain.MemoryDraft {
	t.Helper()
	got, err := ParseMemoryFile(`C:\Users\Admin\.workbuddy\MEMORY.md`, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestParseMemoryFile_SectionTypeMapping(t *testing.T) {
	sections := []struct {
		title string
		want  domain.MemoryType
	}{
		{"用户偏好", domain.TypePreference},
		{"User Preferences", domain.TypePreference},
		{"Coding Style", domain.TypePreference},
		{"代码风格（用户多次强调，务必遵守）", domain.TypePreference},
		{"编码规范", domain.TypeConvention},
		{"约定", domain.TypeConvention},
		{"Engineering Conventions", domain.TypeConvention},
		{"工作流程", domain.TypeConvention},
		{"Workflow", domain.TypeConvention},
		{"硬约束", domain.TypeConstraint},
		{"Hard Constraints", domain.TypeConstraint},
		{"禁止事项", domain.TypeConstraint},
		{"教训", domain.TypeLesson},
		{"Lessons Learned", domain.TypeLesson},
		{"经验", domain.TypeLesson},
		{"技术栈", domain.TypeFact},
		{"其他", domain.TypeFact},
	}
	var b strings.Builder
	for _, s := range sections {
		b.WriteString("## " + s.title + "\n- 内容\n")
	}
	got := parse(t, b.String())
	if len(got) != len(sections) {
		t.Fatalf("got %d drafts, want %d", len(got), len(sections))
	}
	for i, s := range sections {
		if got[i].Type != s.want {
			t.Fatalf("section %q mapped to %q, want %q", s.title, got[i].Type, s.want)
		}
	}
}

// TestParseMemoryFile_StylePrefersPreference: 「风格」同时命中 preference 与
// convention，按判定顺序归 preference。
func TestParseMemoryFile_StylePrefersPreference(t *testing.T) {
	got := parse(t, "## 风格与规范\n- a\n")
	if len(got) != 1 || got[0].Type != domain.TypePreference {
		t.Fatalf("got %+v, want preference", got)
	}
}

func TestParseMemoryFile_NearestHeadingWins(t *testing.T) {
	content := "## 工作流程\n- a\n### 硬约束\n- b\n"
	got := parse(t, content)
	for i, want := range []domain.MemoryType{domain.TypeConvention, domain.TypeConstraint} {
		if got[i].Type != want {
			t.Fatalf("draft %d type = %q, want %q", i, got[i].Type, want)
		}
	}
}

func TestParseMemoryFile_NestedBulletsMergeIntoParent(t *testing.T) {
	content := "# 用户偏好\n\n" +
		"## 代码风格（用户多次强调，务必遵守）\n" +
		"- **查询构造器变量不要命名为 `$query`**：会与 `$request->query()` 混淆。\n" +
		"  - **闭包内可以用**（`function ($query) use (...) { ... }`）\n" +
		"  - **闭包外 / 方法体一律用 `$list`**\n" +
		"- 用 `when()` 替代 `if` 链\n" +
		"  * 子项星号\n" +
		"  + 子项加号\n"
	got := parse(t, content)
	if len(got) != 2 {
		t.Fatalf("got %d drafts, want 2: %+v", len(got), got)
	}
	wantFirst := "**查询构造器变量不要命名为 `$query`**：会与 `$request->query()` 混淆。\n" +
		"**闭包内可以用**（`function ($query) use (...) { ... }`）\n" +
		"**闭包外 / 方法体一律用 `$list`**"
	if got[0].Text != wantFirst {
		t.Fatalf("nested text:\n got %q\nwant %q", got[0].Text, wantFirst)
	}
	if got[0].Quote != got[0].Text {
		t.Fatalf("quote not kept in sync: %q", got[0].Quote)
	}
	if got[0].Type != domain.TypePreference {
		t.Fatalf("type = %q", got[0].Type)
	}
	if got[1].Text != "用 `when()` 替代 `if` 链\n子项星号\n子项加号" {
		t.Fatalf("second draft = %q", got[1].Text)
	}
}

func TestParseMemoryFile_ContinuationLineMergesIntoParent(t *testing.T) {
	content := "## 工作流程\n- 第一步\n  继续说明\n\t再一行\n- 第二步\n"
	got := parse(t, content)
	if len(got) != 2 {
		t.Fatalf("got %d drafts", len(got))
	}
	if got[0].Text != "第一步\n继续说明\n再一行" {
		t.Fatalf("continuation not joined: %q", got[0].Text)
	}
	if got[1].Text != "第二步" {
		t.Fatalf("second draft = %q", got[1].Text)
	}
}

func TestParseMemoryFile_SkipsFrontMatterAndNormalizes(t *testing.T) {
	content := "\ufeff---\r\nname: memory\r\ndescription: 说明文字\r\n---\r\n\r\n## 用户偏好\r\n- 中文偏好\r\n"
	got := parse(t, content)
	if len(got) != 1 {
		t.Fatalf("got %d drafts", len(got))
	}
	if got[0].Type != domain.TypePreference || got[0].Text != "中文偏好" {
		t.Fatalf("frontmatter/BOM/CRLF leaked into draft: %+v", got[0])
	}
}

func TestParseMemoryFile_UnclosedFrontMatterTreatedAsBody(t *testing.T) {
	got := parse(t, "---\nname: x\n\n- 正文\n")
	if len(got) != 1 || got[0].Text != "正文" {
		t.Fatalf("unclosed frontmatter should fall back to body: %+v", got)
	}
}

func TestParseMemoryFile_EmptyFilesYieldNothing(t *testing.T) {
	cases := []string{
		"",
		"  \n\t\r\n",
		"---\nname: x\n---\n",
		"# 用户偏好\n\n## 工作流程\n",
	}
	for _, c := range cases {
		if got := parse(t, c); len(got) != 0 {
			t.Fatalf("content %q produced %+v", c, got)
		}
	}
}

func TestParseMemoryFile_SkipsEmptyBullets(t *testing.T) {
	content := "## 硬约束\n-\n-   \n- \t\n-\t有效\n  - \n"
	got := parse(t, content)
	if len(got) != 1 || got[0].Text != "有效" {
		t.Fatalf("empty bullets not skipped: %+v", got)
	}
	if got[0].Type != domain.TypeConstraint {
		t.Fatalf("type = %q", got[0].Type)
	}
	if got := parse(t, "-\n  不应成为记忆\n"); len(got) != 0 {
		t.Fatalf("continuation after empty bullet produced %+v", got)
	}
}

func TestParseMemoryFile_NoHeadingIsFact(t *testing.T) {
	got := parse(t, "- 一条\n* 两条\n+ 三条\n")
	if len(got) != 3 {
		t.Fatalf("got %d drafts", len(got))
	}
	for _, d := range got {
		if d.Type != domain.TypeFact {
			t.Fatalf("type = %q, want fact", d.Type)
		}
	}
}

// TestParseMemoryFile_IndentedTextWithoutParentIsIgnored: 顶层第一个 bullet
// 之前的缩进内容不属于任何条目，不能凭空产出记忆。
func TestParseMemoryFile_IndentedTextWithoutParentIsIgnored(t *testing.T) {
	got := parse(t, "  孤立缩进\n- 正常\n")
	if len(got) != 1 || got[0].Text != "正常" {
		t.Fatalf("got %+v", got)
	}
}
