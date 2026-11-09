package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// TestMigrateV1KeepsMergedTasks 用 v1 形状的库走一遍 Open：
// 只绑一个会话且没手工改过的任务应转成对话任务，
// 手工改过的与绑了多个会话的任务必须留在合并视图里。
func TestMigrateV1KeepsMergedTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE task (
  id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL DEFAULT 0, goal TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'doing', owner_agent TEXT NOT NULL DEFAULT '',
  claimed_at INTEGER NOT NULL DEFAULT 0, next_steps TEXT NOT NULL DEFAULT '',
  decisions_json TEXT NOT NULL DEFAULT '[]', files_json TEXT NOT NULL DEFAULT '[]',
  commands_json TEXT NOT NULL DEFAULT '[]', errors_json TEXT NOT NULL DEFAULT '[]',
  open_questions TEXT NOT NULL DEFAULT '', source_agent TEXT NOT NULL DEFAULT '',
  source_path TEXT NOT NULL DEFAULT '', manual_edited_at INTEGER NOT NULL DEFAULT 0,
  completed_at INTEGER NOT NULL DEFAULT 0, archived_at INTEGER NOT NULL DEFAULT 0,
  merged_into_task_id INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
);
CREATE TABLE task_session (task_id INTEGER NOT NULL, session_id INTEGER NOT NULL, role TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, PRIMARY KEY(task_id, session_id));
INSERT INTO meta(key, value) VALUES('schema_version','1');
INSERT INTO task(id, project_id, goal, created_at, updated_at) VALUES
  (1, 7, '一次对话的任务', 1, 1),
  (2, 7, '手工改过的任务', 1, 1),
  (3, 7, '绑了两个会话的任务', 1, 1);
INSERT INTO task_session(task_id, session_id, role, created_at) VALUES
  (1, 100, 'source', 1), (2, 200, 'source', 1), (3, 300, 'source', 1), (3, 301, 'continuation', 1);
UPDATE task SET manual_edited_at = 5 WHERE id = 2;`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open 应完成 1→2 迁移：%v", err)
	}
	defer s.Close()

	ctx := context.Background()
	converted, ok, err := s.TaskByKindSession(ctx, 100)
	if err != nil || !ok || converted.ID != 1 {
		t.Fatalf("单会话任务应转成对话任务 ok=%v task=%+v err=%v", ok, converted, err)
	}
	for _, id := range []int64{2, 3} {
		task, ok, err := s.TaskByID(ctx, id)
		if err != nil || !ok {
			t.Fatalf("TaskByID %d ok=%v err=%v", id, ok, err)
		}
		if task.Kind != domain.TaskKindFormal || task.SessionID != 0 {
			t.Fatalf("任务 %d 应保持合并视图，得到 kind=%q session=%d", id, task.Kind, task.SessionID)
		}
	}
	// 迁移后再开一次：ALTER 不能重复执行，重复打开必须仍然成功。
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重复 Open 失败：%v", err)
	}
	if err := s2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestListTasksKindAndSort(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := s.EnsureProject(ctx, "f:/demo", "demo", "", `F:\demo`, now)
	if err != nil {
		t.Fatal(err)
	}
	// 合并任务：本体一小时前动过，没有关联会话 → 排序键就是 now-1h。
	merged, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Goal: "合并任务",
		CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	// 对话任务：本体十小时前就没动了，但那次对话半小时前还在写。
	// 按更新时间它会沉底，按对话时间它必须排最前；步骤刚被补过也不该把它抬上来。
	sessID, err := s.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: "sort-1", ProjectID: pid, Title: "半小时前的对话",
		Path: `C:\sessions\sort-1.jsonl`, UpdatedAt: now.Add(-30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Kind: domain.TaskKindSession, SessionID: sessID, Goal: "更早创建的对话",
		CreatedAt: now.Add(-10 * time.Hour), UpdatedAt: now.Add(-10 * time.Hour),
		Steps: []domain.TaskStep{{Subject: "刚补的一步", Status: domain.StepDone, CreatedAt: now, UpdatedAt: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkTaskSession(ctx, conv, sessID, "source", now.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// 扫描过的对话任务：那次对话八小时前就结束了，但每次扫描都会把本体 updated_at
	// 刷成扫描时间。它必须仍排在后面——排序不能退化成「按扫描时间排」。
	staleSessID, err := s.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: "sort-2", ProjectID: pid, Title: "八小时前的对话",
		Path: `C:\sessions\sort-2.jsonl`, UpdatedAt: now.Add(-8 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	staleConv, err := s.CreateTask(ctx, domain.Task{
		ProjectID: pid, Kind: domain.TaskKindSession, SessionID: staleSessID, Goal: "扫描刷过本体时间的对话",
		CreatedAt: now.Add(-9 * time.Hour), UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkTaskSession(ctx, staleConv, staleSessID, "source", now); err != nil {
		t.Fatal(err)
	}

	sessionTasks, err := s.ListTasks(ctx, TaskFilter{Kind: domain.TaskKindSession, Limit: 10})
	if err != nil || len(sessionTasks) != 2 {
		t.Fatalf("kind=session 应只出两条对话任务：%+v err=%v", sessionTasks, err)
	}
	formalTasks, err := s.ListTasks(ctx, TaskFilter{Kind: domain.TaskKindFormal, Limit: 10})
	if err != nil || len(formalTasks) != 1 || formalTasks[0].ID != merged {
		t.Fatalf("kind=formal 应只出合并任务：%+v err=%v", formalTasks, err)
	}
	byActivity, err := s.ListTasks(ctx, TaskFilter{Limit: 10})
	if err != nil || len(byActivity) != 3 ||
		byActivity[0].ID != conv || byActivity[1].ID != merged || byActivity[2].ID != staleConv {
		t.Fatalf("默认应按最近对话排序（半小时 > 一小时 > 八小时，本体扫描时间不算）：%+v err=%v", byActivity, err)
	}
	byCreated, err := s.ListTasks(ctx, TaskFilter{Sort: "created", Limit: 10})
	if err != nil || len(byCreated) != 3 || byCreated[0].ID != merged {
		t.Fatalf("按创建时间排序失效：%+v err=%v", byCreated, err)
	}
	byIdle, err := s.ListTasks(ctx, TaskFilter{Sort: "idle", Limit: 10})
	if err != nil || len(byIdle) != 3 || byIdle[0].ID != staleConv || byIdle[2].ID != conv {
		t.Fatalf("按最久没动排序失效：%+v err=%v", byIdle, err)
	}
}
