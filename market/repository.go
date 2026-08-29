package market

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"byodb"
)

type repositoryMetrics struct {
	candleBatches          atomic.Uint64
	candlesReceived        atomic.Uint64
	candlesWritten         atomic.Uint64
	candlesRejected        atomic.Uint64
	candleQueries          atomic.Uint64
	candleQueryNanoseconds atomic.Uint64
	featuresComputed       atomic.Uint64
	forecastsEvaluated     atomic.Uint64
}

type Repository struct {
	db      *byodb.DB
	now     func() time.Time
	metrics repositoryMetrics
}

func NewRepository(db *byodb.DB) (*Repository, error) {
	if db == nil {
		return nil, errors.New("market repository requires a database")
	}
	return &Repository{db: db, now: time.Now}, nil
}

func (r *Repository) Metrics() MetricsSnapshot {
	return MetricsSnapshot{
		CandleBatches: r.metrics.candleBatches.Load(), CandlesReceived: r.metrics.candlesReceived.Load(),
		CandlesWritten: r.metrics.candlesWritten.Load(), CandlesRejected: r.metrics.candlesRejected.Load(),
		CandleQueries: r.metrics.candleQueries.Load(), CandleQueryNanoseconds: r.metrics.candleQueryNanoseconds.Load(),
		FeaturesComputed: r.metrics.featuresComputed.Load(), ForecastsEvaluated: r.metrics.forecastsEvaluated.Load(),
	}
}

func (r *Repository) StorageStats() (byodb.DBStats, error) { return r.db.Stats() }

func (r *Repository) UpsertInstrument(in Instrument) error {
	symbol, err := normalizeSymbol(in.Symbol)
	if err != nil {
		return err
	}
	in.Symbol = symbol
	in.Name, in.AssetType, in.Exchange, in.Currency = strings.TrimSpace(in.Name), strings.TrimSpace(in.AssetType), strings.TrimSpace(in.Exchange), strings.ToUpper(strings.TrimSpace(in.Currency))
	if in.Name == "" || in.AssetType == "" || in.Exchange == "" || in.Currency == "" {
		return fmt.Errorf("%w: instrument name, asset type, exchange, and currency are required", ErrInvalidMarketData)
	}
	now := r.now().UTC().Unix()
	if in.CreatedAt == 0 {
		if existing, ok, getErr := r.Instrument(in.Symbol); getErr != nil {
			return getErr
		} else if ok {
			in.CreatedAt = existing.CreatedAt
		} else {
			in.CreatedAt = now
		}
	}
	in.UpdatedAt = now
	_, err = r.db.Upsert(tableInstruments, instrumentRecord(in))
	return err
}

func (r *Repository) Instrument(symbol string) (Instrument, bool, error) {
	symbol, err := normalizeSymbol(symbol)
	if err != nil {
		return Instrument{}, false, err
	}
	rec := (&byodb.Record{}).AddString("symbol", symbol)
	ok, err := r.db.Get(tableInstruments, rec)
	if err != nil || !ok {
		return Instrument{}, ok, err
	}
	return instrumentFromRecord(*rec), true, nil
}

