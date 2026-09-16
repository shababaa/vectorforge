package byodb

import (
	"path/filepath"
	"testing"
)

func TestSyncParentIsSupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.db")
	if err := syncParent(path); err != nil {
		t.Fatalf("sync parent directory: %v", err)
	}
}
