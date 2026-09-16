# Phase 6 validation record

Phase 6 deepens the database engine without pretending that a student project
is a distributed or production trading database. All measurements below were
captured on 2026-09-02 with Go 1.23.12, Linux/amd64, and an AMD EPYC 9V74
runner. They compare the same machine and benchmark shape; they are not latency
promises for another computer.

## Delivered surface

| Area | Implementation | Acceptance evidence |
| --- | --- | --- |
| Process ownership | Non-blocking exclusive writer and shared reader locks on Unix/Windows | Second writer and reader-beside-writer return `ErrDatabaseLocked`; two readers coexist |
| Replica workflow | Pager-mutex snapshot, temp-file `fsync`, atomic rename, parent-directory sync | Snapshot opens read-only while the independent primary writer remains open |
| Read-only mode | `OpenDBReadOnly`, `DBOptions`, CLI and HTTP replica modes | Reads and no-op commits work; a commit containing writes returns `ErrReadOnly` |
| Batch maintenance | Mixed-table `ApplyBatch`, bounded `DeleteRange`, physical prefix deletion, `TableDrop`/`DROP TABLE` | Abort is atomic; secondary keys and catalog entries disappear; prefixes are not reused after reopen |
| Format evolution | Format-v3 prefix-compressed leaf type; format-v2 metadata/pages remain readable | Insert/delete/reopen test validates the complete compressed tree |
| Crash testing | Child process exits at three pager publication boundaries | Old/new-root atomicity holds at every tested boundary |
| Long recovery | 30 commit/close/reopen rounds with 250 changing keys | Final reference map and B+tree invariants match |
| Fuzzing | Codec, B+tree state machine, and parser targets | 3-second local runs completed without a crash or invariant failure |
| Dashboard regression | Explicit `ForecastView` wire DTO plus raw JSON test | Required snake-case fields exist; Go names cannot leak and create chart-wide `NaN` values |

## Screenshot regression explained

The supplied dashboard proved that candles, features, model cards, and analyst
evidence were persisted and served. The forecast panel showed dashes and the
Canvas showed no price line even though the analyst quoted a stored forecast.

`market.Forecast` had no JSON tags, so Go emitted `PredictedClose`,
`LowerBound`, and similar names. The browser requested `predicted_close` and
`lower_bound`. The forecast object itself existed, so JavaScript divided
`undefined` by the price scale; one `NaN` bound made chart minimum/maximum and
every y-coordinate invalid. The service now maps domain forecasts to a tagged
`ForecastView`, a raw-wire test requires the exact snake-case names, and the
chart ignores a malformed forecast instead of hiding valid candle history.

The server log was consistent with this diagnosis: seeding completed, the
overview route returned HTTP 200 with a 43,477-byte body, and a later request
completed in 12 ms. The unrelated 404 was another browser resource request and
did not cause the render failure.

## Lock and replica matrix

| Existing owner | New writer | New read-only handle |
| --- | --- | --- |
| None | Allowed | Allowed |
| Writer | Rejected | Rejected |
| Read-only handle(s) | Rejected | Allowed |

The lock covers the database file, not a deletable sidecar. Unix uses advisory
`flock`; Windows uses `LockFileEx`. Every byodb process follows this protocol,
but unrelated programs can ignore Unix advisory locks. A read-only service uses
an independent snapshot file, so it does not need metadata-refresh semantics or
claim to be a live replica.

## Crash-boundary result

The test executable launches itself as a child and calls `os.Exit(86)` at the
selected stage, preventing Go cleanup from making the test accidentally safer.

| Injected exit | Required recovery result | Observed |
| --- | --- | --- |
| After data/free pages sync, before metadata write | Old root only | Pass |
| After metadata write, before metadata sync | Old or complete new root | Pass |
| After metadata sync | New root | Pass |

The middle state is deliberately nondeterministic: a kernel may later persist a
write that was not explicitly synced. Both meta pages are checksummed, so the
contract is atomic old-or-new visibility, never a partially decoded root.

## Reproducible benchmark evidence

Run:

```sh
go test ./ -run '^$' -bench '^BenchmarkBTreeSequentialInsert$' \
  -benchmem -count=3 -benchtime=20x

go test ./market -run '^$' \
  -bench 'BenchmarkCandle(Ingestion|RangeScan)$' \
  -benchmem -count=3 -benchtime=5x
```

### Engine microbenchmark

One operation constructs a fresh tree and inserts 1,000 ordered keys shaped
like `SYNTH:1d:<timestamp>`.

| Revision | Median time | Bytes/op | Allocations/op |
| --- | ---: | ---: | ---: |
| Phase 5 parent | 74.42 ms | 179,024,230 | 30,844 |
| Phase 6 | 17.36 ms | 26,856,883 | 10,288 |
| Change | **76.7% lower** | **85.0% lower** | **66.6% lower** |

The main cause was visible in allocation profiling: the split search built two
temporary nodes for each candidate boundary. It now computes encoded sizes,
chooses the most balanced valid compressed split, and materializes only the
selected nodes.

### Market repository benchmark

| Operation | Phase 5 median | Phase 6 median | Allocation result |
| --- | ---: | ---: | ---: |
| Atomic ingestion of 100 candles | 26.31 ms | 8.03 ms | 46.82 MB to 14.31 MB; 15,806 to 12,105 allocs |
| Indexed retrieval of latest 252 | 2.48 ms | 0.338 ms | 1.87 MB to 0.546 MB; 5,290 to 6,340 allocs |

The range path became much faster and used fewer bytes, but reconstructed
compressed keys increased the allocation count by about 19.8%. That remaining
tradeoff is published instead of hidden; a future iterator could expose key
views or reuse caller buffers.

### Compression fixture

The recovery fixture inserted 3,000 time-series-shaped keys:

| Live leaves | Compressed leaves | Logical bytes | Stored bytes | Saved |
| ---: | ---: | ---: | ---: | ---: |
| 55 | 54 | 204,234 | 112,330 | 91,904 (45.0%) |

The figure measures encoded live-node bytes, not the whole file. Fixed 4 KiB
pages, two metadata pages, free pages, and internal nodes remain part of file
size.

## Fuzz snapshot

Local three-second runs completed with:

- codec: 28,256 executions;
- B+tree operations: 53,004 executions; and
- parser: 150,875 executions.

Fuzz counts depend on CPU scheduling and are not coverage percentages. CI runs
each target for three seconds as a smoke test; `make fuzz` runs five seconds per
target for local work.

## Commands run before handoff

```sh
test -z "$(gofmt -l .)"
go vet ./...
go test ./...
go test -race ./...
GOOS=windows GOARCH=amd64 go vet ./...
```

The GitHub Actions matrix repeats formatting, vet, tests, and Linux race/fuzz
checks on Ubuntu and compile/tests on Windows. Container construction remains a
separate CI job.

All durability claims are qualified in [DURABILITY.md](DURABILITY.md).
