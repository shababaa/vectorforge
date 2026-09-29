package market

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFiveYearUniverseAcceptance is the Phase 2 multi-asset exit check.
// The files are a frozen Yahoo Chart daily snapshot for 2021-08-30 through
// 2026-08-27. This is not a Twelve Data live run and does not claim a vendor
// vintage; it proves the repeatable import and NYSE quality job on three assets.
func TestFiveYearUniverseAcceptance(t *testing.T) {
	_, repository := openTestRepository(t)
	from := time.Date(2021, 8, 29, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	expected := len(NewNYSECalendar().Sessions(from, to))
	if expected != 1254 {
		t.Fatalf("NYSE sessions=%d, want 1254 for the recorded window", expected)
	}
	const source = "yahoo-chart-v8"
	for _, symbol := range []string{"AAPL", "MSFT", "SPY"} {
		path := filepath.Join("testdata", "universe", symbol+".csv")
		importFile := func() IngestReport {
			t.Helper()
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			report, err := repository.ImportCSV(file, CSVImportOptions{Symbol: symbol, Interval: "1d", Source: source})
			if err != nil {
				t.Fatal(err)
			}
			return report
		}
		first := importFile()
		if first.Inserted != expected || first.Received != expected {
			t.Fatalf("%s import=%+v, want %d inserted", symbol, first, expected)
		}
		second := importFile()
		if second.Unchanged != expected || second.Inserted != 0 {
			t.Fatalf("%s reimport=%+v, want unchanged replay", symbol, second)
		}
		quality, err := repository.AssessDataQuality(QualityOptions{
			Source: source, Symbol: symbol, Interval: "1d",
			From: from.Unix(), To: to.Unix(), Calendar: NewNYSECalendar(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if quality.Expected != expected || quality.Observed != expected || quality.Missing != 0 || quality.Unexpected != 0 || quality.CoveragePPM != RatioScale {
			t.Fatalf("%s quality=%+v", symbol, quality)
		}
		features, err := repository.ComputeFeaturesIncremental(symbol, "1d", from.Unix(), to.Unix(), DefaultFeatureSet)
		if err != nil {
			t.Fatal(err)
		}
		if features < expected-21 {
			t.Fatalf("%s features=%d, want at least %d", symbol, features, expected-21)
		}
		t.Logf("%s rows=%d features=%d outliers=%d hash=%s", symbol, quality.Observed, features, quality.Outliers, quality.DatasetHash)
	}
}
