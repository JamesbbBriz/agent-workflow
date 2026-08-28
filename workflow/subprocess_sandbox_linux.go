//go:build linux

package workflow

import (
	"os"
	"os/exec"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func sandboxDriver() (contractsv1.ProviderIsolationEvidenceDriver, error) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return "", err
	}
	return contractsv1.ProviderIsolationEvidenceDriverBubblewrap, nil
}

func sandboxCommand(executable string, args []string, root string) (string, []string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return "", nil, err
	}
	commandArgs := []string{"--die-with-parent", "--new-session", "--unshare-all", "--proc", "/proc", "--dev", "/dev", "--dir", "/workspace", "--ro-bind", root + "/input", "/workspace/input", "--bind", root + "/output", "/workspace/output", "--ro-bind", executable, "/provider", "--chdir", "/workspace"}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			commandArgs = append(commandArgs, "--ro-bind", path, path)
		}
	}
	commandArgs = append(commandArgs, "--", "/provider")
	return bwrap, append(commandArgs, args...), nil
}
