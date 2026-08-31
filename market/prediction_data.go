package market

import (
	"fmt"
	"math"
)

const PredictionVersion = "prediction-v1"

var predictionFeatureNames = []string{"return_1", "return_5", "close_over_sma_5", "close_over_sma_20", "volatility_20", "rsi_14", "relative_volume_20"}

// PredictionConfig fixes the complete experiment before the test set is read.
// Fractions partition origins chronologically; validation is divided equally
// into tuning and calibration. HorizonBars is observed bars, not elapsed days.
type PredictionConfig struct {
	HorizonBars        int     `json:"horizon_bars"`
	TrainFraction      float64 `json:"train_fraction"`
	ValidationFraction float64 `json:"validation_fraction"`
	RefitEvery         int     `json:"refit_every"`
	TrainWindow        int     `json:"train_window"` // zero means expanding history
	Coverage           float64 `json:"nominal_coverage"`
}

func DefaultPredictionConfig() PredictionConfig {
	return PredictionConfig{HorizonBars: 1, TrainFraction: .6, ValidationFraction: .2, RefitEvery: 63, Coverage: .9}
}

func (c PredictionConfig) validate() error {
	if c.HorizonBars < 1 || c.HorizonBars > 20 || !finite(c.TrainFraction) || !finite(c.ValidationFraction) || !finite(c.Coverage) || c.TrainFraction < .3 || c.ValidationFraction < .1 || c.TrainFraction+c.ValidationFraction > .9 || c.RefitEvery < 1 || c.TrainWindow < 0 || c.Coverage < .5 || c.Coverage >= 1 {
		return fmt.Errorf("%w: invalid prediction configuration", ErrInvalidMarketData)
	}
	return nil
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

type predictionSample struct {
	AsOf, Target  int64
	Close, Actual int64
	X             []float64
	Y             float64 // future log return; never exposed to Predict
	SMA5, SMA20   int64
	Regime        string
}

func predictionSamples(candles []Candle, horizon int) ([]predictionSample, error) {
	if horizon < 1 || horizon > 20 || len(candles) > 20_000 {
		return nil, fmt.Errorf("%w: horizon must be 1..20 and dataset at most 20000 candles", ErrInvalidMarketData)
	}
	for i, c := range candles {
		if c.Symbol == "" || c.Interval == "" || c.Timestamp <= 0 || c.Close <= 0 || c.AdjustedClose <= 0 || c.Volume < 0 || c.Open <= 0 || c.High < max(c.Open, c.Close) || c.Low > min(c.Open, c.Close) || c.Low <= 0 {
			return nil, fmt.Errorf("%w: invalid prediction candle %d", ErrInvalidMarketData, i)
		}
		if i > 0 && (c.Timestamp <= candles[i-1].Timestamp || c.Symbol != candles[0].Symbol || c.Interval != candles[0].Interval || c.Source != candles[0].Source) {
			return nil, fmt.Errorf("%w: prediction dataset must be strictly ordered, single-symbol, single-interval, and single-source", ErrInvalidMarketData)
		}
	}
	rows := make([]predictionSample, 0, max(0, len(candles)-20-horizon))
	for i := 20; i+horizon < len(candles); i++ {
		f := featureFromWindow(candles[i-20:i+1], DefaultFeatureSet, 0)
		c, next := candles[i], candles[i+horizon]
		x := predictionInputs(c, f)
		regime := "flat"
		if x[1] > .01 {
			regime = "uptrend"
		} else if x[1] < -.01 {
			regime = "downtrend"
		}
		rows = append(rows, predictionSample{AsOf: c.Timestamp, Target: next.Timestamp, Close: c.AdjustedClose, Actual: next.AdjustedClose, X: x, Y: math.Log(float64(next.AdjustedClose) / float64(c.AdjustedClose)), SMA5: f.SMA5, SMA20: f.SMA20, Regime: regime})
	}
	return rows, nil
}

func predictionInputs(c Candle, f FeatureSnapshot) []float64 {
	volume := 0.0
	if f.VolumeSMA20 > 0 {
		volume = float64(c.Volume)/float64(f.VolumeSMA20) - 1
	}
	return []float64{float64(f.Return1PPM) / float64(RatioScale), float64(f.Return5PPM) / float64(RatioScale), float64(c.AdjustedClose)/float64(f.SMA5) - 1, float64(c.AdjustedClose)/float64(f.SMA20) - 1, float64(f.Volatility20PPM) / float64(RatioScale), float64(f.RSI14PPM) / float64(RatioScale), volume}
}

type TimePartition struct {
	Count       int   `json:"count"`
	FirstOrigin int64 `json:"first_origin"`
	LastOrigin  int64 `json:"last_origin"`
	LastTarget  int64 `json:"last_target"`
}

type PredictionSplit struct {
	Train       TimePartition `json:"train"`
	Tune        TimePartition `json:"tune"`
	Calibration TimePartition `json:"calibration"`
	Test        TimePartition `json:"test"`
	Purged      int           `json:"purged"`
}

type sampleSplit struct {
	train, tune, calibration, test []predictionSample
	report                         PredictionSplit
}

func partition(rows []predictionSample) TimePartition {
	if len(rows) == 0 {
		return TimePartition{}
	}
	return TimePartition{len(rows), rows[0].AsOf, rows[len(rows)-1].AsOf, rows[len(rows)-1].Target}
}

// Purge labels whose targets meet or cross the next partition's first origin.
func beforeOrigin(rows []predictionSample, origin int64) []predictionSample {
	n := 0
	for n < len(rows) && rows[n].Target < origin {
		n++
	}
	return rows[:n]
}

func splitPredictionSamples(rows []predictionSample, c PredictionConfig) (sampleSplit, error) {
	if err := c.validate(); err != nil {
		return sampleSplit{}, err
	}
	n := len(rows)
	a := int(float64(n) * c.TrainFraction)
	b := a + int(float64(n)*c.ValidationFraction)
	mid := a + (b-a)/2
	if a < 1 || mid <= a || b <= mid || b >= n {
		return sampleSplit{}, ErrInsufficientHistory
	}
	s := sampleSplit{train: beforeOrigin(rows[:a], rows[a].AsOf), tune: beforeOrigin(rows[a:mid], rows[mid].AsOf), calibration: beforeOrigin(rows[mid:b], rows[b].AsOf), test: rows[b:]}
	if len(s.train) < 40 || len(s.tune) < 10 || len(s.calibration) < 10 || len(s.test) < 10 {
		return sampleSplit{}, fmt.Errorf("%w: need 40 training and 10 each tuning/calibration/test samples after label purging", ErrInsufficientHistory)
	}
	if (len(s.test)+c.RefitEvery-1)/c.RefitEvery > 100 {
		return sampleSplit{}, fmt.Errorf("%w: at most 100 walk-forward folds; increase refit_every", ErrInvalidMarketData)
	}
	if c.TrainWindow > 0 && c.TrainWindow < 40+len(s.calibration)+c.HorizonBars {
		return sampleSplit{}, fmt.Errorf("%w: train_window is too short for fit and calibration", ErrInvalidMarketData)
	}
	s.report = PredictionSplit{partition(s.train), partition(s.tune), partition(s.calibration), partition(s.test), n - len(s.train) - len(s.tune) - len(s.calibration) - len(s.test)}
	return s, nil
}
