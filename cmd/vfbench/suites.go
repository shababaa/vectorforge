package main

import (
	"fmt"
	"math"

	"byodb/vectorforge/hrtime"
	"byodb/vectorforge/index"
	"byodb/vectorforge/synth"
	"byodb/vectorforge/vector"
)

// Dataset is a corpus matrix plus held-out query vectors.
type Dataset struct {
	Name     string  `json:"name"`
	Kind     string  `json:"kind"` // clustered, isotropic, real
	N        int     `json:"n"`
	Dim      int     `json:"dim"`
	Queries  int     `json:"queries"`
	Clusters int     `json:"clusters,omitempty"`
	Spread   float64 `json:"spread,omitempty"`
	Seed     int64   `json:"seed,omitempty"`
	Source   string  `json:"source,omitempty"`
	Corpus   string  `json:"corpus,omitempty"` // real data: dataset name from meta.json
	Model    string  `json:"model,omitempty"`  // real data: model label from meta.json

	ids      []int64
	data     []float32
	queries  []float32
	docIDs   []string
	queryIDs []string
	split    string
}

func (d *Dataset) query(i int) []float32 { return d.queries[i*d.Dim : (i+1)*d.Dim] }

func syntheticDataset(kind string, n, dim, nq int, seed int64) *Dataset {
	clusters, spread := 1000, 1.0
	if kind == "isotropic" {
		clusters, spread = 0, 0
	}
	d := &Dataset{Name: fmt.Sprintf("%s-%dk-%dd", kind, n/1000, dim), Kind: kind, N: n, Dim: dim, Queries: nq,
		Clusters: clusters, Spread: spread, Seed: seed}
	d.data = synth.Clustered(n, dim, clusters, spread, seed)
	d.queries = synth.Queries(nq, dim, clusters, spread, seed)
	d.ids = make([]int64, n)
	for i := range d.ids {
		d.ids[i] = int64(i + 1)
	}
	return d
}

// ExactReport is the brute-force baseline, which is also ground truth.
type ExactReport struct {
	K           int     `json:"k"`
	Latency     Latency `json:"latency"`
	QPS         float64 `json:"qps"`
	BuildMillis float64 `json:"build_ms"`
	MemoryBytes int64   `json:"memory_estimate_bytes"`
	HeapBytes   int64   `json:"heap_measured_bytes"`
}

// IVFBuild describes one trained index.
type IVFBuild struct {
	NList        int           `json:"nlist"`
	BuildMillis  float64       `json:"build_ms"` // k-means training + assignment of every vector
	Iterations   int           `json:"iterations"`
	Converged    bool          `json:"converged"`
	Objective    float64       `json:"objective_mean_cosine"`
	SampleSize   int           `json:"sample_size"`
	EmptyReseeds int           `json:"empty_reseeds"`
	MemoryBytes  int64         `json:"memory_estimate_bytes"`
	HeapBytes    int64         `json:"heap_measured_bytes"`
	Balance      index.Balance `json:"balance"`
}

// IVFRow is one (nlist, nprobe) operating point.
type IVFRow struct {
	NList          int     `json:"nlist"`
	NProbe         int     `json:"nprobe"`
	FractionProbed float64 `json:"fraction_of_lists_probed"`
	RecallAt1      float64 `json:"recall_at_1"`
	RecallAt10     float64 `json:"recall_at_10"`
	RecallAt100    float64 `json:"recall_at_100,omitempty"`
	Latency        Latency `json:"latency"`
	QPS            float64 `json:"qps"`
	Candidates     float64 `json:"mean_candidates_scored"`
	FractionScored float64 `json:"fraction_of_vectors_scored"`
	Speedup        float64 `json:"speedup_vs_exact"`
}

// TopKRow measures how k affects latency and recall.
type TopKRow struct {
	K          int     `json:"k"`
	Strategy   string  `json:"strategy"`
	NProbe     int     `json:"nprobe,omitempty"`
	Latency    Latency `json:"latency"`
	QPS        float64 `json:"qps"`
	RecallAtK  float64 `json:"recall_at_k"`
	Candidates float64 `json:"mean_candidates_scored"`
}

