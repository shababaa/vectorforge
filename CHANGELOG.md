# Changelog

## v0.9.0 — Five-year multi-asset acceptance

- Added a frozen Yahoo Chart daily snapshot for AAPL, MSFT, and SPY covering
  2021-08-30 through 2026-08-27, with file hashes recorded in the Phase 2
  validation note.
- Added `TestFiveYearUniverseAcceptance`, which imports each file, checks an
  idempotent reimport, and requires 1,254 observed NYSE sessions, zero missing
  dates, zero unexpected dates, and full coverage.
- Recorded 1,234 incremental feature snapshots and zero 25% log-return outliers
  per symbol. This closes the Phase 2 multi-asset exit check without a personal
  Twelve Data key. It is not a live provider sync and not a trading result.
- Recorded a local Ollama evaluation of `qwen2.5:1.5b`: 6/10 labelled decisions,
  zero completion errors, and four fail-closed rejections, including both
  labelled abstentions. That score is contract compliance, not financial accuracy.
- Accepted the live SEC submissions CIK string and fractional-second timestamps.
  A live AAPL metadata fetch retained 20 recent 10-K/10-Q/8-K rows.

## v0.8.0 — Range-iterator key reuse

- Stopped allocating a logical key on every visit to a prefix-compressed leaf.
  Each B+tree cursor keeps one key buffer; uncompressed keys remain page views.
- Sentinel detection uses the on-page key length, so iterator validity checks
  no longer materialize the key.
- Kept `KVIterator.Deref` as an independent copy. Callers can retain a key and
  value across `Next`.
- Added forward, reverse, and allocation-ceiling coverage for compressed scans.
- Recorded a same-machine range-scan before/after: 5,587 to 4,229 allocs/op and
  528,512 to 496,025 bytes/op, with no latency claim. Corrected the architecture
  note that implied a database file could not be opened by more than one process.

## v0.7.0 — Database systems depth

- Added non-blocking OS file locks on Linux/macOS/BSD and Windows: one writer
  owns a database file, read-only handles may share it only when no writer does,
  and lock conflicts return the stable `ErrDatabaseLocked` error.
- Added explicit read-only opens, atomic fsynced backup files, a read-only
  `marketserver` mode for point-in-time replicas, and CLI backup/read-only flags.
- Added atomic mixed-table write batches, bounded resumable relational range
  deletion, physical prefix-range deletion, catalog-safe table dropping, and
  `DROP TABLE` support.
- Added storage format v3 leaf-page prefix compression while preserving reads of
  format-v2 files; table prefixes are never reused after a drop.
- Replaced allocation-heavy trial node splits with encoded-size arithmetic and
  balance-aware compressed splits, with reproducible before/after benchmarks.
- Added deterministic commit-boundary crash injection in child processes,
  30-round reopen churn, compression recovery tests, and codec/B+tree/parser
  fuzz targets in CI.
- Fixed the dashboard forecast wire contract by introducing a tagged API DTO;
  invalid forecast fields can no longer poison the entire Canvas chart with NaN.
- Documented locking, filesystem, `fsync`, atomicity, backup, corruption, and
  format-compatibility assumptions explicitly.

## v0.6.0 — Single-process market service

- Added schema v5 with durable service jobs, hashed idempotency keys, bounded
  payload/results, atomic claiming, cooperative cancellation, and explicit
  interrupted-job recovery.
- Added built-in background workflows for demo seeding, provider ingestion,
  feature recomputation, experiments, prediction, evaluation, and configured
  grounded analysis.
- Added a bounded REST API with request IDs, deadlines, concurrency admission,
  stable error envelopes, mutation bearer auth, origin checks, security headers,
  liveness/readiness, model cards, and complete analyst audit retrieval.
- Added a responsive embedded dashboard with a custom Canvas market/forecast
  chart, point-in-time features, interval caveats, model evidence, and claim
  lineage; no network-loaded frontend dependencies are required.
- Added Prometheus-compatible HTTP/job/storage metrics and structured JSON logs
  that use route templates and exclude credentials and request bodies.
- Added graceful shutdown, non-root hardened Docker/Compose files, Linux/Windows
  CI, an OpenAPI contract, a bounded Go load generator, and an operations runbook.
- Integrated the Windows directory-sync build-tag fix and cross-platform signal
  selection while retaining file-sync durability.

## v0.5.0 — Grounded local analyst

- Added a native local Ollama adapter with separate system/data roles, JSON
  schema output, request limits, timeout/cancellation, and no remote redirects.
