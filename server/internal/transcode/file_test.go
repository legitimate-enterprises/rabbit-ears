package transcode

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

func TestFileArgs(t *testing.T) {
	v := &mediafile.Track{StreamIndex: 0, Kind: mediafile.Video, Codec: "hevc", Width: ptr(3840), Height: ptr(2160), HDRFormat: "hdr10"}
	a := &mediafile.Track{StreamIndex: 1, Kind: mediafile.Audio, Codec: "truehd", Channels: ptr(8)}
	j := Job{Input: "/in.mkv", Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "convert", AudioChannels: 6, VideoCodec: "h264",
		Encoder: session.EncoderHardware, ToneMap: true}, Video: v, Audio: a, Rung: playback.Rung{Width: 1280, Height: 720, Bitrate: 4e6}}
	got := joined(FileArgs(j, "/out/7.mp4.part"))
	for _, want := range []string{"-i /in.mkv", "-map 0:0", "-c:v h264_videotoolbox", "-b:v 4000000", "-map 0:1", "-c:a aac -ac 6 -b:a 384000",
		"-movflags +faststart", "-f mp4 /out/7.mp4.part", "-progress pipe:1"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q: %s", want, got)
		}
	}
	for _, not := range []string{"-ss", "-copyts", "-f hls", "-output_ts_offset"} {
		if strings.Contains(got, not) {
			t.Errorf("a whole file has no %q: %s", not, got)
		}
	}
	remux := joined(FileArgs(Job{Input: "/in.mkv", Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy"}, Video: v, Audio: a}, "/out/8.mp4.part"))
	if !strings.Contains(remux, "-c:v copy -tag:v hvc1") || !strings.Contains(remux, "-c:a copy") {
		t.Errorf("remux: %s", remux)
	}
}

// probeFile reads a finished conversion as the scanner would.
func probeFile(t *testing.T, path string) (*probe.Result, *mediafile.Track, *mediafile.Track) {
	t.Helper()
	r, err := probe.Run(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	v, a := kinds(r)
	return r, v, a
}

func TestConvertFileMakesOnePlayableFile(t *testing.T) {
	in := fixture(t, "hdr10-main10.mkv")
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	v, a, s := tracks(t, in)
	var pgs *mediafile.Track
	for _, tr := range s {
		if tr.Codec == "hdmv_pgs_subtitle" {
			pgs = tr
		}
	}
	out := filepath.Join(t.TempDir(), "1.mp4")
	var last atomic.Value
	var logged bytes.Buffer
	j := Job{Input: in, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "convert", AudioChannels: 2, VideoCodec: "h264", Encoder: hostEncoder(),
		ToneMap: true, SubtitleAction: session.SubtitleBurn}, Video: v[0], Audio: a[0], Subtitle: pgs, Rung: playback.Rung{Width: 640, Height: 360, Bitrate: 800e3}}
	log := slog.New(slog.NewTextHandler(&logged, nil)).With("conversion_id", 7)
	if err := ConvertFile(context.Background(), j, out, 10_000, func() bool { return false }, func(f float64) { last.Store(f) }, log); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logged.String(), `msg="Conversion started" conversion_id=7 ffmpeg_command="ffmpeg `) {
		t.Fatalf("the start is logged with the caller's attributes: %s", logged.String())
	}
	r, gv, ga := probeFile(t, out)
	if r.Container != "mp4" || gv == nil || gv.Codec != "h264" || *gv.Height != 360 || gv.HDRFormat != "" || ga == nil || ga.Codec != "aac" || *ga.Channels != 2 {
		t.Fatalf("%s %+v %+v", r.Container, gv, ga)
	}
	if r.DurationMs == nil || *r.DurationMs < 9500 || *r.DurationMs > 10500 {
		t.Fatalf("the whole title: %v", r.DurationMs)
	}
	if f, _ := last.Load().(float64); f < 0.9 || f > 1 {
		t.Fatalf("progress ends near 1: %v", f)
	}
	if _, err := os.Stat(out + partSuffix); !os.IsNotExist(err) {
		t.Fatal("the part file is renamed into place")
	}
}

func TestConvertFileSong(t *testing.T) {
	flac := fixture(t, filepath.Join("music", "The Example Band", "First Light (2024)", "01 Opening.flac"))
	_, a, _ := tracks(t, flac)
	out := filepath.Join(t.TempDir(), "2.m4a")
	j := Job{Input: flac, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "convert", AudioChannels: 2, TopBitrate: 256e3}, Audio: a[0]}
	if err := ConvertFile(context.Background(), j, out, 0, func() bool { return false }, func(float64) {}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	r, v, got := probeFile(t, out)
	if v != nil || got == nil || got.Codec != "aac" || r.Bitrate == nil || *r.Bitrate < 200e3 || *r.Bitrate > 300e3 {
		t.Fatalf("AAC at 256 kb/s alone: %+v %+v %v", v, got, r.Bitrate)
	}
}

// While busy says live playback transcodes, the conversion does not start;
// once it clears, the conversion runs to the end.
func TestConvertFileWaitsWhileLiveTranscodes(t *testing.T) {
	in := fixture(t, "h264-high41.mp4")
	v, a, _ := tracks(t, in)
	var busy atomic.Bool
	busy.Store(true)
	out := filepath.Join(t.TempDir(), "3.mp4")
	j := Job{Input: in, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderSoftware},
		Video: v[0], Audio: a[0], Rung: playback.Rung{Width: 320, Height: 180, Bitrate: 300e3}}
	var progressed atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- ConvertFile(context.Background(), j, out, 10_000, busy.Load, func(float64) { progressed.Store(true) }, slog.Default())
	}()
	time.Sleep(2 * time.Second)
	if progressed.Load() {
		t.Fatal("a conversion makes no progress while live playback transcodes")
	}
	select {
	case err := <-done:
		t.Fatalf("finished while held: %v", err)
	default:
	}
	busy.Store(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("never resumed")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
}

