package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

type DataExport struct {
	FormatVersion int             `json:"format_version"`
	GeneratedAt   string          `json:"generated_at"`
	Projects      []ProjectExport `json:"projects"`
	Memories      []MemoryExport  `json:"memories"`
	Tasks         []TaskExport    `json:"tasks"`
}

type ProjectExport struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name"`
	GitRemote   string `json:"git_remote"`
	PrimaryPath string `json:"primary_path"`
}

type MemoryExport struct {
	Scope      string `json:"scope"`
	ProjectKey string `json:"project_key"`
	Type       string `json:"type"`
	Text       string `json:"text"`
	Stale      bool   `json:"stale"`
	Summary    string `json:"summary,omitempty"`
}

type TaskExport struct {
	ID         int64        `json:"-"`
	ProjectKey string       `json:"project_key"`
	Goal       string       `json:"goal"`
	Status     string       `json:"status"`
	OwnerAgent string       `json:"owner_agent"`
	NextSteps  string       `json:"next_steps"`
	Steps      []StepExport `json:"steps"`
	Summary    string       `json:"summary,omitempty"`
}

type StepExport struct {
	Subject     string `json:"subject"`
	Description string `json:"description"`
	ActiveForm  string `json:"active_form"`
	Status      string `json:"status"`
}

func (s *Store) Backup(ctx context.Context, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// 秒级时间戳在同一秒内连续备份会重名（导入前也会自动备份），带纳秒保证唯一。
	now := time.Now()
	name := fmt.Sprintf("memory-%s-%09d.db", now.Format("20060102-150405"), now.Nanosecond())
	path := filepath.Join(dir, name)
	stmt := `VACUUM INTO '` + strings.ReplaceAll(path, "'", "''") + `'`
	if _, err := s.db.ExecContext(ctx, stmt); err != nil {
		return "", err
	}
	entries, _ := os.ReadDir(dir)
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "memory-") && strings.HasSuffix(e.Name(), ".db") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for len(files) > 10 {
		_ = os.Remove(filepath.Join(dir, files[0]))
		files = files[1:]
	}
	return path, nil
}

