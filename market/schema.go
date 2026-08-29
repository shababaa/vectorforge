package market

import (
	"errors"
	"fmt"
	"reflect"

	"byodb"
)

const (
	tableMigrations  = "market_schema_migrations"
	tableInstruments = "market_instruments"
	tableCandles     = "market_candles"
	tableFeatures    = "market_features"
	tableModelRuns   = "market_model_runs"
	tableForecasts   = "market_forecasts"
	tableAnalyses    = "market_analyses"
	tableCheckpoints = "market_ingestion_checkpoints"
	tableActions     = "market_corporate_actions"
	tableQuality     = "market_data_quality_runs"
	tableIngestRuns  = "market_ingestion_runs"
	tableFeatureWM   = "market_feature_watermarks"
)

type schemaMigration struct {
	version int64
	name    string
	tables  []*byodb.TableDef
}

func initialTableDefinitions() []*byodb.TableDef {
	return []*byodb.TableDef{
		{Name: tableMigrations, Cols: []string{"version", "name", "applied_at"}, Types: []uint32{byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64}, PKeys: 1, Indexes: [][]string{{"version"}}},
		{Name: tableInstruments, Cols: []string{"symbol", "name", "asset_type", "exchange", "currency", "active", "created_at", "updated_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64}, PKeys: 1, Indexes: [][]string{{"symbol"}, {"exchange", "symbol"}, {"asset_type", "symbol"}}},
		{Name: tableCandles, Cols: []string{"symbol", "interval", "timestamp", "open", "high", "low", "close", "adjusted_close", "volume", "source", "ingested_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64}, PKeys: 3, Indexes: [][]string{{"symbol", "interval", "timestamp"}, {"timestamp", "symbol", "interval"}}},
		{Name: tableFeatures, Cols: []string{"symbol", "interval", "timestamp", "feature_set", "return_1_ppm", "return_5_ppm", "sma_5", "sma_20", "volatility_20_ppm", "rsi_14_ppm", "volume_sma_20", "computed_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64}, PKeys: 4, Indexes: [][]string{{"symbol", "interval", "timestamp", "feature_set"}, {"feature_set", "timestamp", "symbol", "interval"}}},
		{Name: tableModelRuns, Cols: []string{"run_id", "model_name", "model_version", "feature_set", "target", "horizon_seconds", "training_start", "training_end", "created_at", "parameters_json", "metrics_json", "dataset_hash", "status"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES}, PKeys: 1, Indexes: [][]string{{"run_id"}, {"model_name", "created_at"}, {"status", "created_at"}}},
		{Name: tableForecasts, Cols: []string{"run_id", "symbol", "interval", "as_of_timestamp", "horizon_seconds", "target_timestamp", "baseline_close", "predicted_close", "lower_bound", "upper_bound", "confidence_ppm", "has_actual", "actual_close", "evaluated_at", "absolute_error", "absolute_percentage_ppm", "direction_correct", "created_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64}, PKeys: 5, Indexes: [][]string{{"run_id", "symbol", "interval", "as_of_timestamp", "horizon_seconds"}, {"symbol", "interval", "as_of_timestamp"}, {"target_timestamp", "symbol", "interval"}}},
		{Name: tableAnalyses, Cols: []string{"analysis_id", "symbol", "as_of_timestamp", "provider", "model", "prompt_version", "input_digest", "thesis", "sentiment_ppm", "confidence_ppm", "evidence_json", "forecast_run_id", "created_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64}, PKeys: 1, Indexes: [][]string{{"analysis_id"}, {"symbol", "as_of_timestamp"}, {"forecast_run_id", "created_at"}}},
		{Name: tableCheckpoints, Cols: []string{"source", "dataset", "symbol", "interval", "last_timestamp", "last_cursor", "rows_ingested", "updated_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64}, PKeys: 4, Indexes: [][]string{{"source", "dataset", "symbol", "interval"}, {"updated_at", "source", "dataset"}}},
	}
}

func phaseTwoTableDefinitions() []*byodb.TableDef {
	return []*byodb.TableDef{
		{Name: tableActions, Cols: []string{"symbol", "action_type", "ex_date", "source", "amount", "currency", "split_from", "split_to", "raw_digest", "ingested_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64}, PKeys: 4, Indexes: [][]string{{"symbol", "action_type", "ex_date", "source"}, {"ex_date", "symbol", "action_type"}}},
		{Name: tableQuality, Cols: []string{"report_id", "source", "symbol", "interval", "from_timestamp", "to_timestamp", "expected", "observed", "missing", "unexpected", "duplicates", "invalid", "outliers", "coverage_ppm", "missing_json", "unexpected_json", "outliers_json", "warnings_json", "dataset_hash", "created_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64}, PKeys: 1, Indexes: [][]string{{"report_id"}, {"symbol", "interval", "created_at"}, {"source", "created_at"}}},
		{Name: tableIngestRuns, Cols: []string{"run_id", "provider", "symbol", "interval", "from_timestamp", "to_timestamp", "status", "pages", "received", "inserted", "updated", "unchanged", "duplicates", "retries", "credits_used", "error", "started_at", "finished_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64}, PKeys: 1, Indexes: [][]string{{"run_id"}, {"provider", "status", "started_at"}, {"symbol", "interval", "started_at"}}},
		{Name: tableFeatureWM, Cols: []string{"symbol", "interval", "feature_set", "last_candle_timestamp", "last_feature_timestamp", "updated_at"}, Types: []uint32{byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_BYTES, byodb.TYPE_INT64, byodb.TYPE_INT64, byodb.TYPE_INT64}, PKeys: 3, Indexes: [][]string{{"symbol", "interval", "feature_set"}}},
	}
}

func migrations() []schemaMigration {
	return []schemaMigration{
		{version: 1, name: "initial market intelligence schema", tables: initialTableDefinitions()},
		{version: 2, name: "provider sync, corporate actions, quality, and incremental features", tables: phaseTwoTableDefinitions()},
	}
}

func tableDefinitions() []*byodb.TableDef {
	var out []*byodb.TableDef
	for _, migration := range migrations() {
		out = append(out, migration.tables...)
	}
	return out
}

func sameSchema(actual, expected *byodb.TableDef) bool {
	return actual.Name == expected.Name && actual.PKeys == expected.PKeys &&
		reflect.DeepEqual(actual.Cols, expected.Cols) && reflect.DeepEqual(actual.Types, expected.Types) &&
		reflect.DeepEqual(actual.Indexes, expected.Indexes)
}

// EnsureSchema applies the market schema as one atomic migration. Repeated
// calls are idempotent; an incompatible existing table fails safely.
func (r *Repository) EnsureSchema() error {
	return r.writeTransaction(func(tx *byodb.DBTX) error {
		for _, migration := range migrations() {
			for _, expected := range migration.tables {
				if actual, ok := tx.Table(expected.Name); ok {
					if !sameSchema(actual, expected) {
						return fmt.Errorf("market schema mismatch for table %q", expected.Name)
					}
					continue
				}
				if err := tx.TableNew(expected); err != nil {
					return err
				}
			}
			row := (&byodb.Record{}).AddInt64("version", migration.version)
			exists, err := tx.Get(tableMigrations, row)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			row = (&byodb.Record{}).AddInt64("version", migration.version).AddString("name", migration.name).AddInt64("applied_at", r.now().UTC().Unix())
			if _, err := tx.Set(tableMigrations, *row, byodb.MODE_INSERT_ONLY); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repository) CurrentSchemaVersion() (int64, error) {
	if _, ok := r.db.Table(tableMigrations); !ok {
		return 0, nil
	}
	var tx byodb.DBTX
	if err := r.db.Begin(&tx); err != nil {
		return 0, err
	}
	defer r.db.Abort(&tx)
	sc := &byodb.Scanner{}
	if err := tx.Scan(tableMigrations, sc); err != nil {
		return 0, err
	}
	var latest int64
	for sc.Valid() {
		var rec byodb.Record
		if err := sc.Deref(&rec); err != nil {
			return 0, err
		}
		if rec.Get("version").I64 > latest {
			latest = rec.Get("version").I64
		}
		sc.Next()
	}
	return latest, nil
}

func (r *Repository) writeTransaction(fn func(*byodb.DBTX) error) error {
	for attempt := 0; attempt < 4; attempt++ {
		var tx byodb.DBTX
		if err := r.db.Begin(&tx); err != nil {
			return err
		}
		if err := fn(&tx); err != nil {
			r.db.Abort(&tx)
			return err
		}
		if err := r.db.Commit(&tx); err != nil {
			if errors.Is(err, byodb.ErrConflict) {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("market transaction: %w", byodb.ErrConflict)
}
