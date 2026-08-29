package market

import (
	"fmt"
	"strings"

	"byodb"
)

type WriteReport struct {
	Received  int `json:"received"`
	Inserted  int `json:"inserted"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

func normalizeCorporateAction(v CorporateAction, now int64) (CorporateAction, error) {
	var err error
	if v.Symbol, err = normalizeSymbol(v.Symbol); err != nil {
		return CorporateAction{}, err
	}
	v.ActionType, v.Source, v.Currency = strings.ToLower(strings.TrimSpace(v.ActionType)), strings.TrimSpace(v.Source), strings.ToUpper(strings.TrimSpace(v.Currency))
	if v.ExDate <= 0 || v.Source == "" {
		return CorporateAction{}, fmt.Errorf("%w: corporate action date and source are required", ErrInvalidMarketData)
	}
	switch v.ActionType {
	case CorporateActionDividend:
		if v.Amount <= 0 || v.Currency == "" {
			return CorporateAction{}, fmt.Errorf("%w: dividend amount and currency are required", ErrInvalidMarketData)
		}
		v.SplitFrom, v.SplitTo = 0, 0
	case CorporateActionSplit:
		if v.SplitFrom <= 0 || v.SplitTo <= 0 {
			return CorporateAction{}, fmt.Errorf("%w: split factors must be positive", ErrInvalidMarketData)
		}
		v.Amount, v.Currency = 0, ""
	default:
		return CorporateAction{}, fmt.Errorf("%w: unsupported corporate action %q", ErrInvalidMarketData, v.ActionType)
	}
	if v.IngestedAt == 0 {
		v.IngestedAt = now
	}
	return v, nil
}

func (r *Repository) IngestCorporateActions(input []CorporateAction) (WriteReport, error) {
	report := WriteReport{Received: len(input)}
	normalized := make([]CorporateAction, len(input))
	for i, action := range input {
		var err error
		normalized[i], err = normalizeCorporateAction(action, r.now().UTC().Unix())
		if err != nil {
			return report, fmt.Errorf("corporate action %d: %w", i, err)
		}
	}
	err := r.writeTransaction(func(tx *byodb.DBTX) error {
		report.Inserted, report.Updated, report.Unchanged = 0, 0, 0
		for _, action := range normalized {
			key := (&byodb.Record{}).AddString("symbol", action.Symbol).AddString("action_type", action.ActionType).AddInt64("ex_date", action.ExDate).AddString("source", action.Source)
			exists, err := tx.Get(tableActions, key)
			if err != nil {
				return err
			}
			if exists && corporateActionsEquivalent(corporateActionFromRecord(*key), action) {
				report.Unchanged++
				continue
			}
			changed, err := tx.Set(tableActions, corporateActionRecord(action), byodb.MODE_UPSERT)
			if err != nil {
				return err
			}
			switch {
			case !changed:
				report.Unchanged++
			case exists:
				report.Updated++
			default:
				report.Inserted++
			}
		}
		return nil
	})
	return report, err
}

func corporateActionsEquivalent(a, b CorporateAction) bool {
	return a.Symbol == b.Symbol && a.ActionType == b.ActionType && a.ExDate == b.ExDate && a.Source == b.Source && a.Amount == b.Amount && a.Currency == b.Currency && a.SplitFrom == b.SplitFrom && a.SplitTo == b.SplitTo && a.RawDigest == b.RawDigest
}

func (r *Repository) CorporateActions(symbol string, from, to int64, limit int) ([]CorporateAction, error) {
	var err error
	if symbol, err = normalizeSymbol(symbol); err != nil {
		return nil, err
	}
	if from < 0 || (to != 0 && to < from) || limit <= 0 || limit > 10_000 {
		return nil, fmt.Errorf("%w: invalid corporate action range", ErrInvalidMarketData)
	}
	key := *(&byodb.Record{}).AddString("symbol", symbol)
	sc := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: key, Cmp2: byodb.CMP_LE, Key2: key.Clone(), Index: []string{"symbol", "action_type", "ex_date", "source"}}
	var tx byodb.DBTX
	if err := r.db.Begin(&tx); err != nil {
		return nil, err
	}
	defer r.db.Abort(&tx)
	if err := tx.Scan(tableActions, sc); err != nil {
		return nil, err
	}
	out := make([]CorporateAction, 0, min(limit, 64))
	for sc.Valid() && len(out) < limit {
		var rec byodb.Record
		if err := sc.Deref(&rec); err != nil {
			return nil, err
		}
		action := corporateActionFromRecord(rec)
		if action.ExDate >= from && (to == 0 || action.ExDate <= to) {
			out = append(out, action)
		}
		sc.Next()
	}
	return out, nil
}

func (r *Repository) Checkpoint(source, dataset, symbol, interval string) (IngestionCheckpoint, bool, error) {
	var err error
	source, dataset = strings.TrimSpace(source), strings.TrimSpace(dataset)
	if source == "" || dataset == "" {
		return IngestionCheckpoint{}, false, fmt.Errorf("%w: checkpoint source and dataset are required", ErrInvalidMarketData)
	}
	if symbol, err = normalizeSymbol(symbol); err != nil {
		return IngestionCheckpoint{}, false, err
	}
	if interval, err = validateInterval(interval); err != nil {
		return IngestionCheckpoint{}, false, err
	}
	rec := (&byodb.Record{}).AddString("source", source).AddString("dataset", dataset).AddString("symbol", symbol).AddString("interval", interval)
	ok, err := r.db.Get(tableCheckpoints, rec)
	if err != nil || !ok {
		return IngestionCheckpoint{}, ok, err
	}
	return checkpointFromRecord(*rec), true, nil
}

func checkpointFromRecord(r byodb.Record) IngestionCheckpoint {
	return IngestionCheckpoint{Source: r.Get("source").String(), Dataset: r.Get("dataset").String(), Symbol: r.Get("symbol").String(), Interval: r.Get("interval").String(), LastTimestamp: r.Get("last_timestamp").I64, LastCursor: r.Get("last_cursor").String(), RowsIngested: r.Get("rows_ingested").I64, UpdatedAt: r.Get("updated_at").I64}
}

func (r *Repository) FeatureWatermark(symbol, interval, featureSet string) (FeatureWatermark, bool, error) {
	var err error
	if symbol, err = normalizeSymbol(symbol); err != nil {
		return FeatureWatermark{}, false, err
	}
	if interval, err = validateInterval(interval); err != nil {
		return FeatureWatermark{}, false, err
	}
	if featureSet == "" {
		featureSet = DefaultFeatureSet
	}
	rec := (&byodb.Record{}).AddString("symbol", symbol).AddString("interval", interval).AddString("feature_set", featureSet)
	ok, err := r.db.Get(tableFeatureWM, rec)
	if err != nil || !ok {
		return FeatureWatermark{}, ok, err
	}
	return featureWatermarkFromRecord(*rec), true, nil
}

func (r *Repository) putFeatureWatermark(v FeatureWatermark) error {
	v.UpdatedAt = r.now().UTC().Unix()
	_, err := r.db.Upsert(tableFeatureWM, featureWatermarkRecord(v))
	return err
}

func (r *Repository) PutDataQualityReport(v DataQualityReport) error {
	if v.ReportID == "" {
		id, err := newID("quality")
		if err != nil {
			return err
		}
		v.ReportID = id
	}
	if v.CreatedAt == 0 {
		v.CreatedAt = r.now().UTC().Unix()
	}
	rec, err := qualityRecord(v)
	if err != nil {
		return err
	}
	_, err = r.db.Upsert(tableQuality, rec)
	return err
}

func (r *Repository) DataQualityReport(id string) (DataQualityReport, bool, error) {
	rec := (&byodb.Record{}).AddString("report_id", strings.TrimSpace(id))
	ok, err := r.db.Get(tableQuality, rec)
	if err != nil || !ok {
		return DataQualityReport{}, ok, err
	}
	v, err := qualityFromRecord(*rec)
	return v, err == nil, err
}

func (r *Repository) putIngestionRun(v IngestionRun) error {
	_, err := r.db.Upsert(tableIngestRuns, ingestionRunRecord(v))
	return err
}

func (r *Repository) IngestionRun(id string) (IngestionRun, bool, error) {
	rec := (&byodb.Record{}).AddString("run_id", strings.TrimSpace(id))
	ok, err := r.db.Get(tableIngestRuns, rec)
	if err != nil || !ok {
		return IngestionRun{}, ok, err
	}
	return ingestionRunFromRecord(*rec), true, nil
}
