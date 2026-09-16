package byodb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const (
	metaMagic = "BYODB002"
	// StorageFormatVersion 3 adds prefix-compressed B+tree leaf nodes while
	// retaining read compatibility with format 2.
	StorageFormatVersion = uint32(3)
	freeMagic            = "BYOFL001"
	metaChecksum         = 56
	freeHeader           = 24
	freeEntrySize        = 16
	freeCapacity         = (BTreePageSize - freeHeader) / freeEntrySize
)

var (
	ErrConflict       = errors.New("transaction conflict: retry the transaction")
	ErrClosed         = errors.New("database is closed")
	ErrDatabaseLocked = errors.New("database file is locked by another process")
	ErrReadOnly       = errors.New("database is read-only")
)

// KVOptions controls how a key/value file is opened. Read-only files acquire a
// shared process lock and reject transactions containing writes.
type KVOptions struct {
	ReadOnly    bool
	commitFault func(commitStage) error // deterministic crash-boundary tests only
}

type commitStage string

const (
	commitDataSynced  commitStage = "data_synced"
	commitMetaWritten commitStage = "metadata_written"
	commitMetaSynced  commitStage = "metadata_synced"
)

type freeEntry struct {
	Ptr     uint64
	Version uint64
}

type diskMeta struct {
	Version   uint64
	Root      uint64
	PageCount uint64
	FreeHead  uint64
	FreeCount uint64
	Format    uint32
	Slot      int
	Chain     []uint64
	Free      []freeEntry
}

// KV is the durable, transactional key/value database from chapters 6, 7,
// 11, and 12. It uses two checksummed meta pages. A commit writes new COW data
// pages, fsyncs them, then publishes the new root in the alternate meta page.
type KV struct {
	Path string

	mu          sync.Mutex
	file        *os.File
	root        uint64
	version     uint64
	format      uint32
	pageCount   uint64
	free        []freeEntry
	metaChains  [2][]uint64
	ongoing     map[*KVTX]uint64
	history     []committedTX
	closed      bool
	readOnly    bool
	locked      bool
	commitFault func(commitStage) error
}

func OpenKV(path string) (*KV, error) {
	return OpenKVWithOptions(path, KVOptions{})
}

func OpenKVWithOptions(path string, options KVOptions) (*KV, error) {
	kv := &KV{Path: path, readOnly: options.ReadOnly, commitFault: options.commitFault}
	if err := kv.Open(); err != nil {
		return nil, err
	}
	return kv, nil
}

func (kv *KV) Open() error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.file != nil && !kv.closed {
		return nil
	}
	if kv.Path == "" {
		return errors.New("database path is empty")
	}
	if !kv.readOnly {
		if err := os.MkdirAll(filepath.Dir(kv.Path), 0o755); err != nil {
			return err
		}
	}
	_, statErr := os.Stat(kv.Path)
	created := errors.Is(statErr, os.ErrNotExist)
	flags := os.O_RDONLY
	if !kv.readOnly {
		flags = os.O_RDWR | os.O_CREATE
	}
	f, err := os.OpenFile(kv.Path, flags, 0o664)
	if err != nil {
		return err
	}
	if err := lockDatabaseFile(f, kv.readOnly); err != nil {
		_ = f.Close()
		return err
	}
	kv.file = f
	kv.locked = true
	kv.closed = false
	cleanup := func() {
		_ = unlockDatabaseFile(f)
		_ = f.Close()
		kv.file = nil
		kv.locked = false
		kv.closed = true
	}
	if created {
		if err := syncParent(kv.Path); err != nil {
			cleanup()
			return err
		}
	}
	st, err := f.Stat()
	if err != nil {
		cleanup()
		return err
	}
	if st.Size() == 0 {
		if kv.readOnly {
			cleanup()
			return errors.New("read-only database file is empty")
		}
		if err := kv.initializeFile(); err != nil {
			cleanup()
			return err
		}
	}
	if err := kv.loadFile(); err != nil {
		cleanup()
		return err
	}
	kv.ongoing = map[*KVTX]uint64{}
	kv.history = nil
	return nil
}

func (kv *KV) Close() error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.file == nil || kv.closed {
		return nil
	}
	if len(kv.ongoing) != 0 {
		return fmt.Errorf("cannot close database with %d active transaction(s)", len(kv.ongoing))
	}
	unlockErr := error(nil)
	if kv.locked {
		unlockErr = unlockDatabaseFile(kv.file)
		kv.locked = false
	}
	closeErr := kv.file.Close()
	kv.closed = true
	return errors.Join(unlockErr, closeErr)
}

// ReadOnly reports whether this handle rejects durable writes.
func (kv *KV) ReadOnly() bool {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	return kv.readOnly
}

