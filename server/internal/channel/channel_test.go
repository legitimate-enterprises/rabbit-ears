package channel_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/channel"
)

var t0 = time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)

func at(seconds float64) time.Time { return t0.Add(time.Duration(seconds * float64(time.Second))) }

// lineup is a program of 20 s, an ident to 0:50, a program of 16 s in
// MPEG-TS and nothing after it.
func lineup() channel.Lineup {
	pieces := []*channel.Piece{
		{ID: "a", Start: at(0), Durations: []float64{6, 6, 6, 2}},
		{ID: "ident", Start: at(20), Durations: []float64{6, 6, 6, 6, 6}},
		{ID: "b", Start: at(50), Durations: []float64{8, 8}, TS: true},
	}
	return func(t time.Time) (*channel.Piece, error) {
		for _, p := range pieces {
			if t.Before(p.End()) {
				return p, nil
			}
		}
		return nil, nil
	}
}

func lines(s string) []string { return strings.Split(strings.TrimSpace(s), "\n") }

func TestJoinsAtTheSegmentOnAirAndListsAhead(t *testing.T) {
	var tl channel.Timeline
	if err := tl.Advance(at(7), lineup()); err != nil {
		t.Fatal(err)
	}
	want := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-DISCONTINUITY-SEQUENCE:0
#EXT-X-INDEPENDENT-SEGMENTS
#EXT-X-MAP:URI="a/init.mp4"
#EXTINF:6.000,
a/1.m4s
#EXTINF:6.000,
a/2.m4s
#EXTINF:2.000,
a/3.m4s
#EXT-X-DISCONTINUITY
#EXT-X-MAP:URI="ident/init.mp4"
#EXTINF:6.000,
ident/0.m4s
`
	if got := tl.Playlist(); got != want {
		t.Fatalf("%s", got)
	}
	if p := tl.OnAir(at(7)); p == nil || p.ID != "a" {
		t.Fatalf("%+v", p)
	}
}

func TestWindowSlidesAndCountsWhatItDrops(t *testing.T) {
	var tl channel.Timeline
	next := lineup()
	for s := 7.0; s <= 58; s += 3 {
		if err := tl.Advance(at(s), next); err != nil {
			t.Fatal(err)
		}
	}
	// At 0:58 the window opens at 0:28: three of a and one of the ident
	// are gone, and so is the discontinuity between them.
	got := tl.Playlist()
	for _, want := range []string{"#EXT-X-MEDIA-SEQUENCE:4\n", "#EXT-X-DISCONTINUITY-SEQUENCE:1\n", "#EXT-X-TARGETDURATION:8\n",
		"#EXT-X-MAP:URI=\"ident/init.mp4\"\n#EXTINF:6.000,\nident/1.m4s\n", "#EXT-X-DISCONTINUITY\n#EXTINF:8.000,\nb/0.ts\n#EXTINF:8.000,\nb/1.ts\n#EXT-X-ENDLIST\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "a/") || strings.Count(got, "#EXT-X-DISCONTINUITY\n") != 1 || !tl.Ended() {
		t.Fatalf("%s", got)
	}
	if p := tl.OnAir(at(58)); p == nil || p.ID != "b" {
		t.Fatalf("%+v", p)
	}
	ids := ""
	for _, p := range tl.Pieces() {
		ids += p.ID
	}
	if ids != "identb" {
		t.Fatalf("pieces in use: %s", ids)
	}
}

func TestJoiningMidIdentStartsOnItsGrid(t *testing.T) {
	var tl channel.Timeline
	if err := tl.Advance(at(33), lineup()); err != nil {
		t.Fatal(err)
	}
	got := lines(tl.Playlist())
	if got[6] != `#EXT-X-MAP:URI="ident/init.mp4"` || got[8] != "ident/2.m4s" || got[len(got)-1] != "b/0.ts" || strings.Contains(tl.Playlist(), "ENDLIST") {
		t.Fatalf("%v", got)
	}
}

