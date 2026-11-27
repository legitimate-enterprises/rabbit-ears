package station

import (
	"math/rand/v2"
	"slices"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// The arithmetic here is in the location of the times it is given, which
// the service makes the server's local time: hours, half hours, days and
// time ranges are the server's.

// Range is one time range of a day, in minutes after local midnight. A To
// at or before From runs past midnight into the next day, so a range
// belongs to the day it starts on. ID is its stored row's, zero until
// written.
type Range struct {
	ID       uid.ID
	From, To int
}

// Schedule limits when programs may start: on Days (every day when empty),
// within Times (all day when empty). Both empty is continuous.
type Schedule struct {
	Days  []time.Weekday
	Times []Range
}

// Continuous reports a schedule with no limit.
func (s Schedule) Continuous() bool { return len(s.Days) == 0 && len(s.Times) == 0 }

// at reports whether t falls inside a window of s and, when it does not,
// when the next window opens (zero when none does within a week).
func (s Schedule) at(t time.Time) (bool, time.Time) {
	if s.Continuous() {
		return true, time.Time{}
	}
	times := s.Times
	if len(times) == 0 {
		times = []Range{{From: 0, To: 0}}
	}
	var next time.Time
	y, m, d := t.Date()
	// A day earlier for a range that runs past midnight into t's day.
	for off := -1; off <= 7; off++ {
		if len(s.Days) > 0 && !slices.Contains(s.Days, time.Date(y, m, d+off, 12, 0, 0, 0, t.Location()).Weekday()) {
			continue
		}
		for _, r := range times {
			open := time.Date(y, m, d+off, 0, r.From, 0, 0, t.Location())
			endDay := d + off
			if r.To <= r.From {
				endDay++
			}
			end := time.Date(y, m, endDay, 0, r.To, 0, 0, t.Location())
			if !t.Before(open) && t.Before(end) {
				return true, time.Time{}
			}
			if open.After(t) && (next.IsZero() || open.Before(next)) {
				next = open
			}
		}
	}
	return false, next
}

// Open reports whether t falls inside a window of s.
func (s Schedule) Open(t time.Time) bool {
	in, _ := s.at(t)
	return in
}

// Holds reports whether a program can start inside r under timing, which
// takes the first boundary at or after r's start to lie within r. The day
// is arbitrary: only the clock and the length of the range matter.
func (r Range) Holds(timing string) bool {
	from := time.Date(2000, 1, 1, 0, r.From, 0, 0, time.Local)
	return Schedule{Times: []Range{r}}.Open(Align(timing, from))
}

// Timings: when a program starts after the one before ends.
const (
	Continuous = "continuous"
	HalfHour   = "half_hour"
	Hour       = "hour"
)

// DefaultTiming is a new station's timing for kind.
func DefaultTiming(kind string) string {
	switch kind {
	case playlist.Movies:
		return Hour
	case playlist.TV:
		return HalfHour
	}
	return Continuous
}

// Align is the first boundary at or after t on which a program may start
// under timing.
func Align(timing string, t time.Time) time.Time {
	y, m, d := t.Date()
	h, mins, _ := t.Clock()
	var a time.Time
	step := time.Hour
	switch timing {
	case Hour:
		a = time.Date(y, m, d, h, 0, 0, 0, t.Location())
	case HalfHour:
		a, step = time.Date(y, m, d, h, mins-mins%30, 0, 0, t.Location()), 30*time.Minute
	default:
		return t
	}
	// Stepping instead of building the next boundary keeps a repeated or
	// skipped local hour from landing before t.
	for a.Before(t) {
		a = a.Add(step)
	}
	return a
}

// Start is the first instant at or after t at which a program may start
// under timing and s, and false when there is none before until.
func (s Schedule) Start(timing string, t, until time.Time) (time.Time, bool) {
	for {
		a := Align(timing, t)
		if !a.Before(until) {
			return a, false
		}
		in, next := s.at(a)
		if in {
			return a, true
		}
		if next.IsZero() {
			return a, false
		}
		t = next
	}
}

// Key names one row of a play order across reads. It is a map key, so a
// program with no entry left has a zero EntryID, stored as NULL.
type Key struct{ EntryID, ItemID uid.ID }

// Row is one row of a play order.
type Row struct {
	Key
	EditionID uid.ID
	RuntimeMs int64
}

// Cursor is the last program scheduled: its row, the loop it was in and its
// position there.
type Cursor struct {
	Key
	Loop, Position int
}

// Loop is a randomized station's current loop: its number and its keys in
// the order they play.
type Loop struct {
	No   int
	Keys []Key
}

// Program is one row scheduled from Start to End.
type Program struct {
	Row
	Loop, Position int
	Start, End     time.Time
}

// Plan is how a station schedules.
type Plan struct {
	Timing   string
	Schedule Schedule
	Random   bool
	// Shuffle orders keys in place; nil shuffles at random.
	Shuffle func([]Key)
}

// Fill schedules programs from rows, starting no earlier than from and
// stopping before the first that would start at or after until. cur is the
// program before from, nil to start a new loop. A row without a runtime is
// skipped. A random plan reads and rewrites loop.
func (p Plan) Fill(rows []Row, loop *Loop, cur *Cursor, from, until time.Time) []Program {
	var out []Program
	playable := map[Key]Row{}
	if p.Random {
		for _, r := range rows {
			if r.RuntimeMs > 0 {
				playable[r.Key] = r
			}
		}
	}
	for t := from; ; {
		start, ok := p.Schedule.Start(p.Timing, t, until)
		if !ok {
			return out
		}
		var r Row
		var no, pos int
		if p.Random {
			r, no, pos, ok = p.atRandom(rows, playable, loop, cur, len(out) == 0)
		} else {
			r, no, pos, ok = p.inOrder(rows, cur)
		}
		if !ok {
			return out
		}
		prog := Program{Row: r, Loop: no, Position: pos, Start: start, End: start.Add(time.Duration(r.RuntimeMs) * time.Millisecond)}
		out = append(out, prog)
		cur, t = &Cursor{Key: r.Key, Loop: no, Position: pos}, prog.End
	}
}

// inOrder is the row after cur in the playlist's order. Rows now before
// cur's are missed for its loop; when cur's row is gone, the row now at its
// position follows.
func (p Plan) inOrder(rows []Row, cur *Cursor) (Row, int, int, bool) {
	if len(rows) == 0 {
		return Row{}, 0, 0, false
	}
	idx, no := 0, 0
	if cur != nil {
		idx, no = cur.Position, cur.Loop
		if i := slices.IndexFunc(rows, func(r Row) bool { return r.Key == cur.Key }); i >= 0 {
			idx = i + 1
		}
	}
	for range len(rows) + 1 {
		if idx >= len(rows) {
			idx, no = 0, no+1
		}
		if rows[idx].RuntimeMs > 0 {
			return rows[idx], no, idx, true
		}
		idx++
	}
	return Row{}, 0, 0, false
}

// atRandom is the next row of loop after cur, playable being the rows with
// a runtime. On a Fill's first call, rows the loop lacks join its end; once
// a call returns, the loop holds every row. Keys no longer in rows are
// skipped; a finished loop is followed by a new shuffle of every row. A
// loop numbered past cur's was written by programs since removed, and plays
// from its start.
func (p Plan) atRandom(rows []Row, playable map[Key]Row, loop *Loop, cur *Cursor, first bool) (Row, int, int, bool) {
	if len(playable) == 0 {
		return Row{}, 0, 0, false
	}
	idx := 0
	if cur == nil || len(loop.Keys) == 0 {
		p.reshuffle(rows, loop)
	} else {
		if first {
			known := map[Key]bool{}
			for _, k := range loop.Keys {
				known[k] = true
			}
			var added []Key
			for _, r := range rows {
				if !known[r.Key] {
					added, known[r.Key] = append(added, r.Key), true
				}
			}
			p.shuffle(added)
			loop.Keys = append(loop.Keys, added...)
		}
		if cur.Loop == loop.No {
			idx = cur.Position + 1
		}
	}
	for range 2 {
		for ; idx < len(loop.Keys); idx++ {
			if r, ok := playable[loop.Keys[idx]]; ok {
				return r, loop.No, idx, true
			}
		}
		p.reshuffle(rows, loop)
		idx = 0
	}
	return Row{}, 0, 0, false
}

func (p Plan) shuffle(keys []Key) {
	if p.Shuffle != nil {
		p.Shuffle(keys)
		return
	}
	rand.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] }) //nolint:gosec // a play order needs no unpredictability
}

// reshuffle starts the next loop over every row.
func (p Plan) reshuffle(rows []Row, loop *Loop) {
	loop.Keys = loop.Keys[:0]
	for _, r := range rows {
		loop.Keys = append(loop.Keys, r.Key)
	}
	p.shuffle(loop.Keys)
	loop.No++
}

// Rating is the highest of ratings on the scale of kind: MPAA for movies,
// TV Parental Guidelines for tv, none for music. A rating off the scale, an
// unrated item ("") among them, outranks every rating, so the station has
// none and no kid sees it; so does an empty list.
func Rating(kind string, ratings []string) string {
	scale := map[string][]string{playlist.Movies: account.MovieRatings, playlist.TV: account.TVRatings}[kind]
	top := -1
	for _, r := range ratings {
		i := slices.Index(scale, r)
		if i < 0 {
			return ""
		}
		top = max(top, i)
	}
	if top < 0 {
		return ""
	}
	return scale[top]
}
