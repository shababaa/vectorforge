package market

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"byodb"
)

func newID(prefix string) (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

func (r *Repository) PutModelRun(run ModelRun) error {
	run.RunID, run.ModelName, run.ModelVersion = strings.TrimSpace(run.RunID), strings.TrimSpace(run.ModelName), strings.TrimSpace(run.ModelVersion)
	if run.RunID == "" || run.ModelName == "" || run.ModelVersion == "" || run.HorizonSeconds <= 0 {
		return fmt.Errorf("%w: model run id, name, version, and positive horizon are required", ErrInvalidMarketData)
	}
	if run.FeatureSet == "" {
		run.FeatureSet = DefaultFeatureSet
	}
	if run.Target == "" {
		run.Target = "adjusted_close"
	}
	if run.Status == "" {
		run.Status = "created"
	}
	if run.CreatedAt == 0 {
		key := (&byodb.Record{}).AddString("run_id", run.RunID)
		if exists, err := r.db.Get(tableModelRuns, key); err != nil {
			return err
		} else if exists {
			run.CreatedAt = key.Get("created_at").I64
		} else {
			run.CreatedAt = r.now().UTC().Unix()
		}
	}
	if !validJSONObject(run.ParametersJSON) || !validJSONObject(run.MetricsJSON) {
		return fmt.Errorf("%w: model parameters and metrics must be JSON objects", ErrInvalidMarketData)
	}
	_, err := r.db.Upsert(tableModelRuns, modelRunRecord(run))
	return err
}

func validJSONObject(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	var object map[string]any
	return json.Unmarshal([]byte(value), &object) == nil && object != nil
}

func (r *Repository) PutForecast(forecast Forecast) error {
	var err error
	if forecast.Symbol, err = normalizeSymbol(forecast.Symbol); err != nil {
		return err
	}
	if forecast.Interval, err = validateInterval(forecast.Interval); err != nil {
		return err
	}
	if strings.TrimSpace(forecast.RunID) == "" || forecast.AsOfTimestamp <= 0 || forecast.HorizonSeconds <= 0 || forecast.TargetTimestamp <= forecast.AsOfTimestamp {
		return fmt.Errorf("%w: forecast identity and timestamps are invalid", ErrInvalidMarketData)
	}
	if forecast.BaselineClose <= 0 || forecast.PredictedClose <= 0 || forecast.LowerBound <= 0 || forecast.UpperBound < forecast.LowerBound || forecast.ConfidencePPM < 0 || forecast.ConfidencePPM > RatioScale {
		return fmt.Errorf("%w: forecast prices or confidence are invalid", ErrInvalidMarketData)
	}
	runKey := (&byodb.Record{}).AddString("run_id", strings.TrimSpace(forecast.RunID))
	if exists, err := r.db.Get(tableModelRuns, runKey); err != nil {
		return err
	} else if !exists {
		return fmt.Errorf("%w: model run %q does not exist", ErrInvalidMarketData, forecast.RunID)
	}
	if forecast.CreatedAt == 0 {
		forecast.CreatedAt = r.now().UTC().Unix()
	}
	_, err = r.db.Upsert(tableForecasts, forecastRecord(forecast))
	return err
}

// ForecastsAt returns all horizons emitted by one model run for a point in
// time. The primary key itself covers this prefix scan.
func (r *Repository) ForecastsAt(runID, symbol, interval string, asOf int64, limit int) ([]Forecast, error) {
	var err error
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("%w: run id is required", ErrInvalidMarketData)
	}
	if symbol, err = normalizeSymbol(symbol); err != nil {
		return nil, err
	}
	if interval, err = validateInterval(interval); err != nil {
		return nil, err
	}
	if asOf <= 0 || limit <= 0 || limit > 100 {
		return nil, fmt.Errorf("%w: as-of timestamp and limit are invalid", ErrInvalidMarketData)
	}
	key := *(&byodb.Record{}).AddString("run_id", runID).AddString("symbol", symbol).AddString("interval", interval).AddInt64("as_of_timestamp", asOf)
	sc := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: key, Cmp2: byodb.CMP_LE, Key2: key.Clone(), Index: []string{"run_id", "symbol", "interval", "as_of_timestamp", "horizon_seconds"}}
	var tx byodb.DBTX
	if err := r.db.Begin(&tx); err != nil {
		return nil, err
	}
	defer r.db.Abort(&tx)
	if err := tx.Scan(tableForecasts, sc); err != nil {
		return nil, err
	}
	out := make([]Forecast, 0, limit)
	for sc.Valid() && len(out) < limit {
		var rec byodb.Record
		if err := sc.Deref(&rec); err != nil {
			return nil, err
		}
		out = append(out, forecastFromRecord(rec))
		sc.Next()
	}
	return out, nil
}

