package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

func init() {
	register(command{name: "doctor", desc: "库状态、agent 发现结果、写入目标与失败源清单", run: cmdDoctor})
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	changedSince := fs.String("since", "720h", "列出多久内变更过的源文件，如 24h / 720h")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// 先校验 flag 再打开库：用法错误不能先输出半份报告。
	d, err := time.ParseDuration(*changedSince)
	if err != nil {
		fmt.Fprintf(stderr, "error: bad --since %q\n", *changedSince)
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	stats, err := e.st.Stats(ctx)
	if err != nil {
		return reportError(stderr, err)
	}

	fmt.Fprintf(stdout, "共享记忆库: %s\n", e.st.Path())
	fmt.Fprintf(stdout, "项目 %d，记忆 %d（过时 %d），源 %d（失败 %d，有变更记录 %d）\n",
		stats.Projects, stats.Memories, stats.Stale, stats.Sources, stats.FailedSource, stats.Changed)
	fmt.Fprintf(stdout, "按 agent 的记忆条数: %v\n", stats.MemoriesByAgent)
	fmt.Fprintf(stdout, "按类型的条数: %v\n", stats.MemoriesByType)

	sessions, err := e.st.QueryInt(ctx, `SELECT count(*) FROM session`)
	if err != nil {
		return reportError(stderr, err)
	}
	cards, err := e.st.QueryInt(ctx, `SELECT count(*) FROM task_card`)
	if err != nil {
		return reportError(stderr, err)
	}
	handoffs, err := e.st.QueryInt(ctx, `SELECT count(*) FROM handoff`)
	if err != nil {
		return reportError(stderr, err)
	}
	byStatus, err := e.st.QueryStrings(ctx, `SELECT status || '=' || COUNT(*) FROM task_card GROUP BY status ORDER BY status`)
	if err != nil {
		return reportError(stderr, err)
	}
	fmt.Fprintf(stdout, "会话 %d，任务卡 %d，简报 %d（状态: %v）\n", sessions, cards, handoffs, byStatus)

	fmt.Fprintf(stdout, "\n写入目标（本程序只会改这几处）:\n")
	fmt.Fprintf(stdout, "  %s\n", e.cfg.DataDir)
	fmt.Fprintf(stdout, "  %s\n", e.cfg.DBPath())
	fmt.Fprintf(stdout, "  项目内 .agent-memory/ 与 link 装过的区块（M2/M3 起）\n")
	links, err := e.st.ListAgentLinks(ctx)
	if err != nil {
		return reportError(stderr, err)
	}
	for _, l := range links {
		fmt.Fprintf(stdout, "  %s 区块 -> %s (登记于 %s)\n", l.Agent, l.BlockFile, l.InstalledAt.Format(time.RFC3339))
	}
	if len(links) == 0 {
		fmt.Fprintln(stdout, "  （尚无 link 登记）")
	}

	fmt.Fprintf(stdout, "\nagent 发现结果:\n")
	if home := e.cfg.QoderHome(); home != "" {
		fmt.Fprintf(stdout, "  qoder: %s\n", home)
	} else {
		fmt.Fprintln(stdout, "  qoder: 未发现（可在 config.toml 指定 [qoder] home）")
	}
	if home := e.cfg.TraeHome(); home != "" {
		fmt.Fprintf(stdout, "  trae: %s（workspace 根 %d 个）\n", home, len(e.cfg.TraeWorkspaceRoots()))
	} else {
		fmt.Fprintln(stdout, "  trae: 未发现（可在 config.toml 指定 [trae] home）")
	}
	if home := e.cfg.WorkBuddyHome(); home != "" {
		fmt.Fprintf(stdout, "  workbuddy: %s\n", home)
	} else {
		fmt.Fprintln(stdout, "  workbuddy: 未发现（可在 config.toml 指定 [workbuddy] home）")
	}

	changed, err := e.st.ChangedSources(ctx, time.Now().Add(-d).Unix())
	if err != nil {
		return reportError(stderr, err)
	}
	if len(changed) > 0 {
		fmt.Fprintf(stdout, "\n最近变更的源文件（归属需与 agent 自己的编辑记录比对）:\n")
		for _, c := range changed {
			fmt.Fprintf(stdout, "  %s %s/%s @ %s\n", c.Kind, c.Agent, c.Path, c.ChangedAt.Format(time.RFC3339))
		}
	}

	errs, err := e.st.SourceErrors(ctx)
	if err != nil {
		return reportError(stderr, err)
	}
	if len(errs) > 0 {
		fmt.Fprintf(stdout, "\n解析失败的源:\n")
		for _, c := range errs {
			fmt.Fprintf(stdout, "  %s/%s: %s\n", c.Agent, c.Path, c.ChangedAt.Format(time.RFC3339))
		}
	}

	// 低置信项目用 "agent:slug" 当 key；排除含路径分隔符的项与盘符根 key（f:），
	// 否则 Windows 的 "c:/..." 路径 key 和 "f:" 会被误报为未合并。
	unmerged, err := e.st.QueryStrings(ctx,
		`SELECT key FROM project WHERE key LIKE '%:%' AND key NOT LIKE '%/%' AND key NOT LIKE '_:'
ORDER BY key LIMIT 20`)
	if err != nil {
		return reportError(stderr, err)
	}
	if len(unmerged) > 0 {
		fmt.Fprintf(stdout, "\n未合并项目（路径只能靠 slug 猜，保持独立）:\n")
		for _, k := range unmerged {
			fmt.Fprintf(stdout, "  %s\n", k)
		}
	}

	// 同一物理路径被登记到多个项目：典型成因是外部写入（MCP / CLI add）绕过路径映射
	// 直接用空 remote 新建项目。这里只报出来，不自动合并——哪个项目才是真的需要人判断，
	// 合并错了比留两份更难恢复。
	dupPaths, err := e.st.QueryStrings(ctx, `
SELECT path FROM project_path
GROUP BY path HAVING COUNT(DISTINCT project_id) > 1
ORDER BY path LIMIT 20`)
	if err != nil {
		return reportError(stderr, err)
	}
	if len(dupPaths) > 0 {
		fmt.Fprintf(stdout, "\n警告: 同一路径映射到多个项目（历史脏数据，未自动合并）:\n")
		for _, p := range dupPaths {
			// QueryStrings 会读尽并关闭 rows，单连接下随之逐条查询是安全的。
			keys, err := e.st.QueryStrings(ctx, `
SELECT p.key FROM project_path pp JOIN project p ON p.id = pp.project_id
WHERE pp.path = ? ORDER BY p.id`, p)
			if err != nil {
				return reportError(stderr, err)
			}
			fmt.Fprintf(stdout, "  %s -> %s\n", p, strings.Join(keys, ", "))
		}
		fmt.Fprintln(stdout, "  确认真实项目后手工合并（把该路径改指向它，或删除多余项目的映射）")
	}

	needFull, err := e.st.QueryInt(ctx, `SELECT count(*) FROM source WHERE kind = 'memory' AND last_error <> ''`)
	if err != nil {
		return reportError(stderr, err)
	}
	if needFull > 0 {
		fmt.Fprintf(stdout, "\n提示: %d 个源上次解析失败，修好后跑 agent-memory scan --full\n", needFull)
	}
	return 0
}
