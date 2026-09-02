//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package byodb

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockDatabaseFile(file *os.File, shared bool) error {
	operation := syscall.LOCK_EX | syscall.LOCK_NB
	if shared {
		operation = syscall.LOCK_SH | syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), operation); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return fmt.Errorf("%w: %s", ErrDatabaseLocked, file.Name())
		}
		return fmt.Errorf("lock database file: %w", err)
	}
	return nil
}

func unlockDatabaseFile(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err != nil {
		return fmt.Errorf("unlock database file: %w", err)
	}
	return nil
}
