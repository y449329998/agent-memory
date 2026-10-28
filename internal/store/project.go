package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

// EnsureProject 按 key 建或取项目。已存在时只补空字段，updated_at 只进不退
// （乱序扫描/回放传入更早时间时保持原值），也不覆盖已有 display_name / git_remote /
// primary_path，避免后来者以低质量信息盖掉高质量信息。
func (s *Store) EnsureProject(ctx context.Context, key, displayName, gitRemote, primaryPath string, at time.Time) (int64, error) {
	if key == "" {
		return 0, errors.New("project key is empty")
	}
	now := at.Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project(key, display_name, git_remote, primary_path, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
  updated_at   = MAX(project.updated_at, excluded.updated_at),
  display_name = CASE WHEN project.display_name = '' THEN excluded.display_name ELSE project.display_name END,
  git_remote   = CASE WHEN project.git_remote   = '' THEN excluded.git_remote  ELSE project.git_remote  END,
  primary_path = CASE WHEN project.primary_path = '' THEN excluded.primary_path ELSE project.primary_path END`,
		key, displayName, gitRemote, primaryPath, now, now)
	if err != nil {
		return 0, fmt.Errorf("ensure project %q: %w", key, err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM project WHERE key = ?`, key).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup project %q: %w", key, err)
	}
	return id, nil
}

func (s *Store) ProjectByKey(ctx context.Context, key string) (domain.Project, bool, error) {
	var p domain.Project
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `
SELECT id, key, display_name, git_remote, primary_path, created_at, updated_at
FROM project WHERE key = ?`, key).Scan(
		&p.ID, &p.Key, &p.DisplayName, &p.GitRemote, &p.PrimaryPath, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, false, nil
	}
	if err != nil {
		return domain.Project{}, false, err
	}
	p.CreatedAt = time.Unix(created, 0)
	p.UpdatedAt = time.Unix(updated, 0)
	return p, true, nil
}

func (s *Store) ProjectByID(ctx context.Context, id int64) (domain.Project, bool, error) {
	var (
		p       domain.Project
		created int64
		updated int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, key, display_name, git_remote, primary_path, created_at, updated_at
FROM project WHERE id = ?`, id).Scan(
		&p.ID, &p.Key, &p.DisplayName, &p.GitRemote, &p.PrimaryPath, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, false, nil
	}
	if err != nil {
		return domain.Project{}, false, err
	}
	p.CreatedAt = time.Unix(created, 0)
	p.UpdatedAt = time.Unix(updated, 0)
	return p, true, nil
}

// LinkProjectPath 登记「某 agent 在某处把这个项目写成什么路径」。path 先归一化再存，
// 因为各 agent 对同一路径的写法不同（反斜杠、大小写、尾部分隔符）。
func (s *Store) LinkProjectPath(ctx context.Context, projectID int64, agent, slug, path, confidence string) error {
	key := normalize.ProjectKey("", path)
	if key == "" {
		return errors.New("project path is empty")
	}
	if confidence == "" {
		confidence = "high"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_path(project_id, agent, slug, path, confidence)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(project_id, agent, path) DO UPDATE SET
  slug = CASE WHEN excluded.slug <> '' THEN excluded.slug ELSE project_path.slug END,
  confidence = CASE WHEN excluded.confidence = 'high' THEN 'high' ELSE project_path.confidence END`,
		projectID, agent, slug, key, confidence)
	return err
}

// ResolveProjectByPath 反查路径对应的项目。同一物理路径可能先后被登记到不同项目
// （唯一键含 project_id），因此必须显式排序：高置信度优先，其次最新登记（id 最大），
// 避免结果依赖扫描顺序。
func (s *Store) ResolveProjectByPath(ctx context.Context, path string) (int64, bool, error) {
	key := normalize.ProjectKey("", path)
	if key == "" {
		return 0, false, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `
SELECT project_id FROM project_path WHERE path = ?
ORDER BY CASE confidence WHEN 'high' THEN 0 ELSE 1 END, id DESC
LIMIT 1`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ProjectIDForPath 解析或创建项目：git remote 决定 key，让同一仓库的不同路径写法合并。
// 读不到 remote 时退化为路径 key。primaryPath 存原始写法便于展示。
func (s *Store) ProjectIDForPath(ctx context.Context, agent, slug, rawPath, gitRemote string, at time.Time) (int64, string, error) {
	key := normalize.ProjectKey(gitRemote, rawPath)
	if key == "" {
		return 0, "", fmt.Errorf("cannot derive project key from %q", rawPath)
	}
	id, err := s.EnsureProject(ctx, key, baseName(rawPath), gitRemote, rawPath, at)
	if err != nil {
		return 0, key, err
	}
	if err := s.LinkProjectPath(ctx, id, agent, slug, rawPath, "high"); err != nil {
		return 0, key, err
	}
	return id, key, nil
}

// ProjectIDsForHandoff 列出值得一份简报的项目：有项目级记忆或有任务卡的那些，
// 按 id 升序保证多次运行产出顺序一致。
func (s *Store) ProjectIDsForHandoff(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT DISTINCT id FROM (
  SELECT project_id AS id FROM memory_item WHERE scope = 'project' AND project_id <> 0 AND stale = 0
  UNION SELECT project_id FROM task_card WHERE project_id <> 0
) ORDER BY id`)
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

func baseName(p string) string {
	p = normalize.ProjectKey("", p)
	if i := lastSlash(p); i >= 0 {
		return p[i+1:]
	}
	return p
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
