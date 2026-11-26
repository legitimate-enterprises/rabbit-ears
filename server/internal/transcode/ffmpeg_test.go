package transcode

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", name))
	if _, err := os.Stat(p); err != nil || testing.Short() {
		t.Skipf("fixture %s missing; run make deps", name)
	}
	return p
}

// tracks probes a fixture the way the scanner does.
func tracks(t *testing.T, path string) (video, audio, sub []*mediafile.Track) {
	t.Helper()
	r, err := probe.Run(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range r.Tracks {
		switch tr.Kind {
		case mediafile.Video:
			video = append(video, tr)
		case mediafile.Audio:
			audio = append(audio, tr)
		default:
			sub = append(sub, tr)
		}
	}
	return
}

func joined(args []string) string { return strings.Join(args, " ") }

func TestArgsPerVerdict(t *testing.T) {
	v := &mediafile.Track{StreamIndex: 0, Kind: mediafile.Video, Codec: "hevc", Width: ptr(3840), Height: ptr(2160), HDRFormat: "hdr10"}
	a := &mediafile.Track{StreamIndex: 1, Kind: mediafile.Audio, Codec: "truehd", Channels: ptr(8)}
	s := &mediafile.Track{StreamIndex: 3, Kind: mediafile.Subtitle, Codec: "hdmv_pgs_subtitle"}
	job := func(e *session.PlanEntry, sub *mediafile.Track) Job {
		return Job{Input: "/in.mkv", Entry: e, Video: v, Audio: a, Subtitle: sub, Rung: playback.Rung{Width: 1280, Height: 720, Bitrate: 4e6}, Segment: 2, OutDir: "/out"}
	}
	remux := joined(Args(job(&session.PlanEntry{VideoAction: "copy", AudioAction: "copy"}, nil)))
	for _, want := range []string{"-ss 12", "-copyts", "-map 0:0", "-map 0:1", "-c:v copy", "-tag:v hvc1 -bsf:v hevc_mp4toannexb", "-c:a copy", "-hls_segment_type fmp4", "-hls_time 6", "-start_number 2", "/out/%d.m4s",
		"-hls_segment_options movflags=+frag_discont"} {
		if !strings.Contains(remux, want) {
			t.Errorf("remux lacks %q: %s", want, remux)
		}
	}
	ts := joined(Args(job(&session.PlanEntry{VideoAction: "copy", AudioAction: "copy", TS: true}, nil)))
	for _, want := range []string{"-c:v copy", "-hls_segment_type mpegts", "-start_number 2", "/out/%d.ts"} {
		if !strings.Contains(ts, want) {
			t.Errorf("TS remux lacks %q: %s", want, ts)
		}
	}
	for _, not := range []string{"hvc1", "frag_discont", "-hls_fmp4_init_filename"} {
		if strings.Contains(ts, not) {
			t.Errorf("TS remux has %q: %s", not, ts)
		}
	}
	audio := joined(Args(job(&session.PlanEntry{VideoAction: "copy", AudioAction: "convert", AudioChannels: 2}, nil)))
	if !strings.Contains(audio, "-c:a aac -ac 2") {
		t.Errorf("audio: %s", audio)
	}
	hw := joined(Args(job(&session.PlanEntry{VideoAction: "convert", AudioAction: "convert", AudioChannels: 2, VideoCodec: "h264", Encoder: session.EncoderHardware, ToneMap: true}, nil)))
	for _, want := range []string{"-hwaccel videotoolbox", "scale_vt=w=1280:h=720:color_matrix=bt709:color_primaries=bt709:color_transfer=bt709", "-c:v h264_videotoolbox", "-b:v 4000000",
		"-force_key_frames expr:gte(t,n_forced*6)"} {
		if !strings.Contains(hw, want) {
			t.Errorf("hardware lacks %q: %s", want, hw)
		}
	}
	sw := joined(Args(job(&session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderSoftware, ToneMap: true}, nil)))
	if strings.Contains(sw, "videotoolbox") || !strings.Contains(sw, "-c:v libx264") || !strings.Contains(sw, "out_transfer=bt709") {
		t.Errorf("software: %s", sw)
	}
	burn := Args(job(&session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderHardware, SubtitleAction: "burn"}, s))
	if j := joined(burn); strings.Contains(j, "-hwaccel") || !strings.Contains(j, "[0:0]") || !strings.Contains(j, "[0:3]scale=w=1280:h=720[s];[v][s]overlay") || !strings.Contains(j, "h264_videotoolbox") {
		t.Errorf("burn-in decodes in software, overlays, encodes in hardware: %s", j)
	}
	if !slices.Contains(burn, "[out]") {
		t.Errorf("burn maps the filter output: %v", burn)
	}
	for _, codec := range []string{"mpeg4", "av1", "vp9"} {
		v.Codec = codec
		j := joined(Args(job(&session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderHardware}, nil)))
		if strings.Contains(j, "-hwaccel") || !strings.Contains(j, "scale=w=1280:h=720,format=yuv420p") || !strings.Contains(j, "h264_videotoolbox") {
			t.Errorf("%s decodes in software, encodes in hardware: %s", codec, j)
		}
	}
	v.Codec = "hevc"
	hdr := joined(Args(job(&session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "hevc", Encoder: session.EncoderHardware}, nil)))
	if !strings.Contains(hdr, "hevc_videotoolbox") || !strings.Contains(hdr, "-color_trc smpte2084") || strings.Contains(hdr, "color_transfer=bt709") {
		t.Errorf("HDR kept: %s", hdr)
	}
}

