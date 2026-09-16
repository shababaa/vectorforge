package byodb

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// The B+tree node format follows chapters 4 and 5 of the book:
//
//	| type | nkeys | pointers | offsets | key/value pairs | unused |
//	|  2B  |  2B   | n * 8B   | n * 2B |       ...       |        |
//
// Nodes are exactly one disk page after splitting. Updates are copy-on-write;
// allocation is deliberately abstracted behind callbacks so the same tree can
// be backed by memory or by the durable pager.
const (
	btreeHeader     = 4
	BTreePageSize   = 4096
	BTreeMaxKeySize = 1000
	BTreeMaxValSize = 3000

	bnodeInternal = 1
	bnodeLeaf     = 2
	// Compressed leaves store one common key prefix per page and only the
	// suffix in each entry. Types 1 and 2 remain readable for format-v2 files.
	bnodeLeafCompressed = 3
	compressedHeader    = 6
	minCompressedPrefix = 4
)

// Comparison modes used by BTree.Seek and table range scans.
const (
	CMP_GE = +3
	CMP_GT = +2
	CMP_LT = -2
	CMP_LE = -3
)

// Update modes mirror INSERT, UPDATE, and UPSERT semantics.
const (
	MODE_UPSERT      = 0
	MODE_UPDATE_ONLY = 1
	MODE_INSERT_ONLY = 2
	MODE_DELETE      = 3
)

type bnode []byte

func (n bnode) rawType() uint16 { return binary.LittleEndian.Uint16(n[0:2]) }
func (n bnode) btype() uint16 {
	if n.rawType() == bnodeLeafCompressed {
		return bnodeLeaf
	}
	return n.rawType()
}
func (n bnode) nkeys() uint16 { return binary.LittleEndian.Uint16(n[2:4]) }

func (n bnode) compressed() bool { return n.rawType() == bnodeLeafCompressed }

func (n bnode) prefix() []byte {
	if !n.compressed() {
		return nil
	}
	length := int(binary.LittleEndian.Uint16(n[4:6]))
	must(compressedHeader+length <= len(n), "compressed B+tree prefix out of range")
	return n[compressedHeader : compressedHeader+length]
}

func (n bnode) headerSize() int {
	if !n.compressed() {
		return btreeHeader
	}
	return compressedHeader + len(n.prefix())
}

func (n bnode) setHeader(kind, keys uint16) {
	binary.LittleEndian.PutUint16(n[0:2], kind)
	binary.LittleEndian.PutUint16(n[2:4], keys)
}

func (n bnode) setCompressedHeader(keys uint16, prefix []byte) {
	n.setHeader(bnodeLeafCompressed, keys)
	binary.LittleEndian.PutUint16(n[4:6], uint16(len(prefix)))
	copy(n[compressedHeader:], prefix)
}

func (n bnode) ptr(idx uint16) uint64 {
	must(idx < n.nkeys(), "B+tree child pointer out of range")
	pos := n.headerSize() + 8*int(idx)
	return binary.LittleEndian.Uint64(n[pos : pos+8])
}

func (n bnode) setPtr(idx uint16, ptr uint64) {
	must(idx < n.nkeys(), "B+tree child pointer out of range")
	pos := n.headerSize() + 8*int(idx)
	binary.LittleEndian.PutUint64(n[pos:pos+8], ptr)
}

func (n bnode) offsetPos(idx uint16) int {
	must(idx >= 1 && idx <= n.nkeys(), "B+tree offset out of range")
	return n.headerSize() + 8*int(n.nkeys()) + 2*int(idx-1)
}

func (n bnode) offset(idx uint16) uint16 {
	if idx == 0 {
		return 0
	}
	pos := n.offsetPos(idx)
	return binary.LittleEndian.Uint16(n[pos : pos+2])
}

func (n bnode) setOffset(idx, off uint16) {
	pos := n.offsetPos(idx)
	binary.LittleEndian.PutUint16(n[pos:pos+2], off)
}

