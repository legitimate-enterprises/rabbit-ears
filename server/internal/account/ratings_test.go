package account_test

import (
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
)

func TestAge(t *testing.T) {
	d := func(s string) time.Time { v, _ := time.Parse(time.DateOnly, s); return v }
	for _, c := range []struct {
		bday, now string
		want      int
	}{
		{"2018-05-01", "2026-04-30", 7},
		{"2018-05-01", "2026-05-01", 8},
		{"2016-02-29", "2026-02-28", 9},
		{"2016-02-29", "2026-03-01", 10},
		{"2027-01-01", "2026-01-01", 0},
	} {
		if got := account.Age(d(c.bday), d(c.now)); got != c.want {
			t.Errorf("%s at %s = %d, want %d", c.bday, c.now, got, c.want)
		}
	}
}

func TestSuggestCeilings(t *testing.T) {
	for _, c := range []struct {
		age       int
		movie, tv string
	}{{3, "G", "TV-Y"}, {7, "PG", "TV-Y7"}, {9, "PG", "TV-Y7"}, {10, "PG", "TV-G"}, {12, "PG-13", "TV-PG"}, {14, "PG-13", "TV-14"}, {17, "R", "TV-MA"}} {
		if m, tv := account.SuggestCeilings(c.age); m != c.movie || tv != c.tv {
			t.Errorf("%d: %s %s", c.age, m, tv)
		}
	}
}

func TestAtOrBelow(t *testing.T) {
	if got := account.AtOrBelow(account.MovieRatings, "PG"); !slices.Equal(got, []string{"G", "PG"}) {
		t.Fatal(got)
	}
	if got := account.AtOrBelow(account.TVRatings, ""); len(got) != 0 {
		t.Fatal(got)
	}
}
