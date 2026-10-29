package daemon

import (
	"os"
	"strings"
	"testing"
)

func TestIndexPage_Contract(t *testing.T) {
	data, err := os.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)

	mustHave := []string{
		`id="home-view"`, `id="project-view"`, `id="project-grid"`,
		`id="home-user-list"`, `id="project-mem-list"`, `id="project-user-list"`,
		`id="edit-drawer"`, `id="memory-text"`, `id="memory-type"`,
		`id="memory-save"`, `id="memory-stale"`, `id="edit-close"`,
		`id="project-nav"`, `id="sec-sources"`, `id="sec-sessions"`,
		`id="source-list"`, `id="session-list"`,
		`id="task-list"`, `id="handoff-body"`, `id="stat-band"`,
		`id="drawer"`, `id="drawer-close"`, `id="toasts"`, `id="autostart"`,
		`#/p/`, `立即同步`, `生成交接简报`, `用户级记忆`, `包含过时`,
		`prefers-reduced-motion`, `sessionStorage`, `30000`, `focus`, `pointer-events:none`,
	}
	for _, want := range mustHave {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	mustNotHave := []string{
		`innerHTML`, `id="project-select"`, `写入 HANDOFF.md`, `标过时`,
		`复活<`, `得分 `, `读取概览`, `写入目标`, `tokens`,
	}
	for _, bad := range mustNotHave {
		if strings.Contains(page, bad) {
			t.Fatalf("page still contains legacy copy %q", bad)
		}
	}
}
