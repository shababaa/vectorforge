# Phase 3 prediction service

Phase 3 is an embedded Go prediction/evaluation service and CLI, not an HTTP
service or a trading bot. It intentionally uses the custom database and Go
standard library throughout. A network API and dashboard remain Phase 5 work.

## Repeatable commands

No key is needed for the synthetic engineering demonstration:

```sh
go run ./cmd/marketdb -db prediction-demo.db -command experiment-demo -format markdown
```

The fixture is 600 deterministic NYSE-session bars with changing drift, periodic
structure, and pseudo-random noise. Its symbol is `SYNTH` and source is
`synthetic-phase3-v1`. It is **not real market data**.

For real data, first run Phase 2 ingestion, then evaluate a fixed completed range:

```sh
export TWELVE_DATA_API_KEY="your-key"
go run ./cmd/marketdb -db market.db -command sync -symbol AAPL -interval 1d \
  -start-date 2021-08-29 -end-date 2026-08-27

go run ./cmd/marketdb -db market.db -command experiment -symbol AAPL -interval 1d \
  -to 1787788800 -horizon-bars 1 -refit-every 63 -format markdown

# Rolling instead of expanding walk-forward training; holdout stays frozen.
go run ./cmd/marketdb -db market.db -command experiment -symbol AAPL -interval 1d \
  -to 1787788800 -train-window 504 -refit-every 63 -format markdown
```

Review the Phase 2 quality report before interpreting any experiment. Missing
sessions are not automatically imputed. The horizon means observed bars, not
elapsed calendar days; a missing bar therefore changes its real-world duration.

Save/reload a report or generate a new forecast using IDs printed in the card:

```sh
go run ./cmd/marketdb -db market.db -command model-card \
  -run-id EXPERIMENT_ID -format markdown

go run ./cmd/marketdb -db market.db -command predict -run-id MODEL_RUN_ID
```

The saved model is not automatically retrained. Pick the frozen run or a
walk-forward fold deliberately. `predict` uses the latest stored daily candle
unless `-as-of` is supplied, so it is the caller's responsibility to ensure that
bar is complete. The CLI uses the NYSE calendar. It refuses backdated inference
when calibration used later labels and refuses to overwrite an existing forecast.

## Data and feature contract

- One symbol, interval, and provider source per immutable snapshot.
- At most 20,000 bars per experiment; no silent truncation.
- Twenty-one bars of history per origin, including that completed origin bar.
- Target: `log(adjusted_close[t+h] / adjusted_close[t])`, with `h` observed bars.
- Inputs: one-/five-bar returns, close relative to SMA5/SMA20, 20-return
  volatility, RSI14, and volume relative to its 20-bar mean.
- Model inputs never include the label; training and inference share the exact
  feature mapping. Constant/zero-volume history receives a neutral relative-volume input.
- Dataset hashes omit operational ingestion timestamps. The experiment
  fingerprint includes dataset hash, configuration, and `prediction-v1`.

Training arithmetic uses float64; stored candles, forecasts, and errors retain
the database's fixed-point representation. Model artifacts store all float64
parameters exactly through round-trippable JSON. Repeatability assumes the same
input snapshot and implementation/toolchain; cross-architecture last-bit identity
is not a contractual guarantee.

## Chronology and leakage defenses

Default origin partitions are 60% training, 10% tuning, 10% calibration, and 20%
test (integer rounding goes to the later remainder). Every earlier partition
drops labels with `target_timestamp >= next_first_origin`. A three-bar horizon
therefore removes three samples at each boundary, not merely one.

1. Fit all eight candidate configurations on training data only.
2. Select one configuration per model family and the overall candidate by tuning MAE.
3. Refit each selected family on training plus tuning; fit ridge scaling only there.
4. Estimate each residual interval on the reserved calibration period.
5. Evaluate these frozen fits on the held-out origins without further tuning.
6. Separately simulate walk-forward refits with the same selected configurations.
   At each block, only labels strictly before its first origin are eligible.
   Reserve the latest eligible calibration block, purge its training boundary,
   and fit on the preceding expanding or bounded rolling history.

Walk-forward results can learn from **past realized test labels**. This is an
online evaluation protocol, distinct from the frozen holdout. Neither protocol
uses later test scores to choose hyperparameters. Regimes use origin-time
five-bar returns, not future outcomes.

