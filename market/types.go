// Package market adds a financial time-series, feature, forecast, and analysis
// layer to byodb. Monetary values are stored as fixed-point integers so disk
// representation and model inputs remain deterministic.
package market

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// PriceScale stores one unit of currency as one million integer units.
	PriceScale int64 = 1_000_000
	// RatioScale stores ratios such as returns, confidence, and RSI in ppm.
	RatioScale        int64 = 1_000_000
	SchemaVersion           = int64(4)
	DefaultFeatureSet       = "technical-v1"
)

var (
	ErrInsufficientHistory = errors.New("insufficient candle history")
	ErrInvalidMarketData   = errors.New("invalid market data")
)

type Instrument struct {
	Symbol    string `json:"symbol"`
	Name      string `json:"name"`
	AssetType string `json:"asset_type"`
	Exchange  string `json:"exchange"`
	Currency  string `json:"currency"`
	Active    bool   `json:"active"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type Candle struct {
	Symbol        string `json:"symbol"`
	Interval      string `json:"interval"`
	Timestamp     int64  `json:"timestamp"` // Unix seconds, always interpreted as UTC.
	Open          int64  `json:"open"`      // fixed-point PriceScale
	High          int64  `json:"high"`
	Low           int64  `json:"low"`
	Close         int64  `json:"close"`
	AdjustedClose int64  `json:"adjusted_close"`
	Volume        int64  `json:"volume"`
	Source        string `json:"source"`
	IngestedAt    int64  `json:"ingested_at"`
}

type FeatureSnapshot struct {
	Symbol          string `json:"symbol"`
	Interval        string `json:"interval"`
	Timestamp       int64  `json:"timestamp"`
	FeatureSet      string `json:"feature_set"`
	Return1PPM      int64  `json:"return_1_ppm"`
	Return5PPM      int64  `json:"return_5_ppm"`
	SMA5            int64  `json:"sma_5"`
	SMA20           int64  `json:"sma_20"`
	Volatility20PPM int64  `json:"volatility_20_ppm"`
	RSI14PPM        int64  `json:"rsi_14_ppm"`
	VolumeSMA20     int64  `json:"volume_sma_20"`
	ComputedAt      int64  `json:"computed_at"`
}

type ModelRun struct {
	RunID          string
	ModelName      string
	ModelVersion   string
	FeatureSet     string
	Target         string
	HorizonSeconds int64
	TrainingStart  int64
	TrainingEnd    int64
	CreatedAt      int64
	ParametersJSON string
	MetricsJSON    string
	DatasetHash    string
	Status         string
}

type Forecast struct {
	RunID                 string
	Symbol                string
	Interval              string
	AsOfTimestamp         int64
	HorizonSeconds        int64
	TargetTimestamp       int64
	BaselineClose         int64
	PredictedClose        int64
	LowerBound            int64
	UpperBound            int64
	ConfidencePPM         int64
	HasActual             bool
	ActualClose           int64
	EvaluatedAt           int64
	AbsoluteError         int64
	AbsolutePercentagePPM int64
	DirectionCorrect      bool
	CreatedAt             int64
}

type Analysis struct {
	AnalysisID    string
	Symbol        string
	AsOfTimestamp int64
	Provider      string
	Model         string
	PromptVersion string
	InputDigest   string
	Thesis        string
	SentimentPPM  int64 // -RatioScale (bearish) to +RatioScale (bullish)
	ConfidencePPM int64
	EvidenceJSON  string
	ForecastRunID string
	CreatedAt     int64
}

type IngestionCheckpoint struct {
	Source        string `json:"source"`
	Dataset       string `json:"dataset"`
	Symbol        string `json:"symbol"`
	Interval      string `json:"interval"`
	LastTimestamp int64  `json:"last_timestamp"`
	LastCursor    string `json:"last_cursor"`
	RowsIngested  int64  `json:"rows_ingested"`
	UpdatedAt     int64  `json:"updated_at"`
}

const (
	CorporateActionDividend = "dividend"
	CorporateActionSplit    = "split"
)

type CorporateAction struct {
	Symbol     string `json:"symbol"`
	ActionType string `json:"action_type"`
	ExDate     int64  `json:"ex_date"`
	Source     string `json:"source"`
	Amount     int64  `json:"amount"` // PriceScale for cash dividends.
	Currency   string `json:"currency"`
	SplitFrom  int64  `json:"split_from"`
	SplitTo    int64  `json:"split_to"`
	RawDigest  string `json:"raw_digest"`
	IngestedAt int64  `json:"ingested_at"`
}

type FeatureWatermark struct {
	Symbol               string `json:"symbol"`
	Interval             string `json:"interval"`
	FeatureSet           string `json:"feature_set"`
	LastCandleTimestamp  int64  `json:"last_candle_timestamp"`
	LastFeatureTimestamp int64  `json:"last_feature_timestamp"`
	UpdatedAt            int64  `json:"updated_at"`
}

type DataQualityReport struct {
	ReportID             string   `json:"report_id"`
	Source               string   `json:"source"`
	Symbol               string   `json:"symbol"`
	Interval             string   `json:"interval"`
	From                 int64    `json:"from"`
	To                   int64    `json:"to"`
	Expected             int      `json:"expected"`
	Observed             int      `json:"observed"`
	Missing              int      `json:"missing"`
	Unexpected           int      `json:"unexpected"`
	Duplicates           int      `json:"duplicates"`
	Invalid              int      `json:"invalid"`
	Outliers             int      `json:"outliers"`
	CoveragePPM          int64    `json:"coverage_ppm"`
	MissingTimestamps    []int64  `json:"missing_timestamps,omitempty"`
	UnexpectedTimestamps []int64  `json:"unexpected_timestamps,omitempty"`
	OutlierTimestamps    []int64  `json:"outlier_timestamps,omitempty"`
	Warnings             []string `json:"warnings,omitempty"`
	DatasetHash          string   `json:"dataset_hash"`
	CreatedAt            int64    `json:"created_at"`
}

type IngestionRun struct {
	RunID       string `json:"run_id"`
	Provider    string `json:"provider"`
	Symbol      string `json:"symbol"`
	Interval    string `json:"interval"`
	From        int64  `json:"from"`
	To          int64  `json:"to"`
	Status      string `json:"status"`
	Pages       int64  `json:"pages"`
	Received    int64  `json:"received"`
	Inserted    int64  `json:"inserted"`
	Updated     int64  `json:"updated"`
	Unchanged   int64  `json:"unchanged"`
	Duplicates  int64  `json:"duplicates"`
	Retries     int64  `json:"retries"`
	CreditsUsed int64  `json:"credits_used"`
	Error       string `json:"error,omitempty"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
}