// IngestCandles validates the complete batch, then atomically upserts it. A
// repeated provider payload is safe: unchanged rows do not create new writes.
func (r *Repository) IngestCandles(input []Candle) (IngestReport, error) {
	report := IngestReport{Received: len(input), StartedAt: r.now().UTC()}
	r.metrics.candleBatches.Add(1)
	r.metrics.candlesReceived.Add(uint64(len(input)))
	normalized := make([]Candle, len(input))
	now := report.StartedAt.Unix()
	for i, candle := range input {
		var err error
		normalized[i], err = normalizeCandle(candle, now)
		if err != nil {
			r.metrics.candlesRejected.Add(1)
			return report, fmt.Errorf("candle %d: %w", i, err)
		}
	}
	// Stable order improves B+tree locality for unsorted provider responses.
	sort.SliceStable(normalized, func(i, j int) bool {
		a, b := normalized[i], normalized[j]
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.Interval != b.Interval {
			return a.Interval < b.Interval
		}
		return a.Timestamp < b.Timestamp
	})
	err := r.writeTransaction(func(tx *byodb.DBTX) error {
		report.Inserted, report.Updated, report.Unchanged = 0, 0, 0
		for _, candle := range normalized {
			key := (&byodb.Record{}).AddString("symbol", candle.Symbol).AddString("interval", candle.Interval).AddInt64("timestamp", candle.Timestamp)
			exists, err := tx.Get(tableCandles, key)
			if err != nil {
				return err
			}
			if exists && candlesEquivalent(candleFromRecord(*key), candle) {
				report.Unchanged++
				continue
			}
			changed, err := tx.Set(tableCandles, candleRecord(candle), byodb.MODE_UPSERT)
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
	report.Duration = r.now().UTC().Sub(report.StartedAt)
	if err == nil {
		r.metrics.candlesWritten.Add(uint64(report.Inserted + report.Updated))
	}
	return report, err
}

func candlesEquivalent(a, b Candle) bool {
	return a.Symbol == b.Symbol && a.Interval == b.Interval && a.Timestamp == b.Timestamp && a.Open == b.Open && a.High == b.High && a.Low == b.Low && a.Close == b.Close && a.AdjustedClose == b.AdjustedClose && a.Volume == b.Volume && a.Source == b.Source
}

// Candles performs an indexed time-range scan. Results are ascending unless
// Descending is true. Limit must be positive and is capped to protect callers
// from accidentally loading an unbounded history into memory.
type CandleQuery struct {
	Symbol     string
	Interval   string
	From       int64
	To         int64
	Limit      int
	Descending bool
}

func (r *Repository) Candles(query CandleQuery) ([]Candle, error) {
	started := time.Now()
	defer func() {
		r.metrics.candleQueries.Add(1)
		r.metrics.candleQueryNanoseconds.Add(uint64(time.Since(started)))
	}()
	var err error
	query.Symbol, err = normalizeSymbol(query.Symbol)
	if err != nil {
		return nil, err
	}
	query.Interval, err = validateInterval(query.Interval)
	if err != nil {
		return nil, err
	}
	if query.From < 0 || query.To < query.From {
		return nil, fmt.Errorf("%w: invalid candle time range", ErrInvalidMarketData)
	}
	if query.Limit <= 0 || query.Limit > 100_000 {
		return nil, fmt.Errorf("%w: limit must be between 1 and 100000", ErrInvalidMarketData)
	}
	if query.To == 0 {
		query.To = int64(^uint64(0) >> 1)
	}
	low := *(&byodb.Record{}).AddString("symbol", query.Symbol).AddString("interval", query.Interval).AddInt64("timestamp", query.From)
	high := *(&byodb.Record{}).AddString("symbol", query.Symbol).AddString("interval", query.Interval).AddInt64("timestamp", query.To)
	sc := &byodb.Scanner{Cmp1: byodb.CMP_GE, Key1: low, Cmp2: byodb.CMP_LE, Key2: high, Index: []string{"symbol", "interval", "timestamp"}}
	if query.Descending {
		sc.Cmp1, sc.Key1, sc.Cmp2, sc.Key2 = byodb.CMP_LE, high, byodb.CMP_GE, low
	}
	var tx byodb.DBTX
	if err := r.db.Begin(&tx); err != nil {
		return nil, err
	}
	defer r.db.Abort(&tx)
	if err := tx.Scan(tableCandles, sc); err != nil {
		return nil, err
	}
	out := make([]Candle, 0, min(query.Limit, 256))
	for sc.Valid() && len(out) < query.Limit {
		var rec byodb.Record
		if err := sc.Deref(&rec); err != nil {
			return nil, err
		}
		out = append(out, candleFromRecord(rec))
		sc.Next()
	}
	return out, nil
}

func (r *Repository) PutCheckpoint(checkpoint IngestionCheckpoint) error {
	var err error
	if checkpoint.Symbol, err = normalizeSymbol(checkpoint.Symbol); err != nil {
		return err
	}
	if checkpoint.Interval, err = validateInterval(checkpoint.Interval); err != nil {
		return err
	}
	checkpoint.Source, checkpoint.Dataset = strings.TrimSpace(checkpoint.Source), strings.TrimSpace(checkpoint.Dataset)
	if checkpoint.Source == "" || checkpoint.Dataset == "" {
		return fmt.Errorf("%w: checkpoint source and dataset are required", ErrInvalidMarketData)
	}
	if checkpoint.LastTimestamp < 0 || checkpoint.RowsIngested < 0 {
		return fmt.Errorf("%w: checkpoint counters cannot be negative", ErrInvalidMarketData)
	}
	checkpoint.UpdatedAt = r.now().UTC().Unix()
	_, err = r.db.Upsert(tableCheckpoints, checkpointRecord(checkpoint))
	return err
}
