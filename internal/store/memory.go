package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

// UpsertMemory 写入或合并一条记忆。返回 (id, created, err)。
//
// 合并条件是全字段命中唯一索引 (scope, project_id, type, text_norm_hash)：
// 命中时不复制文本，只追加来源并把条目恢复为新鲜。projectID 为 0 表示用户级。
// 适配器只报原文，归一化与哈希都在这层做，保证不同 agent 的排版差异不影响去重。
//
// 入库文本、text_norm_hash 与 FTS 行三者同源：text 用不做小写的 DisplayText 保留
// 标识符与路径的可读大小写，text_norm_hash 与 FTS 仍由小写化的 canon 派生，
// 所以大小写不同的两条照样合并、照样都能被检索命中。
//
// 四步（查询 / 插入 / FTS / 来源）在同一事务里：扫描与 MCP 写入并发时语句会交错，
// 非原子执行要么撞唯一索引，要么在 FTS 失败后留下检索不到的条目。
func (s *Store) UpsertMemory(ctx context.Context, projectID int64, d domain.MemoryDraft, at time.Time) (int64, bool, error) {
	canon := normalize.CanonicalText(d.Text)
	if canon == "" {
		return 0, false, domain.ErrEmptyMemory
	}
	display := normalize.DisplayText(d.Text)
	hash := normalize.Hash(canon)
	now := at.Unix()
	scope := string(d.Scope)
	if scope == "" {
		scope = string(domain.ScopeUser)
	}
	typ := string(d.Type)
	if typ == "" {
		typ = string(domain.TypeFact)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `
SELECT id FROM memory_item
WHERE scope = ? AND project_id = ? AND type = ? AND text_norm_hash = ?`,
		scope, projectID, typ, hash).Scan(&id)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `
INSERT INTO memory_item(scope, project_id, type, text, text_norm_hash, stale, first_seen_at, last_seen_at)
VALUES(?, ?, ?, ?, ?, 0, ?, ?)`,
			scope, projectID, typ, display, hash, now, now)
		if err != nil {
			return 0, false, fmt.Errorf("insert memory: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, false, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO memory_fts(rowid, text_seg) VALUES(?, ?)`,
			id, strings.Join(normalize.Segment(canon), " ")); err != nil {
			return 0, false, fmt.Errorf("index memory %d: %w", id, err)
		}
		if err := addSource(ctx, tx, id, d); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return id, true, nil
	case err != nil:
		return 0, false, err
	default:
		if _, err := tx.ExecContext(ctx,
			`UPDATE memory_item SET last_seen_at = ?, stale = 0 WHERE id = ?`, now, id); err != nil {
			return 0, false, err
		}
		if err := addSource(ctx, tx, id, d); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
}

