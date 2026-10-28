package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// handoffRefresh 是单个项目的简报刷新结果，scan 与 serve 共用。
type handoffRefresh struct {
	Key     string
	Path    string
	Tokens  int
	Changed bool
	Err     error
}

// refreshHandoffFiles 是简报落盘的唯一实现：scan、serve 与 handoff 都走这里，
// 避免两套逻辑各自漂移。
//
// 每个项目都会写库（SaveHandoff 本身幂等），这样 budget/generated_at 跟着本次扫描走；
// 只有正文变化（changed）才维护 git exclude 并报告「已更新」。
// 单个项目失败只记录，不影响其他项目——一个坏路径不该让整次扫描少交付。
func refreshHandoffFiles(ctx context.Context, st *store.Store, cfg config.Config) []handoffRefresh {
	ids, err := st.ProjectIDsForHandoff(ctx)
	if err != nil {
		return []handoffRefresh{{Err: fmt.Errorf("handoff: %w", err)}}
	}
	composeCfg := compose.Config{BudgetTokens: cfg.Handoff.BudgetTokens, RecentCards: cfg.Handoff.RecentCards}
	var out []handoffRefresh
	for _, id := range ids {
		project, ok, err := st.ProjectByID(ctx, id)
		if err != nil || !ok || project.PrimaryPath == "" {
			continue
		}
		if _, err := os.Stat(project.PrimaryPath); err != nil {
			// 目录已不存在：库里留着项目记录，但不去磁盘上建目录
			continue
		}
		r := handoffRefresh{Key: project.Key}
		res, err := compose.New(st, composeCfg).Render(ctx, id)
		if err != nil {
			r.Err = fmt.Errorf("handoff %s: %w", project.Key, err)
			out = append(out, r)
			continue
		}
		r.Tokens = res.Tokens
		path, changed, err := surface.WriteHandoff(project.PrimaryPath, res.Body)
		r.Path, r.Changed = path, changed
		if err != nil {
			r.Err = fmt.Errorf("handoff %s: %w", project.Key, err)
			out = append(out, r)
			continue
		}
		if changed {
			if _, err := surface.EnsureGitExclude(project.PrimaryPath, "/"+surface.DirName+"/"); err != nil {
				r.Err = fmt.Errorf("handoff %s: exclude: %w", project.Key, err)
			}
		}
		if _, err := st.SaveHandoff(ctx, store.HandoffRow{
			ProjectID: id, Body: res.Body, ContentHash: res.ContentHash,
			BudgetTokens: composeCfg.BudgetTokens, GeneratedAt: time.Now(),
		}); err != nil && r.Err == nil {
			r.Err = fmt.Errorf("handoff %s: %w", project.Key, err)
		}
		out = append(out, r)
	}
	return out
}

// refreshHandoffs 给每个有内容的项目重写简报，返回给 scan 打印的行。
// 内容没变时没有输出，保持 scan 报告简洁。
func refreshHandoffs(ctx context.Context, e *env, _ string) []string {
	var report []string
	for _, r := range refreshHandoffFiles(ctx, e.st, e.cfg) {
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
