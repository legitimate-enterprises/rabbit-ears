package occasion_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// nth is the nth weekday of a month, counted from its end when n < 0.
func nth(y int, m time.Month, wd time.Weekday, n int) time.Time {
	if n < 0 {
		t := day(y, m+1, 0)
		return t.AddDate(0, 0, -int((t.Weekday()-wd+7)%7))
	}
	t := day(y, m, 1)
	return t.AddDate(0, 0, int((wd-t.Weekday()+7)%7)+7*(n-1))
}

// easter is the Gregorian computus (anonymous algorithm).
func easter(y int) time.Time {
	a, b, c := y%19, y/100, y%100
	d, e := b/4, b%4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i, k := c/4, c%4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	return day(y, time.Month((h+l-7*m+114)/31), (h+l-7*m+114)%31+1)
}

func TestHolidayTable(t *testing.T) {
	rules := map[string]func(int) time.Time{
		"new-years-day":    func(y int) time.Time { return day(y, time.January, 1) },
		"valentines-day":   func(y int) time.Time { return day(y, time.February, 14) },
		"st-patricks-day":  func(y int) time.Time { return day(y, time.March, 17) },
		"easter":           easter,
		"mothers-day":      func(y int) time.Time { return nth(y, time.May, time.Sunday, 2) },
		"memorial-day":     func(y int) time.Time { return nth(y, time.May, time.Monday, -1) },
		"fathers-day":      func(y int) time.Time { return nth(y, time.June, time.Sunday, 3) },
		"independence-day": func(y int) time.Time { return day(y, time.July, 4) },
		"labor-day":        func(y int) time.Time { return nth(y, time.September, time.Monday, 1) },
		"halloween":        func(y int) time.Time { return day(y, time.October, 31) },
		"thanksgiving":     func(y int) time.Time { return nth(y, time.November, time.Thursday, 4) },
		"christmas":        func(y int) time.Time { return day(y, time.December, 25) },
		"new-years-eve":    func(y int) time.Time { return day(y, time.December, 31) },
		// The first candle, from Hebcal.
		"hanukkah": func(y int) time.Time {
			return map[int]time.Time{2026: day(2026, 12, 4), 2027: day(2027, 12, 24), 2028: day(2028, 12, 12), 2029: day(2029, 12, 1),
				2030: day(2030, 12, 20), 2031: day(2031, 12, 9), 2032: day(2032, 11, 27), 2033: day(2033, 12, 16), 2034: day(2034, 12, 6),
				2035: day(2035, 12, 25), 2036: day(2036, 12, 13)}[y]
		},
	}
	hs := occasion.Holidays()
	if len(hs) != len(rules) {
		t.Fatalf("%d holidays, want %d", len(hs), len(rules))
	}
	for _, h := range hs {
		rule, ok := rules[h.Key]
		if !ok || h.Name == "" {
			t.Errorf("unexpected holiday %+v", h)
			continue
		}
		if len(h.Dates) != 11 {
			t.Errorf("%s has %d dates", h.Key, len(h.Dates))
		}
		for i, d := range h.Dates {
			if want := rule(2026 + i); !d.Equal(want) {
				t.Errorf("%s %d: %s, want %s", h.Key, 2026+i, d.Format(time.DateOnly), want.Format(time.DateOnly))
			}
		}
	}
	for _, c := range []struct {
		key  string
		want time.Time
	}{{"easter", day(2027, time.March, 28)}, {"thanksgiving", day(2026, time.November, 26)}, {"thanksgiving", day(2027, time.November, 25)}} {
		h, _ := occasion.HolidayByKey(c.key)
		if a, ok := (&occasion.Occasion{Holiday: h.Key}).AnchorIn(c.want.Year()); !ok || !a.Equal(c.want) {
			t.Errorf("%s %d is %s", c.key, c.want.Year(), a.Format(time.DateOnly))
		}
	}
	if _, ok := occasion.HolidayByKey("festivus"); ok {
		t.Error("unknown keys are not holidays")
	}
}
