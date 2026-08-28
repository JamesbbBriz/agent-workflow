//go:build linux

package workflow

import (
	"bytes"
	"errors"
	"os"
	"strconv"
	"syscall"
)

func processAlive(pid int) bool {
	if state, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat"); err == nil {
		if marker := bytes.LastIndex(state, []byte(") ")); marker >= 0 && len(state) > marker+2 && state[marker+2] == 'Z' {
			return false
		}
	}
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}
