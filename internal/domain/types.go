package domain

import (
	"context"
	"time"
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

type Stats struct {
	Projects        int
	Memories        int
	Stale           int
	Sources         int
	FailedSource    int
	Changed         int
	MemoriesByAgent map[string]int
	MemoriesByType  map[string]int
}