func ptr(n int) *int { return &n }

// encode runs a job for real and probes init plus the first segment.
func encode(t *testing.T, j Job) *probe.Result {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	j.OutDir = t.TempDir()
	out, err := exec.Command("ffmpeg", Args(j)...).CombinedOutput() //nolint:gosec // test inputs are fixtures and temp paths
	if err != nil {
		t.Fatalf("ffmpeg: %v\n%s\n%s", err, joined(Args(j)), out)
	}
	init, err := os.ReadFile(filepath.Join(j.OutDir, runInit))
	if err != nil {
		t.Fatal(err)
	}
	seg, err := os.ReadFile(filepath.Join(j.OutDir, SegmentName(j.Segment)))
	if err != nil {
		t.Fatal(err)
	}
	joinedPath := filepath.Join(j.OutDir, "joined.mp4")
	if err := os.WriteFile(joinedPath, append(init, seg...), 0o600); err != nil { //nolint:gosec // test inputs are fixtures and temp paths
		t.Fatal(err)
	}
	r, err := probe.Run(context.Background(), joinedPath)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func kinds(r *probe.Result) (v, a *mediafile.Track) {
	for _, t := range r.Tracks {
		if t.Kind == mediafile.Video && v == nil {
			v = t
		}
		if t.Kind == mediafile.Audio && a == nil {
			a = t
		}
	}
	return
}

func TestEncodeSoftwareDecodeFallback(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	needHardware(t)
	// MPEG-4 Part 2, as in XviD AVIs, has no VideoToolbox decoder.
	in := filepath.Join(t.TempDir(), "xvid.avi")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=640x360:r=24:d=7", //nolint:gosec // temp path
		"-c:v", "mpeg4", "-vtag", "XVID", in).CombinedOutput(); err != nil {
		t.Fatalf("make clip: %v\n%s", err, out)
	}
	v, _, _ := tracks(t, in)
	r := encode(t, Job{Input: in, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderHardware},
		Video: v[0], Rung: playback.Rung{Width: 426, Height: 240, Bitrate: 500e3}})
	if got, _ := kinds(r); got == nil || got.Codec != "h264" || *got.Height != 240 {
		t.Errorf("%+v", got)
	}
}

