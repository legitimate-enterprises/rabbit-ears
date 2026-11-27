package station_test

import (
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

var zone = time.FixedZone("local", -5*3600)

// at is 2026-11-13 (a Friday) plus days, at h:m:s local.
func at(days, h, m, s int) time.Time { return time.Date(2026, 11, 13+days, h, m, s, 0, zone) }

func TestAlign(t *testing.T) {
	for _, c := range []struct {
		timing   string
		in, want time.Time
	}{
		{station.Hour, at(0, 15, 1, 0), at(0, 16, 0, 0)},
		{station.Hour, at(0, 17, 59, 0), at(0, 18, 0, 0)},
		{station.Hour, at(0, 16, 0, 0), at(0, 16, 0, 0)},
		{station.Hour, at(0, 16, 0, 0).Add(time.Millisecond), at(0, 17, 0, 0)},
		{station.Hour, at(0, 23, 30, 0), at(1, 0, 0, 0)},
		{station.HalfHour, at(0, 15, 1, 0), at(0, 15, 30, 0)},
		{station.HalfHour, at(0, 15, 30, 0), at(0, 15, 30, 0)},
		{station.HalfHour, at(0, 15, 31, 0), at(0, 16, 0, 0)},
		{station.HalfHour, at(0, 17, 59, 0), at(0, 18, 0, 0)},
		{station.Continuous, at(0, 15, 1, 7), at(0, 15, 1, 7)},
	} {
		if got := station.Align(c.timing, c.in); !got.Equal(c.want) {
			t.Errorf("%s %v: got %v, want %v", c.timing, c.in, got, c.want)
		}
	}
}

func TestAlignAcrossTheRepeatedHour(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	// 01:30 the second time, after clocks fell back at 02:00 EDT.
	in := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC).In(ny)
	want := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	if got := station.Align(station.Hour, in); !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want.In(ny))
	}
}

func TestScheduleStart(t *testing.T) {
	friday := []time.Weekday{time.Friday}
	evening := station.Schedule{Days: friday, Times: []station.Range{{From: 18 * 60, To: 22 * 60}}}
	late := station.Schedule{Days: friday, Times: []station.Range{{From: 18*60 + 15, To: 22 * 60}}}
	overnight := station.Schedule{Days: friday, Times: []station.Range{{From: 22 * 60, To: 2 * 60}}}
	saturdays := station.Schedule{Days: []time.Weekday{time.Saturday}}
	far := at(30, 0, 0, 0)
	for _, c := range []struct {
		name     string
		s        station.Schedule
		timing   string
		in, want time.Time
	}{
		{"continuous", station.Schedule{}, station.Hour, at(0, 15, 1, 0), at(0, 16, 0, 0)},
		{"before the window", evening, station.Hour, at(0, 17, 10, 0), at(0, 18, 0, 0)},
		{"a boundary at the close is outside", evening, station.Hour, at(0, 21, 30, 0), at(7, 18, 0, 0)},
		{"half hours inside", evening, station.HalfHour, at(0, 21, 29, 0), at(0, 21, 30, 0)},
		{"half hour at the close", evening, station.HalfHour, at(0, 21, 31, 0), at(7, 18, 0, 0)},
		{"a window opening off the hour", late, station.Hour, at(0, 18, 0, 0), at(0, 19, 0, 0)},
		{"music starts at the open", late, station.Continuous, at(0, 17, 0, 0), at(0, 18, 15, 0)},
		{"a day with no times", saturdays, station.Hour, at(0, 23, 10, 0), at(1, 0, 0, 0)},
		{"past midnight belongs to the day before", overnight, station.HalfHour, at(1, 0, 30, 0), at(1, 0, 30, 0)},
		{"past midnight closes", overnight, station.Hour, at(1, 1, 10, 0), at(7, 22, 0, 0)},
	} {
		got, ok := c.s.Start(c.timing, c.in, far)
		if !ok || !got.Equal(c.want) {
			t.Errorf("%s: got %v %v, want %v", c.name, got, ok, c.want)
		}
	}
	if _, ok := evening.Start(station.Hour, at(0, 21, 30, 0), at(3, 0, 0, 0)); ok {
		t.Error("no start before until must report false")
	}
}

