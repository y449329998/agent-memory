package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestPagesServeShells(t *testing.T) {
	s, _ := newTestServer(t)
	cases := []struct {
		path   string
		marker string
	}{
		{"/", `data-view="home"`},
		{"/p/e%3A%2Fcompany%2Fnuonuo-fresh3-api", `data-view="project"`},
		{"/m/123", `data-view="edit"`},
		{"/memories", `data-view="memories"`},
		{"/sources", `data-view="sources"`},
		{"/tasks", `data-view="tasks"`},
		{"/t/new", `data-view="task"`},
		{"/t/1", `data-view="task" data-task-id="1"`},
		{"/mcp", `data-view="mcp"`},
		{"/settings", `data-view="settings"`},
	}
	for _, c := range cases {
		rec := webDo(t, s, http.MethodGet, c.path, "", nil, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: code = %d", c.path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, c.marker) {
			t.Fatalf("%s: missing %q", c.path, c.marker)
		}
		if !strings.Contains(body, "/assets/app.css") || !strings.Contains(body, "/assets/app.js") {
			t.Fatalf("%s: page must reference shared assets", c.path)
		}
		if strings.Contains(body, "Bearer ") {
			t.Fatalf("%s: token must not be embedded in the shell", c.path)
		}
	}
	rec := webDo(t, s, http.MethodGet, "/p/e%3A%2Fcompany%2Fnuonuo-fresh3-api", "", nil, nil)
	if !strings.Contains(rec.Body.String(), `data-key="e:/company/nuonuo-fresh3-api"`) {
		t.Fatalf("project key must be decoded into data-key: %s", rec.Body.String())
	}
	for path, marker := range map[string]string{
		"/mcp": `id="mcp-code"`, "/settings": `class="settings-layout"`,
		"/tasks": `id="tf-kind"`, "/": `id="stat-tasks"`,
	} {
		rec := webDo(t, s, http.MethodGet, path, "", nil, nil)
		if !strings.Contains(rec.Body.String(), marker) {
			t.Fatalf("%s missing %q", path, marker)
		}
	}
	if rec := webDo(t, s, http.MethodGet, "/", "", nil, nil); strings.Contains(rec.Body.String(), "失败源") {
		t.Fatal("首页数字磁贴应已换成任务数，不再显示失败源")
	}
	if rec := webDo(t, s, http.MethodGet, "/", "", nil, nil); !strings.Contains(rec.Body.String(), `href="/assets/app.png"`) {
		t.Fatal("页面必须挂上海豚图标作为 favicon")
	}
}

func TestAssets(t *testing.T) {
	s, _ := newTestServer(t)
	rec := webDo(t, s, http.MethodGet, "/assets/app.css", "", nil, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("css: code=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "--bg") {
		t.Fatal("css should define the paper palette")
	}
	rec = webDo(t, s, http.MethodGet, "/assets/app.js", "", nil, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/javascript") {
		t.Fatalf("js: code=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if !strings.Contains(rec.Body.String(), "initHome") {
		t.Fatal("js should contain the page bootstrap")
	}
	rec = webDo(t, s, http.MethodGet, "/assets/app.png", "", nil, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.Len() < 512 {
		t.Fatalf("icon: code=%d type=%q bytes=%d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}
	rec = webDo(t, s, http.MethodGet, "/assets/secret.txt", "", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown asset: code = %d, want 404", rec.Code)
	}
}

func TestGetMemoryEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := st.EnsureProject(ctx, "f:/ui-item", "ui-item", "", `F:\ui-item`, now)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "编辑页用的单条记忆",
		SourceAgent: "qoder", SourcePath: "/a.md",
	}, now)
	if err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodGet, "/v1/memory/item?id="+strconv.FormatInt(id, 10), webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rec.Code, rec.Body.String())
	}
	var m domain.MemoryItem
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.ID != id || m.Text != "编辑页用的单条记忆" {
		t.Fatalf("memory = %+v", m)
	}
	rec = webDo(t, s, http.MethodGet, "/v1/memory/item?id="+strconv.FormatInt(id, 10), "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("without token: code = %d, want 401", rec.Code)
	}
	rec = webDo(t, s, http.MethodGet, "/v1/memory/item?id=99999", webToken, nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing: code = %d, want 404", rec.Code)
	}
}

func TestWebUIStaticContract(t *testing.T) {
	files, err := filepath.Glob("webui/*")
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	for _, f := range files {
		if filepath.Ext(f) == ".png" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		all.Write(data)
		all.WriteString("\n")
	}
	page := all.String()
	for _, want := range []string{
		"token-gate", "/assets/app.css", "/assets/app.js",
		"/memories", "/sources", "/m/", "/p/",
		"prefers-reduced-motion", "view-transition", "textContent",
		"/v1/memory/update", "offset",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("webui missing %q", want)
		}
	}
	for _, bad := range []string{"innerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(page, bad) {
			t.Fatalf("webui must not use %q for dynamic content", bad)
		}
	}
}
