package market

import (
	"encoding/json"

	"byodb"
)

func corporateActionRecord(v CorporateAction) byodb.Record {
	return *(&byodb.Record{}).AddString("symbol", v.Symbol).AddString("action_type", v.ActionType).AddInt64("ex_date", v.ExDate).AddString("source", v.Source).
		AddInt64("amount", v.Amount).AddString("currency", v.Currency).AddInt64("split_from", v.SplitFrom).AddInt64("split_to", v.SplitTo).
		AddString("raw_digest", v.RawDigest).AddInt64("ingested_at", v.IngestedAt)
}

func corporateActionFromRecord(r byodb.Record) CorporateAction {
	return CorporateAction{Symbol: r.Get("symbol").String(), ActionType: r.Get("action_type").String(), ExDate: r.Get("ex_date").I64, Source: r.Get("source").String(), Amount: r.Get("amount").I64, Currency: r.Get("currency").String(), SplitFrom: r.Get("split_from").I64, SplitTo: r.Get("split_to").I64, RawDigest: r.Get("raw_digest").String(), IngestedAt: r.Get("ingested_at").I64}
}

func featureWatermarkRecord(v FeatureWatermark) byodb.Record {
	return *(&byodb.Record{}).AddString("symbol", v.Symbol).AddString("interval", v.Interval).AddString("feature_set", v.FeatureSet).
		AddInt64("last_candle_timestamp", v.LastCandleTimestamp).AddInt64("last_feature_timestamp", v.LastFeatureTimestamp).AddInt64("updated_at", v.UpdatedAt)
}

func featureWatermarkFromRecord(r byodb.Record) FeatureWatermark {
	return FeatureWatermark{Symbol: r.Get("symbol").String(), Interval: r.Get("interval").String(), FeatureSet: r.Get("feature_set").String(), LastCandleTimestamp: r.Get("last_candle_timestamp").I64, LastFeatureTimestamp: r.Get("last_feature_timestamp").I64, UpdatedAt: r.Get("updated_at").I64}
}

func qualityRecord(v DataQualityReport) (byodb.Record, error) {
	missing, err := json.Marshal(v.MissingTimestamps)
	if err != nil {
		return byodb.Record{}, err
	}
	unexpected, err := json.Marshal(v.UnexpectedTimestamps)
	if err != nil {
		return byodb.Record{}, err
	}
	outliers, err := json.Marshal(v.OutlierTimestamps)
	if err != nil {
		return byodb.Record{}, err
	}
	warnings, err := json.Marshal(v.Warnings)
	if err != nil {
		return byodb.Record{}, err
	}
	return *(&byodb.Record{}).AddString("report_id", v.ReportID).AddString("source", v.Source).AddString("symbol", v.Symbol).AddString("interval", v.Interval).
		AddInt64("from_timestamp", v.From).AddInt64("to_timestamp", v.To).AddInt64("expected", int64(v.Expected)).AddInt64("observed", int64(v.Observed)).
		AddInt64("missing", int64(v.Missing)).AddInt64("unexpected", int64(v.Unexpected)).AddInt64("duplicates", int64(v.Duplicates)).AddInt64("invalid", int64(v.Invalid)).AddInt64("outliers", int64(v.Outliers)).
		AddInt64("coverage_ppm", v.CoveragePPM).AddStr("missing_json", missing).AddStr("unexpected_json", unexpected).AddStr("outliers_json", outliers).AddStr("warnings_json", warnings).
		AddString("dataset_hash", v.DatasetHash).AddInt64("created_at", v.CreatedAt), nil
}

func qualityFromRecord(r byodb.Record) (DataQualityReport, error) {
	v := DataQualityReport{ReportID: r.Get("report_id").String(), Source: r.Get("source").String(), Symbol: r.Get("symbol").String(), Interval: r.Get("interval").String(), From: r.Get("from_timestamp").I64, To: r.Get("to_timestamp").I64, Expected: int(r.Get("expected").I64), Observed: int(r.Get("observed").I64), Missing: int(r.Get("missing").I64), Unexpected: int(r.Get("unexpected").I64), Duplicates: int(r.Get("duplicates").I64), Invalid: int(r.Get("invalid").I64), Outliers: int(r.Get("outliers").I64), CoveragePPM: r.Get("coverage_ppm").I64, DatasetHash: r.Get("dataset_hash").String(), CreatedAt: r.Get("created_at").I64}
	if err := json.Unmarshal(r.Get("missing_json").Str, &v.MissingTimestamps); err != nil {
		return DataQualityReport{}, err
	}
	if err := json.Unmarshal(r.Get("unexpected_json").Str, &v.UnexpectedTimestamps); err != nil {
		return DataQualityReport{}, err
	}
	if err := json.Unmarshal(r.Get("outliers_json").Str, &v.OutlierTimestamps); err != nil {
		return DataQualityReport{}, err
	}
	if err := json.Unmarshal(r.Get("warnings_json").Str, &v.Warnings); err != nil {
		return DataQualityReport{}, err
	}
	return v, nil
}

func ingestionRunRecord(v IngestionRun) byodb.Record {
	return *(&byodb.Record{}).AddString("run_id", v.RunID).AddString("provider", v.Provider).AddString("symbol", v.Symbol).AddString("interval", v.Interval).
		AddInt64("from_timestamp", v.From).AddInt64("to_timestamp", v.To).AddString("status", v.Status).AddInt64("pages", v.Pages).
		AddInt64("received", v.Received).AddInt64("inserted", v.Inserted).AddInt64("updated", v.Updated).AddInt64("unchanged", v.Unchanged).
		AddInt64("duplicates", v.Duplicates).AddInt64("retries", v.Retries).AddInt64("credits_used", v.CreditsUsed).AddString("error", v.Error).
		AddInt64("started_at", v.StartedAt).AddInt64("finished_at", v.FinishedAt)
}

func ingestionRunFromRecord(r byodb.Record) IngestionRun {
	return IngestionRun{RunID: r.Get("run_id").String(), Provider: r.Get("provider").String(), Symbol: r.Get("symbol").String(), Interval: r.Get("interval").String(), From: r.Get("from_timestamp").I64, To: r.Get("to_timestamp").I64, Status: r.Get("status").String(), Pages: r.Get("pages").I64, Received: r.Get("received").I64, Inserted: r.Get("inserted").I64, Updated: r.Get("updated").I64, Unchanged: r.Get("unchanged").I64, Duplicates: r.Get("duplicates").I64, Retries: r.Get("retries").I64, CreditsUsed: r.Get("credits_used").I64, Error: r.Get("error").String(), StartedAt: r.Get("started_at").I64, FinishedAt: r.Get("finished_at").I64}
}
