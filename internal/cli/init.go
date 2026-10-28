package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/store"
)

func init() {
	register(command{name: "init", desc: "创建数据目录与配置文件", run: cmdInit})
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "数据目录，默认 <用户目录>/.agent-memory")
	force := fs.Bool("force", false, "覆盖已存在的配置文件")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return reportError(stderr, err)
	}
	cfgPath := config.ConfigPath(home)
	cfg, err := config.Load(cfgPath, home)
	if err != nil {
		return reportError(stderr, err)
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	if _, statErr := os.Stat(cfgPath); statErr == nil && !*force {
		fmt.Fprintf(stdout, "配置文件已存在，保持不变: %s（--force 覆盖）\n", cfgPath)
	} else if err := config.Save(cfgPath, cfg); err != nil {
		return reportError(stderr, err)
	}

	// 打开一次即可建表；后续子命令复用同一个库
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return reportError(stderr, err)
	}
	defer st.Close()

	fmt.Fprintf(stdout, "数据目录: %s\n", cfg.DataDir)
	fmt.Fprintf(stdout, "共享记忆库: %s\n", cfg.DBPath())
	if q := cfg.QoderHome(); q != "" {
		fmt.Fprintf(stdout, "发现 Qoder: %s\n", q)
	} else {
		fmt.Fprintln(stdout, "未发现 Qoder 目录，可在 config.toml 的 [qoder] home 里指定")
	}
	fmt.Fprintln(stdout, "下一步: agent-memory scan")
	return 0
}
