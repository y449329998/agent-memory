package trae

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// Adapter 读 Trae 的记忆文件。Home 是 Trae 配置根（含 memory/、user_rules/）。
// WorkspaceRoots 是 workspaceStorage 根；为空时自动取 Trae CN、Trae 两个目录。
type Adapter struct {
	Home           string
	WorkspaceRoots []string
}

var _ agent.Adapter = (*Adapter)(nil)

func New(home string) *Adapter { return &Adapter{Home: home} }

func (a *Adapter) Name() string { return "trae" }

// ScanSessionUnits: Trae 的会话存在 state.vscdb 里（结构未公开），
// 本期适配器只导入记忆文件。
func (a *Adapter) ScanSessionUnits(ctx context.Context) ([]agent.SessionUnit, error) {
	return nil, nil
}

// projectMeta 是一批记忆共用的项目归属。
type projectMeta struct {
	slug       string
	path       string
	confidence string
}

// ScanMemoryUnits 按固定顺序扫描：用户级记忆 → 项目级记忆 → 用户规则。
// 项目目录名先去 --p2-<hash> 窗口后缀，再用 workspaceStorage 的
// slug → 路径索引判定置信度；索引不命中时只给 slug，交给 ingest 建独立项目。
func (a *Adapter) ScanMemoryUnits(ctx context.Context) ([]agent.MemoryUnit, error) {
	var units []agent.MemoryUnit

	userUnits, err := a.scanDir(ctx, filepath.Join(a.Home, "memory"), domain.ScopeUser,
		projectMeta{confidence: "high"}, ParseMemoryFile)
	if err != nil {
		return units, err
	}
	units = append(units, userUnits...)

	projectsDir := filepath.Join(a.Home, "memory", "projects")
	entries, err := readDir(projectsDir)
	if err != nil {
		return units, err
	}
	index := workspaceIndex(a.workspaceRoots())
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return units, err
		}
		if !e.IsDir() {
			continue
		}
		path, confidence := projectLocation(e.Name(), index)
		meta := projectMeta{slug: slugKey(e.Name()), path: path, confidence: confidence}
		projectUnits, err := a.scanDir(ctx, filepath.Join(projectsDir, e.Name()),
			domain.ScopeProject, meta, ParseMemoryFile)
		if err != nil {
			return units, err
		}
		units = append(units, projectUnits...)
	}

	ruleUnits, err := a.scanDir(ctx, filepath.Join(a.Home, "user_rules"), domain.ScopeUser,
		projectMeta{confidence: "high"}, ParseRuleFile)
	if err != nil {
		return units, err
	}
	units = append(units, ruleUnits...)
	return units, nil
}

func (a *Adapter) workspaceRoots() []string {
	if len(a.WorkspaceRoots) > 0 {
		return a.WorkspaceRoots
	}
	return defaultWorkspaceRoots()
}

// scanDir 读一个平铺目录下的 .md 文件。目录不存在表示该 agent 没写过这类记忆，
// 返回空；其余读错误必须上抛——吞成空扫描会让 Runner 以为该 agent 本轮扫过，
// 随后把上一轮导入的记忆全部误标 stale。
func (a *Adapter) scanDir(ctx context.Context, dir string, scope domain.Scope, meta projectMeta,
	parse func(string, []byte) ([]domain.MemoryDraft, error)) ([]agent.MemoryUnit, error) {
	entries, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	var out []agent.MemoryUnit
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if filepath.Ext(e.Name()) != ".md" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		unit := agent.MemoryUnit{Path: path}
		content, err := os.ReadFile(path)
		if err != nil {
			// 以 .md 结尾的目录也会走到这里：宁可登记一条读失败，
			// 也不静默吞掉异常布局。
			unit.Err = err
			out = append(out, unit)
			continue
		}
		unit.Content = content
		unit.Size = int64(len(content))
		if st, err := os.Stat(path); err == nil {
			unit.MTime = st.ModTime()
		}
		drafts, err := parse(path, content)
		if err != nil {
			unit.Err = err
			out = append(out, unit)
			continue
		}
		if len(drafts) == 0 {
			continue
		}
		for i := range drafts {
			drafts[i].Scope = scope
			drafts[i].SourceAgent = a.Name()
			drafts[i].ProjectPath = meta.path
			drafts[i].ProjectSlug = meta.slug
			drafts[i].Confidence = meta.confidence
		}
		unit.Drafts = drafts
		out = append(out, unit)
	}
	return out, nil
}

// readDir 读目录并保持文件名有序。目录真的不存在时返回空；存在但读不了
// （含 Windows 上用同名文件代替目录）时返回错误，由调用方上抛。
func readDir(dir string) ([]os.DirEntry, error) {
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
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}
