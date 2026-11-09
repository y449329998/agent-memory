package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/muyi99/agent-memory/internal/agent"
)

const (
	transcriptMaxText = 100000
	transcriptMaxTool = 2000
	transcriptMaxErr  = 4000
)

// ParseTranscript 整场会话 → 完整消息；文本全文（单条 100k 上限），
// 工具调用存摘要，工具结果只存报错。
func (a *Adapter) ParseTranscript(path string) ([]agent.TranscriptMessage, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseTranscript(content)
}

func parseTranscript(content []byte) ([]agent.TranscriptMessage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	var out []agent.TranscriptMessage
	seq := 0
	appendMsg := func(m agent.TranscriptMessage) {
		seq++
		m.Seq = seq
		out = append(out, m)
	}
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		raw = bytes.TrimPrefix(raw, []byte("\ufeff"))
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
		ts := parseTimestamp(l.Timestamp)
		var s string
		if json.Unmarshal(l.Message.Content, &s) == nil {
			if s == "" {
				continue
			}
			appendMsg(agent.TranscriptMessage{
				Role: role, Kind: "text", Text: clipRunes(s, transcriptMaxText), Ts: ts,
				Truncated: len([]rune(s)) > transcriptMaxText,
			})
			continue
		}
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text == "" {
					continue
				}
				appendMsg(agent.TranscriptMessage{
					Role: role, Kind: "text", Text: clipRunes(b.Text, transcriptMaxText), Ts: ts,
					Truncated: len([]rune(b.Text)) > transcriptMaxText,
				})
			case "tool_use":
				appendMsg(agent.TranscriptMessage{
					Role: role, Kind: "tool", Tool: b.Name,
					Text: clipRunes(toolSummary(b.Name, b.Input), transcriptMaxTool),
					Files: filesFromInput(b.Input), Ts: ts,
				})
			case "tool_result":
				if !b.IsError {
					continue
				}
				text := textFromContent(b.Content)
				appendMsg(agent.TranscriptMessage{
					Role: role, Kind: "tool_error", Text: clipRunes(text, transcriptMaxErr), Ts: ts,
					Truncated: len([]rune(text)) > transcriptMaxErr,
				})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func toolSummary(name string, input []byte) string {
	switch name {
	case "Bash":
		if cmd := commandFromInput(input); cmd != "" {
			return cmd
		}
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "Read":
		if files := filesFromInput(input); len(files) > 0 {
			return strings.Join(files, ", ")
		}
	}
	return clipRunes(string(input), transcriptMaxTool)
}
