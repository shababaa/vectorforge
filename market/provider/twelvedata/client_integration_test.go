//go:build integration

package twelvedata

import (
	"context"
	"os"
	"testing"
	"time"

	"byodb/market"
)

func TestLiveTimeSeriesContract(t *testing.T) {
	apiKey := os.Getenv("TWELVE_DATA_API_KEY")
	if apiKey == "" {
		t.Skip("TWELVE_DATA_API_KEY is not set")
	}
	client, err := New(Config{APIKey: apiKey})
	if err != nil {
		t.Fatal(err)
	}
	end := time.Now().UTC()
	start := end.AddDate(0, 0, -10)
	page, err := client.FetchCandles(context.Background(), market.CandleFetchRequest{Symbol: "AAPL", Interval: "1d", Start: start.Unix(), End: end.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Candles) == 0 || page.Instrument.Currency == "" || page.Stats.CreditsUsed <= 0 {
		t.Fatalf("unexpected live page: %+v", page)
	}
}
