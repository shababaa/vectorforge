package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"byodb"
	"byodb/market"
	"byodb/market/llm/ollama"
	"byodb/market/provider/twelvedata"
	"byodb/market/source/sec"
)

func main() {
	dbPath := flag.String("db", "market.db", "database file")
	command := flag.String("command", "init", "init, sync, import, features, features-incremental, quality, backtest, demo, experiment, experiment-demo, model-card, predict, filings, analyze, analysis-demo, analysis-report, analyst-eval, or analyst-eval-report")
	symbol := flag.String("symbol", "AAPL", "market symbol")
	symbols := flag.String("symbols", "", "comma-separated symbols for provider sync")
	interval := flag.String("interval", "1d", "candle interval")
	filePath := flag.String("file", "", "CSV file for import")
	source := flag.String("source", "csv", "data provider/source label")
	from := flag.Int64("from", 0, "inclusive Unix start timestamp")
	to := flag.Int64("to", 0, "inclusive Unix end timestamp; 0 means latest")
	asOf := flag.Int64("as-of", 0, "point-in-time feature timestamp; 0 means latest")
	startDate := flag.String("start-date", "", "sync start date YYYY-MM-DD; defaults to five years ago")
	endDate := flag.String("end-date", "", "sync end date YYYY-MM-DD; defaults to now")
	requestsPerMinute := flag.Int("requests-per-minute", 8, "provider request budget per minute")
	syncActions := flag.Bool("corporate-actions", true, "sync dividends and splits")
	syncFeatures := flag.Bool("compute-features", true, "incrementally compute features after sync")
	horizonBars := flag.Int("horizon-bars", 1, "prediction target in observed bars, not calendar days")
	trainFraction := flag.Float64("train-fraction", .6, "initial chronological training fraction")
	validationFraction := flag.Float64("validation-fraction", .2, "fraction divided into tuning and calibration")
	refitEvery := flag.Int("refit-every", 63, "test origins between walk-forward refits")
	trainWindow := flag.Int("train-window", 0, "rolling eligible training/calibration samples; zero means expanding")
	coverage := flag.Float64("coverage", .9, "nominal empirical prediction-interval coverage")
	runID := flag.String("run-id", "", "experiment ID for model-card; fitted model run ID for predict")
	outputFormat := flag.String("format", "json", "experiment/model-card output: json or markdown")
	cik := flag.String("cik", "", "ten-digit SEC company identifier for filings")
	llmProvider := flag.String("llm-provider", "ollama", "ollama or fixture (fixture is not an LLM)")
	llmModel := flag.String("model", os.Getenv("OLLAMA_MODEL"), "explicit installed local model tag for Ollama")
	ollamaURL := flag.String("ollama-url", "http://127.0.0.1:11434", "numeric loopback Ollama origin")
	analysisID := flag.String("analysis-id", "", "stored analysis ID for analysis-report")
	timeout := flag.Duration("timeout", 2*time.Minute, "total analyst/source job timeout")
	flag.Parse()
	if *outputFormat != "json" && *outputFormat != "markdown" {
		log.Fatal("-format must be json or markdown")
	}

	db, err := byodb.OpenDB(*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	repository, err := market.NewRepository(db)
	if err != nil {
		log.Fatal(err)
	}
	if err := repository.EnsureSchema(); err != nil {
		log.Fatal(err)
	}

	switch *command {
	case "init":
		version, err := repository.CurrentSchemaVersion()
		mustPrint(map[string]any{"database": *dbPath, "market_schema_version": version}, err)
	case "import":
		if *filePath == "" {
			log.Fatal("-file is required for import")
		}
		file, err := os.Open(*filePath)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		report, err := repository.ImportCSV(file, market.CSVImportOptions{Symbol: *symbol, Interval: *interval, Source: *source})
		mustPrint(report, err)
	case "sync":
		apiKey := strings.TrimSpace(os.Getenv("TWELVE_DATA_API_KEY"))
		if apiKey == "" {
			log.Fatal("TWELVE_DATA_API_KEY is required for provider sync")
		}
		start, end, err := syncRange(*startDate, *endDate, *from, *to)
		if err != nil {
			log.Fatal(err)
		}
		client, err := twelvedata.New(twelvedata.Config{APIKey: apiKey, RequestsPerMinute: *requestsPerMinute})
		if err != nil {
			log.Fatal(err)
		}
		service, err := market.NewSyncService(repository, client)
		if err != nil {
			log.Fatal(err)
		}
		selected := parseSymbols(*symbols, *symbol)
		requests := make([]market.SyncRequest, 0, len(selected))
		for _, value := range selected {
			requests = append(requests, market.SyncRequest{Symbol: value, Interval: *interval, Start: start, End: end, SyncCorporateActions: *syncActions, ComputeFeatures: *syncFeatures, Calendar: market.NewNYSECalendar()})
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		result := service.SyncUniverse(ctx, requests, true)
		mustPrint(result, nil)
		if len(result.Failures) > 0 {
			log.Fatalf("%d symbol sync(s) failed", len(result.Failures))
		}
	case "features":
		feature, err := repository.ComputeFeatureSnapshot(*symbol, *interval, *asOf, market.DefaultFeatureSet)
		mustPrint(feature, err)
	case "features-incremental":
		count, err := repository.ComputeFeaturesIncremental(*symbol, *interval, *from, *to, market.DefaultFeatureSet)
		mustPrint(map[string]any{"features_computed": count}, err)
	case "quality":
		if *from <= 0 {
			log.Fatal("-from is required for a quality report")
		}
		report, err := repository.AssessDataQuality(market.QualityOptions{Source: *source, Symbol: *symbol, Interval: *interval, From: *from, To: *to, Calendar: market.NewNYSECalendar()})
		mustPrint(report, err)
	case "backtest":
		report, err := repository.RunPersistenceBacktest(*symbol, *interval, *from, *to)
		mustPrint(report, err)
	case "demo":
		mustPrint(runDemo(repository, *symbol, *interval), nil)
	case "experiment", "experiment-demo":
		selectedSymbol, selectedInterval := *symbol, *interval
		if *command == "experiment-demo" {
			if _, err := repository.IngestCandles(market.PredictionDemoCandles()); err != nil {
				log.Fatal(err)
			}
			selectedSymbol, selectedInterval = "SYNTH", "1d"
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		config := market.PredictionConfig{HorizonBars: *horizonBars, TrainFraction: *trainFraction, ValidationFraction: *validationFraction, RefitEvery: *refitEvery, TrainWindow: *trainWindow, Coverage: *coverage}
		card, err := repository.RunPredictionExperiment(ctx, selectedSymbol, selectedInterval, *from, *to, config)
		printModelCard(card, *outputFormat, err)
	case "model-card":
		if *runID == "" {
			log.Fatal("-run-id is required for model-card")
		}
		card, err := repository.PredictionModelCard(*runID)
		printModelCard(card, *outputFormat, err)
	case "predict":
		if *runID == "" {
			log.Fatal("-run-id is required for predict")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		forecast, err := repository.PredictNextDaily(ctx, *runID, *asOf, market.NewNYSECalendar())
		mustPrint(forecast, err)
	case "filings", "analyze", "analysis-demo", "analysis-report", "analyst-eval", "analyst-eval-report":
		if *timeout <= 0 || *timeout > 10*time.Minute {
			log.Fatal("-timeout must be positive and at most 10m")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		if *command == "filings" {
			client, err := sec.New(sec.Config{UserAgent: os.Getenv("SEC_USER_AGENT")})
			if err != nil {
				log.Fatal(err)
			}
			rows, err := client.RecentFilings(ctx, *symbol, *cik)
			if err != nil {
				log.Fatal(err)
			}
			stored, err := repository.StoreFilingSources(rows)
			mustPrint(stored, err)
			break
		}
		if *command == "analysis-report" {
			report, err := repository.LoadAnalysisReport(*analysisID)
			printAnalysis(report, *outputFormat, err)
			break
		}
		if *command == "analyst-eval-report" {
			report, err := repository.LoadAnalystEvaluation(*runID)
			mustPrint(report, err)
			break
		}
		providerName, modelName := *llmProvider, *llmModel
		selectedSymbol, selectedInterval, selectedRun, selectedAsOf := *symbol, *interval, *runID, *asOf
		if *command == "analysis-demo" {
			providerName, modelName = "fixture", "deterministic-not-an-llm"
			if _, err := repository.IngestCandles(market.AnalystDemoCandles(time.Now().UTC())); err != nil {
				log.Fatal(err)
			}
			card, err := repository.RunPredictionExperiment(ctx, "SYNTH", "1d", 0, 0, market.DefaultPredictionConfig())
			if err != nil {
				log.Fatal(err)
			}
			for _, model := range card.Models {
				if model.Spec == card.SelectedByTuning {
					selectedRun = model.HoldoutRunID
				}
			}
			if _, err := repository.PredictNextDaily(ctx, selectedRun, 0, market.NewNYSECalendar()); err != nil {
				log.Fatal(err)
			}
			selectedSymbol, selectedInterval, selectedAsOf = "SYNTH", "1d", 0
		}
		var client market.LLMClient
		switch providerName {
		case "fixture":
			client = market.FixtureAnalyst{}
			modelName = "deterministic-not-an-llm"
		case "ollama":
			client, err = ollama.New(ollama.Config{BaseURL: *ollamaURL, Model: modelName})
			if err != nil {
				log.Fatal(err)
			}
		default:
			log.Fatal("-llm-provider must be ollama or fixture")
		}
		if *command == "analyst-eval" {
			report, err := repository.EvaluateAnalyst(ctx, client, providerName, modelName)
			mustPrint(report, err)
			if report.Errors > 0 {
				log.Fatal("analyst evaluation had completion errors; report was saved")
			}
			break
		}
		service, err := market.NewAnalysisService(repository, client, providerName, modelName, market.AnalystVersion)
		if err != nil {
			log.Fatal(err)
		}
		report, err := service.AnalyzeReport(ctx, selectedSymbol, selectedInterval, selectedAsOf, selectedRun)
		printAnalysis(report, *outputFormat, err)
		if report.Decision.AbstainReason == "provider_error" || report.Decision.AbstainReason == "validation_failed" {
			log.Fatal("analyst failed closed; inspect the saved analysis report")
		}
	default:
		log.Fatalf("unknown command %q", *command)
	}
}

func printAnalysis(report market.AnalysisReport, format string, err error) {
	if err != nil {
		log.Fatal(err)
	}
	if format == "markdown" {
		fmt.Print(market.AnalysisMarkdown(report))
		return
	}
	mustPrint(report, nil)
}

func printModelCard(card market.PredictionModelCard, format string, err error) {
	if err != nil {
		log.Fatal(err)
	}
	if format == "markdown" {
		fmt.Print(market.ModelCardMarkdown(card))
		return
	}
	mustPrint(card, nil)
}

func syncRange(startDate, endDate string, from, to int64) (int64, int64, error) {
	now := time.Now().UTC()
	start, end := from, to
	if startDate != "" {
		parsed, err := time.ParseInLocation("2006-01-02", startDate, time.UTC)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid -start-date: %w", err)
		}
		start = parsed.Unix()
	}
	if start <= 0 {
		start = time.Date(now.Year()-5, now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()
	}
	if endDate != "" {
		parsed, err := time.ParseInLocation("2006-01-02", endDate, time.UTC)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid -end-date: %w", err)
		}
		end = parsed.Add(24*time.Hour - time.Second).Unix()
	}
	if end <= 0 {
		end = now.Unix()
	}
	if end < start {
		return 0, 0, errors.New("sync end precedes start")
	}
	return start, end, nil
}

func parseSymbols(list, fallback string) []string {
	if strings.TrimSpace(list) == "" {
		list = fallback
	}
	seen := map[string]bool{}
	out := []string{}
	for _, value := range strings.Split(list, ",") {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func runDemo(repository *market.Repository, symbol, interval string) map[string]any {
	if err := repository.UpsertInstrument(market.Instrument{Symbol: symbol, Name: "Demo instrument", AssetType: "equity", Exchange: "DEMO", Currency: "USD", Active: true}); err != nil {
		log.Fatal(err)
	}
	start := time.Date(2026, 1, 2, 21, 0, 0, 0, time.UTC).Unix()
	candles := make([]market.Candle, 60)
	for i := range candles {
		closePrice := 100 + float64(i)*0.35 + math.Sin(float64(i)/4)*2
		openPrice := closePrice - math.Sin(float64(i))*0.5
		openScaled, _ := market.ScalePrice(openPrice)
		closeScaled, _ := market.ScalePrice(closePrice)
		highScaled, _ := market.ScalePrice(math.Max(openPrice, closePrice) + 1)
		lowScaled, _ := market.ScalePrice(math.Min(openPrice, closePrice) - 1)
		candles[i] = market.Candle{Symbol: symbol, Interval: interval, Timestamp: start + int64(i)*86400, Open: openScaled, High: highScaled, Low: lowScaled, Close: closeScaled, AdjustedClose: closeScaled, Volume: 1_000_000 + int64(i)*10_000, Source: "deterministic-demo"}
	}
	ingest, err := repository.IngestCandles(candles)
	if err != nil {
		log.Fatal(err)
	}
	feature, err := repository.ComputeFeatureSnapshot(symbol, interval, 0, market.DefaultFeatureSet)
	if err != nil {
		log.Fatal(err)
	}
	backtest, err := repository.RunPersistenceBacktest(symbol, interval, 0, 0)
	if err != nil {
		log.Fatal(err)
	}
	storage, err := repository.StorageStats()
	if err != nil {
		log.Fatal(err)
	}
	return map[string]any{"ingestion": ingest, "latest_features": feature, "baseline_backtest": backtest, "runtime_metrics": repository.Metrics(), "storage_stats": storage}
}

func mustPrint(value any, err error) {
	if err != nil {
		log.Fatal(err)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}
