# Phase 3 validation record

Captured 2026-08-31 UTC. The implementation is `prediction-v1`, application
release v0.4.0, database schema version 3. The exact model cards are committed
alongside this record; raw provider data and database files are not distributed.

## Engineering checks

```sh
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./...
go test -cover ./...
go build ./cmd/byodb ./cmd/marketdb
```

For source archives without usable Git metadata, add `-buildvcs=false` to the
build command. The final tests cover:

- three chronological label-purge boundaries for one- and three-bar horizons;
- future-candle changes leaving earlier feature vectors unchanged;
- ridge and boosted-stump fitting against analytically known signals;
- deterministic experiment replay and unchanged fingerprints for the same data;
- final test-label perturbations leaving tuning scores, selection, scaling,
  calibration, and frozen fitted models unchanged;
- rolling-window size bounds and no future labels in walk-forward artifacts;
- model/card persistence and daily inference after closing/reopening the DB;
- missing chunks, payload checksums, immutable forecast retries, and retained actuals;
- cancellation before work and persisted failed-parent status during work;
- migration from schema v2 without losing existing candle rows;
- complete CLI init → experiment → card retrieval → saved-model prediction,
  plus failure exit codes for invalid inputs.

Market-package statement coverage: 80.5%; database engine: 75.5%; provider
adapter: 73.9%; market CLI: 51.2%. Coverage is not a reliability guarantee. The
CLI subprocess test also exercises actual command parsing and process exits.

## Deterministic synthetic acceptance

- 600 synthetic NYSE-session candles; source `synthetic-phase3-v1`.
- 346 training, 56 tuning, 57 calibration, 117 test samples; 3 purged samples.
- Four model families; eight candidate configurations; two walk-forward refits per family.
- Fingerprint: `4293f0da9ab3f079c799648c900d60045fe52ac188e2b9ecb0379123239fc0d6`.
- Tuning selected 64-round boosted stumps; the test set did not change that choice.
- Frozen MAE: persistence 0.443923, moving average 0.866318, ridge 0.424135,
  boosted stumps 0.427774.

These are engineering-fixture results, not market performance. Regime breakdowns,
RMSE, MAPE, interval coverage, widths, and run metadata appear in
[EXAMPLE_MODEL_CARD.md](EXAMPLE_MODEL_CARD.md).

## Live single-asset acceptance

The Phase 2 provider client re-ingested AAPL daily history for 2021-08-29 through
2026-08-27 using public demo access: 1,254 candles, one API credit, no retries,
and all 1,254 expected NYSE sessions present. The Phase 3 experiment used the
default configuration with a fixed upper timestamp of `1787788800`.

- Dataset hash: `da3362c6b5226bffeb8a01c5b882845df51ed2a64b8c0b9239d56228d1ba1cea`.
- Experiment fingerprint: `2201fdd14ee8c6c4ed41209a31bc119df6f7c15fe5b6b4df40a4b7a080080e62`.
- 738 training, 122 tuning, 122 calibration, 248 test samples; 3 purged samples.
- Four walk-forward refits per family, every 63 test origins.
- Tuning selected ridge alpha 0.01.

| Model | Frozen MAE | Walk-forward MAE | Frozen MAE improvement vs persistence |
| --- | ---: | ---: | ---: |
| Persistence | 3.128548 | 3.128548 | reference |
| Moving average | 5.123242 | 5.123242 | -63.76% |
| Ridge | 3.133439 | 3.118715 | -0.16% |
| Boosted stumps | 3.110252 | 3.115539 | +0.58% |

The tuning-selected model **did not beat persistence on the frozen holdout**.
The report retains that result rather than selecting the best test score after
the fact. The small boosted-stump advantage is descriptive only: no statistical
significance or profitability is established. See [AAPL_MODEL_CARD.md](AAPL_MODEL_CARD.md).

The run IDs in the example cards refer to the local acceptance databases, which
are not shipped. Repeat the documented commands to create your own IDs and
replayable artifacts. Provider revisions can change the dataset hash. Multi-asset
validation, vintage data, transaction costs, and operational deployment remain
outside this acceptance record.
