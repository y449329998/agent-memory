package compose

import (
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestRenderDigestCapsAndClips(t *testing.T) {
	var items []domain.MemoryItem
	for i := 0; i < 10; i++ {
		items = append(items, domain.MemoryItem{Type: domain.TypePreference, Text: strings.Repeat("长", 200)})
	}
	out := renderDigest("## 用户偏好", items, 8, 120)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// 标题 + 空行 + 8 条 + 1 行提示
	if len(lines) != 11 {
		t.Fatalf("want 11 lines got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(out, "另有 2 条") {
		t.Fatalf("missing truncation hint: %s", out)
	}
	for _, line := range lines[2:10] {
		if len([]rune(line)) > 124 {
			t.Fatalf("entry not clipped: %d runes", len([]rune(line)))
		}
	}
}

func TestPreferByTypeOrdersConstraintsFirst(t *testing.T) {
	items := []domain.MemoryItem{
		{Type: domain.TypeLesson, Text: "教训"},
		{Type: domain.TypeConvention, Text: "约定"},
		{Type: domain.TypePreference, Text: "偏好"},
		{Type: domain.TypeConstraint, Text: "约束"},
	}
	out := preferByType(items, []domain.MemoryType{
		domain.TypeConstraint, domain.TypePreference, domain.TypeConvention, domain.TypeLesson,
	})
	want := []string{"约束", "偏好", "约定", "教训"}
	for i, w := range want {
		if out[i].Text != w {
			t.Fatalf("order = %v", []string{out[0].Text, out[1].Text, out[2].Text, out[3].Text})
		}
	}
}
