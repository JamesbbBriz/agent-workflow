//go:build darwin

package workflow

import (
	"errors"
	"syscall"
)

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}

func childPIDObservable() bool { return true }