func TestScheduleOpen(t *testing.T) {
	evening := station.Schedule{Days: []time.Weekday{time.Friday}, Times: []station.Range{{From: 18 * 60, To: 22 * 60}}}
	if !evening.Open(at(0, 18, 0, 0)) || !evening.Open(at(0, 21, 59, 0)) || evening.Open(at(0, 22, 0, 0)) || evening.Open(at(1, 19, 0, 0)) {
		t.Error("open is [from, to) on the chosen days")
	}
	if !(station.Schedule{}).Open(at(3, 3, 3, 0)) {
		t.Error("continuous is always open")
	}
}

func row(entry, minutes int) station.Row {
	return station.Row{Key: station.Key{EntryID: uid.N(entry), ItemID: uid.N(entry * 10)}, EditionID: uid.N(entry * 100), RuntimeMs: int64(minutes) * 60000}
}

// ns are the fixture ids of entries.
func ns(entries ...int) (out []uid.ID) {
	for _, e := range entries {
		out = append(out, uid.N(e))
	}
	return out
}

type slot struct {
	entry      uid.ID
	start, end time.Time
}

func slots(ps []station.Program) (out []slot) {
	for _, p := range ps {
		out = append(out, slot{p.EntryID, p.Start, p.End})
	}
	return out
}

func entries(ps []station.Program) (out []uid.ID) {
	for _, p := range ps {
		out = append(out, p.EntryID)
	}
	return out
}

func TestFillMoviesOnTheHour(t *testing.T) {
	p := station.Plan{Timing: station.Hour}
	rows := []station.Row{row(1, 61), row(2, 119), row(3, 60)}
	got := p.Fill(rows, &station.Loop{}, nil, at(0, 15, 0, 0), at(0, 21, 0, 0))
	want := []slot{
		{uid.N(1), at(0, 15, 0, 0), at(0, 16, 1, 0)},  // ends 16:01, so the next is at 17:00
		{uid.N(2), at(0, 17, 0, 0), at(0, 18, 59, 0)}, // ends 18:59, so the next is at 19:00
		{uid.N(3), at(0, 19, 0, 0), at(0, 20, 0, 0)},  // ends on the hour, so the next is at it
		{uid.N(1), at(0, 20, 0, 0), at(0, 21, 1, 0)},
	}
	if !slices.Equal(slots(got), want) {
		t.Fatalf("got %v", slots(got))
	}
	if got[3].Loop != got[2].Loop+1 || got[3].Position != 0 || got[2].Position != 2 {
		t.Errorf("the loop wraps: %+v", got)
	}
	if got[0].EditionID != uid.N(100) || got[0].ItemID != uid.N(10) {
		t.Errorf("a program names the row's item and edition: %+v", got[0])
	}
}

func TestFillTVOnTheHalfHour(t *testing.T) {
	p := station.Plan{Timing: station.HalfHour}
	got := p.Fill([]station.Row{row(1, 22), row(2, 44)}, &station.Loop{}, nil, at(0, 15, 1, 0), at(0, 17, 0, 0))
	want := []slot{{uid.N(1), at(0, 15, 30, 0), at(0, 15, 52, 0)}, {uid.N(2), at(0, 16, 0, 0), at(0, 16, 44, 0)}}
	if !slices.Equal(slots(got), want) {
		t.Fatalf("got %v", slots(got))
	}
}

func TestFillMusicBackToBack(t *testing.T) {
	p := station.Plan{Timing: station.Continuous}
	got := p.Fill([]station.Row{row(1, 3), row(2, 4)}, &station.Loop{}, nil, at(0, 10, 0, 30), at(0, 10, 10, 0))
	want := []slot{{uid.N(1), at(0, 10, 0, 30), at(0, 10, 3, 30)}, {uid.N(2), at(0, 10, 3, 30), at(0, 10, 7, 30)}, {uid.N(1), at(0, 10, 7, 30), at(0, 10, 10, 30)}}
	if !slices.Equal(slots(got), want) {
		t.Fatalf("got %v", slots(got))
	}
}