// addSource 幂等登记来源：同一 (memory_id, agent, path) 只保留一行，
// 重复见到时刷新 quote 而不是插入重复来源。必须与 UpsertMemory 同事务。
func addSource(ctx context.Context, tx *sql.Tx, memoryID int64, d domain.MemoryDraft) error {
	agent := d.SourceAgent
	if agent == "" {
		agent = "mcp"
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO memory_source(memory_id, agent, path, quote)
VALUES(?, ?, ?, ?)
ON CONFLICT(memory_id, agent, path) DO UPDATE SET quote = excluded.quote`,
		memoryID, agent, d.SourcePath, truncate(d.Quote, 400))
	if err != nil {
		return fmt.Errorf("add source for memory %d: %w", memoryID, err)
	}
	return nil
}

// truncate 按 rune 截断，避免把 UTF-8 字符切成半个。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// UpdateMemory 编辑已有条目的正文与类型。若改后与同范围/项目/类型下的
// 另一条内容完全相同，则把来源并入那条、把本条标过时，并返回被合并的 id；
// 否则原地更新并重建 FTS。范围、所属项目、首次发现时间和过时状态都不变。
func (s *Store) UpdateMemory(ctx context.Context, id int64, text string, typ domain.MemoryType, at time.Time) (int64, error) {
	old, err := s.MemoryByID(ctx, id)
	if err != nil {
		return 0, err
	}
	canon := normalize.CanonicalText(text)
	if canon == "" {
		return 0, domain.ErrEmptyMemory
	}
	display := normalize.DisplayText(text)
	hash := normalize.Hash(canon)
	if typ == "" {
		typ = old.Type
	}
	if !domain.ValidMemoryType(typ) {
		return 0, domain.ErrInvalidMemoryType
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var other int64
	err = tx.QueryRowContext(ctx, `
SELECT id FROM memory_item
WHERE scope = ? AND project_id = ? AND type = ? AND text_norm_hash = ? AND id <> ?
LIMIT 1`, old.Scope, old.ProjectID, typ, hash, id).Scan(&other)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO memory_source(memory_id, agent, path, quote)
SELECT ?, agent, path, quote FROM memory_source WHERE memory_id = ?`, other, id); err != nil {
			return 0, fmt.Errorf("merge memory sources: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE memory_item SET stale = 1, last_seen_at = ? WHERE id = ?`, at.Unix(), id); err != nil {
			return 0, fmt.Errorf("stale merged memory: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return other, nil
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE memory_item SET type = ?, text = ?, text_norm_hash = ?, last_seen_at = ? WHERE id = ?`,
		typ, display, hash, at.Unix(), id); err != nil {
		return 0, fmt.Errorf("update memory: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE rowid = ?`, id); err != nil {
		return 0, fmt.Errorf("reindex memory %d: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_fts(rowid, text_seg) VALUES(?, ?)`,
		id, strings.Join(normalize.Segment(canon), " ")); err != nil {
		return 0, fmt.Errorf("reindex memory %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return 0, nil
}

func (s *Store) MemoryByID(ctx context.Context, id int64) (domain.MemoryItem, error) {
	var (
		m           domain.MemoryItem
		stale       int
		first, last int64
		embeddedAt  int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, scope, project_id, type, text, text_norm_hash, stale, first_seen_at, last_seen_at, embed_model, embed_dim, embedded_at
FROM memory_item WHERE id = ?`, id).Scan(
		&m.ID, &m.Scope, &m.ProjectID, &m.Type, &m.Text, &m.TextNormHash, &stale, &first, &last,
		&m.EmbedModel, &m.EmbedDim, &embeddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryItem{}, domain.ErrMemoryNotFound
	}
	if err != nil {
		return domain.MemoryItem{}, err
	}
	m.Stale = stale != 0
	m.FirstSeenAt = time.Unix(first, 0)
	m.LastSeenAt = time.Unix(last, 0)
	if embeddedAt != 0 {
		m.EmbeddedAt = time.Unix(embeddedAt, 0)
	}
	sources, err := s.sourcesFor(ctx, id)
	if err != nil {
		return domain.MemoryItem{}, err
	}
	m.Sources = sources
	if summary, ok, err := s.SummaryFor(ctx, "memory", id); err != nil {
		return domain.MemoryItem{}, err
	} else if ok {
		m.Summary, m.SummaryState, m.SummaryError = summary.Summary, summary.State, summary.Error
	}
	return m, nil
}

func (s *Store) sourcesFor(ctx context.Context, memoryID int64) ([]domain.MemorySource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT memory_id, agent, path, quote FROM memory_source WHERE memory_id = ? ORDER BY agent, path`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MemorySource
	for rows.Next() {
		var ms domain.MemorySource
		if err := rows.Scan(&ms.MemoryID, &ms.Agent, &ms.Path, &ms.Quote); err != nil {
			return nil, err
		}
		out = append(out, ms)
	}
	return out, rows.Err()
}

// ListMemories 按项目列条目。projectID 为 0 时只列用户级；大于 0 时同时带出用户级，
// 因为交接简报需要「全局偏好 + 本项目约束」两部分。
//
// 连接数固定为 1：先把 id 收成切片并关闭 rows，再逐条 MemoryByID，
// 不能边遍历边查询（会等不到空闲连接而静默挂起）。
func (s *Store) ListMemories(ctx context.Context, projectID int64, includeStale bool, limit int) ([]domain.MemoryItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT id FROM memory_item WHERE 1=1`
	args := []any{}
	if projectID == 0 {
		q += ` AND scope = ?`
		args = append(args, string(domain.ScopeUser))
	} else {
		q += ` AND (project_id = ? OR scope = ?)`
		args = append(args, projectID, string(domain.ScopeUser))
	}
	if !includeStale {
		q += ` AND stale = 0`
	}
	q += ` ORDER BY type, last_seen_at DESC, id LIMIT ?`
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
	// 显式关闭，保证后续逐条查询能拿到唯一那条连接。
	rows.Close()

	out := make([]domain.MemoryItem, 0, len(ids))
	for _, id := range ids {
		m, err := s.MemoryByID(ctx, id)
		if errors.Is(err, domain.ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// SetStaleByID 置或清条目的过时标记。MCP 的 supersedes、doctor 与测试都走这里；
// 直接对外暴露 *sql.DB 会让「唯一写入口」这条边界失效。
func (s *Store) SetStaleByID(ctx context.Context, id int64, stale bool) error {
	flag := 0
	if stale {
		flag = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE memory_item SET stale = ? WHERE id = ?`, flag, id)
	return err
}

// MarkStaleForRun 把「所有支撑来源都属于本轮扫过的 agent、且本轮一条都没再见到」的条目置为过时。
// 共同支撑的条目只要有一个来源本轮见过就保持新鲜；来源属于未扫描 agent 的条目一律不动——
// 那些 agent 这一轮根本没看，不能把「没看」当成「不见了」。
// 「本轮见过」用 >= 判定：两个进程同时扫描时，旧轮次收尾不能把新轮次刚 touch 的源
// 当成没见到。
// 不硬删——agent 重写记忆文件时常常只是换措辞，直接删会把结论抹掉。
func (s *Store) MarkStaleForRun(ctx context.Context, agents []string, runAt int64) (int, error) {
	if len(agents) == 0 {
		return 0, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(agents)), ",")
	args := make([]any, 0, 3*len(agents)+1)
	for i := 0; i < 3; i++ {
		for _, a := range agents {
			args = append(args, a)
		}
	}
	args = append(args, runAt)

	res, err := s.db.ExecContext(ctx, `
UPDATE memory_item SET stale = 1
WHERE stale = 0
  AND id IN (SELECT memory_id FROM memory_source WHERE agent IN (`+ph+`))
  AND NOT EXISTS (
    SELECT 1 FROM memory_source ms
    WHERE ms.memory_id = memory_item.id AND ms.agent NOT IN (`+ph+`))
  AND NOT EXISTS (
    SELECT 1 FROM memory_source ms
    JOIN source s ON s.agent = ms.agent AND s.path = ms.path
    WHERE ms.memory_id = memory_item.id AND ms.agent IN (`+ph+`) AND s.last_seen_run >= ?)`,
		args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// MarkStaleSuperseded 把一个源里本轮不再出现的旧条目置为过时。
// 保留本轮刚写入的 keepIDs；被其他 agent 的新鲜来源支撑的条目不动。
//
// 场景：agent 重写了记忆文件。旧条目仍挂在同一个 (agent, path) 来源上，
// 该路径本轮又见过，MarkStaleForRun 便不会再判它，结果新旧两版长期并存。
// 这里按「本轮该源写入/命中的集合」做差集，把被替换掉的旧版本收掉。
// keepIDs 为空表示该源本轮没有产出任何条目，所有旧条目都应过时。
func (s *Store) MarkStaleSuperseded(ctx context.Context, agent, kind, path string, keepIDs []int64, runAt int64) (int, error) {
	where := `stale = 0
  AND id IN (SELECT memory_id FROM memory_source WHERE agent = ? AND path = ?)
  AND NOT EXISTS (
    SELECT 1 FROM memory_source ms
    JOIN source src ON src.agent = ms.agent AND src.kind = ? AND src.path = ms.path
    WHERE ms.memory_id = memory_item.id
      AND NOT (ms.agent = ? AND ms.path = ?)
      AND src.last_seen_run >= ?)`
	args := []any{agent, path, kind, agent, path, runAt}

	if len(keepIDs) > 0 {
		where += ` AND id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(keepIDs)), ",") + `)`
		for _, id := range keepIDs {
			args = append(args, id)
		}
	}

	res, err := s.db.ExecContext(ctx, `UPDATE memory_item SET stale = 1 WHERE `+where, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// UnstaleBySource 把某个记忆源支撑的过时条目恢复为新鲜。
//
// 只在「该源刚从解析错误中恢复」时调用：错误那一轮源没露面，条目被
// MarkStaleForRun 判成过时，而文件恢复原样时哈希未变只走 TouchSource，
// 不复活就会一直挂着 stale。
// 已知取舍：人工 supersedes 标过的条目若仍被该源支撑，也会一并复活；
// 因此调用条件必须保持这么窄，不要扩大到「每次 touch 都复活」。
func (s *Store) UnstaleBySource(ctx context.Context, agent, kind, path string) (int, error) {
	if kind != "memory" {
		// 只有记忆源会出现在 memory_source 里；会话源不该触发复活。
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx, `
UPDATE memory_item SET stale = 0
WHERE stale = 1
  AND id IN (SELECT memory_id FROM memory_source WHERE agent = ? AND path = ?)`,
		agent, path)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// PurgeStale 真删所有过时条目，并清掉 FTS 行与来源行。
// memory_fts 是独立虚表，外键级联管不到它，必须显式删除；三者同一事务。
func (s *Store) PurgeStale(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM memory_item WHERE stale = 1`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE rowid = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_source WHERE memory_id = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_item WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// MemoryFilter 是只读浏览用的过滤条件。与 SearchQuery 不同，它不做相关性排序，
// 只按最近变更排；AllProjects 显式表示跨项目浏览，避免与「省略项目 = 用户级」混淆。
type MemoryFilter struct {
	ProjectID   int64
	AllProjects bool
	Scope       domain.Scope
	Type        domain.MemoryType
	Stale       *bool
	Limit       int
	Offset      int
}

func (s *Store) ListMemoriesFiltered(ctx context.Context, f MemoryFilter) ([]domain.MemoryItem, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q := `SELECT id FROM memory_item WHERE 1=1`
	args := []any{}
	switch {
	case f.AllProjects:
	case f.ProjectID > 0:
		q += ` AND (project_id = ? OR scope = ?)`
		args = append(args, f.ProjectID, string(domain.ScopeUser))
	default:
		q += ` AND scope = ?`
		args = append(args, string(domain.ScopeUser))
	}
	if f.Scope != "" {
		q += ` AND scope = ?`
		args = append(args, string(f.Scope))
	}
	if f.Type != "" {
		q += ` AND type = ?`
		args = append(args, string(f.Type))
	}
	if f.Stale != nil {
		if *f.Stale {
			q += ` AND stale = 1`
		} else {
			q += ` AND stale = 0`
		}
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	q += ` ORDER BY last_seen_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, f.Limit, f.Offset)

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

	out := make([]domain.MemoryItem, 0, len(ids))
	for _, id := range ids {
		m, err := s.MemoryByID(ctx, id)
		if errors.Is(err, domain.ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// memoryWhere 把过滤条件拼成 WHERE 片段，列表与计数共用，避免两套条件漂移。
func memoryWhere(f MemoryFilter) (string, []any) {
	q := ` WHERE 1=1`
	args := []any{}
	switch {
	case f.AllProjects:
	case f.ProjectID > 0:
		q += ` AND (project_id = ? OR scope = ?)`
		args = append(args, f.ProjectID, string(domain.ScopeUser))
	default:
		q += ` AND scope = ?`
		args = append(args, string(domain.ScopeUser))
	}
	if f.Scope != "" {
		q += ` AND scope = ?`
		args = append(args, string(f.Scope))
	}
	if f.Type != "" {
		q += ` AND type = ?`
		args = append(args, string(f.Type))
	}
	if f.Stale != nil {
		if *f.Stale {
			q += ` AND stale = 1`
		} else {
			q += ` AND stale = 0`
		}
	}
	return q, args
}

// CountMemoriesFiltered 返回与列表相同条件下的总数，供 UI 显示「共 N 条」。
func (s *Store) CountMemoriesFiltered(ctx context.Context, f MemoryFilter) (int, error) {
	where, args := memoryWhere(f)
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM memory_item`+where, args...).Scan(&n)
	return n, err
}
