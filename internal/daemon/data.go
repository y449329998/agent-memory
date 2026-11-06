package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/muyi99/agent-memory/internal/store"
)

func (s *Server) handleDataBackup(w http.ResponseWriter, r *http.Request) {
	path, err := s.st.Backup(r.Context(), filepath.Join(s.cfg.DataDir, "backups"))
	if err != nil {
		httpError(w, err)
		return
	}
	info, _ := os.Stat(path)
	size := int64(0)
	at := time.Now()
	if info != nil {
		size = info.Size()
		at = info.ModTime()
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "size": size, "time": at})
}

func (s *Server) handleDataExport(w http.ResponseWriter, r *http.Request) {
	body, err := s.st.ExportData(r.Context())
	if err != nil {
		httpError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="agent-memory-export.json"`)
	w.Write(body)
}

func (s *Server) importDir() string { return filepath.Join(s.cfg.DataDir, "imports") }

// import_token 只允许服务自己生成的 32 位十六进制，防止 ../ 路径穿越。
var importTokenPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (s *Server) handleDataImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "需要 multipart 文件字段 file", http.StatusBadRequest)
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(file)
	if err != nil {
		httpError(w, err)
		return
	}
	preview, err := store.PreviewImport(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.importDir(), 0o700); err != nil {
		httpError(w, err)
		return
	}
	token := make([]byte, 16)
	_, _ = rand.Read(token)
	importToken := hex.EncodeToString(token)
	if err := os.WriteFile(filepath.Join(s.importDir(), importToken+".json"), raw, 0o600); err != nil {
		httpError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"import_token": importToken,
		"projects":     preview.Projects,
		"memories":     preview.Memories,
		"tasks":        preview.Tasks,
		"errors":       preview.Errors,
	})
}

func (s *Server) handleDataImportApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"import_token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		httpError(w, err)
		return
	}
	if !importTokenPattern.MatchString(req.Token) {
		http.Error(w, "import_token is invalid", http.StatusBadRequest)
		return
	}
	path := filepath.Join(s.importDir(), req.Token+".json")
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > 10*time.Minute {
		http.Error(w, "导入文件不存在或已过期", http.StatusBadRequest)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		httpError(w, err)
		return
	}
	backupPath, err := s.st.Backup(r.Context(), filepath.Join(s.cfg.DataDir, "backups"))
	if err != nil {
		httpError(w, err)
		return
	}
	preview, err := s.st.ApplyImport(r.Context(), raw, time.Now())
	if err != nil {
		http.Error(w, fmt.Sprintf("导入失败：%v；导入前备份已保留：%s", err, backupPath), http.StatusInternalServerError)
		return
	}
	_ = os.Remove(path)
	writeJSON(w, http.StatusOK, map[string]any{
		"projects": preview.Projects, "memories": preview.Memories,
		"tasks": preview.Tasks, "errors": preview.Errors,
	})
}

func (s *Server) handleDataOpen(w http.ResponseWriter, _ *http.Request) {
	cmd := exec.Command("explorer", s.cfg.DataDir)
	if runtime.GOOS != "windows" {
		cmd = exec.Command("xdg-open", s.cfg.DataDir)
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("打开目录失败：%v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
