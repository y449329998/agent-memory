package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/mcp"
)

func init() {
	register(command{name: "mcp", desc: "以 stdio 方式接入常驻服务（给 agent 的 MCP 配置用）", run: cmdMCP})
}

func cmdMCP(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "常驻服务端口，0 表示从 port 文件读")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// 只读配置：桥不碰共享库，避免与 serve 争 SQLite，也避免在缺 port 时建出空库。
	e, err := loadConfigEnv()
	if err != nil {
		return reportError(stderr, err)
	}

	usePort := *port
	if usePort == 0 {
		usePort, err = daemon.ReadPort(e.cfg.PortPath())
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	// token 优先取环境变量：agent 的 MCP 配置里用 env 传，不进命令行与进程列表
	token := os.Getenv("AGENT_MEMORY_TOKEN")
	if token == "" {
		token, err = daemon.LoadOrCreateToken(e.cfg.TokenPath())
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
	}

	ctx := context.Background()
	bridge := mcp.NewBridge(usePort, token)
	fmt.Fprintf(stderr, "agent-memory mcp -> 127.0.0.1:%d\n", usePort)
	// 不额外观察 stdin 是否关闭：ServeStdio 里的 scanner.Scan() 在 agent 关闭管道时
	// 自然返回 false，函数随之退出。另起 goroutine 去 Read stdin 会抢走协议字节。
	if err := bridge.ServeStdio(ctx, os.Stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "mcp bridge: %v\n", err)
		return 1
	}
	return 0
}
