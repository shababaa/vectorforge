// Package index implements in-memory nearest-neighbour indexes over unit
// vectors: an exact flat scan and an IVF-Flat approximate index trained with
// spherical k-means. Indexes are not internally synchronized; concurrent
// Search calls are safe only while no goroutine mutates the index.
package index

import (
	"errors"
	"fmt"

	"byodb/vectorforge/vector"
)

var (
	ErrDuplicateID = errors.New("vector id already indexed")
	ErrNotTrained  = errors.New("index is not trained")
)

// Filter restricts search to IDs for which it returns true. nil admits all.
type Filter func(id int64) bool

// SearchStats describes the work one search performed.
type SearchStats struct {
	Candidates  int `json:"candidates_scored"`
	ListsProbed int `json:"lists_probed"`
	Filtered    int `json:"filtered_out"`
}

// Flat stores vectors in one contiguous row-major matrix and answers queries
// by scoring every row. It is exact and serves as the recall baseline.
type Flat struct {
	dim  int
	ids  []int64
	data []float32
	pos  map[int64]int
}

// NewFlat returns an empty exact index for vectors of dimension dim.
func NewFlat(dim int) *Flat {
	return &Flat{dim: dim, pos: map[int64]int{}}
}

func (f *Flat) Dim() int { return f.dim }
func (f *Flat) Len() int { return len(f.ids) }

// Add appends a vector. The caller is responsible for normalization.
func (f *Flat) Add(id int64, v []float32) error {
	if len(v) != f.dim {
		return fmt.Errorf("%w: got %d, want %d", vector.ErrDimensionMismatch, len(v), f.dim)
	}
	if _, ok := f.pos[id]; ok {
		return fmt.Errorf("%w: %d", ErrDuplicateID, id)
	}
	f.pos[id] = len(f.ids)
	f.ids = append(f.ids, id)
	f.data = append(f.data, v...)
	return nil
}

// Remove deletes id in O(dim) by moving the last row into its slot.
func (f *Flat) Remove(id int64) bool {
	i, ok := f.pos[id]
	if !ok {
		return false
	}
	last := len(f.ids) - 1
	if i != last {
		f.ids[i] = f.ids[last]
		copy(f.data[i*f.dim:(i+1)*f.dim], f.data[last*f.dim:])
		f.pos[f.ids[i]] = i
	}
	f.ids = f.ids[:last]
	f.data = f.data[:last*f.dim]
	delete(f.pos, id)
	return true
}

// Vector returns a read-only view of the stored vector for id.
func (f *Flat) Vector(id int64) ([]float32, bool) {
	i, ok := f.pos[id]
	if !ok {
		return nil, false
	}
	return f.data[i*f.dim : (i+1)*f.dim : (i+1)*f.dim], true
}

// IDs returns a copy of the indexed IDs in storage order.
func (f *Flat) IDs() []int64 { return append([]int64(nil), f.ids...) }

// Search returns the k highest-scoring vectors by dot product.
func (f *Flat) Search(q []float32, k int, filter Filter) ([]vector.Hit, SearchStats) {
	var stats SearchStats
	if k <= 0 || len(f.ids) == 0 {
		return nil, stats
	}
	top := vector.NewTopK(min(k, len(f.ids)))
	dim := f.dim
	for i, id := range f.ids {
		if filter != nil && !filter(id) {
			stats.Filtered++
			continue
		}
		top.Push(id, vector.Dot(q, f.data[i*dim:(i+1)*dim]))
		stats.Candidates++
	}
	return top.Results(), stats
}

// MemoryBytes estimates resident bytes: vector payload, IDs, and the
// position map (about 16 bytes per entry plus Go map overhead).
func (f *Flat) MemoryBytes() int64 {
	return int64(cap(f.data))*4 + int64(cap(f.ids))*8 + int64(len(f.pos))*mapEntryBytes
}

// mapEntryBytes approximates a map[int64]T entry including bucket overhead.
const mapEntryBytes = 40
