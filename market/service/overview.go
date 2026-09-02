package service

import (
	"byodb"
	"byodb/market"
)

type ModelSummary struct {
	ExperimentID    string                   `json:"experiment_id"`
	SelectedSpec    market.ModelSpec         `json:"selected_spec"`
	SelectedRunID   string                   `json:"selected_run_id"`
	TuningMAE       float64                  `json:"tuning_mae"`
	Holdout         market.EvaluationSummary `json:"holdout"`
	WalkForward     market.EvaluationSummary `json:"walk_forward"`
	DatasetHash     string                   `json:"dataset_hash"`
	CandleCount     int                      `json:"candle_count"`
	NominalCoverage float64                  `json:"nominal_coverage"`
	Limitations     []string                 `json:"limitations"`
}

type AnalysisSummary struct {
	AnalysisID  string                   `json:"analysis_id"`
	Status      string                   `json:"status"`
	Provider    string                   `json:"provider"`
	Model       string                   `json:"model"`
	Thesis      string                   `json:"thesis"`
	Outlook     string                   `json:"outlook"`
	Claims      []market.AnalystEvidence `json:"claims"`
	Warnings    []string                 `json:"warnings"`
	Limitations []string                 `json:"limitations"`
}

// ForecastView is the stable HTTP representation of a stored forecast. Domain
// records intentionally mirror the storage schema and are not an API contract;
// keeping the JSON tags here prevents Go field names from leaking to clients.
type ForecastView struct {
	RunID                 string `json:"run_id"`
	Symbol                string `json:"symbol"`
	Interval              string `json:"interval"`
	AsOfTimestamp         int64  `json:"as_of_timestamp"`
	HorizonSeconds        int64  `json:"horizon_seconds"`
	TargetTimestamp       int64  `json:"target_timestamp"`
	BaselineClose         int64  `json:"baseline_close"`
	PredictedClose        int64  `json:"predicted_close"`
	LowerBound            int64  `json:"lower_bound"`
	UpperBound            int64  `json:"upper_bound"`
	ConfidencePPM         int64  `json:"confidence_ppm"`
	HasActual             bool   `json:"has_actual"`
	ActualClose           int64  `json:"actual_close"`
	EvaluatedAt           int64  `json:"evaluated_at"`
	AbsoluteError         int64  `json:"absolute_error"`
	AbsolutePercentagePPM int64  `json:"absolute_percentage_ppm"`
	DirectionCorrect      bool   `json:"direction_correct"`
	CreatedAt             int64  `json:"created_at"`
}

func forecastView(value market.Forecast) ForecastView {
	return ForecastView{
		RunID: value.RunID, Symbol: value.Symbol, Interval: value.Interval,
		AsOfTimestamp: value.AsOfTimestamp, HorizonSeconds: value.HorizonSeconds,
		TargetTimestamp: value.TargetTimestamp, BaselineClose: value.BaselineClose,
		PredictedClose: value.PredictedClose, LowerBound: value.LowerBound,
		UpperBound: value.UpperBound, ConfidencePPM: value.ConfidencePPM,
		HasActual: value.HasActual, ActualClose: value.ActualClose,
		EvaluatedAt: value.EvaluatedAt, AbsoluteError: value.AbsoluteError,
		AbsolutePercentagePPM: value.AbsolutePercentagePPM,
		DirectionCorrect:      value.DirectionCorrect, CreatedAt: value.CreatedAt,
	}
}

type Overview struct {
	Symbol    string                      `json:"symbol"`
	Interval  string                      `json:"interval"`
	Candles   []market.Candle             `json:"candles"`
	Feature   *market.FeatureSnapshot     `json:"feature,omitempty"`
	Forecasts []ForecastView              `json:"forecasts"`
	Model     *ModelSummary               `json:"model,omitempty"`
	Analysis  *AnalysisSummary            `json:"analysis,omitempty"`
	Demo      *market.ServiceDemoManifest `json:"demo,omitempty"`
	Storage   byodb.DBStats               `json:"storage"`
	Notices   []string                    `json:"notices"`
}

func buildOverview(repository *market.Repository, symbol, interval string, limit int) (Overview, error) {
	result := Overview{Symbol: symbol, Interval: interval, Forecasts: []ForecastView{}, Notices: []string{
		"Educational system; outputs are not financial advice.",
		"Prediction intervals show nominal empirical coverage, not profit probability.",
	}}
	candles, err := repository.Candles(market.CandleQuery{Symbol: symbol, Interval: interval, Limit: limit, Descending: true})
	if err != nil {
		return result, err
	}
	for i, j := 0, len(candles)-1; i < j; i, j = i+1, j-1 {
		candles[i], candles[j] = candles[j], candles[i]
	}
	result.Candles = candles
	if len(candles) > 0 {
		feature, ok, err := repository.Feature(symbol, interval, candles[len(candles)-1].Timestamp, market.DefaultFeatureSet)
		if err != nil {
			return result, err
		}
		if ok {
			result.Feature = &feature
		}
	}
	result.Storage, err = repository.StorageStats()
	if err != nil {
		return result, err
	}
	manifest, ok, err := repository.ServiceDemoManifest()
	if err != nil {
		return result, err
	}
	if !ok || manifest.Symbol != symbol || manifest.Interval != interval {
		return result, nil
	}
	result.Demo = &manifest
	forecasts, err := repository.ForecastsAt(manifest.ModelRunID, symbol, interval, manifest.ForecastAsOf, 10)
	if err != nil {
		return result, err
	}
	result.Forecasts = make([]ForecastView, len(forecasts))
	for index := range forecasts {
		result.Forecasts[index] = forecastView(forecasts[index])
	}
	card, err := repository.PredictionModelCard(manifest.ExperimentID)
	if err != nil {
		return result, err
	}
	model := ModelSummary{ExperimentID: card.ExperimentID, SelectedSpec: card.SelectedByTuning, SelectedRunID: manifest.ModelRunID, DatasetHash: card.DatasetHash, CandleCount: card.CandleCount, NominalCoverage: card.Config.Coverage, Limitations: append([]string(nil), card.Limitations...)}
	for _, candidate := range card.Candidates {
		if candidate.Spec == card.SelectedByTuning {
			model.TuningMAE = candidate.TuningMAE
			break
		}
	}
	for _, candidate := range card.Models {
		if candidate.HoldoutRunID == manifest.ModelRunID {
			model.Holdout, model.WalkForward = candidate.Holdout, candidate.WalkForward
			break
		}
	}
	result.Model = &model
	report, err := repository.LoadAnalysisReport(manifest.AnalysisID)
	if err != nil {
		return result, err
	}
	result.Analysis = &AnalysisSummary{AnalysisID: report.Analysis.AnalysisID, Status: report.Status, Provider: report.Analysis.Provider, Model: report.Analysis.Model, Thesis: report.Analysis.Thesis, Outlook: report.Decision.Outlook, Claims: append([]market.AnalystEvidence(nil), report.Claims...), Warnings: append([]string(nil), report.Context.Warnings...), Limitations: append([]string(nil), report.Limitations...)}
	return result, nil
}
