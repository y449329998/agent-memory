package workbuddy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/muyi99/agent-memory/internal/agent"
)

const (
	wbTranscriptMaxText = 100000
	wbTranscriptMaxTool = 2000
	wbTranscriptMaxErr  = 4000
)

// ParseTranscript 整场 WorkBuddy 会话 → 完整消息：message 全文，
// function_call 存摘要，function_call_result 只在非 completed 时存报错。
func (a *Adapter) ParseTranscript(path string) ([]agent.TranscriptMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseTranscript(io.Reader(f))
}

func parseTranscript(r io.Reader) ([]agent.TranscriptMessage, error) {
	scanner := bufio.NewScanner(r)
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
		var l sessionLine
		if json.Unmarshal(raw, &l) != nil {
			continue
		}
		ts := parseTimestamp(l.Timestamp)
		switch l.Type {
		case "message":
			if l.Role != "user" && l.Role != "assistant" {
				continue
			}
			for _, text := range messageTexts(l.Role, l.Content) {
				appendMsg(agent.TranscriptMessage{
					Role: l.Role, Kind: "text", Text: clip(text, wbTranscriptMaxText), Ts: ts,
					Truncated: len([]rune(text)) > wbTranscriptMaxText,
				})
			}
		case "function_call":
			if l.Name == "" {
				continue
			}
			args := decodeArguments(l.Arguments)
			appendMsg(agent.TranscriptMessage{
				Role: "assistant", Kind: "tool", Tool: l.Name,
				Text:  clip(toolCallSummary(l.Name, args), wbTranscriptMaxTool),
				Files: filesFromArgs(args), Ts: ts,
			})
		case "function_call_result":
			if l.Status == "" || l.Status == "completed" {
				continue
			}
			text := textFrom(l.Output)
			if text == "" {
				text = l.Status
			}
			appendMsg(agent.TranscriptMessage{
				Role: "assistant", Kind: "tool_error", Tool: l.Name,
				Text: clip(text, wbTranscriptMaxErr), Ts: ts,
				Truncated: len([]rune(text)) > wbTranscriptMaxErr,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// messageTexts 拆一条 message 的文本块；用户侧仍做注入过滤，避免把技能正文当对话。
func messageTexts(role string, raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return cleanMessageText(role, s)
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	var out []string
	for _, b := range blocks {
		if b.Type != "text" || b.Text == "" {
			continue
		}
		out = append(out, cleanMessageText(role, b.Text)...)
	}
	return out
}

func cleanMessageText(role, text string) []string {
	if role == "user" {
		text = cleanUserText(text)
		if injected(text) {
			text = summaryGoal(text)
		}
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []string{text}
}

func toolCallSummary(name string, args map[string]any) string {
	switch name {
	case "Bash", "PowerShell":
		if cmd := stringFromArgs(args, "command"); cmd != "" {
			return cmd
		}
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "Read":
		if files := filesFromArgs(args); len(files) > 0 {
			return strings.Join(files, ", ")
		}
	}
	if len(args) == 0 {
		return name
	}
	if b, err := json.Marshal(args); err == nil {
		return string(b)
	}
	return name
}
