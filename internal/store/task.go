package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

type TaskFilter struct {
	ProjectID       int64
	Query           string
	Status          string
	OwnerAgent      string
	SourceAgent     string
	UpdatedAfter    time.Time
	UpdatedBefore   time.Time
	IncludeArchived bool
	Kind            string
	Sort            string
	Limit           int
	Offset          int
}

type TaskUpdate struct {
	ID         int64
	Goal       *string
	Status     *string
	NextSteps  *string
	OwnerAgent *string
	Archived   *bool
	Steps      *[]domain.TaskStep
}

type TaskDetail struct {
	domain.Task
	Snippets []domain.TaskSnippet
	Sessions []domain.TaskSession
}

func validTaskKind(k string) bool {
	switch k {
	case domain.TaskKindSession, domain.TaskKindFormal:
		return true
	}
	return false
}

func validTaskStatus(s string) bool {
	switch s {
	case domain.StatusDoing, domain.StatusDone, domain.StatusBlocked, domain.StatusUnknown:
		return true
	}
	return false
}

func validStepStatus(s string) bool {
	switch s {
	case domain.StepPending, domain.StepDoing, domain.StepDone, domain.StepBlocked, domain.StepSkipped:
		return true
	}
	return false
}

func (s *Store) CreateTask(ctx context.Context, task domain.Task) (int64, error) {
	if task.Goal == "" {
		return 0, errors.New("task goal is empty")
	}
	if task.Status == "" {
		task.Status = domain.StatusDoing
	}
	if !validTaskStatus(task.Status) {
		return 0, fmt.Errorf("invalid task status %q", task.Status)
	}
	if task.Status == domain.StatusDone && task.CompletedAt.IsZero() {
		task.CompletedAt = task.UpdatedAt
	}
	if task.Status != domain.StatusDone {
		task.CompletedAt = time.Time{}
	}
	if task.Kind == "" {
		task.Kind = domain.TaskKindFormal
	}
	if !validTaskKind(task.Kind) {
		return 0, fmt.Errorf("invalid task kind %q", task.Kind)
	}
	if task.Kind == domain.TaskKindSession && task.SessionID == 0 {
		return 0, errors.New("session task requires session_id")
	}
	if task.OwnerAgent != "" && task.ClaimedAt.IsZero() {
		task.ClaimedAt = task.UpdatedAt
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	decisions, _ := marshalList(task.Decisions)
	files, _ := marshalList(task.Files)
	commands, _ := marshalList(task.Commands)
	errorsJSON, _ := marshalList(task.Errors)
	res, err := tx.ExecContext(ctx, `
INSERT INTO task(project_id, kind, session_id, goal, status, owner_agent, claimed_at, next_steps,
  decisions_json, files_json, commands_json, errors_json, open_questions,
  source_agent, source_path, manual_edited_at, completed_at, archived_at,
  created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		task.ProjectID, task.Kind, task.SessionID, task.Goal, task.Status, task.OwnerAgent, unixOrZero(task.ClaimedAt),
		task.NextSteps, decisions, files, commands, errorsJSON, task.OpenQuestions,
		task.SourceAgent, task.SourcePath, unixOrZero(task.ManualEditedAt),
		unixOrZero(task.CompletedAt), unixOrZero(task.CreatedAt), unixOrZero(task.UpdatedAt))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := insertSteps(ctx, tx, id, task.Steps); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func insertSteps(ctx context.Context, tx *sql.Tx, taskID int64, steps []domain.TaskStep) error {
	if len(steps) > 100 {
		return fmt.Errorf("task has too many steps: %d", len(steps))
	}
	for i, step := range steps {
		if strings.TrimSpace(step.Subject) == "" {
			return fmt.Errorf("task step %d has empty subject", i+1)
		}
		status := step.Status
		if status == "" {
			status = domain.StepPending
		}
		if !validStepStatus(status) {
			return fmt.Errorf("invalid step status %q", status)
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO task_step(task_id, seq, subject, description, active_form, status, source_key, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, i+1, step.Subject, step.Description, step.ActiveForm, status,
			step.SourceKey, unixOrZero(step.CreatedAt), unixOrZero(step.UpdatedAt))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) TaskByID(ctx context.Context, id int64) (domain.Task, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, kind, session_id, goal, status, owner_agent, claimed_at, next_steps,
  decisions_json, files_json, commands_json, errors_json, open_questions,
  source_agent, source_path, manual_edited_at, completed_at, archived_at,
  merged_into_task_id, created_at, updated_at
FROM task WHERE id = ?`, id)
	task, err := scanTaskRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, false, nil
	}
	if err != nil {
		return domain.Task{}, false, err
	}
	if task.Steps, err = s.taskSteps(ctx, id); err != nil {
		return domain.Task{}, false, err
	}
	if summary, ok, err := s.SummaryFor(ctx, "task", id); err != nil {
		return domain.Task{}, false, err
	} else if ok {
		task.Summary, task.SummaryState, task.SummaryError = summary.Summary, summary.State, summary.Error
	}
	return task, true, nil
}

