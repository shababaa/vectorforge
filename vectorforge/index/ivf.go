package index

import (
	"errors"
	"fmt"
	"math"

	"byodb/vectorforge/vector"
)

// IVF is an inverted-file index with uncompressed ("flat") vectors.
//
// Training partitions the space into nlist Voronoi cells around unit
// centroids. Every vector lives in exactly one cell's inverted list, stored
// contiguously so a probe is a sequential scan. A query scores all centroids,
// probes the nprobe best cells, and exactly scores only their members. With
// balanced lists a query scores about nprobe/nlist of the collection; recall
// is lost only when a true neighbour lies in a cell that was not probed.
type IVF struct {
	dim       int
	centroids []float32 // nlist*dim, unit norm
	lists     []postingList
	loc       map[int64]location
}

type postingList struct {
	ids  []int64
	data []float32 // len(ids)*dim, row-major
}

type location struct {
	list   int32
	offset int32
}

// NewIVF creates an empty index over trained centroids (nlist*dim values).
func NewIVF(dim int, centroids []float32) (*IVF, error) {
	if dim <= 0 || len(centroids) == 0 || len(centroids)%dim != 0 {
		return nil, fmt.Errorf("ivf: %d centroid values do not form rows of dimension %d", len(centroids), dim)
	}
	for c := 0; c*dim < len(centroids); c++ {
		row := centroids[c*dim : (c+1)*dim]
		if err := vector.Validate(row, dim); err != nil {
			return nil, fmt.Errorf("ivf: centroid %d: %w", c, err)
		}
		if !vector.IsUnit(row) {
			return nil, fmt.Errorf("ivf: centroid %d is not unit length", c)
		}
	}
	nlist := len(centroids) / dim
	return &IVF{
		dim:       dim,
		centroids: append([]float32(nil), centroids...),
		lists:     make([]postingList, nlist),
		loc:       map[int64]location{},
	}, nil
}

// TrainIVF trains centroids on data (row-major, aligned with ids) and assigns
// every vector. For deterministic rebuilds pass rows in a canonical order
// (VectorForge sorts by vector ID).
func TrainIVF(ids []int64, data []float32, dim int, cfg KMeansConfig) (*IVF, KMeansResult, error) {
	if len(ids)*dim != len(data) {
		return nil, KMeansResult{}, errors.New("ivf: ids and data lengths disagree")
	}
	trained, err := TrainKMeans(data, dim, cfg)
	if err != nil {
		return nil, KMeansResult{}, err
	}
	ivf, err := NewIVF(dim, trained.Centroids)
	if err != nil {
		return nil, KMeansResult{}, err
	}
	assign := make([]int32, len(ids))
	parallelRange(len(ids), cfg.Workers, func(start, end int) {
		for i := start; i < end; i++ {
			c, _ := nearest(data[i*dim:(i+1)*dim], ivf.centroids, dim)
			assign[i] = int32(c)
		}
	})
	for i, id := range ids {
		if err := ivf.AddToList(id, data[i*dim:(i+1)*dim], int(assign[i])); err != nil {
			return nil, KMeansResult{}, err
		}
	}
	return ivf, trained, nil
}

func (x *IVF) Dim() int   { return x.dim }
func (x *IVF) NList() int { return len(x.lists) }
func (x *IVF) Len() int   { return len(x.loc) }

// Centroids returns a copy of the centroid matrix.
func (x *IVF) Centroids() []float32 { return append([]float32(nil), x.centroids...) }

// Centroid returns a read-only view of centroid c.
func (x *IVF) Centroid(c int) []float32 {
	return x.centroids[c*x.dim : (c+1)*x.dim : (c+1)*x.dim]
}

// Assign returns the list a vector would be placed in.
func (x *IVF) Assign(v []float32) int {
	c, _ := nearest(v, x.centroids, x.dim)
	return c
}

// Add inserts a vector into its nearest list and returns that list.
func (x *IVF) Add(id int64, v []float32) (int, error) {
	if len(v) != x.dim {
		return 0, fmt.Errorf("%w: got %d, want %d", vector.ErrDimensionMismatch, len(v), x.dim)
	}
	list := x.Assign(v)
	return list, x.AddToList(id, v, list)
}

// AddToList inserts a vector into a specific list. It is used to restore a
// persisted assignment exactly, without recomputing nearest centroids.
func (x *IVF) AddToList(id int64, v []float32, list int) error {
	if len(v) != x.dim {
		return fmt.Errorf("%w: got %d, want %d", vector.ErrDimensionMismatch, len(v), x.dim)
	}
	if list < 0 || list >= len(x.lists) {
		return fmt.Errorf("ivf: list %d out of range [0,%d)", list, len(x.lists))
	}
	if _, ok := x.loc[id]; ok {
		return fmt.Errorf("%w: %d", ErrDuplicateID, id)
	}
	pl := &x.lists[list]
	if len(pl.ids) >= math.MaxInt32 {
		return errors.New("ivf: inverted list is full")
	}
	x.loc[id] = location{list: int32(list), offset: int32(len(pl.ids))}
	pl.ids = append(pl.ids, id)
	pl.data = append(pl.data, v...)
	return nil
}

