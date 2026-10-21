// Command vfbench measures VectorForge's exact and IVF indexes.
//
//	vfbench -suite synthetic            # sizes, nlist x nprobe sweeps, top-k, concurrency
//	vfbench -suite dims                 # dimension sweep at 100k vectors
//	vfbench -suite storage              # byodb persistence: insert, size, cold load vs retrain
//	vfbench -suite real -real dir[,dir] # exported model embeddings + run files for relevance
//
// Results are written as JSON (complete, machine-readable) and CSV tables to
// -out. Every latency is a per-query wall-clock measurement of the in-memory
// index search on one goroutine unless the table says otherwise.
package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"byodb/vectorforge/hrtime"
	"byodb/vectorforge/index"
)

// Env records where a benchmark ran.
type Env struct {
	GoVersion   string `json:"go_version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	CPU         string `json:"cpu"`
	NumCPU      int    `json:"num_cpu"`
	GOMAXPROCS  int    `json:"gomaxprocs"`
	Commit      string `json:"git_commit"`
	Dirty       bool   `json:"git_dirty"` // uncommitted changes outside vectorforge/results and docs
	TimerSource string `json:"timer"`
}

// Report is one vfbench invocation.
type Report struct {
	Tool        string          `json:"tool"`
	Suite       string          `json:"suite"`
	Quick       bool            `json:"quick"`
	Env         Env             `json:"environment"`
	StartedAt   time.Time       `json:"started_at"`
	WallSeconds float64         `json:"wall_seconds"`
	Methodology []string        `json:"methodology"`
	Datasets    []DatasetReport `json:"datasets,omitempty"`
	Storage     *StorageReport  `json:"storage,omitempty"`
}

var methodology = []string{
	"Latency: each query is timed individually with a QueryPerformanceCounter-based clock (100 ns resolution; Go's monotonic clock on Windows advances in ~0.5 ms steps) after 50 warm-up queries; percentiles are nearest-rank.",
	"Scope: index search only (scoring + top-k selection) in memory. Query embedding, HTTP and chunk-text fetches are excluded; the API reports those separately.",
	"QPS (serial) = 1 / mean single-goroutine latency. Concurrent QPS = queries completed / wall time with W goroutines sharing one read-only index.",
	"Recall@k: |IVF top-k ∩ exact top-k| / k averaged over queries, against exact search on the same vectors (ANN recall, not semantic relevance).",
	"Build time: k-means training (sample of at most 256 points per list, <=25 Lloyd iterations, seed 42) plus assignment of every vector.",
	"Memory: estimate = vector payload + IDs + map overhead; measured = live heap delta after two GCs around the build.",
	"Synthetic data: Gaussian blobs around 1000 random unit centres (expected cosine to centre ~0.71) or isotropic unit vectors; queries are fresh draws from the same distribution, never dataset points.",
}

func environment() Env {
	e := Env{GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, NumCPU: runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0), TimerSource: "Go monotonic clock"}
	if runtime.GOOS == "windows" {
		e.TimerSource = "QueryPerformanceCounter"
		if out, err := exec.Command("reg", "query", `HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0`, "/v", "ProcessorNameString").Output(); err == nil {
			if i := strings.Index(string(out), "REG_SZ"); i >= 0 {
				e.CPU = strings.TrimSpace(string(out[i+len("REG_SZ"):]))
			}
		}
	} else if raw, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "model name") {
				e.CPU = strings.TrimSpace(line[strings.Index(line, ":")+1:])
				break
			}
		}
	}
	if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
		e.Commit = strings.TrimSpace(string(out))
	}
	// Dirty means uncommitted source changes; result files and docs written by
	// earlier runs do not count.
	if out, err := exec.Command("git", "status", "--porcelain", "--",
		":(exclude)vectorforge/results", ":(exclude)docs").Output(); err == nil {
		e.Dirty = len(strings.TrimSpace(string(out))) > 0
	}
	return e
}

func main() {
	suite := flag.String("suite", "synthetic", "synthetic | dims | storage | real")
	out := flag.String("out", "vectorforge/results/bench", "output directory")
	nq := flag.Int("queries", 1000, "queries per dataset")
	quick := flag.Bool("quick", false, "small sizes for a fast check")
	realDirs := flag.String("real", "", "comma-separated export directories for -suite real")
	sizes := flag.String("sizes", "10000,100000,500000", "dataset sizes for -suite synthetic")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	logf := func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }
	rep := Report{Tool: "vfbench", Suite: *suite, Quick: *quick, Env: environment(), StartedAt: time.Now().UTC(), Methodology: methodology}
	started := hrtime.Now()
	workers := []int{1, 2, 4, 6, 8, 12}

	switch *suite {
	case "synthetic":
		ns := parseInts(*sizes)
		if *quick {
			ns, *nq = []int{5000, 20000}, 200
		}
		type spec struct {
			kind string
			n    int
		}
		var specs []spec
		for _, n := range ns {
			specs = append(specs, spec{"clustered", n})
		}
		specs = append(specs, spec{"isotropic", ns[min(1, len(ns)-1)]})
		for _, sp := range specs {
			t := hrtime.Now()
			d := syntheticDataset(sp.kind, sp.n, 384, *nq, 7)
			gen := ms(hrtime.Since(t))
			nlists := defaultNLists(sp.n)
			if sp.n >= 400000 {
				nlists = nlists[:3] // 4*sqrt(N) lists take minutes to train at this size
			}
			r, flat, ivfs, _ := runSweep(d, sweep{nlists: nlists, nprobes: defaultNProbes, k: 10, seed: 42}, logf)
			r.GenMillis = gen
			// Top-k and concurrency on the middle size at the sqrt(N) operating point.
			if sp.kind == "clustered" && sp.n == ns[min(1, len(ns)-1)] {
				s := index.DefaultNList(sp.n)
				np := nprobeForRecall(r.IVF, s, 0.95)
				logf("top-k and concurrency at nlist=%d nprobe=%d", s, np)
				r.TopK = topkSuite(d, flat, ivfs[s], np)
				r.Concurrency = concurrencySuite(d, flat, ivfs[s], np, workers, 4000)
			}
			rep.Datasets = append(rep.Datasets, r)
		}
	case "dims":
		n := 100000
		if *quick {
			n, *nq = 10000, 200
		}
		for _, dim := range []int{128, 384, 768} {
			d := syntheticDataset("clustered", n, dim, *nq, 7)
			s := index.DefaultNList(n)
			r, _, _, _ := runSweep(d, sweep{nlists: []int{s}, nprobes: []int{1, 2, 4, 8, 16, 32, 64}, k: 10, seed: 42}, logf)
			rep.Datasets = append(rep.Datasets, r)
		}
	case "storage":
		n := 100000
		if *quick {
			n, *nq = 10000, 200
		}
		d := syntheticDataset("clustered", n, 384, *nq, 7)
		s, err := storageSuite(d, os.TempDir(), logf)
		if err != nil {
			fail(err)
		}
		rep.Storage = &s
	case "real":
		if *realDirs == "" {
			fail(fmt.Errorf("-suite real needs -real export directories"))
		}
		for _, dir := range strings.Split(*realDirs, ",") {
			d, err := loadExport(dir, *nq)
			if err != nil {
				fail(err)
			}
			nlists := defaultNLists(d.N)
			if d.N >= 400000 {
				nlists = nlists[:3]
			}
			r, flat, ivfs, _ := runSweep(d, sweep{nlists: nlists, nprobes: defaultNProbes, k: 10, deep: true, seed: 42}, logf)
			s := index.DefaultNList(d.N)
			runs, err := realRuns(d, flat, ivfs[s], s, defaultNProbes, filepath.Join(*out, "runs"))
			if err != nil {
				fail(err)
			}
			r.Runs = runs
			np := nprobeForRecall(r.IVF, s, 0.95)
			r.Concurrency = concurrencySuite(d, flat, ivfs[s], np, workers, 4000)
			rep.Datasets = append(rep.Datasets, r)
		}
	default:
		fail(fmt.Errorf("unknown suite %q", *suite))
	}
	rep.WallSeconds = hrtime.Since(started).Seconds()

	name := *suite
	if *quick {
		name += "-quick"
	}
	if err := writeJSON(filepath.Join(*out, name+".json"), rep); err != nil {
		fail(err)
	}
	if err := writeCSVs(filepath.Join(*out, name), rep); err != nil {
		fail(err)
	}
	logf("wrote %s.json in %.0f s", filepath.Join(*out, name), rep.WallSeconds)
}

// nprobeForRecall returns the smallest nprobe reaching the target recall@10
// at nlist, or the largest measured nprobe if none does.
func nprobeForRecall(rows []IVFRow, nlist int, target float64) int {
	best := 0
	for _, r := range rows {
		if r.NList != nlist {
			continue
		}
		if r.RecallAt10 >= target {
			return r.NProbe
		}
		best = r.NProbe
	}
	return best
}

func parseInts(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || v < 1 {
			fail(fmt.Errorf("bad size %q", f))
		}
		out = append(out, v)
	}
	return out
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func f(x float64, prec int) string { return strconv.FormatFloat(x, 'f', prec, 64) }

func writeCSVs(prefix string, rep Report) error {
	tables := map[string][][]string{}
	add := func(name string, header []string, row []string) {
		if len(tables[name]) == 0 {
			tables[name] = append(tables[name], header)
		}
		tables[name] = append(tables[name], row)
	}
	for _, d := range rep.Datasets {
		ds := d.Dataset
		add("exact", []string{"dataset", "n", "dim", "queries", "k", "mean_us", "p50_us", "p95_us", "p99_us", "qps", "memory_estimate_bytes", "heap_measured_bytes"},
			[]string{ds.Name, strconv.Itoa(ds.N), strconv.Itoa(ds.Dim), strconv.Itoa(ds.Queries), strconv.Itoa(d.Exact.K),
				f(d.Exact.Latency.Mean, 1), f(d.Exact.Latency.P50, 1), f(d.Exact.Latency.P95, 1), f(d.Exact.Latency.P99, 1),
				f(d.Exact.QPS, 1), strconv.FormatInt(d.Exact.MemoryBytes, 10), strconv.FormatInt(d.Exact.HeapBytes, 10)})
		for _, b := range d.Builds {
			add("builds", []string{"dataset", "n", "dim", "nlist", "build_ms", "iterations", "converged", "objective", "sample", "imbalance", "min_list", "max_list", "memory_estimate_bytes", "heap_measured_bytes"},
				[]string{ds.Name, strconv.Itoa(ds.N), strconv.Itoa(ds.Dim), strconv.Itoa(b.NList), f(b.BuildMillis, 1), strconv.Itoa(b.Iterations),
					strconv.FormatBool(b.Converged), f(b.Objective, 4), strconv.Itoa(b.SampleSize), f(b.Balance.Imbalance, 3),
					strconv.Itoa(b.Balance.Min), strconv.Itoa(b.Balance.Max), strconv.FormatInt(b.MemoryBytes, 10), strconv.FormatInt(b.HeapBytes, 10)})
		}
		for _, r := range d.IVF {
			add("ivf", []string{"dataset", "n", "dim", "nlist", "nprobe", "recall_at_1", "recall_at_10", "recall_at_100", "mean_us", "p50_us", "p95_us", "p99_us", "qps", "fraction_scored", "speedup_vs_exact"},
				[]string{ds.Name, strconv.Itoa(ds.N), strconv.Itoa(ds.Dim), strconv.Itoa(r.NList), strconv.Itoa(r.NProbe), f(r.RecallAt1, 4),
					f(r.RecallAt10, 4), f(r.RecallAt100, 4), f(r.Latency.Mean, 1), f(r.Latency.P50, 1), f(r.Latency.P95, 1), f(r.Latency.P99, 1),
					f(r.QPS, 1), f(r.FractionScored, 4), f(r.Speedup, 2)})
		}
		for _, r := range d.TopK {
			add("topk", []string{"dataset", "k", "strategy", "nprobe", "mean_us", "p50_us", "p99_us", "qps", "recall_at_k"},
				[]string{ds.Name, strconv.Itoa(r.K), r.Strategy, strconv.Itoa(r.NProbe), f(r.Latency.Mean, 1), f(r.Latency.P50, 1), f(r.Latency.P99, 1), f(r.QPS, 1), f(r.RecallAtK, 4)})
		}
		for _, r := range d.Concurrency {
			add("concurrency", []string{"dataset", "strategy", "nprobe", "workers", "queries", "qps", "speedup", "p50_us", "p99_us"},
				[]string{ds.Name, r.Strategy, strconv.Itoa(r.NProbe), strconv.Itoa(r.Workers), strconv.Itoa(r.Queries), f(r.QPS, 1), f(r.Speedup, 2), f(r.Latency.P50, 1), f(r.Latency.P99, 1)})
		}
	}
	for name, rows := range tables {
		file, err := os.Create(prefix + "-" + name + ".csv")
		if err != nil {
			return err
		}
		w := csv.NewWriter(file)
		w.WriteAll(rows)
		file.Close()
		if err := w.Error(); err != nil {
			return err
		}
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "vfbench:", err)
	os.Exit(1)
}
