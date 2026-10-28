package qoder

import (
	"path/filepath"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestParseMemoryFile_FrontMatterTypeMapping(t *testing.T) {
	cases := []struct {
		metadata string
		want     domain.MemoryType
	}{
		{"user", domain.TypePreference},
		{"feedback", domain.TypeConstraint},
		{"project", domain.TypeFact},
		{"reference", domain.TypeFact},
		{"nonsense", domain.TypeFact},
	}
	for _, c := range cases {
		content := "---\nname: x\ndescription: 说明\nmetadata:\n  type: " + c.metadata + "\n---\n\n正文第一段。\n\n第二段。\n"
		got, ok, err := ParseMemoryFile(filepath.Join("dir", c.metadata+".md"), []byte(content))
		if err != nil || !ok {
			t.Fatalf("type %q: ok=%v err=%v", c.metadata, ok, err)
		}
		if got.Type != c.want {
			t.Fatalf("type %q mapped to %q want %q", c.metadata, got.Type, c.want)
		}
		// 整篇正文是一条记忆，frontmatter 不进内容
		if got.Text != "正文第一段。\n\n第二段。" {
			t.Fatalf("body not preserved: %q", got.Text)
		}
	}
}

func TestParseMemoryFile_NoFrontMatterIsFact(t *testing.T) {
	got, ok, err := ParseMemoryFile("/x/plain.md", []byte("# 标题\n\n- 一条没有 frontmatter 的手写记忆\n"))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Type != domain.TypeFact {
		t.Fatalf("type = %q, want fact", got.Type)
	}
	if got.Text == "" {
		t.Fatal("empty text")
	}
}

func TestParseMemoryFile_EmptyBodyFallsBackToDescription(t *testing.T) {
	content := "---\nname: x\ndescription: 只有描述没有正文\nmetadata:\n  type: project\n---\n\n"
	got, ok, err := ParseMemoryFile("/x/d.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Text != "只有描述没有正文" {
		t.Fatalf("ok=%v text=%q", ok, got.Text)
	}
}

func TestParseMemoryFile_SkipsIndexFile(t *testing.T) {
	content := "- [某项目动机](initiative.md) — 索引行不是记忆\n"
	for _, name := range []string{"MEMORY.md", "memory.md"} {
		got, ok, err := ParseMemoryFile(filepath.Join("/x", name), []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("%s should be skipped as an index file, got %+v", name, got)
		}
	}
}

func TestParseMemoryFile_UnclosedFrontMatterTreatedAsBody(t *testing.T) {
	got, ok, err := ParseMemoryFile("/x/u.md", []byte("---\nname: x\n\n正文\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("should still yield an item")
	}
	if got.Type != domain.TypeFact {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Text == "" {
		t.Fatal("empty text")
	}
}

func TestParseMemoryFile_IndentedColonInDescription(t *testing.T) {
	content := "---\nname: x\ndescription: 关键设计: 不能提前扣减余额\nmetadata:\n  type: feedback\n---\n\n正文\n"
	got, _, err := ParseMemoryFile("/x/c.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != domain.TypeConstraint {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Text != "正文" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestParseMemoryFile_BlankFileIsSkipped(t *testing.T) {
	if _, ok, err := ParseMemoryFile("/x/e.md", []byte("  \n\n")); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
