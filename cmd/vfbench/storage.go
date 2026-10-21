package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"byodb/vectorforge/hrtime"
	"byodb/vectorforge/index"
	"byodb/vectorforge/store"
)

// StorageReport measures persistence in byodb for one dataset.
type StorageReport struct {
	Dataset            Dataset        `json:"dataset"`
	VectorsPerTx       int            `json:"vectors_per_transaction"`
	InsertMillis       float64        `json:"bulk_insert_ms"`
	InsertPerSec       float64        `json:"bulk_insert_vectors_per_sec"`
	SmallCommit        Latency        `json:"single_chunk_upsert_latency"`
	FileBytes          int64          `json:"file_bytes"`
	RawVectorBytes     int64          `json:"raw_vector_bytes"`
	BytesPerVector     float64        `json:"file_bytes_per_vector"`
	PageCount          uint64         `json:"page_count"`
	FreePages          uint64         `json:"free_pages"`
	TableRows          map[string]int `json:"table_rows"`
	IVFNList           int            `json:"ivf_nlist"`
	IVFTrainMillis     float64        `json:"ivf_train_ms"`
	IVFPersistMillis   float64        `json:"ivf_persist_ms"`
	OpenMillis         float64        `json:"cold_open_ms"`
	LoadVectorsMillis  float64        `json:"cold_load_vectors_ms"`
	LoadIVFMillis      float64        `json:"cold_load_ivf_ms"`
	ReloadedIdentical  bool           `json:"reloaded_ivf_results_identical"`
	ReloadCheckQueries int            `json:"reload_check_queries"`
}

