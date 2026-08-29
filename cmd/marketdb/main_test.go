package main

import (
	"testing"
	"time"
)

func TestSyncRangeAndSymbols(t *testing.T) {
	start, end, err := syncRange("2026-01-02", "2026-01-03", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if time.Unix(start, 0).UTC().Format("2006-01-02") != "2026-01-02" || time.Unix(end, 0).UTC().Format("2006-01-02 15:04:05") != "2026-01-03 23:59:59" {
		t.Fatalf("range=(%d,%d)", start, end)
	}
	symbols := parseSymbols(" aapl,MSFT,aapl ", "SPY")
	if len(symbols) != 2 || symbols[0] != "AAPL" || symbols[1] != "MSFT" {
		t.Fatalf("symbols=%v", symbols)
	}
}
