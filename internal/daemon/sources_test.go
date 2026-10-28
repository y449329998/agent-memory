package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestSourcesEndpoint(t *testing.T) {
	s, st := newTestServer(t)
	ctx := context.Background()
	if err := st.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1", Size: 11}, 100); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSourceError(ctx, "trae", "memory", "/t/b.md", "bad", 200); err != nil {
		t.Fatal(err)
	}

	rec := webDo(t, s, http.MethodGet, "/v1/sources", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Sources []struct {
			Agent     string `json:"agent"`
			Path      string `json:"path"`
			LastError string `json:"last_error"`
		} `json:"sources"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 2 {
		t.Fatalf("count=%d want 2", out.Count)
	}

	rec = webDo(t, s, http.MethodGet, "/v1/sources?failed=1", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("failed filter code=%d", rec.Code)
	}
	out = struct {
		Sources []struct {
			Agent     string `json:"agent"`
			Path      string `json:"path"`
			LastError string `json:"last_error"`
		} `json:"sources"`
		Count int `json:"count"`
	}{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || out.Sources[0].Agent != "trae" {
		t.Fatalf("failed filter: %+v", out)
	}

	rec = webDo(t, s, http.MethodGet, "/v1/sources?agent=qoder", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent filter code=%d", rec.Code)
	}
	out.Count = 0
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Count != 1 || out.Sources[0].Path != "/q/a.md" {
		t.Fatalf("agent filter: %+v", out)
	}

	rec = webDo(t, s, http.MethodGet, "/v1/sources", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code=%d want 401", rec.Code)
	}
}
