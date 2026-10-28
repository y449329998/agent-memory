package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
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

	fmt.Fprintf(stdout, "\n写入目标（本程序只会改这几处）:\n")
	fmt.Fprintf(stdout, "  %s\n", e.cfg.DataDir)
	fmt.Fprintf(stdout, "  %s\n", e.cfg.DBPath())
	fmt.Fprintf(stdout, "  项目内 .agent-memory/ 与 link 装过的区块（M2/M3 起）\n")

	fmt.Fprintf(stdout, "\nagent 发现结果:\n")
	if home := e.cfg.QoderHome(); home != "" {
		fmt.Fprintf(stdout, "  qoder: %s\n", home)
	} else {
		fmt.Fprintln(stdout, "  qoder: 未发现（可在 config.toml 指定 [qoder] home）")
	}

	d, err := time.ParseDuration(*changedSince)
	if err != nil {
		fmt.Fprintf(stderr, "error: bad --since %q\n", *changedSince)
		return 2
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

	// 低置信项目用 "agent:slug" 当 key；排除含路径分隔符的项，
	// 否则 Windows 的 "c:/..." 路径 key 会被全部误报为未合并。
	unmerged, err := e.st.QueryStrings(ctx,
		`SELECT key FROM project WHERE key LIKE '%:%' AND key NOT LIKE '%/%' ORDER BY key LIMIT 20`)
	if err != nil {
		return reportError(stderr, err)
	}
	if len(unmerged) > 0 {
		fmt.Fprintf(stdout, "\n未合并项目（路径只能靠 slug 猜，保持独立）:\n")
		for _, k := range unmerged {
			fmt.Fprintf(stdout, "  %s\n", k)
		}
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