func (n bnode) kvPos(idx uint16) int {
	must(idx <= n.nkeys(), "B+tree key/value position out of range")
	return n.headerSize() + 10*int(n.nkeys()) + int(n.offset(idx))
}

func (n bnode) key(idx uint16) []byte {
	must(idx < n.nkeys(), "B+tree key out of range")
	prefix, suffix := n.keyParts(idx)
	if !n.compressed() {
		return suffix
	}
	key := make([]byte, len(prefix)+len(suffix))
	copy(key, prefix)
	copy(key[len(prefix):], suffix)
	return key
}

func (n bnode) keyParts(idx uint16) ([]byte, []byte) {
	must(idx < n.nkeys(), "B+tree key out of range")
	pos := n.kvPos(idx)
	length := int(binary.LittleEndian.Uint16(n[pos : pos+2]))
	return n.prefix(), n[pos+4 : pos+4+length]
}

func (n bnode) keyLen(idx uint16) int {
	prefix, suffix := n.keyParts(idx)
	return len(prefix) + len(suffix)
}

func keyByte(prefix, suffix []byte, index int) byte {
	if index < len(prefix) {
		return prefix[index]
	}
	return suffix[index-len(prefix)]
}

func compareNodeKey(node bnode, index uint16, key []byte) int {
	prefix, suffix := node.keyParts(index)
	if len(prefix) == 0 {
		return bytes.Compare(suffix, key)
	}
	length := len(prefix) + len(suffix)
	limit := length
	if len(key) < limit {
		limit = len(key)
	}
	for at := 0; at < limit; at++ {
		left := keyByte(prefix, suffix, at)
		if left < key[at] {
			return -1
		}
		if left > key[at] {
			return 1
		}
	}
	if length < len(key) {
		return -1
	}
	if length > len(key) {
		return 1
	}
	return 0
}

func equalNodeKey(node bnode, index uint16, key []byte) bool {
	prefix, suffix := node.keyParts(index)
	if len(prefix) == 0 {
		return bytes.Equal(suffix, key)
	}
	return len(prefix)+len(suffix) == len(key) && compareNodeKey(node, index, key) == 0
}

func commonNodePrefixLength(node bnode, left, right uint16) int {
	leftPrefix, leftSuffix := node.keyParts(left)
	rightPrefix, rightSuffix := node.keyParts(right)
	limit := len(leftPrefix) + len(leftSuffix)
	if other := len(rightPrefix) + len(rightSuffix); other < limit {
		limit = other
	}
	index := 0
	for index < limit && keyByte(leftPrefix, leftSuffix, index) == keyByte(rightPrefix, rightSuffix, index) {
		index++
	}
	return index
}

func nodeKeyPrefix(node bnode, index uint16, length int) []byte {
	prefix, suffix := node.keyParts(index)
	out := make([]byte, length)
	written := copy(out, prefix)
	if written < length {
		copy(out[written:], suffix[:length-written])
	}
	return out
}

func (n bnode) val(idx uint16) []byte {
	must(idx < n.nkeys(), "B+tree value out of range")
	pos := n.kvPos(idx)
	klen := int(binary.LittleEndian.Uint16(n[pos : pos+2]))
	vlen := int(binary.LittleEndian.Uint16(n[pos+2 : pos+4]))
	return n[pos+4+klen : pos+4+klen+vlen]
}

func (n bnode) nbytes() int { return n.kvPos(n.nkeys()) }

// btree is intentionally small: the pager and in-memory transaction tree both
// provide these three callbacks.
type btree struct {
	root uint64
	get  func(uint64) []byte
	new  func([]byte) uint64
	del  func(uint64)
}

// UpdateReq reports whether an update inserted or changed a key and preserves
// the old value, which is required to maintain secondary indexes.
type UpdateReq struct {
	Key     []byte
	Val     []byte
	Mode    int
	Added   bool
	Updated bool
	Old     []byte
}

