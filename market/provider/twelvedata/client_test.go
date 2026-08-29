package twelvedata

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"byodb/market"
)

func testClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := New(Config{APIKey: "secret-key", BaseURL: server.URL, HTTPClient: server.Client(), MaxAttempts: 3, Limiter: noWaitLimiter{}, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestFetchCandlesRejectsEndBoundaryOverflow(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("overflowing request reached the server")
	}))
	_, err := client.FetchCandles(context.Background(), market.CandleFetchRequest{Symbol: "AAPL", Interval: "1d", Start: 1, End: math.MaxInt64})
	if err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("error=%v", err)
	}
}

func TestFetchCandlesParsesOfficialResponse(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/time_series" || r.URL.Query().Get("apikey") != "secret-key" || r.URL.Query().Get("order") != "ASC" || r.URL.Query().Get("outputsize") != "5000" {
			t.Fatalf("unexpected request %s", r.URL.RequestURI())
		}
		if r.URL.Query().Get("end_date") != "2026-08-27" {
			t.Fatalf("exclusive provider boundary not advanced: %s", r.URL.Query().Get("end_date"))
		}
		w.Header().Set("api-credits-used", "1")
		_, _ = w.Write([]byte(`{"meta":{"symbol":"AAPL","interval":"1day","currency":"USD","exchange_timezone":"America/New_York","exchange":"NASDAQ","type":"Common Stock"},"values":[{"datetime":"2026-08-27","open":"310.50","high":"315.40","low":"309.39","close":"314.58","volume":"32419200"}],"status":"ok"}`))
	}))
	page, err := client.FetchCandles(context.Background(), market.CandleFetchRequest{Symbol: "AAPL", Interval: "1d", Start: 1_787_616_000, End: 1_787_702_400})
	if err != nil || len(page.Candles) != 1 {
		t.Fatalf("page=(%+v,%v)", page, err)
	}
	if page.Candles[0].Close != 314_580_000 || page.Candles[0].Source != "twelvedata" || page.Instrument.Exchange != "NASDAQ" || page.Stats.CreditsUsed != 1 {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestRetries429WithoutLeakingAPIKey(t *testing.T) {
	var calls atomic.Int32
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":429,"message":"quota","status":"error"}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"symbol":"AAPL"},"values":[],"status":"ok"}`))
	}))
	page, err := client.FetchCandles(context.Background(), market.CandleFetchRequest{Symbol: "AAPL", Interval: "1d", Start: 1, End: 2})
	if err != nil || page.Stats.Retries != 1 || calls.Load() != 2 {
		t.Fatalf("retry=(%+v,%v,%d)", page.Stats, err, calls.Load())
	}

	bad := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":400,"message":"bad symbol","status":"error"}`))
	}))
	_, err = bad.FetchCandles(context.Background(), market.CandleFetchRequest{Symbol: "BAD", Interval: "1d", Start: 1, End: 2})
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestFetchCorporateActions(t *testing.T) {
	client := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/splits":
			_, _ = w.Write([]byte(`{"meta":{"symbol":"AAPL","currency":"USD"},"splits":[{"date":"2020-08-31","description":"4-for-1 split","ratio":0.25,"from_factor":4,"to_factor":1}]}`))
		case "/dividends":
			_, _ = w.Write([]byte(`{"meta":{"symbol":"AAPL","currency":"USD"},"dividends":[{"ex_date":"2026-08-10","amount":0.27}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	page, err := client.FetchCorporateActions(context.Background(), "AAPL", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC).Unix())
	if err != nil || len(page.Actions) != 2 {
		t.Fatalf("actions=(%+v,%v)", page, err)
	}
	if page.Actions[0].SplitFrom != 4 || page.Actions[1].Amount != 270_000 || page.Actions[1].RawDigest == "" {
		t.Fatalf("unexpected actions: %+v", page.Actions)
	}
}
