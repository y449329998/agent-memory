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
		`id="stat-band"`, `data-drawer="projects"`, `data-drawer="memories"`,
		`data-drawer="stale"`, `data-drawer="sources"`, `data-drawer="failed"`,
		`id="drawer"`, `id="drawer-close"`, `id="project-select"`,
		`id="search-input"`, `id="task-list"`, `id="handoff-body"`,
		`立即同步`, `生成交接简报`, `不再使用`, `恢复使用`, `约 `,
		`prefers-reduced-motion`, `localStorage`, `30000`, `focus`, `id="autostart"`,
		// .drawer 的 display:flex 会覆盖 UA 的 [hidden]{display:none}，
		// 必须显式把关闭态藏掉并禁用命中，否则透明抽屉会挡住顶栏按钮。
		`.drawer[hidden]`, `pointer-events:none`,
	}
	for _, want := range mustHave {
		if !strings.Contains(page, want) {
			t.Fatalf("page missing %q", want)
		}
	}

	mustNotHave := []string{
		`写入 HANDOFF.md`, `标过时`, `复活<`, `得分 `, `innerHTML`,
		`读取概览`, `写入目标`, `tokens`,
	}
	for _, bad := range mustNotHave {
		if strings.Contains(page, bad) {
			t.Fatalf("page still contains legacy copy %q", bad)
		}
	}
}
