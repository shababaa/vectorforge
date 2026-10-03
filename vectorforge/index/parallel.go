package index

import (
	"runtime"
	"sync"
)

// parallelRange splits [0, n) into at most workers contiguous ranges and runs
// fn on each concurrently. Ranges depend only on n and workers, so work that
// writes to disjoint per-index outputs is deterministic.
func parallelRange(n, workers int, fn func(start, end int)) {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	workers = min(workers, n)
	if workers <= 1 {
		if n > 0 {
			fn(0, n)
		}
		return
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for start := 0; start < n; start += chunk {
		end := min(start+chunk, n)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			fn(start, end)
		}(start, end)
	}
	wg.Wait()
}