// Backup writes the current durable database image to a new file. Holding the
// pager mutex makes the copy a point-in-time snapshot while transactions may
// continue building their private in-memory updates.
func (kv *KV) Backup(path string) (err error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.file == nil || kv.closed {
		return ErrClosed
	}
	if path == "" {
		return errors.New("backup path is empty")
	}
	source, err := filepath.Abs(kv.Path)
	if err != nil {
		return err
	}
	destination, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if source == destination {
		return errors.New("backup path must differ from database path")
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("backup target already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".byodb-backup-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	size := int64(kv.pageCount * BTreePageSize)
	if _, err := io.Copy(temporary, io.NewSectionReader(kv.file, 0, size)); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	return syncParent(destination)
}

func (kv *KV) initializeFile() error {
	if err := kv.file.Truncate(2 * BTreePageSize); err != nil {
		return err
	}
	base := diskMeta{Version: 0, PageCount: 2, Slot: 0}
	if err := writeFullAt(kv.file, encodeMeta(base), 0); err != nil {
		return err
	}
	base.Version, base.Slot = 1, 1
	if err := writeFullAt(kv.file, encodeMeta(base), BTreePageSize); err != nil {
		return err
	}
	return kv.file.Sync()
}

func (kv *KV) loadFile() error {
	st, err := kv.file.Stat()
	if err != nil {
		return err
	}
	if st.Size() < 2*BTreePageSize || st.Size()%BTreePageSize != 0 {
		return fmt.Errorf("invalid database file size: %d", st.Size())
	}
	var candidates []diskMeta
	for slot := 0; slot < 2; slot++ {
		page := make([]byte, BTreePageSize)
		if _, err := kv.file.ReadAt(page, int64(slot*BTreePageSize)); err != nil {
			continue
		}
		meta, err := decodeMeta(page, slot, uint64(st.Size()/BTreePageSize))
		if err != nil {
			continue
		}
		free, chain, err := kv.readFreeList(meta)
		if err != nil {
			continue
		}
		meta.Free, meta.Chain = free, chain
		candidates = append(candidates, meta)
		kv.metaChains[slot] = append([]uint64(nil), chain...)
	}
	if len(candidates) == 0 {
		return errors.New("no valid meta page found; database may be corrupted")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Version > candidates[j].Version })
	meta := candidates[0]
	kv.root = meta.Root
	kv.version = meta.Version
	kv.format = meta.Format
	kv.pageCount = meta.PageCount
	kv.free = meta.Free
	return nil
}

func encodeMeta(m diskMeta) []byte {
	page := make([]byte, BTreePageSize)
	if m.Format == 0 {
		m.Format = StorageFormatVersion
	}
	copy(page[0:8], metaMagic)
	binary.LittleEndian.PutUint64(page[8:16], m.Version)
	binary.LittleEndian.PutUint64(page[16:24], m.Root)
	binary.LittleEndian.PutUint64(page[24:32], m.PageCount)
	binary.LittleEndian.PutUint64(page[32:40], m.FreeHead)
	binary.LittleEndian.PutUint64(page[40:48], m.FreeCount)
	binary.LittleEndian.PutUint32(page[48:52], m.Format)
	binary.LittleEndian.PutUint32(page[60:64], BTreePageSize)
	binary.LittleEndian.PutUint32(page[metaChecksum:metaChecksum+4], checksumPage(page, metaChecksum))
	return page
}

func decodeMeta(page []byte, slot int, filePages uint64) (diskMeta, error) {
	if len(page) != BTreePageSize || string(page[0:8]) != metaMagic {
		return diskMeta{}, errors.New("bad meta signature")
	}
	want := binary.LittleEndian.Uint32(page[metaChecksum : metaChecksum+4])
	if want != checksumPage(page, metaChecksum) {
		return diskMeta{}, errors.New("bad meta checksum")
	}
	if binary.LittleEndian.Uint32(page[60:64]) != BTreePageSize {
		return diskMeta{}, errors.New("unsupported page size")
	}
	m := diskMeta{
		Version:   binary.LittleEndian.Uint64(page[8:16]),
		Root:      binary.LittleEndian.Uint64(page[16:24]),
		PageCount: binary.LittleEndian.Uint64(page[24:32]),
		FreeHead:  binary.LittleEndian.Uint64(page[32:40]),
		FreeCount: binary.LittleEndian.Uint64(page[40:48]),
		Format:    binary.LittleEndian.Uint32(page[48:52]),
		Slot:      slot,
	}
	if m.Format == 0 {
		m.Format = 2 // format-v2 metadata left this reserved field zeroed
	}
	if m.Format < 2 || m.Format > StorageFormatVersion {
		return diskMeta{}, fmt.Errorf("unsupported storage format: %d", m.Format)
	}
	if m.PageCount < 2 || m.PageCount > filePages || m.Root >= m.PageCount || m.FreeHead >= m.PageCount {
		return diskMeta{}, errors.New("meta page contains an invalid page pointer")
	}
	return m, nil
}

func checksumPage(page []byte, checksumOffset int) uint32 {
	h := crc32.NewIEEE()
	h.Write(page[:checksumOffset])
	h.Write([]byte{0, 0, 0, 0})
	h.Write(page[checksumOffset+4:])
	return h.Sum32()
}

func (kv *KV) readFreeList(meta diskMeta) ([]freeEntry, []uint64, error) {
	if meta.FreeHead == 0 {
		if meta.FreeCount != 0 {
			return nil, nil, errors.New("free-list count without a head")
		}
		return nil, nil, nil
	}
	entries := make([]freeEntry, 0, meta.FreeCount)
	chain := []uint64{}
	seen := map[uint64]bool{}
	ptr := meta.FreeHead
	for ptr != 0 {
		if ptr < 2 || ptr >= meta.PageCount || seen[ptr] {
			return nil, nil, errors.New("invalid or cyclic free-list page")
		}
		seen[ptr] = true
		chain = append(chain, ptr)
		page, err := kv.readPageBounded(ptr, meta.PageCount)
		if err != nil {
			return nil, nil, err
		}
		if string(page[0:8]) != freeMagic {
			return nil, nil, errors.New("bad free-list signature")
		}
		want := binary.LittleEndian.Uint32(page[20:24])
		if want != checksumPage(page, 20) {
			return nil, nil, errors.New("bad free-list checksum")
		}
		ptr = binary.LittleEndian.Uint64(page[8:16])
		count := int(binary.LittleEndian.Uint32(page[16:20]))
		if count < 0 || count > freeCapacity {
			return nil, nil, errors.New("bad free-list entry count")
		}
		for i := 0; i < count; i++ {
			pos := freeHeader + i*freeEntrySize
			entry := freeEntry{
				Ptr:     binary.LittleEndian.Uint64(page[pos : pos+8]),
				Version: binary.LittleEndian.Uint64(page[pos+8 : pos+16]),
			}
			if entry.Ptr < 2 || entry.Ptr >= meta.PageCount || seen[entry.Ptr] {
				return nil, nil, errors.New("invalid free page pointer")
			}
			entries = append(entries, entry)
		}
	}
	if uint64(len(entries)) != meta.FreeCount {
		return nil, nil, errors.New("free-list length does not match meta page")
	}
	return entries, chain, nil
}

func encodeFreePage(next uint64, entries []freeEntry) []byte {
	must(len(entries) <= freeCapacity, "too many entries in a free-list page")
	page := make([]byte, BTreePageSize)
	copy(page[0:8], freeMagic)
	binary.LittleEndian.PutUint64(page[8:16], next)
	binary.LittleEndian.PutUint32(page[16:20], uint32(len(entries)))
	for i, entry := range entries {
		pos := freeHeader + i*freeEntrySize
		binary.LittleEndian.PutUint64(page[pos:pos+8], entry.Ptr)
		binary.LittleEndian.PutUint64(page[pos+8:pos+16], entry.Version)
	}
	binary.LittleEndian.PutUint32(page[20:24], checksumPage(page, 20))
	return page
}

func (kv *KV) readPage(ptr uint64) ([]byte, error) {
	kv.mu.Lock()
	count := kv.pageCount
	closed := kv.closed
	kv.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	return kv.readPageBounded(ptr, count)
}

func (kv *KV) readPageBounded(ptr, count uint64) ([]byte, error) {
	if ptr < 2 || ptr >= count {
		return nil, fmt.Errorf("invalid page pointer %d (page count %d)", ptr, count)
	}
	page := make([]byte, BTreePageSize)
	n, err := kv.file.ReadAt(page, int64(ptr*BTreePageSize))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n != BTreePageSize {
		return nil, io.ErrUnexpectedEOF
	}
	return page, nil
}

type pageAllocator struct {
	free       []freeEntry
	pageCount  uint64
	maxVersion uint64
}

func (a *pageAllocator) alloc() uint64 {
	for i, entry := range a.free {
		if entry.Version <= a.maxVersion {
			a.free = append(a.free[:i], a.free[i+1:]...)
			return entry.Ptr
		}
	}
	ptr := a.pageCount
	a.pageCount++
	return ptr
}

func (a *pageAllocator) allocFreeListPage(existing int) uint64 {
	// Do not consume the final entry merely to create an empty list page.
	for i, entry := range a.free {
		remainingPages := (len(a.free) - 1 + freeCapacity - 1) / freeCapacity
		if entry.Version <= a.maxVersion && remainingPages >= existing+1 {
			a.free = append(a.free[:i], a.free[i+1:]...)
			return entry.Ptr
		}
	}
	ptr := a.pageCount
	a.pageCount++
	return ptr
}

func (kv *KV) oldestVersionLocked() uint64 {
	oldest := kv.version
	for _, version := range kv.ongoing {
		if version < oldest {
			oldest = version
		}
	}
	return oldest
}

func dedupeFree(entries []freeEntry, used map[uint64]bool) []freeEntry {
	best := map[uint64]uint64{}
	for _, entry := range entries {
		if entry.Ptr < 2 || used[entry.Ptr] {
			continue
		}
		if old, ok := best[entry.Ptr]; !ok || entry.Version > old {
			best[entry.Ptr] = entry.Version
		}
	}
	out := make([]freeEntry, 0, len(best))
	for ptr, version := range best {
		out = append(out, freeEntry{Ptr: ptr, Version: version})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Version == out[j].Version {
			return out[i].Ptr < out[j].Ptr
		}
		return out[i].Version < out[j].Version
	})
	return out
}

