package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SummaryRow struct {
	Kind        string
	ID          int64
	SourceHash  string
	Summary     string
	State       string
	Model       string
	Error       string
	Attempts    int
	RequestedAt time.Time
	UpdatedAt   time.Time
}

func (s *Store) MarkSummaryPending(ctx context.Context, kind string, id int64, hash string, at time.Time) error {
	var existingHash, state string
	err := s.db.QueryRowContext(ctx, `SELECT source_hash, state FROM summary WHERE entity_kind = ? AND entity_id = ?`, kind, id).Scan(&existingHash, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && existingHash == hash &&
		(state == "ready" || state == "pending" || state == "processing" || state == "failed") {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO summary(entity_kind, entity_id, source_hash, summary, state, model, error, attempts, requested_at, updated_at)
VALUES(?, ?, ?, '', 'pending', '', '', 0, ?, ?)
ON CONFLICT(entity_kind, entity_id) DO UPDATE SET
  source_hash = excluded.source_hash,
  summary = CASE WHEN summary.source_hash = excluded.source_hash THEN summary.summary ELSE '' END,
  state = 'pending',
  error = '',
  attempts = 0,
  requested_at = excluded.requested_at,
  updated_at = excluded.updated_at`,
		kind, id, hash, at.Unix(), at.Unix())
	return err
}

func (s *Store) NextSummary(ctx context.Context, now time.Time) (SummaryRow, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT entity_kind, entity_id, source_hash, summary, state, model, error, attempts, requested_at, updated_at
FROM summary
WHERE state IN ('pending','failed') AND attempts < 3
  AND (state <> 'failed' OR updated_at <= ?)
ORDER BY requested_at LIMIT 1`, now.Add(-30*time.Second).Unix())
	item, err := scanSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SummaryRow{}, false, nil
	}
	if err != nil {
		return SummaryRow{}, false, err
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE summary SET state = 'processing', updated_at = ?
WHERE entity_kind = ? AND entity_id = ? AND state IN ('pending','failed')`,
		now.Unix(), item.Kind, item.ID)
	if err != nil {
		return SummaryRow{}, false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return SummaryRow{}, false, nil
	}
	item.State = "processing"
	return item, true, nil
}

func (s *Store) SaveSummary(ctx context.Context, kind string, id int64, hash, summary, model string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE summary SET summary = ?, state = 'ready', model = ?, error = '', updated_at = ?
WHERE entity_kind = ? AND entity_id = ? AND source_hash = ?`,
		summary, model, at.Unix(), kind, id, hash)
	return err
}

func (s *Store) FailSummary(ctx context.Context, kind string, id int64, message string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE summary SET state = 'failed', error = ?, attempts = attempts + 1, updated_at = ?
WHERE entity_kind = ? AND entity_id = ?`, message, at.Unix(), kind, id)
	return err
}

func (s *Store) SummaryFor(ctx context.Context, kind string, id int64) (SummaryRow, bool, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT entity_kind, entity_id, source_hash, summary, state, model, error, attempts, requested_at, updated_at
FROM summary WHERE entity_kind = ? AND entity_id = ?`, kind, id)
	item, err := scanSummary(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SummaryRow{}, false, nil
	}
	return item, err == nil, err
}

func scanSummary(row *sql.Row) (SummaryRow, error) {
	var item SummaryRow
	var requested, updated int64
	err := row.Scan(&item.Kind, &item.ID, &item.SourceHash, &item.Summary, &item.State,
		&item.Model, &item.Error, &item.Attempts, &requested, &updated)
	item.RequestedAt = timeFromUnix(requested)
	item.UpdatedAt = timeFromUnix(updated)
	return item, err
}

func (s *Store) SummaryCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM summary GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

func (s *Store) ResetProcessingSummaries(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE summary SET state = 'pending', updated_at = ? WHERE state = 'processing'`, at.Unix())
	return err
}

// RetryFailedSummaries 把失败项重新放回队列；重试次数清零，允许再试 3 次。
func (s *Store) RetryFailedSummaries(ctx context.Context, at time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE summary SET state = 'pending', attempts = 0, error = '', updated_at = ? WHERE state = 'failed'`, at.Unix())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (s *Store) MemoryText(ctx context.Context, id int64) (string, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT text FROM memory_item WHERE id = ?`, id).Scan(&text)
	return text, err
}

func (s *Store) TaskSummarySource(ctx context.Context, id int64) (string, error) {
	var goal, next, filesJSON, commandsJSON, errorsJSON string
	if err := s.db.QueryRowContext(ctx, `
SELECT goal, next_steps, files_json, commands_json, errors_json FROM task WHERE id = ?`, id).
		Scan(&goal, &next, &filesJSON, &commandsJSON, &errorsJSON); err != nil {
		return "", err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT subject, status FROM task_step WHERE task_id = ? ORDER BY seq`, id)
	if err != nil {
		return "", err
	}
	var steps []string
	for rows.Next() {
		var subject, status string
		if err := rows.Scan(&subject, &status); err != nil {
			rows.Close()
			return "", err
		}
		steps = append(steps, fmt.Sprintf("- [%s] %s", status, subject))
	}
	rows.Close()

	var b strings.Builder
	b.WriteString(goal)
	if next != "" {
		b.WriteString("\n下一步：" + next)
	}
	if len(steps) > 0 {
		b.WriteString("\n步骤：\n" + strings.Join(steps, "\n"))
	}
	_, files, commands, errs, err := decodeLists("", filesJSON, commandsJSON, errorsJSON)
	if err != nil {
		return "", err
	}
	if len(files) > 0 {
		b.WriteString("\n改动文件：" + strings.Join(headStrings(files, 10), ", "))
	}
	if len(commands) > 0 {
		b.WriteString("\n执行命令：" + strings.Join(headStrings(commands, 5), " && "))
	}
	if len(errs) > 0 {
		b.WriteString("\n未解决报错：" + errs[0])
	}

	rows, err = s.db.QueryContext(ctx, `SELECT role, text FROM task_snippet WHERE task_id = ? ORDER BY seq DESC LIMIT 5`, id)
	if err != nil {
		return "", err
	}
	var snippets []string
	for rows.Next() {
		var role, text string
		if err := rows.Scan(&role, &text); err != nil {
			rows.Close()
			return "", err
		}
		snippets = append([]string{fmt.Sprintf("- [%s] %s", role, text)}, snippets...)
	}
	rows.Close()
	if len(snippets) > 0 {
		b.WriteString("\n最近对话：\n" + strings.Join(snippets, "\n"))
	}
	return strings.TrimSpace(b.String()), nil
}

func headStrings(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}
