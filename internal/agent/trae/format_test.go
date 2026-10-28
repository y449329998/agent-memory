package trae

import (
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func parse(t *testing.T, name, content string) []domain.MemoryDraft {
	t.Helper()
	got, err := ParseMemoryFile(name, []byte(content))
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
		{"Hard Constraints", domain.TypeConstraint},
		{"Constraints", domain.TypeConstraint},
		{"约束", domain.TypeConstraint},
		{"Engineering Conventions", domain.TypeConvention},
		{"Conventions", domain.TypeConvention},
		{"规范", domain.TypeConvention},
		{"约定", domain.TypeConvention},
		{"Lessons Learned", domain.TypeLesson},
		{"Lessons", domain.TypeLesson},
		{"教训", domain.TypeLesson},
		{"经验", domain.TypeLesson},
		{"User Preferences", domain.TypePreference},
		{"Preferences", domain.TypePreference},
		{"偏好", domain.TypePreference},
		{"Tech Stack", domain.TypeFact},
		{"技术栈", domain.TypeFact},
		{"Something Else", domain.TypeFact},
	}
	var b strings.Builder
	for _, s := range sections {
		b.WriteString("## " + s.title + "\n- 内容\n")
	}
	got := parse(t, "/x/project_memory.md", b.String())
	if len(got) != len(sections) {
		t.Fatalf("got %d drafts, want %d", len(got), len(sections))
	}
	for i, s := range sections {
		if got[i].Type != s.want {
			t.Fatalf("section %q mapped to %q, want %q", s.title, got[i].Type, s.want)
		}
	}
}

func TestParseMemoryFile_SectionTitleIgnoresCaseAndSpaces(t *testing.T) {
	content := "##  HARd   ConSTRAINTS  \n- a\n##   lessons   learned:  \n- b\n##  偏好 :\n- c\n"
	got := parse(t, "/x/m.md", content)
	if len(got) != 3 {
		t.Fatalf("got %d drafts", len(got))
	}
	want := []domain.MemoryType{domain.TypeConstraint, domain.TypeLesson, domain.TypePreference}
	for i, w := range want {
		if got[i].Type != w {
			t.Fatalf("draft %d type = %q, want %q", i, got[i].Type, w)
		}
	}
}

func TestParseMemoryFile_ContinuationLinesJoinPreviousBullet(t *testing.T) {
	content := "## Hard Constraints\n" +
		"- 第一行\n" +
		"  续行一\n" +
		"\t续行二\n" +
		"- 第二条\n"
	got := parse(t, "/x/m.md", content)
	if len(got) != 2 {
		t.Fatalf("got %d drafts, want 2", len(got))
	}
	if got[0].Text != "第一行\n续行一\n续行二" {
		t.Fatalf("continuation not joined: %q", got[0].Text)
	}
	if got[0].Quote != got[0].Text {
		t.Fatalf("quote not kept in sync: %q", got[0].Quote)
	}
	if got[1].Text != "第二条" {
		t.Fatalf("second bullet = %q", got[1].Text)
	}
}

func TestParseMemoryFile_NestedBulletIsItsOwnMemory(t *testing.T) {
	got := parse(t, "/x/m.md", "- 父项\n  - 子项\n")
	if len(got) != 2 || got[0].Text != "父项" || got[1].Text != "子项" {
		t.Fatalf("nested bullets should not merge: %+v", got)
	}
}

func TestParseMemoryFile_SkipsEmptyBullets(t *testing.T) {
	content := "## Hard Constraints\n-\n-   \n- \t\n-\t有效\n"
	got := parse(t, "/x/m.md", content)
	if len(got) != 1 || got[0].Text != "有效" {
		t.Fatalf("empty bullets not skipped: %+v", got)
	}
	if got[0].Type != domain.TypeConstraint {
		t.Fatalf("type = %q", got[0].Type)
	}

	// 空 bullet 之后的缩进行不能被当成一条记忆
	if got := parse(t, "/x/m.md", "-\n  不应成为记忆\n"); len(got) != 0 {
		t.Fatalf("continuation after empty bullet produced %+v", got)
	}
}

func TestParseMemoryFile_SkipsFrontMatterAndNormalizes(t *testing.T) {
	content := "\ufeff---\r\nname: project\r\ndescription: 说明\r\n---\r\n\r\n## Hard Constraints\r\n- 中文约束\r\n"
	got := parse(t, "/x/project_memory.md", content)
	if len(got) != 1 {
		t.Fatalf("got %d drafts", len(got))
	}
	if got[0].Type != domain.TypeConstraint {
		t.Fatalf("type = %q", got[0].Type)
	}
	if got[0].Text != "中文约束" {
		t.Fatalf("frontmatter/BOM/CRLF leaked into text: %q", got[0].Text)
	}
}

func TestParseMemoryFile_SkipsIndexFile(t *testing.T) {
	for _, name := range []string{"MEMORY.md", "memory.md"} {
		if got := parse(t, "/x/"+name, "## Hard Constraints\n- a\n"); len(got) != 0 {
			t.Fatalf("%s should be skipped, got %+v", name, got)
		}
	}
}

func TestParseMemoryFile_EmptyFilesYieldNothing(t *testing.T) {
	cases := []string{"", "  \n\t\r\n", "---\nname: x\n---\n"}
	for _, c := range cases {
		if got := parse(t, "/x/e.md", c); len(got) != 0 {
			t.Fatalf("content %q produced %+v", c, got)
		}
	}
}

func TestParseMemoryFile_UnclosedFrontMatterTreatedAsBody(t *testing.T) {
	got := parse(t, "/x/u.md", "---\nname: x\n\n- 正文\n")
	if len(got) != 1 || got[0].Text != "正文" {
		t.Fatalf("unclosed frontmatter should fall back to body: %+v", got)
	}
}

func TestParseMemoryFile_NoHeadingIsFact(t *testing.T) {
	got := parse(t, "/x/plain.md", "- 一条\n- 两条\n")
	if len(got) != 2 {
		t.Fatalf("got %d drafts", len(got))
	}
	for _, d := range got {
		if d.Type != domain.TypeFact {
			t.Fatalf("type = %q, want fact", d.Type)
		}
	}
}

func TestParseRuleFile_WholeFileAsPreference(t *testing.T) {
	path := `C:\Users\Admin\.trae-cn\user_rules\rule-1.md`
	got, err := ParseRuleFile(path, []byte("\ufeff第一行\r\n第二行  \r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d drafts", len(got))
	}
	d := got[0]
	if d.Type != domain.TypePreference {
		t.Fatalf("type = %q", d.Type)
	}
	if d.Text != "第一行\n第二行" {
		t.Fatalf("text = %q", d.Text)
	}
	if d.SourcePath != path || d.Quote != d.Text {
		t.Fatalf("source/quote not set: %+v", d)
	}

	if got, _ := ParseRuleFile(path, []byte(" \r\n\t")); len(got) != 0 {
		t.Fatalf("blank rule produced %+v", got)
	}
}
