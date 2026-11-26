package transcode

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// gopClip is 80 s of H.264 with a keyframe every 4.3 s, off the segment grid.
func gopClip(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "gop.mp4")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-f", "lavfi", "-i", "sine", "-t", "80", //nolint:gosec // temp path
		"-c:v", "libx264", "-preset", "ultrafast", "-x264-params", "scenecut=0:keyint=1000", "-force_key_frames", "expr:gte(t,n_forced*4.3)",
		"-c:a", "aac", clip).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return clip
}

func TestBoundsAreTheKeyframesSeeksLandOn(t *testing.T) {
	m := New(Config{Root: t.TempDir()})
	clip := gopClip(t)
	if _, ok := m.BoundsKnown(clip); ok {
		t.Fatal("known before any probe")
	}
	b, err := m.Bounds(context.Background(), clip, 80_000)
	if err != nil {
		t.Fatal(err)
	}
	if known, ok := m.BoundsKnown(clip); !ok || len(known) != len(b) {
		t.Fatalf("a probe's bounds are known without another: %v %v", known, ok)
	}
	// Slots 0, 6, 12, 18, 24 s land on the keyframes at or before them;
	// two slots landing on one keyframe make one segment.
	want := []Bound{{0}, {4.3}, {8.6}, {17.2}, {21.5}}
	for i, w := range want {
		if math.Abs(b[i].At-w.At) > 0.05 {
			t.Errorf("bound %d = %+v, want %+v", i, b[i], w)
		}
	}
	for i := 1; i < len(b); i++ {
		if b[i].At <= b[i-1].At {
			t.Fatalf("bounds not strictly increasing: %+v", b)
		}
	}
}

func TestPlaylistListsBoundDurations(t *testing.T) {
	p := Playlist(Durations([]Bound{{0.04}, {4.3}, {17.2}}, 20_000), false)
	for _, want := range []string{"#EXT-X-TARGETDURATION:13", "#EXTINF:4.300,\n0.m4s", "#EXTINF:12.900,\n1.m4s", "#EXTINF:2.800,\n2.m4s", "#EXT-X-ENDLIST"} {
		if !strings.Contains(p, want) {
			t.Errorf("playlist lacks %q:\n%s", want, p)
		}
	}
	if q := Playlist(Durations(nil, 13_000), false); !strings.Contains(q, "#EXTINF:6.000,\n1.m4s") || !strings.Contains(q, "#EXTINF:1.000,\n2.m4s") {
		t.Errorf("without bounds, segments are SegmentSeconds long:\n%s", q)
	}
	if q := Playlist(Durations(nil, 13_000), true); strings.Contains(q, "#EXT-X-MAP") || !strings.Contains(q, "#EXTINF:6.000,\n0.ts") || !strings.Contains(q, "#EXTINF:1.000,\n2.ts") {
		t.Errorf("MPEG-TS segments need no init segment:\n%s", q)
	}
}

// Only a sliver of a tail joins the segment before, and never the only one.
func TestFoldTail(t *testing.T) {
	for ms, want := range map[int64][]float64{6_006: {6.006}, 6_200: {6, 0.2}, 13_000: {6, 6, 1}, 50: {0.05}} {
		if got := FoldTail(Durations(nil, ms)); len(got) != len(want) || math.Abs(got[len(got)-1]-want[len(want)-1]) > 1e-9 {
			t.Errorf("%d ms: %v, want %v", ms, got, want)
		}
	}
}

