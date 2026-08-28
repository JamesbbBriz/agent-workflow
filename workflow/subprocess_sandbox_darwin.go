//go:build darwin

package workflow

import (
	"errors"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func sandboxDriver() (contractsv1.ProviderIsolationEvidenceDriver, error) {
	return "", errors.New("staged subprocess isolation requires Linux Bubblewrap process containment")
}

func sandboxCommand(string, []string, string, int) (string, []string, error) {
	return "", nil, errors.New("staged subprocess isolation requires Linux Bubblewrap process containment")
}
