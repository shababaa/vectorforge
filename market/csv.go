package market

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type CSVImportOptions struct {
	Symbol    string
	Interval  string
	Source    string
	BatchSize int
}

// ImportCSV accepts a provider-neutral OHLCV file. Required headers are
// timestamp, open, high, low, close, and volume; adjusted_close is optional.
func (r *Repository) ImportCSV(reader io.Reader, options CSVImportOptions) (IngestReport, error) {
	if options.BatchSize <= 0 {
		options.BatchSize = 1_000
	}
	if options.BatchSize > 25_000 {
		return IngestReport{}, fmt.Errorf("batch size cannot exceed 25000")
	}
	parser := csv.NewReader(reader)
	parser.TrimLeadingSpace = true
	headers, err := parser.Read()
	if err != nil {
		return IngestReport{}, err
	}
	columns := map[string]int{}
	for i, header := range headers {
		columns[strings.ToLower(strings.TrimSpace(header))] = i
	}
	for _, required := range []string{"timestamp", "open", "high", "low", "close", "volume"} {
		if _, ok := columns[required]; !ok {
			return IngestReport{}, fmt.Errorf("CSV is missing required header %q", required)
		}
	}
	total := IngestReport{StartedAt: r.now().UTC()}
	batch := make([]Candle, 0, options.BatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		report, err := r.IngestCandles(batch)
		if err != nil {
			return err
		}
		total.Received += report.Received
		total.Inserted += report.Inserted
		total.Updated += report.Updated
		total.Unchanged += report.Unchanged
		batch = batch[:0]
		return nil
	}
	line := 1
	for {
		line++
		row, err := parser.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("CSV line %d: %w", line, err)
		}
		candle, err := parseCSVRow(row, columns, options)
		if err != nil {
			return total, fmt.Errorf("CSV line %d: %w", line, err)
		}
		batch = append(batch, candle)
		if len(batch) == options.BatchSize {
			if err := flush(); err != nil {
				return total, err
			}
		}
	}
	if err := flush(); err != nil {
		return total, err
	}
	total.Duration = r.now().UTC().Sub(total.StartedAt)
	return total, nil
}

func parseCSVRow(row []string, columns map[string]int, options CSVImportOptions) (Candle, error) {
	get := func(name string) (string, error) {
		index, ok := columns[name]
		if !ok {
			return "", nil
		}
		if index >= len(row) {
			return "", fmt.Errorf("missing value for %q", name)
		}
		return strings.TrimSpace(row[index]), nil
	}
	timestampText, _ := get("timestamp")
	timestamp, err := parseTimestamp(timestampText)
	if err != nil {
		return Candle{}, err
	}
	price := func(name string) (int64, error) {
		text, err := get(name)
		if err != nil {
			return 0, err
		}
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid %s %q", name, text)
		}
		return ScalePrice(value)
	}
	open, err := price("open")
	if err != nil {
		return Candle{}, err
	}
	high, err := price("high")
	if err != nil {
		return Candle{}, err
	}
	low, err := price("low")
	if err != nil {
		return Candle{}, err
	}
	closeValue, err := price("close")
	if err != nil {
		return Candle{}, err
	}
	adjusted := closeValue
	if text, _ := get("adjusted_close"); text != "" {
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return Candle{}, fmt.Errorf("invalid adjusted_close %q", text)
		}
		adjusted, err = ScalePrice(value)
		if err != nil {
			return Candle{}, err
		}
	}
	volumeText, _ := get("volume")
	volume, err := strconv.ParseInt(volumeText, 10, 64)
	if err != nil {
		return Candle{}, fmt.Errorf("invalid volume %q", volumeText)
	}
	return Candle{Symbol: options.Symbol, Interval: options.Interval, Timestamp: timestamp, Open: open, High: high, Low: low, Close: closeValue, AdjustedClose: adjusted, Volume: volume, Source: options.Source}, nil
}

func parseTimestamp(value string) (int64, error) {
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		return unix, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return parsed.Unix(), nil
		}
	}
	return 0, fmt.Errorf("invalid timestamp %q", value)
}
