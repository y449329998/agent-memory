package qoder

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

const (
	maxEventRunes = 500
	maxTitleRunes = 80
	// 技能正文与工具说明会被当成 user 消息灌进来，长度是明显特征；
	// 真实目标陈述极少超过这个长度。
	skillInjectionRunes = 4000
)

// 会话 JSONL 的行结构。timestamp 既可能是 RFC3339 字符串也可能是毫秒数字，
// message.content 既可能是字符串也可能是 block 数组——两者都要兼容。
type jsonlLine struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	SessionID   string          `json:"sessionId"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Cwd         string          `json:"cwd"`
	IsSidechain bool            `json:"isSidechain"`
	Directories []string        `json:"directories"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

func parseSessionJSONL(path string, content []byte) (domain.Session, []agent.SessionEvent, error) {
	sess := domain.Session{Agent: "qoder", Path: path}
	var events []agent.SessionEvent
	seq := 0

	scanner := bufio.NewScanner(bytes.NewReader(content))
	// 真实会话单行可达上百 KB，默认 64KB 上限会直接截断；16MB 留足余量。
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		raw = bytes.TrimPrefix(raw, []byte("\ufeff"))
		var l jsonlLine
		if err := json.Unmarshal(raw, &l); err != nil {
			// 单行坏 JSON 只丢这一行，不中断整个文件
			continue
		}
		ts := parseTimestamp(l.Timestamp)

		switch l.Type {
		case "workspace-directories":
			if l.SessionID != "" && sess.ExternalID == "" {
				sess.ExternalID = l.SessionID
			}
			if len(l.Directories) > 0 {
				sess.ProjectPath = l.Directories[0]
				sess.Confidence = "high"
			}
			continue
		case "user", "assistant":
			// 下面按事件内容处理
		default:
			// runtime-config / queue-operation / last-prompt / ai-title 以及未知类型
			continue
		}

		if l.SessionID != "" && sess.ExternalID == "" {
			sess.ExternalID = l.SessionID
		}
		// 子 agent 的输出不是用户目标，整行丢弃。
		if l.IsSidechain {
			continue
		}
		role := l.Message.Role
		if role == "" {
			role = l.Type
		}
		for _, ev := range eventsFromContent(role, ts, l.Message.Content) {
			seq++
			ev.Seq = seq
			events = append(events, ev)
		}
	}
	if err := scanner.Err(); err != nil {
		return sess, nil, err
	}
	if sess.ExternalID == "" {
		sess.ExternalID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	for _, e := range events {
		if e.Ts.IsZero() {
			continue
		}
		if sess.StartedAt.IsZero() || e.Ts.Before(sess.StartedAt) {
			sess.StartedAt = e.Ts
		}
		if sess.UpdatedAt.IsZero() || e.Ts.After(sess.UpdatedAt) {
			sess.UpdatedAt = e.Ts
		}
		if sess.Title == "" && e.Kind == domain.KindUserText {
			sess.Title = clipRunes(e.Text, maxTitleRunes)
		}
	}
	sess.EventCount = len(events)
	return sess, events, nil
}

// eventsFromContent 把一行消息的 content 拆成 0..n 条事件。
// 一条 assistant 消息常同时带文本与多个工具调用，不能只取第一块。
func eventsFromContent(role string, ts time.Time, raw json.RawMessage) []agent.SessionEvent {
	if len(raw) == 0 {
		return nil
	}
	newEvent := func(kind domain.EventKind) agent.SessionEvent {
		return agent.SessionEvent{Role: role, Kind: kind, Ts: ts}
	}

	// 字符串 content：普通用户消息或 assistant 纯文本
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		text := clipRunes(s, maxEventRunes)
		if text == "" {
			return nil
		}
		switch role {
		case "user":
			if isInjected(s) {
				return nil
			}
			ev := newEvent(domain.KindUserText)
			ev.Text = text
			return []agent.SessionEvent{ev}
		case "assistant":
			ev := newEvent(domain.KindAssistant)
			ev.Text = text
			return []agent.SessionEvent{ev}
		}
		return nil
	}

	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []agent.SessionEvent
	for _, b := range blocks {
		switch b.Type {
		case "text":
			text := clipRunes(b.Text, maxEventRunes)
			if text == "" {
				continue
			}
			switch role {
			case "user":
				if isInjected(b.Text) {
					continue
				}
				ev := newEvent(domain.KindUserText)
				ev.Text = text
				out = append(out, ev)
			case "assistant":
				ev := newEvent(domain.KindAssistant)
				ev.Text = text
				out = append(out, ev)
			}
		case "tool_use":
			switch b.Name {
			case "Edit", "Write", "MultiEdit", "NotebookEdit":
				ev := newEvent(domain.KindToolEdit)
				ev.Tool = b.Name
				ev.Files = filesFromInput(b.Input)
				out = append(out, ev)
			case "Bash":
				ev := newEvent(domain.KindToolCommand)
				ev.Tool = b.Name
				ev.Text = commandFromInput(b.Input)
				out = append(out, ev)
			case "Read", "Glob", "Grep", "LS":
				// 读取类事件对交接没有信息量，丢掉，否则任务卡会被文件清单淹没
			default:
				// 其余工具只保留名字，内容不展开。
				ev := newEvent(domain.KindOther)
				ev.Tool = b.Name
				out = append(out, ev)
			}
		case "tool_result":
			if !b.IsError {
				continue
			}
			ev := newEvent(domain.KindToolError)
			ev.Text = clipRunes(textFromContent(b.Content), maxEventRunes)
			out = append(out, ev)
		}
	}
	return out
}

