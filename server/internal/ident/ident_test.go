package ident_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/ident"
)

func clock(h, m, s int) time.Time { return time.Date(2026, 11, 13, h, m, s, 0, time.UTC) }

func card() ident.Card {
	return ident.Card{Station: "Comedy", Rating: "TV-PG", Next: clock(20, 30, 0), Rows: []ident.Row{
		{At: clock(20, 30, 0), Title: "Seinfeld", Episode: "S7 E17", Name: "The Doll"},
		{At: clock(21, 0, 0), Title: "Seinfeld", Name: "The Friars Club"},
		{At: clock(21, 30, 0), Title: "Heat", Name: "1995"},
	}}
}

func TestBackInCountsWholeMinutesUp(t *testing.T) {
	c := card()
	for at, want := range map[time.Time]int{clock(20, 18, 0): 12, clock(20, 18, 59): 12, clock(20, 29, 30): 1, clock(19, 30, 0): 60} {
		if got := c.BackIn(at); got != want {
			t.Errorf("%s: %d, want %d", at.Format("15:04:05"), got, want)
		}
	}
}

func TestFrameDrawsTheCard(t *testing.T) {
	c := card()
	f := c.Frame(clock(20, 18, 10))
	if b := f.Bounds(); b.Dx() != 1920 || b.Dy() != 1080 {
		t.Fatal(b)
	}
	var (
		bg    = color.RGBA{0x1c, 0x1c, 0x1e, 0xff}
		amber = color.RGBA{0xf2, 0xb3, 0x3d, 0xff}
		text  = color.RGBA{0xf2, 0xf2, 0xf7, 0xff}
		panel = color.RGBA{0x2a, 0x2a, 0x2d, 0xff}
	)
	in := func(f *image.RGBA, c color.RGBA, x0, y0, x1, y1 int) bool {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if f.RGBAAt(x, y) == c {
					return true
				}
			}
		}
		return false
	}
	has := func(c color.RGBA, x0, y0, x1, y1 int) bool { return in(f, c, x0, y0, x1, y1) }
	if f.RGBAAt(5, 5) != bg || f.RGBAAt(1915, 1075) != bg {
		t.Fatal("background")
	}
	for name, ok := range map[string]bool{
		"the logo, in amber, top left":           has(amber, 120, 96, 176, 152),
		"Rabbit Ears beside it":                  has(text, 196, 100, 420, 150),
		"the station's name, bottom left":        has(text, 120, 690, 900, 850),
		"the minutes, in amber":                  has(amber, 250, 880, 500, 984),
		"the next program's card":                has(panel, 1090, 700, 1790, 760),
		"its start, in amber, and title":         has(amber, 1112, 690, 1240, 790) && has(text, 1250, 690, 1700, 790),
		"nothing right of the page's own margin": !has(text, 1801, 0, 1920, 1080) && !has(amber, 1801, 0, 1920, 1080),
	} {
		if !ok {
			t.Error(name)
		}
	}
	if !bytes.Equal(f.Pix, c.Frame(clock(20, 18, 50)).Pix) {
		t.Error("a frame shows no seconds, so the minute's frames are one")
	}
	if bytes.Equal(f.Pix, c.Frame(clock(20, 19, 0)).Pix) {
		t.Error("the next minute's frame differs")
	}
	long := c
	long.Station = strings.Repeat("Saturday Morning Cartoons ", 4)
	if lf := long.Frame(clock(20, 18, 0)); !in(lf, text, 120, 690, 960, 850) || in(lf, text, 961, 600, 1079, 870) {
		t.Error("a long name stays in its column")
	}
}

func TestFrameShowsTheTimeOfTheCardsZone(t *testing.T) {
	east := time.FixedZone("EST", -5*60*60)
	c := card()
	c.Next = c.Next.In(east)
	at := clock(20, 18, 0)
	if !bytes.Equal(c.Frame(at).Pix, c.Frame(at.In(east)).Pix) {
		t.Error("the time top right is the card's zone's, whatever zone the instant came in")
	}
}

func TestFramesDrawnAtOnceAreEachWhole(t *testing.T) {
	c := card()
	want := c.Frame(clock(20, 18, 0)).Pix
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 5 {
				if !bytes.Equal(c.Frame(clock(20, 18, 0)).Pix, want) {
					t.Error("a frame drawn beside another differs from one drawn alone")
				}
			}
		})
	}
	wg.Wait()
}

func TestKeyNamesWhatIsDrawn(t *testing.T) {
	a, b := card(), card()
	from := clock(20, 17, 42)
	if a.Key(from) != b.Key(from) || len(a.Key(from)) < 12 {
		t.Fatal(a.Key(from))
	}
	b.Rows[1].Name = "The Wig Master"
	if a.Key(from) == b.Key(from) || a.Key(from) == a.Key(from.Add(time.Second)) {
		t.Fatal("a change to the card or to where it starts is another ident")
	}
}

