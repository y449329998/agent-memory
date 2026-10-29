package domain

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrMemoryNotFound 表示按 id 取记忆时目标不存在（含已被清理）。
	ErrMemoryNotFound = errors.New("memory not found")
	// ErrEmptyMemory 拒绝把空白内容写成记忆条目。
	ErrEmptyMemory = errors.New("memory text is empty")
	// ErrInvalidMemoryType 拒绝不在枚举内的记忆类型。
	ErrInvalidMemoryType = errors.New("invalid memory type")
	// ErrTaskCardNotFound 表示按 id 取任务卡时目标不存在。
	ErrTaskCardNotFound = errors.New("task card not found")
)

type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

type MemoryType string

const (
	TypePreference MemoryType = "preference"
	TypeConstraint MemoryType = "constraint"
	TypeConvention MemoryType = "convention"
	TypeLesson     MemoryType = "lesson"
	TypeFact       MemoryType = "fact"
	TypeGoal       MemoryType = "goal"
)

func ValidMemoryType(t MemoryType) bool {
	switch t {
	case TypePreference, TypeConstraint, TypeConvention, TypeLesson, TypeFact, TypeGoal:
		return true
	}
	return false
}

// MemoryDraft 是适配器产出的记忆条目，尚未落库。
type MemoryDraft struct {
	Scope       Scope
	Type        MemoryType
	Text        string
	SourceAgent string
	SourcePath  string
	Quote       string
	ProjectPath string
	ProjectSlug string
	Confidence  string
	SeenAt      time.Time
}

type MemorySource struct {
	MemoryID int64
	Agent    string
	Path     string
	Quote    string
}

type MemoryItem struct {
	ID           int64
	Scope        Scope
	ProjectID    int64
	Type         MemoryType
	Text         string
	TextNormHash string
	Stale        bool
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	EmbedModel   string
	EmbedDim     int
	EmbeddedAt   time.Time
	Sources      []MemorySource
}

type Project struct {
	ID          int64
	Key         string
	DisplayName string
	GitRemote   string
	PrimaryPath string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type SearchQuery struct {
	Text         string
	ProjectID    int64
	Scope        Scope
	Type         MemoryType
	IncludeStale bool
	Limit        int
}

type Hit struct {
	Memory MemoryItem
	Score  float64
	Origin string
}

// SearchProvider 是检索抽象。本期只有 ftsProvider 一个实现；
// M5 的向量与 RRF 融合通过新增实现接入，上层不变。
type SearchProvider interface {
	Search(ctx context.Context, q SearchQuery) ([]Hit, error)
}

type SourceState struct {
	Agent       string
	Kind        string
	Path        string
	ContentHash string
	PrevHash    string
	ChangedAt   time.Time
	LastError   string
	LastSeenRun int64
	MTime       time.Time
	Size        int64
}

// Stats 的 json tag 同时服务 `stats --json` 与 /v1/stats：
// 小写键是 CLI 输出契约，json.Unmarshal 也能按 tag 回填到本结构。
type Stats struct {
	Projects        int            `json:"projects"`
	Memories        int            `json:"memories"`
	Stale           int            `json:"stale"`
	Sources         int            `json:"sources"`
	FailedSource    int            `json:"failed_source"`
	Changed         int            `json:"changed"`
	MemoriesByAgent map[string]int `json:"memories_by_agent"`
	MemoriesByType  map[string]int `json:"memories_by_type"`
}

// Session 是一个已入库的会话。ProjectPath/ProjectSlug/Confidence 由适配器给出，
// ProjectID 由 ingest 解析后回填。
type Session struct {
	ID          int64
	Agent       string
	ExternalID  string
	ProjectID   int64
	Title       string
	StartedAt   time.Time
	UpdatedAt   time.Time
	EventCount  int
	Path        string
	ProjectPath string
	ProjectSlug string
	Confidence  string
}

type EventKind string

const (
	KindUserText    EventKind = "user_text"
	KindAssistant   EventKind = "assistant_text"
	KindToolEdit    EventKind = "tool_edit"
	KindToolCommand EventKind = "tool_command"
	KindToolRead    EventKind = "tool_read"
	KindToolError   EventKind = "tool_error"
	KindWorkspace   EventKind = "workspace"
	KindOther       EventKind = "other"
)

// SessionEvent 是会话里按 Seq 排序的一条事件。
type SessionEvent struct {
	ID        int64
	SessionID int64
	Seq       int
	Role      string
	Kind      EventKind
	Text      string
	Tool      string
	Files     []string
	Ts        time.Time
}

// TaskCard 是从会话里提炼出的任务卡，供后续会话接力。
type TaskCard struct {
	ID            int64
	SessionID     int64
	ProjectID     int64
	Goal          string
	Status        string
	Decisions     []string
	Files         []string
	Commands      []string
	Errors        []string
	OpenQuestions string
	NextSteps     string
	Confidence    string
	GeneratedBy   string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// 任务卡状态。
const (
	StatusDoing   = "doing"
	StatusDone    = "done"
	StatusBlocked = "blocked"
	StatusUnknown = "unknown"
)
