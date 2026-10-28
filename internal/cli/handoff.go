package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/compose"
	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/store"
	"github.com/muyi99/agent-memory/internal/surface"
)

func init() {
	register(command{name: "handoff", desc: "生成/打印项目交接简报", run: cmdHandoff})
}

func cmdHandoff(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("handoff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectRef := fs.String("project", "", "项目绝对路径，默认当前工作目录")
	printOnly := fs.Bool("print", false, "只打印到标准输出，不写项目文件")
	budget := fs.Int("budget", 0, "简报 token 预算，0 表示用配置里的值")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	target := *projectRef
	if target == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return reportError(stderr, err)
		}
		target = cwd
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return reportError(stderr, err)
	}
	// 显式 --project 也可能指向 agent 自己的配置树；那里不是项目目录，
	// 写 .agent-memory 会污染 agent 原生配置。跳过不是失败，退出码保持 0。
	if surface.IsProtectedAgentDir(abs) {
		fmt.Fprintf(stderr, "warn: 拒绝写入 agent 原生目录 %s\n", abs)
		return 0
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	projectID, ok, err := e.st.ResolveProjectByPath(ctx, abs)
	if err != nil {
		return reportError(stderr, err)
	}
	if !ok {
		fmt.Fprintf(stderr, "error: 不认识这个项目: %s\n先跑 agent-memory scan，或确认该目录里有 agent 记录\n", abs)
		return 1
	}

	cfg := compose.Config{BudgetTokens: e.cfg.Handoff.BudgetTokens, RecentCards: e.cfg.Handoff.RecentCards}
	if *budget > 0 {
		cfg.BudgetTokens = *budget
	}
	res, err := compose.New(e.st, cfg).Render(ctx, projectID)
	if err != nil {
		return reportError(stderr, err)
	}

	if *printOnly {
		fmt.Fprint(stdout, res.Body)
		return 0
	}

	path, changed, err := surface.WriteHandoff(abs, res.Body)
	if err != nil {
		return reportError(stderr, err)
	}
	if changed {
		if _, err := surface.EnsureGitExclude(abs, "/"+surface.DirName+"/"); err != nil {
			return reportError(stderr, err)
		}
	}
	if _, err := e.st.SaveHandoff(ctx, store.HandoffRow{
		ProjectID: projectID, Body: res.Body, ContentHash: res.ContentHash,
		BudgetTokens: cfg.BudgetTokens, GeneratedAt: time.Now(),
	}); err != nil {
		return reportError(stderr, err)
	}

	state := "已更新"
	if !changed {
		state = "未变化"
	}
	fmt.Fprintf(stdout, "%s %s tokens=%d budget=%d truncated=%v\n", state, path, res.Tokens, cfg.BudgetTokens, res.Truncated)
	return 0
}

// handoffRefresh 是单个项目（或单个写入目标）的简报刷新结果，scan 与 serve 共用。
type handoffRefresh struct {
	Key     string
	Path    string
	Tokens  int
	Changed bool
	Warn    string
	Err     error
}

// handoffDirs 取项目所有已登记的高置信度 checkout，还原成本地路径并过滤掉已不存在的目录。
//
// project_path 里存的是归一化 key（小写、斜杠分隔），写盘前只能用 filepath.FromSlash
// 还原；旧库没有路径映射时退回 primary_path，保证升级后仍能刷新第一份 checkout。
func handoffDirs(ctx context.Context, st *store.Store, projectID int64, primaryPath string) ([]string, error) {
	keys, err := st.ListProjectDirs(ctx, projectID)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	add := func(dir string) {
		dir = filepath.Clean(dir)
		key := strings.ToLower(dir)
		if seen[key] {
			return
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return
		}
		seen[key] = true
		out = append(out, dir)
	}
	for _, key := range keys {
		add(filepath.FromSlash(key))
	}
	if len(out) == 0 && primaryPath != "" {
		add(primaryPath)
	}
	return out, nil
}

