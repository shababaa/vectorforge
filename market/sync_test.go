package market

import (
	"context"
	"errors"
	"math"
	"sort"
	"testing"
	"time"
)

type fakeProvider struct {
	candles  []Candle
	actions  []CorporateAction
	max      int
	requests []CandleFetchRequest
	failAt   int
}

func TestSyncRejectsTimestampOverflowBeforeProviderCall(t *testing.T) {
	_, repository := openTestRepository(t)
	provider := &fakeProvider{max: 2}
	service, _ := NewSyncService(repository, provider)
	_, err := service.Sync(context.Background(), SyncRequest{Symbol: "AAPL", Interval: "1d", Start: 1, End: math.MaxInt64})
	if !errors.Is(err, ErrInvalidMarketData) || len(provider.requests) != 0 {
		t.Fatalf("error=%v requests=%d", err, len(provider.requests))
	}
}

func (f *fakeProvider) Name() string             { return "fake-provider" }
func (f *fakeProvider) MaxPointsPerRequest() int { return f.max }
func (f *fakeProvider) FetchCandles(_ context.Context, request CandleFetchRequest) (CandlePage, error) {
	f.requests = append(f.requests, request)
	if f.failAt > 0 && len(f.requests) == f.failAt {
		return CandlePage{}, errors.New("provider unavailable")
	}
	page := CandlePage{Instrument: Instrument{Symbol: request.Symbol, Name: "Apple", AssetType: "equity", Exchange: "NASDAQ", Currency: "USD", Active: true}, Stats: ProviderRequestStats{CreditsUsed: 1}}
	for _, candle := range f.candles {
		if candle.Timestamp >= request.Start && candle.Timestamp <= request.End {
			page.Candles = append(page.Candles, candle)
		}
	}
	sort.Slice(page.Candles, func(i, j int) bool { return page.Candles[i].Timestamp < page.Candles[j].Timestamp })
	return page, nil
}
func (f *fakeProvider) FetchCorporateActions(context.Context, string, int64, int64) (CorporateActionPage, error) {
	return CorporateActionPage{Actions: f.actions, Stats: ProviderRequestStats{CreditsUsed: 2}}, nil
}

func TestSyncWindowsCheckpointsAndResumesCorrections(t *testing.T) {
	_, repository := openTestRepository(t)
	base := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	candles := make([]Candle, 5)
	for i := range candles {
		price := int64(100+i) * PriceScale
		candles[i] = Candle{Symbol: "AAPL", Interval: "1d", Timestamp: base.AddDate(0, 0, i).Unix(), Open: price, High: price + PriceScale, Low: price - PriceScale, Close: price, AdjustedClose: price, Volume: 1_000, Source: "fake-provider"}
	}
	provider := &fakeProvider{candles: candles, max: 2, actions: []CorporateAction{{Symbol: "AAPL", ActionType: CorporateActionDividend, ExDate: base.Unix(), Source: "fake-provider", Amount: 250_000, Currency: "USD"}}}
	service, err := NewSyncService(repository, provider)
	if err != nil {
		t.Fatal(err)
	}
	request := SyncRequest{Symbol: "AAPL", Interval: "1d", Start: base.Unix(), End: base.AddDate(0, 0, 4).Unix(), SyncCorporateActions: true, ComputeFeatures: true, Calendar: NewNYSECalendar()}
	result, err := service.Sync(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.Status != "completed" || result.Run.Pages != 3 || result.Run.Inserted != 5 || result.Run.CreditsUsed != 5 || result.Checkpoint.LastTimestamp != candles[4].Timestamp || result.CorporateActions.Inserted != 1 {
		t.Fatalf("sync=%+v", result)
	}
	provider.requests = nil
	provider.candles[3].Close += PriceScale
	provider.candles[3].AdjustedClose = provider.candles[3].Close
	provider.candles[3].High = provider.candles[3].Close + PriceScale
	result, err = service.Sync(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveStart != candles[2].Timestamp || result.Run.Updated != 1 || result.Run.Unchanged != 2 || len(provider.requests) != 2 {
		t.Fatalf("resumed=%+v requests=%+v", result, provider.requests)
	}
}

func TestSyncPersistsFailedRunAndPartialCheckpoint(t *testing.T) {
	_, repository := openTestRepository(t)
	base := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	provider := &fakeProvider{max: 2, failAt: 2, candles: []Candle{{Symbol: "AAPL", Interval: "1d", Timestamp: base.Unix(), Open: 100, High: 101, Low: 99, Close: 100, AdjustedClose: 100, Volume: 1, Source: "fake-provider"}}}
	service, _ := NewSyncService(repository, provider)
	result, err := service.Sync(context.Background(), SyncRequest{Symbol: "AAPL", Interval: "1d", Start: base.Unix(), End: base.AddDate(0, 0, 3).Unix()})
	if err == nil {
		t.Fatal("expected provider failure")
	}
	stored, ok, getErr := repository.IngestionRun(result.Run.RunID)
	if getErr != nil || !ok || stored.Status != "failed" || stored.Error == "" {
		t.Fatalf("failed run=(%+v,%v,%v)", stored, ok, getErr)
	}
	checkpoint, ok, getErr := repository.Checkpoint(provider.Name(), "candles", "AAPL", "1d")
	if getErr != nil || !ok || checkpoint.LastCursor == "" {
		t.Fatalf("checkpoint=(%+v,%v,%v)", checkpoint, ok, getErr)
	}
}
