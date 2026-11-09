package ingest

import (
	"context"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// writeMessages 把整场会话的完整对话写进库（适配器支持时）。
// 解析失败不阻断扫描：事件与任务已经入库，完整对话下次再补。
func (r *Runner) writeMessages(ctx context.Context, a agent.Adapter, path string, sessionID int64) error {
	tp, ok := a.(agent.TranscriptParser)
	if !ok {
		return nil
	}
	msgs, err := tp.ParseTranscript(path)
	if err != nil {
		return err
	}
	out := make([]domain.SessionMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, domain.SessionMessage{
			SessionID: sessionID, Seq: m.Seq, Role: m.Role, Kind: m.Kind,
			Text: m.Text, Tool: m.Tool, Files: m.Files, Ts: m.Ts, Truncated: m.Truncated,
		})
	}
	return r.store.ReplaceMessages(ctx, sessionID, out)
}
