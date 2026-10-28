package qoder

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// Adapter 读 Qoder 的记忆文件。Home 是 Qoder 配置根（含 memory/、projects/）。
type Adapter struct {
	Home string
}

func New(home string) *Adapter { return &Adapter{Home: home} }

var (
	_ agent.Adapter   = (*Adapter)(nil)
	_ agent.Watchable = (*Adapter)(nil)
)

// WatchDirs 返回 Qoder 存放记忆与会话的目录。
func (a *Adapter) WatchDirs() []string {
	return []string{filepath.Join(a.Home, "memory"), filepath.Join(a.Home, "projects")}
}

func (a *Adapter) Name() string { return "qoder" }

func (a *Adapter) ScanMemoryUnits(ctx context.Context) ([]agent.MemoryUnit, error) {
	var units []agent.MemoryUnit

	userUnits, err := a.scanFlat(ctx, filepath.Join(a.Home, "memory"), domain.ScopeUser, "", "", "high")
	if err != nil {
		return units, err
	}
	units = append(units, userUnits...)

	projectsDir := filepath.Join(a.Home, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil && !os.IsNotExist(err) {
		return units, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return units, err
		}
		projectDir := filepath.Join(projectsDir, name)
		memoryDir := filepath.Join(projectDir, "memory")
		if !dirExists(memoryDir) {
			continue
		}
		// 只有带会话的项目目录才给出可信路径；这里拿不到就别硬猜
		realPath, confidence := ProjectPathFromSessions(projectDir)
		projectUnits, err := a.scanFlat(ctx, memoryDir, domain.ScopeProject, realPath, name, confidence)
		if err != nil {
			return units, err
		}
		units = append(units, projectUnits...)
	}
	return units, nil
}

// scanFlat 读一个平铺的 memory 目录。目录不存在是正常情况（该 agent 没建过记忆），
// 返回空；其余读错误必须上抛——吞成空扫描会让 Runner 以为该 agent 本轮扫过，
// 随后把上一轮导入的记忆全部误标 stale。
func (a *Adapter) scanFlat(ctx context.Context, dir string, scope domain.Scope, projectPath, slug, confidence string) ([]agent.MemoryUnit, error) {
	var out []agent.MemoryUnit
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Windows 上「同名文件代替目录」的 ReadDir 也报 path-not-found；
			// 用 Stat 区分路径真的缺失（正常空目录）与存在但不可读。
			if _, statErr := os.Stat(dir); statErr == nil {
				return nil, err
			} else if !os.IsNotExist(statErr) {
				return nil, statErr
			}
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isMarkdown(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		path := filepath.Join(dir, name)
		unit := agent.MemoryUnit{Path: path}
		content, err := os.ReadFile(path)
		if err != nil {
			unit.Err = err
			out = append(out, unit)
			continue
		}
		unit.Content = content
		unit.Size = int64(len(content))
		if st, err := os.Stat(path); err == nil {
			unit.MTime = st.ModTime()
		}

		draft, ok, err := ParseMemoryFile(path, content)
		if err != nil {
			unit.Err = err
			out = append(out, unit)
			continue
		}
		if !ok {
			continue
		}
		draft.Scope = scope
		draft.SourceAgent = a.Name()
		draft.ProjectPath = projectPath
		draft.ProjectSlug = slug
		draft.Confidence = confidence
		unit.Drafts = []domain.MemoryDraft{draft}
		out = append(out, unit)
	}
	return out, nil
}

func isMarkdown(name string) bool {
	return filepath.Ext(name) == ".md"
}
