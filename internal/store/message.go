package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

// ReplaceMessages 整场重写一个会话的完整对话，并同步 FTS。
// 会话被压缩/截断时，多出来的旧 seq 自然被删掉。
func (s *Store) ReplaceMessages(ctx context.Context, sessionID int64, in []domain.SessionMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id FROM session_message WHERE session_id = ?`, sessionID)
	if err != nil {
		return err
	}
	var oldIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		oldIDs = append(oldIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range oldIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM session_message_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_message WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	for _, m := range in {
		files, _ := json.Marshal(m.Files)
		res, err := tx.ExecContext(ctx, `
INSERT INTO session_message(session_id, seq, role, kind, text, tool, files_json, ts, truncated)
VALUES(?,?,?,?,?,?,?,?,?)`,
			sessionID, m.Seq, m.Role, m.Kind, m.Text, m.Tool, string(files), m.Ts.Unix(), boolInt(m.Truncated))
		if err != nil {
			return fmt.Errorf("insert message %d: %w", m.Seq, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		seg := strings.Join(normalize.Segment(normalize.CanonicalText(m.Text)), " ")
		if _, err := tx.ExecContext(ctx, `INSERT INTO session_message_fts(rowid, text_seg) VALUES(?, ?)`, id, seg); err != nil {
			return fmt.Errorf("index message %d: %w", m.Seq, err)
		}
	}
	return tx.Commit()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// HasMessages 判断是否已经写过完整对话（旧库回退用）。
func (s *Store) HasMessages(ctx context.Context, sessionID int64) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM session_message WHERE session_id = ? LIMIT 1`, sessionID).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// MessagesForSession 取一页消息（时间正序）。beforeSeq=0 表示从最新往前；
// 返回的 next 是继续往前的游标，0 表示到头。
func (s *Store) MessagesForSession(ctx context.Context, sessionID, beforeSeq int64, limit int) ([]domain.SessionMessage, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT id, session_id, seq, role, kind, text, tool, files_json, ts, truncated
FROM session_message WHERE session_id = ?`
	args := []any{sessionID}
	if beforeSeq > 0 {
		q += ` AND seq < ?`
		args = append(args, beforeSeq)
	}
	q += ` ORDER BY seq DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	var out []domain.SessionMessage
	for rows.Next() {
		var m domain.SessionMessage
		var files string
		var ts int64
		var truncated int
		if err := rows.Scan(&m.ID, &m.SessionID, &m.Seq, &m.Role, &m.Kind, &m.Text, &m.Tool, &files, &ts, &truncated); err != nil {
			rows.Close()
			return nil, 0, err
		}
		m.Files = decodeFiles(files)
		m.Ts = timeFromUnix(ts)
		m.Truncated = truncated != 0
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, err
	}
	rows.Close()
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	var next int64
	if len(out) > 0 {
		minSeq := out[0].Seq
		if minSeq > 1 {
			next = int64(minSeq) - 1
		}
	}
	return out, next, nil
}

func decodeFiles(raw string) []string {
	var files []string
	if json.Unmarshal([]byte(raw), &files) != nil {
		return nil
	}
	return files
}

// MainSessionForTask 取任务的主来源会话：对话任务用它自己那场，合并任务取最近关联那场。
func (s *Store) MainSessionForTask(ctx context.Context, taskID int64) (int64, error) {
	var sid int64
	err := s.db.QueryRowContext(ctx, `SELECT session_id FROM task WHERE id = ?`, taskID).Scan(&sid)
	if err == nil && sid > 0 {
		return sid, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	err = s.db.QueryRowContext(ctx, `SELECT session_id FROM task_session WHERE task_id = ? ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("task %d has no linked session", taskID)
	}
	if err != nil {
		return 0, err
	}
	return sid, nil
}

// SearchMessages 用 FTS5 + 中文二元组检索完整对话；按项目/任务可选过滤。
func (s *Store) SearchMessages(ctx context.Context, query string, projectID, taskID int64, limit int) ([]domain.MessageHit, error) {
	tokens := normalize.QueryTokens(normalize.CanonicalText(query))
	expr := normalize.MatchExpr(tokens...)
	if expr == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	where := []string{"session_message_fts MATCH ?"}
	args := []any{expr}
	if projectID > 0 {
		where = append(where, `se.project_id = ?`)
		args = append(args, projectID)
	}
	if taskID > 0 {
		where = append(where, `m.session_id = (SELECT session_id FROM task WHERE id = ?)`)
		args = append(args, taskID)
	}
	sqlText := `
SELECT m.id, m.session_id, m.seq, m.role, m.kind, m.text, m.tool, m.files_json, m.ts, m.truncated,
       se.agent, se.title, IFNULL(t.id, 0)
FROM session_message_fts
JOIN session_message m ON m.id = session_message_fts.rowid
JOIN session se ON se.id = m.session_id
LEFT JOIN task t ON t.session_id = m.session_id AND t.kind = 'session'
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY bm25(session_message_fts), m.ts DESC
LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("search messages %q: %w", query, err)
	}
	defer rows.Close()
	var out []domain.MessageHit
	for rows.Next() {
		var h domain.MessageHit
		var files string
		var ts int64
		var truncated int
		if err := rows.Scan(&h.ID, &h.SessionID, &h.Seq, &h.Role, &h.Kind, &h.Text, &h.Tool, &files, &ts, &truncated, &h.Agent, &h.Title, &h.TaskID); err != nil {
			return nil, err
		}
		h.Files = decodeFiles(files)
		h.Ts = timeFromUnix(ts)
		h.Truncated = truncated != 0
		out = append(out, h)
	}
	return out, rows.Err()
}