// Copied video cuts only on keyframes, so each segment must start every
// track at its bound whichever segment its encoder started at; converted
// audio that started later would leave a hole players stretch around.
func TestCopiedSegmentsMatchAcrossRuns(t *testing.T) {
	clip := gopClip(t)
	m := New(Config{Root: t.TempDir(), Ahead: 50})
	defer m.Close()
	ctx := context.Background()
	b, err := m.Bounds(ctx, clip, 80_000)
	must(t, err)
	job := Job{Input: clip, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "convert", AudioChannels: 2}, Bounds: b,
		Video: &mediafile.Track{StreamIndex: 0, Kind: mediafile.Video, Codec: "h264"}, Audio: &mediafile.Track{StreamIndex: 1, Kind: mediafile.Audio, Codec: "aac"}}
	for run, from := range []int{0, 2, 1} {
		st := Stream{SessionID: int64(run + 1), Key: "0-0", Job: job}
		init, err := m.Init(ctx, st, from)
		must(t, err)
		for n := from; n < from+3; n++ {
			p, err := m.Segment(ctx, st, n)
			must(t, err)
			starts := trackStarts(t, init, p)
			if len(starts) != 2 {
				t.Fatalf("run from %d: segment %d has %d tracks", from, n, len(starts))
			}
			for k, at := range starts {
				// Video starts on its bound; audio within one AAC frame of
				// it, 23 ms at 44.1 kHz.
				slack := 0.002
				if k == 1 {
					slack = 0.025
				}
				if math.Abs(at-lead-b[n].At) > slack {
					t.Errorf("run from %d: segment %d track %d starts at %.3f, want bound %.3f", from, n, k+1, at-lead, b[n].At)
				}
			}
		}
	}
}

// Matroska with B-frames is the common remux: ffmpeg seeks it to the
// keyframe before the one asked for, and its decode times are guessed.
// Each segment of a run still spans exactly its bounds, and starts where
// every other run's does: a run from the start has decode times before zero.
func TestCopiedBFrameSegmentsSpanTheirBounds(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "bframes.mkv")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-f", "lavfi", "-i", "sine", "-t", "40", //nolint:gosec // temp path
		"-c:v", "libx264", "-preset", "veryfast", "-bf", "3", "-x264-params", "scenecut=0:keyint=1000", "-force_key_frames", "expr:gte(t,n_forced*4.3)",
		"-c:a", "aac", clip).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	m := New(Config{Root: t.TempDir(), Ahead: 50})
	defer m.Close()
	ctx := context.Background()
	b, err := m.Bounds(ctx, clip, 40_000)
	must(t, err)
	for _, ts := range []bool{false, true} {
		job := Job{Input: clip, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy", TS: ts}, Bounds: b,
			Video: &mediafile.Track{StreamIndex: 0, Kind: mediafile.Video, Codec: "h264"}, Audio: &mediafile.Track{StreamIndex: 1, Kind: mediafile.Audio, Codec: "aac"}}
		starts := map[int]float64{}
		for _, from := range []int{0, 2, 1} {
			st := Stream{SessionID: int64(from), Key: fmt.Sprintf("%v", ts), Job: job}
			init := ""
			if !ts {
				init, err = m.Init(ctx, st, from)
				must(t, err)
			}
			var first float64
			for n := from; n < from+3; n++ {
				p, err := m.Segment(ctx, st, n)
				must(t, err)
				at, _ := firstVideo(t, init, p)
				if n == from {
					first = at
				}
				// MPEG-TS moves the whole timeline, so starts are measured
				// from the run's first segment.
				if got, want := at-first, b[n].At-b[from].At; math.Abs(got-want) > 0.002 {
					t.Errorf("ts=%v run from %d: segment %d starts %.3f s after segment %d, its bound %.3f s", ts, from, n, got, from, want)
				}
				if want, ok := starts[n]; ok && math.Abs(at-want) > 0.002 {
					t.Errorf("ts=%v run from %d: segment %d starts at %.3f, an earlier run's at %.3f", ts, from, n, at, want)
				}
				starts[n] = at
			}
		}
	}
}

