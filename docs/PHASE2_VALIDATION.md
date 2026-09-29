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

## Multi-asset acceptance snapshot

Captured 2026-09-29. The public Twelve Data demo key cannot download MSFT or
SPY, so this acceptance uses a frozen Yahoo Chart v8 daily snapshot checked in
at `market/testdata/universe`. Prices are rounded to six decimal places. Session
dates are the UTC calendar date of each Yahoo bar. This is a reproducible file
import, not a live Twelve Data credit report and not a point-in-time vendor
vintage.

Window: 2021-08-29 through 2026-08-27. The NYSE calendar expects 1,254 sessions.
The first bar is 2021-08-30 because 2021-08-29 was a Sunday. The last bar is
2026-08-27.

| Symbol | Rows | Missing | Unexpected | Outliers | Features | Dataset SHA-256 |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| AAPL | 1,254 | 0 | 0 | 0 | 1,234 | `51f1e2d8325c7810e4e0a969cea52253c1e85434c28f8ffab10d13fe18296d2d` |
| MSFT | 1,254 | 0 | 0 | 0 | 1,234 | `5f733380f1c43428ba54c297f0d5d1b9f574830d4ed66694acafebb0ceced682` |
| SPY | 1,254 | 0 | 0 | 0 | 1,234 | `e47f7921299d324498c21153cb572d30db4c6925e7bd036fd7ffb143a1e3e285` |

Coverage is 100% for each symbol. Reimporting a file inserts nothing and leaves
every row unchanged. File SHA-256 values identify the committed CSV bytes:
AAPL `b8e82e5ac38107f0181c436680a8995fe9e1beaf293d252c276dffbc3f53abc7`,
MSFT `ef24926aa69c6c7565555551aa340295efffff57baffa79cc00df68dacbc78a0`,
SPY `b1320059e67052bdefb6dc0a458b3792fde56e9e5b4a4cb4f10b671953bddcd7`.

Repeat the check with:

```sh
go test ./market -run TestFiveYearUniverseAcceptance -count=1 -v
```

Or import one file and write a quality report:

```sh
go run ./cmd/marketdb -db universe.db -command import \
  -file market/testdata/universe/AAPL.csv -symbol AAPL -interval 1d \
  -source yahoo-chart-v8
go run ./cmd/marketdb -db universe.db -command quality \
  -symbol AAPL -interval 1d -source yahoo-chart-v8 \
  -from 1630195200 -to 1787788800
```

`-from` is 2021-08-29 00:00:00 UTC and `-to` is 2026-08-27 00:00:00 UTC.

A personal Twelve Data key can still run the live `AAPL,MSFT,SPY` sync. Do not
replace this snapshot's hashes with that run unless the new provider, row
counts, credits, and quality coverage are recorded the same way.

## Exit-criterion status

The Phase 2 implementation, the single-symbol five-year Twelve Data validation,
and the three-symbol five-year file acceptance are complete. The file acceptance
is the resume-ready multi-asset evidence. A live multi-symbol Twelve Data sync
remains credential-gated and is not claimed here.

## Honest current claims

Supported now:

- designed and tested a restart-safe, idempotent Go ingestion pipeline over a
  from-scratch transactional B+ tree database;
- integrated a rate-limited REST provider with retry/backoff, bounded responses,
  cancellation, secret redaction, durable checkpoints, and job observability;
- validated five years of AAPL daily history against 1,254 NYSE sessions with
  100% date coverage in the recorded Twelve Data acceptance run;
- imported and validated the same 1,254-session window for AAPL, MSFT, and SPY
  from the committed Yahoo Chart snapshot, with idempotent replay and 1,234
  feature snapshots per symbol;
- implemented corporate-action lineage, incremental features, model-evaluation
  hooks, and deterministic data-quality hashes.

Not yet supported as a claim:

- profitable predictions or trading returns;
- a live Twelve Data sync of MSFT and SPY (the demo key cannot fetch them);
- full exchange-calendar coverage or total-return adjustment;
- production availability or regulated-data compliance.
