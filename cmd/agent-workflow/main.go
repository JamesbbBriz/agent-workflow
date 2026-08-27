package main

import (
	"os"

	"github.com/JamesbbBriz/agent-workflow/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
