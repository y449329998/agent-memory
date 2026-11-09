package tailreader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func line(role, text string) string {
	return `{"type":"` + role + `","message":{"role":"` + role + `","content":"` + text + `"}}`
}

func TestPageReadsTailWithoutWholeFile(t *testing.T) {
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, line("user", strings.Repeat("x", 20000)))
	}
	p := writeLines(t, lines)
	msgs, next, err := Page(p, 0, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("want 5 msgs got %d", len(msgs))
	}
	if next <= 0 {
		t.Fatalf("want next>0 for a file bigger than window, got %d", next)
	}
	older, next2, err := Page(p, next, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 5 || next2 >= next {
		t.Fatalf("older page wrong: n=%d next2=%d next=%d", len(older), next2, next)
	}
}

func TestPageDropsPartialFirstLine(t *testing.T) {
	// 首行 3MB > 2MB 窗口：窗口起点切在首行中间，不完整首行应被丢弃
	p := writeLines(t, []string{line("user", strings.Repeat("a", 3*1024*1024)), line("user", "bbb"), line("user", "ccc")})
	msgs, _, err := Page(p, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("want only bbb/ccc after dropping partial first line, got %d", len(msgs))
	}
	for _, m := range msgs {
		if strings.Contains(m.Text, "aaa") {
			t.Fatalf("partial first line leaked: %+v", m)
		}
	}
}

func TestPageTruncatesHugeLine(t *testing.T) {
	p := writeLines(t, []string{line("user", strings.Repeat("y", 1500000))})
	msgs, _, err := Page(p, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || !msgs[0].Truncated {
		t.Fatalf("huge line should be truncated: %+v", msgs)
	}
}

func TestPageMissingFile(t *testing.T) {
	_, _, err := Page(filepath.Join(t.TempDir(), "nope.jsonl"), 0, 5)
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestPageReachedStart(t *testing.T) {
	p := writeLines(t, []string{line("user", "only")})
	_, next, err := Page(p, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if next != 0 {
		t.Fatalf("small file should report next=0, got %d", next)
	}
}
