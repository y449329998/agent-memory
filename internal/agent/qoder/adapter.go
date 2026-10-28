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

func (a *Adapter) Name() string { return "qoder" }

// ScanSessionUnits 目前是占位实现，Task 18 会替换为真实的会话 JSONL 解析。
func (a *Adapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	return nil, nil
}

func (a *Adapter) ScanMemoryUnits(ctx context.Context) ([]agent.MemoryUnit, error) {
	var units []agent.MemoryUnit

	units = append(units, a.scanFlat(ctx, filepath.Join(a.Home, "memory"), domain.ScopeUser, "", "", "high")...)

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
		units = append(units, a.scanFlat(ctx, memoryDir, domain.ScopeProject, realPath, name, confidence)...)
	}
	return units, nil
}

func (a *Adapter) scanFlat(ctx context.Context, dir string, scope domain.Scope, projectPath, slug, confidence string) []agent.MemoryUnit {
	var out []agent.MemoryUnit
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
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
			return out
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
	return out
}

func isMarkdown(name string) bool {
	return filepath.Ext(name) == ".md"
}
