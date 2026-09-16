package market

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"byodb"
)

func TestPredictionSplitsPurgeEveryLabelBoundary(t *testing.T) {
	c := DefaultPredictionConfig()
	c.HorizonBars = 3
	rows, err := predictionSamples(PredictionDemoCandles(), c.HorizonBars)
	if err != nil {
		t.Fatal(err)
	}
	s, err := splitPredictionSamples(rows, c)
	if err != nil {
		t.Fatal(err)
	}
	if s.report.Purged != 9 {
		t.Fatalf("purged=%d want 9", s.report.Purged)
	}
	for _, pair := range [][2][]predictionSample{{s.train, s.tune}, {s.tune, s.calibration}, {s.calibration, s.test}} {
		if pair[0][len(pair[0])-1].Target >= pair[1][0].AsOf {
			t.Fatal("future label crossed a partition")
		}
	}
	bad := c
	bad.Coverage = math.NaN()
	if _, err := splitPredictionSamples(rows, bad); err == nil {
		t.Fatal("accepted NaN")
	}
	bad = c
	bad.RefitEvery = 1
	if _, err := splitPredictionSamples(rows, bad); err == nil {
		t.Fatal("accepted unbounded fold count")
	}
	bad = c
	bad.TrainWindow = 10
	if _, err := splitPredictionSamples(rows, bad); err == nil {
		t.Fatal("accepted insufficient rolling window")
	}
}

func TestPredictionFeaturesCannotSeeFutureCandles(t *testing.T) {
	a := PredictionDemoCandles()[:100]
	b := append([]Candle(nil), a...)
	b[80].Close *= 2
	b[80].AdjustedClose = b[80].Close
	b[80].High = b[80].Close
	ra, _ := predictionSamples(a, 1)
	rb, _ := predictionSamples(b, 1)
	for i := 0; i < 60; i++ {
		if !reflect.DeepEqual(ra[i].X, rb[i].X) {
			t.Fatalf("future candle leaked into origin %d", i+20)
		}
	}
	if ra[59].Y == rb[59].Y {
		t.Fatal("fixture did not change the future label")
	}
	b[1].Timestamp = b[0].Timestamp
	if _, err := predictionSamples(b, 1); err == nil {
		t.Fatal("accepted duplicate timestamp")
	}
	b = append([]Candle(nil), a...)
	b[1].Source = "other"
	if _, err := predictionSamples(b, 1); err == nil {
		t.Fatal("accepted mixed sources")
	}
}

func TestRidgeAndBoostedStumpsLearnKnownSignals(t *testing.T) {
	rows := make([]predictionSample, 100)
	for i := range rows {
		x := float64(i)/100 - .5
		rows[i] = predictionSample{X: []float64{x, 0, 0, 0, 0, 0, 0}, Y: .03 + .1*x}
	}
	m, err := fitPredictor(context.Background(), ModelSpec{Name: "ridge", Alpha: 1e-8}, rows)
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.PredictLogReturn([]float64{.2, 0, 0, 0, 0, 0, 0}, 100, 100, 100)
	if err != nil || math.Abs(p-.05) > 1e-6 {
		t.Fatalf("ridge=%v %v", p, err)
	}
	for i := range rows {
		rows[i].Y = -.05
		if rows[i].X[0] >= 0 {
			rows[i].Y = .05
		}
	}
	m, err = fitPredictor(context.Background(), ModelSpec{Name: "gradient-boosted-stumps", Rounds: 64, LearningRate: .1}, rows)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []float64{-.2, .2} {
		p, err = m.PredictLogReturn([]float64{x, 0, 0, 0, 0, 0, 0}, 100, 100, 100)
		want := .05
		if x < 0 {
			want = -.05
		}
		if err != nil || math.Abs(p-want) > .005 {
			t.Fatalf("boosting x=%v got %v want %v err %v", x, p, want, err)
		}
	}
	b, _ := json.Marshal(m)
	var replay FittedPredictor
	if err := json.Unmarshal(b, &replay); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, replay) {
		t.Fatal("model serialization changed state")
	}
	if _, err = m.PredictLogReturn([]float64{1}, 100, 100, 100); err == nil {
		t.Fatal("accepted wrong feature dimensions")
	}
}

