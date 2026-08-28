//go:build !windows

package workflow

import (
	"fmt"
	"os"
	"syscall"
)

func lockLedger(path string, exclusive bool) (*os.File, error) {
	file, err := openLedgerLock(path)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(file.Fd()), mode); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock ledger: %w", err)
	}
	return file, nil
}

func unlockLedger(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}
