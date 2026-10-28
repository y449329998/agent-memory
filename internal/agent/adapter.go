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

// Adapter 把一个 agent 的原生记忆文件变成结构化草稿。只读，不写库，不认识其他 agent。
type Adapter interface {
	Name() string
	ScanMemoryUnits(ctx context.Context) ([]MemoryUnit, error)
}
