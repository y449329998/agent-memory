package agent

import (
	"context"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// MemoryUnit 是一个源文件及其解析结果。适配器负责读文件并给出 Content，
// 由 ingest 统一算哈希做幂等门控——这样「哪些字节代表这个源」只有一处定义。
//
// Err 非空表示这个源读/解析失败：ingest 会登记 last_error 并跳过其中的 Drafts，
// 一个坏文件不影响同目录其他文件。
type MemoryUnit struct {
	Path    string
	Content []byte
	MTime   time.Time
	Size    int64
	Drafts  []domain.MemoryDraft
	Err     error
}

// SessionUnit 是一个会话源文件及其解析结果。
type SessionUnit struct {
	Path    string
	Content []byte
	MTime   time.Time
	Size    int64
	// Session 的 ProjectPath/ProjectSlug/Confidence 由适配器填；ProjectID 由 ingest 解析。
	Session domain.Session
	Events  []SessionEvent
	Err     error
}

// SessionEvent 是适配器产出的会话事件，与 domain.SessionEvent 同构但不带 SessionID：
// 库里的行主键由 store 决定，适配器不该关心。
type SessionEvent struct {
	Seq   int
	Role  string
	Kind  domain.EventKind
	Text  string
	Tool  string
	Files []string
	Ts    time.Time
}

// Adapter 把一个 agent 的原生记忆文件变成结构化草稿。只读，不写库，不认识其他 agent。
type Adapter interface {
	Name() string
	ScanMemoryUnits(ctx context.Context) ([]MemoryUnit, error)
	ScanSessionUnits(ctx context.Context) ([]SessionUnit, error)
}

// Watchable 是适配器的可选接口：返回希望文件监听覆盖的目录。
// 不实现它（或返回 nil）表示该 agent 只靠轮询兜底，避免把大量应用缓存
// 变成扫描事件。路径应尽量窄，不要递归到无关子树。
type Watchable interface {
	WatchDirs() []string
}

// EventsWithSession 给适配器产出的事件补上库主键。
// 转换只在这里发生：适配器不知道 id，store 不知道适配器结构。
func EventsWithSession(sessionID int64, in []SessionEvent) []domain.SessionEvent {
	out := make([]domain.SessionEvent, 0, len(in))
	for _, e := range in {
		out = append(out, domain.SessionEvent{
			SessionID: sessionID,
			Seq:       e.Seq,
			Role:      e.Role,
			Kind:      e.Kind,
			Text:      e.Text,
			Tool:      e.Tool,
			Files:     e.Files,
			Ts:        e.Ts,
		})
	}
	return out
}
