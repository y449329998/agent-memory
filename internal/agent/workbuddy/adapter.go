package workbuddy

import (
	"context"
	"os"
	"path/filepath"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// homeDirName 是 WorkBuddy 的用户级配置目录名。
const homeDirName = ".workbuddy"

// Adapter 读 WorkBuddy 的用户级记忆。Home 是 WorkBuddy 配置根（含 MEMORY.md）。
// USER.md / SOUL.md / IDENTITY.md 是 persona 模板，不属于可导入记忆。
type Adapter struct {
	Home string
}

var (
	_ agent.Adapter   = (*Adapter)(nil)
	_ agent.Watchable = (*Adapter)(nil)
)

func New(home string) *Adapter { return &Adapter{Home: home} }

func (a *Adapter) Name() string { return "workbuddy" }

// WatchDirs 返回 nil：~/.workbuddy 根目录有大量应用缓存，递归监听会放大事件量，
// 这个 agent 只靠 30 秒轮询兜底。
func (a *Adapter) WatchDirs() []string { return nil }

// ScanSessionUnits: WorkBuddy 的对话在远端/IndexedDB，本地只有会话索引，
// 本期不导入会话。
func (a *Adapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	return nil, nil
}

// ScanMemoryUnits 只读 <Home>/MEMORY.md。文件不存在或没有可导入条目时返回空；
// 读失败返回一条带 Err 的 unit，交给 ingest 登记 last_error。
func (a *Adapter) ScanMemoryUnits(ctx context.Context) ([]agent.MemoryUnit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Home == "" {
		return nil, nil
	}
	path := filepath.Join(a.Home, "MEMORY.md")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return []agent.MemoryUnit{{Path: path, Err: err}}, nil
	}
	drafts, err := ParseMemoryFile(path, content)
	if err != nil {
		return []agent.MemoryUnit{{Path: path, Content: content, Size: int64(len(content)), Err: err}}, nil
	}
	if len(drafts) == 0 {
		return nil, nil
	}
	for i := range drafts {
		drafts[i].Scope = domain.ScopeUser
		drafts[i].SourceAgent = a.Name()
		drafts[i].ProjectPath = ""
		drafts[i].ProjectSlug = ""
		drafts[i].Confidence = "high"
	}
	unit := agent.MemoryUnit{
		Path:    path,
		Content: content,
		Size:    int64(len(content)),
		Drafts:  drafts,
	}
	if st, err := os.Stat(path); err == nil {
		unit.MTime = st.ModTime()
	}
	return []agent.MemoryUnit{unit}, nil
}

// DiscoverHome 在 userHome 下找 WorkBuddy 配置根。
// 判据用 MEMORY.md 或 app/ 的存在性，避免命中同名的空壳目录。
func DiscoverHome(userHome string) (string, bool) {
	if userHome == "" {
		return "", false
	}
	home := filepath.Join(userHome, homeDirName)
	if !dirExists(home) {
		return "", false
	}
	if fileExists(filepath.Join(home, "MEMORY.md")) || dirExists(filepath.Join(home, "app")) {
		return home, true
	}
	return "", false
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
