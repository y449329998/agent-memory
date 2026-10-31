package daemon

import (
	"embed"
	"html/template"
	"net/http"
	"strings"
)

// 多页面壳：页面本身不含数据，也不含令牌；数据一律通过带 Bearer 的 /v1/* 获取。
// 因此页面壳可以公开，API 仍然受保护。
//
//go:embed webui
var webuiFS embed.FS

var webuiPages = func() map[string]*template.Template {
	pages := make(map[string]*template.Template, 7)
	for _, name := range []string{"home", "project", "memories", "sources", "edit", "tasks", "task"} {
		pages[name] = template.Must(template.ParseFS(webuiFS, "webui/layout.html", "webui/"+name+".html"))
	}
	return pages
}()

type pageData struct {
	Title    string
	View     string
	Key      string
	MemoryID string
	TaskID   string
}

func (s *Server) renderPage(w http.ResponseWriter, view string, data pageData) {
	tpl := webuiPages[view]
	if tpl == nil {
		http.Error(w, "page not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := tpl.ExecuteTemplate(w, "layout.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleHomePage(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "home", pageData{Title: "总览", View: "home"})
}

func (s *Server) handleProjectPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "project", pageData{Title: "项目档案", View: "project", Key: r.PathValue("key")})
}

func (s *Server) handleEditPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "edit", pageData{Title: "编辑记忆", View: "edit", MemoryID: r.PathValue("id")})
}

func (s *Server) handleMemoriesPage(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "memories", pageData{Title: "记忆档案", View: "memories"})
}

func (s *Server) handleSourcesPage(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "sources", pageData{Title: "来源", View: "sources"})
}

func (s *Server) handleTasksPage(w http.ResponseWriter, _ *http.Request) {
	s.renderPage(w, "tasks", pageData{Title: "任务", View: "tasks"})
}

func (s *Server) handleTaskPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, "task", pageData{Title: "任务详情", View: "task", TaskID: r.PathValue("id")})
}

func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	switch name {
	case "app.css", "app.js":
	default:
		http.NotFound(w, r)
		return
	}
	data, err := webuiFS.ReadFile("webui/" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctype := "text/css; charset=utf-8"
	if strings.HasSuffix(name, ".js") {
		ctype = "application/javascript; charset=utf-8"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}
