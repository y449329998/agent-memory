package store

import (
	"database/sql"
	"fmt"
)

// migrateV1 把旧库从「一次对话折叠进同项目的进行中任务」改成「一次对话一条任务」。
//
// 只有同时满足三个条件的任务转成对话任务：从未手工编辑、只关联一个会话、
// 且这个会话没有被别的候选任务同时认领。手工编辑过的、或关联多个会话的任务
// 本来就是合并视图，保持 formal。
func migrateV1(db *sql.DB) error {
	if has, err := taskHasColumn(db, "kind"); err != nil {
		return err
	} else if !has {
		if _, err := db.Exec(`ALTER TABLE task ADD COLUMN kind TEXT NOT NULL DEFAULT 'formal'`); err != nil {
			return fmt.Errorf("add task.kind: %w", err)
		}
	}
	if has, err := taskHasColumn(db, "session_id"); err != nil {
		return err
	} else if !has {
		if _, err := db.Exec(`ALTER TABLE task ADD COLUMN session_id INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("add task.session_id: %w", err)
		}
	}
	type link struct {
		taskID    int64
		sessionID int64
	}
	rows, err := db.Query(`
SELECT MIN(t.id), ts.session_id
FROM task t JOIN task_session ts ON ts.task_id = t.id
WHERE t.kind = 'formal' AND t.manual_edited_at = 0
  AND (SELECT COUNT(DISTINCT s2.session_id) FROM task_session s2 WHERE s2.task_id = t.id) = 1
GROUP BY ts.session_id`)
	if err != nil {
		return fmt.Errorf("read migration candidates: %w", err)
	}
	var links []link
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.taskID, &l.sessionID); err != nil {
			rows.Close()
			return err
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, l := range links {
		if _, err := db.Exec(`UPDATE task SET kind = 'session', session_id = ? WHERE id = ?`, l.sessionID, l.taskID); err != nil {
			return fmt.Errorf("convert task %d: %w", l.taskID, err)
		}
	}
	return nil
}

// taskHasColumn 读 task 表列名：SQLite 的 ALTER TABLE 没有 IF NOT EXISTS，
// 迁移必须能重复执行。
func taskHasColumn(db *sql.DB, name string) (bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info('task')`)
	if err != nil {
		return false, fmt.Errorf("read task columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var got string
		if err := rows.Scan(&got); err != nil {
			return false, err
		}
		if got == name {
			return true, nil
		}
	}
	return false, rows.Err()
}
