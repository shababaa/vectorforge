# Phase 2 validation record

This record separates verified engineering evidence from future resume claims.
Times and row counts below refer to a live acceptance run performed on
2026-08-29 UTC against Twelve Data's public demo access.

## Acceptance snapshot

Command shape:

```sh
TWELVE_DATA_API_KEY="[REDACTED]" \
  go run ./cmd/marketdb \
  -db phase2-live.db -command sync \
  -symbol AAPL -interval 1d \
  -start-date 2021-08-29 -end-date 2026-08-27 \
  -corporate-actions=false -compute-features=false
```

Observed result:

| Measure | Result |
| --- | ---: |
| Requested history | Five years |
| Candle pages | 1 |
| API credits | 1 |
| Rows received/inserted | 1,254 / 1,254 |
| Expected NYSE sessions | 1,254 |
| Observed sessions | 1,254 |
| Missing sessions | 0 |
| Unexpected dates | 0 |
| Coverage | 100.0000% |
| Provider retries | 0 |
| Job status | completed |

The expanded pipeline was also exercised with corporate actions and incremental
features enabled. It persisted 20 split/dividend records and computed 1,234
feature snapshots using three provider credits. Those counts are an acceptance
snapshot, not a stable dataset contract; a provider can revise history and
corporate-action coverage.

The initial live run exposed two useful contract bugs that were then covered by
tests: Twelve Data's exclusive historical `end_date`, and NYSE exceptional
closures/holiday observations around 2021-12-31 and 2025-01-09.

## Reproducible local verification

```sh
gofmt -w *.go market/*.go market/provider/twelvedata/*.go cmd/*/*.go
go test ./...
go vet ./...
go test -race ./...
go test -cover ./...
go build ./cmd/byodb ./cmd/marketdb
```

The provider package additionally has an integration-tag contract test; see
[the provider runbook](PROVIDER_TWELVEDATA.md).

Release-candidate verification on 2026-08-29 UTC:

| Check | Result |
| --- | --- |
| `go test ./...` | passed |
| `go vet ./...` | passed |
| `go test -race ./...` | passed |
| Database engine coverage | 75.5% |
| Market package coverage | 75.7% |
| Twelve Data adapter coverage | 73.9% |
| `go build -buildvcs=false ./cmd/byodb ./cmd/marketdb` | passed |
| Deterministic schema-v2 CLI demo | passed |
| Opt-in live Twelve Data contract test | passed |

`-buildvcs=false` is needed only for this distributed source bundle because it
does not include a Git worktree. It does not change the compiled program.

## Exit-criterion status

The Phase 2 implementation and single-symbol five-year live validation are
complete. The roadmap's final multi-asset acceptance remains intentionally
open: the public demo key is restricted to demo symbols. With a personal key,
run the documented `AAPL,MSFT,SPY` command and replace this section with the
resulting immutable data range, row counts, dataset hashes, credits, and quality
coverage before claiming a multi-asset metric on a resume.

## Honest current claims

Supported now:

- designed and tested a restart-safe, idempotent Go ingestion pipeline over a
  from-scratch transactional B+ tree database;
- integrated a rate-limited REST provider with retry/backoff, bounded responses,
  cancellation, secret redaction, durable checkpoints, and job observability;
- validated five years of AAPL daily history against 1,254 NYSE sessions with
  100% date coverage in the recorded acceptance run;
- implemented corporate-action lineage, incremental features, model-evaluation
  hooks, and deterministic data-quality hashes.

Not yet supported as a claim:

- profitable predictions or trading returns;
- completed multi-asset live acceptance;
- full exchange-calendar coverage or total-return adjustment;
- production availability, multi-process safety, or regulated-data compliance.
