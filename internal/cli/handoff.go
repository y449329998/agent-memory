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

// refreshHandoffs 给每个有内容的项目重写简报。
// 单个项目失败只记录，不影响其他项目——一个坏路径不该让整次扫描少交付。
func refreshHandoffs(ctx context.Context, e *env, agentFilter string) []string {
	ids, err := e.st.ProjectIDsForHandoff(ctx)
	if err != nil {
		return []string{"handoff: " + err.Error()}
	}
	cfg := compose.Config{BudgetTokens: e.cfg.Handoff.BudgetTokens, RecentCards: e.cfg.Handoff.RecentCards}
	var report []string
	for _, id := range ids {
		project, ok, err := e.st.ProjectByID(ctx, id)
		if err != nil || !ok || project.PrimaryPath == "" {
			continue
		}
		if _, err := os.Stat(project.PrimaryPath); err != nil {
			// 目录已不存在：库里留着项目记录，但不去磁盘上建目录
			continue
		}
		res, err := compose.New(e.st, cfg).Render(ctx, id)
		if err != nil {
			report = append(report, fmt.Sprintf("handoff %s: %v", project.Key, err))
			continue
		}
		path, changed, err := surface.WriteHandoff(project.PrimaryPath, res.Body)
		if err != nil {
			report = append(report, fmt.Sprintf("handoff %s: %v", project.Key, err))
			continue
		}
		if changed {
			if _, err := surface.EnsureGitExclude(project.PrimaryPath, "/"+surface.DirName+"/"); err != nil {
				report = append(report, fmt.Sprintf("handoff %s: exclude: %v", project.Key, err))
			}
		}
		if _, err := e.st.SaveHandoff(ctx, store.HandoffRow{
			ProjectID: id, Body: res.Body, ContentHash: res.ContentHash,
			BudgetTokens: cfg.BudgetTokens, GeneratedAt: time.Now(),
		}); err != nil {
			report = append(report, fmt.Sprintf("handoff %s: %v", project.Key, err))
			continue
		}
		if changed {
			report = append(report, fmt.Sprintf("handoff %s 已更新 %s tokens=%d", project.Key, path, res.Tokens))
		}
	}
	return report
}
