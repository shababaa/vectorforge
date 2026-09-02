//go:build windows

package byodb

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
	errorSharingViolation   = syscall.Errno(32)
	errorLockViolation      = syscall.Errno(33)
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func lockDatabaseFile(file *os.File, shared bool) error {
	flags := uintptr(lockfileFailImmediately)
	if !shared {
		flags |= lockfileExclusiveLock
	}
	var overlapped syscall.Overlapped
	result, _, callErr := procLockFileEx.Call(file.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result != 0 {
		return nil
	}
	if errors.Is(callErr, errorLockViolation) || errors.Is(callErr, errorSharingViolation) {
		return fmt.Errorf("%w: %s", ErrDatabaseLocked, file.Name())
	}
	return fmt.Errorf("lock database file: %w", callErr)
}

func unlockDatabaseFile(file *os.File) error {
	var overlapped syscall.Overlapped
	result, _, callErr := procUnlockFileEx.Call(file.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if result == 0 {
		return fmt.Errorf("unlock database file: %w", callErr)
	}
	return nil
}
