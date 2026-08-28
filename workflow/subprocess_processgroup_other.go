//go:build !darwin && !linux

package workflow

import "os/exec"

func configureProcessGroup(*exec.Cmd) {}
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func processAlive(int) bool    { return false }
func terminateProcess(int)     {}
func childPIDObservable() bool { return false }
