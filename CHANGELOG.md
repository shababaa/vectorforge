# Changelog

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
