# Phase 4 validation record

Captured 2026-08-31 UTC. Application v0.5.0, schema v4, analyst `grounded-v1`.

## What was actually run

- Complete unit/integration-fixture suite: passed.
- Race detector across all packages, including real CLI subprocesses: passed.
- `go vet ./...`: passed.
- Both CLI builds with `-buildvcs=false`: passed.
- Empty-database `analysis-demo`: passed through 600 synthetic candles → Phase 3
  model selection → saved-model forecast → cited analysis → persistent audit.
- Fixture `analyst-eval`: all 10 contract cases passed; 14/14 recognized citations,
  2/2 correct labelled abstentions, zero false abstentions and zero errors.
- Structured-output decoder fuzz smoke test: 57,409 executions in three seconds
  with two workers, no crash or round-trip failure. This is a smoke test, not an
  extensive fuzzing campaign or security proof.

Coverage at this checkpoint: database engine 75.5%; market 81.7%; market CLI
59.7%; Ollama HTTP adapter 93.8%; SEC HTTP adapter 87.6%; Twelve Data adapter 73.9%.
Coverage is a test-execution measure, not a production-reliability guarantee.

Evaluation dataset SHA-256:
`3cfd61f9405c8307c650ca6aac74ecf55452a97bad26f561025c06add530069d`.

**Those evaluation figures use a deterministic fixture, not an LLM.** They verify
the plumbing and validator. The injected-output unit tests also demonstrate
rejection and abstention, not a measured jailbreak-resistance rate for a model.

## Important tests

- Unknown and duplicate IDs, missing counter-evidence, contradictory outlooks,
  extra JSON fields, duplicate keys, nulls, fenced/trailing JSON.
- Successful repair, exhausted repair, retryable/permanent provider failures,
  safe error handling, cancellation, and persisted failure abstentions.
- Stale/short/mixed-source history abstaining without a provider call.
- Future bars, later-created forecasts, and realized forecast outcomes excluded.
- Filing publication/first-retrieval filtering, immutable retry timestamps,
  content hashes, ticker/CIK mismatch, malformed arrays and unsafe URLs/paths.
- HTTP route/role/schema checks, response limits, redirect refusal, timeouts,
  SEC pacing and `Retry-After` handling.
- DB schema-v3 migration preserving candles and artifacts; close/reopen replay;
  deliberate audit corruption detection; context stability during DB corrections.
- CLI demo, report retrieval, evaluation/retrieval, invalid provider/model/interval,
  missing IDs, and failure exit codes.

## What is not verified yet

No Ollama executable/model was available in the build environment. The opt-in
real-model integration test was **skipped**, not passed. SEC live retrieval was
not run with an invented identity; it requires the user's identifying
`SEC_USER_AGENT`. Its integration test is opt-in, while its HTTP contract and
parser tests run offline. No model downloads, paid API calls, or new accounts
were used.

Before calling the analyst live-validated, run the commands in [ANALYST.md](ANALYST.md)
on your machine and record the exact model tag/digest, server version, hardware,
raw evaluation counts, and source retrieval output. Review failed cases rather
than lowering thresholds or hiding errors. This implementation's scope is
technical indicators, numeric forecasts, and SEC **filing metadata**, not news
sentiment or financial-statement analysis. Profitability is not claimed.
