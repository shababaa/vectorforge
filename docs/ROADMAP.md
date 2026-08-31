# Professional project roadmap

Each phase ends in a demo, measurable acceptance criteria, and a clean commit.
This prevents the project from becoming a collection of half-connected AI
features.

## Phase 1 — market data foundation (complete)

- Versioned domain schema and safe schema introspection
- Fixed-point OHLCV types and validation
- Atomic idempotent ingestion and CSV adapter
- Composite time-series indexes and bounded range API
- Point-in-time technical features
- Model/forecast/evaluation lineage
- Walk-forward persistence baseline and metrics
- Provider-neutral LLM interface and auditable analysis storage
- Unit, integration, race, and benchmark coverage

Exit criteria: deterministic demo runs from an empty file and every test passes.

## Phase 2 — real data pipeline (engineering complete; universe acceptance pending)

- [x] Official Twelve Data adapter behind a provider-neutral Go interface
- [x] Shared rate limiting, `Retry-After`, exponential backoff, bounded windows,
  and resumable checkpoints
- [x] Persisted ingestion runs with row, retry, API-credit, and failure metrics
- [x] NYSE trading calendar with regular holidays and known full-day special closures
- [x] Missing/unexpected-session, duplicate, invalid, zero-volume, and outlier checks
- [x] Idempotent split/dividend ingestion and intraday split adjustment
- [x] Incremental feature recomputation with durable watermarks and correction overlap
- [x] Multi-symbol CLI orchestration with isolated failure reporting
- [x] Unit, integration-tag, cancellation, recovery, migration, and live contract coverage

Exit criteria: ingest and validate at least five years of daily data for a
multi-asset universe with a repeatable CLI job.

Status: a live five-year AAPL acceptance run passed with 1,254 observed of
1,254 expected NYSE sessions, zero missing and zero unexpected dates. The
repeatable CLI accepts a multi-asset universe, but the final AAPL/MSFT/SPY live
artifact requires a personal Twelve Data key because the public demo key is
restricted to demo symbols. See [Phase 2 validation](PHASE2_VALIDATION.md).

## Phase 3 — prediction service (complete for the educational baseline scope)

- [x] Chronological train/tune/calibration/test splits with horizon-aware label purging
- [x] Persistence, moving-average, standardized ridge, and gradient-boosted regression stumps
- [x] Shared point-in-time rolling features for training and saved-model inference
- [x] Eight deterministic candidate configurations; selection uses tuning MAE only
- [x] Hyperparameter/config tracking through `market_model_runs`
- [x] Schema-v3 checksummed model artifacts, normalization state, and model cards
- [x] MAE, RMSE, MAPE, three-way direction accuracy, interval coverage/width/gap,
  and persistence-relative error comparisons
- [x] Frozen holdout plus rolling/expanding walk-forward evaluation and regime slices
- [x] Calendar-aware saved-model daily forecasting with immutable prediction writes
- [x] Reopen/replay, future-label perturbation, horizon, cancellation, corruption,
  and deterministic-repeat tests

Exit criteria: produce a reproducible model card and show whether each model
beats the persistence baseline after costs are excluded from the claim.

Evidence: [synthetic model card](EXAMPLE_MODEL_CARD.md) and
[AAPL model card](AAPL_MODEL_CARD.md), captured on 2026-08-31. The AAPL run uses
1,254 candles and 248 test origins. Tuning selected ridge, which did **not** beat
persistence on the frozen holdout (MAE 3.133439 vs 3.128548). Boosted stumps had
0.58% lower holdout MAE, but that observation does not retroactively change model
selection and is not a statistically established or tradable advantage.

The Phase 2 multi-asset live acceptance remains open. Phase 3 does not promote a
model into production or claim profitable trading. See [methodology and
operations](PREDICTION.md) for bounds, interval caveats, and exact commands.

## Phase 4 — grounded LLM analyst (engineering complete; live-model acceptance pending)

- [x] Native local Ollama structured-output adapter behind `LLMClient`
- [x] SEC recent-filing metadata retrieval, canonical source IDs, hashes, and two-clock availability
- [x] Strict JSON/ID validation, bounded retry/repair, cancellation, and audited abstention
- [x] System/data separation, no LLM tools, and controlled-language output capability
- [x] One-snapshot evidence retrieval; feature/forecast/source IDs and exact audit payloads
- [x] Ten labelled contract cases for citations, consistency, abstention, and injection attempts
- [x] Exact-rendering factual-fidelity tests, schema-v4 migration, CLI workflow and replay tests

Exit criteria: the analyst refuses unsupported claims and every visible claim
can be traced to stored evidence.

Implementation scope: Go computes and renders claims; the LLM selects evidence,
not unrestricted prose. Sources currently cover SEC **metadata**, not filing
contents or news sentiment. The deterministic demo/evaluation and adversarial
validator tests pass. A real local-model evaluation and live SEC ingestion with
the user's identifying User-Agent remain to be recorded; no fixture score is
presented as LLM accuracy. See [analyst runbook](ANALYST.md) and
[validation evidence](PHASE4_VALIDATION.md).

## Phase 5 — service and dashboard

- HTTP/gRPC API with timeouts, request IDs, and graceful shutdown
- Background ingestion/evaluation workers
- React dashboard for charts, features, forecasts, confidence, and model cards
- Prometheus-compatible metrics and structured logs
- Docker, CI, load tests, and reproducible demo deployment

Exit criteria: one command starts a documented end-to-end system with health
checks and a seeded demo.

## Phase 6 — database systems depth

- Inter-process file locking and read-only replicas
- Write batching/profile-driven allocation improvements
- Table dropping and efficient range deletion
- Prefix compression for time-series keys
- Fuzzing, crash-fault injection, and long-running recovery tests

Exit criteria: publish before/after benchmark evidence and document every
durability assumption.

## Resume-ready metrics to earn, not invent

- Number of candles/assets and years of history ingested
- Sustained rows/second and p50/p95 indexed query latency
- Crash/recovery and fuzz-test duration
- Model error relative to persistence baseline on an untouched test window
- LLM factuality/abstention score on a labelled evaluation set
- API load-test throughput and p95 latency

Numbers belong on a resume only after the benchmark command, data snapshot, and
evaluation method are committed so an interviewer can reproduce them.
