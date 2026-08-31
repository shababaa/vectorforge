package market

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"byodb"
)

const artifactChunkBytes = 1800
const maxArtifactBytes = 8 << 20

func writeModelArtifact(tx *byodb.DBTX, runID, kind string, payload []byte) error {
	if len(payload) == 0 || len(payload) > maxArtifactBytes {
		return errors.New("model artifact must contain 1 byte to 8 MiB")
	}
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	chunks := (len(payload) + artifactChunkBytes - 1) / artifactChunkBytes
	for i := 0; i < chunks; i++ {
		row := (&byodb.Record{}).AddString("run_id", runID).AddString("kind", kind).AddInt64("chunk", int64(i)).AddStr("payload", payload[i*artifactChunkBytes:min((i+1)*artifactChunkBytes, len(payload))]).AddString("digest", digest).AddInt64("chunks", int64(chunks))
		changed, err := tx.Set(tableArtifacts, *row, byodb.MODE_INSERT_ONLY)
		if err != nil {
			return err
		}
		if !changed {
			return errors.New("immutable model artifact already exists")
		}
	}
	return nil
}

func (r *Repository) readModelArtifact(runID, kind string) ([]byte, error) {
	var tx byodb.DBTX
	if err := r.db.Begin(&tx); err != nil {
		return nil, err
	}
	defer r.db.Abort(&tx)
	key := *(&byodb.Record{}).AddString("run_id", runID).AddString("kind", kind)
	sc := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: key, Cmp2: byodb.CMP_LE, Key2: key.Clone(), Index: []string{"run_id", "kind", "chunk"}}
	if err := tx.Scan(tableArtifacts, sc); err != nil {
		return nil, err
	}
	var payload []byte
	var digest string
	expected := int64(0)
	count := int64(0)
	for sc.Valid() {
		var row byodb.Record
		if err := sc.Deref(&row); err != nil {
			return nil, err
		}
		if count == 0 {
			expected = row.Get("chunks").I64
			digest = row.Get("digest").String()
		}
		if row.Get("chunk").I64 != count || row.Get("chunks").I64 != expected || row.Get("digest").String() != digest || expected <= 0 || expected > maxArtifactBytes/artifactChunkBytes+1 {
			return nil, errors.New("invalid model artifact chunk sequence")
		}
		payload = append(payload, row.Get("payload").Str...)
		if len(payload) > maxArtifactBytes {
			return nil, errors.New("model artifact exceeds size limit")
		}
		count++
		sc.Next()
	}
	if count == 0 {
		return nil, errors.New("model artifact not found")
	}
	if count != expected {
		return nil, errors.New("incomplete model artifact")
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("model artifact checksum mismatch")
	}
	return payload, nil
}

func (r *Repository) persistPredictionRun(ctx context.Context, card PredictionModelCard, id, mode string, fold int, artifact PredictorArtifact, rows []scoredPrediction, metrics PredictionMetrics) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(rows) == 0 || artifact.Calibration.LastTarget >= rows[0].forecast.AsOfTimestamp {
		return errors.New("calibration labels overlap evaluation origins")
	}
	artifact.Symbol, artifact.Interval, artifact.Source = card.Symbol, card.Interval, card.Source
	modelBytes, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	params, _ := json.Marshal(map[string]any{"experiment_id": card.ExperimentID, "spec": artifact.Model.Spec, "mode": mode, "fold": fold, "horizon_bars": card.Config.HorizonBars})
	metricBytes, _ := json.Marshal(metrics)
	period, _ := intervalSeconds(card.Interval)
	run := ModelRun{RunID: id, ModelName: artifact.Model.Spec.Name, ModelVersion: PredictionVersion, FeatureSet: DefaultFeatureSet, Target: "future_log_return", HorizonSeconds: period * int64(card.Config.HorizonBars), TrainingStart: artifact.Training.FirstOrigin, TrainingEnd: artifact.Training.LastTarget, CreatedAt: r.now().UTC().Unix(), ParametersJSON: string(params), MetricsJSON: string(metricBytes), DatasetHash: artifact.TrainingHash, Status: "completed"}
	return r.writeTransaction(func(tx *byodb.DBTX) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := tx.Set(tableModelRuns, modelRunRecord(run), byodb.MODE_INSERT_ONLY); err != nil {
			return err
		}
		if err := writeModelArtifact(tx, id, "predictor", modelBytes); err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			f := row.forecast
			f.Symbol, f.Interval = card.Symbol, card.Interval
			if _, err := tx.Set(tableForecasts, forecastRecord(f), byodb.MODE_INSERT_ONLY); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) completePredictionExperiment(ctx context.Context, run ModelRun, card []byte) error {
	return r.writeTransaction(func(tx *byodb.DBTX) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := writeModelArtifact(tx, run.RunID, "model-card", card); err != nil {
			return err
		}
		// Preserve the parent run's original creation timestamp.
		key := (&byodb.Record{}).AddString("run_id", run.RunID)
		ok, err := tx.Get(tableModelRuns, key)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("experiment run missing")
		}
		run.CreatedAt = key.Get("created_at").I64
		_, err = tx.Set(tableModelRuns, modelRunRecord(run), byodb.MODE_UPDATE_ONLY)
		return err
	})
}

