//go:build !windows

package byodb

import (
	"os"
	"path/filepath"
)

// syncParent makes the creation of path durable on platforms where directories
// can be synced through the standard library.
func syncParent(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
