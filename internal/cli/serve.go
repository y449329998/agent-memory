package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/daemon"
	"github.com/muyi99/agent-memory/internal/ingest"
	"github.com/muyi99/agent-memory/internal/store"
)

func init() {
	register(command{name: "serve", desc: "启动常驻服务（扫描 + HTTP + MCP）", run: cmdServe})
	register(command{name: "stats", desc: "打印共享库统计", run: cmdStats})
}

func cmdServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	port := fs.Int("port", 0, "监听端口，0 表示随机")
	pollFlag := fs.Int("poll", 0, "轮询兜底间隔秒数，0 表示用配置 scan.interval")
	debounce := fs.Duration("debounce", 2*time.Second, "事件去抖窗口")
	noWatch := fs.Bool("no-watch", false, "只服务不扫描（调试用）")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	token, err := daemon.LoadOrCreateToken(e.cfg.TokenPath())
	if err != nil {
		return reportError(stderr, err)
	}

	if err := refuseIfAlreadyRunning(*port, e.cfg.PortPath()); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	adapters := buildAdapters(e.cfg)
	scanOnce := newScanOnce(e.st, adapters, e.cfg, stdout)

	srv := daemon.New(daemon.Config{
		Token:        token,
		DataDir:      e.cfg.DataDir,
		Port:         *port,
		BudgetTokens: e.cfg.Handoff.BudgetTokens,
		RecentCards:  e.cfg.Handoff.RecentCards,
		Scan: func(ctx context.Context) (map[string]any, error) {
			res, err := scanOnce(ctx)
			if err != nil {
				return nil, err
			}
			return scanResult(res), nil
		},
	}, e.st)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if !*noWatch && len(adapters) > 0 {
		go scanLoop(ctx, scanOnce, adapters, *debounce, resolvePollSeconds(*pollFlag, e.cfg.Scan.Interval), stdout)
	}

	fmt.Fprintf(stdout, "serve on 127.0.0.1 (token: %s)\n", e.cfg.TokenPath())
	fmt.Fprintf(stdout, "agents: %v\n", adapterNames(adapters))
	if err := srv.ListenAndServe(ctx, e.cfg.PortPath()); err != nil {
		return reportError(stderr, err)
	}
	return 0
}

// resolvePollSeconds 把 --poll 解析成实际轮询间隔：0 表示跟随配置的 scan.interval。
// 配置单位是 Duration，而 watcher 只接受秒；不足一秒按 1 秒处理，避免 0 被 watcher
// 当成「没配置」再回落到它自己的 30 秒默认值。
func resolvePollSeconds(flagValue int, interval time.Duration) int {
	if flagValue > 0 {
		return flagValue
	}
	seconds := int(interval / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return seconds
}

// scanResult 把一轮扫描的汇总摊成 /v1/scan 的 JSON。
func scanResult(res ingest.Result) map[string]any {
	errs := res.Errors
	if errs == nil {
		errs = []string{}
	}
	return map[string]any{
		"units": res.Units, "created": res.Created, "merged": res.Merged,
		"unchanged": res.Unchanged, "staled": res.Staled,
		"sessions": res.Sessions, "sessions_unchanged": res.SessionUnchanged,
		"cards": res.Cards, "unsettled": res.SkippedUnsettled,
		"errors": errs,
	}
}

// newScanOnce 返回「扫一轮 + 刷新简报」的共享入口：serve 的启动/事件扫描与
// Web UI 的 /v1/scan 都调它，避免两套编排漂移。内部用信号量串行化，
// 事件扫描与手动触发撞车时排队，而不是并发争抢 SQLite。
func newScanOnce(st *store.Store, adapters []agent.Adapter, cfg config.Config, stdout io.Writer) func(context.Context) (ingest.Result, error) {
	runner := ingest.New(st, adapters...)
	runner.SettleIdle = cfg.Handoff.SettleIdle
	sem := make(chan struct{}, 1)
	return func(ctx context.Context) (ingest.Result, error) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return ingest.Result{}, ctx.Err()
		}
		res, err := runner.Run(ctx, "")
		if err != nil {
			return res, err
		}
		refreshHandoffsFor(ctx, st, cfg, stdout)
		return res, nil
	}
}

