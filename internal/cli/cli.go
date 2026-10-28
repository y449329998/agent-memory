package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/agent/qoder"
	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

type env struct {
	cfg      config.Config
	userHome string
	st       *store.Store
}

// openEnv 读配置并打开共享库。每个子命令都从这里开始。
func openEnv() (*env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve user home: %w", err)
	}
	cfg, err := config.Load(config.ConfigPath(home), home)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	return &env{cfg: cfg, userHome: home, st: st}, nil
}

// buildAdapters 把配置翻译成启用的适配器列表。
// 新增 agent 时只改这里：其余层都通过 agent.Adapter 接口工作。
func buildAdapters(cfg config.Config) []agent.Adapter {
	var out []agent.Adapter
	if home := cfg.QoderHome(); home != "" {
		out = append(out, qoder.New(home))
	}
	return out
}

// resolveProjectRef 接受项目路径或 project key，返回 project_id。
// 空串表示不限定项目（只查用户级）。
func resolveProjectRef(ctx context.Context, st *store.Store, ref string) (int64, error) {
	if ref == "" {
		return 0, nil
	}
	if id, ok, err := st.ResolveProjectByPath(ctx, ref); err != nil {
		return 0, err
	} else if ok {
		return id, nil
	}
	if p, ok, err := st.ProjectByKey(ctx, ref); err != nil {
		return 0, err
	} else if ok {
		return p.ID, nil
	}
	return 0, fmt.Errorf("unknown project %q: run `agent-memory scan` first", ref)
}

func reportError(w io.Writer, err error) int {
	fmt.Fprintf(w, "error: %v\n", err)
	return 1
}

// formatMemoryLine 是 search 与 list 共用的单行格式。
func formatMemoryLine(scope domain.Scope, typ domain.MemoryType, text string) string {
	return fmt.Sprintf("[%s/%s] %s", scope, typ, text)
}

// splitFlags 把出现在位置参数之后的 flag 挪到前面。
// 标准库 flag 遇到第一个非 flag 参数就停止解析，而
// `memory search 偏好 --limit 5`、`memory add 文本 --type convention`
// 是更自然的写法；重排后两类顺序都能工作。
// 未知 flag 原样留下，交给 Parse 报错（退出码 2）。
func splitFlags(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			// 把 "--" 本身交回 flag.Parse：只有它能终止 flag 解析，
			// 否则 `memory add -- "-5% 阈值"` 里的 "-5%" 仍会被当成未定义 flag。
			flags = append(flags, "--")
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if j := strings.Index(name, "="); j >= 0 {
			name = name[:j]
		}
		f := fs.Lookup(name)
		if f == nil {
			flags = append(flags, a)
			continue
		}
		flags = append(flags, a)
		if _, isBool := f.Value.(interface{ IsBoolFlag() bool }); isBool || strings.Contains(a, "=") {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, positional...)
}