func TestFillWindowClosingMidProgram(t *testing.T) {
	p := station.Plan{Timing: station.Hour, Schedule: station.Schedule{Times: []station.Range{{From: 18 * 60, To: 20 * 60}}}}
	got := p.Fill([]station.Row{row(1, 90), row(2, 180)}, &station.Loop{}, nil, at(0, 18, 0, 0), at(3, 0, 0, 0))
	want := []slot{
		{uid.N(1), at(0, 18, 0, 0), at(0, 19, 30, 0)}, // 20:00 is the close, so nothing starts then
		{uid.N(2), at(1, 18, 0, 0), at(1, 21, 0, 0)},  // plays to its end past the close
		{uid.N(1), at(2, 18, 0, 0), at(2, 19, 30, 0)},
	}
	if !slices.Equal(slots(got), want) {
		t.Fatalf("got %v", slots(got))
	}
}

func TestFillSkipsUnplayableAndStopsWhenNothingPlays(t *testing.T) {
	p := station.Plan{Timing: station.Continuous}
	got := p.Fill([]station.Row{row(1, 0), row(2, 5)}, &station.Loop{}, nil, at(0, 10, 0, 0), at(0, 10, 12, 0))
	if !slices.Equal(entries(got), ns(2, 2, 2)) {
		t.Errorf("a row without a runtime is skipped: %v", entries(got))
	}
	if got := p.Fill([]station.Row{row(1, 0)}, &station.Loop{}, nil, at(0, 10, 0, 0), at(1, 0, 0, 0)); len(got) != 0 {
		t.Errorf("nothing playable schedules nothing: %v", got)
	}
	if got := p.Fill(nil, &station.Loop{}, nil, at(0, 10, 0, 0), at(1, 0, 0, 0)); len(got) != 0 {
		t.Errorf("an empty play order schedules nothing: %v", got)
	}
	p.Random = true
	if got := p.Fill([]station.Row{row(1, 0)}, &station.Loop{}, nil, at(0, 10, 0, 0), at(1, 0, 0, 0)); len(got) != 0 {
		t.Errorf("nothing playable schedules nothing at random: %v", got)
	}
}

func TestFillSequentialMissesContentPlacedBefore(t *testing.T) {
	p := station.Plan{Timing: station.Continuous}
	// Entry 2 played last at position 1; entry 9 was placed before it and
	// entry 8 after it since.
	rows := []station.Row{row(9, 1), row(1, 1), row(2, 1), row(8, 1), row(3, 1)}
	cur := &station.Cursor{Key: row(2, 1).Key, Loop: 4, Position: 1}
	got := p.Fill(rows, &station.Loop{}, cur, at(0, 10, 0, 0), at(0, 10, 4, 0))
	if !slices.Equal(entries(got), ns(8, 3, 9, 1)) || got[0].Loop != 4 || got[2].Loop != 5 || got[2].Position != 0 {
		t.Fatalf("got %v %+v", entries(got), got)
	}
}

func TestFillSequentialAfterTheCursorRowIsGone(t *testing.T) {
	p := station.Plan{Timing: station.Continuous}
	cur := &station.Cursor{Key: row(2, 1).Key, Position: 1}
	got := p.Fill([]station.Row{row(1, 1), row(3, 1)}, &station.Loop{}, cur, at(0, 10, 0, 0), at(0, 10, 1, 0))
	if !slices.Equal(entries(got), ns(3)) {
		t.Fatalf("the row that took its place plays next: %v", entries(got))
	}
}

func reverse(keys []station.Key) { slices.Reverse(keys) }

func TestFillRandomKeepsEachLoop(t *testing.T) {
	p := station.Plan{Timing: station.Continuous, Random: true, Shuffle: reverse}
	rows := []station.Row{row(1, 1), row(2, 1), row(3, 1)}
	loop := &station.Loop{}
	got := p.Fill(rows, loop, nil, at(0, 10, 0, 0), at(0, 10, 4, 0))
	if !slices.Equal(entries(got), ns(3, 2, 1, 3)) || got[0].Loop != 1 || got[3].Loop != 2 || got[3].Position != 0 {
		t.Fatalf("got %v %+v", entries(got), got)
	}
	if loop.No != 2 || len(loop.Keys) != 3 || loop.Keys[0] != row(3, 1).Key {
		t.Errorf("the loop in force is kept: %+v", loop)
	}
}

