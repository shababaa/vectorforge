package byodb

// DBStats is a point-in-time operational snapshot suitable for health checks,
// benchmarks, and application observability.
type DBStats struct {
	Version            uint64 `json:"version"`
	PageSizeBytes      uint64 `json:"page_size_bytes"`
	PageCount          uint64 `json:"page_count"`
	FreePages          uint64 `json:"free_pages"`
	FileSizeBytes      int64  `json:"file_size_bytes"`
	ActiveTransactions int    `json:"active_transactions"`
	TableCount         int    `json:"table_count"`
}

func (db *DB) Stats() (DBStats, error) {
	if db.kv == nil {
		return DBStats{}, ErrClosed
	}
	db.kv.mu.Lock()
	if db.kv.closed || db.kv.file == nil {
		db.kv.mu.Unlock()
		return DBStats{}, ErrClosed
	}
	info, err := db.kv.file.Stat()
	stats := DBStats{
		Version: db.kv.version, PageSizeBytes: BTreePageSize, PageCount: db.kv.pageCount,
		FreePages: uint64(len(db.kv.free)), ActiveTransactions: len(db.kv.ongoing),
	}
	if err == nil {
		stats.FileSizeBytes = info.Size()
	}
	db.kv.mu.Unlock()
	if err != nil {
		return DBStats{}, err
	}
	db.mu.RLock()
	stats.TableCount = len(db.tables)
	db.mu.RUnlock()
	return stats, nil
}
