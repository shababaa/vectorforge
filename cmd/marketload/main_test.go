package main

import (
	"testing"
	"time"
)

func TestSummarizeLoadResults(t *testing.T) {
	results := []result{{latency: time.Millisecond, status: 200, bytes: 10}, {latency: 3 * time.Millisecond, status: 200, bytes: 20}, {latency: 2 * time.Millisecond, status: 503, bytes: 5}}
	report := summarize("http://example.test/healthz", 2, time.Second, results)
	if report.Requests != 3 || report.Successful != 2 || report.Errors != 1 || report.RequestsPerSec != 3 || report.P50Milliseconds != 2 || report.P95Milliseconds != 3 || report.ResponseBytes != 35 || report.StatusCodes["503"] != 1 {
		t.Fatalf("report=%+v", report)
	}
}
