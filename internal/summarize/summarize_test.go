package summarize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func TestClientSummarizeSendsOpenAIRequest(t *testing.T) {
	var gotPath, gotAuth, gotModel, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		gotModel = body.Model
		if len(body.Messages) > 1 {
			gotPrompt = body.Messages[len(body.Messages)-1].Content
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  简洁摘要  "}}]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL+"/v1/", "sk-test", "qwen", 5*time.Second)
	got, err := client.Summarize(context.Background(), "原始内容")
	if err != nil {
		t.Fatal(err)
	}
	if got != "简洁摘要" {
		t.Fatalf("summary = %q", got)
	}
	if gotPath != "/v1/chat/completions" || gotAuth != "Bearer sk-test" || gotModel != "qwen" || !strings.Contains(gotPrompt, "原始内容") {
		t.Fatalf("path=%q auth=%q model=%q prompt=%q", gotPath, gotAuth, gotModel, gotPrompt)
	}
}

func TestClientSummarizeRejectsEmptyAndError(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"empty": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"choices":[]}`))
		},
		"error": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`bad gateway`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if _, err := NewClient(srv.URL, "", "m", time.Second).Summarize(context.Background(), "x"); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestWorkerWritesReadySummary(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	pid, _ := st.EnsureProject(ctx, "f:/worker", "worker", "", `F:\worker`, now)
	memID, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "worker 要处理的记忆原文",
		SourceAgent: "test", SourcePath: "test",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSummaryPending(ctx, "memory", memID, "hash-worker", now); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"worker 摘要"}}]}`))
	}))
	defer srv.Close()

	w := &Worker{Store: st, Client: NewClient(srv.URL, "", "fake", 5*time.Second), MaxInputChar: 100}
	w.runOnce(ctx)

	row, ok, err := st.SummaryFor(ctx, "memory", memID)
	if err != nil || !ok || row.State != "ready" || row.Summary != "worker 摘要" || row.Model != "fake" {
		t.Fatalf("row=%+v ok=%v err=%v", row, ok, err)
	}
}

func TestWorkerRecordsFailure(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	pid, _ := st.EnsureProject(ctx, "f:/worker-fail", "worker-fail", "", `F:\worker-fail`, time.Now())
	memID, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
		Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "失败的记忆",
		SourceAgent: "test", SourcePath: "test",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkSummaryPending(ctx, "memory", memID, "hash-fail", time.Now()); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`boom`))
	}))
	defer srv.Close()

	w := &Worker{Store: st, Client: NewClient(srv.URL, "", "fake", 5*time.Second)}
	w.runOnce(ctx)

	row, _, err := st.SummaryFor(ctx, "memory", memID)
	if err != nil || row.State != "failed" || row.Attempts != 1 || !strings.Contains(row.Error, "500") {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}

func TestWorkerSkipsWhenClientMissing(t *testing.T) {
	w := &Worker{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Run(ctx)
}
