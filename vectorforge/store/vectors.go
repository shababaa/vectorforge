package store

import (
	"encoding/json"
	"fmt"

	"byodb"
	"byodb/vectorforge/vector"
)

// StoredVector is a decoded vector row group.
type StoredVector struct {
	ID     int64
	DocID  string
	Vector []float32
}

// Vector reads one vector by ID.
func (r *Reader) Vector(id int64) (StoredVector, error) {
	cfg, ok := r.store.Config()
	if !ok {
		return StoredVector{}, ErrNotConfigured
	}
	out := StoredVector{ID: id, Vector: make([]float32, 0, cfg.Dimension)}
	segments := 0
	key := (&byodb.Record{}).AddInt64("vector_id", id)
	err := r.scanPrefix(tableVectors, []string{"vector_id", "segment"}, key, func(rec byodb.Record) error {
		if int(i64(rec, "segment")) != segments {
			return fmt.Errorf("vector %d: segment %d out of order", id, i64(rec, "segment"))
		}
		segments++
		out.DocID = str(rec, "doc_id")
		var err error
		out.Vector, err = vector.Decode(out.Vector, rec.Get("data").Str)
		return err
	})
	if err != nil {
		return StoredVector{}, err
	}
	if segments == 0 {
		return StoredVector{}, fmt.Errorf("vector %d: %w", id, ErrNotFound)
	}
	return out, checkDecoded(out, cfg.Dimension)
}

func checkDecoded(v StoredVector, dim int) error {
	if err := vector.Validate(v.Vector, dim); err != nil {
		return fmt.Errorf("stored vector %d is corrupt: %w", v.ID, err)
	}
	return nil
}

// Vectors streams every vector in vector_id order, reassembling segments.
// The slice passed to fn is reused between calls.
func (r *Reader) Vectors(fn func(StoredVector) error) error {
	cfg, ok := r.store.Config()
	if !ok {
		return nil // an unconfigured collection has no vectors
	}
	want := vector.SegmentCount(cfg.Dimension)
	current := StoredVector{ID: -1, Vector: make([]float32, 0, cfg.Dimension)}
	segments := 0
	flush := func() error {
		if current.ID < 0 {
			return nil
		}
		if segments != want {
			return fmt.Errorf("stored vector %d has %d of %d segments", current.ID, segments, want)
		}
		if err := checkDecoded(current, cfg.Dimension); err != nil {
			return err
		}
		return fn(current)
	}
	err := r.scanAll(tableVectors, func(rec byodb.Record) error {
		id, seg := i64(rec, "vector_id"), int(i64(rec, "segment"))
		if id != current.ID {
			if err := flush(); err != nil {
				return err
			}
			current.ID, current.DocID, current.Vector, segments = id, str(rec, "doc_id"), current.Vector[:0], 0
		}
		if seg != segments {
			return fmt.Errorf("stored vector %d: segment %d out of order", id, seg)
		}
		segments++
		var err error
		current.Vector, err = vector.Decode(current.Vector, rec.Get("data").Str)
		return err
	})
	if err != nil {
		return err
	}
	return flush()
}

// IVFMeta describes the persisted IVF index state.
type IVFMeta struct {
	NList          int       `json:"nlist"`
	Dimension      int       `json:"dimension"`
	ModelID        string    `json:"model_id"`
	Seed           int64     `json:"seed"`
	Generation     int64     `json:"generation"`
	TrainedAt      int64     `json:"trained_at"`
	TrainedVectors int       `json:"trained_vectors"`
	SampleSize     int       `json:"sample_size"`
	Iterations     int       `json:"iterations"`
	Converged      bool      `json:"converged"`
	Objective      float64   `json:"objective"`
	History        []float64 `json:"objective_history,omitempty"`
	EmptyReseeds   int       `json:"empty_reseeds"`
	TrainMillis    int64     `json:"train_millis"`
}

// IVFState is everything needed to reconstruct the index without retraining.
type IVFState struct {
	Meta      IVFMeta
	Centroids []float32     // nlist*dim
	Postings  map[int64]int // vector_id -> list_id
	ListSizes []int         // per-list counts from the postings table
	Orphans   []int64       // postings whose list_id is out of range
}

// IVF loads the persisted index. ok is false if the index was never trained.
func (r *Reader) IVF() (IVFState, bool, error) {
	raw, ok, err := r.getMeta(metaIVF)
	if err != nil || !ok {
		return IVFState{}, false, err
	}
	var state IVFState
	if err := json.Unmarshal([]byte(raw), &state.Meta); err != nil {
		return IVFState{}, false, fmt.Errorf("decode IVF metadata: %w", err)
	}
	dim, nlist := state.Meta.Dimension, state.Meta.NList
	if dim < 1 || nlist < 1 {
		return IVFState{}, false, fmt.Errorf("invalid IVF metadata: nlist=%d dimension=%d", nlist, dim)
	}
	state.Centroids = make([]float32, 0, nlist*dim)
	expectList, expectSeg := int64(0), 0
	err = r.scanAll(tableCentroids, func(rec byodb.Record) error {
		list, seg := i64(rec, "list_id"), int(i64(rec, "segment"))
		if list != expectList || seg != expectSeg {
			return fmt.Errorf("IVF centroid rows out of sequence at list %d segment %d", list, seg)
		}
		var err error
		state.Centroids, err = vector.Decode(state.Centroids, rec.Get("data").Str)
		if expectSeg++; expectSeg == vector.SegmentCount(dim) {
			expectList, expectSeg = expectList+1, 0
		}
		return err
	})
	if err != nil {
		return IVFState{}, false, err
	}
	if len(state.Centroids) != nlist*dim {
		return IVFState{}, false, fmt.Errorf("IVF has %d centroid values, want %d", len(state.Centroids), nlist*dim)
	}
	state.Postings = map[int64]int{}
	state.ListSizes = make([]int, nlist)
	err = r.scanAll(tablePostings, func(rec byodb.Record) error {
		list, id := i64(rec, "list_id"), i64(rec, "vector_id")
		if list < 0 || list >= int64(nlist) {
			state.Orphans = append(state.Orphans, id)
			return nil
		}
		if _, dup := state.Postings[id]; dup {
			return fmt.Errorf("vector %d has more than one IVF posting", id)
		}
		state.Postings[id] = int(list)
		state.ListSizes[list]++
		return nil
	})
	return state, true, err
}

