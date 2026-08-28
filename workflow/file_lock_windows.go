//go:build windows

package workflow

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func lockLedger(path string, exclusive bool) (*os.File, error) {
	file, err := openLedgerLock(path)
	if err != nil {
		return nil, err
	}
	flags := uintptr(0)
	if exclusive {
		flags = 2 // LOCKFILE_EXCLUSIVE_LOCK
	}
	var overlapped syscall.Overlapped
	ok, _, callErr := lockFileEx.Call(file.Fd(), flags, 0, 0xffffffff, 0xffffffff, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		file.Close()
		return nil, fmt.Errorf("lock ledger: %w", callErr)
	}
	return file, nil
}

func unlockLedger(file *os.File) {
	var overlapped syscall.Overlapped
	_, _, _ = unlockFileEx.Call(file.Fd(), 0, 0xffffffff, 0xffffffff, uintptr(unsafe.Pointer(&overlapped)))
	_ = file.Close()
}
