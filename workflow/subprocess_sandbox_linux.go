//go:build linux

package workflow

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

func sandboxDriver() (contractsv1.ProviderIsolationEvidenceDriver, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return "", err
	}
	probe := exec.Command(bwrap, "--unshare-all", "--ro-bind", "/", "/", "--", "/bin/true")
	probe.Env = []string{}
	if err := probe.Run(); err != nil {
		return "", fmt.Errorf("bubblewrap isolation is unavailable: %w", err)
	}
	return contractsv1.ProviderIsolationEvidenceDriverBubblewrap, nil
}

func sandboxCommand(executable string, args []string, root string, maxOutputBytes int, allowNetwork bool) (string, []string, error) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return "", nil, err
	}
	commandArgs := []string{"--die-with-parent", "--new-session", "--unshare-all", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/workspace", "--ro-bind", root + "/input", "/workspace/input", "--ro-bind", root + "/output", "/workspace/output", "--bind", root + "/output/result", "/workspace/output/result", "--ro-bind", executable, "/provider", "--chdir", "/workspace"}
	if allowNetwork {
		commandArgs = append(commandArgs, "--share-net")
		for _, path := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/ssl/certs"} {
			if _, err := os.Stat(path); err == nil {
				commandArgs = append(commandArgs, "--ro-bind", path, path)
			}
		}
	}
	for _, path := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if _, err := os.Stat(path); err == nil {
			commandArgs = append(commandArgs, "--ro-bind", path, path)
		}
	}
	commandArgs = append(commandArgs, "--", "/bin/sh", "-c", `ulimit -f "$1"; shift; exec "$@"`, "agent-workflow-provider", strconv.Itoa(maxOutputBytes/512), "/provider")
	return bwrap, append(commandArgs, args...), nil
}
