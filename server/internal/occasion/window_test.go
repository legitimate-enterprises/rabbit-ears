package occasion_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
)

func TestWindow(t *testing.T) {
	christmas := &occasion.Occasion{Holiday: "christmas", DaysBefore: 30, DaysAfter: 5}
	newYear := &occasion.Occasion{Month: 1, Day: 1, DaysBefore: 3, DaysAfter: 6}
	leap := &occasion.Occasion{Month: 2, Day: 29, DaysBefore: 1, DaysAfter: 1}
	easter := &occasion.Occasion{Holiday: "easter", DaysBefore: 2, DaysAfter: 0}
	for _, c := range []struct {
		name          string
		o             *occasion.Occasion
		on            time.Time
		active        bool
		anchor, first time.Time
		last          time.Time
		distance      int
	}{
		{"inside", christmas, day(2026, 12, 10), true, day(2026, 12, 25), day(2026, 11, 25), day(2026, 12, 30), 15},
		{"first day", christmas, day(2026, 11, 25), true, day(2026, 12, 25), day(2026, 11, 25), day(2026, 12, 30), 30},
		{"day before the first", christmas, day(2026, 11, 24), false, time.Time{}, time.Time{}, time.Time{}, 0},
		{"last day", christmas, day(2026, 12, 30), true, day(2026, 12, 25), day(2026, 11, 25), day(2026, 12, 30), 5},
		{"day after the last", christmas, day(2026, 12, 31), false, time.Time{}, time.Time{}, time.Time{}, 0},
		{"after New Year, a window begun the year before", newYear, day(2027, 1, 5), true, day(2027, 1, 1), day(2026, 12, 29), day(2027, 1, 7), 4},
		{"before New Year, next year's anchor", newYear, day(2026, 12, 30), true, day(2027, 1, 1), day(2026, 12, 29), day(2027, 1, 7), 2},
		{"a window after Christmas crosses New Year", &occasion.Occasion{Holiday: "christmas", DaysAfter: 10}, day(2027, 1, 2), true,
			day(2026, 12, 25), day(2026, 12, 25), day(2027, 1, 4), 8},
		{"Feb 29 in a leap year", leap, day(2028, 3, 1), true, day(2028, 2, 29), day(2028, 2, 28), day(2028, 3, 1), 1},
		{"Feb 29 in another year", leap, day(2027, 2, 28), false, time.Time{}, time.Time{}, time.Time{}, 0},
		{"a holiday the table has no date for", christmas, day(2037, 12, 25), false, time.Time{}, time.Time{}, time.Time{}, 0},
		{"a moving holiday", easter, day(2027, 3, 26), true, day(2027, 3, 28), day(2027, 3, 26), day(2027, 3, 28), 2},
		{"a moving holiday on last year's date", easter, day(2027, 4, 5), false, time.Time{}, time.Time{}, time.Time{}, 0},
	} {
		a, ok := c.o.On(c.on)
		if ok != c.active {
			t.Errorf("%s: active %v", c.name, ok)
			continue
		}
		if ok && (!a.Anchor.Equal(c.anchor) || !a.First.Equal(c.first) || !a.Last.Equal(c.last) || a.Distance != c.distance) {
			t.Errorf("%s: %s %s..%s %d", c.name, a.Anchor.Format(time.DateOnly), a.First.Format(time.DateOnly), a.Last.Format(time.DateOnly), a.Distance)
		}
	}
}
