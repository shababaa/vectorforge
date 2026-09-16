package byodb

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

const (
	flagDeleted = byte(1)
	flagUpdated = byte(2)
	tempPageBit = uint64(1) << 63
)

type KeyRange struct {
	Start []byte // nil means -infinity
	Stop  []byte // nil means +infinity; otherwise inclusive
}

type committedTX struct {
	version uint64
	writes  []KeyRange
}

type DeleteReq struct {
	Key []byte
	Old []byte
}

// KVTX holds an immutable disk snapshot plus an in-memory B+tree of pending
// updates. This matches chapter 12 and lets readers and writers proceed without
// blocking one another between Begin and Commit.
type KVTX struct {
	db       *KV
	version  uint64
	snapshot btree
	pending  *btree
	reads    []KeyRange
	writes   []KeyRange
	active   bool
}

func (kv *KV) Begin(tx *KVTX) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if kv.closed || kv.file == nil {
		return ErrClosed
	}
	if tx.active {
		return errors.New("transaction is already active")
	}
	*tx = KVTX{db: kv, version: kv.version, active: true}
	tx.snapshot.root = kv.root
	pageCount := kv.pageCount
	tx.snapshot.get = func(ptr uint64) []byte {
		page, err := kv.readPageBounded(ptr, pageCount)
		if err != nil {
			panic(fmt.Sprintf("database page read failed: %v", err))
		}
		return page
	}
	tx.pending, _ = newMemoryTree()
	kv.ongoing[tx] = tx.version
	return nil
}

func (kv *KV) Commit(tx *KVTX) (err error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if !tx.active || tx.db != kv {
		return errors.New("transaction is not active on this database")
	}
	defer func() {
		delete(kv.ongoing, tx)
		tx.active = false
		kv.trimHistoryLocked()
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("commit failed: %v", recovered)
		}
	}()
	if len(tx.writes) == 0 {
		return nil
	}
	if kv.readOnly {
		return ErrReadOnly
	}
	if kv.detectConflictLocked(tx) {
		return ErrConflict
	}

	work := newCommitWork(kv)
	it := tx.pending.seek(nil, CMP_GE)
	for it.valid() {
		key, tagged := it.deref()
		switch tagged[0] {
		case flagUpdated:
			work.tree.insert(key, tagged[1:])
		case flagDeleted:
			work.tree.delete(key)
		default:
			panic("invalid pending update flag")
		}
		it.next()
	}
	root, pages, obsolete, pageCount, err := work.materialize()
	if err != nil {
		return err
	}
	// publish() performs free-list allocation too. The work allocator has
	// already chosen physical pages, so expose its resulting page count/free set.
	oldCount, oldFree := kv.pageCount, kv.free
	kv.pageCount, kv.free = pageCount, work.alloc.free
	if err := kv.publish(root, pages, obsolete); err != nil {
		kv.pageCount, kv.free = oldCount, oldFree
		return err
	}
	kv.history = append(kv.history, committedTX{version: kv.version, writes: cloneRanges(tx.writes)})
	return nil
}

func (kv *KV) Abort(tx *KVTX) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if tx.active && tx.db == kv {
		delete(kv.ongoing, tx)
		tx.active = false
		kv.trimHistoryLocked()
	}
}

func (tx *KVTX) Get(key []byte) ([]byte, bool) {
	tx.assertActive()
	tx.trackRead(KeyRange{Start: cloneBytes(key), Stop: cloneBytes(key)})
	if tagged, ok := tx.pending.getValue(key); ok {
		switch tagged[0] {
		case flagUpdated:
			return cloneBytes(tagged[1:]), true
		case flagDeleted:
			return nil, false
		default:
			panic("invalid pending update flag")
		}
	}
	return tx.snapshot.getValue(key)
}

func (tx *KVTX) Set(key, val []byte) {
	tx.assertActive()
	must(len(key) > 0 && len(key) <= BTreeMaxKeySize, "invalid KV key size")
	must(len(val) <= BTreeMaxValSize, "invalid KV value size")
	_, _ = tx.Get(key) // writes depend on the previous state
	tagged := make([]byte, 1+len(val))
	tagged[0] = flagUpdated
	copy(tagged[1:], val)
	tx.pending.insert(key, tagged)
	tx.trackWrite(key)
}

func (tx *KVTX) Update(req *UpdateReq) bool {
	tx.assertActive()
	old, exists := tx.Get(req.Key)
	req.Old, req.Added = old, !exists
	switch {
	case req.Mode == MODE_INSERT_ONLY && exists:
		return false
	case req.Mode == MODE_UPDATE_ONLY && !exists:
		return false
	case exists && bytes.Equal(old, req.Val):
		return false
	}
	tx.Set(req.Key, req.Val)
	req.Updated = true
	return true
}