func (kv *KV) publish(root uint64, data map[uint64][]byte, obsolete map[uint64]bool) error {
	newVersion := kv.version + 1
	allocator := pageAllocator{
		free:       append([]freeEntry(nil), kv.free...),
		pageCount:  kv.pageCount,
		maxVersion: kv.oldestVersionLocked(),
	}
	// Logical temporary pages are materialized before publish; data keys are
	// already physical pointers chosen by the caller's allocator.
	used := map[uint64]bool{}
	for ptr := range data {
		used[ptr] = true
	}
	entries := append([]freeEntry(nil), allocator.free...)
	for ptr := range obsolete {
		entries = append(entries, freeEntry{Ptr: ptr, Version: newVersion})
	}
	slot := int(newVersion % 2)
	for _, ptr := range kv.metaChains[slot] {
		entries = append(entries, freeEntry{Ptr: ptr, Version: newVersion})
	}
	allocator.free = dedupeFree(entries, used)

	chain := []uint64{}
	for len(chain) < (len(allocator.free)+freeCapacity-1)/freeCapacity {
		ptr := allocator.allocFreeListPage(len(chain))
		used[ptr] = true
		chain = append(chain, ptr)
	}
	allocator.free = dedupeFree(allocator.free, used)

	for ptr, page := range data {
		if len(page) != BTreePageSize {
			return fmt.Errorf("page %d has invalid size %d", ptr, len(page))
		}
		if err := writeFullAt(kv.file, page, int64(ptr*BTreePageSize)); err != nil {
			return err
		}
	}
	for i, ptr := range chain {
		start := i * freeCapacity
		end := start + freeCapacity
		if end > len(allocator.free) {
			end = len(allocator.free)
		}
		var next uint64
		if i+1 < len(chain) {
			next = chain[i+1]
		}
		page := encodeFreePage(next, allocator.free[start:end])
		if err := writeFullAt(kv.file, page, int64(ptr*BTreePageSize)); err != nil {
			return err
		}
	}
	if err := kv.file.Sync(); err != nil { // data-before-root ordering
		return err
	}
	if kv.commitFault != nil {
		if err := kv.commitFault(commitDataSynced); err != nil {
			return err
		}
	}
	meta := diskMeta{
		Version:   newVersion,
		Root:      root,
		PageCount: allocator.pageCount,
		FreeCount: uint64(len(allocator.free)),
		Format:    StorageFormatVersion,
		Slot:      slot,
	}
	if len(chain) > 0 {
		meta.FreeHead = chain[0]
	}
	if err := writeFullAt(kv.file, encodeMeta(meta), int64(slot*BTreePageSize)); err != nil {
		return err
	}
	if kv.commitFault != nil {
		if err := kv.commitFault(commitMetaWritten); err != nil {
			return err
		}
	}
	if err := kv.file.Sync(); err != nil { // durable commit point
		return err
	}
	if kv.commitFault != nil {
		if err := kv.commitFault(commitMetaSynced); err != nil {
			return err
		}
	}
	kv.root = root
	kv.version = newVersion
	kv.format = StorageFormatVersion
	kv.pageCount = allocator.pageCount
	kv.free = allocator.free
	kv.metaChains[slot] = chain
	return nil
}

func writeFullAt(file *os.File, data []byte, offset int64) error {
	for len(data) > 0 {
		n, err := file.WriteAt(data, offset)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
		offset += int64(n)
	}
	return nil
}
