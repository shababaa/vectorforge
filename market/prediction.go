package market

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

type CandidateScore struct {
	Spec      ModelSpec `json:"spec"`
	TuningMAE float64   `json:"tuning_mae"`
}

type PredictionModelResult struct {
	Spec              ModelSpec         `json:"spec"`
	HoldoutRunID      string            `json:"holdout_run_id"`
	WalkForwardRunIDs []string          `json:"walk_forward_run_ids"`
	Holdout           EvaluationSummary `json:"holdout"`
	WalkForward       EvaluationSummary `json:"walk_forward"`
}

type PredictionModelCard struct {
	ExperimentID     string                  `json:"experiment_id"`
	Version          string                  `json:"version"`
	Fingerprint      string                  `json:"fingerprint"`
	DatasetHash      string                  `json:"dataset_hash"`
	Symbol           string                  `json:"symbol"`
	Interval         string                  `json:"interval"`
	Source           string                  `json:"source"`
	CandleCount      int                     `json:"candle_count"`
	From             int64                   `json:"from"`
	To               int64                   `json:"to"`
	Config           PredictionConfig        `json:"config"`
	Split            PredictionSplit         `json:"split"`
	FeatureNames     []string                `json:"feature_names"`
	SelectedByTuning ModelSpec               `json:"selected_by_tuning"`
	Candidates       []CandidateScore        `json:"candidates"`
	Models           []PredictionModelResult `json:"models"`
	Limitations      []string                `json:"limitations"`
}

type PredictorArtifact struct {
	Symbol          string          `json:"symbol"`
	Interval        string          `json:"interval"`
	Source          string          `json:"source"`
	Model           FittedPredictor `json:"model"`
	HorizonBars     int             `json:"horizon_bars"`
	NominalCoverage float64         `json:"nominal_coverage"`
	ResidualRadius  float64         `json:"residual_radius_log_return"`
	Training        TimePartition   `json:"training"`
	Calibration     TimePartition   `json:"calibration"`
	TrainingHash    string          `json:"training_hash"`
	CalibrationHash string          `json:"calibration_hash"`
}