func (tx *KVTX) Del(req *DeleteReq) bool {
	tx.assertActive()
	old, exists := tx.Get(req.Key)
	if !exists {
		return false
	}
	req.Old = old
	tx.pending.insert(req.Key, []byte{flagDeleted})
	tx.trackWrite(req.Key)
	return true
}

// DeleteRange marks at most limit keys in [start, stop) for deletion and
// reports whether another batch remains. Collection happens before mutation so
// the merged snapshot/pending iterator is never invalidated underneath itself.
func (tx *KVTX) DeleteRange(start, stop []byte, limit int) (deleted int, more bool, err error) {
	tx.assertActive()
	if limit <= 0 || limit > MaxDeleteRangeKeys {
		return 0, false, fmt.Errorf("range delete limit must be between 1 and %d", MaxDeleteRangeKeys)
	}
	if stop != nil && start != nil && bytes.Compare(start, stop) >= 0 {
		return 0, false, nil
	}
	tx.TrackRange(start, stop)
	iterator := tx.seekNoTrack(start, CMP_GE)
	keys := make([][]byte, 0, limit)
	for iterator.Valid() && len(keys) < limit {
		key, _ := iterator.Deref()
		if stop != nil && bytes.Compare(key, stop) >= 0 {
			break
		}
		keys = append(keys, key)
		iterator.Next()
	}
	if iterator.Valid() {
		key, _ := iterator.Deref()
		more = stop == nil || bytes.Compare(key, stop) < 0
	}
	for _, key := range keys {
		tx.pending.insert(key, []byte{flagDeleted})
		tx.trackWrite(key)
	}
	return len(keys), more, nil
}

func (tx *KVTX) assertActive() {
	if tx == nil || !tx.active {
		panic("operation on inactive transaction")
	}
}

func (tx *KVTX) trackRead(r KeyRange) { tx.reads = append(tx.reads, r) }

func (tx *KVTX) trackWrite(key []byte) {
	tx.writes = append(tx.writes, KeyRange{Start: cloneBytes(key), Stop: cloneBytes(key)})
}

func (tx *KVTX) TrackRange(start, stop []byte) {
	tx.assertActive()
	tx.trackRead(KeyRange{Start: cloneBytes(start), Stop: cloneBytes(stop)})
}

func (kv *KV) detectConflictLocked(tx *KVTX) bool {
	deps := append(cloneRanges(tx.reads), tx.writes...)
	for i := len(kv.history) - 1; i >= 0; i-- {
		committed := kv.history[i]
		if committed.version <= tx.version {
			break
		}
		if rangesOverlap(deps, committed.writes) {
			return true
		}
	}
	return false
}

func rangesOverlap(a, b []KeyRange) bool {
	for _, x := range a {
		for _, y := range b {
			if rangeOverlaps(x, y) {
				return true
			}
		}
	}
	return false
}

func rangeOverlaps(a, b KeyRange) bool {
	if a.Stop != nil && b.Start != nil && bytes.Compare(a.Stop, b.Start) < 0 {
		return false
	}
	if b.Stop != nil && a.Start != nil && bytes.Compare(b.Stop, a.Start) < 0 {
		return false
	}
	return true
}

func cloneRanges(in []KeyRange) []KeyRange {
	out := make([]KeyRange, len(in))
	for i, r := range in {
		out[i] = KeyRange{Start: cloneBytes(r.Start), Stop: cloneBytes(r.Stop)}
	}
	return out
}

func (kv *KV) trimHistoryLocked() {
	oldest := kv.version
	for _, version := range kv.ongoing {
		if version < oldest {
			oldest = version
		}
	}
	cut := 0
	for cut < len(kv.history) && kv.history[cut].version <= oldest {
		cut++
	}
	if cut > 0 {
		kv.history = append([]committedTX(nil), kv.history[cut:]...)
	}
}

// KVIterator merges the sorted transaction-local updates over the immutable
// snapshot. A pending deletion shadows the corresponding snapshot key.
type KVIterator struct {
	top       *biter
	bottom    *biter
	direction int
	valid     bool
	key       []byte
	val       []byte
}

func (tx *KVTX) Seek(key []byte, cmp int) *KVIterator {
	tx.assertActive()
	if cmp > 0 {
		tx.TrackRange(key, nil)
	} else {
		tx.TrackRange(nil, key)
	}
	return tx.seekNoTrack(key, cmp)
}

func (tx *KVTX) seekNoTrack(key []byte, cmp int) *KVIterator {
	direction := 1
	if cmp < 0 {
		direction = -1
	}
	it := &KVIterator{
		top:       tx.pending.seek(key, cmp),
		bottom:    tx.snapshot.seek(key, cmp),
		direction: direction,
	}
	it.choose()
	return it
}

func (it *KVIterator) Valid() bool { return it != nil && it.valid }

func (it *KVIterator) Deref() ([]byte, []byte) {
	must(it.Valid(), "dereference of invalid KV iterator")
	return cloneBytes(it.key), cloneBytes(it.val)
}

