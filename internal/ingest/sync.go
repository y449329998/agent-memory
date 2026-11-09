package ingest

import (
	"context"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/extract"
)

// sessionParser 是适配器的可选能力：解析单个会话文件（定向同步用）。
type sessionParser interface {
	ParseSessionFile(path string) (agent.SessionUnit, error)
}

// SyncSession 定向同步一个会话文件，绕过静默窗口与哈希门控（task_sync 用）。
// 返回任务 id 与是否已静默；正在写的会话 settled=false。
func (r *Runner) SyncSession(ctx context.Context, agentName, path string) (int64, bool, error) {
	var parser sessionParser
	var adapter agent.Adapter
	for _, a := range r.adapters {
		if a.Name() == agentName {
			p, ok := a.(sessionParser)
			if !ok {
				return 0, false, fmt.Errorf("agent %s 不支持定向同步", agentName)
			}
			parser = p
			adapter = a
		}
	}
	if parser == nil {
		return 0, false, fmt.Errorf("unknown agent %q", agentName)
	}
	u, err := parser.ParseSessionFile(path)
	if err != nil {
		return 0, false, err
	}
	now := time.Now()
	projectID, err := r.resolveSessionProject(ctx, agentName, u.Session.ProjectSlug, u.Session.ProjectPath, u.Session.Confidence, now)
	if err != nil {
		return 0, false, err
	}
	if projectID == 0 {
		return 0, false, fmt.Errorf("无法确定会话 %s 的项目归属", path)
	}
	sess := u.Session
	sess.Agent = agentName
	sess.ProjectID = projectID
	sessID, err := r.store.UpsertSession(ctx, sess)
	if err != nil {
		return 0, false, err
	}
	events := agent.EventsWithSession(sessID, u.Events)
	if err := r.store.ReplaceEvents(ctx, sessID, events); err != nil {
		return 0, false, err
	}
	if err := r.writeMessages(ctx, adapter, path, sessID); err != nil {
		return 0, false, err
	}
	settled := extract.Settled(u.MTime, now, r.SettleIdle)
	var card *domain.TaskCard
	if extracted, ok := extract.TaskCard(sess, events, now); ok {
		extracted.SessionID = sessID
		extracted.ProjectID = projectID
		if _, err := r.store.UpsertTaskCard(ctx, extracted); err == nil {
			card = &extracted
		}
	}
	if err := r.syncTaskFromSession(ctx, agentName, sess, sessID, card, u.Steps, now); err != nil {
		return 0, false, err
	}
	tk, ok, err := r.store.TaskByKindSession(ctx, sessID)
	if err != nil || !ok {
		return 0, settled, fmt.Errorf("同步后未找到对话任务")
	}
	return tk.ID, settled, nil
}