// PutPosting records that vector id belongs to IVF list.
func (w *Writer) PutPosting(list int, id int64) error {
	rec := (&byodb.Record{}).AddInt64("list_id", int64(list)).AddInt64("vector_id", id)
	changed, err := w.tx.Set(tablePostings, *rec, byodb.MODE_INSERT_ONLY)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: duplicate posting for vector %d", ErrInvalidRecord, id)
	}
	return nil
}

// DeletePostingsFor removes every posting of a vector, found through the
// postings table's vector_id secondary index.
func (w *Writer) DeletePostingsFor(id int64) error {
	var lists []int64
	key := (&byodb.Record{}).AddInt64("vector_id", id)
	err := w.scanPrefix(tablePostings, []string{"vector_id"}, key, func(rec byodb.Record) error {
		lists = append(lists, i64(rec, "list_id"))
		return nil
	})
	if err != nil {
		return err
	}
	for _, list := range lists {
		rec := (&byodb.Record{}).AddInt64("list_id", list).AddInt64("vector_id", id)
		if _, err := w.tx.Delete(tablePostings, *rec); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceIVF swaps the complete persisted index for a newly trained one.
// Because it runs in one transaction, a crash before commit leaves the
// previous centroids and postings intact.
func (w *Writer) ReplaceIVF(meta IVFMeta, centroids []float32, assignments map[int64]int) error {
	cfg, ok := w.store.Config()
	if !ok {
		return ErrNotConfigured
	}
	if meta.Dimension != cfg.Dimension || meta.NList < 1 || len(centroids) != meta.NList*meta.Dimension {
		return fmt.Errorf("%w: IVF metadata does not match %d centroid values", ErrInvalidRecord, len(centroids))
	}
	if _, err := w.deleteAll(tableCentroids); err != nil {
		return err
	}
	if _, err := w.deleteAll(tablePostings); err != nil {
		return err
	}
	dim := meta.Dimension
	for list := 0; list < meta.NList; list++ {
		row := centroids[list*dim : (list+1)*dim]
		for seg := 0; seg < vector.SegmentCount(dim); seg++ {
			rec := (&byodb.Record{}).AddInt64("list_id", int64(list)).AddInt64("segment", int64(seg)).
				AddStr("data", vector.Encode(nil, vector.Segment(row, seg)))
			if _, err := w.tx.Set(tableCentroids, *rec, byodb.MODE_INSERT_ONLY); err != nil {
				return err
			}
		}
	}
	for id, list := range assignments {
		if list < 0 || list >= meta.NList {
			return fmt.Errorf("%w: vector %d assigned to list %d", ErrInvalidRecord, id, list)
		}
		if err := w.PutPosting(list, id); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return w.setMeta(metaIVF, string(encoded))
}

// PutRun upserts an ingestion run summary.
func (w *Writer) PutRun(id, status string, startedAt, finishedAt int64, summary any) error {
	encoded, err := json.Marshal(summary)
	if err != nil {
		return err
	}
	if len(encoded) > 2500 {
		return fmt.Errorf("%w: run summary is %d bytes", ErrInvalidRecord, len(encoded))
	}
	rec := (&byodb.Record{}).AddString("run_id", id).AddString("status", status).
		AddInt64("started_at", startedAt).AddInt64("finished_at", finishedAt).AddStr("summary", encoded)
	_, err = w.tx.Set(tableRuns, *rec, byodb.MODE_UPSERT)
	return err
}

// Run is a persisted ingestion run.
type Run struct {
	ID         string          `json:"run_id"`
	Status     string          `json:"status"`
	StartedAt  int64           `json:"started_at"`
	FinishedAt int64           `json:"finished_at"`
	Summary    json.RawMessage `json:"summary"`
}

// Runs returns ingestion runs in run_id order.
func (r *Reader) Runs() ([]Run, error) {
	var out []Run
	err := r.scanAll(tableRuns, func(rec byodb.Record) error {
		out = append(out, Run{ID: str(rec, "run_id"), Status: str(rec, "status"), StartedAt: i64(rec, "started_at"),
			FinishedAt: i64(rec, "finished_at"), Summary: json.RawMessage(append([]byte(nil), rec.Get("summary").Str...))})
		return nil
	})
	return out, err
}
