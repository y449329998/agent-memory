package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// SetStepOverride 记录人工对某个稳定 source_key 步骤的状态修正。
// 只接受稳定 key 与合法状态；不触碰 manual_edited_at，扫描继续同步源文本。
func (s *Store) SetStepOverride(ctx context.Context, taskID int64, sourceKey, status string, at time.Time) error {
	if sourceKey == "" || !stableSourceKey(sourceKey) {
		return fmt.Errorf("step override requires a stable source_key, got %q", sourceKey)
	}
	if !validStepStatus(status) {
		return fmt.Errorf("invalid step status %q", status)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO task_step_override(task_id, source_key, status, updated_at) VALUES(?,?,?,?)
ON CONFLICT(task_id, source_key) DO UPDATE SET status=excluded.status, updated_at=excluded.updated_at`,
		taskID, sourceKey, status, at.Unix())
	return err
}

// stableSourceKey 反推步骤的 key 是 cmd:0/err:1 这类序号，内容一变就错位，不接受覆盖。
func stableSourceKey(k string) bool {
	for _, prefix := range []string{"cmd:", "err:", "next:", "file:"} {
		if strings.HasPrefix(k, prefix) {
			return false
		}
	}
	return true
}

func (s *Store) stepOverrides(ctx context.Context, taskID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_key, status FROM task_step_override WHERE task_id = ?`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// PruneStaleOverrides 删除该任务下已不存在于 task_step 的覆盖（agent 删了那条 todo）。
func (s *Store) PruneStaleOverrides(ctx context.Context, taskID int64, aliveKeys []string) error {
	alive := map[string]bool{}
	for _, k := range aliveKeys {
		alive[k] = true
	}
	ov, err := s.stepOverrides(ctx, taskID)
	if err != nil {
		return err
	}
	for k := range ov {
		if !alive[k] {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM task_step_override WHERE task_id = ? AND source_key = ?`, taskID, k); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyOverrides 把人工状态叠加到读取视图；只命中稳定 key，孤儿不叠加。
func applyOverrides(task *domain.Task, ov map[string]string) {
	if len(ov) == 0 {
		return
	}
	for i := range task.Steps {
		if v, ok := ov[task.Steps[i].SourceKey]; ok && stableSourceKey(task.Steps[i].SourceKey) {
			task.Steps[i].Status = v
		}
	}
}
