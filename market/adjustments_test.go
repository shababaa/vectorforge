package market

import (
	"testing"
	"time"
)

func TestIntradaySplitAdjustmentAvoidsDoubleAdjustingDaily(t *testing.T) {
	_, repository := openTestRepository(t)
	base := time.Date(2026, 1, 5, 15, 0, 0, 0, time.UTC)
	candles := []Candle{
		{Symbol: "ACME", Interval: "1h", Timestamp: base.Unix(), Open: 100 * PriceScale, High: 101 * PriceScale, Low: 99 * PriceScale, Close: 100 * PriceScale, AdjustedClose: 100 * PriceScale, Volume: 10, Source: "fixture"},
		{Symbol: "ACME", Interval: "1h", Timestamp: base.Add(24 * time.Hour).Unix(), Open: 50 * PriceScale, High: 51 * PriceScale, Low: 49 * PriceScale, Close: 50 * PriceScale, AdjustedClose: 50 * PriceScale, Volume: 20, Source: "fixture"},
	}
	if _, err := repository.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	action := CorporateAction{Symbol: "ACME", ActionType: CorporateActionSplit, ExDate: base.Add(24 * time.Hour).Unix(), Source: "fixture", SplitFrom: 2, SplitTo: 1}
	if _, err := repository.IngestCorporateActions([]CorporateAction{action}); err != nil {
		t.Fatal(err)
	}
	changed, err := repository.RebuildSplitAdjustedCloses("ACME", "1h", base.Unix(), base.Add(24*time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed=%d", changed)
	}
	rows, err := repository.Candles(CandleQuery{Symbol: "ACME", Interval: "1h", From: base.Unix(), To: base.Add(24 * time.Hour).Unix(), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].AdjustedClose != 50*PriceScale || rows[1].AdjustedClose != 50*PriceScale {
		t.Fatalf("adjusted=%+v", rows)
	}
	if changed, err = repository.RebuildSplitAdjustedCloses("ACME", "1d", base.Unix(), base.Add(24*time.Hour).Unix()); err != nil || changed != 0 {
		t.Fatalf("daily adjustment=(%d,%v)", changed, err)
	}
}
