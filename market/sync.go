package market

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type SyncRequest struct {
	Symbol               string
	Interval             string
	Start                int64
	End                  int64
	ResumeOverlapPeriods int
	SyncCorporateActions bool
	ComputeFeatures      bool
	FeatureSet           string
	Calendar             TradingCalendar
}

type SyncResult struct {
	Run                IngestionRun        `json:"run"`
	Checkpoint         IngestionCheckpoint `json:"checkpoint"`
	CorporateActions   WriteReport         `json:"corporate_actions"`
	PricesAdjusted     int                 `json:"prices_adjusted"`
	FeaturesComputed   int                 `json:"features_computed"`
	ForecastsEvaluated int                 `json:"forecasts_evaluated"`
	Quality            DataQualityReport   `json:"quality"`
	EffectiveStart     int64               `json:"effective_start"`
}

type SyncService struct {
	repository *Repository
	provider   MarketDataProvider
}

func NewSyncService(repository *Repository, provider MarketDataProvider) (*SyncService, error) {
	if repository == nil || provider == nil {
		return nil, errors.New("sync service requires a repository and provider")
	}
	if strings.TrimSpace(provider.Name()) == "" || provider.MaxPointsPerRequest() <= 0 {
		return nil, errors.New("market provider identity and page size are required")
	}
	return &SyncService{repository: repository, provider: provider}, nil
}

// Sync executes bounded provider windows and persists a checkpoint after every
// committed page. Candle writes are idempotent, so a crash between the data
// commit and checkpoint update is safely replayed.
func (s *SyncService) Sync(ctx context.Context, request SyncRequest) (result SyncResult, err error) {
	if request.Symbol, err = normalizeSymbol(request.Symbol); err != nil {
		return result, err
	}
	if request.Interval, err = validateInterval(request.Interval); err != nil {
		return result, err
	}
	if request.Start <= 0 {
		return result, fmt.Errorf("%w: sync start timestamp is required", ErrInvalidMarketData)
	}
	if request.End <= 0 {
		request.End = s.repository.now().UTC().Unix()
	}
	if request.End < request.Start {
		return result, fmt.Errorf("%w: sync end precedes start", ErrInvalidMarketData)
	}
	if request.ResumeOverlapPeriods == 0 {
		request.ResumeOverlapPeriods = 2
	}
	if request.ResumeOverlapPeriods < 0 || request.ResumeOverlapPeriods > 100 {
		return result, fmt.Errorf("%w: resume overlap must be between 0 and 100", ErrInvalidMarketData)
	}
	if request.FeatureSet == "" {
		request.FeatureSet = DefaultFeatureSet
	}
	if request.Calendar == nil {
		request.Calendar = NewNYSECalendar()
	}
	period, err := intervalSeconds(request.Interval)
	if err != nil {
		return result, err
	}
	if request.End > math.MaxInt64-period {
		return result, fmt.Errorf("%w: sync end timestamp overflows interval boundary", ErrInvalidMarketData)
	}

	runID, err := newID("ingest")
	if err != nil {
		return result, err
	}
	run := IngestionRun{RunID: runID, Provider: s.provider.Name(), Symbol: request.Symbol, Interval: request.Interval, From: request.Start, To: request.End, Status: "running", StartedAt: s.repository.now().UTC().Unix()}
	result.Run = run
	if err := s.repository.putIngestionRun(run); err != nil {
		return result, err
	}
	defer func() {
		if err != nil {
			run.Status, run.Error, run.FinishedAt = "failed", boundedString(err.Error(), 512), s.repository.now().UTC().Unix()
			result.Run = run
			_ = s.repository.putIngestionRun(run)
		}
	}()

	effectiveStart := request.Start
	checkpoint, exists, err := s.repository.Checkpoint(s.provider.Name(), "candles", request.Symbol, request.Interval)
	if err != nil {
		return result, err
	}
	if exists && checkpoint.LastTimestamp > 0 {
		candidate := checkpoint.LastTimestamp - int64(request.ResumeOverlapPeriods)*period
		if candidate > effectiveStart {
			effectiveStart = candidate
		}
	} else if exists {
		if cursor, parseErr := strconv.ParseInt(checkpoint.LastCursor, 10, 64); parseErr == nil && cursor > effectiveStart {
			effectiveStart = cursor
		}
	}
	result.EffectiveStart = effectiveStart
	maxPoints := int64(s.provider.MaxPointsPerRequest())
	if maxPoints > math.MaxInt64/period {
		return result, errors.New("provider page size overflows sync window")
	}
	windowSpan := (maxPoints - 1) * period
	earliestChanged := int64(0)
	duplicateCount := 0
	for windowStart := effectiveStart; windowStart <= request.End; {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		windowEnd := windowStart + windowSpan
		if windowEnd < windowStart || windowEnd > request.End {
			windowEnd = request.End
		}
		page, fetchErr := s.provider.FetchCandles(ctx, CandleFetchRequest{Symbol: request.Symbol, Interval: request.Interval, Start: windowStart, End: windowEnd})
		if fetchErr != nil {
			return result, fetchErr
		}
		if len(page.Candles) > s.provider.MaxPointsPerRequest() {
			return result, fmt.Errorf("provider returned %d candles, limit is %d", len(page.Candles), s.provider.MaxPointsPerRequest())
		}
		page.Candles = candlesWithin(page.Candles, windowStart, windowEnd)
		duplicateCount += duplicateCandles(page.Candles)
		if page.Instrument.Symbol != "" {
			if err := s.repository.UpsertInstrument(page.Instrument); err != nil {
				return result, err
			}
		}
		written, writeErr := s.repository.IngestCandles(page.Candles)
		if writeErr != nil {
			return result, writeErr
		}
		if written.Inserted+written.Updated > 0 {
			for _, candle := range page.Candles {
				if earliestChanged == 0 || candle.Timestamp < earliestChanged {
					earliestChanged = candle.Timestamp
				}
			}
		}
		for _, candle := range page.Candles {
			evaluated, evalErr := s.repository.EvaluateForecastsAt(candle.Symbol, candle.Interval, candle.Timestamp, candle.AdjustedClose)
			if evalErr != nil {
				return result, evalErr
			}
			result.ForecastsEvaluated += evaluated
		}
		run.Pages++
		run.Received += int64(written.Received)
		run.Inserted += int64(written.Inserted)
		run.Updated += int64(written.Updated)
		run.Unchanged += int64(written.Unchanged)
		run.Retries += page.Stats.Retries
		run.CreditsUsed += page.Stats.CreditsUsed
		run.Duplicates = int64(duplicateCount)
		for _, candle := range page.Candles {
			if candle.Timestamp > checkpoint.LastTimestamp {
				checkpoint.LastTimestamp = candle.Timestamp
			}
		}
		checkpoint.Source, checkpoint.Dataset, checkpoint.Symbol, checkpoint.Interval = s.provider.Name(), "candles", request.Symbol, request.Interval
		checkpoint.LastCursor = strconv.FormatInt(windowEnd+period, 10)
		checkpoint.RowsIngested += int64(written.Inserted + written.Updated)
		checkpoint.UpdatedAt = s.repository.now().UTC().Unix()
		if err := s.repository.PutCheckpoint(checkpoint); err != nil {
			return result, err
		}
		if err := s.repository.putIngestionRun(run); err != nil {
			return result, err
		}
		if windowEnd == request.End {
			break
		}
		windowStart = windowEnd + period
	}
	result.Checkpoint = checkpoint

	if request.SyncCorporateActions {
		actions, actionErr := s.provider.FetchCorporateActions(ctx, request.Symbol, request.Start, request.End)
		if actionErr != nil {
			return result, actionErr
		}
		run.Retries += actions.Stats.Retries
		run.CreditsUsed += actions.Stats.CreditsUsed
		result.CorporateActions, err = s.repository.IngestCorporateActions(actions.Actions)
		if err != nil {
			return result, err
		}
		result.PricesAdjusted, err = s.repository.RebuildSplitAdjustedCloses(request.Symbol, request.Interval, request.Start, request.End)
		if err != nil {
			return result, err
		}
		if result.PricesAdjusted > 0 && (earliestChanged == 0 || request.Start < earliestChanged) {
			earliestChanged = request.Start
		}
	}
	if request.ComputeFeatures {
		result.FeaturesComputed, err = s.repository.ComputeFeaturesIncremental(request.Symbol, request.Interval, earliestChanged, request.End, request.FeatureSet)
		if err != nil {
			return result, err
		}
	}
	result.Quality, err = s.repository.AssessDataQuality(QualityOptions{Source: s.provider.Name(), Symbol: request.Symbol, Interval: request.Interval, From: request.Start, To: request.End, Calendar: request.Calendar, Duplicates: duplicateCount})
	if err != nil {
		return result, err
	}
	run.Status, run.FinishedAt = "completed", s.repository.now().UTC().Unix()
	result.Run = run
	if err := s.repository.putIngestionRun(run); err != nil {
		return result, err
	}
	return result, nil
}

