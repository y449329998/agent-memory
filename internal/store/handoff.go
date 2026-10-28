package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type HandoffRow struct {
	ProjectID    int64
	Body         string
	ContentHash  string
	BudgetTokens int
	GeneratedAt  time.Time
}

// SaveHandoff 每个项目只保留最近一版简报。内容没变时返回 changed=false，
// 调用方据此决定要不要重写磁盘文件——重写会刷新 mtime，白耗一次监听事件。
func (s *Store) SaveHandoff(ctx context.Context, row HandoffRow) (changed bool, err error) {
	var existing string
	err = s.db.QueryRowContext(ctx, `SELECT content_hash FROM handoff WHERE project_id = ?`, row.ProjectID).Scan(&existing)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		changed = true
	case err != nil:
		return false, err
	default:
		changed = existing != row.ContentHash
	}

	_, err = s.db.ExecContext(ctx, `
INSERT INTO handoff(project_id, body, content_hash, budget_tokens, generated_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(project_id) DO UPDATE SET
  body = excluded.body,
  content_hash = excluded.content_hash,
  budget_tokens = excluded.budget_tokens,
  generated_at = excluded.generated_at`,
		row.ProjectID, row.Body, row.ContentHash, row.BudgetTokens, row.GeneratedAt.Unix())
	if err != nil {
		return false, err
	}
	return changed, nil
}

func (s *Store) LastHandoff(ctx context.Context, projectID int64) (HandoffRow, bool, error) {
	var (
		row       HandoffRow
		generated int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT project_id, body, content_hash, budget_tokens, generated_at
FROM handoff WHERE project_id = ?`, projectID).Scan(
		&row.ProjectID, &row.Body, &row.ContentHash, &row.BudgetTokens, &generated)
	if errors.Is(err, sql.ErrNoRows) {
		return HandoffRow{}, false, nil
	}
	if err != nil {
		return HandoffRow{}, false, err
	}
	row.GeneratedAt = time.Unix(generated, 0)
	return row, true, nil
}
