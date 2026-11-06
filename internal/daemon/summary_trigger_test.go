package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSummaryTriggerOnlyWhenLLMReady(t *testing.T) {
	enabled := false
	s, st := newTestServer(t, func(c *Config) {
		c.SummaryEnabled = func() bool { return enabled }
	})
	ctx := context.Background()

	rec := webDo(t, s, http.MethodPost, "/v1/memory", webToken, map[string]any{
		"text": "未配置模型时不该排队", "type": "fact", "scope": "user",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("add code=%d body=%s", rec.Code, rec.Body.String())
	}
	var first struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.SummaryFor(ctx, "memory", first.ID); err != nil || ok {
		t.Fatalf("summary should not be queued when disabled, ok=%v err=%v", ok, err)
	}

	enabled = true
	rec = webDo(t, s, http.MethodPost, "/v1/memory", webToken, map[string]any{
		"text": "启用模型后进入摘要队列", "type": "fact", "scope": "user",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("add code=%d body=%s", rec.Code, rec.Body.String())
	}
	var second struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	row, ok, err := st.SummaryFor(ctx, "memory", second.ID)
	if err != nil || !ok || row.State != "pending" {
		t.Fatalf("summary row=%+v ok=%v err=%v", row, ok, err)
	}
}

func TestTaskSummaryQueuedAndReturned(t *testing.T) {
	enabled := true
	s, st := newTestServer(t, func(c *Config) {
		c.SummaryEnabled = func() bool { return enabled }
	})
	ctx := context.Background()
	now := time.Now()
	if _, err := st.EnsureProject(ctx, "f:/summary-task", "summary-task", "", `F:\summary-task`, now); err != nil {
		t.Fatal(err)
	}
	rec := webDo(t, s, http.MethodPost, "/v1/tasks", webToken, map[string]any{
		"project_path": "f:/summary-task", "goal": "需要摘要的任务",
		"next_steps": "继续验证", "steps": []map[string]any{{"subject": "验证摘要"}},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("create code=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	row, ok, err := st.SummaryFor(ctx, "task", created.ID)
	if err != nil || !ok || row.State != "pending" {
		t.Fatalf("summary row=%+v ok=%v err=%v", row, ok, err)
	}

	if _, _, err := st.NextSummary(ctx, now); err != nil {
		t.Fatal(err)
	}
	row, _, err = st.SummaryFor(ctx, "task", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSummary(ctx, "task", created.ID, row.SourceHash, "这是任务摘要", "fake", now); err != nil {
		t.Fatal(err)
	}

	rec = webDo(t, s, http.MethodGet, "/v1/tasks?project_key=f%3A%2Fsummary-task", webToken, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "这是任务摘要") {
		t.Fatalf("list should expose summary: code=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = webDo(t, s, http.MethodGet, fmt.Sprintf("/v1/tasks/item?id=%d", created.ID), webToken, nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"summary":"这是任务摘要"`) {
		t.Fatalf("detail should expose summary: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMemoryUpdateRequeuesSummary(t *testing.T) {
	enabled := true
	s, st := newTestServer(t, func(c *Config) {
		c.SummaryEnabled = func() bool { return enabled }
	})
	ctx := context.Background()
	rec := webDo(t, s, http.MethodPost, "/v1/memory", webToken, map[string]any{
		"text": "原始内容", "type": "fact", "scope": "user",
	}, nil)
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	row, _, _ := st.SummaryFor(ctx, "memory", created.ID)
	if row.State != "pending" {
		t.Fatalf("expected pending, got %+v", row)
	}
	// 先标记 ready，再编辑正文；新 hash 应重新排队。
	if err := st.SaveSummary(ctx, "memory", created.ID, row.SourceHash, "旧摘要", "fake", time.Now()); err != nil {
		t.Fatal(err)
	}
	rec = webDo(t, s, http.MethodPost, "/v1/memory/update", webToken, map[string]any{
		"id": created.ID, "text": "修改后的内容", "type": "fact",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update code=%d body=%s", rec.Code, rec.Body.String())
	}
	row, _, err := st.SummaryFor(ctx, "memory", created.ID)
	if err != nil || row.State != "pending" || row.Summary != "" {
		t.Fatalf("row=%+v err=%v", row, err)
	}
}
