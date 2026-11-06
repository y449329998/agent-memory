package daemon

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func waitPortFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if port, err := ReadPort(path); err == nil && port > 0 {
			return port
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port file %s not ready", path)
	return 0
}

func TestServerRebindPort(t *testing.T) {
	s, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	portPath := filepath.Join(t.TempDir(), "port")
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx, portPath) }()
	oldPort := waitPortFile(t, portPath)

	if err := s.Rebind(ctx, 0); err != nil {
		t.Fatal(err)
	}
	newPort := waitPortFile(t, portPath)
	if newPort == oldPort {
		t.Fatalf("port did not change: %d", newPort)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/healthz", newPort))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz code = %d", resp.StatusCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ListenAndServe did not stop")
	}
}

func TestServerRebindKeepsOldListenerOnFailure(t *testing.T) {
	s, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	portPath := filepath.Join(t.TempDir(), "port")
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe(ctx, portPath) }()
	oldPort := waitPortFile(t, portPath)

	blocked, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Close()
	busyPort := blocked.Addr().(*net.TCPAddr).Port
	if err := s.Rebind(ctx, busyPort); err == nil {
		t.Fatal("rebind to an occupied port should fail")
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/healthz", oldPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("old listener should stay healthy, code=%d", resp.StatusCode)
	}
	cancel()
	<-done
}
