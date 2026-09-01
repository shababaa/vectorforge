# Benchmark methodology

Run all correctness checks first:

```sh
go test ./...
go test -race ./...
go vet ./...
```

Then collect allocation-aware microbenchmarks:

```sh
go test ./market -run '^$' \
  -bench 'BenchmarkCandle(Ingestion|RangeScan)$' \
  -benchmem -count 3 -benchtime=5x
```

The ingestion benchmark writes batches of 100 candles. The range benchmark
loads 2,000 candles in bounded setup batches and repeatedly retrieves the most
recent 252 through the composite primary index.

Record CPU, Go version, commit, sample count, and raw output whenever publishing
numbers. Do not compare results across machines as if they were the same test.
The current copy-on-write engine favors educational clarity and durability over
bulk-ingestion throughput; allocation profiles should guide Phase 6 rather than
being hidden.

## Validation snapshot

Captured on 2026-08-29 with Go 1.23.12 on an AMD EPYC 9V74 runner. These are
development-machine results, not cross-machine performance claims.

| Operation | Result | Allocations |
| --- | ---: | ---: |
| Atomic ingestion of 100 candles | 26.31 ms/op median (~3.8k rows/s) | 46.82 MB, 15,806 allocs/op |
| Indexed retrieval of latest 252 candles | 2.48 ms/op median | 1.87 MB, 5,290 allocs/op |

Command used three samples of five iterations with `-benchmem`. The range latency is already suitable
for an interactive student demo; ingestion allocations are the clearest target
for a future profiling and optimization write-up.

## Phase 3 model-fit snapshot

Captured on 2026-08-31 with Go 1.23.12 on the same AMD EPYC 9V74 runner.
The benchmark fits the 346-sample, seven-feature training partition from the
600-bar synthetic fixture. It excludes database writes, tuning, calibration,
and evaluation, so these are model-fit microbenchmarks, not job throughput.

```sh
go test ./market -run '^$' -bench BenchmarkPredictionFit \
  -benchmem -count=3 -benchtime=5x
```

| Fit | Median time | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| Standardized ridge (alpha 0.1) | 64.38 microseconds | 816 | 11 |
| 64 gradient-boosted stumps | 737.41 microseconds | 225,816 | 94 |

Three short samples are a development snapshot, not a statistically robust
performance comparison. Use longer runs and profiles before making an
optimization or production-latency claim.

## Phase 5 HTTP load methodology

Start a seeded server in one terminal, then run the bounded read-only client in
another:

```sh
go run ./cmd/marketserver -db service-demo.db -seed-demo
go run ./cmd/marketload -duration 10s -concurrency 16
```

The target is the overview query with 120 candles plus feature, forecast, model,
analysis, and storage summaries. The client reuses HTTP connections and reports
status counts, response bytes, requests/second, and nearest-rank p50/p95/p99.
It exits non-zero for any transport, body-read, or non-2xx response. This tests
the complete service/repository/serialization path, not browser rendering.

Before publishing a result, record CPU, operating system, Go version, commit,
database size, command, warm/cold-cache state, and raw JSON. Loopback throughput
is not internet latency, and multiple workers do not make the embedded engine a
distributed database. The current measured acceptance snapshot is in
[PHASE5_VALIDATION.md](PHASE5_VALIDATION.md).