type IngestReport struct {
	Received  int           `json:"received"`
	Inserted  int           `json:"inserted"`
	Updated   int           `json:"updated"`
	Unchanged int           `json:"unchanged"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration_ns"`
}

type MetricsSnapshot struct {
	CandleBatches          uint64 `json:"candle_batches"`
	CandlesReceived        uint64 `json:"candles_received"`
	CandlesWritten         uint64 `json:"candles_written"`
	CandlesRejected        uint64 `json:"candles_rejected"`
	CandleQueries          uint64 `json:"candle_queries"`
	CandleQueryNanoseconds uint64 `json:"candle_query_nanoseconds"`
	FeaturesComputed       uint64 `json:"features_computed"`
	ForecastsEvaluated     uint64 `json:"forecasts_evaluated"`
}

func ScalePrice(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > float64(math.MaxInt64)/float64(PriceScale) {
		return 0, fmt.Errorf("%w: invalid price %v", ErrInvalidMarketData, value)
	}
	return int64(math.Round(value * float64(PriceScale))), nil
}

func UnscalePrice(value int64) float64 { return float64(value) / float64(PriceScale) }

func normalizeSymbol(symbol string) (string, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" || len(symbol) > 32 {
		return "", fmt.Errorf("%w: symbol must contain 1-32 characters", ErrInvalidMarketData)
	}
	for _, r := range symbol {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune(".-^=/", r) {
			continue
		}
		return "", fmt.Errorf("%w: symbol %q contains an unsupported character", ErrInvalidMarketData, symbol)
	}
	return symbol, nil
}

func validateInterval(interval string) (string, error) {
	interval = strings.ToLower(strings.TrimSpace(interval))
	switch interval {
	case "1m", "5m", "15m", "30m", "1h", "4h", "1d", "1wk", "1mo":
		return interval, nil
	default:
		return "", fmt.Errorf("%w: unsupported interval %q", ErrInvalidMarketData, interval)
	}
}

func normalizeCandle(in Candle, now int64) (Candle, error) {
	var err error
	if in.Symbol, err = normalizeSymbol(in.Symbol); err != nil {
		return Candle{}, err
	}
	if in.Interval, err = validateInterval(in.Interval); err != nil {
		return Candle{}, err
	}
	if in.Timestamp <= 0 {
		return Candle{}, fmt.Errorf("%w: timestamp must be positive", ErrInvalidMarketData)
	}
	if in.Open <= 0 || in.High <= 0 || in.Low <= 0 || in.Close <= 0 {
		return Candle{}, fmt.Errorf("%w: OHLC prices must be positive", ErrInvalidMarketData)
	}
	if in.High < in.Open || in.High < in.Close || in.High < in.Low || in.Low > in.Open || in.Low > in.Close {
		return Candle{}, fmt.Errorf("%w: OHLC price invariants failed for %s at %d", ErrInvalidMarketData, in.Symbol, in.Timestamp)
	}
	if in.AdjustedClose == 0 {
		in.AdjustedClose = in.Close
	}
	if in.AdjustedClose <= 0 || in.Volume < 0 {
		return Candle{}, fmt.Errorf("%w: adjusted close must be positive and volume non-negative", ErrInvalidMarketData)
	}
	in.Source = strings.TrimSpace(in.Source)
	if in.Source == "" {
		return Candle{}, fmt.Errorf("%w: source is required", ErrInvalidMarketData)
	}
	if in.IngestedAt == 0 {
		in.IngestedAt = now
	}
	return in, nil
}
