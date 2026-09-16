package byodb

import (
	"bytes"
	"fmt"
	"testing"
)

func FuzzCodecRoundTrip(f *testing.F) {
	f.Add([]byte("SYNTH\x00daily"), int64(-42))
	f.Add([]byte{0, 1, 2, 255}, int64(1<<62))
	f.Fuzz(func(t *testing.T, text []byte, number int64) {
		if len(text) > 512 {
			t.Skip()
		}
		values := []Value{BytesValue(text), Int64Value(number)}
		encoded := encodeValues(nil, values)
		decoded, err := decodeValues(encoded, []uint32{TYPE_BYTES, TYPE_INT64})
		if err != nil {
			t.Fatal(err)
		}
		if len(decoded) != 2 || !bytes.Equal(decoded[0].Str, text) || decoded[1].I64 != number {
			t.Fatalf("round trip=%+v", decoded)
		}
	})
}

func FuzzBTreeStateMachine(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte("prefix-compression-and-delete"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 512 {
			t.Skip()
		}
		tree, _ := newMemoryTree()
		reference := map[string]string{}
		for index, operation := range input {
			key := fmt.Sprintf("market:SYNTH:1d:%03d", int(operation)%64)
			if operation%4 == 0 {
				got := tree.delete([]byte(key))
				_, want := reference[key]
				if got != want {
					t.Fatalf("delete %q=%t, want %t", key, got, want)
				}
				delete(reference, key)
			} else {
				value := fmt.Sprintf("value-%d-%d", index, operation)
				tree.insert([]byte(key), []byte(value))
				reference[key] = value
			}
		}
		if err := tree.validate(); err != nil {
			t.Fatal(err)
		}
		for key, expected := range reference {
			value, ok := tree.getValue([]byte(key))
			if !ok || string(value) != expected {
				t.Fatalf("%q=(%q,%t), want %q", key, value, ok, expected)
			}
		}
	})
}

func FuzzParserNeverPanics(f *testing.F) {
	f.Add("SELECT * FROM market_candles;")
	f.Add("DROP TABLE quotes;")
	f.Add("INSERT INTO t (id) VALUES (1), (2);")
	f.Fuzz(func(t *testing.T, query string) {
		if len(query) > 2048 {
			t.Skip()
		}
		_, _ = Parse(query)
	})
}
