package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestMCPSnippetsUseLivePortAndToken(t *testing.T) {
	s, _ := newTestServer(t, func(c *Config) { c.Port = 47123 })
	rec := webDo(t, s, http.MethodGet, "/v1/mcp/snippets", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out mcpSnippetsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Port != 47123 || out.TokenMasked == "" || out.TokenMasked == webToken {
		t.Fatalf("response = %+v", out)
	}
	if !strings.Contains(out.Snippets["qoder"], webToken) {
		t.Fatalf("copied snippet must contain the real token: %s", out.Snippets["qoder"])
	}
	for _, key := range []string{"qoder", "opencode", "claude", "generic"} {
		if out.Snippets[key] == "" {
			t.Fatalf("missing snippet %q", key)
		}
	}
	if !strings.Contains(out.Snippets["qoder"], "AGENT_MEMORY_TOKEN") ||
		!strings.Contains(out.Snippets["opencode"], `"type": "local"`) ||
		!strings.Contains(out.Snippets["generic"], "args:") {
		t.Fatalf("snippets = %+v", out.Snippets)
	}
}

func TestMCPSnippetsRequiresAuth(t *testing.T) {
	s, _ := newTestServer(t)
	rec := webDo(t, s, http.MethodGet, "/v1/mcp/snippets", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d", rec.Code)
	}
}
