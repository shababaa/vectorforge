package market

import "byodb"

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func instrumentRecord(v Instrument) byodb.Record {
	return *(&byodb.Record{}).AddString("symbol", v.Symbol).AddString("name", v.Name).AddString("asset_type", v.AssetType).
		AddString("exchange", v.Exchange).AddString("currency", v.Currency).AddInt64("active", boolInt(v.Active)).
		AddInt64("created_at", v.CreatedAt).AddInt64("updated_at", v.UpdatedAt)
}

func instrumentFromRecord(r byodb.Record) Instrument {
	return Instrument{Symbol: r.Get("symbol").String(), Name: r.Get("name").String(), AssetType: r.Get("asset_type").String(), Exchange: r.Get("exchange").String(), Currency: r.Get("currency").String(), Active: r.Get("active").I64 != 0, CreatedAt: r.Get("created_at").I64, UpdatedAt: r.Get("updated_at").I64}
}

func candleRecord(v Candle) byodb.Record {
	return *(&byodb.Record{}).AddString("symbol", v.Symbol).AddString("interval", v.Interval).AddInt64("timestamp", v.Timestamp).
		AddInt64("open", v.Open).AddInt64("high", v.High).AddInt64("low", v.Low).AddInt64("close", v.Close).
		AddInt64("adjusted_close", v.AdjustedClose).AddInt64("volume", v.Volume).AddString("source", v.Source).AddInt64("ingested_at", v.IngestedAt)
}

func candleFromRecord(r byodb.Record) Candle {
	return Candle{Symbol: r.Get("symbol").String(), Interval: r.Get("interval").String(), Timestamp: r.Get("timestamp").I64, Open: r.Get("open").I64, High: r.Get("high").I64, Low: r.Get("low").I64, Close: r.Get("close").I64, AdjustedClose: r.Get("adjusted_close").I64, Volume: r.Get("volume").I64, Source: r.Get("source").String(), IngestedAt: r.Get("ingested_at").I64}
}

func featureRecord(v FeatureSnapshot) byodb.Record {
	return *(&byodb.Record{}).AddString("symbol", v.Symbol).AddString("interval", v.Interval).AddInt64("timestamp", v.Timestamp).AddString("feature_set", v.FeatureSet).
		AddInt64("return_1_ppm", v.Return1PPM).AddInt64("return_5_ppm", v.Return5PPM).AddInt64("sma_5", v.SMA5).AddInt64("sma_20", v.SMA20).
		AddInt64("volatility_20_ppm", v.Volatility20PPM).AddInt64("rsi_14_ppm", v.RSI14PPM).AddInt64("volume_sma_20", v.VolumeSMA20).AddInt64("computed_at", v.ComputedAt)
}

func modelRunRecord(v ModelRun) byodb.Record {
	return *(&byodb.Record{}).AddString("run_id", v.RunID).AddString("model_name", v.ModelName).AddString("model_version", v.ModelVersion).AddString("feature_set", v.FeatureSet).
		AddString("target", v.Target).AddInt64("horizon_seconds", v.HorizonSeconds).AddInt64("training_start", v.TrainingStart).AddInt64("training_end", v.TrainingEnd).
		AddInt64("created_at", v.CreatedAt).AddString("parameters_json", v.ParametersJSON).AddString("metrics_json", v.MetricsJSON).AddString("dataset_hash", v.DatasetHash).AddString("status", v.Status)
}

func forecastRecord(v Forecast) byodb.Record {
	return *(&byodb.Record{}).AddString("run_id", v.RunID).AddString("symbol", v.Symbol).AddString("interval", v.Interval).AddInt64("as_of_timestamp", v.AsOfTimestamp).AddInt64("horizon_seconds", v.HorizonSeconds).
		AddInt64("target_timestamp", v.TargetTimestamp).AddInt64("baseline_close", v.BaselineClose).AddInt64("predicted_close", v.PredictedClose).AddInt64("lower_bound", v.LowerBound).
		AddInt64("upper_bound", v.UpperBound).AddInt64("confidence_ppm", v.ConfidencePPM).AddInt64("has_actual", boolInt(v.HasActual)).AddInt64("actual_close", v.ActualClose).
		AddInt64("evaluated_at", v.EvaluatedAt).AddInt64("absolute_error", v.AbsoluteError).AddInt64("absolute_percentage_ppm", v.AbsolutePercentagePPM).
		AddInt64("direction_correct", boolInt(v.DirectionCorrect)).AddInt64("created_at", v.CreatedAt)
}

func forecastFromRecord(r byodb.Record) Forecast {
	return Forecast{RunID: r.Get("run_id").String(), Symbol: r.Get("symbol").String(), Interval: r.Get("interval").String(), AsOfTimestamp: r.Get("as_of_timestamp").I64, HorizonSeconds: r.Get("horizon_seconds").I64, TargetTimestamp: r.Get("target_timestamp").I64, BaselineClose: r.Get("baseline_close").I64, PredictedClose: r.Get("predicted_close").I64, LowerBound: r.Get("lower_bound").I64, UpperBound: r.Get("upper_bound").I64, ConfidencePPM: r.Get("confidence_ppm").I64, HasActual: r.Get("has_actual").I64 != 0, ActualClose: r.Get("actual_close").I64, EvaluatedAt: r.Get("evaluated_at").I64, AbsoluteError: r.Get("absolute_error").I64, AbsolutePercentagePPM: r.Get("absolute_percentage_ppm").I64, DirectionCorrect: r.Get("direction_correct").I64 != 0, CreatedAt: r.Get("created_at").I64}
}

func analysisRecord(v Analysis) byodb.Record {
	return *(&byodb.Record{}).AddString("analysis_id", v.AnalysisID).AddString("symbol", v.Symbol).AddInt64("as_of_timestamp", v.AsOfTimestamp).AddString("provider", v.Provider).
		AddString("model", v.Model).AddString("prompt_version", v.PromptVersion).AddString("input_digest", v.InputDigest).AddString("thesis", v.Thesis).
		AddInt64("sentiment_ppm", v.SentimentPPM).AddInt64("confidence_ppm", v.ConfidencePPM).AddString("evidence_json", v.EvidenceJSON).AddString("forecast_run_id", v.ForecastRunID).AddInt64("created_at", v.CreatedAt)
}

func checkpointRecord(v IngestionCheckpoint) byodb.Record {
	return *(&byodb.Record{}).AddString("source", v.Source).AddString("dataset", v.Dataset).AddString("symbol", v.Symbol).AddString("interval", v.Interval).
		AddInt64("last_timestamp", v.LastTimestamp).AddString("last_cursor", v.LastCursor).AddInt64("rows_ingested", v.RowsIngested).AddInt64("updated_at", v.UpdatedAt)
}
