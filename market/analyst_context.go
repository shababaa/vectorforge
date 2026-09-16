package market

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"byodb"
)

// BuildAnalystContext reads candles, forecasts, model lineage and SEC metadata
// from ONE snapshot. No network calls or long LLM calls hold a DB transaction.
// v1 intentionally supports completed daily bars only.
func (r *Repository) BuildAnalystContext(symbol, interval string, asOf int64, runID string) (AnalystContext, error) {
	var out AnalystContext
	var err error
	if symbol, err = normalizeSymbol(symbol); err != nil {
		return out, err
	}
	if interval != "1d" {
		return out, errors.New("grounded analyst v1 requires daily (1d) bars")
	}
	if asOf == 0 {
		asOf = r.now().UTC().Unix()
	}
	if asOf <= 86400 || asOf > r.now().UTC().Unix() {
		return out, errors.New("analysis decision time must be in the observed past or present")
	}
	runID = strings.TrimSpace(runID)
	if len(runID) > 120 {
		return out, errors.New("forecast run ID too long")
	}
	out = AnalystContext{Version: AnalystVersion, Symbol: symbol, Interval: interval, DecisionAt: asOf, SupportedOutlook: "abstain", Warnings: []string{}, Candles: []Candle{}, Forecasts: []AnalystForecast{}, Sources: []FilingSource{}, Evidence: []AnalystEvidence{}}
	var tx byodb.DBTX
	if err := r.db.Begin(&tx); err != nil {
		return out, err
	}
	defer r.db.Abort(&tx)
	low := *(&byodb.Record{}).AddString("symbol", symbol).AddString("interval", interval).AddInt64("timestamp", 1)
	high := *(&byodb.Record{}).AddString("symbol", symbol).AddString("interval", interval).AddInt64("timestamp", asOf-86400)
	sc := &byodb.Scanner{Cmp1: byodb.CMP_LE, Key1: high, Cmp2: byodb.CMP_GE, Key2: low, Index: []string{"symbol", "interval", "timestamp"}}
	if err := tx.Scan(tableCandles, sc); err != nil {
		return out, err
	}
	for sc.Valid() && len(out.Candles) < 21 {
		var row byodb.Record
		if err := sc.Deref(&row); err != nil {
			return out, err
		}
		out.Candles = append(out.Candles, candleFromRecord(row))
		sc.Next()
	}
	for i, j := 0, len(out.Candles)-1; i < j; i, j = i+1, j-1 {
		out.Candles[i], out.Candles[j] = out.Candles[j], out.Candles[i]
	}
	if len(out.Candles) > 0 {
		out.BarTimestamp = out.Candles[len(out.Candles)-1].Timestamp
	}
	out.Sources, err = filingsAt(&tx, symbol, asOf)
	if err != nil {
		return out, err
	}
	if len(out.Candles) < 21 {
		out.Warnings = append(out.Warnings, "need_21_completed_bars")
		return out, nil
	}
	if asOf-out.BarTimestamp > 7*86400 {
		out.Warnings = append(out.Warnings, "stale_market_data")
		return out, nil
	}
	for _, c := range out.Candles {
		if c.Source != out.Candles[0].Source {
			out.Warnings = append(out.Warnings, "mixed_candle_sources")
			return out, nil
		}
	}
	f := featureFromWindow(out.Candles, DefaultFeatureSet, r.now().UTC().Unix())
	out.Feature = &f
	featureID := fmt.Sprintf("feature:%s:%s:%d:%s", symbol, interval, f.Timestamp, f.FeatureSet)
	add := func(id, kind, record, text string, direction int, required bool) {
		out.Evidence = append(out.Evidence, AnalystEvidence{ID: id, Kind: kind, RecordID: record, Text: text, Direction: direction, Required: required})
	}
	add("return_1", "feature", featureID, fmt.Sprintf("The one-bar adjusted-close return is %.4f%%.", float64(f.Return1PPM)/10000), sign64(f.Return1PPM), true)
	add("return_5", "feature", featureID, fmt.Sprintf("The five-bar adjusted-close return is %.4f%%.", float64(f.Return5PPM)/10000), sign64(f.Return5PPM), true)
	last := out.Candles[len(out.Candles)-1]
	add("trend_20", "feature", featureID, fmt.Sprintf("Adjusted close is %.6f; its 20-bar average is %.6f.", UnscalePrice(last.AdjustedClose), UnscalePrice(f.SMA20)), sign64(last.AdjustedClose-f.SMA20), true)
	add("volatility_20", "feature", featureID, fmt.Sprintf("The sample standard deviation of the last 20 log returns is %.4f%% per bar (not annualized).", float64(f.Volatility20PPM)/10000), 0, false)
	if runID != "" {
		key := (&byodb.Record{}).AddString("run_id", runID)
		ok, err := tx.Get(tableModelRuns, key)
		if err != nil {
			return out, err
		}
		if !ok || key.Get("created_at").I64 > asOf || key.Get("training_end").I64 >= last.Timestamp || key.Get("status").String() != "completed" {
			out.Warnings = append(out.Warnings, "unavailable_forecast_lineage")
			return out, nil
		}
		prefix := *(&byodb.Record{}).AddString("run_id", runID).AddString("symbol", symbol).AddString("interval", interval).AddInt64("as_of_timestamp", last.Timestamp)
		fs := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: prefix, Cmp2: byodb.CMP_LE, Key2: prefix.Clone(), Index: []string{"run_id", "symbol", "interval", "as_of_timestamp", "horizon_seconds"}}
		if err := tx.Scan(tableForecasts, fs); err != nil {
			return out, err
		}
		for count := 0; fs.Valid(); count++ {
			if count >= 100 {
				return out, errors.New("too many forecast horizons")
			}
			var row byodb.Record
			if err := fs.Deref(&row); err != nil {
				return out, err
			}
			v := forecastFromRecord(row)
			if v.CreatedAt <= asOf && v.TargetTimestamp > last.Timestamp && v.TargetTimestamp > asOf-86400 {
				if len(out.Forecasts) == 2 {
					return out, errors.New("analyst supports at most two active forecast horizons")
				}
				id := fmt.Sprintf("forecast:%s:%s:%s:%d:%d", runID, symbol, interval, v.AsOfTimestamp, v.HorizonSeconds)
				dto := AnalystForecast{RecordID: id, RunID: runID, AsOf: v.AsOfTimestamp, Target: v.TargetTimestamp, Baseline: v.BaselineClose, Predicted: v.PredictedClose, Lower: v.LowerBound, Upper: v.UpperBound, CoveragePPM: v.ConfidencePPM, CreatedAt: v.CreatedAt}
				out.Forecasts = append(out.Forecasts, dto)
				add(fmt.Sprintf("forecast_%d", len(out.Forecasts)), "forecast", id, fmt.Sprintf("The stored model predicts %.6f for session %s versus baseline %.6f; interval [%.6f, %.6f] has nominal coverage %.2f%%, not a profit probability.", UnscalePrice(v.PredictedClose), time.Unix(v.TargetTimestamp, 0).UTC().Format("2006-01-02"), UnscalePrice(v.BaselineClose), UnscalePrice(v.LowerBound), UnscalePrice(v.UpperBound), float64(v.ConfidencePPM)/10000), sign64(v.PredictedClose-v.BaselineClose), true)
			}
			fs.Next()
		}
		if len(out.Forecasts) == 0 {
			out.Warnings = append(out.Warnings, "no_available_active_forecast")
			return out, nil
		}
	}
	if len(out.Sources) == 0 {
		out.Warnings = append(out.Warnings, "no_available_SEC_filings")
	}
	for _, v := range out.Sources {
		id := "filing_" + v.Digest[:16]
		add(id, "filing", v.SourceID, fmt.Sprintf("SEC submissions metadata lists form %s, accession %s, accepted at %s. Filing contents were not retrieved.", v.Form, v.Accession, time.Unix(v.PublishedAt, 0).UTC().Format(time.RFC3339)), 0, false)
		out.Evidence[len(out.Evidence)-1].URL = v.URL
	}
	positive, negative := false, false
	for _, e := range out.Evidence {
		if e.Required {
			positive = positive || e.Direction > 0
			negative = negative || e.Direction < 0
		}
	}
	out.SupportedOutlook = "flat_signals"
	if positive && negative {
		out.SupportedOutlook = "mixed_signals"
	} else if positive {
		out.SupportedOutlook = "positive_signals"
	} else if negative {
		out.SupportedOutlook = "negative_signals"
	}
	out.Ready = true
	return out, nil
}

func sign64(n int64) int {
	if n > 0 {
		return 1
	}
	if n < 0 {
		return -1
	}
	return 0
}