type UniverseSyncResult struct {
	Results  []SyncResult      `json:"results"`
	Failures map[string]string `json:"failures,omitempty"`
}

// SyncUniverse runs sequentially so the provider's credit limiter remains the
// single source of truth. A symbol failure does not hide successful results.
func (s *SyncService) SyncUniverse(ctx context.Context, requests []SyncRequest, continueOnError bool) UniverseSyncResult {
	out := UniverseSyncResult{Failures: map[string]string{}}
	for _, request := range requests {
		result, err := s.Sync(ctx, request)
		if err != nil {
			out.Failures[request.Symbol] = err.Error()
			if !continueOnError {
				return out
			}
			continue
		}
		out.Results = append(out.Results, result)
	}
	if len(out.Failures) == 0 {
		out.Failures = nil
	}
	return out
}

func intervalSeconds(interval string) (int64, error) {
	values := map[string]int64{"1m": 60, "5m": 300, "15m": 900, "30m": 1800, "1h": 3600, "4h": 14400, "1d": 86400, "1wk": 604800, "1mo": 2678400}
	value, ok := values[interval]
	if !ok {
		return 0, fmt.Errorf("unsupported interval %q", interval)
	}
	return value, nil
}

func candlesWithin(input []Candle, from, to int64) []Candle {
	out := make([]Candle, 0, len(input))
	for _, candle := range input {
		if candle.Timestamp >= from && candle.Timestamp <= to {
			out = append(out, candle)
		}
	}
	return out
}
func duplicateCandles(candles []Candle) int {
	seen := map[string]bool{}
	duplicates := 0
	for _, candle := range candles {
		key := candle.Symbol + "\x00" + candle.Interval + "\x00" + strconv.FormatInt(candle.Timestamp, 10)
		if seen[key] {
			duplicates++
		} else {
			seen[key] = true
		}
	}
	return duplicates
}

func boundedString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
