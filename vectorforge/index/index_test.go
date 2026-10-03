package index

import (
	"errors"
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"

	"byodb/vectorforge/synth"
	"byodb/vectorforge/vector"
)

func rows(data []float32, dim int) int { return len(data) / dim }

func sequentialIDs(n int) []int64 {
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	return ids
}

func buildFlat(t testing.TB, ids []int64, data []float32, dim int) *Flat {
	t.Helper()
	f := NewFlat(dim)
	for i, id := range ids {
		if err := f.Add(id, data[i*dim:(i+1)*dim]); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func bruteForce(ids []int64, data []float32, dim int, q []float32, k int) []vector.Hit {
	hits := make([]vector.Hit, len(ids))
	for i, id := range ids {
		hits[i] = vector.Hit{ID: id, Score: vector.Dot(q, data[i*dim:(i+1)*dim])}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	return hits[:min(k, len(hits))]
}

func sameHits(t *testing.T, label string, got, want []vector.Hit) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d hits, want %d", label, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s rank %d: got %+v, want %+v", label, i, got[i], want[i])
		}
	}
}

func TestFlatMatchesBruteForce(t *testing.T) {
	const dim = 32
	data := synth.Clustered(500, dim, 10, 1, 1)
	ids := sequentialIDs(rows(data, dim))
	// Duplicate rows create exact score ties, which must order by ID.
	copy(data[10*dim:11*dim], data[20*dim:21*dim])
	f := buildFlat(t, ids, data, dim)
	queries := synth.Queries(20, dim, 10, 1, 1)
	for qi := 0; qi < rows(queries, dim); qi++ {
		q := queries[qi*dim : (qi+1)*dim]
		for _, k := range []int{1, 10, 600} {
			got, stats := f.Search(q, k, nil)
			sameHits(t, "flat", got, bruteForce(ids, data, dim, q, k))
			if stats.Candidates != len(ids) {
				t.Fatalf("scored %d of %d", stats.Candidates, len(ids))
			}
		}
	}
}

