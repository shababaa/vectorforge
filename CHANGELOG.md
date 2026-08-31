# Changelog

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
