package byodb

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestBatchRangeDeleteAndTableDrop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "maintenance.db")
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	def := &TableDef{
		Name: "events", Cols: []string{"id", "series", "payload"},
		Types: []uint32{TYPE_INT64, TYPE_BYTES, TYPE_BYTES}, PKeys: 1,
		Indexes: [][]string{{"series"}},
	}
	if err := db.TableNew(def); err != nil {
		t.Fatal(err)
	}
	stored, _ := db.Table("events")
	oldPrefix := stored.Prefixes[len(stored.Prefixes)-1]
	mutations := make([]Mutation, 25)
	for index := range mutations {
		mutations[index] = Mutation{Table: "events", Mode: MODE_INSERT_ONLY, Record: *(&Record{}).
			AddInt64("id", int64(index)).AddString("series", "SYNTH:1d").AddString("payload", fmt.Sprintf("row-%02d", index))}
	}
	if changed, err := db.ApplyBatch(mutations); err != nil || changed != len(mutations) {
		t.Fatalf("batch=(%d,%v)", changed, err)
	}

	scan := Scanner{Cmp1: CMP_GE, Key1: *(&Record{}).AddInt64("id", 5), Cmp2: CMP_LE, Key2: *(&Record{}).AddInt64("id", 14)}
	want := []DeleteRangeResult{{Deleted: 4, More: true}, {Deleted: 4, More: true}, {Deleted: 2, More: false}}
	for iteration, expected := range want {
		result, err := db.DeleteRange("events", scan, 4)
		if err != nil || result != expected {
			t.Fatalf("delete batch %d=(%+v,%v), want %+v", iteration, result, err, expected)
		}
	}
	var read DBTX
	if err := db.Begin(&read); err != nil {
		t.Fatal(err)
	}
	remaining := 0
	var all Scanner
	if err := read.Scan("events", &all); err != nil {
		t.Fatal(err)
	}
	for all.Valid() {
		remaining++
		all.Next()
	}
	db.Abort(&read)
	if remaining != 15 {
		t.Fatalf("remaining rows=%d, want 15", remaining)
	}

	physical, err := db.TableDrop("events")
	if err != nil || physical != 30 { // one primary and one secondary key per row
		t.Fatalf("drop=(%d,%v), want 30 physical keys", physical, err)
	}
	if _, ok := db.Table("events"); ok {
		t.Fatal("dropped table remained in the in-memory catalog")
	}
	replacement := &TableDef{Name: "replacement", Cols: []string{"id"}, Types: []uint32{TYPE_INT64}, PKeys: 1}
	if err := db.TableNew(replacement); err != nil {
		t.Fatal(err)
	}
	newDef, _ := db.Table("replacement")
	if newDef.Prefixes[0] <= oldPrefix {
		t.Fatalf("dropped prefix was reused: old=%d new=%d", oldPrefix, newDef.Prefixes[0])
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, ok := db.Table("events"); ok {
		t.Fatal("dropped table returned after recovery")
	}
}

func TestApplyBatchAbortsAtomicallyAndDropSQL(t *testing.T) {
	db, err := OpenDB(filepath.Join(t.TempDir(), "batch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE items (id INT64, value BYTES, PRIMARY KEY (id));"); err != nil {
		t.Fatal(err)
	}
	valid := *(&Record{}).AddInt64("id", 1).AddString("value", "one")
	mutations := []Mutation{
		{Table: "items", Record: valid, Mode: MODE_INSERT_ONLY},
		{Table: "missing", Record: valid, Mode: MODE_INSERT_ONLY},
	}
	if _, err := db.ApplyBatch(mutations); err == nil {
		t.Fatal("invalid batch unexpectedly committed")
	}
	query := (&Record{}).AddInt64("id", 1)
	if ok, err := db.Get("items", query); err != nil || ok {
		t.Fatalf("first mutation escaped aborted batch: (%t,%v)", ok, err)
	}
	if _, err := db.Exec("DROP TABLE items;"); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Table("items"); ok {
		t.Fatal("DROP TABLE did not update the catalog")
	}
}