// DatasetReport is everything measured on one dataset.
type DatasetReport struct {
	Dataset     Dataset       `json:"dataset"`
	GenMillis   float64       `json:"generate_ms,omitempty"`
	Exact       ExactReport   `json:"exact"`
	Builds      []IVFBuild    `json:"ivf_builds"`
	IVF         []IVFRow      `json:"ivf"`
	TopK        []TopKRow     `json:"topk,omitempty"`
	Concurrency []ConcRow     `json:"concurrency,omitempty"`
	Runs        []RunFileInfo `json:"run_files,omitempty"`
}

// ConcRow is throughput under concurrency for one strategy.
type ConcRow struct {
	Strategy string `json:"strategy"`
	NProbe   int    `json:"nprobe,omitempty"`
	Concurrent
}

type sweep struct {
	nlists  []int
	nprobes []int
	k       int
	deep    bool // also measure recall@100 with k=100 searches
	seed    int64
	keep    int // the one IVF returned to the caller; 0 = DefaultNList(N). Others are dropped to bound memory.
}

func buildFlat(d *Dataset) (*index.Flat, ExactReport) {
	before := heapAfterGC()
	start := hrtime.Now()
	flat := index.NewFlat(d.Dim)
	for i, id := range d.ids {
		if err := flat.Add(id, d.data[i*d.Dim:(i+1)*d.Dim]); err != nil {
			panic(err)
		}
	}
	rep := ExactReport{BuildMillis: ms(hrtime.Since(start)), MemoryBytes: flat.MemoryBytes()}
	rep.HeapBytes = int64(heapAfterGC()) - int64(before)
	return flat, rep
}

func buildIVF(d *Dataset, nlist int, seed int64) (*index.IVF, IVFBuild) {
	before := heapAfterGC()
	start := hrtime.Now()
	ivf, km, err := index.TrainIVF(d.ids, d.data, d.Dim, index.KMeansConfig{K: nlist, Seed: seed})
	if err != nil {
		panic(err)
	}
	b := IVFBuild{NList: nlist, BuildMillis: ms(hrtime.Since(start)), Iterations: km.Iterations, Converged: km.Converged,
		Objective: km.Objective, SampleSize: km.SampleSize, EmptyReseeds: km.EmptyReseeds,
		MemoryBytes: ivf.MemoryBytes(), Balance: ivf.Balance()}
	b.HeapBytes = int64(heapAfterGC()) - int64(before)
	return ivf, b
}