func TestAPieceIsFetchedAheadOfItsStartButListedOnTime(t *testing.T) {
	var asked []time.Time
	next := lineup()
	var tl channel.Timeline
	err := tl.Advance(at(-40), func(t time.Time) (*channel.Piece, error) {
		asked = append(asked, t)
		return next(t)
	})
	if err != nil || len(asked) != 1 || strings.Contains(tl.Playlist(), "EXTINF") {
		t.Fatalf("0:40 ahead a piece is fetched, so its encoder starts, and nothing is listed: %v %v\n%s", err, asked, tl.Playlist())
	}
	if ps := tl.Pieces(); len(ps) != 1 || ps[0].ID != "a" {
		t.Fatalf("%v", ps)
	}
	if err := tl.Advance(at(-10), next); err != nil || !strings.Contains(tl.Playlist(), "a/0.m4s\n#EXTINF:6.000,\na/1.m4s\n") {
		t.Fatalf("%v\n%s", err, tl.Playlist())
	}
}

func TestAPendingPieceHoldsThePlaylistWhereItIs(t *testing.T) {
	var tl channel.Timeline
	pending := func(time.Time) (*channel.Piece, error) { return nil, channel.ErrPending }
	if err := tl.Advance(at(7), pending); err != nil || tl.Ended() || strings.Contains(tl.Playlist(), "EXTINF") {
		t.Fatalf("%v\n%s", err, tl.Playlist())
	}
	broken := errors.New("broken")
	if err := tl.Advance(at(7), func(time.Time) (*channel.Piece, error) { return nil, broken }); !errors.Is(err, broken) {
		t.Fatal(err)
	}
	if err := tl.Advance(at(7), lineup()); err != nil || !strings.Contains(tl.Playlist(), "a/1.m4s") {
		t.Fatalf("%v\n%s", err, tl.Playlist())
	}
}

func TestComingBackAfterSilenceJumpsToNowAcrossADiscontinuity(t *testing.T) {
	long := &channel.Piece{ID: "film", Start: at(0), Durations: make([]float64, 100)}
	for i := range long.Durations {
		long.Durations[i] = 6
	}
	next := func(time.Time) (*channel.Piece, error) { return long, nil }
	var tl channel.Timeline
	if err := tl.Advance(at(1), next); err != nil {
		t.Fatal(err)
	}
	if err := tl.Advance(at(301), next); err != nil {
		t.Fatal(err)
	}
	got := tl.Playlist()
	if !strings.Contains(got, "#EXT-X-MEDIA-SEQUENCE:4\n#EXT-X-DISCONTINUITY-SEQUENCE:1\n") || !strings.Contains(got, "#EXT-X-MAP:URI=\"film/init.mp4\"\n#EXTINF:6.000,\nfilm/50.m4s\n") {
		t.Fatalf("%s", got)
	}
}

func TestASegmentMostlyPastIsSkippedWhenContinuing(t *testing.T) {
	// The program ran 4 s over the ident's start: the ident's first
	// segment would repeat more of the gap than it fills.
	pieces := []*channel.Piece{{ID: "a", Start: at(0), Durations: []float64{6, 6}}, {ID: "ident", Start: at(8), Durations: []float64{6, 6, 6}}}
	var tl channel.Timeline
	err := tl.Advance(at(1), func(t time.Time) (*channel.Piece, error) {
		if t.Before(pieces[0].End()) {
			return pieces[0], nil
		}
		return pieces[1], nil
	})
	if err != nil || !strings.Contains(tl.Playlist(), "a/1.m4s\n#EXT-X-DISCONTINUITY\n#EXT-X-MAP:URI=\"ident/init.mp4\"\n#EXTINF:6.000,\nident/1.m4s\n") {
		t.Fatalf("%v\n%s", err, tl.Playlist())
	}
}

func TestAPieceWithNoSegmentsIsRefused(t *testing.T) {
	empty := &channel.Piece{ID: "a", Start: at(2)}
	var tl channel.Timeline
	if err := tl.Advance(at(0), func(time.Time) (*channel.Piece, error) { return empty, nil }); err == nil {
		t.Fatalf("listed:\n%s", tl.Playlist())
	}
}
