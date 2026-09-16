<!-- Live acceptance captured 2026-08-31 using the provider demo key. Vendor history can change; the dataset hash identifies this snapshot. Run IDs refer to the local validation database, not a shipped database file. -->

# Prediction model card: AAPL

Version: `prediction-v1`  
Experiment: `experiment_744808316791b6d4c881747b`  
Fingerprint: `2201fdd14ee8c6c4ed41209a31bc119df6f7c15fe5b6b4df40a4b7a080080e62`  
Dataset SHA-256: `da3362c6b5226bffeb8a01c5b882845df51ed2a64b8c0b9239d56228d1ba1cea`

Dataset: **twelvedata**, 1254 1d candles, 2021-08-30 through 2026-08-27. Horizon: 1 observed bar(s).

Selected using tuning MAE only: **ridge**. Holdout/test scores do not select the model.

## Chronological partitions

| Partition | Samples | First origin | Last origin | Last target |
| --- | ---: | --- | --- | --- |
| Train | 738 | 2021-09-28 | 2024-09-04 | 2024-09-05 |
| Tune | 122 | 2024-09-06 | 2025-03-04 | 2025-03-05 |
| Calibration | 122 | 2025-03-06 | 2025-08-28 | 2025-08-29 |
| Test | 248 | 2025-09-02 | 2026-08-26 | 2026-08-27 |

3 overlapping-label samples purged. Refit interval: 63 test origins; training window: 0 eligible samples (0 = expanding). Nominal interval coverage: 90.0%.

## Tuning trials

| Model | Window | Ridge alpha | Boost rounds | Learning rate | Tuning MAE |
| --- | ---: | ---: | ---: | ---: | ---: |
| persistence | 0 | 0 | 0 | 0 | 2.408524 |
| moving-average | 5 | 0 | 0 | 0 | 3.926262 |
| moving-average | 20 | 0 | 0 | 0 | 6.728627 |
| ridge | 0 | 0.01 | 0 | 0 | 2.386424 |
| ridge | 0 | 0.1 | 0 | 0 | 2.391908 |
| ridge | 0 | 1 | 0 | 0 | 2.398557 |
| gradient-boosted-stumps | 0 | 0 | 32 | 0.05 | 2.407148 |
| gradient-boosted-stumps | 0 | 0 | 64 | 0.05 | 2.417084 |

## Frozen holdout evaluation

| Model | N | MAE | RMSE | MAPE | Direction | Coverage | Coverage gap | Mean width | MAE improvement | Beats persistence? |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| persistence | 248 | 3.128548 | 4.469055 | 1.127% | 0.00% | 96.77% | 6.77 pp | 21.050931 | +0.00% | false |
| moving-average | 248 | 5.123242 | 6.905020 | 1.842% | 51.61% | 96.77% | 6.77 pp | 33.700477 | -63.76% | false |
| ridge | 248 | 3.133439 | 4.489916 | 1.128% | 48.39% | 97.98% | 7.98 pp | 22.617710 | -0.16% | false |
| gradient-boosted-stumps | 248 | 3.110252 | 4.446600 | 1.121% | 54.03% | 97.98% | 7.98 pp | 21.735927 | +0.58% | true |

## Walk-forward evaluation

| Model | N | MAE | RMSE | MAPE | Direction | Coverage | Coverage gap | Mean width | MAE improvement | Beats persistence? |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| persistence | 248 | 3.128548 | 4.469055 | 1.127% | 0.00% | 90.32% | 0.32 pp | 15.387386 | +0.00% | false |
| moving-average | 248 | 5.123242 | 6.905020 | 1.842% | 51.61% | 91.13% | 1.13 pp | 23.756161 | -63.76% | false |
| ridge | 248 | 3.118715 | 4.465902 | 1.124% | 51.61% | 91.94% | 1.94 pp | 16.038445 | +0.31% | true |
| gradient-boosted-stumps | 248 | 3.115539 | 4.459473 | 1.123% | 52.82% | 90.73% | 0.73 pp | 15.411799 | +0.42% | true |

MAE, RMSE, and interval width are in quote-currency units; MAPE is presented as a percentage. Direction compares negative/zero/positive changes (a persistence forecast predicts zero). A positive improvement is lower MAE than persistence; it is not a significance test or a return claim.

## Point-in-time regime slices (frozen holdout)

Regimes use only the origin's five-bar return: above +1% = uptrend, below -1% = downtrend, otherwise flat.

| Model | Regime | N | MAE | RMSE | Direction |
| --- | --- | ---: | ---: | ---: | ---: |
| persistence | uptrend | 123 | 3.235283 | 4.567281 | 0.00% |
| persistence | flat | 49 | 2.602044 | 4.055741 | 0.00% |
| persistence | downtrend | 76 | 3.295263 | 4.561536 | 0.00% |
| moving-average | uptrend | 123 | 5.543350 | 7.102145 | 47.97% |
| moving-average | flat | 49 | 3.741635 | 5.670190 | 46.94% |
| moving-average | downtrend | 76 | 5.334104 | 7.295354 | 60.53% |
| ridge | uptrend | 123 | 3.240516 | 4.583344 | 47.15% |
| ridge | flat | 49 | 2.675024 | 4.107316 | 38.78% |
| ridge | downtrend | 76 | 3.255700 | 4.572542 | 56.58% |
| gradient-boosted-stumps | uptrend | 123 | 3.233673 | 4.559263 | 50.41% |
| gradient-boosted-stumps | flat | 49 | 2.601984 | 4.045679 | 51.02% |
| gradient-boosted-stumps | downtrend | 76 | 3.238203 | 4.508362 | 61.84% |

## Replayable model runs

| Model | Frozen run ID | Walk-forward folds |
| --- | --- | ---: |
| persistence | `model_db6a5e5dd8555bfe1db99a24` | 4 |
| moving-average | `model_20389bc0699999bf86433c84` | 4 |
| ridge | `model_adfd9fae7a21c42a37921b4c` | 4 |
| gradient-boosted-stumps | `model_f5dfd48ac283a7ef1d82b003` | 4 |

The database stores every frozen/fold predictor, normalization parameters, residual radius, training/calibration hashes, forecasts, and realized outcomes. JSON model cards include fold run IDs and regime metrics for both modes.

## Limitations

- Research evaluation, not financial advice or evidence of profitable trading; costs, slippage, and execution are not modeled.
- Adjusted history may be revised retrospectively; point-in-time feature construction is not point-in-time vendor vintage data.
- Empirical residual intervals have no guaranteed time-series coverage; confidence_ppm stores nominal interval coverage, not probability of profit.
- Horizon counts observed bars; missing sessions must be reviewed in Phase 2 quality reports. Current bars may be incomplete: choose an explicit completed end date.
- One asset/source per experiment; sample selection and survivorship bias remain. Repeat on an untouched multi-asset dataset before promotion.
- Holdout fits are frozen; walk-forward fits may learn only previously realized test labels. Tuning specs never use test scores.

