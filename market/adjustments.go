package market

import (
	"errors"
	"fmt"
	"math/big"
	"sort"
)

// RebuildSplitAdjustedCloses adjusts intraday closes using stored split events.
// Twelve Data daily/weekly/monthly prices are already split-adjusted, so those
// intervals are deliberately left untouched to prevent double adjustment.
func (r *Repository) RebuildSplitAdjustedCloses(symbol, interval string, from, to int64) (int, error) {
	var err error
	if symbol, err = normalizeSymbol(symbol); err != nil {
		return 0, err
	}
	if interval, err = validateInterval(interval); err != nil {
		return 0, err
	}
	if interval == "1d" || interval == "1wk" || interval == "1mo" {
		return 0, nil
	}
	if from <= 0 || to < from {
		return 0, fmt.Errorf("%w: adjustment range is invalid", ErrInvalidMarketData)
	}
	candles, err := r.Candles(CandleQuery{Symbol: symbol, Interval: interval, From: from, To: to, Limit: 100_000})
	if err != nil {
		return 0, err
	}
	actions, err := r.CorporateActions(symbol, from, to, 10_000)
	if err != nil {
		return 0, err
	}
	splits := make([]CorporateAction, 0, len(actions))
	for _, action := range actions {
		if action.ActionType == CorporateActionSplit {
			splits = append(splits, action)
		}
	}
	sort.Slice(splits, func(i, j int) bool { return splits[i].ExDate < splits[j].ExDate })
	changed := make([]Candle, 0)
	for _, candle := range candles {
		numerator, denominator := big.NewInt(1), big.NewInt(1)
		for _, split := range splits {
			if candle.Timestamp < split.ExDate {
				numerator.Mul(numerator, big.NewInt(split.SplitTo))
				denominator.Mul(denominator, big.NewInt(split.SplitFrom))
			}
		}
		adjusted, err := scaledRatio(candle.Close, numerator, denominator)
		if err != nil {
			return 0, err
		}
		if adjusted != candle.AdjustedClose {
			candle.AdjustedClose = adjusted
			changed = append(changed, candle)
		}
	}
	for offset := 0; offset < len(changed); offset += 500 {
		end := min(offset+500, len(changed))
		if _, err := r.IngestCandles(changed[offset:end]); err != nil {
			return 0, err
		}
	}
	return len(changed), nil
}

func scaledRatio(value int64, numerator, denominator *big.Int) (int64, error) {
	if denominator.Sign() <= 0 {
		return 0, errors.New("split adjustment denominator is not positive")
	}
	result := new(big.Int).Mul(big.NewInt(value), numerator)
	result.Add(result, new(big.Int).Quo(new(big.Int).Set(denominator), big.NewInt(2)))
	result.Quo(result, denominator)
	if !result.IsInt64() || result.Sign() <= 0 {
		return 0, fmt.Errorf("%w: adjusted price overflow", ErrInvalidMarketData)
	}
	return result.Int64(), nil
}