// EvaluateForecastsAt attaches a realized price to every forecast targeting the
// same symbol, interval, and timestamp. Model output is never overwritten.
func (r *Repository) EvaluateForecastsAt(symbol, interval string, targetTimestamp, actualClose int64) (int, error) {
	var err error
	if symbol, err = normalizeSymbol(symbol); err != nil {
		return 0, err
	}
	if interval, err = validateInterval(interval); err != nil {
		return 0, err
	}
	if targetTimestamp <= 0 || actualClose <= 0 {
		return 0, fmt.Errorf("%w: evaluation timestamp and actual close must be positive", ErrInvalidMarketData)
	}
	var evaluated int
	err = r.writeTransaction(func(tx *byodb.DBTX) error {
		key := *(&byodb.Record{}).AddInt64("target_timestamp", targetTimestamp).AddString("symbol", symbol).AddString("interval", interval)
		sc := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: key, Cmp2: byodb.CMP_LE, Key2: key.Clone(), Index: []string{"target_timestamp", "symbol", "interval"}}
		if err := tx.Scan(tableForecasts, sc); err != nil {
			return err
		}
		pending := []Forecast{}
		for sc.Valid() {
			var rec byodb.Record
			if err := sc.Deref(&rec); err != nil {
				return err
			}
			forecast := forecastFromRecord(rec)
			if !forecast.HasActual {
				pending = append(pending, forecast)
			}
			sc.Next()
		}
		evaluated = 0
		for _, forecast := range pending {
			forecast.HasActual, forecast.ActualClose, forecast.EvaluatedAt = true, actualClose, r.now().UTC().Unix()
			forecast.AbsoluteError = abs64(actualClose - forecast.PredictedClose)
			forecast.AbsolutePercentagePPM = int64(math.Round(float64(forecast.AbsoluteError) / float64(actualClose) * float64(RatioScale)))
			forecast.DirectionCorrect = direction(forecast.PredictedClose-forecast.BaselineClose) == direction(actualClose-forecast.BaselineClose)
			if _, err := tx.Set(tableForecasts, forecastRecord(forecast), byodb.MODE_UPDATE_ONLY); err != nil {
				return err
			}
			evaluated++
		}
		return nil
	})
	if err == nil {
		r.metrics.forecastsEvaluated.Add(uint64(evaluated))
	}
	return evaluated, err
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
func direction(value int64) int {
	if value < 0 {
		return -1
	}
	if value > 0 {
		return 1
	}
	return 0
}

type BacktestReport struct {
	RunID                string  `json:"run_id"`
	Observations         int     `json:"observations"`
	MeanAbsoluteError    float64 `json:"mean_absolute_error"`
	MeanAbsolutePctError float64 `json:"mean_absolute_percentage_error"`
	DirectionAccuracy    float64 `json:"direction_accuracy"`
	DatasetHash          string  `json:"dataset_hash"`
}

