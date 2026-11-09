package daemon

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/muyi99/agent-memory/internal/tailreader"
)

// handleTaskConversation 对话区数据源：完整对话表优先，回溯按 seq 游标；
// 旧库还没有消息时回退 tailreader 读源 jsonl。
func (s *Server) handleTaskConversation(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	beforeSeq, _ := strconv.ParseInt(r.URL.Query().Get("before_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	detail, ok, err := s.st.TaskDetail(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	if sessionID, err := s.st.MainSessionForTask(r.Context(), id); err == nil && sessionID > 0 {
		if has, _ := s.st.HasMessages(r.Context(), sessionID); has {
			msgs, next, err := s.st.MessagesForSession(r.Context(), sessionID, beforeSeq, limit)
			if err != nil {
				httpError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"source": "db", "messages": msgs, "next": next})
			return
		}
	}
	path := detail.SourcePath
	if path == "" && len(detail.Sessions) > 0 {
		path = detail.Sessions[0].Path
	}
	if before == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"source": "store", "messages": detail.Snippets, "next": 0, "file_size": fileSize(path),
		})
		return
	}
	msgs, next, err := tailreader.Page(path, before, limit)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"source": "store_only", "messages": detail.Snippets, "next": 0,
			"warning": "源文件不可读，只显示库里最近 20 条",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": "file", "messages": msgs, "next": next})
}

func fileSize(path string) int64 {
	if path == "" {
		return 0
	}
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

func (s *Server) handleTaskFiles(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("task_id"), 10, 64)
	files, err := s.st.TaskFiles(r.Context(), id)
	if err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (s *Server) handleStepStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID    int64  `json:"task_id"`
		SourceKey string `json:"source_key"`
		Status    string `json:"status"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if err := s.st.SetStepOverride(r.Context(), req.TaskID, req.SourceKey, req.Status, time.Now()); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