func isInjected(s string) bool {
	if len([]rune(s)) > skillInjectionRunes {
		return true
	}
	for _, p := range []string{"<command-message>", "<command-name>", "<system-reminder>", "<local-command-caveat>"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return strings.Contains(s, "Base directory for this skill:")
}

func filesFromInput(raw json.RawMessage) []string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if v, ok := m[key].(string); ok && v != "" {
			out = append(out, v)
		}
	}
	if edits, ok := m["edits"].([]any); ok {
		for _, e := range edits {
			if em, ok := e.(map[string]any); ok {
				if v, ok := em["file_path"].(string); ok && v != "" {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

func commandFromInput(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	if cmd, ok := m["command"].(string); ok {
		return clipRunes(cmd, maxEventRunes)
	}
	return ""
}

func textFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// parseTimestamp 同时接受 "2026-10-27T08:26:02.995Z" 与 1793089562320。
// 认不出来时返回零值，让调用方自己决定要不要忽略这条事件。
func parseTimestamp(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00"} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC()
			}
		}
		return time.Time{}
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		if ms > 1_000_000_000_000 {
			return time.UnixMilli(ms).UTC()
		}
		return time.Unix(ms, 0).UTC()
	}
	return time.Time{}
}

func clipRunes(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func (a *Adapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	var out []agent.SessionUnit
	projectsDir := filepath.Join(a.Home, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		dir := filepath.Join(projectsDir, name)
		files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			return out, err
		}
		sort.Strings(files)
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			u := agent.SessionUnit{Path: f}
			content, err := os.ReadFile(f)
			if err != nil {
				u.Err = err
				out = append(out, u)
				continue
			}
			u.Content = content
			u.Size = int64(len(content))
			if st, err := os.Stat(f); err == nil {
				u.MTime = st.ModTime()
			}
			sess, events, err := parseSessionJSONL(f, content)
			if err != nil {
				u.Err = err
				out = append(out, u)
				continue
			}
			sess.Agent = a.Name()
			sess.ProjectSlug = name
			if sess.ProjectPath == "" {
				// 猜不出项目归属就明确失败：把整场会话并到错误项目里，比丢掉它更糟
				u.Err = fmt.Errorf("no workspace-directories record in %s", f)
				out = append(out, u)
				continue
			}
			if sess.Confidence == "" {
				sess.Confidence = "high"
			}
			u.Session = sess
			u.Events = events
			u.Steps = parseTodos(content)
			out = append(out, u)
		}
	}
	return out, nil
}

// ParseSessionFile 解析单个会话文件，供定向同步（task_sync）复用，不走目录扫描。
func (a *Adapter) ParseSessionFile(path string) (agent.SessionUnit, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return agent.SessionUnit{}, err
	}
	u := agent.SessionUnit{Path: path, Content: content, Size: int64(len(content))}
	if st, err := os.Stat(path); err == nil {
		u.MTime = st.ModTime()
	}
	sess, events, err := parseSessionJSONL(path, content)
	if err != nil {
		return u, err
	}
	sess.Agent = a.Name()
	u.Session = sess
	u.Events = events
	u.Steps = parseTodos(content)
	return u, nil
}
