# VectorForge implementation plan and log

The working plan, kept current as each stage lands. See
[vector-search-design.md](vector-search-design.md) for the design rationale.

## Baseline (before any VectorForge code)

- Branch `vectorforge` created from `main` at `7bffe89`. Nothing is pushed.
- `go test ./... -count=1`: all 8 test packages pass (Go 1.26.0, Windows amd64,
  AMD Ryzen 5 5600, 16 GB RAM; ~14 s wall time).
- Python 3.13.3 and Node 22.15 are available. PyTorch, Sentence Transformers
  and Docker are not installed. There is no CUDA GPU (AMD RX 6700), so ML
  runs on CPU.

## Reused components

- `byodb.DB`, `DBTX`, `TableDef`, `Record`, `Scanner`, secondary indexes,
  `TableNew`, `Stats`, and `KeyCompressionStats`, all through the public API.
  No engine changes.
- The HTTP conventions of `market/service` (`{"data":…}` / `{"error":{code,message}}`
  envelopes, request IDs, bounded bodies, concurrency semaphore, security
  headers), re-implemented in a small `vectorforge/api` package. Importing
  the market package would couple the two applications.

## New packages and files

| Path | Responsibility |
| --- | --- |
| `vectorforge/vector` | Dot, cosine, L2, normalize, validation, float32 codec and segments, top-k heap |
| `vectorforge/index` | `Flat` exact index, spherical k-means, `IVF` index, recall helpers |
| `vectorforge/store` | `vf_*` schema, documents, chunks, vector segments, IVF state, ingest runs |
| `vectorforge/ingest` | File discovery, Markdown, Go (AST), Python, and text chunkers, stable IDs |
| `vectorforge/embed` | `Embedder` interface and HTTP client for the Python service |
| `vectorforge/engine` | Startup load, ingestion pipeline and jobs, search, rebuild, stats |
| `vectorforge/api` | HTTP handlers and validation |
| `cmd/vectorforge` | Server binary |
| `cmd/vfbench` | Reproducible ANN benchmark runner (JSON/CSV output) |
| `vectorforge/ml` | Python: embedding service, BM25, metrics, SciFact fine-tune and evaluation, tests |
| `vectorforge/web` | React + TypeScript UI (Vite) |
| `vectorforge/eval` | Hand-labelled developer-search queries |
| `docs/vectorforge/*` | Design, architecture, ML methodology, benchmarks, interview guide |

## Dependencies added

- Go: none (standard library only, as before).
- Python (`vectorforge/ml/requirements.txt`): torch (CPU), sentence-transformers,
  fastapi, uvicorn, numpy, pytest, httpx.
- Web: react, react-dom, vite, typescript (dev only; the Go binary serves the build).

## Testing strategy

- Unit tests for every math edge case listed in the brief. Index property
  tests: Flat equals a brute-force sort, and IVF with `nprobe = nlist` equals
  Flat exactly. Determinism tests for seeded training.
- Store tests against real database files: reopen, update, delete, duplicate
  IDs, and adversarial vectors that maximize escape expansion.
- Engine tests with a deterministic test-only embedder for API and pipeline
  logic, a reopen test asserting that the IVF was loaded rather than retrained,
  `-race` concurrent search during ingest, and a child-process crash test.
- Python tests for metrics (hand-computed), BM25, preprocessing, real-model
  dimension, normalization, and batch consistency, plus FastAPI responses.
- An end-to-end script that uses the real model and a real restart.

## Major risks

1. CPU-only fine-tuning time. Mitigation: a small model (22M parameters), at
   most 256 tokens, and SciFact's small training split.
2. Engine commit cost for large batches. Mitigation: bounded batches, with
   time measured before choosing sizes.
3. Fine-tuning may not beat the pretrained model. Mitigation: report the
   result truthfully. Selection uses validation data only.
4. Windows-specific filesystem behaviour (no directory fsync). This is
   documented already in `DURABILITY.md`.

## Stages

- [x] 1. Vector math and record codec (`53f8ba8`)
- [x] 2. Persistent vector store (`53f8ba8`)
- [x] 3. Exact search (`53f8ba8`)
- [x] 4. IVF-Flat: k-means, persisted centroids and postings, incremental
      assignment, deterministic rebuild (`53f8ba8`, `cb1d77d`)
- [x] 5. Embedding service (`cb1d77d`)
- [x] 6. Ingestion pipeline (`53f8ba8`, `cb1d77d`, `225b2c0`)
- [x] 7. Search API and server (`225b2c0`)
- [x] 8. ML evaluation and fine-tuning (`9e059d2`, `b5ebf7d`)
- [x] 9. Frontend (`a2a1e24`)
- [x] 10. Benchmarks and documentation (`a2a1e24`, `b5ebf7d`, `538c6b0`, final commit)
- [ ] P2: HNSW — not implemented
- [ ] P2: FAISS reference comparison — not run
- [ ] P2: Docker packaging — not provided (Docker is not installed here)

## Progress log

1. **Audit and design.** Read byodb's engine, table layer and the market
   service; ran the existing suite (green). Wrote
   [vector-search-design.md](vector-search-design.md). Key finding: the key
   codec escapes 0x00/0x01, so a float32 payload can double in size and
   vectors must be split into 256-float segments to stay under the 3,000-byte
   value limit.
2. **`53f8ba8` — core.** `vector` (dot, cosine, L2, normalize, validation,
   segment codec, top-k heap), `index` (`Flat`, spherical k-means with
   k-means++ seeding, `IVF`), `store` (`vf_*` tables and transactional
   operations), `ingest` (discovery and the Markdown/Go/Python/text chunkers).
   Property tests: Flat equals a sorted brute force; IVF with
   `nprobe = nlist` equals Flat; seeded training is deterministic.
