package market

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type QualityOptions struct {
	Source     string
	Symbol     string
	Interval   string
	From       int64
	To         int64
	Calendar   TradingCalendar
	Duplicates int
	Invalid    int
}

func (r *Repository) AssessDataQuality(options QualityOptions) (DataQualityReport, error) {
	var err error
	if options.Symbol, err = normalizeSymbol(options.Symbol); err != nil {
		return DataQualityReport{}, err
	}
	if options.Interval, err = validateInterval(options.Interval); err != nil {
		return DataQualityReport{}, err
	}
	options.Source = strings.TrimSpace(options.Source)
	if options.Source == "" {
		return DataQualityReport{}, fmt.Errorf("%w: quality source is required", ErrInvalidMarketData)
	}
	if options.Calendar == nil {
		options.Calendar = NewNYSECalendar()
	}
	if options.To <= 0 {
		options.To = r.now().UTC().Unix()
	}
	if options.From <= 0 || options.To < options.From {
		return DataQualityReport{}, fmt.Errorf("%w: quality range is invalid", ErrInvalidMarketData)
	}
	candles, err := r.Candles(CandleQuery{Symbol: options.Symbol, Interval: options.Interval, From: options.From, To: options.To, Limit: 100_000})
	if err != nil {
		return DataQualityReport{}, err
	}
	if len(candles) == 100_000 && candles[len(candles)-1].Timestamp < options.To {
		return DataQualityReport{}, errors.New("quality range exceeds 100000 candles; split the requested range")
	}
	id, err := newID("quality")
	if err != nil {
		return DataQualityReport{}, err
	}
	report := DataQualityReport{ReportID: id, Source: options.Source, Symbol: options.Symbol, Interval: options.Interval, From: options.From, To: options.To, Observed: len(candles), Duplicates: options.Duplicates, Invalid: options.Invalid, DatasetHash: hashCandles(candles), CreatedAt: r.now().UTC().Unix()}
	if options.Interval == "1d" {
		expected := options.Calendar.Sessions(time.Unix(options.From, 0), time.Unix(options.To, 0))
		report.Expected = len(expected)
		expectedSet := make(map[int64]bool, len(expected))
		for _, session := range expected {
			expectedSet[session.Unix()] = true
		}
		observed := make(map[int64]bool, len(candles))
		for _, candle := range candles {
			observed[midnightUTC(time.Unix(candle.Timestamp, 0)).Unix()] = true
		}
		for _, session := range expected {
			if !observed[session.Unix()] {
				report.MissingTimestamps = append(report.MissingTimestamps, session.Unix())
			}
		}
		report.Missing = len(report.MissingTimestamps)
		for timestamp := range observed {
			if !expectedSet[timestamp] {
				report.UnexpectedTimestamps = append(report.UnexpectedTimestamps, timestamp)
			}
		}
		sort.Slice(report.UnexpectedTimestamps, func(i, j int) bool { return report.UnexpectedTimestamps[i] < report.UnexpectedTimestamps[j] })
		report.Unexpected = len(report.UnexpectedTimestamps)
		if report.Expected > 0 {
			covered := report.Expected - report.Missing
			if covered < 0 {
				covered = 0
			}
			report.CoveragePPM = int64(math.Round(float64(covered) / float64(report.Expected) * float64(RatioScale)))
		} else {
			report.CoveragePPM = RatioScale
		}
	} else {
		report.Expected, report.CoveragePPM = report.Observed, RatioScale
		report.Warnings = append(report.Warnings, "session-calendar gap detection currently applies to daily candles; intraday continuity is checked by the provider window only")
	}
	zeroVolume := 0
	for i, candle := range candles {
		if candle.Volume == 0 {
			zeroVolume++
		}
		if i > 0 {
			move := math.Abs(math.Log(float64(candle.AdjustedClose) / float64(candles[i-1].AdjustedClose)))
			if move > 0.25 {
				report.OutlierTimestamps = append(report.OutlierTimestamps, candle.Timestamp)
			}
		}
	}
	report.Outliers = len(report.OutlierTimestamps)
	if report.Missing > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d expected %s sessions are missing", report.Missing, options.Calendar.Name()))
	}
	if report.Unexpected > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d provider candles fall outside the %s calendar", report.Unexpected, options.Calendar.Name()))
	}
	if zeroVolume > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d candles have zero volume; valid for some asset types but suspicious for US equities", zeroVolume))
	}
	if report.Outliers > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d absolute log returns exceed 25%% and require corporate-action/news review", report.Outliers))
	}
	if len(report.MissingTimestamps) > 50 {
		report.Warnings = append(report.Warnings, "missing timestamp samples truncated to 50; the missing count remains complete")
		report.MissingTimestamps = report.MissingTimestamps[:50]
	}
	if len(report.OutlierTimestamps) > 50 {
		report.Warnings = append(report.Warnings, "outlier timestamp samples truncated to 50; the outlier count remains complete")
		report.OutlierTimestamps = report.OutlierTimestamps[:50]
	}
	if len(report.UnexpectedTimestamps) > 50 {
		report.Warnings = append(report.Warnings, "unexpected timestamp samples truncated to 50; the unexpected count remains complete")
		report.UnexpectedTimestamps = report.UnexpectedTimestamps[:50]
	}
	if err := r.PutDataQualityReport(report); err != nil {
		return DataQualityReport{}, err
	}
	return report, nil
}