func scanTaskRow(row *sql.Row) (domain.Task, error) {
	var (
		task                         domain.Task
		decisions, files, cmds, errs string
		claimed, manual, completed   int64
		archived, created, updated   int64
	)
	err := row.Scan(&task.ID, &task.ProjectID, &task.Kind, &task.SessionID, &task.Goal, &task.Status, &task.OwnerAgent,
		&claimed, &task.NextSteps, &decisions, &files, &cmds, &errs, &task.OpenQuestions,
		&task.SourceAgent, &task.SourcePath, &manual, &completed, &archived,
		&task.MergedIntoTaskID, &created, &updated)
	if err != nil {
		return domain.Task{}, err
	}
	task.Decisions, task.Files, task.Commands, task.Errors, err = decodeLists(decisions, files, cmds, errs)
	if err != nil {
		return domain.Task{}, err
	}
	task.ClaimedAt = timeFromUnix(claimed)
	task.ManualEditedAt = timeFromUnix(manual)
	task.CompletedAt = timeFromUnix(completed)
	task.ArchivedAt = timeFromUnix(archived)
	task.CreatedAt = timeFromUnix(created)
	task.UpdatedAt = timeFromUnix(updated)
	return task, nil
}

func (s *Store) taskSteps(ctx context.Context, taskID int64) ([]domain.TaskStep, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, seq, subject, description, active_form, status, source_key, created_at, updated_at
FROM task_step WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskStep
	for rows.Next() {
		var (
			step             domain.TaskStep
			created, updated int64
		)
		if err := rows.Scan(&step.ID, &step.TaskID, &step.Seq, &step.Subject, &step.Description,
			&step.ActiveForm, &step.Status, &step.SourceKey, &created, &updated); err != nil {
			return nil, err
		}
		step.CreatedAt = timeFromUnix(created)
		step.UpdatedAt = timeFromUnix(updated)
		out = append(out, step)
	}
	return out, rows.Err()
}