3. **`cb1d77d` — service and engine.** FastAPI embedding service
   (`/health`, `/embed`, `/embed/batch`) with a pinned model revision; Go
   client; `engine` with startup load (IVF from postings, never retrained),
   batched transactional ingest, search, background rebuild, `verify`, and
   startup repair counts. `TestCrashMidIngestRecovers` kills a child test
   process mid-ingest and checks recovery.
4. **`225b2c0` — API and server.** HTTP API with the market service's
   envelope conventions, `cmd/vectorforge`, `vfctl.py`, the demo source
   definition. Added the QueryPerformanceCounter clock after finding Go's
   monotonic clock on Windows ticks in ~0.5 ms steps. Switched chunk budgets
   from one byte budget to per-kind budgets after measuring with the real
   tokenizer: truncated chunks fell from 65 % to 16 %.
5. **`9e059d2` — ML.** Pinned BEIR SciFact download, train/validation split
   of the official training queries, BM25 baseline, BM25-mined hard
   negatives, InfoNCE fine-tuning, validation-only checkpoint selection,
   paired bootstrap comparisons.
6. **`a2a1e24` — benchmarks and UI.** `cmd/vfbench` (synthetic, dims,
   storage and real suites; JSON and CSV output) and the React + TypeScript
   UI (search with exact-vs-IVF comparison, documents and ingest jobs,
   index/benchmark dashboard) under the server's strict CSP.
7. **`b5ebf7d` — evaluation tooling.** `vfml.export` (real-embedding exports
   for vfbench, labels never exported), `vfml.annrelevance` (scores exact and
   IVF run files against BEIR labels), `vfml.codesearch` with a hand-labelled
   44-query set, `e2e.py` (kill/restart/mutate/kill/restart), `apibench.py`.
8. **`538c6b0` — measurements.** SciFact experiment (43.5 min), Quora export
   (523k vectors), all vfbench suites on an idle machine, ANN-vs-relevance
   scoring, interleaved encode latency, e2e (all checks pass), code-search
   evaluation, API benchmark. Docs: architecture, ML methodology, generated
   benchmark tables and charts.
9. **Final commit — documentation and verification.** benchmarks.md
   narrative, interview guide completed, READMEs, this log; two further API
   benchmark runs (default and 4 torch threads) after the first showed
   half-second tail spikes; full verification (below).

## Decisions and deviations

- **No engine or market changes.** Everything uses byodb's public API.
  Outside `vectorforge/`, `cmd/vectorforge`, `cmd/vfbench` and
  `docs/vectorforge/`, only `.gitignore` and the root README changed since
  `7bffe89`.
- **Separate database file, guarded.** VectorForge refuses to open a file
  containing any table outside `vf_*`, so it cannot write into the market
  database. Mutations from non-loopback clients need an API token.
- **IVF-Flat, not HNSW.** IVF's state maps onto rows and rebuilds
  deterministically; HNSW was P2 and was not reached.
- **Single writer, fail fast.** A second concurrent mutation gets HTTP 409
  rather than queueing behind a long ingest.
- **Recall claims on real embeddings only.** Synthetic clustered data turned
  out to be IVF's best case (recall 1.0 at one probe) and isotropic data its
  worst case, so both are reported as brackets, and Quora (523k real
  vectors) was added because SciFact (5k) is too small to show ANN behaviour
  at scale.
- **Test labels never used for training or selection.** Validation is 15 %
  of BEIR's training queries (seed 2027).
- **Fine-tuned model evaluated, not served.** It is tuned for scientific
  claims; the demo corpus is code.
- **Latency re-measured.** The fine-tuned models' encode latency in the
  first SciFact run overlapped other work, so all three models were
  re-measured interleaved on an idle machine (`encode_latency.py`).
- **Benchmark provenance.** All vfbench JSON records commit `b5ebf7d` with
  `git_dirty: true`; the dirty files were Python and docs only (no Go source
  changed after `b5ebf7d`).
- **Interview guide.** The brief's list of 15 questions was not available
  when the guide was written, so it covers the topics the brief named
  (22 questions).

## Known gaps

- IVF persist is slow (24.3 s for 100k postings, more than training) and has
  not been profiled.
- No SIMD or quantization: exact search is scalar, and multi-threaded
  throughput on large collections levels off at ~30 GB/s of vector reads.
- One training seed per configuration; the hard-negative vs in-batch gap is
  small and single-seed.
- The API benchmark shows intermittent tail spikes with the embedder's
  default thread count; one run with 4 threads did not, which is suggestive
  but not conclusive.

## Final verification

Run before the final commit (Go 1.26.0, Windows amd64):

| check | result |
|---|---|
| `go vet ./...` | clean |
| `gofmt -l vectorforge cmd/vectorforge cmd/vfbench` | clean |
| `go test ./... -count=1` (whole repository, market service included) | 17 packages with tests, all pass |
| `go test -race` on `./vectorforge/...` and `./cmd/vfbench` (llvm-mingw clang) | all pass |
| `go vet` + `go test` on the same packages with Go 1.22.12 (the `go.mod` minimum) | all pass |
| `pytest` in `vectorforge/ml` | 22 passed |
| `npm run build` in `vectorforge/web` (includes `tsc --noEmit`) | passes |
| `scripts/e2e.py` (`results/e2e/e2e.json`, Go sources as of `b5ebf7d`) | 17 of 17 checks pass |

VectorForge has 83 Go test functions and 6 Go benchmarks across 9 packages.