func TestDurations(t *testing.T) {
	for d, want := range map[time.Duration][]float64{
		20 * time.Second:         {6, 6, 6, 2},
		12 * time.Second:         {6, 6},
		15500 * time.Millisecond: {6, 6, 3.5},
		400 * time.Millisecond:   {0.4},
		0:                        nil,
	} {
		if got := ident.Durations(d); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", d, got)
		}
	}
	if n := len(ident.Durations(59 * time.Minute)); n != 590 {
		t.Fatal(n)
	}
}

func probe(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("ffprobe", append([]string{"-v", "error"}, args...)...).Output() //nolint:gosec // the test's own arguments
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// first is the first line of ffprobe's answer, which names a stream of an
// MPEG-TS file once for its program and once for itself.
func first(s string) string { return strings.Fields(s + "\n")[0] }

// Render's segments must be the ones Durations promises, or the playlist
// that lists them lies about the stream.
func TestRenderWritesTheSegmentsDurationsNames(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	c := card()
	// A part of a longer ident: it stops short of the next program.
	from := c.Next.Add(-5 * time.Minute).Add(500 * time.Millisecond)
	until := from.Add(75500 * time.Millisecond)
	for _, ts := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "ident")
		if err := ident.Render(context.Background(), dir, c, from, until, ts); err != nil {
			t.Fatal(err)
		}
		ext := ".m4s"
		if ts {
			ext = ".ts"
		}
		durs := ident.Durations(until.Sub(from))
		var all, init []byte
		if !ts {
			var err error
			if init, err = os.ReadFile(filepath.Join(dir, "init.mp4")); err != nil { //nolint:gosec // a temp path
				t.Fatal(err)
			}
			all = init
		}
		for i := range durs {
			b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(i)+ext)) //nolint:gosec // a temp path
			if err != nil {
				t.Fatalf("segment %d of %d: %v", i, len(durs), err)
			}
			all = append(all, b...)
			// A segment holds the sound of its own seconds: a player given
			// pictures without it waits for sound that comes segments later.
			alone := filepath.Join(t.TempDir(), "alone"+ext)
			if err := os.WriteFile(alone, append(slices.Clone(init), b...), 0o600); err != nil { //nolint:gosec // a temp path
				t.Fatal(err)
			}
			n := len(strings.Fields(strings.ReplaceAll(probe(t, "-select_streams", "a:0", "-show_entries", "packet=pts_time", "-of", "csv=p=0", alone), ",", " ")))
			if want := durs[i] * 48000 / 1024; math.Abs(float64(n)-want) > 4 {
				t.Errorf("ts=%v: segment %d of %g s holds %d audio packets, want about %.0f", ts, i, durs[i], n, want)
			}
		}
		if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(len(durs))+ext)); err == nil {
			t.Fatal("more segments than Durations names")
		}
		whole := filepath.Join(t.TempDir(), "whole"+ext)
		if err := os.WriteFile(whole, all, 0o600); err != nil { //nolint:gosec // a temp path
			t.Fatal(err)
		}
		if got := first(probe(t, "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height,pix_fmt", "-of", "csv=p=0", whole)); got != "h264,1920,1080,yuv420p" {
			t.Fatalf("video: %s", got)
		}
		if got := first(probe(t, "-select_streams", "a:0", "-show_entries", "stream=codec_name,channels", "-of", "csv=p=0", whole)); got != "aac,2" {
			t.Fatalf("audio: %s", got)
		}
		// Every segment starts on a keyframe where the grid says, and the
		// audio ends where the next program starts.
		keys := strings.Fields(strings.ReplaceAll(probe(t, "-select_streams", "v:0", "-skip_frame", "nokey", "-show_entries", "frame=pts_time", "-of", "csv=p=0", whole), ",", " "))
		if len(keys) != len(durs) {
			t.Fatalf("%d keyframes for %d segments: %v", len(keys), len(durs), keys)
		}
		first, _ := strconv.ParseFloat(keys[0], 64)
		for i, k := range keys {
			at, _ := strconv.ParseFloat(k, 64)
			if math.Abs(at-first-float64(6*i)) > 0.01 {
				t.Fatalf("keyframe %d at %s", i, k)
			}
		}
		packets := strings.Fields(strings.ReplaceAll(probe(t, "-select_streams", "a:0", "-show_entries", "packet=pts_time", "-of", "csv=p=0", whole), ",", " "))
		begin, _ := strconv.ParseFloat(packets[0], 64)
		to, _ := strconv.ParseFloat(packets[len(packets)-1], 64)
		if length := to - begin + 1024.0/48000; math.Abs(length-75.5) > 0.05 {
			t.Fatalf("ts=%v: audio runs %.3f s, want 75.5", ts, length)
		}
		if err := ident.Render(context.Background(), dir, c, from, until, ts); err != nil {
			t.Fatalf("a rendered ident is kept: %v", err)
		}
	}
}