func hashSamples(rows []predictionSample) string {
	b, _ := json.Marshal(rows)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// RunPredictionExperiment reads one consistent candle snapshot, selects specs
// solely on tuning data, then independently evaluates frozen and online modes.
func (r *Repository) RunPredictionExperiment(ctx context.Context, symbol, interval string, from, to int64, config PredictionConfig) (card PredictionModelCard, err error) {
	if err = config.validate(); err != nil {
		return card, err
	}
	if to == 0 {
		to = r.now().UTC().Unix()
	}
	candles, err := r.Candles(CandleQuery{Symbol: symbol, Interval: interval, From: from, To: to, Limit: 20_001})
	if err != nil {
		return card, err
	}
	rows, err := predictionSamples(candles, config.HorizonBars)
	if err != nil {
		return card, err
	}
	split, err := splitPredictionSamples(rows, config)
	if err != nil {
		return card, err
	}
	if err = ctx.Err(); err != nil {
		return card, err
	}
	id, err := newID("experiment")
	if err != nil {
		return card, err
	}
	hash := hashCandles(candles)
	fingerprintBytes, _ := json.Marshal(struct {
		Version, Hash string
		Config        PredictionConfig
	}{PredictionVersion, hash, config})
	fingerprint := sha256.Sum256(fingerprintBytes)
	card = PredictionModelCard{ExperimentID: id, Version: PredictionVersion, Fingerprint: hex.EncodeToString(fingerprint[:]), DatasetHash: hash, Symbol: candles[0].Symbol, Interval: candles[0].Interval, Source: candles[0].Source, CandleCount: len(candles), From: candles[0].Timestamp, To: candles[len(candles)-1].Timestamp, Config: config, Split: split.report, FeatureNames: append([]string(nil), predictionFeatureNames...), Limitations: []string{
		"Research evaluation, not financial advice or evidence of profitable trading; costs, slippage, and execution are not modeled.",
		"Adjusted history may be revised retrospectively; point-in-time feature construction is not point-in-time vendor vintage data.",
		"Empirical residual intervals have no guaranteed time-series coverage; confidence_ppm stores nominal interval coverage, not probability of profit.",
		"Horizon counts observed bars; missing sessions must be reviewed in Phase 2 quality reports. Current bars may be incomplete: choose an explicit completed end date.",
		"One asset/source per experiment; sample selection and survivorship bias remain. Repeat on an untouched multi-asset dataset before promotion.",
		"Holdout fits are frozen; walk-forward fits may learn only previously realized test labels. Tuning specs never use test scores.",
	}}
	period, _ := intervalSeconds(card.Interval)
	params, _ := json.Marshal(config)
	parent := ModelRun{RunID: id, ModelName: "prediction-experiment", ModelVersion: PredictionVersion, FeatureSet: DefaultFeatureSet, Target: "future_log_return", HorizonSeconds: period * int64(config.HorizonBars), TrainingStart: split.train[0].AsOf, TrainingEnd: split.train[len(split.train)-1].Target, ParametersJSON: string(params), DatasetHash: hash, Status: "running"}
	if err = r.PutModelRun(parent); err != nil {
		return card, err
	}
	defer func() {
		if err != nil {
			parent.Status = "failed"
			failure, _ := json.Marshal(map[string]string{"error": boundedString(err.Error(), 512)})
			parent.MetricsJSON = string(failure)
			_ = r.PutModelRun(parent)
		}
	}()
	selected := map[string]CandidateScore{}
	for _, spec := range candidateModels() {
		model, fitErr := fitPredictor(ctx, spec, split.train)
		if fitErr != nil {
			return card, fitErr
		}
		preds, scoreErr := scorePredictor(model, split.tune, 0, config.Coverage, "", 0)
		if scoreErr != nil {
			return card, scoreErr
		}
		score := CandidateScore{spec, predictionMetrics(preds, config.Coverage).MAE}
		card.Candidates = append(card.Candidates, score)
		if prior, ok := selected[spec.Name]; !ok || score.TuningMAE < prior.TuningMAE {
			selected[spec.Name] = score
		}
	}
	best := card.Candidates[0]
	for _, c := range card.Candidates {
		if c.TuningMAE < best.TuningMAE {
			best = c
		}
	}
	card.SelectedByTuning = best.Spec
	for _, name := range []string{"persistence", "moving-average", "ridge", "gradient-boosted-stumps"} {
		spec := selected[name].Spec
		fitRows := append(append([]predictionSample(nil), split.train...), split.tune...)
		artifact, fitErr := fitArtifact(ctx, spec, fitRows, split.calibration, config)
		if fitErr != nil {
			return card, fitErr
		}
		runID, idErr := newID("model")
		if idErr != nil {
			return card, idErr
		}
		preds, scoreErr := scorePredictor(artifact.Model, split.test, artifact.ResidualRadius, config.Coverage, runID, r.now().UTC().Unix())
		if scoreErr != nil {
			return card, scoreErr
		}
		result := PredictionModelResult{Spec: spec, HoldoutRunID: runID, Holdout: summarizePredictions(preds, config.Coverage)}
		if err = r.persistPredictionRun(ctx, card, runID, "holdout", 0, artifact, preds, result.Holdout.Metrics); err != nil {
			return card, err
		}
		var online []scoredPrediction
		for start := 0; start < len(split.test); start += config.RefitEvery {
			if err = ctx.Err(); err != nil {
				return card, err
			}
			block := split.test[start:min(start+config.RefitEvery, len(split.test))]
			eligible := beforeOrigin(rows, block[0].AsOf)
			if config.TrainWindow > 0 && len(eligible) > config.TrainWindow {
				eligible = eligible[len(eligible)-config.TrainWindow:]
			}
			cut := len(eligible) - len(split.calibration)
			cal := eligible[cut:]
			train := beforeOrigin(eligible[:cut], cal[0].AsOf)
			foldArtifact, fitErr := fitArtifact(ctx, spec, train, cal, config)
			if fitErr != nil {
				return card, fitErr
			}
			foldID, idErr := newID("model")
			if idErr != nil {
				return card, idErr
			}
			foldPreds, scoreErr := scorePredictor(foldArtifact.Model, block, foldArtifact.ResidualRadius, config.Coverage, foldID, r.now().UTC().Unix())
			if scoreErr != nil {
				return card, scoreErr
			}
			if err = r.persistPredictionRun(ctx, card, foldID, "walk-forward", start/config.RefitEvery, foldArtifact, foldPreds, predictionMetrics(foldPreds, config.Coverage)); err != nil {
				return card, err
			}
			result.WalkForwardRunIDs = append(result.WalkForwardRunIDs, foldID)
			online = append(online, foldPreds...)
		}
		result.WalkForward = summarizePredictions(online, config.Coverage)
		card.Models = append(card.Models, result)
	}
	for i := range card.Models {
		comparePersistence(&card.Models[i].Holdout, card.Models[0].Holdout.Metrics.MAE)
		comparePersistence(&card.Models[i].WalkForward, card.Models[0].WalkForward.Metrics.MAE)
	}
	encoded, err := json.Marshal(card)
	if err != nil {
		return card, err
	}
	parent.Status = "completed"
	metrics, _ := json.Marshal(map[string]any{"fingerprint": card.Fingerprint, "selected_by_tuning": card.SelectedByTuning, "models": len(card.Models), "test_observations": len(split.test)})
	parent.MetricsJSON = string(metrics)
	err = r.completePredictionExperiment(ctx, parent, encoded)
	return card, err
}

func fitArtifact(ctx context.Context, spec ModelSpec, train, cal []predictionSample, c PredictionConfig) (PredictorArtifact, error) {
	if len(train) < 40 || len(cal) < 10 {
		return PredictorArtifact{}, ErrInsufficientHistory
	}
	if train[len(train)-1].Target >= cal[0].AsOf {
		return PredictorArtifact{}, fmt.Errorf("prediction training labels overlap calibration origins")
	}
	m, err := fitPredictor(ctx, spec, train)
	if err != nil {
		return PredictorArtifact{}, err
	}
	q, err := intervalRadius(m, cal, c.Coverage)
	if err != nil {
		return PredictorArtifact{}, err
	}
	return PredictorArtifact{Model: m, HorizonBars: c.HorizonBars, NominalCoverage: c.Coverage, ResidualRadius: q, Training: partition(train), Calibration: partition(cal), TrainingHash: hashSamples(train), CalibrationHash: hashSamples(cal)}, nil
}