func TestEncodeFixtures(t *testing.T) {
	hdrFile := fixture(t, "hdr10-main10.mkv")
	h264File := fixture(t, "h264-high41.mp4")
	hv, ha, hs := tracks(t, hdrFile)
	bv, ba, _ := tracks(t, h264File)
	truehd, aac := ha[0], ha[1]
	pgs := hs[0]
	rung := playback.Rung{Width: 640, Height: 360, Bitrate: 800e3}
	cases := []struct {
		name  string
		job   Job
		check func(t *testing.T, v, a *mediafile.Track)
	}{
		{"remux", Job{Input: h264File, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy"}, Video: bv[0], Audio: ba[0], Rung: rung},
			func(t *testing.T, v, a *mediafile.Track) {
				if v.Codec != "h264" || v.Profile != "High" || a.Codec != "aac" {
					t.Errorf("%+v %+v", v, a)
				}
			}},
		{"remux HEVC HDR10 keeps HDR", Job{Input: hdrFile, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy"}, Video: hv[0], Audio: aac, Rung: rung},
			func(t *testing.T, v, a *mediafile.Track) {
				if v.Codec != "hevc" || v.HDRFormat != "hdr10" || a.Codec != "aac" {
					t.Errorf("%+v %+v", v, a)
				}
			}},
		{"audio TrueHD to AAC with downmix", Job{Input: hdrFile, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "convert", AudioChannels: 1}, Video: hv[0], Audio: truehd, Rung: rung},
			func(t *testing.T, v, a *mediafile.Track) {
				if a.Codec != "aac" || *a.Channels != 1 || v.Codec != "hevc" {
					t.Errorf("%+v %+v", v, a)
				}
			}},
		{"video hardware with tone-map", Job{Input: hdrFile, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderHardware, ToneMap: true},
			Video: hv[0], Audio: aac, Rung: rung, Segment: 1},
			func(t *testing.T, v, a *mediafile.Track) {
				if v.Codec != "h264" || v.HDRFormat != "" || v.ColorTransfer != "bt709" || *v.BitDepth != 8 || *v.Height != 360 {
					t.Errorf("%+v", v)
				}
			}},
		{"video software with tone-map", Job{Input: hdrFile, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderSoftware, ToneMap: true},
			Video: hv[0], Audio: aac, Rung: playback.Rung{Width: 426, Height: 240, Bitrate: 500e3}},
			func(t *testing.T, v, a *mediafile.Track) {
				if v.Codec != "h264" || v.ColorTransfer != "bt709" || *v.Height != 240 {
					t.Errorf("%+v", v)
				}
			}},
		{"video hardware keeping HDR10", Job{Input: hdrFile, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "hevc", Encoder: session.EncoderHardware},
			Video: hv[0], Audio: aac, Rung: rung},
			func(t *testing.T, v, a *mediafile.Track) {
				if v.Codec != "hevc" || v.HDRFormat != "hdr10" || *v.BitDepth != 10 {
					t.Errorf("%+v", v)
				}
			}},
		{"video from H.264 in software", Job{Input: h264File, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "convert", AudioChannels: 2, VideoCodec: "h264", Encoder: session.EncoderSoftware},
			Video: bv[0], Audio: ba[0], Rung: rung},
			func(t *testing.T, v, a *mediafile.Track) {
				if v.Codec != "h264" || a.Codec != "aac" || *a.Channels != 2 {
					t.Errorf("%+v %+v", v, a)
				}
			}},
		{"PGS burn-in", Job{Input: hdrFile, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", SubtitleAction: "burn", VideoCodec: "h264", Encoder: session.EncoderHardware, ToneMap: true},
			Video: hv[0], Audio: aac, Subtitle: pgs, Rung: rung},
			func(t *testing.T, v, a *mediafile.Track) {
				if v.Codec != "h264" || v.ColorTransfer != "bt709" {
					t.Errorf("%+v", v)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.job.Entry.Encoder == session.EncoderHardware {
				needHardware(t)
			}
			v, a := kinds(encode(t, c.job))
			if v == nil || a == nil {
				t.Fatalf("missing track: %v %v", v, a)
			}
			c.check(t, v, a)
		})
	}
}