func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]domain.Task, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 20
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	q := `SELECT id FROM task WHERE 1 = 1`
	args := []any{}
	if f.ProjectID != 0 {
		q += ` AND project_id = ?`
		args = append(args, f.ProjectID)
	}
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if !f.IncludeArchived {
		q += ` AND archived_at = 0`
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if query := strings.TrimSpace(f.Query); query != "" {
		like := "%" + query + "%"
		q += ` AND (goal LIKE ? OR next_steps LIKE ? OR files_json LIKE ? OR EXISTS (
		  SELECT 1 FROM task_step ts WHERE ts.task_id = task.id
		  AND (ts.subject LIKE ? OR ts.description LIKE ?)))`
		args = append(args, like, like, like, like, like)
	}
	if f.OwnerAgent != "" {
		q += ` AND owner_agent = ?`
		args = append(args, f.OwnerAgent)
	}
	if f.SourceAgent != "" {
		q += ` AND source_agent = ?`
		args = append(args, f.SourceAgent)
	}
	if !f.UpdatedAfter.IsZero() {
		q += ` AND updated_at >= ?`
		args = append(args, f.UpdatedAfter.Unix())
	}
	if !f.UpdatedBefore.IsZero() {
		q += ` AND updated_at <= ?`
		args = append(args, f.UpdatedBefore.Unix())
	}
	// 排序键取「这个任务最近一次对话发生在什么时候」：关联会话的 updated_at 与
	// 排序看「最近对话」：关联会话的最新时间与片段事件时间取最新的那个。
	// 任务本体的 updated_at 不参与：扫描会把它刷成最近一次扫描时间，
	// 掺进来等于按扫描时间排，早就没动的老对话也会浮到顶上。
	// 只有既没会话又没片段（手工新建的合并任务）才退回本体时间。
	const activity = `CASE WHEN EXISTS (SELECT 1 FROM task_session ts WHERE ts.task_id = task.id)
	  OR EXISTS (SELECT 1 FROM task_snippet sn WHERE sn.task_id = task.id)
	  THEN MAX(
	    IFNULL((SELECT MAX(se.updated_at) FROM task_session ts JOIN session se ON se.id = ts.session_id WHERE ts.task_id = task.id), 0),
	    IFNULL((SELECT MAX(sn.ts) FROM task_snippet sn WHERE sn.task_id = task.id), 0))
	  ELSE task.updated_at END`
	switch f.Sort {
	case "created":
		q += ` ORDER BY created_at DESC, id DESC`
	case "idle":
		q += ` ORDER BY ` + activity + ` ASC, id ASC`
	default:
		q += ` ORDER BY ` + activity + ` DESC, id DESC`
	}
	q += ` LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]domain.Task, 0, len(ids))
	for _, id := range ids {
		task, ok, err := s.TaskByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			ov, err := s.stepOverrides(ctx, id)
			if err != nil {
				return nil, err
			}
			applyOverrides(&task, ov)
			out = append(out, task)
		}
	}
	return out, nil
}

func (s *Store) UpdateTask(ctx context.Context, in TaskUpdate, at time.Time) (domain.Task, error) {
	if in.Status != nil && !validTaskStatus(*in.Status) {
		return domain.Task{}, fmt.Errorf("invalid task status %q", *in.Status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Task{}, err
	}
	defer tx.Rollback()
	var exists int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM task WHERE id = ?`, in.ID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Task{}, domain.ErrTaskCardNotFound
		}
		return domain.Task{}, err
	}
	now := at.Unix()
	if in.Goal != nil {
		if strings.TrimSpace(*in.Goal) == "" {
			return domain.Task{}, errors.New("task goal is empty")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task SET goal = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.Goal, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.NextSteps != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE task SET next_steps = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.NextSteps, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.OwnerAgent != nil {
		claimed := now
		if *in.OwnerAgent == "" {
			claimed = 0
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task SET owner_agent = ?, claimed_at = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.OwnerAgent, claimed, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.Status != nil {
		completed := int64(0)
		if *in.Status == domain.StatusDone {
			completed = now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE task SET status = ?, completed_at = ?, archived_at = 0, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
			*in.Status, completed, now, now, in.ID); err != nil {
			return domain.Task{}, err
		}
	}
	if in.Archived != nil {
		if *in.Archived {
			if _, err := tx.ExecContext(ctx, `UPDATE task SET archived_at = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
				now, now, now, in.ID); err != nil {
				return domain.Task{}, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE task SET archived_at = 0, completed_at = CASE WHEN status = 'done' THEN ? ELSE 0 END, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
				now, now, now, in.ID); err != nil {
				return domain.Task{}, err
			}
		}
	}
	if in.Steps != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM task_step WHERE task_id = ?`, in.ID); err != nil {
			return domain.Task{}, err
		}
		if err := insertSteps(ctx, tx, in.ID, *in.Steps); err != nil {
			return domain.Task{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET manual_edited_at = ?, updated_at = ? WHERE id = ?`,
		now, now, in.ID); err != nil {
		return domain.Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Task{}, err
	}
	task, ok, err := s.TaskByID(ctx, in.ID)
	if err != nil || !ok {
		return domain.Task{}, fmt.Errorf("reload task %d: %w", in.ID, err)
	}
	return task, nil
}

func (s *Store) UpdateTaskFromSource(ctx context.Context, task domain.Task, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	decisions, _ := marshalList(task.Decisions)
	files, _ := marshalList(task.Files)
	commands, _ := marshalList(task.Commands)
	errorsJSON, _ := marshalList(task.Errors)
	res, err := tx.ExecContext(ctx, `
UPDATE task SET goal = ?, status = ?, next_steps = ?, decisions_json = ?, files_json = ?,
  commands_json = ?, errors_json = ?, open_questions = ?,
  completed_at = CASE
    WHEN ? = 'done' AND completed_at = 0 THEN ?
    WHEN ? <> 'done' THEN 0
    ELSE completed_at
  END,
  archived_at = CASE WHEN ? <> 'done' THEN 0 ELSE archived_at END,
  updated_at = ?
WHERE id = ? AND manual_edited_at = 0`,
		task.Goal, task.Status, task.NextSteps, decisions, files, commands, errorsJSON,
		task.OpenQuestions, task.Status, at.Unix(), task.Status, task.Status,
		at.Unix(), task.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_step WHERE task_id = ?`, task.ID); err != nil {
		return err
	}
	if err := insertSteps(ctx, tx, task.ID, task.Steps); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LinkTaskSession(ctx context.Context, taskID, sessionID int64, role string, at time.Time) error {
	if role == "" {
		role = "continuation"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO task_session(task_id, session_id, role, created_at) VALUES(?, ?, ?, ?)
ON CONFLICT(task_id, session_id) DO NOTHING`, taskID, sessionID, role, at.Unix())
	return err
}

// TaskByKindSession 按会话主键取对话任务。task.session_id 是唯一权威绑定，
// 不再从 task_session 反查：合并视图也会写 task_session，反查会把对话任务
// 错认成某个合并任务。
func (s *Store) TaskByKindSession(ctx context.Context, sessionID int64) (domain.Task, bool, error) {
	if sessionID == 0 {
		return domain.Task{}, false, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM task WHERE kind = 'session' AND session_id = ?`, sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Task{}, false, nil
	}
	if err != nil {
		return domain.Task{}, false, err
	}
	return s.TaskByID(ctx, id)
}

func (s *Store) TaskDetail(ctx context.Context, id int64) (TaskDetail, bool, error) {
	task, ok, err := s.TaskByID(ctx, id)
	if err != nil || !ok {
		return TaskDetail{}, ok, err
	}
	detail := TaskDetail{Task: task}
	detail.Snippets, err = s.taskSnippets(ctx, id)
	if err != nil {
		return TaskDetail{}, false, err
	}
	detail.Sessions, err = s.taskSessions(ctx, id)
	if err != nil {
		return TaskDetail{}, false, err
	}
	ov, err := s.stepOverrides(ctx, id)
	if err != nil {
		return TaskDetail{}, false, err
	}
	applyOverrides(&detail.Task, ov)
	return detail, true, err
}

func (s *Store) taskSnippets(ctx context.Context, taskID int64) ([]domain.TaskSnippet, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, task_id, seq, role, kind, text, ts FROM task_snippet
WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskSnippet
	for rows.Next() {
		var item domain.TaskSnippet
		var ts int64
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Seq, &item.Role, &item.Kind, &item.Text, &ts); err != nil {
			return nil, err
		}
		item.Ts = timeFromUnix(ts)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) taskSessions(ctx context.Context, taskID int64) ([]domain.TaskSession, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT ts.task_id, ts.session_id, ts.role, ts.created_at, s.agent, s.title, s.path, s.updated_at
FROM task_session ts JOIN session s ON s.id = ts.session_id
WHERE ts.task_id = ? ORDER BY ts.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TaskSession
	for rows.Next() {
		var item domain.TaskSession
		var created, updated int64
		if err := rows.Scan(&item.TaskID, &item.SessionID, &item.Role, &created, &item.Agent, &item.Title, &item.Path, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt = timeFromUnix(created)
		item.UpdatedAt = timeFromUnix(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) RefreshTaskSnippets(ctx context.Context, taskID int64, at time.Time) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.role, e.kind, e.text, e.files_json, e.ts
FROM session_event e JOIN task_session ts ON ts.session_id = e.session_id
WHERE ts.task_id = ? AND e.kind IN ('user_text','assistant_text','tool_command','tool_error','tool_edit')
ORDER BY e.ts DESC, e.id DESC LIMIT 20`, taskID)
	if err != nil {
		return err
	}
	var snippets []domain.TaskSnippet
	for rows.Next() {
		var item domain.TaskSnippet
		var files string
		var tsInt int64
		if err := rows.Scan(&item.Role, &item.Kind, &item.Text, &files, &tsInt); err != nil {
			rows.Close()
			return err
		}
		item.Ts = timeFromUnix(tsInt)
		if item.Kind == "tool_edit" {
			var list []string
			if json.Unmarshal([]byte(files), &list) == nil && len(list) > 0 {
				item.Text = "改动: " + strings.Join(list, ", ")
			}
		}
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		snippets = append(snippets, item)
	}
	rows.Close()
	for i, j := 0, len(snippets)-1; i < j; i, j = i+1, j-1 {
		snippets[i], snippets[j] = snippets[j], snippets[i]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_snippet WHERE task_id = ?`, taskID); err != nil {
		return err
	}
	for i, item := range snippets {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_snippet(task_id, seq, role, kind, text, ts) VALUES(?, ?, ?, ?, ?, ?)`,
			taskID, i+1, item.Role, item.Kind, item.Text, unixOrZero(item.Ts)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ArchiveCompletedTasks(ctx context.Context, before, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE task SET archived_at = ?, updated_at = ?
WHERE archived_at = 0 AND status = 'done' AND completed_at > 0 AND completed_at <= ?`,
		at.Unix(), at.Unix(), before.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) MergeTasks(ctx context.Context, sourceID, targetID int64, at time.Time) error {
	if sourceID == targetID {
		return errors.New("cannot merge a task into itself")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var targetArchived int64
	if err := tx.QueryRowContext(ctx, `SELECT archived_at FROM task WHERE id = ?`, targetID).Scan(&targetArchived); err != nil {
		return err
	}
	if targetArchived != 0 {
		return errors.New("target task is archived")
	}
	seen := map[string]bool{}
	count := 0
	nextSeq := 1
	rows, err := tx.QueryContext(ctx, `SELECT seq, source_key, subject FROM task_step WHERE task_id = ? ORDER BY seq`, targetID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var seq int
		var sourceKey, subject string
		if err := rows.Scan(&seq, &sourceKey, &subject); err != nil {
			rows.Close()
			return err
		}
		count++
		if seq >= nextSeq {
			nextSeq = seq + 1
		}
		key := sourceKey
		if key == "" {
			key = subject
		}
		seen[key] = true
	}
	rows.Close()
	rows, err = tx.QueryContext(ctx, `SELECT subject, description, active_form, status, source_key FROM task_step WHERE task_id = ? ORDER BY seq`, sourceID)
	if err != nil {
		return err
	}
	var merged []domain.TaskStep
	for rows.Next() && count < 100 {
		var step domain.TaskStep
		if err := rows.Scan(&step.Subject, &step.Description, &step.ActiveForm, &step.Status, &step.SourceKey); err != nil {
			rows.Close()
			return err
		}
		key := step.SourceKey
		if key == "" {
			key = step.Subject
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, step)
		count++
	}
	rows.Close()
	for i, step := range merged {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task_step(task_id, seq, subject, description, active_form, status, source_key, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			targetID, nextSeq+i, step.Subject, step.Description, step.ActiveForm, step.Status,
			step.SourceKey, at.Unix(), at.Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_session(task_id, session_id, role, created_at)
SELECT ?, session_id, 'continuation', ? FROM task_session WHERE task_id = ?`, targetID, at.Unix(), sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_session WHERE task_id = ?`, sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET archived_at = ?, merged_into_task_id = ?, manual_edited_at = ?, updated_at = ? WHERE id = ?`,
		at.Unix(), targetID, at.Unix(), at.Unix(), sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task SET manual_edited_at = ?, updated_at = ? WHERE id = ?`,
		at.Unix(), at.Unix(), targetID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.RefreshTaskSnippets(ctx, targetID, at)
}