func TestPredictionMetricsAndIntervals(t *testing.T) {
	rows := []scoredPrediction{{forecast: Forecast{AbsoluteError: 2 * PriceScale, ActualClose: 10 * PriceScale, LowerBound: 9 * PriceScale, UpperBound: 11 * PriceScale, DirectionCorrect: true}}, {forecast: Forecast{AbsoluteError: 4 * PriceScale, ActualClose: 20 * PriceScale, LowerBound: 10 * PriceScale, UpperBound: 12 * PriceScale}}}
	m := predictionMetrics(rows, .9)
	if m.MAE != 3 || math.Abs(m.RMSE-math.Sqrt(10)) > 1e-10 || math.Abs(m.MAPE-.2) > 1e-10 || m.IntervalCoverage != .5 || m.DirectionAccuracy != .5 || m.MeanIntervalWidth != 2 {
		t.Fatalf("metrics=%+v", m)
	}
	if _, err := scaledLogPrice(math.MaxInt64, 1); err == nil {
		t.Fatal("accepted price overflow")
	}
}

func TestPredictionExperimentPersistsAndReplaysAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "experiment.db")
	db, err := byodb.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := NewRepository(db)
	if err = r.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	candles := PredictionDemoCandles()[:300]
	if _, err = r.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	config := DefaultPredictionConfig()
	config.RefitEvery = 25
	card, err := r.RunPredictionExperiment(context.Background(), "SYNTH", "1d", 0, 0, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(card.Models) != 4 || len(card.Candidates) != 8 {
		t.Fatalf("card=%+v", card)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = byodb.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, _ = NewRepository(db)
	stored, err := r.PredictionModelCard(card.ExperimentID)
	if err != nil || !reflect.DeepEqual(stored, card) {
		t.Fatalf("reopened card error=%v", err)
	}
	for _, result := range card.Models {
		a, err := r.LoadPredictor(result.HoldoutRunID)
		if err != nil {
			t.Fatal(err)
		}
		if a.Training.LastTarget >= a.Calibration.FirstOrigin || a.Calibration.LastTarget >= card.Split.Test.FirstOrigin {
			t.Fatal("artifact contains future labels")
		}
		if result.Holdout.Metrics.Observations != card.Split.Test.Count || result.WalkForward.Metrics.Observations != card.Split.Test.Count {
			t.Fatal("test rows were skipped")
		}
		rows, err := r.ForecastsAt(result.HoldoutRunID, "SYNTH", "1d", card.Split.Test.FirstOrigin, 10)
		if err != nil || len(rows) != 1 || !rows[0].HasActual {
			t.Fatalf("stored forecasts=%v %v", rows, err)
		}
		for fold, runID := range result.WalkForwardRunIDs {
			fa, err := r.LoadPredictor(runID)
			if err != nil {
				t.Fatal(err)
			}
			samples, _ := predictionSamples(candles, 1)
			split, _ := splitPredictionSamples(samples, config)
			origin := split.test[fold*config.RefitEvery].AsOf
			if fa.Calibration.LastTarget >= origin {
				t.Fatal("walk-forward used a future label")
			}
		}
	}
	f, err := r.PredictNextDaily(context.Background(), card.Models[0].HoldoutRunID, 0, NewNYSECalendar())
	if err != nil {
		t.Fatal(err)
	}
	if f.HasActual || f.PredictedClose != candles[len(candles)-1].AdjustedClose || f.TargetTimestamp <= f.AsOfTimestamp || !NewNYSECalendar().IsSession(time.Unix(f.TargetTimestamp, 0)) {
		t.Fatalf("forecast=%+v", f)
	}
	if _, err = r.PredictNextDaily(context.Background(), f.RunID, 0, nil); err == nil {
		t.Fatal("overwrote an existing forecast")
	}
	if _, err = r.PredictNextDaily(context.Background(), f.RunID, card.Split.Train.LastOrigin, nil); err == nil {
		t.Fatal("backdated model with future calibration")
	}
	markdown := ModelCardMarkdown(card)
	if !strings.Contains(markdown, "Frozen holdout") || !strings.Contains(markdown, "synthetic-phase3-v1") {
		t.Fatal("incomplete model card")
	}
}

func TestTestLabelsDoNotSelectOrFitFrozenModels(t *testing.T) {
	_, r := openTestRepository(t)
	candles := PredictionDemoCandles()[:250]
	if _, err := r.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	c := DefaultPredictionConfig()
	a, err := r.RunPredictionExperiment(context.Background(), "SYNTH", "1d", 0, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := r.RunPredictionExperiment(context.Background(), "SYNTH", "1d", 0, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint != repeated.Fingerprint || !reflect.DeepEqual(a.Candidates, repeated.Candidates) {
		t.Fatal("experiment is not deterministic")
	}
	for i := range a.Models {
		if !reflect.DeepEqual(a.Models[i].Holdout, repeated.Models[i].Holdout) || !reflect.DeepEqual(a.Models[i].WalkForward, repeated.Models[i].WalkForward) {
			t.Fatal("metrics changed on replay")
		}
	}
	last := &candles[len(candles)-1]
	last.Close += 3 * PriceScale
	last.AdjustedClose = last.Close
	last.High = max(last.High, last.Close)
	if _, err := r.IngestCandles(candles[len(candles)-1:]); err != nil {
		t.Fatal(err)
	}
	b, err := r.RunPredictionExperiment(context.Background(), "SYNTH", "1d", 0, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint == b.Fingerprint || !reflect.DeepEqual(a.Candidates, b.Candidates) || a.SelectedByTuning != b.SelectedByTuning {
		t.Fatal("test data influenced selection or was omitted from hash")
	}
	for i := range a.Models {
		ma, _ := r.LoadPredictor(a.Models[i].HoldoutRunID)
		mb, _ := r.LoadPredictor(b.Models[i].HoldoutRunID)
		if !reflect.DeepEqual(ma, mb) {
			t.Fatal("test label affected a frozen artifact")
		}
	}
	if a.Models[0].Holdout.Metrics.MAE == b.Models[0].Holdout.Metrics.MAE {
		t.Fatal("fixture did not affect test score")
	}
}

func TestModelArtifactsChunkChecksumsAndCancellation(t *testing.T) {
	db, r := openTestRepository(t)
	payload := []byte(strings.Repeat("abc", 2000))
	if err := r.writeTransaction(func(tx *byodb.DBTX) error { return writeModelArtifact(tx, "test", "payload", payload) }); err != nil {
		t.Fatal(err)
	}
	b, err := r.readModelArtifact("test", "payload")
	if err != nil || string(b) != string(payload) {
		t.Fatalf("roundtrip=%v", err)
	}
	key := (&byodb.Record{}).AddString("run_id", "test").AddString("kind", "payload").AddInt64("chunk", 0)
	if ok, err := db.Get(tableArtifacts, key); err != nil || !ok {
		t.Fatal(err)
	}
	key.Get("payload").Str[0] = 'x'
	if _, err := db.Upsert(tableArtifacts, *key); err != nil {
		t.Fatal(err)
	}
	if _, err := r.readModelArtifact("test", "payload"); err == nil {
		t.Fatal("corrupted payload passed checksum")
	}
	if err := r.writeTransaction(func(tx *byodb.DBTX) error { return writeModelArtifact(tx, "test", "partial", payload) }); err != nil {
		t.Fatal(err)
	}
	missing := (&byodb.Record{}).AddString("run_id", "test").AddString("kind", "partial").AddInt64("chunk", 1)
	if ok, err := db.Delete(tableArtifacts, *missing); err != nil || !ok {
		t.Fatalf("delete chunk: deleted=%v err=%v", ok, err)
	}
	if _, err := r.readModelArtifact("test", "partial"); err == nil {
		t.Fatal("incomplete artifact passed validation")
	}
	if _, err := r.IngestCandles(PredictionDemoCandles()[:200]); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.RunPredictionExperiment(ctx, "SYNTH", "1d", 0, 0, DefaultPredictionConfig())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestPhaseTwoDatabaseMigratesPredictionArtifacts(t *testing.T) {
	db, err := byodb.OpenDB(filepath.Join(t.TempDir(), "v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, _ := NewRepository(db)
	if err = r.writeTransaction(func(tx *byodb.DBTX) error {
		for _, def := range append(initialTableDefinitions(), phaseTwoTableDefinitions()...) {
			if err := tx.TableNew(def); err != nil {
				return err
			}
		}
		for version := int64(1); version <= 2; version++ {
			row := (&byodb.Record{}).AddInt64("version", version).AddString("name", "existing").AddInt64("applied_at", 1)
			if _, err := tx.Set(tableMigrations, *row, byodb.MODE_INSERT_ONLY); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.IngestCandles(testCandles(3)); err != nil {
		t.Fatal(err)
	}
	if err = r.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	v, _ := r.CurrentSchemaVersion()
	if v != SchemaVersion {
		t.Fatalf("version=%d", v)
	}
	rows, err := r.Candles(CandleQuery{Symbol: "AAPL", Interval: "1d", Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatal("migration lost candles")
	}
}

func TestRollingWindowMultiBarPrediction(t *testing.T) {
	_, r := openTestRepository(t)
	candles := PredictionDemoCandles()[:300]
	if _, err := r.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	c := DefaultPredictionConfig()
	c.HorizonBars = 3
	c.TrainWindow = 120
	c.RefitEvery = 20
	card, err := r.RunPredictionExperiment(context.Background(), "SYNTH", "1d", 0, 0, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range card.Models {
		for _, id := range m.WalkForwardRunIDs {
			a, err := r.LoadPredictor(id)
			if err != nil {
				t.Fatal(err)
			}
			if a.Training.Count+a.Calibration.Count > c.TrainWindow {
				t.Fatal("rolling training window exceeded")
			}
		}
	}
	f, err := r.PredictNextDaily(context.Background(), card.Models[0].HoldoutRunID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewNYSECalendar().Sessions(time.Unix(f.AsOfTimestamp, 0).AddDate(0, 0, 1), time.Unix(f.TargetTimestamp, 0))
	if len(sessions) != 3 {
		t.Fatalf("future sessions=%d", len(sessions))
	}
}

func TestPutForecastPreservesPredictionAndRealization(t *testing.T) {
	_, r := openTestRepository(t)
	if err := r.PutModelRun(ModelRun{RunID: "immutable", ModelName: "fixture", ModelVersion: "1", HorizonSeconds: 86400}); err != nil {
		t.Fatal(err)
	}
	f := Forecast{RunID: "immutable", Symbol: "AAPL", Interval: "1d", AsOfTimestamp: 100, TargetTimestamp: 86500, HorizonSeconds: 86400, BaselineClose: 100, PredictedClose: 100, LowerBound: 90, UpperBound: 110}
	if err := r.PutForecast(f); err != nil {
		t.Fatal(err)
	}
	if _, err := r.EvaluateForecastsAt("AAPL", "1d", f.TargetTimestamp, 105); err != nil {
		t.Fatal(err)
	}
	if err := r.PutForecast(f); err != nil {
		t.Fatal(err)
	}
	stored, err := r.ForecastsAt(f.RunID, "AAPL", "1d", f.AsOfTimestamp, 10)
	if err != nil || len(stored) != 1 || !stored[0].HasActual || stored[0].ActualClose != 105 {
		t.Fatal("retry lost realized outcome")
	}
	f.PredictedClose = 101
	if err := r.PutForecast(f); err == nil {
		t.Fatal("prediction was overwritten")
	}
}

func TestCancelledExperimentRecordsFailedParent(t *testing.T) {
	db, r := openTestRepository(t)
	candles := PredictionDemoCandles()[:200]
	if _, err := r.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	r.now = func() time.Time {
		calls++
		if calls == 2 {
			cancel()
		}
		return time.Unix(1800000000, 0)
	}
	card, err := r.RunPredictionExperiment(ctx, "SYNTH", "1d", 0, candles[len(candles)-1].Timestamp, DefaultPredictionConfig())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	key := (&byodb.Record{}).AddString("run_id", card.ExperimentID)
	ok, getErr := db.Get(tableModelRuns, key)
	if getErr != nil || !ok || key.Get("status").String() != "failed" {
		t.Fatalf("failed parent: %v %v %+v", ok, getErr, key)
	}
}

func BenchmarkPredictionFit(b *testing.B) {
	rows, _ := predictionSamples(PredictionDemoCandles(), 1)
	split, _ := splitPredictionSamples(rows, DefaultPredictionConfig())
	for _, spec := range []ModelSpec{{Name: "ridge", Alpha: .1}, {Name: "gradient-boosted-stumps", Rounds: 64, LearningRate: .05}} {
		b.Run(spec.Name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := fitPredictor(context.Background(), spec, split.train); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
