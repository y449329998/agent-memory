package daemon

import (
	"net/http"
	"testing"
	"time"
)

func TestShutdownEndpointCallsHook(t *testing.T) {
	called := make(chan struct{}, 1)
	s, _ := newTestServer(t, func(c *Config) { c.Shutdown = func() { called <- struct{}{} } })

	rec := webDo(t, s, http.MethodPost, "/v1/shutdown", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("shutdown hook not called")
	}

	rec = webDo(t, s, http.MethodPost, "/v1/shutdown", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: code=%d want 401", rec.Code)
	}
}

func TestDesktopSettingsWithoutDesktopFile(t *testing.T) {
	s, _ := newTestServer(t)

	rec := webDo(t, s, http.MethodGet, "/v1/desktop-settings", webToken, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get code=%d", rec.Code)
	}

	rec = webDo(t, s, http.MethodPost, "/v1/desktop-settings", webToken, map[string]any{"autostart": true}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing desktop.json should be 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
