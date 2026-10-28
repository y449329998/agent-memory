package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func (s *Store) UpsertSession(ctx context.Context, sess domain.Session) (int64, error) {
	if sess.ExternalID == "" {
		return 0, errors.New("session external id is empty")
	}
	if sess.Agent == "" {
		return 0, errors.New("session agent is empty")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO session(agent, external_id, project_id, title, started_at, updated_at, event_count, path)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(agent, external_id) DO UPDATE SET
  project_id = excluded.project_id,
  title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE session.title END,
  started_at = CASE WHEN session.started_at = 0 THEN excluded.started_at ELSE session.started_at END,
  updated_at = excluded.updated_at,
  event_count = excluded.event_count,
  path = excluded.path`,
		sess.Agent, sess.ExternalID, sess.ProjectID, sess.Title,
		sess.StartedAt.Unix(), sess.UpdatedAt.Unix(), sess.EventCount, sess.Path)
	if err != nil {
		return 0, fmt.Errorf("upsert session %s/%s: %w", sess.Agent, sess.ExternalID, err)
	}

	// 走 ON CONFLICT DO UPDATE 时 last_insert_rowid() 不指向被更新的那行，必须回查。
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM session WHERE agent = ? AND external_id = ?`,
		sess.Agent, sess.ExternalID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (s *Store) SessionByID(ctx context.Context, id int64) (domain.Session, bool, error) {
	var (
		sess    domain.Session
		started int64
		updated int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, agent, external_id, project_id, title, started_at, updated_at, event_count, path
FROM session WHERE id = ?`, id).Scan(
		&sess.ID, &sess.Agent, &sess.ExternalID, &sess.ProjectID, &sess.Title,
		&started, &updated, &sess.EventCount, &sess.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Session{}, false, nil
	}
	if err != nil {
		return domain.Session{}, false, err
	}
	sess.StartedAt = time.Unix(started, 0)
	sess.UpdatedAt = time.Unix(updated, 0)
	return sess, true, nil
}

// ReplaceEvents 整批替换一个会话的事件。会话内容完全由源文件决定，
// 重扫时全删全插比对增量对账简单，且 JSONL 没有可用的稳定事件主键。
func (s *Store) ReplaceEvents(ctx context.Context, sessionID int64, events []domain.SessionEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM session_event WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	for i, e := range events {
		// 空 Files 必须落成 []，json.Marshal(nil) 会写出 null。
		files := e.Files
		if files == nil {
			files = []string{}
		}
		raw, err := json.Marshal(files)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO session_event(session_id, seq, role, kind, text, tool, files_json, ts)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID, i+1, e.Role, string(e.Kind), e.Text, e.Tool, string(raw), e.Ts.Unix()); err != nil {
			return fmt.Errorf("insert event %d: %w", i+1, err)
		}
	}
	return tx.Commit()
}

func (s *Store) EventsFor(ctx context.Context, sessionID int64) ([]domain.SessionEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, seq, role, kind, text, tool, files_json, ts
FROM session_event WHERE session_id = ? ORDER BY seq`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.SessionEvent
	for rows.Next() {
		var (
			e        domain.SessionEvent
			kind     string
			filesStr string
			ts       int64
		)
		if err := rows.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Role, &kind, &e.Text, &e.Tool, &filesStr, &ts); err != nil {
			return nil, err
		}
		e.Kind = domain.EventKind(kind)
		e.Ts = time.Unix(ts, 0)
		if filesStr != "" && filesStr != "[]" {
			if err := json.Unmarshal([]byte(filesStr), &e.Files); err != nil {
				return nil, fmt.Errorf("event %d files_json: %w", e.ID, err)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) ListSessions(ctx context.Context, projectID int64, limit int) ([]domain.Session, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id FROM session WHERE project_id = ? ORDER BY updated_at DESC, id LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]domain.Session, 0, len(ids))
	for _, id := range ids {
		sess, ok, err := s.SessionByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, sess)
		}
	}
	return out, nil
}
