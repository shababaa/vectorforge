package market

import (
	"math"
	"time"
)

// PredictionDemoCandles is a deterministic, synthetic multi-regime fixture.
// It is not real market history and must never be used to claim trading alpha.
func PredictionDemoCandles() []Candle {
	calendar := NewNYSECalendar()
	date := time.Date(2022, 1, 3, 0, 0, 0, 0, time.UTC)
	price := 100.0
	state := uint64(42)
	rows := make([]Candle, 0, 600)
	for len(rows) < 600 {
		if calendar.IsSession(date) {
			i := len(rows)
			state = state*6364136223846793005 + 1442695040888963407
			noise := (float64(state>>11)/float64(uint64(1)<<53) - .5) * .012
			drift := .0008
			if i >= 200 && i < 400 {
				drift = -.0006
			}
			open := price
			price *= math.Exp(drift + .004*math.Sin(float64(i)/9) + noise)
			o, _ := ScalePrice(open)
			c, _ := ScalePrice(price)
			hi, _ := ScalePrice(math.Max(open, price) * 1.003)
			lo, _ := ScalePrice(math.Min(open, price) * .997)
			rows = append(rows, Candle{Symbol: "SYNTH", Interval: "1d", Timestamp: date.Unix(), Open: o, High: hi, Low: lo, Close: c, AdjustedClose: c, Volume: 1_000_000 + int64(i%17)*25_000, Source: "synthetic-phase3-v1"})
		}
		date = date.AddDate(0, 0, 1)
	}
	return rows
}
