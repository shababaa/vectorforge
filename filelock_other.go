//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package byodb

import (
	"errors"
	"os"
)

func lockDatabaseFile(*os.File, bool) error {
	return errors.New("inter-process database locking is unsupported on this platform")
}

func unlockDatabaseFile(*os.File) error { return nil }
