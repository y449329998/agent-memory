package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func init() {
	register(command{name: "memory", desc: "检索/写入共享记忆: search|add|list|purge", run: cmdMemory})
}

func cmdMemory(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-memory memory <search|add|list|purge> [args]")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "search":
		return memorySearch(rest, stdout, stderr)
	case "add":
		return memoryAdd(rest, stdout, stderr)
	case "list":
		return memoryList(rest, stdout, stderr)
	case "purge":
		return memoryPurge(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown memory subcommand %q\n", sub)
		return 2
	}
}

func memorySearch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectRef := fs.String("project", "", "限定项目（路径或 project key）；用户级记忆始终参与")
	typ := fs.String("type", "", "限定记忆类型")
	limit := fs.Int("limit", 20, "返回条数")
	includeStale := fs.Bool("include-stale", false, "包含已过时条目")
	if err := fs.Parse(splitFlags(fs, args)); err != nil {
		return 2
	}
	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(stderr, "usage: agent-memory memory search <关键词>")
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	ctx := context.Background()
	projectID, err := resolveProjectRef(ctx, e.st, *projectRef)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if *typ != "" && !domain.ValidMemoryType(domain.MemoryType(*typ)) {
		fmt.Fprintf(stderr, "error: unknown type %q\n", *typ)
		return 2
	}

	hits, err := store.NewFTS(e.st).Search(ctx, domain.SearchQuery{
		Text:         query,
		ProjectID:    projectID,
		Type:         domain.MemoryType(*typ),
		IncludeStale: *includeStale,
		Limit:        *limit,
	})
	if err != nil {
		return reportError(stderr, err)
	}
	if len(hits) == 0 {
		fmt.Fprintf(stdout, "无命中: %s\n", query)
		return 0
	}
	for _, h := range hits {
		fmt.Fprintf(stdout, "%.3f  %s\n", h.Score, formatMemoryLine(h.Memory.Scope, h.Memory.Type, h.Memory.Text))
		for _, src := range h.Memory.Sources {
			fmt.Fprintf(stdout, "        来源 %s: %s\n", src.Agent, src.Path)
		}
		fmt.Fprintf(stdout, "        最后见到 %s\n", h.Memory.LastSeenAt.Format(time.RFC3339))
	}
	return 0
}

func memoryAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	typ := fs.String("type", string(domain.TypeFact), "记忆类型")
	scope := fs.String("scope", string(domain.ScopeUser), "user 或 project")
	projectRef := fs.String("project", "", "scope=project 时必填：项目绝对路径")
	agentName := fs.String("agent", "cli", "写入方标识，MCP 端会传 mcp")
	if err := fs.Parse(splitFlags(fs, args)); err != nil {
		return 2
	}
	text := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(stderr, "usage: agent-memory memory add <文本> [--type T] [--scope S] [--project P]")
		return 2
	}
	if !domain.ValidMemoryType(domain.MemoryType(*typ)) {
		fmt.Fprintf(stderr, "error: unknown type %q\n", *typ)
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	var projectID int64
	switch domain.Scope(*scope) {
	case domain.ScopeUser:
		projectID = 0
	case domain.ScopeProject:
		if *projectRef == "" {
			fmt.Fprintln(stderr, "error: --scope project 需要 --project <绝对路径>")
			return 2
		}
		if projectID, _, err = e.st.ProjectIDForPath(ctx, *agentName, "", *projectRef, "", time.Now()); err != nil {
			return reportError(stderr, err)
		}
	default:
		fmt.Fprintf(stderr, "error: unknown scope %q\n", *scope)
		return 2
	}

	draft := domain.MemoryDraft{
		Scope:       domain.Scope(*scope),
		Type:        domain.MemoryType(*typ),
		Text:        text,
		SourceAgent: *agentName,
		SourcePath:  "cli",
		Quote:       text,
	}
	id, created, err := e.st.UpsertMemory(ctx, projectID, draft, time.Now())
	if err != nil {
		return reportError(stderr, err)
	}
	word := "合并到已有条目"
	if created {
		word = "新建"
	}
	fmt.Fprintf(stdout, "memory#%d %s (%s/%s)\n", id, word, draft.Scope, draft.Type)
	return 0
}

func memoryList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectRef := fs.String("project", "", "项目路径或 key；空则只列用户级")
	limit := fs.Int("limit", 50, "返回条数")
	includeStale := fs.Bool("include-stale", false, "包含已过时条目")
	if err := fs.Parse(splitFlags(fs, args)); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	projectID, err := resolveProjectRef(ctx, e.st, *projectRef)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	items, err := e.st.ListMemories(ctx, projectID, *includeStale, *limit)
	if err != nil {
		return reportError(stderr, err)
	}
	for _, m := range items {
		flag := ""
		if m.Stale {
			flag = " (stale)"
		}
		agents := make([]string, 0, len(m.Sources))
		for _, s := range m.Sources {
			agents = append(agents, s.Agent)
		}
		fmt.Fprintf(stdout, "#%-4d %-8s %-11s %-40s [%s]%s\n",
			m.ID, m.Scope, m.Type, clip(m.Text, 40), strings.Join(agents, ","), flag)
	}
	fmt.Fprintf(stdout, "共 %d 条\n", len(items))
	return 0
}

func memoryPurge(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory purge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "确认删除全部过时条目")
	if err := fs.Parse(splitFlags(fs, args)); err != nil {
		return 2
	}
	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	n, err := e.st.QueryInt(ctx, `SELECT count(*) FROM memory_item WHERE stale = 1`)
	if err != nil {
		return reportError(stderr, err)
	}
	if !*yes {
		fmt.Fprintf(stdout, "有过时条目 %d 条，加 --yes 才会真删\n", n)
		return 0
	}
	deleted, err := e.st.PurgeStale(ctx)
	if err != nil {
		return reportError(stderr, err)
	}
	fmt.Fprintf(stdout, "已删除 %d 条过时记忆\n", deleted)
	return 0
}

func clip(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