// trackStarts is each track's first decode time in seg, in seconds.
func trackStarts(t *testing.T, initPath, segPath string) []float64 {
	t.Helper()
	init, err := os.ReadFile(initPath) //nolint:gosec // test temp path
	must(t, err)
	seg, err := os.ReadFile(segPath) //nolint:gosec // test temp path
	must(t, err)
	var scales []uint32
	boxes(init, func(typ string, moov []byte) bool {
		if typ == "moov" {
			boxes(moov[8:], func(typ string, trak []byte) bool {
				if typ == "trak" {
					scales = append(scales, timescale(append(append([]byte{0, 0, 0, 0}, "moov"...), trak...)))
				}
				return true
			})
		}
		return typ != "moov"
	})
	var starts []float64
	boxes(seg, func(typ string, moof []byte) bool {
		if typ != "moof" {
			return true
		}
		boxes(moof[8:], func(typ string, traf []byte) bool {
			if typ == "traf" && len(starts) < len(scales) {
				dt, ok := decodeTime(append(append([]byte{0, 0, 0, 0}, "moof"...), traf...))
				if !ok {
					t.Fatal("traf without tfdt")
				}
				starts = append(starts, float64(dt)/float64(scales[len(starts)]))
			}
			return true
		})
		return false
	})
	return starts
}

// A seek restarts the encoder while players may still be reading what the
// last run wrote, so a restart keeps those files and never rewrites init.
func TestRestartKeepsEncodedSegments(t *testing.T) {
	clip := gopClip(t)
	m := New(Config{Root: t.TempDir(), Ahead: 2})
	defer m.Close()
	ctx := context.Background()
	b, err := m.Bounds(ctx, clip, 80_000)
	must(t, err)
	st := Stream{SessionID: 1, Key: "0-0", Job: Job{Input: clip, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy"}, Bounds: b,
		Video: &mediafile.Track{StreamIndex: 0, Kind: mediafile.Video, Codec: "h264"}}}
	init, err := m.Init(ctx, st, 0)
	must(t, err)
	first, err := m.Segment(ctx, st, 1)
	must(t, err)
	before, err := os.Stat(init)
	must(t, err)
	if _, err := m.start(st, 8); err != nil {
		t.Fatal(err)
	}
	p, err := m.Segment(ctx, st, 8)
	must(t, err)
	if at := tfdt(t, init, p) - lead; math.Abs(at-b[8].At) > 0.002 {
		t.Errorf("segment 8 starts at %.3f, want %.3f", at, b[8].At)
	}
	if _, err := os.Stat(first); err != nil {
		t.Errorf("restart deleted segment 1: %v", err)
	}
	if after, err := os.Stat(init); err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("restart rewrote init: %v", err)
	}
}

// slowTool puts a tool, ffprobe or ffmpeg, on PATH that waits before running
// the real one and counts its runs in the returned file.
func slowTool(t *testing.T, tool, wait string) string {
	t.Helper()
	real, err := exec.LookPath(tool)
	if err != nil || testing.Short() {
		t.Skip(tool + " not installed")
	}
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	script := "#!/bin/sh\necho x >> '" + count + "'\nsleep " + wait + "\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil { //nolint:gosec // a test script
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return count
}

func runs(t *testing.T, count string) int {
	t.Helper()
	b, _ := os.ReadFile(count) //nolint:gosec // test temp path
	return strings.Count(string(b), "x")
}

// A caller that gives up does not take the probe with it, and callers that
// arrive while it runs share it.
func TestBoundsProbeIsSharedAndOutlivesItsCaller(t *testing.T) {
	clip := gopClip(t)
	count := slowTool(t, "ffprobe", "0.5")
	m := New(Config{Root: t.TempDir()})
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	gaveUp := make(chan error, 1)
	go func() { _, err := m.Bounds(short, clip, 80_000); gaveUp <- err }()
	time.Sleep(20 * time.Millisecond)
	b, err := m.Bounds(context.Background(), clip, 80_000)
	if err != nil || len(b) == 0 {
		t.Fatal(b, err)
	}
	if err := <-gaveUp; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the caller that gave up: %v", err)
	}
	if n := runs(t, count); n != 1 {
		t.Fatalf("%d probes", n)
	}
}
