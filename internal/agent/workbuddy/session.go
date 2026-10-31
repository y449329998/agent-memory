package workbuddy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

const (
	wbMaxEventRunes = 500
	wbMaxTitleRunes = 80
	// 技能正文与工具说明会作为 user 消息灌进来，真实目标陈述极少超过这个长度。
	wbInjectionRunes = 4000
	// 没有 TodoWrite 清单的 WorkBuddy 会话只导入最近这段时间的，避免把
	// 历史里几百场闲聊全部变成任务。
	wbRecentSessionWindow = 30 * 24 * time.Hour
)

// sessionLine 是 projects/*.jsonl 的行结构。消息的 content 既可能是字符串，
// 也可能是 block 数组；function_call.arguments 是「JSON 字符串」而不是对象。
type sessionLine struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Timestamp json.RawMessage `json:"timestamp"`
	Cwd       string          `json:"cwd"`
	Role      string          `json:"role"`
	Status    string          `json:"status"`
	AiTitle   string          `json:"aiTitle"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// scanSessions 导入带 TodoWrite 清单的会话，以及最近 30 天没有清单的会话：
// 前者用来生成完整步骤，后者覆盖 WorkBuddy 侧栏里的近期任务/对话。
// 更老的闲聊不导入，避免把几百场历史会话全部变成任务。
//
// 解析过的文件按 mtime+size 缓存在内存里，内容未变时只返回哈希、会话元数据
// 与已解析事件，既避免 30 秒轮询反复读大文件，也避免把整份原文留在内存里。
func (a *Adapter) scanSessions(ctx context.Context) ([]agent.SessionUnit, error) {
	var out []agent.SessionUnit
	if a.Home == "" {
		return out, nil
	}
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

	for _, projectSlug := range names {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		dir := filepath.Join(projectsDir, projectSlug)
		files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		if err != nil {
			return out, err
		}
		sort.Strings(files)
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			externalID := strings.TrimSuffix(filepath.Base(f), ".jsonl")
			st, err := os.Stat(f)
			if err != nil {
				out = append(out, agent.SessionUnit{Path: f, Err: err})
				continue
			}
			if !dirExists(filepath.Join(a.Home, "tasks", externalID)) &&
				time.Since(st.ModTime()) > wbRecentSessionWindow {
				continue
			}
			if e, ok := a.cachedSession(f, st.Size(), st.ModTime()); ok {
				out = append(out, agent.SessionUnit{
					Path: f, Hash: e.hash, MTime: st.ModTime(), Size: st.Size(),
					Session: e.sess, Events: e.events, Steps: readTaskSteps(a.Home, externalID),
				})
				continue
			}
			sess, events, hash, err := parseSessionFile(f, externalID, projectSlug)
			if err != nil {
				out = append(out, agent.SessionUnit{Path: f, Err: err, MTime: st.ModTime(), Size: st.Size()})
				continue
			}
			if sess.ProjectPath == "" {
				// 没有 cwd 就无从判断归属；交给 ingest 只会建出孤立项目。
				continue
			}
			steps := readTaskSteps(a.Home, externalID)
			a.rememberSession(f, st.Size(), st.ModTime(), hash, sess, events, steps)
			out = append(out, agent.SessionUnit{
				Path: f, Hash: hash, MTime: st.ModTime(), Size: st.Size(),
				Session: sess, Events: events, Steps: steps,
			})
		}
	}
	return out, nil
}

func (a *Adapter) cachedSession(path string, size int64, mtime time.Time) (sessionCacheEntry, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e, ok := a.session[path]
	if !ok || e.size != size || !e.mtime.Equal(mtime) {
		return sessionCacheEntry{}, false
	}
	return e, true
}

func (a *Adapter) rememberSession(path string, size int64, mtime time.Time, hash string, sess domain.Session, events []agent.SessionEvent, steps []agent.TaskStepDraft) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		a.session = make(map[string]sessionCacheEntry)
	}
	a.session[path] = sessionCacheEntry{size: size, mtime: mtime, hash: hash, sess: sess, events: events, steps: steps}
}

func readTaskSteps(home, sessionID string) []agent.TaskStepDraft {
	dir := filepath.Join(home, "tasks", sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type item struct {
		key  int
		file string
	}
	var files []item
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		files = append(files, item{key: n, file: filepath.Join(dir, e.Name())})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].key < files[j].key })
	var out []agent.TaskStepDraft
	for _, f := range files {
		raw, err := os.ReadFile(f.file)
		if err != nil {
			continue
		}
		var payload struct {
			Subject     string `json:"subject"`
			Description string `json:"description"`
			ActiveForm  string `json:"activeForm"`
			Status      string `json:"status"`
		}
		if json.Unmarshal(raw, &payload) != nil || strings.TrimSpace(payload.Subject) == "" {
			continue
		}
		status := domain.StepPending
		switch payload.Status {
		case "in_progress":
			status = domain.StepDoing
		case "completed":
			status = domain.StepDone
		}
		out = append(out, agent.TaskStepDraft{
			SourceKey: strings.TrimSuffix(filepath.Base(f.file), ".json"),
			Subject:   payload.Subject, Description: payload.Description,
			ActiveForm: payload.ActiveForm, Status: status,
		})
		if len(out) == 100 {
			break
		}
	}
	return out
}

// parseSessionFile 流式读取会话并同时算哈希，避免把几十 MB 原文整份读进内存。
func parseSessionFile(path, externalID, projectSlug string) (domain.Session, []agent.SessionEvent, string, error) {
	sess := domain.Session{
		Agent:       "workbuddy",
		Path:        path,
		ExternalID:  externalID,
		ProjectSlug: projectSlug,
	}
	var events []agent.SessionEvent
	var aiTitle, firstUser string

	f, err := os.Open(path)
	if err != nil {
		return sess, nil, "", err
	}
	defer f.Close()

	h := sha256.New()
	scanner := bufio.NewScanner(io.TeeReader(f, h))
	// 工具输出单行可能很大，给足缓冲；超限行会让扫描报错，交给上层登记坏源。
	scanner.Buffer(make([]byte, 0, 256*1024), 32*1024*1024)
	seq := 0
	seenUser := map[string]bool{}
	appendEvents := func(evs []agent.SessionEvent) {
		for _, ev := range evs {
			if ev.Kind == domain.KindUserText {
				// 续聊摘要会反复带回同一条原始需求，只保留第一次。
				if seenUser[ev.Text] {
					continue
				}
				seenUser[ev.Text] = true
			}
			seq++
			ev.Seq = seq
			events = append(events, ev)
		}
	}

	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		raw = bytes.TrimPrefix(raw, []byte("\ufeff"))
		var l sessionLine
		if err := json.Unmarshal(raw, &l); err != nil {
			// 单行坏 JSON 只丢这一行，不中断整个文件
			continue
		}
		ts := parseTimestamp(l.Timestamp)
		if l.Cwd != "" && sess.ProjectPath == "" {
			sess.ProjectPath = l.Cwd
		}

		switch l.Type {
		case "ai-title":
			if l.AiTitle != "" {
				aiTitle = l.AiTitle
			}
		case "message":
			switch l.Role {
			case "user", "assistant":
				appendEvents(messageEvents(l.Role, ts, l.Content))
			}
		case "function_call":
			appendEvents(toolCallEvents(l.Name, l.Arguments, ts))
		case "function_call_result":
			if l.Status == "" || l.Status == "completed" {
				continue
			}
			text := textFrom(l.Output)
			if text == "" {
				text = l.Status
			}
			appendEvents([]agent.SessionEvent{{
				Role: "assistant", Kind: domain.KindToolError,
				Tool: l.Name, Text: clip(text, wbMaxEventRunes), Ts: ts,
			}})
		}
	}
	if err := scanner.Err(); err != nil {
		return sess, nil, "", err
	}

	for _, e := range events {
		if e.Kind == domain.KindUserText && firstUser == "" {
			firstUser = e.Text
		}
		if e.Ts.IsZero() {
			continue
		}
		if sess.StartedAt.IsZero() || e.Ts.Before(sess.StartedAt) {
			sess.StartedAt = e.Ts
		}
		if sess.UpdatedAt.IsZero() || e.Ts.After(sess.UpdatedAt) {
			sess.UpdatedAt = e.Ts
		}
	}
	// 用户只发截图时没有可读目标；用 WorkBuddy 生成的会话标题兜底，
	// 让任务卡至少能按标题归档，而不是把图片路径当成目标。
	if firstUser == "" && aiTitle != "" {
		events = append([]agent.SessionEvent{{
			Role: "user", Kind: domain.KindUserText,
			Text: clip(aiTitle, wbMaxEventRunes),
		}}, events...)
		for i := range events {
			events[i].Seq = i + 1
		}
		firstUser = aiTitle
	}
	if aiTitle != "" {
		sess.Title = clip(aiTitle, wbMaxTitleRunes)
	} else {
		sess.Title = clip(firstUser, wbMaxTitleRunes)
	}
	if sess.ProjectPath != "" {
		sess.Confidence = "high"
	}
	sess.EventCount = len(events)
	return sess, events, hex.EncodeToString(h.Sum(nil)), nil
}

func messageEvents(role string, ts time.Time, raw json.RawMessage) []agent.SessionEvent {
	if len(raw) == 0 {
		return nil
	}
	kind := domain.KindUserText
	if role == "assistant" {
		kind = domain.KindAssistant
	}
	event := func(text string) []agent.SessionEvent {
		if role == "user" {
			text = cleanUserText(text)
			if injected(text) {
				// 续聊摘要里保留了用户最初的需求，取出来当目标；
				// 纯截图/提醒/任务通知则丢弃。
				text = summaryGoal(text)
				if text == "" {
					return nil
				}
			}
		}
		text = clip(text, wbMaxEventRunes)
		if text == "" {
			return nil
		}
		return []agent.SessionEvent{{Role: role, Kind: kind, Text: text, Ts: ts}}
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return event(s)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}
	var out []agent.SessionEvent
	for _, b := range blocks {
		switch b.Type {
		case "input_text", "output_text", "text":
			out = append(out, event(b.Text)...)
		}
	}
	return out
}

// toolCallEvents 只保留对交接有用的调用：命令与文件改动。
// 读取类工具会把事件淹没，MCP 调用没有可复用的本地语义，一律丢弃。
func toolCallEvents(name string, raw json.RawMessage, ts time.Time) []agent.SessionEvent {
	if name == "" {
		return nil
	}
	args := decodeArguments(raw)
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		files := filesFromArgs(args)
		if len(files) == 0 {
			return nil
		}
		return []agent.SessionEvent{{
			Role: "assistant", Kind: domain.KindToolEdit, Tool: name, Files: files, Ts: ts,
		}}
	case "Bash", "PowerShell":
		cmd := stringFromArgs(args, "command")
		if cmd == "" {
			return nil
		}
		return []agent.SessionEvent{{
			Role: "assistant", Kind: domain.KindToolCommand,
			Tool: name, Text: clip(cmd, wbMaxEventRunes), Ts: ts,
		}}
	case "Read", "Glob", "Grep", "LS", "NotebookRead", "ToolSearch",
		"DeferExecuteTool", "read_me", "show_widget",
		"TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet", "AskUserQuestion":
		return nil
	default:
		return []agent.SessionEvent{{Role: "assistant", Kind: domain.KindOther, Tool: name, Ts: ts}}
	}
}

func decodeArguments(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		raw = json.RawMessage(s)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func filesFromArgs(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	var out []string
	for _, key := range []string{"file_path", "notebook_path", "path"} {
		if v, ok := args[key].(string); ok && v != "" {
			out = append(out, v)
		}
	}
	if edits, ok := args["edits"].([]any); ok {
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

func stringFromArgs(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// textFrom 兼容 {"type":"text","text":"..."}、纯字符串与 block 数组三种输出。
func textFrom(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		Text    string         `json:"text"`
		Content []contentBlock `json:"content"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Text != "" {
			return obj.Text
		}
		var parts []string
		for _, b := range obj.Content {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
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

func injected(s string) bool {
	if len([]rune(s)) > wbInjectionRunes {
		return true
	}
	for _, p := range []string{
		"<system-reminder", "<local-command-caveat>", "<command-name>",
		"<command-message>", "<user_info>", "<image_local_path>",
		"<task-notification>", "<cb_summary>", "<previous_user_message>",
		"Please continue with the conversation based on the summarized context above",
	} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return strings.Contains(s, "Base directory for this skill:")
}

var (
	userQueryRe    = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)
	skillMentionRe = regexp.MustCompile(`^\s*@skill(?::(?:"[^"]*"|\S+))?\s*`)
	imageMentionRe = regexp.MustCompile(`@image#\d+:\S+\s*`)
)

// cleanUserText 去掉 WorkBuddy 注入的技能路径与图片引用，只留用户真正说的话。
func cleanUserText(s string) string {
	s = skillMentionRe.ReplaceAllString(s, "")
	s = imageMentionRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// summaryGoal 从 WorkBuddy 的续聊摘要里取第一条原始需求。
func summaryGoal(s string) string {
	if !strings.Contains(s, "<cb_summary>") {
		return ""
	}
	m := userQueryRe.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	q := cleanUserText(strings.TrimSpace(m[1]))
	if q == "" || len([]rune(q)) > wbInjectionRunes {
		return ""
	}
	return q
}

func parseTimestamp(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		if ms > 1_000_000_000_000 {
			return time.UnixMilli(ms).UTC()
		}
		return time.Unix(ms, 0).UTC()
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z07:00"} {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC()
			}
		}
	}
	return time.Time{}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string(r[:n-1]) + "…"
}
