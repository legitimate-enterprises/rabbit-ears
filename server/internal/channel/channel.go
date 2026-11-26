// Package channel writes a station's continuous stream: one live HLS media
// playlist whose segments come from one piece after another, a program or
// the ident between two, each on the clock where the schedule put it. It is
// pure: a clock and a lineup in, a playlist out.
package channel

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// Ahead is how far past now segments are listed. A player starts three
	// segments from a live playlist's end, which is then about now.
	Ahead = 18 * time.Second
	// Back is how long a segment stays listed after it has aired.
	Back = 30 * time.Second
	// Lead is how long before a piece is listed it is asked for, so its
	// encoder has its first segments in time.
	Lead = 30 * time.Second
)

// ErrPending is a Lineup's answer while the piece that airs next is still
// being prepared; the playlist stays as it is until it is ready.
var ErrPending = errors.New("the next piece is not ready")

// Piece is one source of segments on air from Start: segment n is
// Durations[n] seconds long and starts where segment n-1 ends. ID names it
// in the playlist's URLs, and two pieces with one ID are the same.
type Piece struct {
	ID        string
	Start     time.Time
	Durations []float64
	TS        bool // MPEG-TS segments, which have no init segment
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// End is when the piece's last segment ends.
func (p *Piece) End() time.Time {
	var total float64
	for _, d := range p.Durations {
		total += d
	}
	return p.Start.Add(seconds(total))
}

// At is the segment on air at t and when it starts; the last one when t is
// past them all.
func (p *Piece) At(t time.Time) (int, time.Time) {
	start := p.Start
	for i, d := range p.Durations {
		if end := start.Add(seconds(d)); t.Before(end) || i == len(p.Durations)-1 {
			return i, start
		}
		start = start.Add(seconds(d))
	}
	return 0, start
}

// Lineup answers the piece on air at t, or the next to air when none is: a
// piece that ends after t. Nil means nothing more airs and the stream ends.
type Lineup func(t time.Time) (*Piece, error)

type segment struct {
	piece      *Piece
	n          int
	start, end time.Time
	// disc counts the discontinuities up to this segment since the
	// timeline began.
	disc int
}

// Timeline is one listener's sliding window on a channel. The zero value
// joins at the first Advance.
type Timeline struct {
	segs []segment
	// seq is the media sequence number of segs[0], or of the next segment
	// when there is none.
	seq    int
	last   *segment  // the newest segment ever listed
	cursor time.Time // when the next segment to list airs
	ahead  *Piece    // the piece that airs at cursor, once asked for
	target int
	ended  bool
}

// Advance slides the window to now: it drops what aired more than Back ago
// and lists what airs before now plus Ahead, asking next for each piece
// Lead before that. A listener back after a silence longer than the window
// joins again at now. Once next answers nil the stream has ended for good.
func (t *Timeline) Advance(now time.Time, next Lineup) error {
	for len(t.segs) > 0 && !t.segs[0].end.After(now.Add(-Back)) {
		t.segs = t.segs[1:]
		t.seq++
	}
	if len(t.segs) == 0 && t.cursor.Before(now) {
		t.cursor = now
	}
	for !t.ended && t.cursor.Before(now.Add(Ahead+Lead)) {
		p, err := next(t.cursor)
		due := t.cursor.Before(now.Add(Ahead))
		if errors.Is(err, ErrPending) || err != nil && !due {
			return nil
		}
		if err != nil {
			return err
		}
		if p == nil {
			t.ended, t.ahead = true, nil
			break
		}
		if len(p.Durations) == 0 || !p.End().After(t.cursor) {
			return fmt.Errorf("channel: piece %s has no segment after %s", p.ID, t.cursor)
		}
		t.ahead = p
		if p.Start.After(t.cursor) {
			t.cursor = p.Start
		}
		if !t.cursor.Before(now.Add(Ahead)) {
			break
		}
		i, start := p.At(t.cursor)
		// Continuing from another segment, one that is mostly past is left
		// out: the stream then runs short by less than it would run long.
		if d := seconds(p.Durations[i]); t.last != nil && t.cursor.Sub(start) > d/2 {
			i, start = i+1, start.Add(d)
		}
		if i >= len(p.Durations) {
			t.cursor = p.End()
			continue
		}
		s := segment{piece: p, n: i, start: start, end: start.Add(seconds(p.Durations[i]))}
		if t.last != nil {
			s.disc = t.last.disc
			if t.last.piece.ID != p.ID || t.last.n+1 != i {
				s.disc++
			}
		}
		t.segs = append(t.segs, s)
		t.last, t.cursor = &s, s.end
		t.target = max(t.target, int(math.Ceil(p.Durations[i])))
	}
	return nil
}

// Ended reports that the lineup has nothing more: the playlist lists the
// stream's last segments and closes.
func (t *Timeline) Ended() bool { return t.ended }

// Pieces are the pieces the window lists and the one asked for ahead, in
// the order they air.
func (t *Timeline) Pieces() []*Piece {
	var out []*Piece
	add := func(p *Piece) {
		if p != nil && (len(out) == 0 || out[len(out)-1].ID != p.ID) {
			out = append(out, p)
		}
	}
	for _, s := range t.segs {
		add(s.piece)
	}
	add(t.ahead)
	return out
}

// OnAir is the piece whose segment airs at now, else the next listed.
func (t *Timeline) OnAir(now time.Time) *Piece {
	for _, s := range t.segs {
		if now.Before(s.end) {
			return s.piece
		}
	}
	return nil
}

func (s segment) name() string {
	ext := ".m4s"
	if s.piece.TS {
		ext = ".ts"
	}
	return fmt.Sprintf("%s/%d%s", s.piece.ID, s.n, ext)
}

// Playlist is the live media playlist of the window. The target duration
// only grows, with the longest segment listed so far.
func (t *Timeline) Playlist() string {
	disc := 0
	if len(t.segs) > 0 {
		disc = t.segs[0].disc
	} else if t.last != nil {
		disc = t.last.disc
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXT-X-DISCONTINUITY-SEQUENCE:%d\n#EXT-X-INDEPENDENT-SEGMENTS\n",
		max(t.target, 1), t.seq, disc)
	for i, s := range t.segs {
		fresh := i == 0 || t.segs[i-1].disc != s.disc
		if i > 0 && fresh {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if fresh && !s.piece.TS {
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s/init.mp4\"\n", s.piece.ID)
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", s.piece.Durations[s.n], s.name())
	}
	if t.ended {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return b.String()
}
