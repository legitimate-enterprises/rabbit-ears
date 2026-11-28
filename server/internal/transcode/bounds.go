package transcode

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

// Bound is where a copied-video segment starts: At is the source keyframe,
// in seconds.
type Bound struct{ At float64 }

// Bounds lists the segment starts of copied video in f: for each multiple
// of SegmentSeconds, the keyframe a seek there lands on, without repeats.
// Seeking reads only the container's index and one packet per slot. The
// encoder seeks just past a bound, so its runs start on these keyframes.
// Cached per file and modification time, and kept in Config.Files so they
// outlive the process.
func (m *Manager) Bounds(ctx context.Context, f *mediafile.File) ([]Bound, error) {
	key, mod, err := boundsKey(f)
	if err != nil {
		return nil, err
	}
	if b, ok := m.known(f, key, mod); ok {
		return b, nil
	}
	// The probe runs on its own clock, so a player abandoning the request
	// that started it neither kills it nor turns that into a failure for the
	// requests sharing it.
	ch := m.probes.DoChan(key, func() (any, error) {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTimeout)
		defer cancel()
		var durationMs int64
		if f.DurationMs != nil {
			durationMs = *f.DurationMs
		}
		b, err := probeBounds(pctx, f.Path, durationMs)
		if err == nil {
			m.mu.Lock()
			m.bounds[key] = b
			m.mu.Unlock()
			if m.cfg.Files != nil {
				at := make([]float64, len(b))
				for i, x := range b {
					at[i] = x.At
				}
				if perr := m.cfg.Files.ReplaceKeyframes(f.ID, mod, at); perr != nil {
					logstore.Log("playback").Warn("Keyframes not kept", "path", f.Path, "err", perr)
				}
			}
		}
		return b, err
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return r.Val.([]Bound), nil
	}
}

// BoundsKnown is Bounds without the probe: the bounds of f when an earlier
// Bounds found them, and whether one has.
func (m *Manager) BoundsKnown(f *mediafile.File) ([]Bound, bool) {
	key, mod, err := boundsKey(f)
	if err != nil {
		return nil, false
	}
	return m.known(f, key, mod)
}

func boundsKey(f *mediafile.File) (string, time.Time, error) {
	info, err := os.Stat(f.Path)
	if err != nil {
		return "", time.Time{}, err
	}
	return f.Path + "\x00" + strconv.FormatInt(info.ModTime().UnixNano(), 10), info.ModTime(), nil
}

// known looks in memory, then in Config.Files, remembering what it finds
// there.
func (m *Manager) known(f *mediafile.File, key string, mod time.Time) ([]Bound, bool) {
	m.mu.Lock()
	b, ok := m.bounds[key]
	m.mu.Unlock()
	if ok || m.cfg.Files == nil {
		return b, ok
	}
	at, err := m.cfg.Files.Keyframes(f.ID, mod)
	if err != nil {
		return nil, false
	}
	b = make([]Bound, len(at))
	for i, x := range at {
		b[i] = Bound{At: x}
	}
	m.mu.Lock()
	m.bounds[key] = b
	m.mu.Unlock()
	return b, true
}

// probeTimeout bounds one keyframe probe, which seeks once per segment.
const probeTimeout = 5 * time.Minute

func probeBounds(ctx context.Context, input string, durationMs int64) ([]Bound, error) {
	var b []Bound
	slots := max(1, int(math.Ceil(float64(durationMs)/1000/SegmentSeconds)))
	intervals := make([]string, slots)
	for k := range intervals {
		intervals[k] = fmt.Sprintf("%d%%+#1", k*SegmentSeconds)
	}
	//nolint:gosec // input is a scanned library path
	out, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-read_intervals", strings.Join(intervals, ","),
		"-select_streams", "v:0", "-show_entries", "packet=pts_time", "-of", "csv=p=0", input).Output()
	if err != nil {
		return nil, fmt.Errorf("%w: keyframes: %w", ErrEncoder, err)
	}
	lines := strings.Fields(string(out))
	if len(lines) != slots {
		return nil, fmt.Errorf("%w: keyframes: %d slots, %d packets", ErrEncoder, slots, len(lines))
	}
	for _, l := range lines {
		at, err := strconv.ParseFloat(l, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: keyframes: %q", ErrEncoder, l)
		}
		if len(b) == 0 || at > b[len(b)-1].At {
			b = append(b, Bound{At: at})
		}
	}
	return b, nil
}

// SegmentAt is the segment holding positionMs, segmented as Durations says.
func SegmentAt(bounds []Bound, positionMs int64) int {
	if bounds == nil {
		return int(positionMs / 1000 / SegmentSeconds)
	}
	pos := float64(positionMs) / 1000
	return max(0, sort.Search(len(bounds), func(i int) bool { return bounds[i].At > pos })-1)
}