// scanLoop 启动先扫一次，之后由监听事件驱动。扫描期间收到的事件靠 channel 合并成一次补扫。
func scanLoop(ctx context.Context, scanOnce func(context.Context) (ingest.Result, error), adapters []agent.Adapter, debounce time.Duration, pollSeconds int, stdout io.Writer) {
	dirs := watchDirs(adapters)
	w := ingest.NewWatcher(ingest.WatchConfig{Dirs: dirs, Debounce: debounce, PollSeconds: pollSeconds})

	requested := make(chan struct{}, 1)
	w.SetHandler(func(context.Context) {
		select {
		case requested <- struct{}{}:
		default:
		}
	})
	go w.Run(ctx)

	run := func(reason string) {
		res, err := scanOnce(ctx)
		if err != nil {
			fmt.Fprintf(stdout, "scan %s error: %v\n", reason, err)
			return
		}
		if res.Created+res.Merged+res.Cards+res.Staled > 0 {
			fmt.Fprintf(stdout, "scan (%s): %s\n", reason, res.String())
		}
	}
	run("startup")

	for {
		select {
		case <-ctx.Done():
			return
		case <-requested:
			// watcher 已去过抖；这里再等一个窗口，把「保存到一半」的连续事件合并掉。
			time.Sleep(debounce)
			run("event")
		}
	}
}

// refuseIfAlreadyRunning 检查目标端口上是否已有一个 serve：
// 两个实例同时扫描会争抢 SQLite 并重复写简报，第二个必须直接退出。
func refuseIfAlreadyRunning(port int, portPath string) error {
	probe := port
	fromPortFile := false
	if probe == 0 {
		// --port 0 也要能发现「上次 serve 还活着」：读它留下的 port 文件。
		p, err := daemon.ReadPort(portPath)
		if err != nil {
			return nil
		}
		probe = p
		fromPortFile = true
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", probe), 500*time.Millisecond)
	if err != nil {
		return nil
	}
	conn.Close()

	// 端口通还不算：确认它真的应答 /v1/healthz，避免别的程序占了同一端口。
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/healthz", probe))
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return fmt.Errorf("agent-memory serve 已在运行（端口 %d），先停掉它或用 --port 换一个端口", probe)
		}
	}
	if fromPortFile {
		// port 文件是上次运行留下的；端口被别的程序接手时，本次仍可用随机端口启动。
		return nil
	}
	// 显式 --port 已被占用：即使健康检查不通过也不可能绑定成功，直接给出可操作的报错。
	return fmt.Errorf("端口 %d 已被占用：agent-memory serve 可能已在运行；先停掉它或用 --port 换一个端口", probe)
}

// adapterNames 用于启动日志。
func adapterNames(adapters []agent.Adapter) []string {
	out := make([]string, 0, len(adapters))
	for _, a := range adapters {
		out = append(out, a.Name())
	}
	return out
}

// watchDirs 收集所有实现 agent.Watchable 的适配器目录，保持适配器顺序并去重。
// 不实现该接口的适配器（或返回 nil 的）不产生监听目录，只靠轮询兜底。
func watchDirs(adapters []agent.Adapter) []string {
	var out []string
	seen := make(map[string]bool)
	for _, a := range adapters {
		w, ok := a.(agent.Watchable)
		if !ok {
			continue
		}
		for _, dir := range w.WatchDirs() {
			if dir == "" {
				continue
			}
			// Windows 路径大小写不敏感：键统一小写，但保留首个写法。
			key := strings.ToLower(filepath.Clean(dir))
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, dir)
		}
	}
	return out
}

// refreshHandoffsFor 是 serve 扫描循环的简报刷新入口，逻辑与 scan 共用
// refreshHandoffFiles；这里只负责把「已更新」打到常驻日志。
func refreshHandoffsFor(ctx context.Context, st *store.Store, cfg config.Config, stdout io.Writer) {
	for _, r := range refreshHandoffFiles(ctx, st, cfg) {
		if r.Warn != "" {
			fmt.Fprintf(stdout, "  %s\n", r.Warn)
			continue
		}
		if r.Err != nil {
			fmt.Fprintf(stdout, "  %v\n", r.Err)
			continue
		}
		if r.Changed {
			fmt.Fprintf(stdout, "handoff 已更新: %s\n", r.Path)
		}
	}
}

func cmdStats(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "输出 json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	stats, err := e.st.Stats(context.Background())
	if err != nil {
		return reportError(stderr, err)
	}
	if *asJSON {
		return printJSON(stdout, stats)
	}
	fmt.Fprintf(stdout, "项目 %d，记忆 %d（过时 %d），源 %d（失败 %d）\n",
		stats.Projects, stats.Memories, stats.Stale, stats.Sources, stats.FailedSource)
	fmt.Fprintf(stdout, "按 agent: %v\n按类型: %v\n", stats.MemoriesByAgent, stats.MemoriesByType)
	return 0
}

func printJSON(w io.Writer, v any) int {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return 1
	}
	return 0
}
