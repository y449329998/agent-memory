package main

import (
	"os"

	"github.com/muyi99/agent-memory/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
