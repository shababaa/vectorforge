package main

import (
	"math"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"byodb/vectorforge/hrtime"
	"byodb/vectorforge/index"
	"byodb/vectorforge/vector"
)

// Latency summarizes per-query latencies in microseconds. Percentiles use
// the nearest-rank method on the sorted samples.
type Latency struct {
	N    int     `json:"n"`
	Mean float64 `json:"mean_us"`
	P50  float64 `json:"p50_us"`
	P95  float64 `json:"p95_us"`
	P99  float64 `json:"p99_us"`
	Max  float64 `json:"max_us"`
}

func summarize(samples []time.Duration) Latency {
	if len(samples) == 0 {
		return Latency{}
	}
	s := append([]time.Duration(nil), samples...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	us := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e3 }
	rank := func(p float64) float64 {
		i := int(math.Ceil(p/100*float64(len(s)))) - 1
		return us(s[max(0, min(i, len(s)-1))])
	}
	var sum time.Duration
	for _, d := range s {
		sum += d
	}
	return Latency{N: len(s), Mean: us(sum) / float64(len(s)), P50: rank(50), P95: rank(95), P99: rank(99), Max: us(s[len(s)-1])}
}

// searchFn answers query qi.
type searchFn func(qi int) ([]vector.Hit, index.SearchStats)

// Serial runs every query once after warm-up, timing each individually.
type Serial struct {
	Latency    Latency
	QPS        float64 // 1 / mean latency on one goroutine
	Candidates float64 // mean vectors scored per query
	Hits       [][]vector.Hit
}

func runSerial(nq int, search searchFn) Serial {
	for i := 0; i < min(nq, 50); i++ { // warm caches and branch predictors
		search(i)
	}
	samples := make([]time.Duration, nq)
	hits := make([][]vector.Hit, nq)
	var candidates int
	for i := 0; i < nq; i++ {
		start := hrtime.Now()
		h, st := search(i)
		samples[i] = hrtime.Since(start)
		hits[i] = h
		candidates += st.Candidates
	}
	lat := summarize(samples)
	return Serial{Latency: lat, QPS: 1e6 / lat.Mean, Candidates: float64(candidates) / float64(nq), Hits: hits}
}

// Concurrent is throughput with several goroutines sharing one index.
type Concurrent struct {
	Workers int     `json:"workers"`
	Queries int     `json:"queries"`
	QPS     float64 `json:"qps"`
	Latency Latency `json:"latency"`
	Speedup float64 `json:"speedup_vs_1_worker,omitempty"`
}

func runConcurrent(workers, total, nq int, search searchFn) Concurrent {
	var next atomic.Int64
	samples := make([]time.Duration, total)
	var wg sync.WaitGroup
	start := hrtime.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= total {
					return
				}
				t := hrtime.Now()
				search(i % nq)
				samples[i] = hrtime.Since(t)
			}
		}()
	}
	wg.Wait()
	elapsed := hrtime.Since(start)
	return Concurrent{Workers: workers, Queries: total, QPS: float64(total) / elapsed.Seconds(), Latency: summarize(samples)}
}

func meanRecall(exact, approx [][]vector.Hit, k int) float64 {
	var sum float64
	for i := range exact {
		sum += index.RecallAtK(exact[i], approx[i], k)
	}
	return sum / float64(len(exact))
}

// heapAfterGC returns live heap bytes, for measuring what an index retains.
func heapAfterGC() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func ms(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1e6 }
