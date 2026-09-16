package byodb

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestTablesRangesAndSecondaryIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "table.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	def := &TableDef{
		Name:    "users",
		Cols:    []string{"id", "name", "age"},
		Types:   []uint32{TYPE_INT64, TYPE_BYTES, TYPE_INT64},
		PKeys:   1,
		Indexes: [][]string{{"age", "name"}, {"name"}},
	}
	if err := db.TableNew(def); err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 100; i++ {
		rec := (&Record{}).AddInt64("id", i).AddString("name", fmt.Sprintf("user-%03d", i)).AddInt64("age", 20+i%5)
		if ok, err := db.Insert("users", *rec); err != nil || !ok {
			t.Fatalf("insert %d=(%v,%v)", i, ok, err)
		}
	}
	var tx DBTX
	if err := db.Begin(&tx); err != nil {
		t.Fatal(err)
	}
	sc := Scanner{
		Cmp1: CMP_GE, Key1: *(&Record{}).AddInt64("age", 22),
		Cmp2: CMP_LE, Key2: *(&Record{}).AddInt64("age", 23),
	}
	if err := tx.Scan("users", &sc); err != nil {
		t.Fatal(err)
	}
	count := 0
	for sc.Valid() {
		var rec Record
		if err := sc.Deref(&rec); err != nil {
			t.Fatal(err)
		}
		age := rec.Get("age").I64
		if age < 22 || age > 23 {
			t.Fatalf("age %d outside range", age)
		}
		count++
		sc.Next()
	}
	if count != 40 {
		t.Fatalf("range returned %d rows, want 40", count)
	}
	db.Abort(&tx)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rec := (&Record{}).AddInt64("id", 42)
	if ok, err := db.Get("users", rec); err != nil || !ok || rec.Get("name").String() != "user-042" {
		t.Fatalf("reopened get=(%v,%v,%v)", ok, err, rec)
	}
}

func TestOversizedRelationalValueReturnsError(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "oversized.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.TableNew(&TableDef{Name: "docs", Cols: []string{"id", "body"}, Types: []uint32{TYPE_INT64, TYPE_BYTES}, PKeys: 1, Indexes: [][]string{{"id"}}}); err != nil {
		t.Fatal(err)
	}
	record := (&Record{}).AddInt64("id", 1).AddStr("body", make([]byte, BTreeMaxValSize+1))
	if _, err := db.Insert("docs", *record); err == nil {
		t.Fatal("expected oversized row error")
	}
}
