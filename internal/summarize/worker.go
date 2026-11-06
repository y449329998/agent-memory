package summarize

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/store"
)

type Worker struct {
	Store        *store.Store
	Client       *Client
	Interval     time.Duration
	MaxInputChar int
	Log          *log.Logger
}

func (w *Worker) Run(ctx context.Context) {
	if w.Client == nil || w.Store == nil {
		return
	}
	interval := w.Interval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	_ = w.Store.ResetProcessingSummaries(ctx, time.Now())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	for {
		job, ok, err := w.Store.NextSummary(ctx, time.Now())
		if err != nil {
			w.logf("claim summary: %v", err)
			return
		}
		if !ok {
			return
		}
		var source string
		switch job.Kind {
		case "memory":
			source, err = w.Store.MemoryText(ctx, job.ID)
		case "task":
			source, err = w.Store.TaskSummarySource(ctx, job.ID)
		default:
			err = fmt.Errorf("unknown summary kind %q", job.Kind)
		}
		if err != nil {
			_ = w.Store.FailSummary(ctx, job.Kind, job.ID, err.Error(), time.Now())
			continue
		}
		source = strings.TrimSpace(source)
		if limit := w.MaxInputChar; limit > 0 && len([]rune(source)) > limit {
			source = string([]rune(source)[:limit])
		}
		prompt := "请把下面内容压缩成简洁中文摘要，保留关键事实、约束、文件和下一步，不要编造：\n\n" + source
		summary, err := w.Client.Summarize(ctx, prompt)
		if err != nil {
			_ = w.Store.FailSummary(ctx, job.Kind, job.ID, err.Error(), time.Now())
			w.logf("summary %s/%d: %v", job.Kind, job.ID, err)
			continue
		}
		summary = truncateRunes(summary, 1200)
		if err := w.Store.SaveSummary(ctx, job.Kind, job.ID, job.SourceHash, summary, w.Client.Model, time.Now()); err != nil {
			w.logf("save summary %s/%d: %v", job.Kind, job.ID, err)
		}
	}
}

func (w *Worker) logf(format string, args ...any) {
	if w.Log != nil {
		w.Log.Printf(format, args...)
	}
}