func (r *Repository) PredictionModelCard(id string) (PredictionModelCard, error) {
	var card PredictionModelCard
	b, err := r.readModelArtifact(id, "model-card")
	if err != nil {
		return card, err
	}
	err = json.Unmarshal(b, &card)
	if err == nil && (card.Version != PredictionVersion || card.ExperimentID != id) {
		err = errors.New("model card identity mismatch")
	}
	return card, err
}

func (r *Repository) LoadPredictor(runID string) (PredictorArtifact, error) {
	var a PredictorArtifact
	b, err := r.readModelArtifact(runID, "predictor")
	if err != nil {
		return a, err
	}
	if err = json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	if a.Model.Version != PredictionVersion || a.HorizonBars < 1 || a.HorizonBars > 20 || a.ResidualRadius < 0 || !finite(a.ResidualRadius) || !finite(a.NominalCoverage) || a.NominalCoverage < .5 || a.NominalCoverage >= 1 {
		return a, errors.New("invalid predictor artifact metadata")
	}
	return a, nil
}

// PredictNextDaily replays a saved model after a completed daily bar. It uses
// the supplied exchange calendar for the future target and inserts immutably.
// Callers must ensure asOf refers to a completed bar, not an in-progress one.
func (r *Repository) PredictNextDaily(ctx context.Context, runID string, asOf int64, calendar TradingCalendar) (Forecast, error) {
	if err := ctx.Err(); err != nil {
		return Forecast{}, err
	}
	a, err := r.LoadPredictor(runID)
	if err != nil {
		return Forecast{}, err
	}
	if a.Interval != "1d" {
		return Forecast{}, errors.New("live model replay currently requires daily bars")
	}
	if asOf <= 0 {
		asOf = r.now().UTC().Unix()
	}
	if calendar == nil {
		calendar = NewNYSECalendar()
	}
	candles, err := r.Candles(CandleQuery{Symbol: a.Symbol, Interval: a.Interval, From: 0, To: asOf, Limit: 21, Descending: true})
	if err != nil {
		return Forecast{}, err
	}
	if len(candles) != 21 {
		return Forecast{}, ErrInsufficientHistory
	}
	for i, j := 0, len(candles)-1; i < j; i, j = i+1, j-1 {
		candles[i], candles[j] = candles[j], candles[i]
	}
	for _, c := range candles {
		if c.Source != a.Source {
			return Forecast{}, errors.New("prediction history source differs from training source")
		}
	}
	last := candles[20]
	if a.Calibration.LastTarget > last.Timestamp {
		return Forecast{}, errors.New("cannot use a model calibrated on future labels")
	}
	if !calendar.IsSession(time.Unix(last.Timestamp, 0)) {
		return Forecast{}, errors.New("forecast origin is not an exchange session")
	}
	// Build the same features without a target or label.
	f := featureFromWindow(candles, DefaultFeatureSet, 0)
	x := predictionInputs(last, f)
	p, err := a.Model.PredictLogReturn(x, last.AdjustedClose, f.SMA5, f.SMA20)
	if err != nil {
		return Forecast{}, err
	}
	price, err := scaledLogPrice(last.AdjustedClose, p)
	if err != nil {
		return Forecast{}, err
	}
	low, err := scaledLogPrice(last.AdjustedClose, p-a.ResidualRadius)
	if err != nil {
		return Forecast{}, err
	}
	high, err := scaledLogPrice(last.AdjustedClose, p+a.ResidualRadius)
	if err != nil {
		return Forecast{}, err
	}
	target := time.Unix(last.Timestamp, 0).UTC()
	remaining := a.HorizonBars
	for days := 0; remaining > 0 && days < 366; days++ {
		target = target.AddDate(0, 0, 1)
		if calendar.IsSession(target) {
			remaining--
		}
	}
	if remaining > 0 {
		return Forecast{}, errors.New("calendar supplied no future sessions within one year")
	}
	out := Forecast{RunID: runID, Symbol: a.Symbol, Interval: a.Interval, AsOfTimestamp: last.Timestamp, TargetTimestamp: target.Unix(), HorizonSeconds: target.Unix() - last.Timestamp, BaselineClose: last.AdjustedClose, PredictedClose: price, LowerBound: low, UpperBound: high, ConfidencePPM: int64(math.Round(a.NominalCoverage * float64(RatioScale))), CreatedAt: r.now().UTC().Unix()}
	if err = ctx.Err(); err != nil {
		return Forecast{}, err
	}
	changed, err := r.db.Insert(tableForecasts, forecastRecord(out))
	if err != nil {
		return Forecast{}, err
	}
	if !changed {
		return Forecast{}, fmt.Errorf("forecast already exists for this model and origin; original prediction was preserved")
	}
	return out, nil
}
