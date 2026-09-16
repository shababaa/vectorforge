package byodb

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

func TestBTreeRandomized(t *testing.T) {
	tree, _ := newMemoryTree()
	ref := map[string]string{}
	rng := rand.New(rand.NewSource(20240611))
	for i := 0; i < 4000; i++ {
		key := fmt.Sprintf("key-%04d", rng.Intn(1400))
		if rng.Intn(4) == 0 {
			before, beforeOK := tree.getValue([]byte(key))
			got := tree.delete([]byte(key))
			_, want := ref[key]
			if got != want {
				t.Fatalf("operation %d: before=(%q,%v), delete(%q)=%v, want %v", i, before, beforeOK, key, got, want)
			}
			delete(ref, key)
		} else {
			val := fmt.Sprintf("value-%d-%s", i, string(make([]byte, rng.Intn(120))))
			tree.insert([]byte(key), []byte(val))
			ref[key] = val
		}
		if i%100 == 0 {
			if err := tree.validate(); err != nil {
				t.Fatalf("after operation %d: %v", i, err)
			}
		}
	}
	for key, want := range ref {
		got, ok := tree.getValue([]byte(key))
		if !ok || string(got) != want {
			t.Fatalf("get(%q)=(%q,%v), want (%q,true)", key, got, ok, want)
		}
	}
	keys := make([]string, 0, len(ref))
	for key := range ref {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	it := tree.seek(nil, CMP_GE)
	for i := 0; i < len(keys); i++ {
		if !it.valid() {
			t.Fatalf("iterator stopped at item %d", i)
		}
		key, _ := it.deref()
		if string(key) != keys[i] {
			t.Fatalf("iterator item %d=%q, want %q", i, key, keys[i])
		}
		it.next()
	}
	if it.valid() {
		t.Fatal("iterator has extra items")
	}
}

func TestBTreeLargeValuesForceThreeWaySplit(t *testing.T) {
	tree, _ := newMemoryTree()
	for i := 0; i < 80; i++ {
		key := fmt.Sprintf("%03d", i)
		val := make([]byte, 2500+(i%3)*200)
		tree.insert([]byte(key), val)
	}
	if err := tree.validate(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		key := fmt.Sprintf("%03d", i)
		if _, ok := tree.getValue([]byte(key)); !ok {
			t.Fatalf("missing %q", key)
		}
	}
}

func BenchmarkBTreeSequentialInsert(b *testing.B) {
	keys := make([][]byte, 1000)
	values := make([][]byte, 1000)
	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("SYNTH:1d:%010d", i))
		values[i] = []byte(fmt.Sprintf("market-row-%010d", i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		tree, _ := newMemoryTree()
		for i := range keys {
			tree.insert(keys[i], values[i])
		}
	}
}
