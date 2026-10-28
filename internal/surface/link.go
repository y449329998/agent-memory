package surface

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/normalize"
)

const (
	BeginMarker = "<!-- agent-memory:begin -->"
	EndMarker   = "<!-- agent-memory:end -->"
	MCPName     = "agent-memory"
)

// LinkOptions 是一次 link/unlink 的全部输入。Home 与 ProjectDir 都可注入，
// 让测试不必碰真实用户目录。
type LinkOptions struct {
	Target     string
	Home       string
	ProjectDir string
	Binary     string
	Token      string
	Port       int
}

type Plan struct {
	Target      string
	MCPPath     string
	MCPRoot     string
	MCPEntry    map[string]any
	BlockFile   string
	BlockExists bool
}

type Report struct {
	MCPChanged bool
	Backup     string
	BlockFile  string
	BlockAdded bool
}

// LinkTargets 只列出配置格式已在本机确证过的 agent。
// 没确证就拒绝写入，比照着别人的目录结构猜一个更安全。
func LinkTargets() []string { return []string{"qoder", "opencode", "claude"} }

func PlanLink(target string, opts LinkOptions) (Plan, error) {
	home := opts.Home
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Plan{}, err
		}
		home = h
	}
	if opts.Binary == "" {
		return Plan{}, fmt.Errorf("binary path is required (link needs an absolute launcher)")
	}

	plan := Plan{Target: target}
	switch target {
	case "qoder":
		qHome := config.QoderHomeDir(home)
		if qHome == "" {
			// 新机器上 .qoder-cn 还不存在：按 Qoder CN 的默认位置落配置。
			qHome = filepath.Join(home, ".qoder-cn")
		}
		plan.MCPPath = filepath.Join(qHome, "mcp.json")
		plan.MCPRoot = "mcpServers"
		plan.MCPEntry = stdioEntry(opts, false)
		plan.BlockFile = filepath.Join(opts.ProjectDir, "AGENTS.md")
	case "opencode":
		plan.MCPPath = filepath.Join(home, ".config", "opencode", "opencode.json")
		plan.MCPRoot = "mcp"
		plan.MCPEntry = stdioEntry(opts, true)
		plan.BlockFile = filepath.Join(opts.ProjectDir, "AGENTS.md")
	case "claude":
		plan.MCPPath = filepath.Join(home, ".claude.json")
		plan.MCPRoot = "mcpServers"
		plan.MCPEntry = stdioEntry(opts, false)
		plan.BlockFile = filepath.Join(opts.ProjectDir, "CLAUDE.md")
	default:
		return Plan{}, fmt.Errorf("agent %q 的配置格式尚未确证，link 拒绝写入；它仍可通过项目里的 HANDOFF.md 读取共享记忆", target)
	}
	if opts.ProjectDir != "" {
		plan.BlockExists = BlockExists(plan.BlockFile, BeginMarker)
	}
	if plan.BlockFile == "" {
		return Plan{}, fmt.Errorf("project dir is required to place the context block")
	}
	return plan, nil
}

// stdioEntry 用环境变量传 token：命令行参数会进 shell 历史与进程列表。
// 两家 agent 的键名不同：Qoder/Claude 用 env，OpenCode 实测是 environment，
// 且 OpenCode 的 local MCP 没有 enabled 键——写错键名 token 就不生效，全部 401。
func stdioEntry(opts LinkOptions, openCode bool) map[string]any {
	args := []string{"mcp", "--port", fmt.Sprint(opts.Port)}
	tokenEnv := map[string]string{"AGENT_MEMORY_TOKEN": opts.Token}
	if openCode {
		// OpenCode 的 local MCP 用 command 数组，不用 command+args 两段式
		return map[string]any{
			"type":        "local",
			"command":     append([]string{opts.Binary}, args...),
			"environment": tokenEnv,
		}
	}
	return map[string]any{
		"command": opts.Binary,
		"args":    args,
		"env":     tokenEnv,
	}
}

func ApplyLink(plan Plan, opts LinkOptions) (Report, error) {
	var rep Report
	changed, backup, err := UpsertJSONKey(plan.MCPPath, plan.MCPRoot, MCPName, plan.MCPEntry)
	if err != nil {
		return rep, fmt.Errorf("mcp config: %w", err)
	}
	rep.MCPChanged = changed
	rep.Backup = backup

	content := BlockContent(projectKeyFor(opts.ProjectDir))
	blockChanged, err := InstallBlock(plan.BlockFile, BeginMarker, EndMarker, content)
	if err != nil {
		return rep, fmt.Errorf("context block: %w", err)
	}
	rep.BlockAdded = blockChanged
	rep.BlockFile = plan.BlockFile
	return rep, nil
}

func ApplyUnlink(opts LinkOptions) (Report, error) {
	var rep Report
	plan, err := PlanLink(opts.Target, opts)
	if err != nil {
		return rep, err
	}
	changed, backup, err := RemoveJSONKey(plan.MCPPath, plan.MCPRoot, MCPName)
	if err != nil {
		return rep, err
	}
	rep.MCPChanged = changed
	rep.Backup = backup
	blockChanged, err := RemoveBlock(plan.BlockFile, BeginMarker, EndMarker)
	if err != nil {
		return rep, err
	}
	rep.BlockAdded = blockChanged
	rep.BlockFile = plan.BlockFile
	return rep, nil
}

// BlockContent 是区块正文：只放指针和 project_key，正文留在 HANDOFF.md。
// 简报本身会随扫描变化，把它的副本塞进 AGENTS.md 只会让仓库文件反复产生 diff。
func BlockContent(projectKey string) string {
	return fmt.Sprintf("## 共享记忆（agent-memory）\n\n"+
		"这个项目接入跨 agent 共享记忆，project_key: `%s`。\n\n"+
		"- 接手前**先读** `.agent-memory/HANDOFF.md`：目标、进度、下一步与硬约束都在里面\n"+
		"- 学到的约定/约束/踩坑用 MCP tool `memory_add` 写回共享库，别只留在本 agent 记忆里\n"+
		"- 需要更多细节时用 `memory_search` 按需检索，不要复述整份简报\n"+
		"- 简报由 `agent-memory scan` 自动更新；手动重生成：`agent-memory handoff --project .`\n",
		projectKey)
}

func projectKeyFor(dir string) string {
	remote, _, err := readRemote(dir)
	if err != nil || remote == "" {
		return normalizePathKey(dir)
	}
	return remote
}

// readRemote / normalizePathKey 是 normalize 的薄封装：surface 不该依赖 store。
func readRemote(dir string) (string, bool, error) {
	return normalize.GitRemoteFromConfig(dir)
}

func normalizePathKey(dir string) string {
	return normalize.ProjectKey("", dir)
}
