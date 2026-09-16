# Twelve Data provider runbook

This document describes the Phase 2 live market-data boundary. The adapter is
implemented in `market/provider/twelvedata`; orchestration and persistence live
in `market/sync.go`.

## Provider contract and plan assumptions

The implementation follows Twelve Data's official REST documentation:

- API reference: <https://twelvedata.com/docs>
- Basic/free plan: <https://support.twelvedata.com/en/articles/5335783-trial>
- Historical time series and the 5,000-point response limit:
  <https://support.twelvedata.com/en/articles/5656039-how-to-get-historical-prices>
- Credits, per-minute reset, 429 responses, and credit headers:
  <https://support.twelvedata.com/en/articles/5615854-credits>
- Adjusted daily data versus unadjusted intraday data:
  <https://support.twelvedata.com/en/articles/5179064-are-the-prices-adjusted>

The CLI defaults to eight requests per minute, matching the documented Basic
plan. Provider plans and limits can change, so confirm the official pages and
set `-requests-per-minute` no higher than the active account permits.

## Secret handling

Set the API key in the process environment:

```sh
export TWELVE_DATA_API_KEY="your-key"
```

There is deliberately no API-key CLI flag: command-line arguments can appear
in shell history and process listings. The key is never stored in byodb,
included in structured results, or logged by the adapter. Provider error text
is scrubbed if it echoes the configured key. Keep `.env` files and database
snapshots with sensitive data out of version control.

## Repeatable sync jobs

Initialize or migrate the database, then sync a daily universe:

```sh
go run ./cmd/marketdb -db market.db -command init

go run ./cmd/marketdb \
  -db market.db -command sync \
  -symbols AAPL,MSFT,SPY \
  -interval 1d \
  -start-date 2021-08-29 \
  -end-date 2026-08-27
```

Omit the dates to request approximately five years through the current time.
The supported internal intervals are `1m`, `5m`, `15m`, `30m`, `1h`, `4h`,
`1d`, `1wk`, and `1mo`.

Useful operational variants:

```sh
# Prices only; useful for a lower-credit smoke test.
go run ./cmd/marketdb -db smoke.db -command sync \
  -symbol AAPL -interval 1d \
  -start-date 2026-08-01 -end-date 2026-08-27 \
  -corporate-actions=false -compute-features=false

# Recompute a bounded corrected feature range explicitly.
go run ./cmd/marketdb -db market.db -command features-incremental \
  -symbol AAPL -interval 1d -from 1756425600 -to 1787788800

# Persist another NYSE daily quality report.
go run ./cmd/marketdb -db market.db -command quality \
  -source twelvedata -symbol AAPL -interval 1d \
  -from 1630195200 -to 1787788800
```

## Reliability semantics

1. `SyncService` reads `(provider, dataset, symbol, interval)` checkpoint state.
2. A resumed run backs up two intervals to capture recently corrected values.
3. The date range is divided into windows containing at most 5,000 intervals.
4. The client paces all calls through one shared limiter. It retries transport
   errors, HTTP 429, and HTTP 5xx up to four total attempts with exponential
   backoff capped at five seconds; `Retry-After` takes precedence.
5. Each response is capped at 32 MiB and decoded into fixed-point domain types.
6. A validated candle page commits before its checkpoint advances. A crash in
   between causes safe replay because writes compare logical data and are
   idempotent.
7. The ingestion run is updated after every page with rows, retries, and API
   credits. Context cancellation or a terminal error persists a bounded failed
   run instead of losing partial progress.
8. Successful candle sync is followed by optional corporate actions, split
   adjustment, realized-forecast evaluation, incremental features, and a
   persisted quality report.

Symbols run sequentially so every request shares the same provider-account
budget. One failed symbol is recorded in `failures`; completed symbols remain
available and are not rolled back.

## Time and adjustment conventions

- Provider boundaries use UTC and application ranges are inclusive.
- Twelve Data's historical `end_date` behaves exclusively in live tests. The
  adapter advances it by one interval and the sync layer filters the response
  to the exact requested inclusive range.
- Daily/weekly/monthly provider prices are treated as already split-adjusted.
  They are not adjusted again.
- Intraday `adjusted_close` is reconstructed from stored split actions using
  exact rational factors. Dividend total-return adjustment is out of scope.
- NYSE gap checks currently apply only to daily candles. Intraday quality
  reports explicitly warn that full session/early-close continuity is not yet
  modeled.

## Test modes

The default test suite uses deterministic local HTTP servers and never spends
provider credits:

```sh
go test ./...
go test -race ./...
```

Run the opt-in live contract test only with an available key:

```sh
TWELVE_DATA_API_KEY="your-key" \
  go test -tags=integration ./market/provider/twelvedata \
  -run TestLiveTimeSeriesContract -v
```

Do not put a real key into test fixtures, terminal transcripts, screenshots,
CI logs, or bug reports.
