package cli

import (
	"fmt"
	"io"
	"sort"
)

const Version = "0.1.0-m1"

type command struct {
	name string
	desc string
	run  func(args []string, stdout, stderr io.Writer) int
}

var commands = map[string]command{}

func register(c command) { commands[c.name] = c }

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: agent-memory <command> [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-10s %s\n", name, commands[name].desc)
	}
}

// Run 分发子命令。0 成功，1 运行错误，2 用法错误。
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "version", "--version":
		fmt.Fprintf(stdout, "agent-memory %s\n", Version)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	return cmd.run(args[1:], stdout, stderr)
}
