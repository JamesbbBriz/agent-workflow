//go:build !darwin && !linux

package workflow

import (
	"errors"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func sandboxDriver() (contractsv1.ProviderIsolationEvidenceDriver, error) {
	return "", errors.New("no staged subprocess sandbox is available on this platform")
}

func sandboxCommand(string, []string, string, int, bool) (string, []string, error) {
	return "", nil, errors.New("no staged subprocess sandbox is available on this platform")
}