func (s *Store) ExportData(ctx context.Context) ([]byte, error) {
	data := DataExport{FormatVersion: 1, GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	rows, err := s.db.QueryContext(ctx, `SELECT key, display_name, git_remote, primary_path FROM project ORDER BY id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p ProjectExport
		if err := rows.Scan(&p.Key, &p.DisplayName, &p.GitRemote, &p.PrimaryPath); err != nil {
			rows.Close()
			return nil, err
		}
		data.Projects = append(data.Projects, p)
	}
	rows.Close()

	rows, err = s.db.QueryContext(ctx, `
SELECT COALESCE(p.key, ''), m.scope, m.type, m.text, m.stale, COALESCE(s.summary, '')
FROM memory_item m
LEFT JOIN project p ON p.id = m.project_id
LEFT JOIN summary s ON s.entity_kind = 'memory' AND s.entity_id = m.id AND s.state = 'ready'
ORDER BY m.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m MemoryExport
		if err := rows.Scan(&m.ProjectKey, &m.Scope, &m.Type, &m.Text, &m.Stale, &m.Summary); err != nil {
			rows.Close()
			return nil, err
		}
		data.Memories = append(data.Memories, m)
	}
	rows.Close()

	// 只导出合并任务：对话任务由目标机扫描各 agent 的会话重新生成，
	// 带过去会在导入后与重新扫出的那批重复。
	rows, err = s.db.QueryContext(ctx, `
SELECT t.id, COALESCE(p.key, ''), t.goal, t.status, t.owner_agent, t.next_steps, COALESCE(s.summary, '')
FROM task t
LEFT JOIN project p ON p.id = t.project_id
LEFT JOIN summary s ON s.entity_kind = 'task' AND s.entity_id = t.id AND s.state = 'ready'
WHERE t.kind = 'formal'
ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	var tasks []TaskExport
	for rows.Next() {
		var t TaskExport
		if err := rows.Scan(&t.ID, &t.ProjectKey, &t.Goal, &t.Status, &t.OwnerAgent, &t.NextSteps, &t.Summary); err != nil {
			rows.Close()
			return nil, err
		}
		tasks = append(tasks, t)
	}
	rows.Close()
	for i := range tasks {
		steps, err := s.exportSteps(ctx, tasks[i])
		if err != nil {
			return nil, err
		}
		tasks[i].Steps = steps
	}
	data.Tasks = tasks
	return json.MarshalIndent(data, "", "  ")
}

func (s *Store) exportSteps(ctx context.Context, task TaskExport) ([]StepExport, error) {
	if task.ID == 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT subject, description, active_form, status FROM task_step WHERE task_id = ? ORDER BY seq`, task.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepExport
	for rows.Next() {
		var step StepExport
		if err := rows.Scan(&step.Subject, &step.Description, &step.ActiveForm, &step.Status); err != nil {
			return nil, err
		}
		out = append(out, step)
	}
	return out, rows.Err()
}

type ImportPreview struct {
	Projects int
	Memories int
	Tasks    int
	Errors   []string
}

func PreviewImport(data []byte) (ImportPreview, error) {
	doc, err := parseImport(data)
	if err != nil {
		return ImportPreview{}, err
	}
	return ImportPreview{Projects: len(doc.Projects), Memories: len(doc.Memories), Tasks: len(doc.Tasks)}, nil
}

func parseImport(data []byte) (DataExport, error) {
	var doc DataExport
	if err := json.Unmarshal(data, &doc); err != nil {
		return doc, fmt.Errorf("parse import: %w", err)
	}
	if doc.FormatVersion != 1 {
		return doc, fmt.Errorf("unsupported format_version %d", doc.FormatVersion)
	}
	keys := map[string]bool{}
	for i, p := range doc.Projects {
		if strings.TrimSpace(p.Key) == "" {
			return doc, fmt.Errorf("project %d has empty key", i+1)
		}
		if keys[p.Key] {
			return doc, fmt.Errorf("duplicate project key %q", p.Key)
		}
		keys[p.Key] = true
	}
	known := func(key string) bool { return key == "" || keys[key] }
	for i := range doc.Memories {
		m := &doc.Memories[i]
		if strings.TrimSpace(m.Text) == "" {
			return doc, fmt.Errorf("memory %d has empty text", i+1)
		}
		if m.Scope == "" {
			if m.ProjectKey != "" {
				m.Scope = string(domain.ScopeProject)
			} else {
				m.Scope = string(domain.ScopeUser)
			}
		}
		if m.Scope != string(domain.ScopeUser) && m.Scope != string(domain.ScopeProject) {
			return doc, fmt.Errorf("memory %d has invalid scope %q", i+1, m.Scope)
		}
		if m.Scope == string(domain.ScopeProject) && strings.TrimSpace(m.ProjectKey) == "" {
			return doc, fmt.Errorf("memory %d is project-scoped but has no project_key", i+1)
		}
		if m.Scope == string(domain.ScopeUser) {
			m.ProjectKey = ""
		}
		if m.Type != "" && !domain.ValidMemoryType(domain.MemoryType(m.Type)) {
			return doc, fmt.Errorf("memory %d has invalid type %q", i+1, m.Type)
		}
		if !known(m.ProjectKey) {
			return doc, fmt.Errorf("memory %d references unknown project %q", i+1, m.ProjectKey)
		}
	}
	for i := range doc.Tasks {
		t := &doc.Tasks[i]
		if strings.TrimSpace(t.Goal) == "" {
			return doc, fmt.Errorf("task %d has empty goal", i+1)
		}
		if t.Status == "" {
			t.Status = domain.StatusDoing
		}
		if t.Status != domain.StatusDoing && t.Status != domain.StatusDone &&
			t.Status != domain.StatusBlocked && t.Status != domain.StatusUnknown {
			return doc, fmt.Errorf("task %d has invalid status %q", i+1, t.Status)
		}
		if !known(t.ProjectKey) {
			return doc, fmt.Errorf("task %d references unknown project %q", i+1, t.ProjectKey)
		}
		if len(t.Steps) > 100 {
			return doc, fmt.Errorf("task %d has too many steps: %d", i+1, len(t.Steps))
		}
		for j, s := range t.Steps {
			if strings.TrimSpace(s.Subject) == "" {
				return doc, fmt.Errorf("task %d step %d has empty subject", i+1, j+1)
			}
			if s.Status != "" && !validStepStatus(s.Status) {
				return doc, fmt.Errorf("task %d step %d has invalid status %q", i+1, j+1, s.Status)
			}
		}
	}
	return doc, nil
}

func (s *Store) ApplyImport(ctx context.Context, data []byte, at time.Time) (ImportPreview, error) {
	doc, err := parseImport(data)
	if err != nil {
		return ImportPreview{}, err
	}
	projects := map[string]int64{}
	for _, p := range doc.Projects {
		id, err := s.EnsureProject(ctx, p.Key, p.DisplayName, p.GitRemote, p.PrimaryPath, at)
		if err != nil {
			return ImportPreview{}, err
		}
		projects[p.Key] = id
	}
	preview := ImportPreview{Projects: len(doc.Projects)}
	for _, m := range doc.Memories {
		pid := projects[m.ProjectKey]
		if m.ProjectKey != "" && pid == 0 {
			preview.Errors = append(preview.Errors, "unknown project "+m.ProjectKey)
			continue
		}
		if m.Scope == string(domain.ScopeUser) {
			pid = 0
		}
		id, _, err := s.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: domain.Scope(m.Scope), Type: domain.MemoryType(m.Type), Text: m.Text,
			SourceAgent: "import", SourcePath: "import", Quote: m.Text,
		}, at)
		if err != nil {
			preview.Errors = append(preview.Errors, err.Error())
			continue
		}
		if m.Stale {
			_ = s.SetStaleByID(ctx, id, true)
		}
		if m.Summary != "" {
			_ = s.importSummary(ctx, "memory", id, m.Summary, at)
		}
		preview.Memories++
	}
	for _, t := range doc.Tasks {
		pid := projects[t.ProjectKey]
		if t.ProjectKey != "" && pid == 0 {
			preview.Errors = append(preview.Errors, "unknown project "+t.ProjectKey)
			continue
		}
		var existing int64
		err = s.db.QueryRowContext(ctx, `SELECT id FROM task WHERE project_id = ? AND goal = ? AND archived_at = 0 LIMIT 1`, pid, t.Goal).Scan(&existing)
		steps := make([]domain.TaskStep, 0, len(t.Steps))
		for _, step := range t.Steps {
			steps = append(steps, domain.TaskStep{
				Subject: step.Subject, Description: step.Description,
				ActiveForm: step.ActiveForm, Status: step.Status,
				CreatedAt: at, UpdatedAt: at,
			})
		}
		var taskID int64
		if errors.Is(err, sql.ErrNoRows) {
			taskID, err = s.CreateTask(ctx, domain.Task{
				ProjectID: pid, Goal: t.Goal, Status: t.Status, OwnerAgent: t.OwnerAgent,
				NextSteps: t.NextSteps, SourceAgent: "import", ManualEditedAt: at,
				CreatedAt: at, UpdatedAt: at, Steps: steps,
			})
			if err != nil {
				preview.Errors = append(preview.Errors, err.Error())
				continue
			}
		} else if err == nil {
			taskID = existing
			if _, err := s.UpdateTask(ctx, TaskUpdate{
				ID: existing, Goal: &t.Goal, Status: &t.Status,
				NextSteps: &t.NextSteps, OwnerAgent: &t.OwnerAgent, Steps: &steps,
			}, at); err != nil {
				preview.Errors = append(preview.Errors, err.Error())
				continue
			}
		} else if err != nil {
			preview.Errors = append(preview.Errors, err.Error())
			continue
		}
		if t.Summary != "" && taskID != 0 {
			_ = s.importSummary(ctx, "task", taskID, t.Summary, at)
		}
		preview.Tasks++
	}
	return preview, nil
}

// importSummary 直接把导出里的 ready 摘要写回队列，不重新调用模型。
// 源内容哈希按当前库内文本计算，后续内容变化时仍会正常重新排队。
func (s *Store) importSummary(ctx context.Context, kind string, id int64, summary string, at time.Time) error {
	var source string
	var err error
	switch kind {
	case "memory":
		var text string
		if text, err = s.MemoryText(ctx, id); err == nil {
			source = normalize.Hash(normalize.CanonicalText(text))
		}
	case "task":
		if source, err = s.TaskSummarySource(ctx, id); err == nil {
			source = normalize.Hash(source)
		}
	default:
		return fmt.Errorf("unknown summary kind %q", kind)
	}
	if err != nil {
		return err
	}
	if err := s.MarkSummaryPending(ctx, kind, id, source, at); err != nil {
		return err
	}
	return s.SaveSummary(ctx, kind, id, source, truncate(summary, 1200), "import", at)
}
