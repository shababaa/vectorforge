package byodb

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestProcessLockAndReadOnlyOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "locked.db")
	writer, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenDB(path); !errors.Is(err, ErrDatabaseLocked) {
		t.Fatalf("second writer error=%v, want ErrDatabaseLocked", err)
	}
	if _, err := OpenDBReadOnly(path); !errors.Is(err, ErrDatabaseLocked) {
		t.Fatalf("reader beside writer error=%v, want ErrDatabaseLocked", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	first, err := OpenDBReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenDBReadOnly(path)
	if err != nil {
		t.Fatalf("read-only handles should share the file lock: %v", err)
	}
	defer second.Close()
	if !first.ReadOnly() {
		t.Fatal("read-only handle did not report its mode")
	}

	var tx DBTX
	if err := first.Begin(&tx); err != nil {
		t.Fatal(err)
	}
	tx.kv.Set([]byte("forbidden"), []byte("write"))
	if err := first.Commit(&tx); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("write commit error=%v, want ErrReadOnly", err)
	}
}

func TestBackupCreatesIndependentReadOnlySnapshot(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "primary.db")
	replica := filepath.Join(directory, "replica.db")
	db, err := OpenDB(source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	def := &TableDef{Name: "quotes", Cols: []string{"symbol", "price"}, Types: []uint32{TYPE_BYTES, TYPE_INT64}, PKeys: 1}
	if err := db.TableNew(def); err != nil {
		t.Fatal(err)
	}
	record := *(&Record{}).AddString("symbol", "SYNTH").AddInt64("price", 123_000_000)
	if _, err := db.Insert("quotes", record); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(replica); err != nil {
		t.Fatal(err)
	}
	if err := db.Backup(replica); err == nil {
		t.Fatal("backup unexpectedly replaced an existing target")
	}

	readOnly, err := OpenDBReadOnly(replica)
	if err != nil {
		t.Fatalf("open replica while primary writer is live: %v", err)
	}
	defer readOnly.Close()
	query := (&Record{}).AddString("symbol", "SYNTH")
	ok, err := readOnly.Get("quotes", query)
	if err != nil || !ok || query.Get("price").I64 != 123_000_000 {
		t.Fatalf("replica row=(%+v,%t,%v)", query, ok, err)
	}
}
