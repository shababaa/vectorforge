package market

import "time"

// AnalystDemoCandles reuses the deterministic price fixture on 600 actual
// calendar sessions ending before today's UTC date. Source remains synthetic.
func AnalystDemoCandles(now time.Time) []Candle {
	calendar := NewNYSECalendar()
	end := midnightUTC(now).AddDate(0, 0, -1)
	dates := calendar.Sessions(end.AddDate(-4, 0, 0), end)
	rows := PredictionDemoCandles()
	dates = dates[len(dates)-len(rows):]
	for i := range rows {
		rows[i].Timestamp = dates[i].Unix()
		rows[i].Source = "synthetic-analyst-v1"
	}
	return rows
}
