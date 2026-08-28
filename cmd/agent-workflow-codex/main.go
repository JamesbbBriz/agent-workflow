package main

import (
	"log"
	"os"

	"github.com/JamesbbBriz/agent-workflow/internal/adapterbridge"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func main() {
	if err := adapterbridge.Run(adapterbridge.Config{Provider: contractsv1.ProviderIDCodex, Upstream: "codex"}, os.Stdin, os.Stdout); err != nil {
		log.Fatal(err)
	}
}