func TestFlatMutations(t *testing.T) {
	f := NewFlat(2)
	if err := f.Add(1, []float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if err := f.Add(1, []float32{0, 1}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := f.Add(2, []float32{1, 0, 0}); !errors.Is(err, vector.ErrDimensionMismatch) {
		t.Fatalf("dimension: %v", err)
	}
	_ = f.Add(2, []float32{0, 1})
	_ = f.Add(3, []float32{-1, 0})
	if !f.Remove(1) || f.Remove(1) {
		t.Fatal("remove reported the wrong result")
	}
	// Removing row 0 moved id 3 into it; its vector must follow.
	if v, ok := f.Vector(3); !ok || v[0] != -1 {
		t.Fatalf("moved vector %v %v", v, ok)
	}
	hits, _ := f.Search([]float32{1, 0}, 10, nil)
	if len(hits) != 2 || hits[0].ID != 2 || hits[1].ID != 3 {
		t.Fatalf("hits after remove: %+v", hits)
	}
	if hits, _ := NewFlat(2).Search([]float32{1, 0}, 3, nil); hits != nil {
		t.Fatal("empty index returned hits")
	}
}

func TestFilter(t *testing.T) {
	const dim = 16
	data := synth.Clustered(300, dim, 5, 1, 2)
	ids := sequentialIDs(rows(data, dim))
	even := func(id int64) bool { return id%2 == 0 }
	f := buildFlat(t, ids, data, dim)
	ivf, _, err := TrainIVF(ids, data, dim, KMeansConfig{K: 8, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	q := data[:dim]
	exact, stats := f.Search(q, 20, even)
	approx, _ := ivf.Search(q, 20, ivf.NList(), even)
	if stats.Filtered != 150 {
		t.Fatalf("filtered %d", stats.Filtered)
	}
	for _, h := range append(exact, approx...) {
		if h.ID%2 != 0 {
			t.Fatalf("filter admitted %d", h.ID)
		}
	}
	sameHits(t, "filtered full probe", approx, exact)
}

func TestKMeansIsDeterministic(t *testing.T) {
	const dim = 24
	data := synth.Clustered(2000, dim, 12, 1, 3)
	cfg := KMeansConfig{K: 16, Seed: 42, MaxPointsPerCentroid: 64}
	a, err := TrainKMeans(data, dim, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Worker count changes goroutine scheduling but must not change results.
	cfg.Workers = 1
	b, err := TrainKMeans(data, dim, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.SampleSize != 16*64 || a.Iterations != b.Iterations {
		t.Fatalf("sample %d, iterations %d vs %d", a.SampleSize, a.Iterations, b.Iterations)
	}
	for i := range a.Centroids {
		if math.Float32bits(a.Centroids[i]) != math.Float32bits(b.Centroids[i]) {
			t.Fatalf("centroid value %d differs: %v vs %v", i, a.Centroids[i], b.Centroids[i])
		}
	}
	for c := 0; c < cfg.K; c++ {
		if !vector.IsUnit(a.Centroids[c*dim : (c+1)*dim]) {
			t.Fatalf("centroid %d is not unit length", c)
		}
	}
}

func TestKMeansRecoversSeparatedClusters(t *testing.T) {
	const dim, k = 16, 6
	// Tight blobs: each point is within ~0.1 radians of its true centre.
	data := synth.Clustered(1200, dim, k, 0.1, 4)
	result, err := TrainKMeans(data, dim, KMeansConfig{K: k, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Converged || result.Objective < 0.98 {
		t.Fatalf("converged=%t objective=%.4f", result.Converged, result.Objective)
	}
	for i := 1; i < len(result.History); i++ {
		if result.History[i] < result.History[i-1]-1e-9 {
			t.Fatalf("objective decreased: %v", result.History)
		}
	}
}

func TestKMeansRejectsInvalidInput(t *testing.T) {
	data := synth.Clustered(10, 4, 2, 1, 5)
	if _, err := TrainKMeans(data, 4, KMeansConfig{K: 11}); err == nil {
		t.Fatal("trained more centroids than points")
	}
	if _, err := TrainKMeans(data, 4, KMeansConfig{K: 0}); err == nil {
		t.Fatal("accepted K=0")
	}
	if _, err := TrainKMeans(data[:7], 4, KMeansConfig{K: 1}); err == nil {
		t.Fatal("accepted a partial row")
	}
}

func TestKMeansHandlesDuplicatePoints(t *testing.T) {
	const dim = 4
	data := make([]float32, 0, 20*dim)
	for i := 0; i < 20; i++ {
		data = append(data, 1, 0, 0, 0) // every point identical: k-means++ has no distance mass
	}
	result, err := TrainKMeans(data, dim, KMeansConfig{K: 4, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.EmptyReseeds == 0 {
		t.Fatal("expected empty clusters to be reseeded")
	}
	ivf, _, err := TrainIVF(sequentialIDs(20), data, dim, KMeansConfig{K: 4, Seed: 1})
	if err != nil || ivf.Len() != 20 {
		t.Fatalf("ivf over duplicates: %v", err)
	}
}

func TestIVFFullProbeEqualsExact(t *testing.T) {
	const dim = 48
	data := synth.Clustered(3000, dim, 30, 1, 6)
	ids := sequentialIDs(rows(data, dim))
	f := buildFlat(t, ids, data, dim)
	ivf, _, err := TrainIVF(ids, data, dim, KMeansConfig{K: 40, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	queries := synth.Queries(30, dim, 30, 1, 6)
	for qi := 0; qi < rows(queries, dim); qi++ {
		q := queries[qi*dim : (qi+1)*dim]
		exact, _ := f.Search(q, 10, nil)
		approx, stats := ivf.Search(q, 10, ivf.NList(), nil)
		sameHits(t, "full probe", approx, exact)
		if stats.Candidates != len(ids) || stats.ListsProbed != ivf.NList() {
			t.Fatalf("full probe scored %d in %d lists", stats.Candidates, stats.ListsProbed)
		}
	}
}

func TestIVFRecallIsMonotoneInNProbe(t *testing.T) {
	const dim, n = 64, 5000
	data := synth.Clustered(n, dim, 50, 1, 7)
	ids := sequentialIDs(n)
	f := buildFlat(t, ids, data, dim)
	ivf, _, err := TrainIVF(ids, data, dim, KMeansConfig{K: DefaultNList(n), Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	queries := synth.Queries(100, dim, 50, 1, 7)
	probes := []int{1, 2, 4, 8, 16, 32, ivf.NList()}
	mean := make([]float64, len(probes))
	for qi := 0; qi < rows(queries, dim); qi++ {
		q := queries[qi*dim : (qi+1)*dim]
		exact, _ := f.Search(q, 10, nil)
		prev := -1.0
		for pi, nprobe := range probes {
			approx, _ := ivf.Search(q, 10, nprobe, nil)
			r := RecallAtK(exact, approx, 10)
			// Probed lists only grow, and any candidate outranking a true
			// neighbour is itself a true neighbour, so recall cannot drop.
			if r < prev {
				t.Fatalf("query %d: recall fell from %.2f to %.2f at nprobe=%d", qi, prev, r, nprobe)
			}
			prev = r
			mean[pi] += r
		}
	}
	for i := range mean {
		mean[i] /= float64(rows(queries, dim))
	}
	if math.Abs(mean[len(mean)-1]-1) > 1e-12 {
		t.Fatalf("full-probe recall %.3f", mean[len(mean)-1])
	}
	if mean[3] < 0.9 { // nprobe=8 of ~71 lists on clustered data
		t.Fatalf("recall@10 at nprobe=8 is %.3f; curve %v", mean[3], mean)
	}
	t.Logf("recall@10 by nprobe %v: %v", probes, mean)
}

func TestIVFMutationsAndRestore(t *testing.T) {
	const dim = 32
	data := synth.Clustered(1000, dim, 10, 1, 8)
	ids := sequentialIDs(rows(data, dim))
	ivf, _, err := TrainIVF(ids, data, dim, KMeansConfig{K: 16, Seed: 9})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 500, 1000, 37} {
		if !ivf.Remove(id) {
			t.Fatalf("remove %d failed", id)
		}
	}
	if ivf.Remove(1) || ivf.Len() != 996 {
		t.Fatal("double remove or wrong length")
	}
	extra := data[:dim]
	list, err := ivf.Add(5000, extra)
	if err != nil || list != ivf.Assign(extra) {
		t.Fatalf("add: list %d err %v", list, err)
	}
	if _, err := ivf.Add(5000, extra); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("duplicate add: %v", err)
	}

	// Restoring from centroids + postings (what the store persists) must
	// reproduce the same index without retraining.
	restored, err := NewIVF(dim, ivf.Centroids())
	if err != nil {
		t.Fatal(err)
	}
	for c := 0; c < ivf.NList(); c++ {
		for _, id := range ivf.Postings(c) {
			v := vectorFor(id, ids, data, dim, extra)
			if err := restored.AddToList(id, v, c); err != nil {
				t.Fatal(err)
			}
		}
	}
	queries := synth.Queries(20, dim, 10, 1, 8)
	for qi := 0; qi < rows(queries, dim); qi++ {
		q := queries[qi*dim : (qi+1)*dim]
		a, _ := ivf.Search(q, 10, 4, nil)
		b, _ := restored.Search(q, 10, 4, nil)
		sameHits(t, "restored", b, a)
		for _, h := range a {
			if h.ID == 1 || h.ID == 500 || h.ID == 1000 || h.ID == 37 {
				t.Fatalf("removed id %d returned", h.ID)
			}
		}
	}
	balance := ivf.Balance()
	if balance.TotalItems != 997 || balance.Imbalance < 1 || balance.Min > balance.Max {
		t.Fatalf("balance %+v", balance)
	}
}

func vectorFor(id int64, ids []int64, data []float32, dim int, extra []float32) []float32 {
	if id == 5000 {
		return extra
	}
	i := sort.Search(len(ids), func(i int) bool { return ids[i] >= id })
	return data[i*dim : (i+1)*dim]
}

func TestTrainIVFIsDeterministic(t *testing.T) {
	const dim = 16
	data := synth.Clustered(800, dim, 8, 1, 10)
	ids := sequentialIDs(rows(data, dim))
	a, _, err := TrainIVF(ids, data, dim, KMeansConfig{K: 10, Seed: 5})
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := TrainIVF(ids, data, dim, KMeansConfig{K: 10, Seed: 5, Workers: 3})
	for c := 0; c < a.NList(); c++ {
		pa, pb := a.Postings(c), b.Postings(c)
		if len(pa) != len(pb) {
			t.Fatalf("list %d sizes %d vs %d", c, len(pa), len(pb))
		}
		for i := range pa {
			if pa[i] != pb[i] {
				t.Fatalf("list %d differs", c)
			}
		}
	}
}

func TestNewIVFValidatesCentroids(t *testing.T) {
	if _, err := NewIVF(2, []float32{1, 0, 0}); err == nil {
		t.Fatal("accepted partial centroid")
	}
	if _, err := NewIVF(2, []float32{2, 0}); err == nil {
		t.Fatal("accepted non-unit centroid")
	}
	if _, err := NewIVF(2, []float32{float32(math.NaN()), 1}); err == nil {
		t.Fatal("accepted NaN centroid")
	}
	ivf, _ := NewIVF(2, []float32{1, 0})
	if err := ivf.AddToList(1, []float32{1, 0}, 1); err == nil {
		t.Fatal("accepted out-of-range list")
	}
}

func TestRecallAtK(t *testing.T) {
	exact := []vector.Hit{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}
	approx := []vector.Hit{{ID: 2}, {ID: 9}, {ID: 1}}
	if r := RecallAtK(exact, approx, 3); math.Abs(r-2.0/3) > 1e-12 {
		t.Fatalf("recall %v", r)
	}
	if r := RecallAtK(nil, approx, 3); r != 1 {
		t.Fatalf("empty exact recall %v", r)
	}
	if DefaultNList(10_000) != 100 || DefaultNList(0) != 1 {
		t.Fatal("DefaultNList")
	}
}

func TestConcurrentSearches(t *testing.T) {
	const dim = 32
	data := synth.Clustered(2000, dim, 20, 1, 11)
	ids := sequentialIDs(rows(data, dim))
	f := buildFlat(t, ids, data, dim)
	ivf, _, err := TrainIVF(ids, data, dim, KMeansConfig{K: 32, Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	q := data[:dim]
	wantExact, _ := f.Search(q, 10, nil)
	wantIVF, _ := ivf.Search(q, 10, 4, nil)
	var wg sync.WaitGroup
	errs := make(chan string, 16)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				a, _ := f.Search(q, 10, nil)
				b, _ := ivf.Search(q, 10, 4, nil)
				if a[0] != wantExact[0] || b[0] != wantIVF[0] {
					errs <- "concurrent search returned a different result"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

func benchmarkData(b *testing.B, n, dim int) ([]int64, []float32, []float32) {
	b.Helper()
	data := synth.Clustered(n, dim, n/100, 1, 1)
	queries := synth.Queries(256, dim, n/100, 1, 1)
	return sequentialIDs(n), data, queries
}

func BenchmarkFlatSearch(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		ids, data, queries := benchmarkData(b, n, 384)
		f := buildFlat(b, ids, data, 384)
		b.Run(sizeName(n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				q := queries[(i%256)*384 : (i%256+1)*384]
				f.Search(q, 10, nil)
			}
		})
	}
}

func BenchmarkIVFSearch(b *testing.B) {
	const n, dim = 100_000, 384
	ids, data, queries := benchmarkData(b, n, dim)
	ivf, _, err := TrainIVF(ids, data, dim, KMeansConfig{K: DefaultNList(n), Seed: 1})
	if err != nil {
		b.Fatal(err)
	}
	for _, nprobe := range []int{1, 8, 32} {
		b.Run("nprobe="+itoa(nprobe), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				q := queries[(i%256)*dim : (i%256+1)*dim]
				ivf.Search(q, 10, nprobe, nil)
			}
		})
	}
}

func sizeName(n int) string { return "n=" + itoa(n) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestSeedPlusPlusSpreadsCentroids(t *testing.T) {
	// Two far-apart blobs: k-means++ with K=2 should seed one centroid in each.
	const dim = 8
	data := synth.Clustered(400, dim, 2, 0.05, 12)
	r := rand.New(rand.NewSource(1))
	c := seedPlusPlus(data, dim, 2, r, 1)
	if s := vector.Dot(c[:dim], c[dim:]); s > 0.9 {
		t.Fatalf("seeded centroids nearly identical (cos %.3f)", s)
	}
}