func TestFillRandomAddsNewContentToTheEndOfTheLoop(t *testing.T) {
	p := station.Plan{Timing: station.Continuous, Random: true, Shuffle: reverse}
	loop := &station.Loop{No: 7, Keys: []station.Key{row(3, 1).Key, row(2, 1).Key, row(1, 1).Key}}
	cur := &station.Cursor{Key: row(3, 1).Key, Loop: 7, Position: 0}
	rows := []station.Row{row(1, 1), row(2, 1), row(3, 1), row(4, 1), row(5, 1)}
	got := p.Fill(rows, loop, cur, at(0, 10, 0, 0), at(0, 10, 5, 0))
	if !slices.Equal(entries(got), ns(2, 1, 5, 4, 5)) || got[3].Loop != 7 || got[4].Loop != 8 {
		t.Fatalf("got %v %+v", entries(got), got)
	}
}

func TestFillRandomSkipsWhatLeftAndResumesALaterLoop(t *testing.T) {
	p := station.Plan{Timing: station.Continuous, Random: true, Shuffle: reverse}
	keys := []station.Key{row(3, 1).Key, row(2, 1).Key, row(1, 1).Key}
	cur := &station.Cursor{Key: row(3, 1).Key, Loop: 1, Position: 0}
	got := p.Fill([]station.Row{row(1, 1), row(3, 1)}, &station.Loop{No: 1, Keys: slices.Clone(keys)}, cur, at(0, 10, 0, 0), at(0, 10, 1, 0))
	if !slices.Equal(entries(got), ns(1)) {
		t.Errorf("a row gone from the playlist is skipped: %v", entries(got))
	}
	// The loop stored is the one after the cursor's, written by programs
	// since removed: it plays from its start.
	got = p.Fill([]station.Row{row(1, 1), row(2, 1), row(3, 1)}, &station.Loop{No: 2, Keys: slices.Clone(keys)}, cur,
		at(0, 10, 0, 0), at(0, 10, 1, 0))
	if !slices.Equal(entries(got), ns(3)) || got[0].Loop != 2 {
		t.Errorf("got %v %+v", entries(got), got)
	}
}

func TestRating(t *testing.T) {
	for _, c := range []struct {
		kind    string
		ratings []string
		want    string
	}{
		{playlist.Movies, []string{"PG", "R", "G"}, "R"},
		{playlist.Movies, []string{"PG", ""}, ""},
		{playlist.Movies, []string{"PG", "TV-MA"}, ""},
		{playlist.Movies, nil, ""},
		{playlist.TV, []string{"TV-PG", "TV-14", "TV-Y"}, "TV-14"},
		{playlist.Music, []string{"R"}, ""},
	} {
		if got := station.Rating(c.kind, c.ratings); got != c.want {
			t.Errorf("%s %v: got %q, want %q", c.kind, c.ratings, got, c.want)
		}
	}
}

func TestDefaultTiming(t *testing.T) {
	for kind, want := range map[string]string{playlist.Movies: station.Hour, playlist.TV: station.HalfHour, playlist.Music: station.Continuous} {
		if got := station.DefaultTiming(kind); got != want {
			t.Errorf("%s: got %s, want %s", kind, got, want)
		}
	}
}

func TestRangeHoldsABoundary(t *testing.T) {
	for _, c := range []struct {
		timing   string
		from, to int
		want     bool
	}{
		{station.Hour, 20*60 + 5, 20*60 + 25, false},
		{station.Hour, 20 * 60, 20*60 + 25, true},
		{station.Hour, 20*60 + 5, 21 * 60, false},
		{station.Hour, 20*60 + 5, 21*60 + 1, true},
		{station.HalfHour, 20*60 + 5, 20*60 + 25, false},
		{station.HalfHour, 20*60 + 25, 20*60 + 35, true},
		{station.Hour, 23*60 + 45, 10, true},
		{station.Hour, 23*60 + 45, 23*60 + 55, false},
		{station.Hour, 20*60 + 5, 20*60 + 5, true},
		{station.Continuous, 20*60 + 5, 20*60 + 25, true},
	} {
		if got := (station.Range{From: c.from, To: c.to}).Holds(c.timing); got != c.want {
			t.Errorf("%s %d-%d: got %v, want %v", c.timing, c.from, c.to, got, c.want)
		}
	}
}
