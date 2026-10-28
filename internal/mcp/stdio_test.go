package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBridge_ForwardsRequestAndResponse(t *testing.T) {
	var gotAuth string
	var gotPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"pong":true}}`)
	}))
	defer upstream.Close()

	br := NewBridge(0, "secret-token")
	br.SetEndpoint(upstream.URL + "/mcp")
	var out bytes.Buffer
	err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/mcp" {
		t.Fatalf("upstream path = %q, want /mcp", gotPath)
	}
	if gotAuth != "Bearer secret-token" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if !strings.Contains(out.String(), `"pong":true`) {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestBridge_DropsNotificationResponsesAndWritesNothing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer upstream.Close()
	br := NewBridge(0, "t")
	br.SetEndpoint(upstream.URL)
	var out bytes.Buffer
	if err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("notification must produce no stdout, got %q", out.String())
	}
}

func TestBridge_ReportsDaemonDownActionably(t *testing.T) {
	br := NewBridge(1, "t") // 端口 1 必然连不上
	var out bytes.Buffer
	err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if err != nil {
		t.Fatalf("transport failure must not kill the loop: %v", err)
	}
	line := strings.TrimSpace(out.String())
	if line == "" {
		t.Fatal("bridge must answer the agent instead of staying silent")
	}
	var msg map[string]any
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("error reply must be valid json: %v\n%s", err, line)
	}
	errObj := msg["error"].(map[string]any)
	text := errObj["message"].(string)
	if !strings.Contains(text, "agent-memory serve") {
		t.Fatalf("error should tell the user what to run: %q", text)
	}
}

func TestBridge_DaemonDownEchoesRequestID(t *testing.T) {
	br := NewBridge(1, "t") // 端口 1 必然连不上
	var out bytes.Buffer
	if err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":42,"method":"ping"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var msg struct {
		ID    json.RawMessage `json:"id"`
		Error map[string]any  `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &msg); err != nil {
		t.Fatalf("error reply must be valid json: %v\n%s", err, out.String())
	}
	// 客户端按 id 关联响应；回 null 会让它一直等到超时。
	if string(msg.ID) != "42" {
		t.Fatalf("reply id = %s, want 42", msg.ID)
	}
	text, _ := msg.Error["message"].(string)
	if !strings.Contains(text, "agent-memory serve") {
		t.Fatalf("error should tell the user what to run: %q", text)
	}
}

func TestBridge_DaemonDownNotificationStaysSilent(t *testing.T) {
	br := NewBridge(1, "t")
	var out bytes.Buffer
	if err := br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	// 通知没有 id，JSON-RPC 不允许回包；传输失败时也不能回。
	if out.Len() != 0 {
		t.Fatalf("notification must not get an error reply, got %q", out.String())
	}
}

func TestBridge_RejectsTokenMismatch(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer upstream.Close()
	br := NewBridge(0, "wrong")
	br.SetEndpoint(upstream.URL)
	var out bytes.Buffer
	br.ServeStdio(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n"), &out)
	if !strings.Contains(out.String(), "token") {
		t.Fatalf("want a token-specific error, got %q", out.String())
	}
}

func TestBridge_SkipsBlankLines(t *testing.T) {
	var count int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer upstream.Close()
	br := NewBridge(0, "t")
	br.SetEndpoint(upstream.URL)
	var out bytes.Buffer
	in := "\n\n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n\n"
	if err := br.ServeStdio(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("blank lines forwarded %d times", count)
	}
	lines := 0
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		lines++
	}
	if lines != 1 {
		t.Fatalf("want 1 response line, got %d", lines)
	}
}
