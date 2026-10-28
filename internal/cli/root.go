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

// reservedNames 由 Run 的 switch 直接处理，不经过命令表。
var reservedNames = map[string]bool{
	"help":   true,
	"-h":     true,
	"--help": true,
}

// register 登记子命令，通常在 init() 中调用。重名或使用保留名时 panic。
func register(c command) {
	if reservedNames[c.name] {
		panic(fmt.Sprintf("cli: command name is reserved: %q", c.name))
	}
	if _, ok := commands[c.name]; ok {
		panic(fmt.Sprintf("cli: duplicate command %q", c.name))
	}
	commands[c.name] = c
}

func init() {
	register(command{
		name: "version",
		desc: "打印版本号",
		run: func(_ []string, stdout, _ io.Writer) int {
			fmt.Fprintf(stdout, "agent-memory %s\n", Version)
			return 0
		},
	})
}

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
	case "--version":
		return commands["version"].run(args[1:], stdout, stderr)
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	return cmd.run(args[1:], stdout, stderr)
}