func nodeLookupLE(n bnode, key []byte) uint16 {
	keys := n.nkeys()
	must(keys > 0, "lookup in an empty B+tree node")
	lo, hi := 0, int(keys)
	for lo < hi {
		mid := lo + (hi-lo)/2
		if compareNodeKey(n, uint16(mid), key) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == 0 {
		return 0 // the sentinel makes this case unreachable for valid user keys
	}
	return uint16(lo - 1)
}

func nodeAppendKV(dst bnode, idx uint16, ptr uint64, key, val []byte) {
	dst.setPtr(idx, ptr)
	if dst.compressed() {
		prefix := dst.prefix()
		must(bytes.HasPrefix(key, prefix), "compressed B+tree key does not share its node prefix")
		key = key[len(prefix):]
	}
	pos := dst.kvPos(idx)
	binary.LittleEndian.PutUint16(dst[pos:pos+2], uint16(len(key)))
	binary.LittleEndian.PutUint16(dst[pos+2:pos+4], uint16(len(val)))
	copy(dst[pos+4:], key)
	copy(dst[pos+4+len(key):], val)
	dst.setOffset(idx+1, dst.offset(idx)+uint16(4+len(key)+len(val)))
}

func nodeAppendRange(dst, src bnode, dstAt, srcAt, count uint16) {
	for i := uint16(0); i < count; i++ {
		s := srcAt + i
		nodeAppendKVFrom(dst, dstAt+i, src, s)
	}
}

func nodeAppendKVFrom(dst bnode, dstIndex uint16, src bnode, srcIndex uint16) {
	dst.setPtr(dstIndex, src.ptr(srcIndex))
	sourcePrefix, sourceSuffix := src.keyParts(srcIndex)
	strip := len(dst.prefix())
	keyLength := len(sourcePrefix) + len(sourceSuffix) - strip
	value := src.val(srcIndex)
	position := dst.kvPos(dstIndex)
	binary.LittleEndian.PutUint16(dst[position:position+2], uint16(keyLength))
	binary.LittleEndian.PutUint16(dst[position+2:position+4], uint16(len(value)))
	keyOut := dst[position+4 : position+4+keyLength]
	written := 0
	if strip < len(sourcePrefix) {
		written = copy(keyOut, sourcePrefix[strip:])
		strip = 0
	} else {
		strip -= len(sourcePrefix)
	}
	copy(keyOut[written:], sourceSuffix[strip:])
	copy(dst[position+4+keyLength:], value)
	dst.setOffset(dstIndex+1, dst.offset(dstIndex)+uint16(4+keyLength+len(value)))
}

func leafInsert(dst, old bnode, idx uint16, key, val []byte) {
	if old.compressed() && bytes.HasPrefix(key, old.prefix()) {
		dst.setCompressedHeader(old.nkeys()+1, old.prefix())
	} else {
		dst.setHeader(bnodeLeaf, old.nkeys()+1)
	}
	nodeAppendRange(dst, old, 0, 0, idx)
	nodeAppendKV(dst, idx, 0, key, val)
	nodeAppendRange(dst, old, idx+1, idx, old.nkeys()-idx)
}

func leafUpdate(dst, old bnode, idx uint16, key, val []byte) {
	if old.compressed() && bytes.HasPrefix(key, old.prefix()) {
		dst.setCompressedHeader(old.nkeys(), old.prefix())
	} else {
		dst.setHeader(bnodeLeaf, old.nkeys())
	}
	nodeAppendRange(dst, old, 0, 0, idx)
	nodeAppendKV(dst, idx, 0, key, val)
	nodeAppendRange(dst, old, idx+1, idx+1, old.nkeys()-idx-1)
}

func nodeReplaceKids(tree *btree, dst, old bnode, idx uint16, kids ...bnode) {
	inc := uint16(len(kids))
	must(inc > 0, "B+tree child replacement requires a child")
	dst.setHeader(bnodeInternal, old.nkeys()+inc-1)
	nodeAppendRange(dst, old, 0, 0, idx)
	for i, kid := range kids {
		nodeAppendKV(dst, idx+uint16(i), tree.new(kid), kid.key(0), nil)
	}
	nodeAppendRange(dst, old, idx+inc, idx+1, old.nkeys()-idx-1)
}

func buildNodeRange(dst, old bnode, start, count uint16) {
	prefix := commonNodePrefix(old, start, count)
	if old.btype() == bnodeLeaf && len(prefix) >= minCompressedPrefix && compressedNodeRangeBytes(old, start, count, prefix) < plainNodeRangeBytes(old, start, count) {
		dst.setCompressedHeader(count, prefix)
	} else {
		dst.setHeader(old.btype(), count)
	}
	nodeAppendRange(dst, old, 0, start, count)
}

func plainNodeRangeBytes(node bnode, start, count uint16) int {
	total := btreeHeader + 10*int(count)
	for index := uint16(0); index < count; index++ {
		at := start + index
		total += 4 + node.keyLen(at) + len(node.val(at))
	}
	return total
}

func commonNodePrefix(node bnode, start, count uint16) []byte {
	if node.btype() != bnodeLeaf || count < 2 {
		return nil
	}
	length := commonNodePrefixLength(node, start, start+count-1)
	return nodeKeyPrefix(node, start, length)
}

func compressedNodeRangeBytes(node bnode, start, count uint16, prefix []byte) int {
	total := compressedHeader + len(prefix) + 10*int(count)
	for index := uint16(0); index < count; index++ {
		at := start + index
		total += 4 + node.keyLen(at) - len(prefix) + len(node.val(at))
	}
	return total
}

func nodeRangeBytes(node bnode, start, count uint16) int {
	plain := plainNodeRangeBytes(node, start, count)
	prefix := commonNodePrefix(node, start, count)
	if len(prefix) < minCompressedPrefix {
		return plain
	}
	compressed := compressedNodeRangeBytes(node, start, count, prefix)
	if compressed < plain {
		return compressed
	}
	return plain
}

func compactNode(node bnode) bnode {
	size := nodeRangeBytes(node, 0, node.nkeys())
	must(size <= BTreePageSize, "compacted B+tree node exceeds one page")
	out := make(bnode, BTreePageSize)
	buildNodeRange(out, node, 0, node.nkeys())
	return out
}

// nodeSplit2 picks a boundary by encoded byte size, not key count. This is
// essential because the book intentionally permits variable-sized values.
func nodeSplit2(left, right, old bnode) {
	must(old.nkeys() >= 2, "cannot split a one-key node")
	var chosen, fallback uint16
	best, fallbackBest := int(^uint(0)>>1), int(^uint(0)>>1)
	// Prefer a true two-way split. Computing encoded sizes avoids rebuilding
	// two temporary nodes for every possible boundary, which was the dominant
	// allocation source in sequential time-series inserts.
	for nleft := uint16(1); nleft < old.nkeys(); nleft++ {
		leftBytes := nodeRangeBytes(old, 0, nleft)
		rightBytes := nodeRangeBytes(old, nleft, old.nkeys()-nleft)
		if rightBytes > BTreePageSize || leftBytes > len(left) {
			continue
		}
		score := leftBytes
		if rightBytes > score {
			score = rightBytes
		}
		if score < fallbackBest {
			fallback, fallbackBest = nleft, score
		}
		if leftBytes <= BTreePageSize && score < best {
			chosen, best = nleft, score
		}
	}
	if chosen == 0 {
		chosen = fallback
	}
	must(chosen != 0, "a B+tree key/value exceeds one page")
	must(nodeRangeBytes(old, 0, chosen) <= len(left), "left split destination is too small")
	must(nodeRangeBytes(old, chosen, old.nkeys()-chosen) <= len(right), "right split destination is too small")
	buildNodeRange(left, old, 0, chosen)
	buildNodeRange(right, old, chosen, old.nkeys()-chosen)
}

func nodeSplit3(old bnode) (uint16, [3]bnode) {
	if old.nbytes() <= BTreePageSize {
		one := make(bnode, BTreePageSize)
		copy(one, old[:old.nbytes()])
		return 1, [3]bnode{one}
	}
	if nodeRangeBytes(old, 0, old.nkeys()) <= BTreePageSize {
		return 1, [3]bnode{compactNode(old)}
	}
	left := make(bnode, 2*BTreePageSize)
	right := make(bnode, BTreePageSize)
	nodeSplit2(left, right, old)
	if left.nbytes() <= BTreePageSize {
		trimmed := make(bnode, BTreePageSize)
		copy(trimmed, left[:left.nbytes()])
		return 2, [3]bnode{trimmed, right}
	}
	leftLeft := make(bnode, BTreePageSize)
	middle := make(bnode, BTreePageSize)
	nodeSplit2(leftLeft, middle, left)
	must(leftLeft.nbytes() <= BTreePageSize, "failed to split B+tree node")
	return 3, [3]bnode{leftLeft, middle, right}
}

func treeInsert(tree *btree, old bnode, key, val []byte) bnode {
	idx := nodeLookupLE(old, key)
	switch old.btype() {
	case bnodeLeaf:
		// A compressed leaf may represent more than 8 KiB of logical full keys.
		// Size the transient expanded node from its contents instead of assuming
		// that two physical pages are always enough.
		size := plainNodeRangeBytes(old, 0, old.nkeys()) + 14 + len(key) + len(val)
		if old.compressed() && bytes.HasPrefix(key, old.prefix()) {
			size = old.nbytes() + 14 + len(key) - len(old.prefix()) + len(val)
		}
		dst := make(bnode, size)
		if equalNodeKey(old, idx, key) {
			leafUpdate(dst, old, idx, key, val)
		} else {
			leafInsert(dst, old, idx+1, key, val)
		}
		return dst
	case bnodeInternal:
		dst := make(bnode, 2*BTreePageSize)
		ptr := old.ptr(idx)
		kid := treeInsert(tree, bnode(tree.get(ptr)), key, val)
		count, split := nodeSplit3(kid)
		tree.del(ptr)
		nodeReplaceKids(tree, dst, old, idx, split[:count]...)
		return dst
	default:
		panic("invalid B+tree node type")
	}
}

func (tree *btree) getValue(key []byte) ([]byte, bool) {
	if tree.root == 0 {
		return nil, false
	}
	ptr := tree.root
	for ptr != 0 {
		n := bnode(tree.get(ptr))
		idx := nodeLookupLE(n, key)
		if n.btype() == bnodeLeaf {
			if equalNodeKey(n, idx, key) && len(key) != 0 {
				return cloneBytes(n.val(idx)), true
			}
			return nil, false
		}
		ptr = n.ptr(idx)
	}
	return nil, false
}

func (tree *btree) insert(key, val []byte) {
	must(len(key) > 0, "empty keys are reserved for the B+tree sentinel")
	must(len(key) <= BTreeMaxKeySize, "B+tree key too large")
	// Transaction-local trees prefix values with a one-byte update/delete flag.
	// Durable user values are still checked against BTreeMaxValSize by KVTX.
	must(len(val) <= BTreeMaxValSize+1, "B+tree value too large")
	if tree.root == 0 {
		root := make(bnode, BTreePageSize)
		root.setHeader(bnodeLeaf, 2)
		nodeAppendKV(root, 0, 0, nil, nil)
		nodeAppendKV(root, 1, 0, key, val)
		tree.root = tree.new(root)
		return
	}
	updated := treeInsert(tree, bnode(tree.get(tree.root)), key, val)
	count, split := nodeSplit3(updated)
	tree.del(tree.root)
	if count == 1 {
		tree.root = tree.new(split[0])
		return
	}
	root := make(bnode, BTreePageSize)
	root.setHeader(bnodeInternal, count)
	for i, kid := range split[:count] {
		nodeAppendKV(root, uint16(i), tree.new(kid), kid.key(0), nil)
	}
	tree.root = tree.new(root)
}

func (tree *btree) update(req *UpdateReq) bool {
	old, exists := tree.getValue(req.Key)
	req.Added = !exists
	req.Old = old
	switch {
	case req.Mode == MODE_INSERT_ONLY && exists:
		return false
	case req.Mode == MODE_UPDATE_ONLY && !exists:
		return false
	case exists && bytes.Equal(old, req.Val):
		return false
	}
	tree.insert(req.Key, req.Val)
	req.Updated = true
	return true
}

func leafDelete(dst, old bnode, idx uint16) {
	dst.setHeader(bnodeLeaf, old.nkeys()-1)
	nodeAppendRange(dst, old, 0, 0, idx)
	nodeAppendRange(dst, old, idx, idx+1, old.nkeys()-idx-1)
}

func nodeMerge(dst, left, right bnode) {
	must(left.btype() == right.btype(), "cannot merge different B+tree node types")
	dst.setHeader(left.btype(), left.nkeys()+right.nkeys())
	nodeAppendRange(dst, left, 0, 0, left.nkeys())
	nodeAppendRange(dst, right, left.nkeys(), 0, right.nkeys())
}

func mergedNodeBytes(left, right bnode) int {
	count := left.nkeys() + right.nkeys()
	plain := btreeHeader + 10*int(count)
	for _, node := range []bnode{left, right} {
		for index := uint16(0); index < node.nkeys(); index++ {
			plain += 4 + node.keyLen(index) + len(node.val(index))
		}
	}
	if left.btype() != bnodeLeaf || count < 2 {
		return plain
	}
	firstNode, firstIndex := left, uint16(0)
	if left.nkeys() == 0 {
		firstNode = right
	}
	lastNode, lastIndex := right, right.nkeys()-1
	if right.nkeys() == 0 {
		lastNode, lastIndex = left, left.nkeys()-1
	}
	firstPrefix, firstSuffix := firstNode.keyParts(firstIndex)
	lastPrefix, lastSuffix := lastNode.keyParts(lastIndex)
	limit := len(firstPrefix) + len(firstSuffix)
	if other := len(lastPrefix) + len(lastSuffix); other < limit {
		limit = other
	}
	index := 0
	for index < limit && keyByte(firstPrefix, firstSuffix, index) == keyByte(lastPrefix, lastSuffix, index) {
		index++
	}
	if index < minCompressedPrefix {
		return plain
	}
	compressed := plain + (compressedHeader - btreeHeader) + index - int(count)*index
	if compressed < plain {
		return compressed
	}
	return plain
}

func nodeReplaceTwoKids(dst, old bnode, idx uint16, ptr uint64, key []byte) {
	dst.setHeader(bnodeInternal, old.nkeys()-1)
	nodeAppendRange(dst, old, 0, 0, idx)
	nodeAppendKV(dst, idx, ptr, key, nil)
	nodeAppendRange(dst, old, idx+1, idx+2, old.nkeys()-idx-2)
}

func shouldMerge(tree *btree, parent bnode, idx uint16, updated bnode) (int, bnode) {
	if updated.nbytes() > BTreePageSize/4 {
		return 0, nil
	}
	if idx > 0 {
		left := bnode(tree.get(parent.ptr(idx - 1)))
		if mergedNodeBytes(left, updated) <= BTreePageSize {
			return -1, left
		}
	}
	if idx+1 < parent.nkeys() {
		right := bnode(tree.get(parent.ptr(idx + 1)))
		if mergedNodeBytes(updated, right) <= BTreePageSize {
			return +1, right
		}
	}
	return 0, nil
}

func treeDelete(tree *btree, old bnode, key []byte) bnode {
	idx := nodeLookupLE(old, key)
	switch old.btype() {
	case bnodeLeaf:
		if old.keyLen(idx) == 0 || !equalNodeKey(old, idx, key) {
			return nil
		}
		dst := make(bnode, plainNodeRangeBytes(old, 0, old.nkeys()))
		leafDelete(dst, old, idx)
		return compactNode(dst)
	case bnodeInternal:
		return nodeDelete(tree, old, idx, key)
	default:
		panic("invalid B+tree node type")
	}
}

func nodeDelete(tree *btree, old bnode, idx uint16, key []byte) bnode {
	ptr := old.ptr(idx)
	updated := treeDelete(tree, bnode(tree.get(ptr)), key)
	if len(updated) == 0 {
		return nil
	}
	tree.del(ptr)
	dst := make(bnode, BTreePageSize)
	direction, sibling := shouldMerge(tree, old, idx, updated)
	switch {
	case direction < 0:
		expanded := make(bnode, plainNodeRangeBytes(sibling, 0, sibling.nkeys())+plainNodeRangeBytes(updated, 0, updated.nkeys()))
		nodeMerge(expanded, sibling, updated)
		merged := compactNode(expanded)
		tree.del(old.ptr(idx - 1))
		nodeReplaceTwoKids(dst, old, idx-1, tree.new(merged), merged.key(0))
	case direction > 0:
		expanded := make(bnode, plainNodeRangeBytes(updated, 0, updated.nkeys())+plainNodeRangeBytes(sibling, 0, sibling.nkeys()))
		nodeMerge(expanded, updated, sibling)
		merged := compactNode(expanded)
		tree.del(old.ptr(idx + 1))
		nodeReplaceTwoKids(dst, old, idx, tree.new(merged), merged.key(0))
	case updated.nkeys() == 0:
		must(old.nkeys() == 1 && idx == 0, "empty B+tree child unexpectedly has siblings")
		dst.setHeader(bnodeInternal, 0)
	default:
		nodeReplaceKids(tree, dst, old, idx, updated)
	}
	return dst
}

func (tree *btree) delete(key []byte) bool {
	if tree.root == 0 || len(key) == 0 {
		return false
	}
	updated := treeDelete(tree, bnode(tree.get(tree.root)), key)
	if len(updated) == 0 {
		return false
	}
	tree.del(tree.root)
	if updated.btype() == bnodeInternal && updated.nkeys() == 1 {
		tree.root = updated.ptr(0)
	} else {
		tree.root = tree.new(updated)
	}
	return true
}

// biter stores the entire root-to-leaf path, exactly as described in chapter 9.
type biter struct {
	tree *btree
	path []bnode
	pos  []uint16
}

func (tree *btree) seekLE(key []byte) *biter {
	it := &biter{tree: tree}
	for ptr := tree.root; ptr != 0; {
		n := bnode(tree.get(ptr))
		if n.nkeys() == 0 {
			break
		}
		idx := nodeLookupLE(n, key)
		it.path = append(it.path, n)
		it.pos = append(it.pos, idx)
		if n.btype() == bnodeLeaf {
			break
		}
		ptr = n.ptr(idx)
	}
	return it
}

func (tree *btree) seek(key []byte, cmp int) *biter {
	it := tree.seekLE(key)
	if !it.rawValid() {
		return it
	}
	k, _ := it.rawDeref()
	switch cmp {
	case CMP_GE:
		if bytes.Compare(k, key) < 0 || len(k) == 0 {
			it.next()
		}
	case CMP_GT:
		if bytes.Compare(k, key) <= 0 {
			it.next()
		}
	case CMP_LE:
		// seekLE already has the desired position.
	case CMP_LT:
		if bytes.Compare(k, key) >= 0 {
			it.prev()
		}
	default:
		panic("invalid B+tree comparison mode")
	}
	return it
}

func (it *biter) rawValid() bool {
	if len(it.path) == 0 {
		return false
	}
	last := len(it.path) - 1
	return it.path[last].btype() == bnodeLeaf && it.pos[last] < it.path[last].nkeys()
}

func (it *biter) valid() bool {
	if !it.rawValid() {
		return false
	}
	k, _ := it.rawDeref()
	return len(k) != 0 // hide the sentinel
}

func (it *biter) rawDeref() ([]byte, []byte) {
	last := len(it.path) - 1
	n := it.path[last]
	idx := it.pos[last]
	return n.key(idx), n.val(idx)
}

func (it *biter) deref() ([]byte, []byte) {
	must(it.valid(), "dereference of invalid B+tree iterator")
	k, v := it.rawDeref()
	return k, v
}

func (it *biter) next() {
	if len(it.path) == 0 {
		return
	}
	last := len(it.path) - 1
	leaf := it.path[last]
	if it.pos[last]+1 < leaf.nkeys() {
		it.pos[last]++
		return
	}
	for level := last - 1; level >= 0; level-- {
		if it.pos[level]+1 >= it.path[level].nkeys() {
			continue
		}
		it.pos[level]++
		ptr := it.path[level].ptr(it.pos[level])
		for down := level + 1; down <= last; down++ {
			n := bnode(it.tree.get(ptr))
			it.path[down] = n
			it.pos[down] = 0
			if n.btype() == bnodeInternal {
				ptr = n.ptr(0)
			}
		}
		return
	}
	it.pos[last] = leaf.nkeys() // after the final key
}

func (it *biter) prev() {
	if len(it.path) == 0 {
		return
	}
	last := len(it.path) - 1
	if it.pos[last] > 0 && it.pos[last] <= it.path[last].nkeys() {
		it.pos[last]--
		return
	}
	for level := last - 1; level >= 0; level-- {
		if it.pos[level] == 0 {
			continue
		}
		it.pos[level]--
		ptr := it.path[level].ptr(it.pos[level])
		for down := level + 1; down <= last; down++ {
			n := bnode(it.tree.get(ptr))
			it.path[down] = n
			it.pos[down] = n.nkeys() - 1
			if n.btype() == bnodeInternal {
				ptr = n.ptr(it.pos[down])
			}
		}
		return
	}
	it.pos[last] = 0 // sentinel; public Valid reports false
}

func (tree *btree) validate() error {
	if tree.root == 0 {
		return nil
	}
	leafDepth := -1
	var walk func(uint64, int, []byte, []byte) error
	walk = func(ptr uint64, depth int, low, high []byte) error {
		n := bnode(tree.get(ptr))
		if n.nbytes() > BTreePageSize || n.nkeys() == 0 {
			return fmt.Errorf("invalid node size/key count at page %d", ptr)
		}
		for i := uint16(1); i < n.nkeys(); i++ {
			if bytes.Compare(n.key(i-1), n.key(i)) >= 0 {
				return fmt.Errorf("unsorted keys at page %d", ptr)
			}
		}
		if low != nil && bytes.Compare(n.key(0), low) < 0 {
			return fmt.Errorf("page %d is below parent range", ptr)
		}
		if high != nil && bytes.Compare(n.key(n.nkeys()-1), high) >= 0 {
			return fmt.Errorf("page %d is above parent range", ptr)
		}
		if n.btype() == bnodeLeaf {
			if leafDepth < 0 {
				leafDepth = depth
			} else if leafDepth != depth {
				return fmt.Errorf("B+tree leaves have different depths")
			}
			return nil
		}
		for i := uint16(0); i < n.nkeys(); i++ {
			var upper []byte
			if i+1 < n.nkeys() {
				upper = n.key(i + 1)
			} else {
				upper = high
			}
			if err := walk(n.ptr(i), depth+1, n.key(i), upper); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(tree.root, 0, nil, nil)
}

func newMemoryTree() (*btree, map[uint64]bnode) {
	pages := map[uint64]bnode{}
	var next uint64 = 1
	tree := &btree{}
	tree.get = func(ptr uint64) []byte {
		n, ok := pages[ptr]
		must(ok, "invalid in-memory B+tree pointer")
		return n
	}
	tree.new = func(raw []byte) uint64 {
		n := make(bnode, BTreePageSize)
		copy(n, raw)
		ptr := next
		next++
		pages[ptr] = n
		return ptr
	}
	tree.del = func(ptr uint64) {
		must(pages[ptr] != nil, "double free in in-memory B+tree")
		delete(pages, ptr)
	}
	return tree, pages
}

func must(ok bool, msg string) {
	if !ok {
		panic(msg)
	}
}

func cloneBytes(in []byte) []byte {
	if in == nil {
		return nil
	}
	out := make([]byte, len(in))
	copy(out, in)
	return out
}
