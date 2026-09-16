package market

import (
	"fmt"
	"strings"
	"time"
)

// ModelCardMarkdown renders the persisted evidence, never a generated claim.
func ModelCardMarkdown(c PredictionModelCard) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Prediction model card: %s\n\n", c.Symbol)
	fmt.Fprintf(&b, "Version: `%s`  \nExperiment: `%s`  \nFingerprint: `%s`  \nDataset SHA-256: `%s`\n\n", c.Version, c.ExperimentID, c.Fingerprint, c.DatasetHash)
	fmt.Fprintf(&b, "Dataset: **%s**, %d %s candles, %s through %s. Horizon: %d observed bar(s).\n\n", c.Source, c.CandleCount, c.Interval, cardDate(c.From), cardDate(c.To), c.Config.HorizonBars)
	fmt.Fprintf(&b, "Selected using tuning MAE only: **%s**. Holdout/test scores do not select the model.\n\n", c.SelectedByTuning.Name)
	fmt.Fprint(&b, "## Chronological partitions\n\n| Partition | Samples | First origin | Last origin | Last target |\n| --- | ---: | --- | --- | --- |\n")
	for _, p := range []struct {
		name string
		p    TimePartition
	}{{"Train", c.Split.Train}, {"Tune", c.Split.Tune}, {"Calibration", c.Split.Calibration}, {"Test", c.Split.Test}} {
		fmt.Fprintf(&b, "| %s | %d | %s | %s | %s |\n", p.name, p.p.Count, cardDate(p.p.FirstOrigin), cardDate(p.p.LastOrigin), cardDate(p.p.LastTarget))
	}
	fmt.Fprintf(&b, "\n%d overlapping-label samples purged. Refit interval: %d test origins; training window: %d eligible samples (0 = expanding). Nominal interval coverage: %.1f%%.\n\n", c.Split.Purged, c.Config.RefitEvery, c.Config.TrainWindow, c.Config.Coverage*100)
	fmt.Fprint(&b, "## Tuning trials\n\n| Model | Window | Ridge alpha | Boost rounds | Learning rate | Tuning MAE |\n| --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, trial := range c.Candidates {
		fmt.Fprintf(&b, "| %s | %d | %.3g | %d | %.3g | %.6f |\n", trial.Spec.Name, trial.Spec.Window, trial.Spec.Alpha, trial.Spec.Rounds, trial.Spec.LearningRate, trial.TuningMAE)
	}
	for _, mode := range []string{"Frozen holdout", "Walk-forward"} {
		fmt.Fprintf(&b, "\n## %s evaluation\n\n", mode)
		fmt.Fprint(&b, "| Model | N | MAE | RMSE | MAPE | Direction | Coverage | Coverage gap | Mean width | MAE improvement | Beats persistence? |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
		for _, model := range c.Models {
			e := model.Holdout
			if mode == "Walk-forward" {
				e = model.WalkForward
			}
			m := e.Metrics
			fmt.Fprintf(&b, "| %s | %d | %.6f | %.6f | %.3f%% | %.2f%% | %.2f%% | %.2f pp | %.6f | %+.2f%% | %t |\n", model.Spec.Name, m.Observations, m.MAE, m.RMSE, m.MAPE*100, m.DirectionAccuracy*100, m.IntervalCoverage*100, m.CalibrationError*100, m.MeanIntervalWidth, e.MAEImprovement*100, e.BeatsPersistence)
		}
	}
	fmt.Fprint(&b, "\nMAE, RMSE, and interval width are in quote-currency units; MAPE is presented as a percentage. Direction compares negative/zero/positive changes (a persistence forecast predicts zero). A positive improvement is lower MAE than persistence; it is not a significance test or a return claim.\n\n## Point-in-time regime slices (frozen holdout)\n\nRegimes use only the origin's five-bar return: above +1% = uptrend, below -1% = downtrend, otherwise flat.\n\n| Model | Regime | N | MAE | RMSE | Direction |\n| --- | --- | ---: | ---: | ---: | ---: |\n")
	for _, model := range c.Models {
		for _, regime := range []string{"uptrend", "flat", "downtrend"} {
			if m, ok := model.Holdout.Regimes[regime]; ok {
				fmt.Fprintf(&b, "| %s | %s | %d | %.6f | %.6f | %.2f%% |\n", model.Spec.Name, regime, m.Observations, m.MAE, m.RMSE, m.DirectionAccuracy*100)
			}
		}
	}
	fmt.Fprint(&b, "\n## Replayable model runs\n\n| Model | Frozen run ID | Walk-forward folds |\n| --- | --- | ---: |\n")
	for _, m := range c.Models {
		fmt.Fprintf(&b, "| %s | `%s` | %d |\n", m.Spec.Name, m.HoldoutRunID, len(m.WalkForwardRunIDs))
	}
	fmt.Fprint(&b, "\nThe database stores every frozen/fold predictor, normalization parameters, residual radius, training/calibration hashes, forecasts, and realized outcomes. JSON model cards include fold run IDs and regime metrics for both modes.\n\n## Limitations\n\n")
	for _, line := range c.Limitations {
		fmt.Fprintf(&b, "- %s\n", line)
	}
	return b.String()
}

func cardDate(timestamp int64) string { return time.Unix(timestamp, 0).UTC().Format("2006-01-02") }
