package byodb

import "fmt"

// DBStats is a point-in-time operational snapshot suitable for health checks,
// benchmarks, and application observability.
type DBStats struct {
	Version            uint64 `json:"version"`
	FormatVersion      uint32 `json:"format_version"`
	PageSizeBytes      uint64 `json:"page_size_bytes"`
	PageCount          uint64 `json:"page_count"`
	FreePages          uint64 `json:"free_pages"`
	FileSizeBytes      int64  `json:"file_size_bytes"`
	ActiveTransactions int    `json:"active_transactions"`
	TableCount         int    `json:"table_count"`
}

// KeyCompressionStats measures the live B+tree only; obsolete and free pages
// are intentionally excluded. LogicalBytes is the size full keys would use in
// the same nodes, while StoredBytes is their prefix-compressed encoded size.
type KeyCompressionStats struct {
	LeafPages           uint64 `json:"leaf_pages"`
	CompressedLeafPages uint64 `json:"compressed_leaf_pages"`
	LogicalBytes        uint64 `json:"logical_bytes"`
	StoredBytes         uint64 `json:"stored_bytes"`
	BytesSaved          uint64 `json:"bytes_saved"`
}

func (kv *KV) KeyCompressionStats() (KeyCompressionStats, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.closed || kv.file == nil {
		return KeyCompressionStats{}, ErrClosed
	}
	result := KeyCompressionStats{}
	seen := map[uint64]bool{}
	var walk func(uint64) error
	walk = func(pointer uint64) error {
		if pointer == 0 {
			return nil
		}
		if seen[pointer] {
			return fmt.Errorf("cycle or duplicate page %d in B+tree", pointer)
		}
		seen[pointer] = true
		page, err := kv.readPageBounded(pointer, kv.pageCount)
		if err != nil {
			return err
		}
		node := bnode(page)
		if node.btype() == bnodeLeaf {
			result.LeafPages++
			logical := uint64(plainNodeRangeBytes(node, 0, node.nkeys()))
			stored := uint64(node.nbytes())
			result.LogicalBytes += logical
			result.StoredBytes += stored
			if node.compressed() {
				result.CompressedLeafPages++
				result.BytesSaved += logical - stored
			}
			return nil
		}
		if node.btype() != bnodeInternal {
			return fmt.Errorf("invalid B+tree node type %d at page %d", node.rawType(), pointer)
		}
		for index := uint16(0); index < node.nkeys(); index++ {
			if err := walk(node.ptr(index)); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(kv.root); err != nil {
		return KeyCompressionStats{}, err
	}
	return result, nil
}

func (db *DB) KeyCompressionStats() (KeyCompressionStats, error) {
	if db.kv == nil {
		return KeyCompressionStats{}, ErrClosed
	}
	return db.kv.KeyCompressionStats()
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
		Version: db.kv.version, FormatVersion: db.kv.format, PageSizeBytes: BTreePageSize, PageCount: db.kv.pageCount,
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