Minimum usable partitions after purging are 40 fit samples and 10 each for
tuning, calibration, and testing. At most 100 refit blocks are allowed; increase
`-refit-every` for longer histories.

The chronological design follows the motivation in the official
[TimeSeriesSplit documentation](https://scikit-learn.org/stable/modules/generated/sklearn.model_selection.TimeSeriesSplit.html)
and [data-leakage guidance](https://scikit-learn.org/stable/common_pitfalls.html).
This project implements its own observed-bar partitioner, not scikit-learn.

## Models implemented from scratch

| Family | Candidate grid | Fitted state |
| --- | --- | --- |
| Persistence | previous adjusted close | no coefficients |
| Moving average | 5 or 20 bars | selected window |
| Ridge regression | alpha 0.01, 0.1, 1 | train-only means/scales, intercept, seven coefficients |
| Gradient-boosted stumps | 32 or 64 rounds, learning rate 0.05 | intercept and ordered depth-one regression trees |

Ridge minimizes mean squared log-return error plus an L2 penalty; the intercept
is unpenalized. A pivoted linear-system solver fits standardized features.
Boosting fits residual squared loss using up to 32 candidate split positions per
feature and a minimum five samples per leaf. Sorting, candidate order, and ties
are deterministic. These are educational stumps, **not XGBoost**. See Friedman's
[gradient boosting paper](https://doi.org/10.1214/aos/1013203451) for the underlying
stagewise residual-fitting principle.

All model log-return outputs are clipped to [-0.5, 0.5] before conversion to
prices. This numerical guard is fixed in `prediction-v1`, applies during tuning,
calibration, testing, and inference, and can bias extreme/long-horizon forecasts.

## Metrics and uncertainty

Cards report MAE, RMSE, MAPE, negative/zero/positive direction accuracy, nominal
versus observed interval coverage, absolute coverage gap, mean interval width,
and MAE improvement against persistence on exactly the same origins. Persistence
predicts zero change, so its three-way direction accuracy can be zero even when
its price error is competitive. MAPE is a fraction in JSON and percent in Markdown.

Intervals use the `ceil((n+1)*coverage)` absolute log-residual order statistic from
the reserved calibration block (capped at the largest residual). Price bounds
are positive and asymmetric after exponentiation. `confidence_ppm` stores this
**nominal interval coverage**, not directional confidence or probability of profit.
Time-series residuals are dependent and nonstationary: no distribution-free
coverage guarantee is asserted. Always inspect observed coverage and width.

## Persistence, failure behavior, and replay

Schema version 3 adds `market_model_artifacts`. Predictors and complete model
cards are stored in 1,800-byte chunks because the engine limits a row value to
3,000 bytes. Reads verify contiguous chunk numbering, total size, and a SHA-256
checksum. Payloads are capped at 8 MiB.

Each model/fold run stores its specification, mode, fold number, experiment ID,
training timestamps/hash, and metrics in `market_model_runs`. Its complete fitted
state, calibration bounds/hash, and residual radius are stored alongside the
forecasts in one transaction. The parent becomes completed only when its model
card commits. A failed/cancelled run retains completed child runs and records
failure; a hard process kill can leave a running parent. Reruns create new IDs.

`PutForecast` now treats the original output as immutable: identical retries are
safe and preserve realized outcomes; changed predictions are rejected. Actual
outcomes still attach through `EvaluateForecastsAt` and never replace model output.

## Validation evidence and limits

See [synthetic example](EXAMPLE_MODEL_CARD.md) and [live AAPL card](AAPL_MODEL_CARD.md).
The AAPL validation used the public provider demo key, 1,254 daily candles,
248 test origins, and four walk-forward refits per model. It did not need a
personal key or spend a user's paid credits. No API keys or market database
files are committed.

Tuning chose ridge. On the frozen holdout it slightly lost to persistence.
Boosted stumps had 0.58% lower MAE, but this single-asset, single-window observation
is not a significance test and does not change the tuning-selected model.
No P&L, costs, slippage, execution, survivorship-bias correction, or point-in-time
vendor vintage reconstruction is included. The Phase 2 price-history acceptance
covers AAPL, MSFT, and SPY; this prediction card is still single-asset. The
recorded local-model analyst score is in the Phase 4 validation note.