// Durations are the lengths, in seconds, of the segments of a file that
// runs for durationMs. With bounds, segment i spans bounds i to i+1 and the
// first starts at zero; without, segments are SegmentSeconds long.
func Durations(bounds []Bound, durationMs int64) []float64 {
	end := float64(durationMs) / 1000
	var durs []float64
	if bounds == nil {
		for at := 0.0; at < end; at += SegmentSeconds {
			durs = append(durs, min(SegmentSeconds, end-at))
		}
	} else {
		for i := range bounds {
			next := end
			if i+1 < len(bounds) {
				next = bounds[i+1].At
			}
			from := bounds[i].At
			if i == 0 {
				from = 0
			}
			durs = append(durs, max(next-from, 0.001))
		}
	}
	return durs
}

// shortTail is the longest last segment FoldTail folds, in seconds: a frame
// or two.
const shortTail = 0.1

// FoldTail joins a last segment shorter than shortTail to the one before,
// for converted video: its encoder cuts a segment at the first frame on or
// past a boundary, and so writes none for a sliver of a file's end that
// holds no frame.
func FoldTail(durs []float64) []float64 {
	if n := len(durs); n > 1 && durs[n-1] < shortTail {
		durs[n-2] += durs[n-1]
		return durs[:n-1]
	}
	return durs
}

// Playlist lists every segment up front so players can seek anywhere, each
// as long as durs says. MPEG-TS segments, when ts, need no init segment.
func Playlist(durs []float64, ts bool) string {
	target := SegmentSeconds
	for _, d := range durs {
		target = max(target, int(math.Ceil(d)))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:%d\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXT-X-INDEPENDENT-SEGMENTS\n", target)
	if !ts {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", InitName)
	}
	for i, d := range durs {
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%s\n", d, segmentName(i, ts))
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// boxes calls fn with each ISO BMFF box in b: its type and the whole box.
func boxes(b []byte, fn func(typ string, box []byte) bool) {
	for len(b) >= 8 {
		size, hdr := uint64(binary.BigEndian.Uint32(b)), uint64(8)
		switch size {
		case 0:
			size = uint64(len(b))
		case 1:
			if len(b) < 16 {
				return
			}
			size, hdr = binary.BigEndian.Uint64(b[8:]), 16
		}
		if size < hdr || size > uint64(len(b)) {
			return
		}
		if !fn(string(b[4:8]), b[:size]) {
			return
		}
		b = b[size:]
	}
}

// child returns the payload of the first box of typ inside container box.
func child(box []byte, hdr int, typ string) []byte {
	var out []byte
	boxes(box[hdr:], func(t string, c []byte) bool {
		if t == typ {
			out = c
		}
		return out == nil
	})
	return out
}

// timescale is the first track's media timescale in an init segment.
func timescale(init []byte) uint32 {
	var mdhd []byte
	boxes(init, func(t string, box []byte) bool {
		if t != "moov" {
			return true
		}
		mdhd = box
		for _, c := range []string{"trak", "mdia", "mdhd"} {
			if mdhd = child(mdhd, 8, c); mdhd == nil {
				break
			}
		}
		return false
	})
	off := 8 + 4 + 8 // header, version and flags, creation and modification times
	if len(mdhd) > 8 && mdhd[8] == 1 {
		off = 8 + 4 + 16
	}
	if len(mdhd) < off+4 {
		return 0
	}
	return binary.BigEndian.Uint32(mdhd[off:])
}

// decodeTime is the first track fragment's decode time in a media segment.
func decodeTime(seg []byte) (uint64, bool) {
	var tfdt []byte
	boxes(seg, func(t string, box []byte) bool {
		if t == "moof" {
			if traf := child(box, 8, "traf"); traf != nil {
				tfdt = child(traf, 8, "tfdt")
			}
		}
		return t != "moof"
	})
	switch {
	case len(tfdt) >= 20 && tfdt[8] == 1:
		return binary.BigEndian.Uint64(tfdt[12:]), true
	case len(tfdt) >= 16:
		return uint64(binary.BigEndian.Uint32(tfdt[12:])), true
	}
	return 0, false
}

// firstPTS is the presentation time, in 90 kHz ticks, of the first video
// PES packet in an MPEG-TS segment.
func firstPTS(ts []byte) (uint64, bool) {
	for i := 0; i+188 <= len(ts); i += 188 {
		p := ts[i : i+188]
		// Sync byte, payload unit start, and a payload.
		if p[0] != 0x47 || p[1]&0x40 == 0 || p[3]&0x10 == 0 {
			continue
		}
		off := 4
		if p[3]&0x20 != 0 {
			off += 1 + int(p[4])
		}
		if off+14 > len(p) {
			continue
		}
		h := p[off:]
		// A PES start code, a video stream id, and a PTS.
		if h[0] != 0 || h[1] != 0 || h[2] != 1 || h[3]&0xf0 != 0xe0 || h[7]&0x80 == 0 {
			continue
		}
		return uint64(h[9]>>1&7)<<30 | uint64(h[10])<<22 | uint64(h[11]>>1)<<15 | uint64(h[12])<<7 | uint64(h[13]>>1), true
	}
	return 0, false
}

// fragments keeps a media segment's moof and mdat boxes, dropping the
// segment-level styp and sidx so several can be joined into one segment.
func fragments(seg []byte) []byte {
	var out []byte
	boxes(seg, func(t string, box []byte) bool {
		if t == "moof" || t == "mdat" {
			out = append(out, box...)
		}
		return true
	})
	return out
}
