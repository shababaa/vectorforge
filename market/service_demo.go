package market

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const serviceDemoMetadataKey = "service-demo-v1"

// ServiceDemoManifest points the HTTP layer to already-normalized records. It
// contains no derived market claims and can be safely returned by the API.
type ServiceDemoManifest struct {
	Version           string `json:"version"`
	Symbol            string `json:"symbol"`
	Interval          string `json:"interval"`
	ExperimentID      string `json:"experiment_id"`
	ModelRunID        string `json:"model_run_id"`
	AnalysisID        string `json:"analysis_id"`
	ForecastAsOf      int64  `json:"forecast_as_of"`
	ForecastHorizon   int64  `json:"forecast_horizon_seconds"`
	LatestCandleAt    int64  `json:"latest_candle_at"`
	SeededAt          int64  `json:"seeded_at"`
	SyntheticDataOnly bool   `json:"synthetic_data_only"`
}

func (r *Repository) ServiceDemoManifest() (ServiceDemoManifest, bool, error) {
	var manifest ServiceDemoManifest
	raw, ok, err := r.AppMetadata(serviceDemoMetadataKey)
	if err != nil || !ok {
		return manifest, ok, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Version != serviceDemoMetadataKey || manifest.Symbol == "" || manifest.ExperimentID == "" || manifest.ModelRunID == "" || manifest.AnalysisID == "" {
		return ServiceDemoManifest{}, false, errors.New("invalid service demo manifest")
	}
	return manifest, true, nil
}

// SeedServiceDemo builds a complete, deterministic synthetic workflow once.
// Repeated starts reuse the manifest instead of creating duplicate experiments.
func (r *Repository) SeedServiceDemo(ctx context.Context, now time.Time) (ServiceDemoManifest, error) {
	if manifest, ok, err := r.ServiceDemoManifest(); err != nil || ok {
		return manifest, err
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	if err := ctx.Err(); err != nil {
		return ServiceDemoManifest{}, err
	}
	if err := r.UpsertInstrument(Instrument{Symbol: "SYNTH", Name: "Synthetic University Market Fixture", AssetType: "synthetic", Exchange: "XNYS", Currency: "USD", Active: true}); err != nil {
		return ServiceDemoManifest{}, err
	}
	candles := AnalystDemoCandles(now)
	if _, err := r.IngestCandles(candles); err != nil {
		return ServiceDemoManifest{}, err
	}
	last := candles[len(candles)-1]
	if _, err := r.ComputeFeatureSnapshot(last.Symbol, last.Interval, last.Timestamp, DefaultFeatureSet); err != nil {
		return ServiceDemoManifest{}, err
	}
	card, err := r.RunPredictionExperiment(ctx, last.Symbol, last.Interval, candles[0].Timestamp, last.Timestamp, DefaultPredictionConfig())
	if err != nil {
		return ServiceDemoManifest{}, err
	}
	selectedRunID := ""
	for _, result := range card.Models {
		if result.Spec == card.SelectedByTuning {
			selectedRunID = result.HoldoutRunID
			break
		}
	}
	if selectedRunID == "" {
		return ServiceDemoManifest{}, errors.New("selected prediction model has no holdout artifact")
	}
	forecast, err := r.PredictNextDaily(ctx, selectedRunID, last.Timestamp, NewNYSECalendar())
	if err != nil {
		return ServiceDemoManifest{}, err
	}
	analyst, err := NewAnalysisService(r, FixtureAnalyst{}, "fixture", "deterministic-not-an-llm", AnalystVersion)
	if err != nil {
		return ServiceDemoManifest{}, err
	}
	report, err := analyst.AnalyzeReport(ctx, last.Symbol, last.Interval, r.now().UTC().Unix(), selectedRunID)
	if err != nil {
		return ServiceDemoManifest{}, err
	}
	manifest := ServiceDemoManifest{
		Version: serviceDemoMetadataKey, Symbol: last.Symbol, Interval: last.Interval,
		ExperimentID: card.ExperimentID, ModelRunID: selectedRunID, AnalysisID: report.Analysis.AnalysisID,
		ForecastAsOf: forecast.AsOfTimestamp, ForecastHorizon: forecast.HorizonSeconds, LatestCandleAt: last.Timestamp,
		SeededAt: now.Unix(), SyntheticDataOnly: true,
	}
	if err := r.PutAppMetadata(serviceDemoMetadataKey, manifest); err != nil {
		return ServiceDemoManifest{}, err
	}
	return manifest, nil
}
