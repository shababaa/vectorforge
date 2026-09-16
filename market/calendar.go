package market

import "time"

// TradingCalendar supplies expected daily sessions. Dates returned by Sessions
// are canonical midnight UTC session dates, matching daily candle keys.
type TradingCalendar interface {
	Name() string
	IsSession(time.Time) bool
	Sessions(time.Time, time.Time) []time.Time
}

type NYSECalendar struct{ extraClosures map[string]bool }

func NewNYSECalendar(extraClosures ...time.Time) *NYSECalendar {
	c := &NYSECalendar{extraClosures: defaultNYSEClosures()}
	for _, date := range extraClosures {
		c.extraClosures[date.UTC().Format("2006-01-02")] = true
	}
	return c
}

func (c *NYSECalendar) Name() string { return "XNYS-v1" }

func (c *NYSECalendar) IsSession(value time.Time) bool {
	date := midnightUTC(value)
	if date.Weekday() == time.Saturday || date.Weekday() == time.Sunday || c.extraClosures[date.Format("2006-01-02")] {
		return false
	}
	return !nyseHolidays(date.Year())[date.Format("2006-01-02")]
}

func (c *NYSECalendar) Sessions(from, to time.Time) []time.Time {
	from, to = midnightUTC(from), midnightUTC(to)
	if to.Before(from) {
		return nil
	}
	out := []time.Time{}
	for date := from; !date.After(to); date = date.AddDate(0, 0, 1) {
		if c.IsSession(date) {
			out = append(out, date)
		}
	}
	return out
}

func midnightUTC(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func nyseHolidays(year int) map[string]bool {
	out := map[string]bool{}
	add := func(date time.Time) { out[date.Format("2006-01-02")] = true }
	newYear := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
	if newYear.Weekday() == time.Sunday {
		add(newYear.AddDate(0, 0, 1))
	} else if newYear.Weekday() != time.Saturday {
		add(newYear)
	}
	if year >= 1998 {
		add(nthWeekday(year, time.January, time.Monday, 3))
	}
	add(nthWeekday(year, time.February, time.Monday, 3))
	add(easterSunday(year).AddDate(0, 0, -2))
	add(lastWeekday(year, time.May, time.Monday))
	if year >= 2022 {
		add(observedHoliday(time.Date(year, time.June, 19, 0, 0, 0, 0, time.UTC)))
	}
	add(observedHoliday(time.Date(year, time.July, 4, 0, 0, 0, 0, time.UTC)))
	add(nthWeekday(year, time.September, time.Monday, 1))
	add(nthWeekday(year, time.November, time.Thursday, 4))
	add(observedHoliday(time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC)))
	return out
}

func observedHoliday(date time.Time) time.Time {
	switch date.Weekday() {
	case time.Saturday:
		return date.AddDate(0, 0, -1)
	case time.Sunday:
		return date.AddDate(0, 0, 1)
	default:
		return date
	}
}

func nthWeekday(year int, month time.Month, weekday time.Weekday, n int) time.Time {
	date := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	offset := (int(weekday) - int(date.Weekday()) + 7) % 7
	return date.AddDate(0, 0, offset+(n-1)*7)
}

func lastWeekday(year int, month time.Month, weekday time.Weekday) time.Time {
	date := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
	offset := (int(date.Weekday()) - int(weekday) + 7) % 7
	return date.AddDate(0, 0, -offset)
}

// Meeus/Jones/Butcher Gregorian Easter algorithm.
func easterSunday(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := (h + l - 7*m + 114) / 31
	day := (h+l-7*m+114)%31 + 1
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func defaultNYSEClosures() map[string]bool {
	return map[string]bool{
		"2001-09-11": true, "2001-09-12": true, "2001-09-13": true, "2001-09-14": true,
		"2004-06-11": true, "2007-01-02": true, "2012-10-29": true, "2012-10-30": true, "2018-12-05": true,
		"2025-01-09": true,
	}
}