func TestConvertFileCancelLeavesNothing(t *testing.T) {
	in := fixture(t, "h264-high41.mp4")
	v, a, _ := tracks(t, in)
	dir := t.TempDir()
	out := filepath.Join(dir, "4.mp4")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	j := Job{Input: in, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy"}, Video: v[0], Audio: a[0]}
	if err := ConvertFile(ctx, j, out, 10_000, func() bool { return false }, func(float64) {}, slog.Default()); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("left %v", left)
	}
}

// HEVC copied into a file keeps its picture and HDR, tagged as Apple
// players need it.
func TestConvertFileRemuxesHEVC(t *testing.T) {
	in := fixture(t, "hdr10-main10.mkv")
	v, a, _ := tracks(t, in)
	out := filepath.Join(t.TempDir(), "5.mp4")
	j := Job{Input: in, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy"}, Video: v[0], Audio: a[1]}
	if err := ConvertFile(context.Background(), j, out, 10_000, func() bool { return false }, func(float64) {}, slog.Default()); err != nil {
		t.Fatal(err)
	}
	_, gv, ga := probeFile(t, out)
	if gv == nil || gv.Codec != "hevc" || gv.HDRFormat != "hdr10" || ga == nil || ga.Codec != "aac" {
		t.Fatalf("%+v %+v", gv, ga)
	}
	//nolint:gosec // a temp path
	b, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_tag_string", "-of", "csv=p=0", out).Output()
	if err != nil || strings.TrimSpace(string(b)) != "hvc1" {
		t.Fatalf("tagged hvc1: %q %v", b, err)
	}
}

// Progress is logged at each quarter it passes, once, and not at the end,
// which the caller logs as ready; an unknown length logs none.
func TestReadLogsEachQuarterPassedOnce(t *testing.T) {
	report := "out_time_us=1000000\nout_time_us=3000000\nout_time_us=5500000\nout_time_us=5600000\nout_time_us=9000000\nprogress=end\n"
	for _, c := range []struct {
		durationMs int64
		want       []string
	}{
		{10_000, []string{"Conversion 25% done", "Conversion 50% done", "Conversion 75% done"}},
		{0, nil},
	} {
		var logged bytes.Buffer
		read(strings.NewReader(report), c.durationMs, func(float64) {}, slog.New(slog.NewTextHandler(&logged, nil)))
		var got []string
		for _, line := range strings.Split(strings.TrimSpace(logged.String()), "\n") {
			if _, msg, ok := strings.Cut(line, `msg="`); ok {
				got = append(got, strings.SplitN(msg, `"`, 2)[0])
			}
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("duration %d: %q", c.durationMs, got)
		}
	}
}
