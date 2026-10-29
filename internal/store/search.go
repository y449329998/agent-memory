package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 200
)

// FTS 是基于 FTS5 的检索实现，满足 domain.SearchProvider。
// 索引文本与查询文本必须同源：索引写 Segment(canon) 的结果，查询用 QueryTokens(canon)。
// 换成 trigram 或 unicode61 直搜原文，中文两字词会全部搜不到（见 spec 5.3 实测）。
type FTS struct {
	store *Store
}

// NewFTS 给 CLI 与 M3 的 daemon 构造检索器：字段非导出，包外只能用它。
func NewFTS(s *Store) domain.SearchProvider { return FTS{store: s} }

var _ domain.SearchProvider = FTS{}

func (f FTS) Search(ctx context.Context, q domain.SearchQuery) ([]domain.Hit, error) {
	tokens := normalize.QueryTokens(normalize.CanonicalText(q.Text))
	expr := normalize.MatchExpr(tokens...)
	if expr == "" {
		return nil, nil
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	where := []string{"memory_fts MATCH ?"}
	args := []any{expr}
	switch {
	case q.AllProjects:
		// 跨项目检索：不按项目过滤；显式给了 scope 时再按范围收窄。
		if q.Scope != "" {
			where = append(where, `m.scope = ?`)
			args = append(args, string(q.Scope))
		}
	case q.ProjectID > 0:
		where = append(where, `(m.project_id = ? OR m.scope = ?)`)
		args = append(args, q.ProjectID, string(domain.ScopeUser))
		if q.Scope != "" {
			where = append(where, `m.scope = ?`)
			args = append(args, string(q.Scope))
		}
	case q.Scope == domain.ScopeProject:
		// 显式要 project 级却没给项目：没有任何条目能匹配，保持 CLI 简单，直接空结果。
		return nil, nil
	default:
		// 没有项目上下文时只查用户级，与 ListMemories(0) 及工具描述「省略则只查全局记忆」一致。
		scope := q.Scope
		if scope == "" {
			scope = domain.ScopeUser
		}
		where = append(where, `m.scope = ?`)
		args = append(args, string(scope))
	}
	if q.Type != "" {
		where = append(where, `m.type = ?`)
		args = append(args, string(q.Type))
	}
	if !q.IncludeStale {
		where = append(where, `m.stale = 0`)
	}

	sqlText := `
SELECT m.id, bm25(memory_fts) AS score
FROM memory_fts
JOIN memory_item m ON m.id = memory_fts.rowid
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY score ASC, m.last_seen_at DESC, m.id ASC
LIMIT ?`
	args = append(args, limit)

	rows, err := f.store.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search %q: %w", q.Text, err)
	}
	defer rows.Close()

	type pair struct {
		id    int64
		score float64
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.score); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hits := make([]domain.Hit, 0, len(pairs))
	for _, p := range pairs {
		memory, err := f.store.MemoryByID(ctx, p.id)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		hits = append(hits, domain.Hit{Memory: memory, Score: p.score, Origin: "fts"})
	}
	return hits, nil
}

// RecentMemories 给交接简报取「最近见过」的条目，检索排序不参与。
func (s *Store) RecentMemories(ctx context.Context, projectID int64, typ domain.MemoryType, limit int) ([]domain.MemoryItem, error) {
	if limit <= 0 || limit > maxSearchLimit {
		limit = defaultSearchLimit
	}
	q := `SELECT id FROM memory_item WHERE stale = 0 AND (project_id = ? OR scope = ?)`
	args := []any{projectID, string(domain.ScopeUser)}
	if typ != "" {
		q += ` AND type = ?`
		args = append(args, string(typ))
	}
	q += ` ORDER BY last_seen_at DESC, id LIMIT ?`
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

// RecentMemoriesByType 按 scope 与一组类型取最近见过的条目。
// 简报需要「全局偏好」与「本项目约束」两组，各自单独取，互不挤占额度。
func (s *Store) RecentMemoriesByType(ctx context.Context, projectID int64, scope domain.Scope, types []domain.MemoryType, limit int) ([]domain.MemoryItem, error) {
	if limit <= 0 || limit > maxSearchLimit {
		limit = defaultSearchLimit
	}
	q := `SELECT id FROM memory_item WHERE stale = 0 AND scope = ?`
	args := []any{string(scope)}
	if scope == domain.ScopeProject {
		q += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if len(types) > 0 {
		placeholders := strings.Repeat("?,", len(types))
		q += ` AND type IN (` + strings.TrimSuffix(placeholders, ",") + `)`
		for _, t := range types {
			args = append(args, string(t))
		}
	}
	q += ` ORDER BY last_seen_at DESC, id LIMIT ?`
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
