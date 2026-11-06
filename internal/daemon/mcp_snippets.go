package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

type mcpSnippetsResponse struct {
	Port        int               `json:"port"`
	Binary      string            `json:"binary"`
	TokenMasked string            `json:"token_masked"`
	Verified    []string          `json:"verified"`
	Snippets    map[string]string `json:"snippets"`
}

func (s *Server) Port() int {
	s.listenMu.Lock()
	actual := s.actualPort
	s.listenMu.Unlock()
	if actual > 0 {
		return actual
	}
	if s.cfg.Port > 0 {
		return s.cfg.Port
	}
	if port, err := ReadPort(filepath.Join(s.cfg.DataDir, "port")); err == nil {
		return port
	}
	return 0
}

func (s *Server) mcpBinaryPath() string {
	if s.cfg.MCPBinary != "" {
		return s.cfg.MCPBinary
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	if p := filepath.Join(dir, "agent-memory.exe"); p != "" {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return exe
}

func maskToken(token string) string {
	if len(token) <= 8 {
		return "****"
	}
	return token[:4] + "…" + token[len(token)-4:]
}

func (s *Server) mcpSnippets() mcpSnippetsResponse {
	binary := s.mcpBinaryPath()
	port := s.Port()
	portText := strconv.Itoa(port)
	args := []string{"mcp", "--port", portText}
	env := map[string]string{"AGENT_MEMORY_TOKEN": s.cfg.Token}
	marshal := func(v any) string {
		raw, _ := json.MarshalIndent(v, "", "  ")
		return string(raw)
	}
	serverEntry := map[string]any{"command": binary, "args": args, "env": env}
	qoder := marshal(map[string]any{"mcpServers": map[string]any{"agent-memory": serverEntry}})
	claude := marshal(map[string]any{"mcpServers": map[string]any{"agent-memory": serverEntry}})
	opencode := marshal(map[string]any{"mcp": map[string]any{"agent-memory": map[string]any{
		"type":        "local",
		"command":     append([]string{binary}, args...),
		"environment": env,
	}}})
	generic := "command: " + binary + "\nargs: [\"mcp\", \"--port\", \"" + portText + "\"]\nenv:\n  AGENT_MEMORY_TOKEN: " + s.cfg.Token + "\n"
	return mcpSnippetsResponse{
		Port: port, Binary: binary, TokenMasked: maskToken(s.cfg.Token),
		Verified: []string{"qoder", "opencode", "claude"},
		Snippets: map[string]string{
			"qoder": qoder, "opencode": opencode, "claude": claude, "generic": generic,
		},
	}
}

func (s *Server) handleMCPSnippets(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.mcpSnippets())
}