func (it *KVIterator) Next() {
	if !it.valid {
		return
	}
	if it.top.valid() {
		key, _ := it.top.deref()
		if bytes.Equal(key, it.key) {
			it.move(it.top)
		}
	}
	if it.bottom.valid() {
		key, _ := it.bottom.deref()
		if bytes.Equal(key, it.key) {
			it.move(it.bottom)
		}
	}
	it.choose()
}

func (it *KVIterator) move(inner *biter) {
	if it.direction > 0 {
		inner.next()
	} else {
		inner.prev()
	}
}

func (it *KVIterator) choose() {
	for {
		it.valid = false
		topOK, bottomOK := it.top.valid(), it.bottom.valid()
		if !topOK && !bottomOK {
			return
		}
		useTop := topOK
		if topOK && bottomOK {
			topKey, _ := it.top.deref()
			bottomKey, _ := it.bottom.deref()
			cmp := bytes.Compare(topKey, bottomKey) * it.direction
			useTop = cmp <= 0 // pending wins ties
		}
		if useTop {
			key, tagged := it.top.deref()
			if tagged[0] == flagDeleted {
				if bottomOK {
					bottomKey, _ := it.bottom.deref()
					if bytes.Equal(key, bottomKey) {
						it.move(it.bottom)
					}
				}
				it.move(it.top)
				continue
			}
			it.key, it.val, it.valid = key, tagged[1:], true
			return
		}
		key, val := it.bottom.deref()
		it.key, it.val, it.valid = key, val, true
		return
	}
}

type commitWork struct {
	db       *KV
	tree     btree
	temp     map[uint64]bnode
	obsolete map[uint64]bool
	nextTemp uint64
	alloc    pageAllocator
}

func newCommitWork(kv *KV) *commitWork {
	w := &commitWork{
		db:       kv,
		temp:     map[uint64]bnode{},
		obsolete: map[uint64]bool{},
		nextTemp: tempPageBit,
		alloc: pageAllocator{
			free:       append([]freeEntry(nil), kv.free...),
			pageCount:  kv.pageCount,
			maxVersion: kv.oldestVersionLocked(),
		},
	}
	w.tree.root = kv.root
	w.tree.get = func(ptr uint64) []byte {
		if ptr&tempPageBit != 0 {
			n, ok := w.temp[ptr]
			must(ok, "invalid temporary page pointer")
			return n
		}
		page, err := kv.readPageBounded(ptr, kv.pageCount)
		if err != nil {
			panic(err)
		}
		return page
	}
	w.tree.new = func(raw []byte) uint64 {
		ptr := w.nextTemp
		w.nextTemp++
		n := make(bnode, BTreePageSize)
		copy(n, raw)
		w.temp[ptr] = n
		return ptr
	}
	w.tree.del = func(ptr uint64) {
		if ptr&tempPageBit != 0 {
			delete(w.temp, ptr)
		} else {
			w.obsolete[ptr] = true
		}
	}
	return w
}

func (w *commitWork) materialize() (uint64, map[uint64][]byte, map[uint64]bool, uint64, error) {
	pages := map[uint64][]byte{}
	remap := map[uint64]uint64{}
	visiting := map[uint64]bool{}
	var visit func(uint64) (uint64, error)
	visit = func(ptr uint64) (uint64, error) {
		if ptr == 0 || ptr&tempPageBit == 0 {
			return ptr, nil
		}
		if actual, ok := remap[ptr]; ok {
			return actual, nil
		}
		if visiting[ptr] {
			return 0, errors.New("cycle in temporary B+tree pages")
		}
		visiting[ptr] = true
		raw, ok := w.temp[ptr]
		if !ok {
			return 0, fmt.Errorf("temporary page %d is missing", ptr)
		}
		n := make(bnode, BTreePageSize)
		copy(n, raw)
		if n.btype() == bnodeInternal {
			for i := uint16(0); i < n.nkeys(); i++ {
				child, err := visit(n.ptr(i))
				if err != nil {
					return 0, err
				}
				n.setPtr(i, child)
			}
		}
		actual := w.alloc.alloc()
		remap[ptr] = actual
		pages[actual] = n
		delete(visiting, ptr)
		return actual, nil
	}
	root, err := visit(w.tree.root)
	return root, pages, w.obsolete, w.alloc.pageCount, err
}

func sortAndDedupeRanges(ranges []KeyRange) []KeyRange {
	sort.Slice(ranges, func(i, j int) bool {
		return bytes.Compare(ranges[i].Start, ranges[j].Start) < 0
	})
	out := ranges[:0]
	for _, r := range ranges {
		if len(out) == 0 || !bytes.Equal(out[len(out)-1].Start, r.Start) || !bytes.Equal(out[len(out)-1].Stop, r.Stop) {
			out = append(out, r)
		}
	}
	return out
}
