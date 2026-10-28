package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/muyi99/agent-memory/internal/domain"
)

// marshalList 把字符串切片编码为 JSON 数组。nil 切片也要落成 []，
// 与 schema 的 DEFAULT '[]' 保持一致；json.Marshal(nil) 会写出 null。
func marshalList(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// UpsertTaskCard 按会话写任务卡：一个会话只有一张卡，重复写入只更新不新增。
func (s *Store) UpsertTaskCard(ctx context.Context, card domain.TaskCard) (int64, error) {
	if card.SessionID == 0 {
		return 0, errors.New("task card needs a session id")
	}
	decisions, err := marshalList(card.Decisions)
	if err != nil {
		return 0, err
	}
	files, err := marshalList(card.Files)
	if err != nil {
		return 0, err
	}
	commands, err := marshalList(card.Commands)
	if err != nil {
		return 0, err
	}
	errs, err := marshalList(card.Errors)
	if err != nil {
		return 0, err
	}
	if card.Status == "" {
		card.Status = domain.StatusUnknown
	}
	if card.GeneratedBy == "" {
		card.GeneratedBy = "rule"
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO task_card(session_id, project_id, goal, status, decisions_json, files_json, commands_json,
  errors_json, open_questions, next_steps, confidence, generated_by, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET
  project_id = excluded.project_id,
  goal = excluded.goal,
  status = excluded.status,
  decisions_json = excluded.decisions_json,
  files_json = excluded.files_json,
  commands_json = excluded.commands_json,
  errors_json = excluded.errors_json,
  open_questions = excluded.open_questions,
  next_steps = excluded.next_steps,
  confidence = excluded.confidence,
  generated_by = excluded.generated_by,
  updated_at = excluded.updated_at`,
		card.SessionID, card.ProjectID, card.Goal, card.Status,
		decisions, files, commands, errs,
		card.OpenQuestions, card.NextSteps, card.Confidence, card.GeneratedBy,
		unixOrZero(card.CreatedAt), unixOrZero(card.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("upsert task card for session %d: %w", card.SessionID, err)
	}
	// 走 ON CONFLICT DO UPDATE 时 last_insert_rowid() 不指向被更新的那行，必须回查。
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM task_card WHERE session_id = ?`, card.SessionID).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

// TaskCardBySession 取会话的任务卡；没有卡时返回包装过的 sql.ErrNoRows。
func (s *Store) TaskCardBySession(ctx context.Context, sessionID int64) (domain.TaskCard, error) {
	card, err := scanTaskCard(s.db.QueryRowContext(ctx, `
SELECT id, session_id, project_id, goal, status, decisions_json, files_json, commands_json,
  errors_json, open_questions, next_steps, confidence, generated_by, created_at, updated_at
FROM task_card WHERE session_id = ?`, sessionID))
	if err != nil {
		return domain.TaskCard{}, fmt.Errorf("task card for session %d: %w", sessionID, err)
	}
	return card, nil
}

// ListTaskCards 按项目取任务卡，顺序跟随所属会话的最近活动时间。
// 简报要的是「最近在做什么」，不是「卡片最后被改的时间」——两者在重扫时会错开。
func (s *Store) ListTaskCards(ctx context.Context, projectID int64, status string, limit int) ([]domain.TaskCard, error) {
	if limit <= 0 || limit > 100 {
		limit = 5
	}
	q := `
SELECT t.id FROM task_card t
JOIN session se ON se.id = t.session_id
WHERE t.project_id = ?`
	args := []any{projectID}
	if status != "" {
		q += ` AND t.status = ?`
		args = append(args, status)
	}
	q += ` ORDER BY se.updated_at DESC, t.id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
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

	// 连接数只有 1，必须先把 id 收完再逐条查，不能在 rows 未读尽时再发查询。
	out := make([]domain.TaskCard, 0, len(ids))
	for _, id := range ids {
		card, err := s.taskCardByID(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, nil
}

func (s *Store) taskCardByID(ctx context.Context, id int64) (domain.TaskCard, error) {
	return scanTaskCard(s.db.QueryRowContext(ctx, `
SELECT id, session_id, project_id, goal, status, decisions_json, files_json, commands_json,
  errors_json, open_questions, next_steps, confidence, generated_by, created_at, updated_at
FROM task_card WHERE id = ?`, id))
}

// GetTaskCard 按 id 取任务卡。找不到返回 domain.ErrTaskCardNotFound，
// 让 MCP 层能给出「没有这张卡」而不是把空白卡当成结果贴回去。
func (s *Store) GetTaskCard(ctx context.Context, id int64) (domain.TaskCard, error) {
	card, err := s.taskCardByID(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TaskCard{}, domain.ErrTaskCardNotFound
	}
	return card, err
}

func scanTaskCard(row *sql.Row) (domain.TaskCard, error) {
	var (
		card      domain.TaskCard
		decisions string
		files     string
		commands  string
		errs      string
		created   int64
		updated   int64
	)
	err := row.Scan(
		&card.ID, &card.SessionID, &card.ProjectID, &card.Goal, &card.Status,
		&decisions, &files, &commands, &errs,
		&card.OpenQuestions, &card.NextSteps, &card.Confidence, &card.GeneratedBy, &created, &updated)
	if err != nil {
		return domain.TaskCard{}, err
	}
	card.Decisions, card.Files, card.Commands, card.Errors, err = decodeLists(decisions, files, commands, errs)
	if err != nil {
		return domain.TaskCard{}, err
	}
	card.CreatedAt = timeFromUnix(created)
	card.UpdatedAt = timeFromUnix(updated)
	return card, nil
}

func decodeLists(decisions, files, commands, errs string) ([]string, []string, []string, []string, error) {
	var d, f, c, e []string
	for _, item := range []struct {
		raw  string
		dest *[]string
	}{{decisions, &d}, {files, &f}, {commands, &c}, {errs, &e}} {
		if item.raw == "" {
			continue
		}
		if err := json.Unmarshal([]byte(item.raw), item.dest); err != nil {
			return nil, nil, nil, nil, err
		}
	}
	return d, f, c, e, nil
}
