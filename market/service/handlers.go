package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"byodb/market"
)

type JobDependencies struct {
	Repository *market.Repository
	Sync       *market.SyncService
	Analysis   *market.AnalysisService
	Now        func() time.Time
}

func RegisterBuiltinJobs(manager *JobManager, deps JobDependencies) error {
	if manager == nil || deps.Repository == nil {
		return fmt.Errorf("built-in jobs require a manager and repository")
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	registrations := []struct {
		kind    string
		handler JobHandler
	}{
		{"seed_demo", seedDemoHandler(deps)},
		{"recompute_features", featureHandler(deps)},
		{"prediction_experiment", experimentHandler(deps)},
		{"predict", predictHandler(deps)},
		{"evaluate_forecasts", evaluateHandler(deps)},
	}
	if deps.Sync != nil {
		registrations = append(registrations, struct {
			kind    string
			handler JobHandler
		}{"provider_sync", syncHandler(deps)})
	}
	if deps.Analysis != nil {
		registrations = append(registrations, struct {
			kind    string
			handler JobHandler
		}{"grounded_analysis", analysisHandler(deps)})
	}
	for _, registration := range registrations {
		if err := manager.Register(registration.kind, registration.handler); err != nil {
			return err
		}
	}
	return nil
}

func decodePayload(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &JobError{Code: "invalid_request", Err: err}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &JobError{Code: "invalid_request", Err: fmt.Errorf("request contains trailing JSON")}
	}
	return nil
}

func seedDemoHandler(deps JobDependencies) JobHandler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var request struct{}
		if err := decodePayload(raw, &request); err != nil {
			return nil, err
		}
		return deps.Repository.SeedServiceDemo(ctx, deps.Now())
	}
}

func featureHandler(deps JobDependencies) JobHandler {
	type request struct {
		Symbol      string `json:"symbol"`
		Interval    string `json:"interval"`
		ChangedFrom int64  `json:"changed_from,omitempty"`
		Through     int64  `json:"through,omitempty"`
		FeatureSet  string `json:"feature_set,omitempty"`
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var input request
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := deps.Repository.ComputeFeaturesIncremental(input.Symbol, input.Interval, input.ChangedFrom, input.Through, input.FeatureSet)
		return map[string]any{"features_computed": count, "symbol": input.Symbol, "interval": input.Interval}, err
	}
}

func experimentHandler(deps JobDependencies) JobHandler {
	type request struct {
		Symbol   string                   `json:"symbol"`
		Interval string                   `json:"interval"`
		From     int64                    `json:"from,omitempty"`
		To       int64                    `json:"to,omitempty"`
		Config   *market.PredictionConfig `json:"config,omitempty"`
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var input request
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		config := market.DefaultPredictionConfig()
		if input.Config != nil {
			config = *input.Config
		}
		card, err := deps.Repository.RunPredictionExperiment(ctx, input.Symbol, input.Interval, input.From, input.To, config)
		if err != nil {
			return nil, err
		}
		return map[string]any{"experiment_id": card.ExperimentID, "selected_model": card.SelectedByTuning.Name, "candle_count": card.CandleCount, "test_observations": card.Split.Test.Count}, nil
	}
}

func predictHandler(deps JobDependencies) JobHandler {
	type request struct {
		RunID string `json:"run_id"`
		AsOf  int64  `json:"as_of,omitempty"`
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var input request
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		return deps.Repository.PredictNextDaily(ctx, input.RunID, input.AsOf, market.NewNYSECalendar())
	}
}

func evaluateHandler(deps JobDependencies) JobHandler {
	type request struct {
		Symbol          string `json:"symbol"`
		Interval        string `json:"interval"`
		TargetTimestamp int64  `json:"target_timestamp"`
		ActualClose     int64  `json:"actual_close"`
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var input request
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := deps.Repository.EvaluateForecastsAt(input.Symbol, input.Interval, input.TargetTimestamp, input.ActualClose)
		return map[string]any{"forecasts_evaluated": count}, err
	}
}

func syncHandler(deps JobDependencies) JobHandler {
	type request struct {
		Symbol               string `json:"symbol"`
		Interval             string `json:"interval"`
		Start                int64  `json:"start"`
		End                  int64  `json:"end,omitempty"`
		ResumeOverlapPeriods int    `json:"resume_overlap_periods,omitempty"`
		SyncCorporateActions bool   `json:"sync_corporate_actions,omitempty"`
		ComputeFeatures      bool   `json:"compute_features,omitempty"`
		FeatureSet           string `json:"feature_set,omitempty"`
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var input request
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		result, err := deps.Sync.Sync(ctx, market.SyncRequest{Symbol: input.Symbol, Interval: input.Interval, Start: input.Start, End: input.End, ResumeOverlapPeriods: input.ResumeOverlapPeriods, SyncCorporateActions: input.SyncCorporateActions, ComputeFeatures: input.ComputeFeatures, FeatureSet: input.FeatureSet, Calendar: market.NewNYSECalendar()})
		if err != nil {
			return nil, err
		}
		return map[string]any{"run_id": result.Run.RunID, "received": result.Run.Received, "inserted": result.Run.Inserted, "updated": result.Run.Updated, "features_computed": result.FeaturesComputed, "quality_report_id": result.Quality.ReportID}, nil
	}
}

func analysisHandler(deps JobDependencies) JobHandler {
	type request struct {
		Symbol   string `json:"symbol"`
		Interval string `json:"interval"`
		AsOf     int64  `json:"as_of,omitempty"`
		RunID    string `json:"run_id,omitempty"`
	}
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var input request
		if err := decodePayload(raw, &input); err != nil {
			return nil, err
		}
		report, err := deps.Analysis.AnalyzeReport(ctx, input.Symbol, input.Interval, input.AsOf, input.RunID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"analysis_id": report.Analysis.AnalysisID, "status": report.Status, "outlook": report.Decision.Outlook, "claims": len(report.Claims)}, nil
	}
}
