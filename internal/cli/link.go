package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/normalize"
	"github.com/muyi99/agent-memory/internal/store"
	"github.com/muyi99/agent-memory/internal/surface"
)

func init() {
	register(command{name: "link", desc: "把 agent-memory 接入某个 agent（改配置，先看 --dry-run）", run: cmdLink})
	register(command{name: "unlink", desc: "撤销 link 写入的配置与区块", run: cmdUnlink})
}

func cmdLink(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "项目绝对路径，默认当前目录")
	dryRun := fs.Bool("dry-run", false, "只打印计划，不写任何文件")
	if err := fs.Parse(splitFlags(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: agent-memory link <%s> [--project PATH] [--dry-run]\n",
			joinNames(surface.LinkTargets()))
		return 2
	}
	target := fs.Arg(0)

	// link 只改 agent 配置与项目区块，不需要打开共享库；
	// 用只读配置入口，--dry-run 才能真的一个字节都不写。
	e, err := loadConfigEnv()
	if err != nil {
		return reportError(stderr, err)
	}

	opts, err := linkOptions(e, target, *project, *dryRun)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	plan, err := surface.PlanLink(target, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	service := fmt.Sprintf("127.0.0.1:%d", opts.Port)
	if opts.Port == 0 {
		service = "127.0.0.1:<serve 启动后的端口>"
	}
	header := "计划:"
	if *dryRun {
		header = "计划（dry-run 未写入）:"
	}
	fmt.Fprintf(stdout, "%s\n  agent:        %s\n  MCP 配置:     %s\n  配置键:       %q -> %q\n  上下文区块:   %s\n  服务地址:     %s\n",
		header, plan.Target, plan.MCPPath, plan.MCPRoot, surface.MCPName, plan.BlockFile, service)

	if *dryRun {
		fmt.Fprintln(stdout, "去掉 --dry-run 才会写入；写前会自动备份为 <文件>.bak-<时间戳>")
		return 0
	}

	report, err := surface.ApplyLink(plan, opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	mcpState := "未变化"
	if report.MCPChanged {
		mcpState = "已写入，备份 " + report.Backup
	}
	blockState := "未变化"
	if report.BlockAdded {
		blockState = "已安装"
	}
	fmt.Fprintf(stdout, "MCP 配置: %s\n上下文区块: %s (%s)\n", mcpState, blockState, report.BlockFile)
	fmt.Fprintln(stdout, "重启该 agent 后，新会话会自动读到 HANDOFF.md")

	// 登记是尽力而为：配置与区块已经落盘，登记失败只能提示，不能回滚成功的那部分。
	ctx := context.Background()
	st, err := store.Open(e.cfg.DBPath())
	if err != nil {
		fmt.Fprintf(stderr, "warn: 项目未登记进库: %v\n", err)
		return 0
	}
	defer st.Close()
	// 新项目先 link 再 scan 也是合法顺序：库里没有映射时按 remote / 路径建。
	projectID, _, err := st.ResolveOrCreateProject(ctx, target, "", opts.ProjectDir, time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "warn: 项目未登记进库: %v\n", err)
		return 0
	}
	if err := st.RecordAgentLink(ctx, projectID, target, report.BlockFile, contentHashOf(report.BlockFile), time.Now()); err != nil {
		fmt.Fprintf(stderr, "warn: agent_link 登记失败: %v\n", err)
	}
	return 0
}

func cmdUnlink(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("unlink", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "项目绝对路径，默认当前目录")
	if err := fs.Parse(splitFlags(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: agent-memory unlink <agent> [--project PATH]")
		return 2
	}

	e, err := loadConfigEnv()
	if err != nil {
		return reportError(stderr, err)
	}

	// unlink 不需要活的 serve：端口只是配置里的一个数字，撤销时不必校验
	opts, err := linkOptions(e, fs.Arg(0), *project, true)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	report, err := surface.ApplyUnlink(opts)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "MCP 配置回滚: %v\n上下文区块移除: %v\n", report.MCPChanged, report.BlockAdded)

	// 撤销登记同样尽力而为：配置与区块已经回滚，库里的残行最多让 doctor 多列一行。
	// 库不存在就没有可撤销的登记，也不该为了撤销顺手建一个空库。
	if _, statErr := os.Stat(e.cfg.DBPath()); statErr != nil {
		return 0
	}
	ctx := context.Background()
	st, err := store.Open(e.cfg.DBPath())
	if err != nil {
		fmt.Fprintf(stderr, "warn: agent_link 撤销失败: %v\n", err)
		return 0
	}
	defer st.Close()
	id, ok, err := st.ResolveProjectByPath(ctx, opts.ProjectDir)
	if err != nil {
		fmt.Fprintf(stderr, "warn: agent_link 撤销失败: %v\n", err)
		return 0
	}
	if ok {
		if _, err := st.ClearAgentLink(ctx, id, fs.Arg(0)); err != nil {
			fmt.Fprintf(stderr, "warn: agent_link 撤销失败: %v\n", err)
		}
	}
	return 0
}

// contentHashOf 算区块文件的当前内容哈希。读不到就返回空串：
// 登记仍要写（doctor 至少能列出目标），哈希只用于判断区块是否被外部改过。
func contentHashOf(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return normalize.HashBytes(data)
}

// linkOptions 组装 link/unlink 的输入。
// port 与 token 来自正在运行的 serve：没有它就没法给出可用的 MCP 配置。
// allowMissingPort 给 dry-run 与 unlink 用——看计划和撤销都不需要活的 serve，
// 也不能以「补齐令牌」为名写文件。
func linkOptions(e *env, target, project string, allowMissingPort bool) (surface.LinkOptions, error) {
	abs := project
	if abs == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return surface.LinkOptions{}, err
		}
		abs = cwd
	}
	abs, err := filepath.Abs(abs)
	if err != nil {
		return surface.LinkOptions{}, err
	}
	bin, err := os.Executable()
	if err != nil {
		return surface.LinkOptions{}, err
	}
	port, err := daemon.ReadPort(e.cfg.PortPath())
	if err != nil {
		if !allowMissingPort {
			return surface.LinkOptions{}, fmt.Errorf("agent-memory serve 未运行：先启动它再 link（link 需要真实端口写进 agent 配置）")
		}
		port = 0
	}
	token := ""
	if !allowMissingPort {
		token, err = daemon.LoadOrCreateToken(e.cfg.TokenPath())
		if err != nil {
			return surface.LinkOptions{}, err
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return surface.LinkOptions{}, err
	}
	return surface.LinkOptions{
		Target: target, Home: home, ProjectDir: abs,
		Binary: bin, Token: token, Port: port,
	}, nil
}

func joinNames(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += "|"
		}
		out += s
	}
	return out
}
