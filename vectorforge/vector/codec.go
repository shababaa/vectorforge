package vector

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// SegmentFloats is the number of float32 values stored per database row.
//
// byodb limits encoded row values to 3,000 bytes, and its order-preserving
// byte-string codec escapes 0x00 and 0x01 as two bytes. A 1,024-byte raw
// segment therefore encodes to at most 2,048 bytes plus framing regardless of
// its bit pattern, whereas a whole 384-d vector (1,536 raw bytes) could
// expand to 3,072 bytes and be rejected.
const SegmentFloats = 256

// MaxDimension bounds the collection dimension to keep a vector to at most 16
// segment rows and make allocation sizes predictable.
const MaxDimension = 16 * SegmentFloats

var ErrCorrupt = errors.New("corrupt vector encoding")

// Encode appends the little-endian IEEE-754 representation of v to dst.
func Encode(dst []byte, v []float32) []byte {
	start := len(dst)
	dst = append(dst, make([]byte, 4*len(v))...)
	out := dst[start:]
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(x))
	}
	return dst
}

// Decode appends the float32 values encoded in b to dst.
func Decode(dst []float32, b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return dst, fmt.Errorf("%w: %d bytes is not a multiple of 4", ErrCorrupt, len(b))
	}
	for i := 0; i < len(b); i += 4 {
		dst = append(dst, math.Float32frombits(binary.LittleEndian.Uint32(b[i:])))
	}
	return dst, nil
}

// SegmentCount returns how many rows a vector of dimension dim occupies.
func SegmentCount(dim int) int {
	return (dim + SegmentFloats - 1) / SegmentFloats
}

// Segment returns the slice of v stored in row seg.
func Segment(v []float32, seg int) []float32 {
	start := seg * SegmentFloats
	end := min(start+SegmentFloats, len(v))
	return v[start:end]
}
