package market

import (
	"fmt"
	"math"
	"sort"
)

type PredictionMetrics struct {
	Observations      int     `json:"observations"`
	MAE               float64 `json:"mae"`
	RMSE              float64 `json:"rmse"`
	MAPE              float64 `json:"mape"` // fraction, not percent
	DirectionAccuracy float64 `json:"direction_accuracy"`
	IntervalCoverage  float64 `json:"interval_coverage"`
	CalibrationError  float64 `json:"calibration_error"`
	MeanIntervalWidth float64 `json:"mean_interval_width"`
}

type EvaluationSummary struct {
	Metrics          PredictionMetrics            `json:"metrics"`
	Regimes          map[string]PredictionMetrics `json:"regimes"`
	MAEImprovement   float64                      `json:"mae_improvement_vs_persistence"`
	BeatsPersistence bool                         `json:"beats_persistence_mae"`
}

type scoredPrediction struct {
	forecast Forecast
	regime   string
}

func intervalRadius(model FittedPredictor, rows []predictionSample, coverage float64) (float64, error) {
	if len(rows) < 10 {
		return 0, ErrInsufficientHistory
	}
	residual := make([]float64, len(rows))
	for i, s := range rows {
		p, err := predictSample(model, s)
		if err != nil {
			return 0, err
		}
		residual[i] = math.Abs(s.Y - p)
	}
	sort.Float64s(residual)
	// Finite-sample corrected empirical residual quantile. Time-series data is
	// not exchangeable: this is an empirical interval, NOT a coverage guarantee.
	rank := min(len(residual), int(math.Ceil(float64(len(residual)+1)*coverage)))
	return residual[max(0, rank-1)], nil
}

func scorePredictor(model FittedPredictor, rows []predictionSample, radius, coverage float64, runID string, now int64) ([]scoredPrediction, error) {
	out := make([]scoredPrediction, 0, len(rows))
	for _, s := range rows {
		p, err := predictSample(model, s)
		if err != nil {
			return nil, err
		}
		price, err := scaledLogPrice(s.Close, p)
		if err != nil {
			return nil, err
		}
		low, err := scaledLogPrice(s.Close, p-radius)
		if err != nil {
			return nil, err
		}
		high, err := scaledLogPrice(s.Close, p+radius)
		if err != nil {
			return nil, err
		}
		f := Forecast{RunID: runID, AsOfTimestamp: s.AsOf, TargetTimestamp: s.Target, HorizonSeconds: s.Target - s.AsOf, BaselineClose: s.Close, PredictedClose: price, LowerBound: low, UpperBound: high, ConfidencePPM: int64(math.Round(coverage * float64(RatioScale))), HasActual: true, ActualClose: s.Actual, CreatedAt: now, EvaluatedAt: now}
		f.AbsoluteError = abs64(f.PredictedClose - f.ActualClose)
		f.AbsolutePercentagePPM = int64(math.Round(float64(f.AbsoluteError) / float64(f.ActualClose) * float64(RatioScale)))
		f.DirectionCorrect = direction(f.PredictedClose-f.BaselineClose) == direction(f.ActualClose-f.BaselineClose)
		out = append(out, scoredPrediction{f, s.Regime})
	}
	return out, nil
}

func scaledLogPrice(closePrice int64, logReturn float64) (int64, error) {
	value := float64(closePrice) * math.Exp(logReturn)
	if !finite(value) || value >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("%w: predicted price overflow", ErrInvalidMarketData)
	}
	return max(1, int64(math.Round(value))), nil
}

func predictionMetrics(rows []scoredPrediction, nominal float64) PredictionMetrics {
	m := PredictionMetrics{Observations: len(rows)}
	if len(rows) == 0 {
		return m
	}
	var sq float64
	for _, row := range rows {
		f := row.forecast
		e := float64(f.AbsoluteError) / float64(PriceScale)
		m.MAE += e
		sq += e * e
		m.MAPE += float64(f.AbsoluteError) / float64(f.ActualClose)
		if f.DirectionCorrect {
			m.DirectionAccuracy++
		}
		if f.ActualClose >= f.LowerBound && f.ActualClose <= f.UpperBound {
			m.IntervalCoverage++
		}
		m.MeanIntervalWidth += float64(f.UpperBound-f.LowerBound) / float64(PriceScale)
	}
	n := float64(len(rows))
	m.MAE /= n
	m.RMSE = math.Sqrt(sq / n)
	m.MAPE /= n
	m.DirectionAccuracy /= n
	m.IntervalCoverage /= n
	m.CalibrationError = math.Abs(m.IntervalCoverage - nominal)
	m.MeanIntervalWidth /= n
	return m
}

func summarizePredictions(rows []scoredPrediction, nominal float64) EvaluationSummary {
	s := EvaluationSummary{Metrics: predictionMetrics(rows, nominal), Regimes: map[string]PredictionMetrics{}}
	for _, name := range []string{"uptrend", "flat", "downtrend"} {
		var group []scoredPrediction
		for _, r := range rows {
			if r.regime == name {
				group = append(group, r)
			}
		}
		if len(group) > 0 {
			s.Regimes[name] = predictionMetrics(group, nominal)
		}
	}
	return s
}

func comparePersistence(s *EvaluationSummary, baseline float64) {
	if baseline > 0 {
		s.MAEImprovement = 1 - s.Metrics.MAE/baseline
	}
	s.BeatsPersistence = s.Metrics.MAE < baseline
}