func storageSuite(d *Dataset, dir string, logf func(string, ...any)) (StorageReport, error) {
	rep := StorageReport{Dataset: *d, VectorsPerTx: 1000, RawVectorBytes: int64(d.N * d.Dim * 4)}
	path := filepath.Join(dir, "storage-bench.db")
	os.Remove(path)
	defer os.Remove(path)
	st, err := store.Open(path, store.Options{})
	if err != nil {
		return rep, err
	}
	if err := st.Configure(store.Config{Dimension: d.Dim, Metric: store.MetricCosine, ModelID: "synthetic/" + d.Name}); err != nil {
		return rep, err
	}
	const perDoc = 100
	doc := func(n int) store.Document {
		id := fmt.Sprintf("doc%07d", n)
		return store.Document{ID: id, Source: "bench", Path: id + ".txt", Kind: "text", ContentHash: id,
			ChunkerVersion: "bench", ModelID: "synthetic/" + d.Name}
	}
	start := hrtime.Now()
	next := 0
	for next < d.N {
		end := min(next+rep.VectorsPerTx, d.N)
		err := st.Update(func(w *store.Writer) error {
			first, err := w.AllocateVectorIDs(end - next)
			if err != nil {
				return err
			}
			for base := next; base < end; base += perDoc {
				top := min(base+perDoc, end)
				vectors := make([]store.NewVector, 0, top-base)
				for i := base; i < top; i++ {
					vectors = append(vectors, store.NewVector{
						Chunk:  store.Chunk{VectorID: first + int64(i-next), StartLine: 1, EndLine: 1, Text: "synthetic vector " + strconv.Itoa(i)},
						Vector: d.data[i*d.Dim : (i+1)*d.Dim]})
				}
				if _, err := w.PutDocument(doc(base/perDoc), vectors, false); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return rep, err
		}
		next = end
	}
	rep.InsertMillis = ms(hrtime.Since(start))
	rep.InsertPerSec = float64(d.N) / (rep.InsertMillis / 1000)
	logf("storage: inserted %d vectors in %.1f s (%.0f vectors/s)", d.N, rep.InsertMillis/1000, rep.InsertPerSec)

	// Small transactions are dominated by the two fsyncs of a commit.
	var small []time.Duration
	for i := 0; i < 50; i++ {
		t := hrtime.Now()
		err := st.Update(func(w *store.Writer) error {
			id, err := w.AllocateVectorIDs(1)
			if err != nil {
				return err
			}
			_, err = w.PutDocument(store.Document{ID: "small" + strconv.Itoa(i), Source: "bench", Path: "small.txt", Kind: "text",
				ContentHash: "x", ChunkerVersion: "bench", ModelID: "synthetic/" + d.Name},
				[]store.NewVector{{Chunk: store.Chunk{VectorID: id, StartLine: 1, EndLine: 1, Text: "small"}, Vector: d.query(i % d.Queries)}}, false)
			return err
		})
		if err != nil {
			return rep, err
		}
		small = append(small, hrtime.Since(t))
	}
	rep.SmallCommit = summarize(small)

	// Train on the stored vectors in vector-ID order, as a rebuild does.
	var ids []int64
	var data []float32
	if err := st.View(func(r *store.Reader) error {
		return r.Vectors(func(v store.StoredVector) error {
			ids = append(ids, v.ID)
			data = append(data, v.Vector...)
			return nil
		})
	}); err != nil {
		return rep, err
	}
	rep.IVFNList = index.DefaultNList(len(ids))
	t := hrtime.Now()
	ivf, km, err := index.TrainIVF(ids, data, d.Dim, index.KMeansConfig{K: rep.IVFNList, Seed: 42})
	if err != nil {
		return rep, err
	}
	rep.IVFTrainMillis = ms(hrtime.Since(t))
	assignments := make(map[int64]int, len(ids))
	for _, id := range ids {
		assignments[id], _ = ivf.ListOf(id)
	}
	t = hrtime.Now()
	err = st.Update(func(w *store.Writer) error {
		return w.ReplaceIVF(store.IVFMeta{NList: rep.IVFNList, Dimension: d.Dim, ModelID: "synthetic/" + d.Name, Seed: 42,
			Generation: 1, TrainedVectors: len(ids), Iterations: km.Iterations}, ivf.Centroids(), assignments)
	})
	if err != nil {
		return rep, err
	}
	rep.IVFPersistMillis = ms(hrtime.Since(t))
	stats, err := st.Stats()
	if err != nil {
		return rep, err
	}
	rep.FileBytes, rep.PageCount, rep.FreePages = stats.FileSizeBytes, stats.PageCount, stats.FreePages
	rep.BytesPerVector = float64(rep.FileBytes) / float64(len(ids))
	if err := st.View(func(r *store.Reader) error {
		rep.TableRows, err = r.TableRows()
		return err
	}); err != nil {
		return rep, err
	}
	rep.ReloadCheckQueries = min(200, d.Queries)
	before := make([][]int64, rep.ReloadCheckQueries)
	for i := range before {
		hits, _ := ivf.Search(d.query(i), 10, 8, nil)
		for _, h := range hits {
			before[i] = append(before[i], h.ID)
		}
	}
	if err := st.Close(); err != nil {
		return rep, err
	}
	ivf, data = nil, nil

	// Cold start: what the engine does at process startup, without k-means.
	t = hrtime.Now()
	st, err = store.Open(path, store.Options{ReadOnly: true})
	if err != nil {
		return rep, err
	}
	defer st.Close()
	rep.OpenMillis = ms(hrtime.Since(t))
	t = hrtime.Now()
	flat := index.NewFlat(d.Dim)
	if err := st.View(func(r *store.Reader) error {
		return r.Vectors(func(v store.StoredVector) error { return flat.Add(v.ID, v.Vector) })
	}); err != nil {
		return rep, err
	}
	rep.LoadVectorsMillis = ms(hrtime.Since(t))
	t = hrtime.Now()
	var state store.IVFState
	if err := st.View(func(r *store.Reader) error {
		state, _, err = r.IVF()
		return err
	}); err != nil {
		return rep, err
	}
	reloaded, err := index.NewIVF(d.Dim, state.Centroids)
	if err != nil {
		return rep, err
	}
	loaded := flat.IDs()
	sort.Slice(loaded, func(i, j int) bool { return loaded[i] < loaded[j] })
	for _, id := range loaded {
		v, _ := flat.Vector(id)
		if err := reloaded.AddToList(id, v, state.Postings[id]); err != nil {
			return rep, err
		}
	}
	rep.LoadIVFMillis = ms(hrtime.Since(t))
	rep.ReloadedIdentical = true
	for i := range before {
		hits, _ := reloaded.Search(d.query(i), 10, 8, nil)
		if len(hits) != len(before[i]) {
			rep.ReloadedIdentical = false
			break
		}
		for j, h := range hits {
			if h.ID != before[i][j] {
				rep.ReloadedIdentical = false
			}
		}
	}
	logf("storage: file %.1f MB (%.0f B/vector), cold load vectors %.0f ms + IVF %.0f ms vs retrain %.0f ms; identical=%v",
		float64(rep.FileBytes)/1e6, rep.BytesPerVector, rep.LoadVectorsMillis, rep.LoadIVFMillis, rep.IVFTrainMillis, rep.ReloadedIdentical)
	return rep, nil
}