// RunPersistenceBacktest establishes the baseline every later model must beat.
// It performs walk-forward evaluation: observation N is predicted using only
// observation N-1, never future data.
func (r *Repository) RunPersistenceBacktest(symbol, interval string, from, to int64) (BacktestReport, error) {
	candles, err := r.Candles(CandleQuery{Symbol: symbol, Interval: interval, From: from, To: to, Limit: 100_000})
	if err != nil {
		return BacktestReport{}, err
	}
	if len(candles) < 2 {
		return BacktestReport{}, fmt.Errorf("%w: backtest needs at least 2 candles", ErrInsufficientHistory)
	}
	runID, err := newID("run")
	if err != nil {
		return BacktestReport{}, err
	}
	hash := hashCandles(candles)
	horizon := candles[1].Timestamp - candles[0].Timestamp
	run := ModelRun{RunID: runID, ModelName: "persistence-baseline", ModelVersion: "1.0.0", FeatureSet: DefaultFeatureSet, Target: "adjusted_close", HorizonSeconds: horizon, TrainingStart: candles[0].Timestamp, TrainingEnd: candles[len(candles)-2].Timestamp, ParametersJSON: `{"strategy":"previous_adjusted_close"}`, DatasetHash: hash, Status: "running"}
	if err := r.PutModelRun(run); err != nil {
		return BacktestReport{}, err
	}
	var absError, ape float64
	var correct int
	err = r.writeTransaction(func(tx *byodb.DBTX) error {
		for i := 1; i < len(candles); i++ {
			previous, actual := candles[i-1], candles[i]
			forecast := Forecast{RunID: runID, Symbol: actual.Symbol, Interval: actual.Interval, AsOfTimestamp: previous.Timestamp, HorizonSeconds: actual.Timestamp - previous.Timestamp, TargetTimestamp: actual.Timestamp, BaselineClose: previous.AdjustedClose, PredictedClose: previous.AdjustedClose, LowerBound: previous.AdjustedClose, UpperBound: previous.AdjustedClose, ConfidencePPM: RatioScale / 2, HasActual: true, ActualClose: actual.AdjustedClose, EvaluatedAt: r.now().UTC().Unix(), CreatedAt: r.now().UTC().Unix()}
			forecast.AbsoluteError = abs64(actual.AdjustedClose - forecast.PredictedClose)
			forecast.AbsolutePercentagePPM = int64(math.Round(float64(forecast.AbsoluteError) / float64(actual.AdjustedClose) * float64(RatioScale)))
			forecast.DirectionCorrect = direction(forecast.PredictedClose-forecast.BaselineClose) == direction(actual.AdjustedClose-forecast.BaselineClose)
			if forecast.DirectionCorrect {
				correct++
			}
			absError += float64(forecast.AbsoluteError)
			ape += float64(forecast.AbsolutePercentagePPM) / float64(RatioScale)
			if _, err := tx.Set(tableForecasts, forecastRecord(forecast), byodb.MODE_INSERT_ONLY); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return BacktestReport{}, err
	}
	count := len(candles) - 1
	report := BacktestReport{RunID: runID, Observations: count, MeanAbsoluteError: UnscalePrice(int64(math.Round(absError / float64(count)))), MeanAbsolutePctError: ape / float64(count), DirectionAccuracy: float64(correct) / float64(count), DatasetHash: hash}
	metrics, _ := json.Marshal(report)
	run.MetricsJSON, run.Status = string(metrics), "completed"
	if err := r.PutModelRun(run); err != nil {
		return BacktestReport{}, err
	}
	r.metrics.forecastsEvaluated.Add(uint64(count))
	return report, nil
}

func hashCandles(candles []Candle) string {
	h := sha256.New()
	encoder := json.NewEncoder(h)
	encoder.SetEscapeHTML(false)
	// Operational metadata such as IngestedAt is excluded: the same provider
	// dataset must produce the same lineage hash when re-imported later.
	type datasetCandle struct {
		Symbol, Interval, Source          string
		Timestamp, Open, High, Low, Close int64
		AdjustedClose, Volume             int64
	}
	for _, candle := range candles {
		_ = encoder.Encode(datasetCandle{Symbol: candle.Symbol, Interval: candle.Interval, Source: candle.Source, Timestamp: candle.Timestamp, Open: candle.Open, High: candle.High, Low: candle.Low, Close: candle.Close, AdjustedClose: candle.AdjustedClose, Volume: candle.Volume})
	}
	return hex.EncodeToString(h.Sum(nil))
}