// ListOf reports which list holds id.
func (x *IVF) ListOf(id int64) (int, bool) {
	l, ok := x.loc[id]
	return int(l.list), ok
}

// Remove deletes id from its list by swapping in the list's last vector.
func (x *IVF) Remove(id int64) bool {
	l, ok := x.loc[id]
	if !ok {
		return false
	}
	pl := &x.lists[l.list]
	i, last := int(l.offset), len(pl.ids)-1
	if i != last {
		moved := pl.ids[last]
		pl.ids[i] = moved
		copy(pl.data[i*x.dim:(i+1)*x.dim], pl.data[last*x.dim:])
		x.loc[moved] = location{list: l.list, offset: int32(i)}
	}
	pl.ids = pl.ids[:last]
	pl.data = pl.data[:last*x.dim]
	delete(x.loc, id)
	return true
}

// Search returns the approximate top-k by probing the nprobe lists whose
// centroids score highest against q. nprobe is clamped to [1, nlist];
// nprobe == nlist scans every vector and returns exactly Flat's result.
func (x *IVF) Search(q []float32, k, nprobe int, filter Filter) ([]vector.Hit, SearchStats) {
	var stats SearchStats
	if k <= 0 || len(x.loc) == 0 {
		return nil, stats
	}
	nprobe = max(1, min(nprobe, len(x.lists)))
	probe := vector.NewTopK(nprobe)
	for c := range x.lists {
		probe.Push(int64(c), vector.Dot(q, x.centroids[c*x.dim:(c+1)*x.dim]))
	}
	top := vector.NewTopK(min(k, len(x.loc)))
	dim := x.dim
	for _, cell := range probe.Results() {
		pl := &x.lists[cell.ID]
		stats.ListsProbed++
		for i, id := range pl.ids {
			if filter != nil && !filter(id) {
				stats.Filtered++
				continue
			}
			top.Push(id, vector.Dot(q, pl.data[i*dim:(i+1)*dim]))
			stats.Candidates++
		}
	}
	return top.Results(), stats
}

// ListSizes returns the number of vectors in each list.
func (x *IVF) ListSizes() []int {
	out := make([]int, len(x.lists))
	for i := range x.lists {
		out[i] = len(x.lists[i].ids)
	}
	return out
}

// Postings returns the IDs in list c, in storage order.
func (x *IVF) Postings(c int) []int64 { return append([]int64(nil), x.lists[c].ids...) }

// Balance summarizes list sizes. Imbalance is nlist*sum(n_i^2)/N^2: 1.0 for
// perfectly even lists, and equal to the expected number of vectors scanned
// per probe divided by N/nlist when queries follow the data distribution.
type Balance struct {
	Min        int     `json:"min"`
	Max        int     `json:"max"`
	Mean       float64 `json:"mean"`
	Empty      int     `json:"empty"`
	Imbalance  float64 `json:"imbalance_factor"`
	TotalItems int     `json:"total"`
}

func (x *IVF) Balance() Balance {
	sizes := x.ListSizes()
	b := Balance{Min: math.MaxInt}
	var squares float64
	for _, s := range sizes {
		b.Min = min(b.Min, s)
		b.Max = max(b.Max, s)
		b.TotalItems += s
		squares += float64(s) * float64(s)
		if s == 0 {
			b.Empty++
		}
	}
	if len(sizes) == 0 {
		b.Min = 0
		return b
	}
	b.Mean = float64(b.TotalItems) / float64(len(sizes))
	if b.TotalItems > 0 {
		b.Imbalance = float64(len(sizes)) * squares / (float64(b.TotalItems) * float64(b.TotalItems))
	}
	return b
}

// MemoryBytes estimates resident bytes for centroids, lists and the
// id-to-location map.
func (x *IVF) MemoryBytes() int64 {
	total := int64(cap(x.centroids)) * 4
	for i := range x.lists {
		total += int64(cap(x.lists[i].data))*4 + int64(cap(x.lists[i].ids))*8
	}
	return total + int64(len(x.loc))*mapEntryBytes
}

// DefaultNList is round(sqrt(n)) clamped to [1, 65536]: with balanced lists
// it equalizes the centroid-scoring cost (nlist) and the per-list scan cost
// (n/nlist), which minimizes their sum for nprobe=1.
func DefaultNList(n int) int {
	if n <= 1 {
		return 1
	}
	return max(1, min(65536, int(math.Round(math.Sqrt(float64(n))))))
}