// runSweep measures exact search and every IVF operating point on d.
func runSweep(d *Dataset, sw sweep, logf func(string, ...any)) (DatasetReport, *index.Flat, map[int]*index.IVF, [][]vector.Hit) {
	rep := DatasetReport{Dataset: *d}
	flat, exact := buildFlat(d)
	exact.K = sw.k
	es := runSerial(d.Queries, func(i int) ([]vector.Hit, index.SearchStats) { return flat.Search(d.query(i), sw.k, nil) })
	exact.Latency, exact.QPS = es.Latency, es.QPS
	rep.Exact = exact
	truth := es.Hits
	var deepTruth [][]vector.Hit
	if sw.deep {
		deepTruth = make([][]vector.Hit, d.Queries)
		for i := range deepTruth {
			deepTruth[i], _ = flat.Search(d.query(i), 100, nil)
		}
	}
	logf("%s exact: p50 %.0f µs, %.0f QPS", d.Name, exact.Latency.P50, exact.QPS)

	ivfs := map[int]*index.IVF{}
	keep := sw.keep
	if keep == 0 {
		keep = index.DefaultNList(d.N)
	}
	for _, nlist := range sw.nlists {
		if nlist > d.N {
			continue
		}
		ivf, build := buildIVF(d, nlist, sw.seed)
		if nlist == keep {
			ivfs[nlist] = ivf
		}
		rep.Builds = append(rep.Builds, build)
		logf("%s nlist=%d built in %.0f ms (%d iterations, imbalance %.2f)", d.Name, nlist, build.BuildMillis, build.Iterations, build.Balance.Imbalance)
		for _, nprobe := range sw.nprobes {
			if nprobe > nlist {
				continue
			}
			s := runSerial(d.Queries, func(i int) ([]vector.Hit, index.SearchStats) { return ivf.Search(d.query(i), sw.k, nprobe, nil) })
			row := IVFRow{NList: nlist, NProbe: nprobe, FractionProbed: float64(nprobe) / float64(nlist),
				RecallAt1: meanRecall(truth, s.Hits, 1), RecallAt10: meanRecall(truth, s.Hits, min(10, sw.k)),
				Latency: s.Latency, QPS: s.QPS, Candidates: s.Candidates, FractionScored: s.Candidates / float64(d.N),
				Speedup: exact.Latency.Mean / s.Latency.Mean}
			if sw.deep {
				approx := make([][]vector.Hit, d.Queries)
				for i := range approx {
					approx[i], _ = ivf.Search(d.query(i), 100, nprobe, nil)
				}
				row.RecallAt100 = meanRecall(deepTruth, approx, 100)
			}
			rep.IVF = append(rep.IVF, row)
			logf("  nprobe=%-4d recall@10 %.3f  p50 %7.0f µs  p99 %7.0f µs  %6.0f QPS  scored %.1f%%",
				nprobe, row.RecallAt10, row.Latency.P50, row.Latency.P99, row.QPS, 100*row.FractionScored)
		}
	}
	return rep, flat, ivfs, truth
}

func defaultNLists(n int) []int {
	s := int(math.Round(math.Sqrt(float64(n))))
	return []int{max(1, s/2), s, 2 * s, 4 * s}
}

var defaultNProbes = []int{1, 2, 4, 8, 16, 32, 64, 128, 256}

func topkSuite(d *Dataset, flat *index.Flat, ivf *index.IVF, nprobe int) []TopKRow {
	var rows []TopKRow
	for _, k := range []int{1, 10, 50, 100} {
		e := runSerial(d.Queries, func(i int) ([]vector.Hit, index.SearchStats) { return flat.Search(d.query(i), k, nil) })
		a := runSerial(d.Queries, func(i int) ([]vector.Hit, index.SearchStats) { return ivf.Search(d.query(i), k, nprobe, nil) })
		rows = append(rows,
			TopKRow{K: k, Strategy: "exact", Latency: e.Latency, QPS: e.QPS, RecallAtK: 1, Candidates: e.Candidates},
			TopKRow{K: k, Strategy: "ivf", NProbe: nprobe, Latency: a.Latency, QPS: a.QPS, RecallAtK: meanRecall(e.Hits, a.Hits, k), Candidates: a.Candidates})
	}
	return rows
}

func concurrencySuite(d *Dataset, flat *index.Flat, ivf *index.IVF, nprobe int, workers []int, total int) []ConcRow {
	var rows []ConcRow
	for _, strat := range []string{"exact", "ivf"} {
		search := func(i int) ([]vector.Hit, index.SearchStats) { return flat.Search(d.query(i), 10, nil) }
		np := 0
		if strat == "ivf" {
			np = nprobe
			search = func(i int) ([]vector.Hit, index.SearchStats) { return ivf.Search(d.query(i), 10, nprobe, nil) }
		}
		runConcurrent(1, min(total, 200), d.Queries, search) // warm-up
		var base float64
		for _, w := range workers {
			n := total
			if strat == "exact" {
				n = max(total/4, 4*w) // exact is slow; fewer queries still give stable QPS
			}
			c := runConcurrent(w, n, d.Queries, search)
			if w == 1 {
				base = c.QPS
			}
			c.Speedup = c.QPS / base
			rows = append(rows, ConcRow{Strategy: strat, NProbe: np, Concurrent: c})
		}
	}
	return rows
}
