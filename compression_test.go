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
