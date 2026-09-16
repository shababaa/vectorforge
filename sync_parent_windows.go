//go:build windows

package byodb

// Windows does not support syncing an open directory through os.File.Sync.
// Database contents are still made durable by syncing the database file itself.
func syncParent(string) error {
	return nil
}