// A bitmap subtitle drawn for a smaller picture than the video's, as a UHD
// disc's 1080p PGS, covers the whole picture when burned in.
func TestBurnInFillsThePictureWithASmallerSubtitleCanvas(t *testing.T) {
	pgs := fixture(t, "hdr10-main10.mkv") // a 640x360 PGS box at 272,300 shown from 0.5 to 2 s
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "uhd.mkv"), filepath.Join(dir, "out.mp4")
	if b, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "color=black:s=1280x720:r=24:d=3", "-i", pgs, //nolint:gosec // temp paths
		"-map", "0:v", "-map", "1:3", "-c:v", "libx264", "-c:s", "copy", in).CombinedOutput(); err != nil {
		t.Fatalf("make clip: %v\n%s", err, b)
	}
	v, _, s := tracks(t, in)
	j := Job{Input: in, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", SubtitleAction: "burn", VideoCodec: "h264", Encoder: session.EncoderSoftware},
		Video: v[0], Subtitle: s[0], Rung: playback.Rung{Width: 640, Height: 360, Bitrate: 800e3}}
	if b, err := exec.Command("ffmpeg", FileArgs(j, out)...).CombinedOutput(); err != nil { //nolint:gosec // temp paths
		t.Fatalf("%v %s\n%s", err, joined(FileArgs(j, out)), b)
	}
	luma, err := exec.Command("ffmpeg", "-v", "error", "-ss", "1", "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-").Output() //nolint:gosec // temp path
	if err != nil || len(luma) != 640*360 {
		t.Fatalf("frame: %v, %d bytes", err, len(luma))
	}
	at := func(x, y int) byte { return luma[y*640+x] }
	if at(320, 312) < 200 || at(160, 156) > 50 {
		t.Fatalf("the box sits at the bottom centre: there %d, at the top left quarter's spot %d", at(320, 312), at(160, 156))
	}
}

// Burning a subtitle into HDR10 output keeps the picture's 10 bits, so a
// smooth gradient does not band.
func TestBurnInKeepsTenBits(t *testing.T) {
	pgs := fixture(t, "hdr10-main10.mkv")
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "ramp.mkv"), filepath.Join(dir, "out.mp4")
	// Luma rises by one 10-bit step per column.
	if b, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "nullsrc=s=1024x64:r=24:d=1,format=yuv420p10le,geq=lum='X+64':cb=512:cr=512", //nolint:gosec // temp paths
		"-i", pgs, "-map", "0:v", "-map", "1:3", "-c:v", "ffv1", "-c:s", "copy", in).CombinedOutput(); err != nil {
		t.Fatalf("make clip: %v\n%s", err, b)
	}
	v, _, s := tracks(t, in)
	j := Job{Input: in, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", SubtitleAction: "burn", VideoCodec: "hevc", Encoder: session.EncoderSoftware},
		Video: v[0], Subtitle: s[0], Rung: playback.Rung{Width: 1024, Height: 64, Bitrate: 20e6}}
	if b, err := exec.Command("ffmpeg", FileArgs(j, out)...).CombinedOutput(); err != nil { //nolint:gosec // temp paths
		t.Fatalf("%v %s\n%s", err, joined(FileArgs(j, out)), b)
	}
	raw, err := exec.Command("ffmpeg", "-v", "error", "-i", out, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray10le", "-").Output() //nolint:gosec // temp path
	if err != nil || len(raw) != 1024*64*2 {
		t.Fatalf("frame: %v, %d bytes", err, len(raw))
	}
	seen := map[uint16]bool{}
	for x := range 1024 {
		i := (32*1024 + x) * 2
		seen[uint16(raw[i])|uint16(raw[i+1])<<8] = true
	}
	if len(seen) < 600 {
		t.Fatalf("a row of the ramp holds %d levels, as 8 bits would", len(seen))
	}
}

