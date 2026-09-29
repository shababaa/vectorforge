package byodb

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPrefixCompressedLeavesSurviveUpdatesAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compressed.db")
	kv, err := OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	var insert KVTX
	if err := kv.Begin(&insert); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3000; index++ {
		key := []byte(fmt.Sprintf("market_candles:SYNTH:1d:%010d", index))
		insert.Set(key, []byte(fmt.Sprintf("ohlcv-row-%010d", index)))
	}
	if err := kv.Commit(&insert); err != nil {
		t.Fatal(err)
	}
	stats, err := kv.KeyCompressionStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.CompressedLeafPages == 0 || stats.BytesSaved < 10_000 || stats.StoredBytes >= stats.LogicalBytes {
		t.Fatalf("compression stats=%+v", stats)
	}
	t.Logf("compression stats: %+v", stats)
	if kv.format != StorageFormatVersion {
		t.Fatalf("format=%d, want %d", kv.format, StorageFormatVersion)
	}

	var remove KVTX
	if err := kv.Begin(&remove); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3000; index += 3 {
		remove.Del(&DeleteReq{Key: []byte(fmt.Sprintf("market_candles:SYNTH:1d:%010d", index))})
	}
	if err := kv.Commit(&remove); err != nil {
		t.Fatal(err)
	}
	if err := kv.Close(); err != nil {
		t.Fatal(err)
	}

	kv, err = OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	var read KVTX
	if err := kv.Begin(&read); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{0, 1, 1499, 2998, 2999} {
		key := []byte(fmt.Sprintf("market_candles:SYNTH:1d:%010d", index))
		value, ok := read.Get(key)
		want := index%3 != 0
		if ok != want {
			t.Fatalf("key %d exists=%t, want %t", index, ok, want)
		}
		if ok && string(value) != fmt.Sprintf("ohlcv-row-%010d", index) {
			t.Fatalf("key %d value=%q", index, value)
		}
	}
	if err := read.snapshot.validate(); err != nil {
		t.Fatal(err)
	}
	kv.Abort(&read)
}

func TestFormatTwoMetadataOpensAndUpgradesOnWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "format-v2.db")
	kv, err := OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := kv.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	for slot := 0; slot < 2; slot++ {
		page := make([]byte, BTreePageSize)
		if _, err := file.ReadAt(page, int64(slot*BTreePageSize)); err != nil {
			t.Fatal(err)
		}
		binary.LittleEndian.PutUint32(page[48:52], 0)
		binary.LittleEndian.PutUint32(page[metaChecksum:metaChecksum+4], checksumPage(page, metaChecksum))
		if err := writeFullAt(file, page, int64(slot*BTreePageSize)); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	kv, err = OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	if kv.format != 2 {
		t.Fatalf("legacy format=%d, want 2", kv.format)
	}
	var tx KVTX
	_ = kv.Begin(&tx)
	tx.Set([]byte("upgrade"), []byte("format-three"))
	if err := kv.Commit(&tx); err != nil {
		t.Fatal(err)
	}
	if kv.format != StorageFormatVersion {
		t.Fatalf("upgraded format=%d, want %d", kv.format, StorageFormatVersion)
	}
}

func TestCompressedRangeIteratorReusesKeys(t *testing.T) {
	const count = 3000
	path := filepath.Join(t.TempDir(), "iter.db")
	kv, err := OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	var insert KVTX
	if err := kv.Begin(&insert); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		key := []byte(fmt.Sprintf("market_candles:SYNTH:1d:%010d", index))
		insert.Set(key, []byte(fmt.Sprintf("ohlcv-row-%010d", index)))
	}
	if err := kv.Commit(&insert); err != nil {
		t.Fatal(err)
	}
	stats, err := kv.KeyCompressionStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.CompressedLeafPages == 0 {
		t.Fatalf("expected compressed leaves, stats=%+v", stats)
	}

	var read KVTX
	if err := kv.Begin(&read); err != nil {
		t.Fatal(err)
	}
	defer kv.Abort(&read)
	assertCompressedScan(t, &read, count, +1)
	assertCompressedScan(t, &read, count, -1)

	// Deref clones the key and value. Rebuilding a compressed key on every
	// iterator step would add another allocation per row.
	limit := float64(count)*2 + 80
	for _, cmp := range []int{CMP_GE, CMP_LE} {
		start := []byte("market_candles:SYNTH:1d:")
		if cmp < 0 {
			start = []byte(fmt.Sprintf("market_candles:SYNTH:1d:%010d", count))
		}
		allocs := testing.AllocsPerRun(8, func() {
			it := read.Seek(start, cmp)
			seen := 0
			for it.Valid() && seen < count {
				_, _ = it.Deref()
				it.Next()
				seen++
			}
			if seen != count {
				panic(fmt.Sprintf("seen=%d", seen))
			}
		})
		if allocs > limit {
			t.Fatalf("cmp=%d allocs/scan=%.0f, limit=%.0f", cmp, allocs, limit)
		}
	}
}

func assertCompressedScan(t *testing.T, tx *KVTX, count int, direction int) {
	t.Helper()
	var it *KVIterator
	if direction > 0 {
		it = tx.Seek([]byte("market_candles:SYNTH:1d:"), CMP_GE)
	} else {
		it = tx.Seek([]byte(fmt.Sprintf("market_candles:SYNTH:1d:%010d", count)), CMP_LT)
	}
	seen := 0
	var previous string
	for it.Valid() {
		key, value := it.Deref()
		keyText, valueText := string(key), string(value)
		wantIndex := seen
		if direction < 0 {
			wantIndex = count - 1 - seen
		}
		wantKey := fmt.Sprintf("market_candles:SYNTH:1d:%010d", wantIndex)
		wantValue := fmt.Sprintf("ohlcv-row-%010d", wantIndex)
		if keyText != wantKey || valueText != wantValue {
			t.Fatalf("direction=%d index=%d key=%q value=%q", direction, seen, keyText, valueText)
		}
		if previous != "" && ((direction > 0 && keyText <= previous) || (direction < 0 && keyText >= previous)) {
			t.Fatalf("direction=%d order %q then %q", direction, previous, keyText)
		}
		it.Next()
		if string(key) != keyText || string(value) != valueText {
			t.Fatal("Deref buffers changed after Next")
		}
		previous = keyText
		seen++
	}
	if seen != count {
		t.Fatalf("direction=%d seen=%d, want %d", direction, seen, count)
	}
}
