package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/muyi99/agent-memory/internal/ingest"
)

func init() {
	register(command{name: "scan", desc: "扫描 agent 记忆文件并导入共享库", run: cmdScan})
}

func cmdScan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentFilter := fs.String("agent", "", "只扫描指定 agent")
	full := fs.Bool("full", false, "忽略内容哈希，全部重新解析")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	adapters := buildAdapters(e.cfg)
	if len(adapters) == 0 {
		fmt.Fprintln(stderr, "error: 没有可用的 agent，检查 config.toml")
		return 1
	}
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
	}
	if err := ingest.ValidateAgent(names, *agentFilter); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	ctx := context.Background()
	if *full {
		n, err := e.st.ResetSources(ctx, *agentFilter)
		if err != nil {
			return reportError(stderr, err)
		}
		fmt.Fprintf(stdout, "已重置 %d 个扫描游标\n", n)
	}

	r := ingest.New(e.st, adapters...)
	res, runErr := r.Run(ctx, *agentFilter)
	fmt.Fprintf(stdout, "scan: %s\n", res.String())
	for _, msg := range res.Errors {
		fmt.Fprintf(stdout, "  ! %s\n", msg)
	}
	if runErr != nil {
		return reportError(stderr, runErr)
	}
	return 0
}
