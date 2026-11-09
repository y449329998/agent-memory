// Package tailreader 按字节窗口从 JSONL 文件尾往前读，只读不整份解析。
// 用于任务详情页「加载更早」：44MB 的会话也只读几 MB。
package tailreader

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"time"
)

const (
	windowBytes = 2 * 1024 * 1024
	maxLine     = 1 * 1024 * 1024
)

// Message 是一条回溯出来的对话消息。Truncated 表示单行超过 maxLine 被截断。
type Message struct {
	Role      string    `json:"role"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	Tool      string    `json:"tool"`
	Ts        time.Time `json:"ts"`
	Truncated bool      `json:"truncated"`
}

type jsonlLine struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

// Page 读 [before-window, before) 窗口，返回窗口内最多 limit 条消息（时间正序）
// 与下一次回溯的偏移 next（=窗口起点；到文件头时为 0）。
// before<=0 或超过文件大小时视为文件尾。文件不存在返回 error，由调用方回退。
func Page(path string, before int64, limit int) ([]Message, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	size := st.Size()
	if before <= 0 || before > size {
		before = size
	}
	start := before - windowBytes
	if start < 0 {
		start = 0
	}
	buf := make([]byte, before-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, 0, err
	}
	lines := bytes.Split(buf, []byte("\n"))
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // 窗口起点可能切在半行，丢弃不完整首行
	}
	var all []Message
	for _, raw := range lines {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		trunc := len(raw) > maxLine
		var l jsonlLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		role := l.Message.Role
		if role == "" {
			role = l.Type
		}
		ts, _ := time.Parse(time.RFC3339, l.Timestamp)
		for _, m := range messagesFromContent(role, l.Message.Content) {
			m.Ts = ts
			m.Truncated = trunc
			if trunc {
				m.Text = clipRunes(m.Text, 4000)
			}
			all = append(all, m)
		}
	}
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	return all, start, nil
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func messagesFromContent(role string, raw json.RawMessage) []Message {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []Message{{Role: role, Kind: "text", Text: s}}
	}
	var blocks []block
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []Message
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				out = append(out, Message{Role: role, Kind: "text", Text: b.Text})
			}
		case "tool_use":
			out = append(out, Message{Role: role, Kind: "tool", Tool: b.Name})
		}
	}
	return out
}
