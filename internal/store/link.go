package store

import (
	"context"
	"time"
)

// AgentLinkRow 对应 agent_link 表：某个项目的某个 agent 装了区块、装的是哪份文件、内容哈希多少。
type AgentLinkRow struct {
	ProjectID     int64
	Agent         string
	BlockFile     string
	InstalledHash string
	InstalledAt   time.Time
}

func (s *Store) RecordAgentLink(ctx context.Context, projectID int64, agent, blockFile, hash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO agent_link(project_id, agent, block_file, installed_hash, installed_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(project_id, agent) DO UPDATE SET
  block_file = excluded.block_file,
  installed_hash = excluded.installed_hash,
  installed_at = excluded.installed_at`,
		projectID, agent, blockFile, hash, at.Unix())
	return err
}

func (s *Store) ClearAgentLink(ctx context.Context, projectID int64, agent string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agent_link WHERE project_id = ? AND agent = ?`, projectID, agent)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) ListAgentLinks(ctx context.Context) ([]AgentLinkRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT project_id, agent, block_file, installed_hash, installed_at
FROM agent_link ORDER BY project_id, agent`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentLinkRow
	for rows.Next() {
		var (
			l  AgentLinkRow
			at int64
		)
		if err := rows.Scan(&l.ProjectID, &l.Agent, &l.BlockFile, &l.InstalledHash, &at); err != nil {
			return nil, err
		}
		l.InstalledAt = time.Unix(at, 0)
		out = append(out, l)
	}
	return out, rows.Err()
}

// OutdatedEmbeddings 返回需要重算向量条目的 id：没嵌入过、模型换了、或嵌入之后又被重新见过。
// M5 的回填按这个集合走，不做全表重算。
//
// 没配模型时只挑「完全没嵌入过」的条目——不能因为调用方没给模型就把全库列为过期。
func (s *Store) OutdatedEmbeddings(ctx context.Context, model string) ([]int64, error) {
	q := `SELECT id FROM memory_item WHERE stale = 0 AND embedding IS NULL`
	args := []any{}
	if model != "" {
		q = `
SELECT id FROM memory_item
WHERE stale = 0 AND (embedding IS NULL OR embed_model <> ? OR embedded_at < last_seen_at)`
		args = []any{model}
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
