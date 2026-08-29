package byodb

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestKVDurableTransactionsAndRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	kv, err := OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	var tx KVTX
	if err := kv.Begin(&tx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1200; i++ {
		tx.Set([]byte(fmt.Sprintf("key-%04d", i)), []byte(fmt.Sprintf("value-%04d", i)))
	}
	if err := kv.Commit(&tx); err != nil {
		t.Fatal(err)
	}

	var aborted KVTX
	if err := kv.Begin(&aborted); err != nil {
		t.Fatal(err)
	}
	aborted.Set([]byte("not-durable"), []byte("no"))
	kv.Abort(&aborted)
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
	for i := 0; i < 1200; i++ {
		key := []byte(fmt.Sprintf("key-%04d", i))
		want := fmt.Sprintf("value-%04d", i)
		got, ok := read.Get(key)
		if !ok || string(got) != want {
			t.Fatalf("Get(%q)=(%q,%v), want (%q,true)", key, got, ok, want)
		}
	}
	if _, ok := read.Get([]byte("not-durable")); ok {
		t.Fatal("aborted key survived")
	}
	kv.Abort(&read)
}

func TestKVSnapshotAndWriteConflict(t *testing.T) {
	kv, err := OpenKV(filepath.Join(t.TempDir(), "conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	var seed KVTX
	if err := kv.Begin(&seed); err != nil {
		t.Fatal(err)
	}
	seed.Set([]byte("balance"), []byte("10"))
	if err := kv.Commit(&seed); err != nil {
		t.Fatal(err)
	}

	var first, second KVTX
	if err := kv.Begin(&first); err != nil {
		t.Fatal(err)
	}
	if err := kv.Begin(&second); err != nil {
		t.Fatal(err)
	}
	if got, _ := first.Get([]byte("balance")); string(got) != "10" {
		t.Fatalf("first snapshot=%q", got)
	}
	second.Set([]byte("balance"), []byte("20"))
	if err := kv.Commit(&second); err != nil {
		t.Fatal(err)
	}
	if got, _ := first.Get([]byte("balance")); string(got) != "10" {
		t.Fatalf("snapshot changed to %q", got)
	}
	first.Set([]byte("audit"), []byte("saw 10"))
	if err := kv.Commit(&first); !errors.Is(err, ErrConflict) {
		t.Fatalf("commit error=%v, want ErrConflict", err)
	}
}

func TestKVNonConflictingWritersRebaseOnLatestRoot(t *testing.T) {
	kv, err := OpenKV(filepath.Join(t.TempDir(), "rebase.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	var left, right KVTX
	kv.Begin(&left)
	kv.Begin(&right)
	left.Set([]byte("left"), []byte("one"))
	right.Set([]byte("right"), []byte("two"))
	if err := kv.Commit(&left); err != nil {
		t.Fatal(err)
	}
	if err := kv.Commit(&right); err != nil {
		t.Fatal(err)
	}
	var read KVTX
	kv.Begin(&read)
	for key, want := range map[string]string{"left": "one", "right": "two"} {
		got, ok := read.Get([]byte(key))
		if !ok || string(got) != want {
			t.Fatalf("%s=(%q,%v), want %q", key, got, ok, want)
		}
	}
	kv.Abort(&read)
}

func TestKVCombinedIterator(t *testing.T) {
	kv, err := OpenKV(filepath.Join(t.TempDir(), "iter.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	var seed KVTX
	kv.Begin(&seed)
	for _, key := range []string{"a", "c", "e"} {
		seed.Set([]byte(key), []byte(key+"0"))
	}
	if err := kv.Commit(&seed); err != nil {
		t.Fatal(err)
	}
	var tx KVTX
	kv.Begin(&tx)
	tx.Set([]byte("b"), []byte("b1"))
	tx.Set([]byte("c"), []byte("c1"))
	tx.Del(&DeleteReq{Key: []byte("e")})
	it := tx.Seek([]byte("a"), CMP_GE)
	var got []string
	for it.Valid() {
		key, val := it.Deref()
		got = append(got, string(key)+"="+string(val))
		it.Next()
	}
	want := []string{"a=a0", "b=b1", "c=c1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("iterator=%v, want %v", got, want)
	}
	kv.Abort(&tx)
}

func TestKVFreeListStressAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reuse.db")
	var kv *KV
	var err error
	for round := 0; round < 8; round++ {
		if kv == nil {
			kv, err = OpenKV(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		var tx KVTX
		if err := kv.Begin(&tx); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 700; i++ {
			key := []byte(fmt.Sprintf("item-%04d", i))
			if (i+round)%4 == 0 {
				tx.Del(&DeleteReq{Key: key})
			} else {
				tx.Set(key, []byte(fmt.Sprintf("round-%d-value-%04d", round, i)))
			}
		}
		if err := kv.Commit(&tx); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if round%2 == 1 {
			if err := kv.Close(); err != nil {
				t.Fatal(err)
			}
			kv = nil
		}
	}
	if kv == nil {
		kv, err = OpenKV(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer kv.Close()
	var read KVTX
	kv.Begin(&read)
	for i := 0; i < 700; i++ {
		key := []byte(fmt.Sprintf("item-%04d", i))
		got, ok := read.Get(key)
		wantOK := (i+7)%4 != 0
		if ok != wantOK {
			t.Fatalf("%q exists=%v, want %v", key, ok, wantOK)
		}
		if ok && string(got) != fmt.Sprintf("round-7-value-%04d", i) {
			t.Fatalf("%q=%q", key, got)
		}
	}
	kv.Abort(&read)
}

func TestKVFallsBackFromCorruptNewestMetaPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.db")
	kv, err := OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	var first, second KVTX
	kv.Begin(&first)
	first.Set([]byte("first"), []byte("durable"))
	if err := kv.Commit(&first); err != nil {
		t.Fatal(err)
	}
	kv.Begin(&second)
	second.Set([]byte("second"), []byte("newest"))
	if err := kv.Commit(&second); err != nil {
		t.Fatal(err)
	}
	newestSlot := int(kv.version % 2)
	if err := kv.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("BROKEN!!"), int64(newestSlot*BTreePageSize)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	kv, err = OpenKV(path)
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	var read KVTX
	kv.Begin(&read)
	if got, ok := read.Get([]byte("first")); !ok || string(got) != "durable" {
		t.Fatalf("fallback lost prior commit: (%q,%v)", got, ok)
	}
	if _, ok := read.Get([]byte("second")); ok {
		t.Fatal("corrupt newest commit should have fallen back")
	}
	kv.Abort(&read)
}
