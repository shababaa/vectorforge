package byodb

import (
	"bytes"
	"math"
	"testing"
)

func TestOrderPreservingEncodingRoundTrip(t *testing.T) {
	ints := []int64{math.MinInt64, -100, -1, 0, 1, 100, math.MaxInt64}
	var previous []byte
	for _, n := range ints {
		encoded := encodeValues(nil, []Value{Int64Value(n)})
		if previous != nil && bytes.Compare(previous, encoded) >= 0 {
			t.Fatalf("integer encoding is not ordered at %d", n)
		}
		decoded, err := decodeValues(encoded, []uint32{TYPE_INT64})
		if err != nil || decoded[0].I64 != n {
			t.Fatalf("round trip %d=(%v,%v)", n, decoded, err)
		}
		previous = encoded
	}
	stringsInOrder := [][]byte{{}, {0}, {0, 0}, {0, 1}, {1}, []byte("a"), []byte("aa")}
	previous = nil
	for _, value := range stringsInOrder {
		encoded := encodeValues(nil, []Value{BytesValue(value)})
		if previous != nil && bytes.Compare(previous, encoded) >= 0 {
			t.Fatalf("byte encoding is not ordered at %v", value)
		}
		decoded, err := decodeValues(encoded, []uint32{TYPE_BYTES})
		if err != nil || !bytes.Equal(decoded[0].Str, value) {
			t.Fatalf("round trip %v=(%v,%v)", value, decoded, err)
		}
		previous = encoded
	}
}
