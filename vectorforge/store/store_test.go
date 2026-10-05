package store

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"byodb"
	"byodb/vectorforge/synth"
	"byodb/vectorforge/vector"
)

const testDim = 384

var testConfig = Config{Dimension: testDim, Metric: MetricCosine, ModelID: "test-model@1"}

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Configure(testConfig); err != nil {
		t.Fatal(err)
	}
	return s
}

func testDocument(id string) Document {
	return Document{ID: id, Source: "test", Path: id + ".md", Kind: "markdown", ContentHash: "hash-" + id,
		ChunkerVersion: "v1", ModelID: testConfig.ModelID, SizeBytes: 10, Metadata: map[string]string{"team": "db"}}
}

// oneHot is a valid unit vector whose float32 bytes are almost all 0x00, the
// worst case for byodb's byte-string escaping.
func oneHot(i int) []float32 {
	v := make([]float32, testDim)
	v[i] = 1
	return v
}

func newVectors(t *testing.T, w *Writer, vecs [][]float32) []NewVector {
	t.Helper()
	first, err := w.AllocateVectorIDs(len(vecs))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]NewVector, len(vecs))
	for i, v := range vecs {
		out[i] = NewVector{
			Chunk:  Chunk{VectorID: first + int64(i), Section: fmt.Sprintf("section %d", i), StartLine: i + 1, EndLine: i + 2, Tokens: 12, Text: fmt.Sprintf("chunk text %d", i)},
			Vector: v,
			List:   i % 2,
		}
	}
	return out
}

func TestPersistenceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vf.db")
	s := openTest(t, path)
	random := synth.Clustered(3, testDim, 1, 1, 1)
	vecs := [][]float32{oneHot(0), oneHot(200), random[:testDim], random[testDim : 2*testDim], random[2*testDim:]}
	err := s.Update(func(w *Writer) error {
		_, err := w.PutDocument(testDocument("doc-a"), newVectors(t, w, vecs), false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if cfg, ok := s.Config(); !ok || cfg.ModelID != testConfig.ModelID || cfg.Dimension != testDim {
		t.Fatalf("config after reopen: %+v %t", cfg, ok)
	}
	err = s.View(func(r *Reader) error {
		doc, err := r.Document("doc-a")
		if err != nil {
			return err
		}
		if doc.ChunkCount != 5 || doc.Metadata["team"] != "db" {
			return fmt.Errorf("document %+v", doc)
		}
		chunks, err := r.Chunks("doc-a")
		if err != nil || len(chunks) != 5 || chunks[3].Text != "chunk text 3" {
			return fmt.Errorf("chunks %+v %v", chunks, err)
		}
		var ids []int64
		err = r.Vectors(func(v StoredVector) error {
			i := len(ids)
			for j := range v.Vector {
				if math.Float32bits(v.Vector[j]) != math.Float32bits(vecs[i][j]) {
					return fmt.Errorf("vector %d value %d changed", v.ID, j)
				}
			}
			ids = append(ids, v.ID)
			return nil
		})
		if err != nil {
			return err
		}
		if len(ids) != 5 || ids[0] != 1 || ids[4] != 5 {
			return fmt.Errorf("vector ids %v", ids)
		}
		hit, err := r.ChunkByVectorID(4)
		if err != nil || hit.Ordinal != 3 || hit.DocID != "doc-a" {
			return fmt.Errorf("chunk by vector id: %+v %v", hit, err)
		}
		single, err := r.Vector(1)
		if err != nil || single.Vector[0] != 1 || single.DocID != "doc-a" {
			return fmt.Errorf("vector 1: %+v %v", single.DocID, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReplaceAndDeleteDocument(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "vf.db"))
	defer s.Close()
	put := func(vecs [][]float32) []int64 {
		var removed []int64
		err := s.Update(func(w *Writer) error {
			var err error
			removed, err = w.PutDocument(testDocument("doc"), newVectors(t, w, vecs), true)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return removed
	}
	if removed := put([][]float32{oneHot(1), oneHot(2)}); len(removed) != 0 {
		t.Fatalf("first insert removed %v", removed)
	}
	removed := put([][]float32{oneHot(3)})
	if len(removed) != 2 || removed[0] != 1 || removed[1] != 2 {
		t.Fatalf("replacement removed %v", removed)
	}
	counts := rowCounts(t, s)
	if counts[tableChunks] != 1 || counts[tableVectors] != 2 || counts[tablePostings] != 1 {
		t.Fatalf("rows after replace: %v", counts)
	}
	// IDs are never reused: the replacement got id 3.
	err := s.View(func(r *Reader) error {
		c, err := r.ChunkByVectorID(3)
		if err != nil || c.Text != "chunk text 0" {
			return fmt.Errorf("%+v %v", c, err)
		}
		if _, err := r.ChunkByVectorID(1); !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("stale chunk lookup: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(w *Writer) error {
		removed, err := w.DeleteDocument("doc")
		if len(removed) != 1 || removed[0] != 3 {
			return fmt.Errorf("delete removed %v", removed)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	counts = rowCounts(t, s)
	if counts[tableDocuments] != 0 || counts[tableChunks] != 0 || counts[tableVectors] != 0 || counts[tablePostings] != 0 {
		t.Fatalf("rows after delete: %v", counts)
	}
	err = s.Update(func(w *Writer) error { _, err := w.DeleteDocument("doc"); return err })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a missing document: %v", err)
	}
}

func rowCounts(t *testing.T, s *Store) map[string]int {
	t.Helper()
	var counts map[string]int
	if err := s.View(func(r *Reader) error {
		var err error
		counts, err = r.TableRows()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestInvalidWritesAbortTheTransaction(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "vf.db"))
	defer s.Close()
	nan := oneHot(0)
	nan[5] = float32(math.NaN())
	notUnit := oneHot(0)
	notUnit[0] = 2
	cases := []struct {
		name string
		vecs func(w *Writer) []NewVector
		doc  Document
	}{
		{"NaN", func(w *Writer) []NewVector { return newVectors(t, w, [][]float32{oneHot(1), nan}) }, testDocument("a")},
		{"not unit", func(w *Writer) []NewVector { return newVectors(t, w, [][]float32{notUnit}) }, testDocument("b")},
		{"zero", func(w *Writer) []NewVector { return newVectors(t, w, [][]float32{make([]float32, testDim)}) }, testDocument("c")},
		{"wrong dimension", func(w *Writer) []NewVector { return newVectors(t, w, [][]float32{{1, 0}}) }, testDocument("d")},
		{"duplicate vector id", func(w *Writer) []NewVector {
			v := newVectors(t, w, [][]float32{oneHot(1), oneHot(2)})
			v[1].Chunk.VectorID = v[0].Chunk.VectorID
			return v
		}, testDocument("e")},
		{"NUL in text", func(w *Writer) []NewVector {
			v := newVectors(t, w, [][]float32{oneHot(1)})
			v[0].Chunk.Text = "bad\x00text"
			return v
		}, testDocument("f")},
		{"oversized text", func(w *Writer) []NewVector {
			v := newVectors(t, w, [][]float32{oneHot(1)})
			v[0].Chunk.Text = string(make([]byte, MaxChunkBytes+1))
			return v
		}, testDocument("g")},
		{"wrong model", func(w *Writer) []NewVector { return newVectors(t, w, [][]float32{oneHot(1)}) },
			func() Document { d := testDocument("h"); d.ModelID = "other"; return d }()},
	}
	for _, c := range cases {
		err := s.Update(func(w *Writer) error { _, err := w.PutDocument(c.doc, c.vecs(w), true); return err })
		if err == nil {
			t.Fatalf("%s: write succeeded", c.name)
		}
	}
	counts := rowCounts(t, s)
	for _, table := range []string{tableDocuments, tableChunks, tableVectors, tablePostings} {
		if counts[table] != 0 {
			t.Fatalf("aborted writes left %d rows in %s", counts[table], table)
		}
	}
}

func TestIVFStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vf.db")
	s := openTest(t, path)
	centroids := append(oneHot(0), oneHot(1)...)
	meta := IVFMeta{NList: 2, Dimension: testDim, ModelID: testConfig.ModelID, Seed: 7, Generation: 1, TrainedVectors: 3}
	err := s.Update(func(w *Writer) error {
		if _, err := w.PutDocument(testDocument("doc"), newVectors(t, w, [][]float32{oneHot(0), oneHot(1), oneHot(2)}), false); err != nil {
			return err
		}
		return w.ReplaceIVF(meta, centroids, map[int64]int{1: 0, 2: 1, 3: 0})
	})
	if err != nil {
		t.Fatal(err)
	}
	// A second rebuild must fully replace the first.
	meta.Generation = 2
	err = s.Update(func(w *Writer) error { return w.ReplaceIVF(meta, centroids, map[int64]int{1: 1, 2: 1, 3: 1}) })
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.View(func(r *Reader) error {
		state, ok, err := r.IVF()
		if err != nil || !ok {
			return fmt.Errorf("ivf state: %t %v", ok, err)
		}
		if state.Meta.Generation != 2 || len(state.Postings) != 3 || state.ListSizes[0] != 0 || state.ListSizes[1] != 3 {
			return fmt.Errorf("state %+v sizes %v", state.Meta, state.ListSizes)
		}
		if state.Centroids[0] != 1 || state.Centroids[testDim+1] != 1 {
			return fmt.Errorf("centroids changed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(w *Writer) error { _, err := w.DeleteDocument("doc"); return err }); err != nil {
		t.Fatal(err)
	}
	if counts := rowCounts(t, s); counts[tablePostings] != 0 || counts[tableCentroids] != 4 {
		t.Fatalf("rows %v", counts)
	}
}

func TestConfigurationIsFixed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vf.db")
	s := openTest(t, path)
	other := testConfig
	other.ModelID = "different-model"
	if err := s.Configure(other); !errors.Is(err, ErrConfigMismatch) {
		t.Fatalf("model change accepted: %v", err)
	}
	other = testConfig
	other.Dimension = 768
	if err := s.Configure(other); !errors.Is(err, ErrConfigMismatch) {
		t.Fatalf("dimension change accepted: %v", err)
	}
	if err := s.Configure(Config{Dimension: 0, Metric: MetricCosine, ModelID: "x"}); err == nil {
		t.Fatal("accepted dimension 0")
	}
	s.Close()
}

func TestRefusesForeignDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "market.db")
	db, err := byodb.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	def := &byodb.TableDef{Name: "market_candles", Cols: []string{"symbol", "close"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_INT64}, PKeys: 1}
	if err := db.TableNew(def); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path, Options{}); !errors.Is(err, ErrForeignDatabase) {
		t.Fatalf("opened a market database: %v", err)
	}
}

func TestReadOnlySnapshot(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, filepath.Join(dir, "vf.db"))
	err := s.Update(func(w *Writer) error {
		_, err := w.PutDocument(testDocument("doc"), newVectors(t, w, [][]float32{oneHot(4)}), false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.db.Backup(filepath.Join(dir, "replica.db")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	replica, err := Open(filepath.Join(dir, "replica.db"), Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	if err := replica.View(func(r *Reader) error { _, err := r.Document("doc"); return err }); err != nil {
		t.Fatal(err)
	}
	if err := replica.Update(func(w *Writer) error { return nil }); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("replica write: %v", err)
	}
}

func TestOneHotVectorFitsDespiteEscaping(t *testing.T) {
	// A whole one-hot 384-d vector encodes to ~3,070 bytes, beyond the
	// engine's 3,000-byte row limit; segmentation keeps each row bounded.
	whole := vector.Encode(nil, oneHot(0))
	escaped := 0
	for _, b := range whole {
		if b == 0 || b == 1 {
			escaped++
		}
	}
	if len(whole)+escaped <= byodb.BTreeMaxValSize {
		t.Fatalf("expected the unsegmented encoding (%d bytes) to exceed the limit", len(whole)+escaped)
	}
	s := openTest(t, filepath.Join(t.TempDir(), "vf.db"))
	defer s.Close()
	err := s.Update(func(w *Writer) error {
		_, err := w.PutDocument(testDocument("doc"), newVectors(t, w, [][]float32{oneHot(383)}), false)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
