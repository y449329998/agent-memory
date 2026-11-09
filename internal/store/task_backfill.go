package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BackfillTasks 把旧 task_card 一次性提升为合并任务，任务 id 复用卡片 id。
// 标记写在 meta 里，重复 Open 不会重复插入；步骤等下一次扫描再补齐。
func (s *Store) BackfillTasks(ctx context.Context) error {
	var done string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'task_backfill_v1'`).Scan(&done)
	if err == nil && done == "1" {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.project_id, c.goal, c.status, c.next_steps,
       c.decisions_json, c.files_json, c.commands_json, c.errors_json, c.open_questions,
       c.created_at, c.updated_at, s.id, s.agent, s.path
FROM task_card c JOIN session s ON s.id = c.session_id
WHERE NOT EXISTS (SELECT 1 FROM task t WHERE t.id = c.id)`)
	if err != nil {
		return err
	}
	type row struct {
		cardID, projectID, sessionID int64
		goal, status, next           string
		decisions, files, commands   string
		errorsJSON, questions        string
		agent, path                  string
		created, updated             int64
	}
	var items []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.cardID, &r.projectID, &r.goal, &r.status, &r.next,
			&r.decisions, &r.files, &r.commands, &r.errorsJSON, &r.questions,
			&r.created, &r.updated, &r.sessionID, &r.agent, &r.path); err != nil {
			rows.Close()
			return err
		}
		items = append(items, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range items {
		completed := int64(0)
		if r.status == "done" {
			completed = r.updated
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO task(id, project_id, kind, session_id, goal, status, next_steps, decisions_json, files_json,
  commands_json, errors_json, open_questions, source_agent, source_path,
  completed_at, created_at, updated_at)
VALUES(?, ?, 'session', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.cardID, r.projectID, r.sessionID, r.goal, r.status, r.next,
			r.decisions, r.files, r.commands, r.errorsJSON, r.questions,
			r.agent, r.path, completed, r.created, r.updated); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO task_session(task_id, session_id, role, created_at) VALUES(?, ?, 'source', ?)`,
			r.cardID, r.sessionID, r.created); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES('task_backfill_v1','1')
ON CONFLICT(key) DO UPDATE SET value = excluded.value`); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, r := range items {
		if err := s.RefreshTaskSnippets(ctx, r.cardID, time.Unix(r.updated, 0)); err != nil {
			return err
		}
	}
	return nil
}
