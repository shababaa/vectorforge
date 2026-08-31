<!-- Generated from experiment-demo on 2026-08-31. Run IDs refer to that local snapshot; rerun the demo to create your own replayable database. -->

# Prediction model card: SYNTH

Version: `prediction-v1`  
Experiment: `experiment_318b0bdf516ad8ce7e7847cc`  
Fingerprint: `4293f0da9ab3f079c799648c900d60045fe52ac188e2b9ecb0379123239fc0d6`  
Dataset SHA-256: `b4ce07c27d7306ad627446cebedfa71a910b848f325ed7683555da37bdabc1f3`

Dataset: **synthetic-phase3-v1**, 600 1d candles, 2022-01-03 through 2024-05-22. Horizon: 1 observed bar(s).

Selected using tuning MAE only: **gradient-boosted-stumps**. Holdout/test scores do not select the model.

## Chronological partitions

| Partition | Samples | First origin | Last origin | Last target |
| --- | ---: | --- | --- | --- |
| Train | 346 | 2022-02-01 | 2023-06-16 | 2023-06-20 |
| Tune | 56 | 2023-06-21 | 2023-09-08 | 2023-09-11 |
| Calibration | 57 | 2023-09-12 | 2023-11-30 | 2023-12-01 |
| Test | 117 | 2023-12-04 | 2024-05-21 | 2024-05-22 |

3 overlapping-label samples purged. Refit interval: 63 test origins; training window: 0 eligible samples (0 = expanding). Nominal interval coverage: 90.0%.

## Tuning trials

| Model | Window | Ridge alpha | Boost rounds | Learning rate | Tuning MAE |
| --- | ---: | ---: | ---: | ---: | ---: |
| persistence | 0 | 0 | 0 | 0 | 0.483667 |
| moving-average | 5 | 0 | 0 | 0 | 1.221752 |
| moving-average | 20 | 0 | 0 | 0 | 3.324638 |
| ridge | 0 | 0.01 | 0 | 0 | 0.397505 |
| ridge | 0 | 0.1 | 0 | 0 | 0.399875 |
| ridge | 0 | 1 | 0 | 0 | 0.405386 |
| gradient-boosted-stumps | 0 | 0 | 32 | 0.05 | 0.391241 |
| gradient-boosted-stumps | 0 | 0 | 64 | 0.05 | 0.386008 |

## Frozen holdout evaluation

| Model | N | MAE | RMSE | MAPE | Direction | Coverage | Coverage gap | Mean width | MAE improvement | Beats persistence? |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| persistence | 117 | 0.443923 | 0.537219 | 0.347% | 0.00% | 99.15% | 9.15 pp | 2.290166 | +0.00% | false |
| moving-average | 117 | 0.866318 | 0.994110 | 0.678% | 35.90% | 99.15% | 9.15 pp | 4.115722 | -95.15% | false |
| ridge | 117 | 0.424135 | 0.504415 | 0.332% | 62.39% | 92.31% | 2.31 pp | 1.679485 | +4.46% | true |
| gradient-boosted-stumps | 117 | 0.427774 | 0.509747 | 0.334% | 64.96% | 85.47% | 4.53 pp | 1.547994 | +3.64% | true |

## Walk-forward evaluation

| Model | N | MAE | RMSE | MAPE | Direction | Coverage | Coverage gap | Mean width | MAE improvement | Beats persistence? |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| persistence | 117 | 0.443923 | 0.537219 | 0.347% | 0.00% | 96.58% | 6.58 pp | 2.127967 | +0.00% | false |
| moving-average | 117 | 0.866318 | 0.994110 | 0.678% | 35.90% | 94.02% | 4.02 pp | 3.594299 | -95.15% | false |
| ridge | 117 | 0.423392 | 0.503198 | 0.331% | 62.39% | 92.31% | 2.31 pp | 1.685041 | +4.62% | true |
| gradient-boosted-stumps | 117 | 0.429257 | 0.512024 | 0.336% | 62.39% | 88.03% | 1.97 pp | 1.594900 | +3.30% | true |

MAE, RMSE, and interval width are in quote-currency units; MAPE is presented as a percentage. Direction compares negative/zero/positive changes (a persistence forecast predicts zero). A positive improvement is lower MAE than persistence; it is not a significance test or a return claim.

## Point-in-time regime slices (frozen holdout)

Regimes use only the origin's five-bar return: above +1% = uptrend, below -1% = downtrend, otherwise flat.

| Model | Regime | N | MAE | RMSE | Direction |
| --- | --- | ---: | ---: | ---: | ---: |
| persistence | uptrend | 43 | 0.409680 | 0.526677 | 0.00% |
| persistence | flat | 50 | 0.475395 | 0.572525 | 0.00% |
| persistence | downtrend | 24 | 0.439709 | 0.476522 | 0.00% |
| moving-average | uptrend | 43 | 1.078335 | 1.171178 | 30.23% |
| moving-average | flat | 50 | 0.653613 | 0.797718 | 40.00% |
| moving-average | downtrend | 24 | 0.929588 | 1.017076 | 37.50% |
| ridge | uptrend | 43 | 0.387829 | 0.467098 | 69.77% |
| ridge | flat | 50 | 0.462274 | 0.550914 | 56.00% |
| ridge | downtrend | 24 | 0.409728 | 0.466004 | 62.50% |
| gradient-boosted-stumps | uptrend | 43 | 0.431045 | 0.510588 | 67.44% |
| gradient-boosted-stumps | flat | 50 | 0.447879 | 0.538583 | 64.00% |
| gradient-boosted-stumps | downtrend | 24 | 0.380025 | 0.441960 | 62.50% |

## Replayable model runs

| Model | Frozen run ID | Walk-forward folds |
| --- | --- | ---: |
| persistence | `model_ded2f4ba65b092c173c1c613` | 2 |
| moving-average | `model_e4a29a804aea9074233f203d` | 2 |
| ridge | `model_7e73a1858858d1973a906f28` | 2 |
| gradient-boosted-stumps | `model_c83b2f819ea9a28954245e5c` | 2 |

The database stores every frozen/fold predictor, normalization parameters, residual radius, training/calibration hashes, forecasts, and realized outcomes. JSON model cards include fold run IDs and regime metrics for both modes.

## Limitations

- Research evaluation, not financial advice or evidence of profitable trading; costs, slippage, and execution are not modeled.
- Adjusted history may be revised retrospectively; point-in-time feature construction is not point-in-time vendor vintage data.
- Empirical residual intervals have no guaranteed time-series coverage; confidence_ppm stores nominal interval coverage, not probability of profit.
- Horizon counts observed bars; missing sessions must be reviewed in Phase 2 quality reports. Current bars may be incomplete: choose an explicit completed end date.
- One asset/source per experiment; sample selection and survivorship bias remain. Repeat on an untouched multi-asset dataset before promotion.
- Holdout fits are frozen; walk-forward fits may learn only previously realized test labels. Tuning specs never use test scores.