// Segments from an encoder started mid-file sit at their playlist times,
// so segments from encoders started at different points line up.
func TestSegmentTiming(t *testing.T) {
	clip := longClip(t)
	j := convertJob(clip)
	j.Entry.Encoder = hostEncoder()
	j.Segment, j.OutDir = 2, t.TempDir()
	if out, err := exec.Command("ffmpeg", Args(j)...).CombinedOutput(); err != nil { //nolint:gosec // test inputs are fixtures and temp paths
		t.Fatalf("%v %s", err, out)
	}
	init, _ := os.ReadFile(filepath.Join(j.OutDir, runInit))
	for _, n := range []int{2, 3, 4} {
		seg, _ := os.ReadFile(filepath.Join(j.OutDir, SegmentName(n))) //nolint:gosec // test inputs are fixtures and temp paths
		p := filepath.Join(j.OutDir, "j.mp4")
		_ = os.WriteFile(p, append(init, seg...), 0o600)                                                                                             //nolint:gosec // test inputs are fixtures and temp paths
		out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "packet=pts_time", "-of", "csv=p=0", p).Output() //nolint:gosec // test inputs are fixtures and temp paths
		if err != nil {
			t.Fatal(err)
		}
		pts := strings.Fields(string(out))
		if want := fmt.Sprintf("%d.0", lead+n*SegmentSeconds); len(pts) == 0 || !strings.HasPrefix(pts[0], want) {
			t.Fatalf("segment %d starts at %v, want %s", n, pts[:min(len(pts), 1)], want)
		}
		if got := len(pts); got != SegmentSeconds*24 {
			t.Fatalf("segment %d has %d frames, want %d", n, got, SegmentSeconds*24)
		}
	}
	if _, err := os.Stat(filepath.Join(j.OutDir, SegmentName(1))); err == nil {
		t.Fatal("segments before the start are not encoded")
	}
}

func TestSidecar(t *testing.T) {
	file := fixture(t, "hdr10-main10.mkv")
	_, _, subs := tracks(t, file)
	out := filepath.Join(t.TempDir(), "s.vtt")
	if b, err := exec.Command("ffmpeg", SidecarArgs(file, map[int]string{subs[1].StreamIndex: out})...).CombinedOutput(); err != nil { //nolint:gosec // test inputs are fixtures and temp paths
		t.Fatalf("%v %s", err, b)
	}
	b, _ := os.ReadFile(out) //nolint:gosec // test inputs are fixtures and temp paths
	if !strings.HasPrefix(string(b), "WEBVTT") || !strings.Contains(string(b), "-->") {
		t.Fatalf("%q", b)
	}
}

// A song converts to AAC at its entry's bitrate, and remuxes FLAC into
// fragmented MP4 untouched; neither carries the file's cover picture.
func TestEncodeSongs(t *testing.T) {
	flac := fixture(t, filepath.Join("music", "The Example Band", "First Light (2024)", "01 Opening.flac"))
	_, a, _ := tracks(t, flac)
	args := joined(Args(Job{Input: flac, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "convert", AudioChannels: 2, TopBitrate: 256e3},
		Audio: a[0], OutDir: "/out"}))
	if !strings.Contains(args, "-c:a aac -ac 2 -b:a 256000") || strings.Contains(args, "-c:v") {
		t.Errorf("song conversion: %s", args)
	}
	for _, c := range []struct {
		action, codec string
	}{{"convert", "aac"}, {"copy", "flac"}} {
		r := encode(t, Job{Input: flac, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: c.action, AudioChannels: 2, TopBitrate: 256e3}, Audio: a[0]})
		v, got := kinds(r)
		if v != nil || got == nil || got.Codec != c.codec || *got.Channels != 2 {
			t.Errorf("%s: %+v %+v", c.action, v, got)
		}
	}
}
