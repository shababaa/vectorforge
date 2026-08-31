package sec

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const submissions = `{"cik":320193,"tickers":["AAPL"],"filings":{"recent":{"accessionNumber":["0000320193-26-000001","0000320193-26-000002"],"form":["10-Q","4"],"acceptanceDateTime":["2026-01-15T16:00:00Z","2026-01-16T17:00:00Z"],"primaryDocument":["aapl-20251227.htm","ownership.xml"]}}}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSECParsingAndIdentifiers(t *testing.T) {
	rows, err := ParseSubmissions([]byte(submissions), "AAPL", "0000320193")
	if err != nil || len(rows) != 1 || rows[0].URL != "https://www.sec.gov/Archives/edgar/data/320193/000032019326000001/aapl-20251227.htm" || rows[0].RetrievedAt != 0 {
		t.Fatalf("%+v %v", rows, err)
	}
	for _, raw := range []string{"{}", `[]`, strings.Replace(submissions, "320193", "123", 1), strings.Replace(submissions, `"AAPL"`, `"MSFT"`, 1), strings.Replace(submissions, `"4"`, `"8-K","10-K"`, 1), strings.Replace(submissions, "aapl-20251227.htm", "../../secret.htm", 1), strings.Replace(submissions, "2026-01-15T16:00:00Z", "yesterday", 1)} {
		if _, err := ParseSubmissions([]byte(raw), "AAPL", "0000320193"); err == nil {
			t.Fatal("invalid payload accepted", raw)
		}
	}
}

func TestSECRequestPolicy(t *testing.T) {
	calls := 0
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != "https://data.sec.gov/submissions/CIK0000320193.json" || req.Header.Get("User-Agent") != "Student Project student@example.test" || req.Header.Get("Accept") != "application/json" {
			t.Fatal("wrong request", req.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(submissions)), Request: req}, nil
	})
	client, err := New(Config{UserAgent: "Student Project student@example.test", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.RecentFilings(context.Background(), "AAPL", "0000320193"); err != nil || calls != 1 {
		t.Fatal(err)
	}
	if _, err := client.RecentFilings(context.Background(), "AAPL", "../../secret"); err == nil {
		t.Fatal("bad CIK accepted")
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("anonymous user agent accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.RecentFilings(ctx, "AAPL", "0000320193"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSECResponseLimitsAndCooldown(t *testing.T) {
	for _, tc := range []struct {
		status      int
		body, after string
	}{
		{403, "SECRET", ""}, {302, "SECRET", ""}, {200, strings.Repeat("x", (4<<20)+1), ""}, {429, "SECRET", "60"}, {429, "SECRET", "9223372036854775807"}, {503, "SECRET", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)},
	} {
		client, _ := New(Config{UserAgent: "Student Project student@example.test", HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{tc.after}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
		})}})
		_, err := client.RecentFilings(context.Background(), "AAPL", "0000320193")
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
	}
}

func TestSECRetryAndContext(t *testing.T) {
	calls := 0
	client, _ := New(Config{UserAgent: "Student Project student@example.test", HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		status, body := 503, "temporarily unavailable"
		if calls == 2 {
			status, body = 200, submissions
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
	})}})
	rows, err := client.RecentFilings(context.Background(), "AAPL", "0000320193")
	if err != nil || calls != 2 || len(rows) != 1 {
		t.Fatal(calls, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	client.next = time.Now().Add(time.Second)
	if err := client.wait(ctx, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