// refreshHandoffFiles 是简报落盘的唯一实现：scan 与 serve 都走这里，避免两套逻辑各自漂移。
//
// 同一 remote 的每份 checkout 都要拿到简报：只写 primary_path 会让第二个 checkout 的
// AGENTS.md 指向一个永远不存在的文件。每个目标独立处理，一个失败不影响其他目标；
// 项目只要至少有一个可写目标就写库（SaveHandoff 本身幂等），budget/generated_at
// 跟着本次扫描走。只有正文变化（changed）才维护 git exclude 并报告「已更新」。
func refreshHandoffFiles(ctx context.Context, st *store.Store, cfg config.Config) []handoffRefresh {
	ids, err := st.ProjectIDsForHandoff(ctx)
	if err != nil {
		return []handoffRefresh{{Err: fmt.Errorf("handoff: %w", err)}}
	}
	composeCfg := compose.Config{BudgetTokens: cfg.Handoff.BudgetTokens, RecentCards: cfg.Handoff.RecentCards}
	var out []handoffRefresh
	for _, id := range ids {
		project, ok, err := st.ProjectByID(ctx, id)
		if err != nil || !ok {
			continue
		}
		dirs, err := handoffDirs(ctx, st, id, project.PrimaryPath)
		if err != nil {
			out = append(out, handoffRefresh{Key: project.Key, Err: fmt.Errorf("handoff %s: %w", project.Key, err)})
			continue
		}
		if len(dirs) == 0 {
			// 目录都已不存在：库里留着项目记录，但不去磁盘上建目录
			continue
		}
		res, err := compose.New(st, composeCfg).Render(ctx, id)
		if err != nil {
			out = append(out, handoffRefresh{Key: project.Key, Err: fmt.Errorf("handoff %s: %w", project.Key, err)})
			continue
		}
		wrote := false
		for _, dir := range dirs {
			if surface.IsProtectedAgentDir(dir) {
				out = append(out, handoffRefresh{Key: project.Key, Warn: fmt.Sprintf("warn: 拒绝写入 agent 原生目录 %s", dir)})
				continue
			}
			path, changed, err := surface.WriteHandoff(dir, res.Body)
			if err != nil {
				out = append(out, handoffRefresh{Key: project.Key, Path: path, Tokens: res.Tokens,
					Err: fmt.Errorf("handoff %s: %w", project.Key, err)})
				continue
			}
			wrote = true
			if changed {
				if _, err := surface.EnsureGitExclude(dir, "/"+surface.DirName+"/"); err != nil {
					out = append(out, handoffRefresh{Key: project.Key, Path: path, Tokens: res.Tokens,
						Err: fmt.Errorf("handoff %s: exclude: %w", project.Key, err)})
					continue
				}
			}
			out = append(out, handoffRefresh{Key: project.Key, Path: path, Tokens: res.Tokens, Changed: changed})
		}
		if wrote {
			if _, err := st.SaveHandoff(ctx, store.HandoffRow{
				ProjectID: id, Body: res.Body, ContentHash: res.ContentHash,
				BudgetTokens: composeCfg.BudgetTokens, GeneratedAt: time.Now(),
			}); err != nil {
				out = append(out, handoffRefresh{Key: project.Key, Err: fmt.Errorf("handoff %s: %w", project.Key, err)})
			}
		}
	}
	return out
}

// refreshHandoffs 给每个有内容的项目重写简报，返回给 scan 打印的行。
// 内容没变时没有输出，保持 scan 报告简洁。
func refreshHandoffs(ctx context.Context, e *env, _ string) []string {
	var report []string
	for _, r := range refreshHandoffFiles(ctx, e.st, e.cfg) {
		if r.Warn != "" {
			report = append(report, r.Warn)
			continue
		}
		if r.Err != nil {
			report = append(report, r.Err.Error())
			continue
		}
		if r.Changed {
			report = append(report, fmt.Sprintf("handoff %s 已更新 %s tokens=%d", r.Key, r.Path, r.Tokens))
		}
	}
	return report
}
