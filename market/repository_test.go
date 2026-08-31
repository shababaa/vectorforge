package market

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"byodb"
)

func openTestRepository(t *testing.T) (*byodb.DB, *Repository) {
	t.Helper()
	db, err := byodb.OpenDB(filepath.Join(t.TempDir(), "market.db"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db, repository
}

func testCandles(count int) []Candle {
	start := time.Date(2026, 1, 2, 21, 0, 0, 0, time.UTC).Unix()
	out := make([]Candle, count)
	for i := range out {
		closePrice := int64(100+i) * PriceScale
		out[i] = Candle{Symbol: "aapl", Interval: "1d", Timestamp: start + int64(i)*86400, Open: closePrice - PriceScale/2, High: closePrice + PriceScale, Low: closePrice - PriceScale, Close: closePrice, AdjustedClose: closePrice, Volume: 1_000_000 + int64(i), Source: "test"}
	}
	return out
}

func TestSchemaIsIdempotentAndIntrospectable(t *testing.T) {
	db, repository := openTestRepository(t)
	if err := repository.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	version, err := repository.CurrentSchemaVersion()
	if err != nil || version != SchemaVersion {
		t.Fatalf("version=(%d,%v)", version, err)
	}
	if def, ok := db.Table(tableCandles); !ok || def.PKeys != 3 || len(def.Indexes) != 2 {
		t.Fatalf("unexpected candle schema: %+v", def)
	}
	stats, err := db.Stats()
	if err != nil || stats.TableCount != len(tableDefinitions())+2 || stats.PageSizeBytes != byodb.BTreePageSize {
		t.Fatalf("stats=(%+v,%v)", stats, err)
	}
}

func TestPhaseOneDatabaseMigratesToLatest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := byodb.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := NewRepository(db)
	if err := repository.writeTransaction(func(tx *byodb.DBTX) error {
		for _, def := range initialTableDefinitions() {
			if err := tx.TableNew(def); err != nil {
				return err
			}
		}
		row := (&byodb.Record{}).AddInt64("version", 1).AddString("name", "initial market intelligence schema").AddInt64("applied_at", 1)
		_, err := tx.Set(tableMigrations, *row, byodb.MODE_INSERT_ONLY)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := db.Table(tableActions); ok {
		t.Fatal("phase two table exists before migration")
	}
	if err := repository.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	version, err := repository.CurrentSchemaVersion()
	if err != nil || version != SchemaVersion {
		t.Fatalf("version=(%d,%v)", version, err)
	}
	if _, ok := db.Table(tableActions); !ok {
		t.Fatal("phase two table missing after migration")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIngestionIsAtomicIdempotentAndIndexed(t *testing.T) {
	_, repository := openTestRepository(t)
	candles := testCandles(30)
	// Reverse the provider payload to verify ingestion restores key locality.
	for left, right := 0, len(candles)-1; left < right; left, right = left+1, right-1 {
		candles[left], candles[right] = candles[right], candles[left]
	}
	report, err := repository.IngestCandles(candles)
	if err != nil || report.Inserted != 30 {
		t.Fatalf("ingest=(%+v,%v)", report, err)
	}
	report, err = repository.IngestCandles(candles)
	if err != nil || report.Unchanged != 30 {
		t.Fatalf("idempotent ingest=(%+v,%v)", report, err)
	}
	rows, err := repository.Candles(CandleQuery{Symbol: "AAPL", Interval: "1d", From: 0, To: 0, Limit: 5, Descending: true})
	if err != nil || len(rows) != 5 || rows[0].Timestamp <= rows[1].Timestamp {
		t.Fatalf("descending scan=(%+v,%v)", rows, err)
	}
	bad := append(testCandles(1), Candle{Symbol: "AAPL", Interval: "1d", Timestamp: 9, Open: 10, High: 5, Low: 1, Close: 8, Source: "test"})
	if _, err := repository.IngestCandles(bad); !errors.Is(err, ErrInvalidMarketData) {
		t.Fatalf("invalid batch error=%v", err)
	}
}

func TestFeaturesUseOnlyPointInTimeHistory(t *testing.T) {
	_, repository := openTestRepository(t)
	candles := testCandles(30)
	if _, err := repository.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	feature, err := repository.ComputeFeatureSnapshot("AAPL", "1d", candles[20].Timestamp, DefaultFeatureSet)
	if err != nil {
		t.Fatal(err)
	}
	if feature.Timestamp != candles[20].Timestamp || feature.SMA5 != 118*PriceScale || feature.RSI14PPM != RatioScale || feature.Return1PPM <= 0 {
		t.Fatalf("unexpected feature: %+v", feature)
	}
	stored, ok, err := repository.Feature("AAPL", "1d", feature.Timestamp, DefaultFeatureSet)
	if err != nil || !ok || stored.Timestamp != feature.Timestamp {
		t.Fatalf("stored feature=(%+v,%v,%v)", stored, ok, err)
	}
}

func TestIncrementalFeaturesResumeAndRecomputeCorrections(t *testing.T) {
	_, repository := openTestRepository(t)
	candles := testCandles(25)
	if _, err := repository.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	count, err := repository.ComputeFeaturesIncremental("AAPL", "1d", 0, 0, DefaultFeatureSet)
	if err != nil || count != 5 {
		t.Fatalf("initial features=(%d,%v)", count, err)
	}
	more := testCandles(27)[25:]
	if _, err := repository.IngestCandles(more); err != nil {
		t.Fatal(err)
	}
	count, err = repository.ComputeFeaturesIncremental("AAPL", "1d", 0, 0, DefaultFeatureSet)
	if err != nil || count != 2 {
		t.Fatalf("resumed features=(%d,%v)", count, err)
	}
	if count, err = repository.ComputeFeaturesIncremental("AAPL", "1d", 0, 0, DefaultFeatureSet); err != nil || count != 0 {
		t.Fatalf("idempotent features=(%d,%v)", count, err)
	}
	corrected := more[0]
	corrected.Close += PriceScale
	corrected.AdjustedClose = corrected.Close
	corrected.High = corrected.Close + PriceScale
	if _, err := repository.IngestCandles([]Candle{corrected}); err != nil {
		t.Fatal(err)
	}
	count, err = repository.ComputeFeaturesIncremental("AAPL", "1d", corrected.Timestamp, 0, DefaultFeatureSet)
	if err != nil || count != 2 {
		t.Fatalf("corrected features=(%d,%v)", count, err)
	}
	watermark, ok, err := repository.FeatureWatermark("AAPL", "1d", DefaultFeatureSet)
	if err != nil || !ok || watermark.LastFeatureTimestamp != more[1].Timestamp {
		t.Fatalf("watermark=(%+v,%v,%v)", watermark, ok, err)
	}
}

func TestForecastEvaluationAndWalkForwardBaseline(t *testing.T) {
	_, repository := openTestRepository(t)
	candles := testCandles(6)
	if _, err := repository.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	run := ModelRun{RunID: "run-test", ModelName: "test", ModelVersion: "1", HorizonSeconds: 86400, ParametersJSON: `{}`, MetricsJSON: `{}`}
	if err := repository.PutModelRun(run); err != nil {
		t.Fatal(err)
	}
	forecast := Forecast{RunID: run.RunID, Symbol: "AAPL", Interval: "1d", AsOfTimestamp: candles[0].Timestamp, HorizonSeconds: 86400, TargetTimestamp: candles[1].Timestamp, BaselineClose: candles[0].Close, PredictedClose: candles[0].Close + PriceScale/2, LowerBound: candles[0].Close - PriceScale, UpperBound: candles[0].Close + 2*PriceScale, ConfidencePPM: 600_000}
	if err := repository.PutForecast(forecast); err != nil {
		t.Fatal(err)
	}
	count, err := repository.EvaluateForecastsAt("AAPL", "1d", candles[1].Timestamp, candles[1].Close)
	if err != nil || count != 1 {
		t.Fatalf("evaluate=(%d,%v)", count, err)
	}
	report, err := repository.RunPersistenceBacktest("AAPL", "1d", 0, 0)
	if err != nil || report.Observations != 5 || report.MeanAbsoluteError != 1 || report.DatasetHash == "" {
		t.Fatalf("backtest=(%+v,%v)", report, err)
	}
}

func TestDatasetHashIgnoresOperationalIngestionTime(t *testing.T) {
	first := testCandles(3)
	second := append([]Candle(nil), first...)
	for i := range first {
		first[i].IngestedAt = 100
		second[i].IngestedAt = 999
	}
	if hashCandles(first) != hashCandles(second) {
		t.Fatal("dataset hash changed with ingestion metadata")
	}
}

type fakeLLM struct{ input []byte }

func (f *fakeLLM) Complete(_ context.Context, request LLMRequest) (LLMResponse, error) {
	f.input = append([]byte(nil), request.InputJSON...)
	return LLMResponse{Thesis: "Momentum is positive, with elevated realized volatility.", SentimentPPM: 250_000, ConfidencePPM: 700_000, Evidence: []string{"five-day return is positive"}}, nil
}

func TestAnalysisServicePersistsAuditablePointInTimeOutput(t *testing.T) {
	db, repository := openTestRepository(t)
	candles := testCandles(25)
	if _, err := repository.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	client := &fakeLLM{}
	service, err := NewAnalysisService(repository, client, "fake", "deterministic", "v1")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := service.Analyze(context.Background(), "AAPL", "1d", candles[20].Timestamp, "")
	if err != nil {
		t.Fatal(err)
	}
	if analysis.InputDigest == "" || analysis.AsOfTimestamp != candles[20].Timestamp {
		t.Fatalf("unexpected analysis: %+v", analysis)
	}
	stored := (&byodb.Record{}).AddString("analysis_id", analysis.AnalysisID)
	if ok, err := db.Get(tableAnalyses, stored); err != nil || !ok || stored.Get("input_digest").String() != analysis.InputDigest {
		t.Fatalf("stored analysis=(%v,%v,%+v)", ok, err, stored)
	}
	var input analysisContext
	if err := json.Unmarshal(client.input, &input); err != nil {
		t.Fatal(err)
	}
	for _, candle := range input.Candles {
		if candle.Timestamp > candles[20].Timestamp {
			t.Fatal("future candle leaked into LLM context")
		}
	}
}

func TestMarketDataSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reopen.db")
	db, err := byodb.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := NewRepository(db)
	if err := repository.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.IngestCandles(testCandles(3)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = byodb.OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ = NewRepository(db)
	if err := repository.EnsureSchema(); err != nil {
		t.Fatal(err)
	}
	rows, err := repository.Candles(CandleQuery{Symbol: "AAPL", Interval: "1d", From: 0, To: 0, Limit: 10})
	if err != nil || len(rows) != 3 {
		t.Fatalf("reopened rows=(%d,%v)", len(rows), err)
	}
}

func TestCSVImport(t *testing.T) {
	_, repository := openTestRepository(t)
	csvText := "timestamp,open,high,low,close,adjusted_close,volume\n2026-01-02,100,102,99,101,101,1000\n2026-01-03,101,103,100,102,102,1200\n"
	report, err := repository.ImportCSV(strings.NewReader(csvText), CSVImportOptions{Symbol: "AAPL", Interval: "1d", Source: "fixture", BatchSize: 1})
	if err != nil || report.Inserted != 2 {
		t.Fatalf("import=(%+v,%v)", report, err)
	}
}

func BenchmarkCandleIngestion(b *testing.B) {
	db, err := byodb.OpenDB(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	repository, _ := NewRepository(db)
	if err := repository.EnsureSchema(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		candles := testCandles(100)
		for j := range candles {
			candles[j].Timestamp += int64(i) * 100 * 86400
		}
		if _, err := repository.IngestCandles(candles); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCandleRangeScan(b *testing.B) {
	db, err := byodb.OpenDB(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	repository, _ := NewRepository(db)
	if err := repository.EnsureSchema(); err != nil {
		b.Fatal(err)
	}
	candles := testCandles(2_000)
	for start := 0; start < len(candles); start += 250 {
		if _, err := repository.IngestCandles(candles[start : start+250]); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := repository.Candles(CandleQuery{Symbol: "AAPL", Interval: "1d", From: 0, To: 0, Limit: 252, Descending: true})
		if err != nil || len(rows) != 252 {
			b.Fatal(fmt.Errorf("scan rows=%d err=%v", len(rows), err))
		}
	}
}
