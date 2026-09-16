package market

import (
	"testing"
	"time"
)

func TestNYSECalendarHolidays(t *testing.T) {
	calendar := NewNYSECalendar()
	checks := map[string]bool{"2026-04-03": false, "2026-07-03": false, "2026-11-26": false, "2026-07-02": true, "2026-11-27": true, "2021-12-31": true, "2025-01-09": false}
	for value, want := range checks {
		date, _ := time.Parse("2006-01-02", value)
		if got := calendar.IsSession(date); got != want {
			t.Errorf("session %s=%v want %v", value, got, want)
		}
	}
	sessions := calendar.Sessions(time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC))
	if len(sessions) != 2 || sessions[0].Day() != 2 || sessions[1].Day() != 6 {
		t.Fatalf("sessions=%v", sessions)
	}
}

func TestQualityReportFindsMissingSessionAndOutlier(t *testing.T) {
	_, repository := openTestRepository(t)
	base := time.Date(2026, 7, 6, 0, 0, 0, 0, time.UTC)
	prices := []int64{100, 101, 150}
	days := []int{0, 2, 3} // July 7 is deliberately missing.
	candles := make([]Candle, len(days))
	for i, day := range days {
		price := prices[i] * PriceScale
		candles[i] = Candle{Symbol: "AAPL", Interval: "1d", Timestamp: base.AddDate(0, 0, day).Unix(), Open: price, High: price + PriceScale, Low: price - PriceScale, Close: price, AdjustedClose: price, Volume: 1_000, Source: "fixture"}
	}
	if _, err := repository.IngestCandles(candles); err != nil {
		t.Fatal(err)
	}
	report, err := repository.AssessDataQuality(QualityOptions{Source: "fixture", Symbol: "AAPL", Interval: "1d", From: base.Unix(), To: base.AddDate(0, 0, 3).Unix(), Calendar: NewNYSECalendar()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Expected != 4 || report.Observed != 3 || report.Missing != 1 || report.Outliers != 1 || report.CoveragePPM != 750_000 {
		t.Fatalf("quality=%+v", report)
	}
	stored, ok, err := repository.DataQualityReport(report.ReportID)
	if err != nil || !ok || len(stored.MissingTimestamps) != 1 {
		t.Fatalf("stored=(%+v,%v,%v)", stored, ok, err)
	}
}

func TestQualityReportPersistsUnexpectedNonSession(t *testing.T) {
	_, repository := openTestRepository(t)
	saturday := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	price := int64(100 * PriceScale)
	if _, err := repository.IngestCandles([]Candle{{Symbol: "AAPL", Interval: "1d", Timestamp: saturday.Unix(), Open: price, High: price + PriceScale, Low: price - PriceScale, Close: price, AdjustedClose: price, Volume: 1_000, Source: "fixture"}}); err != nil {
		t.Fatal(err)
	}
	report, err := repository.AssessDataQuality(QualityOptions{Source: "fixture", Symbol: "AAPL", Interval: "1d", From: saturday.Unix(), To: saturday.Unix(), Calendar: NewNYSECalendar()})
	if err != nil {
		t.Fatal(err)
	}
	if report.Expected != 0 || report.Unexpected != 1 || len(report.UnexpectedTimestamps) != 1 {
		t.Fatalf("quality=%+v", report)
	}
	stored, ok, err := repository.DataQualityReport(report.ReportID)
	if err != nil || !ok || stored.Unexpected != 1 || len(stored.UnexpectedTimestamps) != 1 {
		t.Fatalf("stored=(%+v,%v,%v)", stored, ok, err)
	}
}
