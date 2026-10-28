package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// PutSource 记录一个源文件的当前状态。哈希变化时把旧哈希留在 prev_hash，
// 并把 changed_at 置成本轮时间——doctor 用它回答「这个文件什么时候变过」。
// 同一份内容重复写入（重扫、重启）不会记为变更；首次入库没有旧哈希可对照，
// 因此 changed_at 保持 0。成功写入同时清空 last_error。
func (s *Store) PutSource(ctx context.Context, st domain.SourceState, runAt int64) error {
	var mtime int64
	if !st.MTime.IsZero() {
		mtime = st.MTime.Unix()
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO source(agent, kind, path, content_hash, prev_hash, changed_at, mtime, size, last_seen_run, updated_at, last_error)
VALUES(?, ?, ?, ?, '', 0, ?, ?, ?, ?, '')
ON CONFLICT(agent, kind, path) DO UPDATE SET
  prev_hash = CASE WHEN source.content_hash = excluded.content_hash THEN source.prev_hash ELSE source.content_hash END,
  changed_at = CASE WHEN source.content_hash = excluded.content_hash THEN source.changed_at ELSE ? END,
  content_hash = excluded.content_hash,
  mtime = excluded.mtime,
  size = excluded.size,
  last_seen_run = excluded.last_seen_run,
  updated_at = excluded.updated_at,
  last_error = ''`,
		st.Agent, st.Kind, st.Path, st.ContentHash, mtime, st.Size, runAt, time.Now().Unix(), runAt)
	if err != nil {
		return fmt.Errorf("put source %s:%s: %w", st.Agent, st.Path, err)
	}
	return nil
}

// TouchSource 只刷新 last_seen_run：内容没变的源仍然要在本轮"露过面"，
// 否则 stale 判定会把没变化的条目误判成已消失。
// 源行不存在时显式报错——静默成功会让编排层以为这一轮已经见过它。
func (s *Store) TouchSource(ctx context.Context, agent, kind, path string, runAt int64) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE source SET last_seen_run = ?, updated_at = ?
WHERE agent = ? AND kind = ? AND path = ?`,
		runAt, time.Now().Unix(), agent, kind, path)
	if err != nil {
		return fmt.Errorf("touch source %s:%s: %w", agent, path, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("touch source %s:%s: no such row", agent, path)
	}
	return nil
}

// MarkSourceError 只登记错误，不动 content_hash 也不推进 last_seen_run。
// 推进了会让 stale 判定以为「这一轮确实见到了这个源」。
func (s *Store) MarkSourceError(ctx context.Context, agent, kind, path, msg string, at int64) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO source(agent, kind, path, content_hash, last_error, updated_at)
VALUES(?, ?, ?, '', ?, ?)
ON CONFLICT(agent, kind, path) DO UPDATE SET
  last_error = excluded.last_error,
  updated_at = excluded.updated_at`,
		agent, kind, path, msg, at)
	if err != nil {
		return fmt.Errorf("mark source error %s:%s: %w", agent, path, err)
	}
	return nil
}

// SourceHash 返回源文件上次成功入库时记录的内容哈希；源不存在时 ok 为 false。
func (s *Store) SourceHash(ctx context.Context, agent, kind, path string) (string, bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT content_hash FROM source WHERE agent = ? AND kind = ? AND path = ?`, agent, kind, path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

func (s *Store) GetSource(ctx context.Context, agent, kind, path string) (domain.SourceState, error) {
	var (
		st        domain.SourceState
		mtime     int64
		changedAt int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT agent, kind, path, content_hash, prev_hash, changed_at, mtime, size, last_error, last_seen_run
FROM source WHERE agent = ? AND kind = ? AND path = ?`, agent, kind, path).Scan(
		&st.Agent, &st.Kind, &st.Path, &st.ContentHash, &st.PrevHash, &changedAt,
		&mtime, &st.Size, &st.LastError, &st.LastSeenRun)
	if err != nil {
		return domain.SourceState{}, fmt.Errorf("get source %s:%s: %w", agent, path, err)
	}
	st.MTime = time.Unix(mtime, 0)
	if changedAt != 0 {
		st.ChangedAt = time.Unix(changedAt, 0)
	}
	return st, nil
}

// ChangedSource 是有变更记录（ChangedSources）或最近失败（SourceErrors）的源。
// 两种查询共用此结构，ChangedAt 分别承载 changed_at 与 updated_at。
type ChangedSource struct {
	Agent       string
	Kind        string
	Path        string
	PrevHash    string
	ContentHash string
	ChangedAt   time.Time
}

// ChangedSources 返回有变更记录的源，供 doctor 列出「哪些原生文件动过」。
// since 为 0 时不过滤。
func (s *Store) ChangedSources(ctx context.Context, since int64) ([]ChangedSource, error) {
	q := `SELECT agent, kind, path, prev_hash, content_hash, changed_at FROM source WHERE changed_at <> 0`
	args := []any{}
	if since > 0 {
		q += ` AND changed_at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY changed_at DESC LIMIT 200`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangedSource
	for rows.Next() {
		var (
			c         ChangedSource
			changedAt int64
		)
		if err := rows.Scan(&c.Agent, &c.Kind, &c.Path, &c.PrevHash, &c.ContentHash, &changedAt); err != nil {
			return nil, err
		}
		c.ChangedAt = time.Unix(changedAt, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SourceErrors 返回最近解析失败的源，供 doctor 提示用户修文件。
func (s *Store) SourceErrors(ctx context.Context) ([]ChangedSource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent, kind, path, prev_hash, content_hash, updated_at FROM source
WHERE last_error <> '' ORDER BY updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangedSource
	for rows.Next() {
		var (
			c         ChangedSource
			updatedAt int64
		)
		if err := rows.Scan(&c.Agent, &c.Kind, &c.Path, &c.PrevHash, &c.ContentHash, &updatedAt); err != nil {
			return nil, err
		}
		c.ChangedAt = time.Unix(updatedAt, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Stats 汇总库内规模与分布，供 memory stats / doctor 展示。
// 连接数为 1：每个 rows 都读尽并关闭后再发下一个查询。
func (s *Store) Stats(ctx context.Context) (domain.Stats, error) {
	var st domain.Stats
	scalars := []struct {
		q    string
		dest *int
	}{
		{`SELECT count(*) FROM project`, &st.Projects},
		{`SELECT count(*) FROM memory_item`, &st.Memories},
		{`SELECT count(*) FROM memory_item WHERE stale = 1`, &st.Stale},
		{`SELECT count(*) FROM source`, &st.Sources},
		{`SELECT count(*) FROM source WHERE last_error <> ''`, &st.FailedSource},
		{`SELECT count(*) FROM source WHERE changed_at <> 0`, &st.Changed},
	}
	for _, sc := range scalars {
		if err := s.db.QueryRowContext(ctx, sc.q).Scan(sc.dest); err != nil {
			return st, err
		}
	}

	st.MemoriesByAgent = map[string]int{}
	rows, err := s.db.QueryContext(ctx, `
SELECT ms.agent, count(DISTINCT ms.memory_id) FROM memory_source ms GROUP BY ms.agent`)
	if err != nil {
		return st, err
	}
	for rows.Next() {
		var agent string
		var n int
		if err := rows.Scan(&agent, &n); err != nil {
			rows.Close()
			return st, err
		}
		st.MemoriesByAgent[agent] = n
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return st, err
	}
	rows.Close()

	st.MemoriesByType = map[string]int{}
	rows2, err := s.db.QueryContext(ctx, `SELECT type, count(*) FROM memory_item GROUP BY type`)
	if err != nil {
		return st, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var typ string
		var n int
		if err := rows2.Scan(&typ, &n); err != nil {
			return st, err
		}
		st.MemoriesByType[typ] = n
	}
	return st, rows2.Err()
}