- Replaced free-form `LLMResponse` fields with an evidence-ID/outlook/abstention
  contract. **Adapter API change:** legacy custom clients must adopt that contract;
  existing stored analyses remain intact.
- Added one-snapshot evidence retrieval, mandatory directional counter-evidence,
  forecast outcome allowlisting, and conservative daily-bar/time-availability rules.
- Added SEC recent-submissions metadata ingestion, validated ticker/CIK mapping,
  canonical filing URLs, immutable first-observation times, hashes, and schema v4.
- Added three-attempt validation/transport budget, audited failure abstentions,
  atomic checksummed analysis artifacts, replay, and Markdown-safe metadata.
- Added analyst/demo/report, filing retrieval, and labelled evaluation CLI commands.
- Added deterministic/adversarial tests, HTTP contract tests, opt-in live tests,
  migration/reopen tests, a JSON decoder fuzz target, and a student-focused runbook.
- Documented that fixture evaluation is not LLM quality; live Ollama/SEC acceptance
  remains pending in the supplied environment.

## v0.4.0 — Reproducible prediction service

- Added chronological train/tune/calibration/test partitioning and horizon-aware
  purging of labels that cross partition boundaries.
- Added deterministic persistence, moving-average, standardized ridge, and
  gradient-boosted regression-stump models implemented with the Go standard library.
- Added tuning-only hyperparameter selection, frozen holdout evaluation, and
  rolling/expanding walk-forward refits with past-label-only training.
- Added MAE, RMSE, MAPE, direction accuracy, empirical interval coverage/width/gap,
  origin-time regime slices, and persistence-relative comparisons.
- Added migration v3 and checksummed/chunked fitted-model and model-card storage.
- Added saved-model NYSE daily forecasting, shared train/inference feature inputs,
  immutable forecast writes, and exact replay after database reopen.
- Added `experiment`, `experiment-demo`, `model-card`, and `predict` CLI commands,
  synthetic fixtures, methodology/runbook, and synthetic/live-AAPL model cards.
- Added future-label perturbation, deterministic-repeat, multi-bar/rolling-window,
  artifact-corruption, migration, cancellation, and numerical-learning tests.

## v0.3.0 — Resumable real-market pipeline

- Added a provider-neutral market-data contract and a live Twelve Data REST
  adapter with API-key redaction, request timeouts, bounded response bodies,
  account-wide pacing, exponential backoff, and `Retry-After` support.
- Added 5,000-point window planning, inclusive boundary filtering, durable
  page checkpoints, correction overlap, cancellation, and sequential
  multi-symbol jobs with per-symbol failure isolation.
- Added four atomic migration-v2 tables for corporate actions, quality reports,
  ingestion runs, and feature watermarks.
- Added idempotent dividends/splits, exact-rational split reconstruction for
  intraday adjusted closes, and protection against double-adjusting daily data.
- Added a NYSE daily calendar, including known special closures, and persisted
  reports for coverage, missing/unexpected dates, duplicates, invalid rows,
  zero volume, large returns, warnings, and dataset hashes.
- Added incremental, correction-aware feature calculation with durable
  watermarks, page-level forecast realization, and new `sync`, `quality`, and
  `features-incremental` CLI commands.
- Added oversized relational-row rejection before mutation, live-contract test
  support, Phase 2 runbooks, and a five-year live AAPL acceptance result.

## v0.2.0 — Market data foundation

- Added defensive database catalog introspection and operational storage stats.
- Added an atomic, idempotent versioned market schema with eight domain tables.
- Added fixed-point instruments and OHLCV candles with validation.
- Added sorted atomic batch ingestion, CSV import, checkpoints, and metrics.
- Added indexed ascending and descending time-series range queries.
- Added point-in-time returns, moving averages, volatility, RSI, and volume features.
- Added model-run lineage, immutable forecast identity, realized evaluation, and
  a leakage-safe persistence baseline.
- Added a provider-neutral LLM client boundary, typed response validation,
  structured point-in-time context, quantitative-forecast grounding, and input
  digests.
- Added the `marketdb` CLI, deterministic end-to-end demo, tests, benchmarks,
  architecture notes, and delivery roadmap.

## v0.1.0 — Textbook database

- Copy-on-write B+ tree with fixed 4 KiB pages.
- Durable page store, checksummed alternating metadata, and crash fallback.
- Versioned free-page reuse, transactions, snapshots, and optimistic conflicts.
- Relational records, composite keys, range scans, and secondary indexes.
- Simplified SQL-like parser/interpreter and interactive CLI.
