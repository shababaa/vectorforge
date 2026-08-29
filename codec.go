package byodb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	TYPE_BYTES = 1
	TYPE_INT64 = 2
	TYPE_BOOL  = 3 // expression-only; table schemas use bytes and int64
)

type Value struct {
	Type uint32
	I64  int64
	Str  []byte
	Bool bool
}

func BytesValue(v []byte) Value  { return Value{Type: TYPE_BYTES, Str: cloneBytes(v)} }
func StringValue(v string) Value { return BytesValue([]byte(v)) }
func Int64Value(v int64) Value   { return Value{Type: TYPE_INT64, I64: v} }
func BoolValue(v bool) Value     { return Value{Type: TYPE_BOOL, Bool: v} }

func (v Value) String() string {
	switch v.Type {
	case TYPE_BYTES:
		return string(v.Str)
	case TYPE_INT64:
		return fmt.Sprintf("%d", v.I64)
	case TYPE_BOOL:
		return fmt.Sprintf("%t", v.Bool)
	default:
		return "<invalid>"
	}
}

func valueEqual(a, b Value) bool {
	if a.Type != b.Type {
		return false
	}
	switch a.Type {
	case TYPE_BYTES:
		return bytes.Equal(a.Str, b.Str)
	case TYPE_INT64:
		return a.I64 == b.I64
	case TYPE_BOOL:
		return a.Bool == b.Bool
	default:
		return false
	}
}

type Record struct {
	Cols []string
	Vals []Value
}

func (r *Record) AddStr(col string, val []byte) *Record {
	r.Cols = append(r.Cols, col)
	r.Vals = append(r.Vals, BytesValue(val))
	return r
}

func (r *Record) AddString(col, val string) *Record { return r.AddStr(col, []byte(val)) }

func (r *Record) AddInt64(col string, val int64) *Record {
	r.Cols = append(r.Cols, col)
	r.Vals = append(r.Vals, Int64Value(val))
	return r
}

func (r *Record) Get(col string) *Value {
	for i := range r.Cols {
		if r.Cols[i] == col {
			return &r.Vals[i]
		}
	}
	return nil
}

func (r Record) Clone() Record {
	out := Record{Cols: append([]string(nil), r.Cols...), Vals: make([]Value, len(r.Vals))}
	for i, v := range r.Vals {
		out.Vals[i] = v
		out.Vals[i].Str = cloneBytes(v.Str)
	}
	return out
}

func escapeString(out, in []byte) []byte {
	for _, b := range in {
		switch b {
		case 0:
			out = append(out, 1, 1)
		case 1:
			out = append(out, 1, 2)
		default:
			out = append(out, b)
		}
	}
	return out
}

// encodeValues is order preserving: signed integers have their sign bit
// flipped and are big-endian; byte strings are escaped and NUL terminated.
func encodeValues(out []byte, vals []Value) []byte {
	for _, v := range vals {
		out = append(out, byte(v.Type))
		switch v.Type {
		case TYPE_INT64:
			var buf [8]byte
			binary.BigEndian.PutUint64(buf[:], uint64(v.I64)+(uint64(1)<<63))
			out = append(out, buf[:]...)
		case TYPE_BYTES:
			out = escapeString(out, v.Str)
			out = append(out, 0)
		case TYPE_BOOL:
			if v.Bool {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
		default:
			panic("cannot encode unknown value type")
		}
	}
	return out
}

func decodeOne(in []byte, expected uint32) (Value, []byte, error) {
	if len(in) == 0 || uint32(in[0]) != expected {
		return Value{}, nil, fmt.Errorf("encoded value type mismatch: want %d", expected)
	}
	in = in[1:]
	switch expected {
	case TYPE_INT64:
		if len(in) < 8 {
			return Value{}, nil, errors.New("truncated int64 value")
		}
		u := binary.BigEndian.Uint64(in[:8]) - (uint64(1) << 63)
		return Int64Value(int64(u)), in[8:], nil
	case TYPE_BYTES:
		out := []byte{}
		for i := 0; i < len(in); i++ {
			switch in[i] {
			case 0:
				return BytesValue(out), in[i+1:], nil
			case 1:
				if i+1 >= len(in) || (in[i+1] != 1 && in[i+1] != 2) {
					return Value{}, nil, errors.New("invalid byte-string escape")
				}
				out = append(out, in[i+1]-1)
				i++
			default:
				out = append(out, in[i])
			}
		}
		return Value{}, nil, errors.New("unterminated byte string")
	case TYPE_BOOL:
		if len(in) < 1 || in[0] > 1 {
			return Value{}, nil, errors.New("invalid boolean value")
		}
		return BoolValue(in[0] == 1), in[1:], nil
	default:
		return Value{}, nil, fmt.Errorf("unknown value type %d", expected)
	}
}

func decodeValues(in []byte, types []uint32) ([]Value, error) {
	out := make([]Value, len(types))
	var err error
	for i, typ := range types {
		out[i], in, err = decodeOne(in, typ)
		if err != nil {
			return nil, err
		}
	}
	if len(in) != 0 {
		return nil, errors.New("trailing bytes after encoded values")
	}
	return out, nil
}

func encodeKey(out []byte, prefix uint32, vals []Value) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], prefix)
	out = append(out, buf[:]...)
	return encodeValues(out, vals)
}

func encodeKeyPartial(out []byte, prefix uint32, vals []Value, cmp int) []byte {
	out = encodeKey(out, prefix, vals)
	if cmp == CMP_GT || cmp == CMP_LE {
		out = append(out, 0xff) // unreachable positive infinity for omitted columns
	}
	return out
}

func prefixKey(prefix uint32) []byte { return encodeKey(nil, prefix, nil) }

func keySuccessor(key []byte) []byte {
	out := cloneBytes(key)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] != 0xff {
			out[i]++
			return out[:i+1]
		}
	}
	return nil
}
