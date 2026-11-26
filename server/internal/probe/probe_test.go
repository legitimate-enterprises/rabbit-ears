package probe

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	p, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", name))
	if _, err := os.Stat(p); err != nil || testing.Short() {
		t.Skipf("fixture %s missing; run make deps", name)
	}
	return p
}

func byKind(r *Result, kind string) []*mediafile.Track {
	var out []*mediafile.Track
	for _, tr := range r.Tracks {
		if tr.Kind == kind {
			out = append(out, tr)
		}
	}
	return out
}

func TestHDR10MKV(t *testing.T) {
	r, err := Run(context.Background(), fixture(t, "hdr10-main10.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Container != "mkv" || r.DurationMs == nil || *r.DurationMs < 9000 || *r.DurationMs > 11000 {
		t.Errorf("container %q duration %v", r.Container, r.DurationMs)
	}
	v := byKind(r, "video")
	if len(v) != 1 {
		t.Fatalf("video tracks: %d", len(v))
	}
	x := v[0]
	if x.Codec != "hevc" || x.Profile != "Main 10" || x.Width == nil || *x.Width != 640 || *x.Height != 360 ||
		*x.BitDepth != 10 || x.PixelFormat != "yuv420p10le" || x.ColorTransfer != "smpte2084" ||
		x.ColorPrimaries != "bt2020" || x.HDRFormat != "hdr10" || *x.FrameRate != 24 || x.Level == nil || *x.Level != 63 {
		t.Errorf("video: %+v", x)
	}
	a := byKind(r, "audio")
	if len(a) != 2 || a[0].Codec != "truehd" || a[1].Codec != "aac" || a[0].Language != "eng" ||
		*a[1].Channels != 2 || a[1].ChannelLayout != "stereo" || *a[1].SampleRate != 48000 {
		t.Errorf("audio: %+v", a)
	}
	s := byKind(r, "subtitle")
	if len(s) != 2 || s[0].Codec != "hdmv_pgs_subtitle" || *s[0].SubtitleIsText || !s[0].IsForced ||
		s[1].Codec != "subrip" || !*s[1].SubtitleIsText || s[1].IsForced {
		t.Errorf("subtitles: %+v", s)
	}
	if s[0].StreamIndex != 3 || s[1].StreamIndex != 4 {
		t.Errorf("stream indexes %d %d", s[0].StreamIndex, s[1].StreamIndex)
	}
}

func TestH264MP4(t *testing.T) {
	r, err := Run(context.Background(), fixture(t, "h264-high41.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	v := byKind(r, "video")
	if r.Container != "mp4" || len(v) != 1 || v[0].Codec != "h264" || v[0].Profile != "High" || *v[0].Level != 41 || *v[0].BitDepth != 8 || v[0].HDRFormat != "" {
		t.Errorf("%q %+v", r.Container, v)
	}
	if a := byKind(r, "audio"); len(a) != 1 || a[0].Codec != "aac" {
		t.Errorf("audio %+v", a)
	}
}

func TestChapters(t *testing.T) {
	r, err := Run(context.Background(), fixture(t, "chapters.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Chapters) != 3 || r.Chapters[0].StartMs != 0 || r.Chapters[1].StartMs <= 0 ||
		r.Chapters[0].EndMs != r.Chapters[1].StartMs || r.Chapters[2].EndMs != 10000 {
		t.Errorf("chapters %+v", r.Chapters)
	}
}

func TestAudioOnly(t *testing.T) {
	for _, c := range []struct{ file, container, codec string }{{"sample.flac", "flac", "flac"}, {"sample.mp3", "mp3", "mp3"}} {
		r, err := Run(context.Background(), fixture(t, c.file))
		if err != nil {
			t.Fatal(err)
		}
		a := byKind(r, "audio")
		if r.Container != c.container || len(a) != 1 || a[0].Codec != c.codec || len(byKind(r, "video")) != 0 {
			t.Errorf("%s: %q %+v", c.file, r.Container, r.Tracks)
		}
	}
}

func TestParseSideDataAndHLG(t *testing.T) {
	r, err := Parse([]byte(`{"format":{"format_name":"matroska,webm","duration":"1.5","bit_rate":"1000"},"chapters":[{"start_time":"0.0","end_time":"1.5","tags":{"title":"Intro"}}],"streams":[
	 {"index":0,"codec_type":"video","codec_name":"hevc","pix_fmt":"yuv420p10le","width":3840,"height":2160,"avg_frame_rate":"24000/1001","color_transfer":"smpte2084","side_data_list":[{"side_data_type":"DOVI configuration record","dv_profile":7}]},
	 {"index":1,"codec_type":"video","codec_name":"hevc","pix_fmt":"yuv420p10le","avg_frame_rate":"0/0","r_frame_rate":"25/1","color_transfer":"arib-std-b67"},
	 {"index":2,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}}]}`), "x.webm")
	if err != nil {
		t.Fatal(err)
	}
	if r.Container != "webm" || len(r.Tracks) != 2 || r.Tracks[0].HDRFormat != "dolby_vision" || r.Tracks[1].HDRFormat != "hlg" ||
		r.Tracks[0].DVProfile == nil || *r.Tracks[0].DVProfile != 7 || r.Tracks[1].DVProfile != nil ||
		*r.Tracks[1].FrameRate != 25 || *r.Bitrate != 1000 || r.Chapters[0].Title != "Intro" {
		t.Errorf("%+v %+v", r, r.Tracks[0])
	}
	if _, err := Parse([]byte(`not json`), "x"); err == nil {
		t.Error("want parse error")
	}
}

func TestRunMissingFile(t *testing.T) {
	if _, err := Run(context.Background(), "/nonexistent.mkv"); err == nil {
		t.Error("want error")
	}
}

// Tags come from the container with key case folded, then from the first
// audio stream for those the container lacks (Ogg keeps them there); an
// attached picture is not a track but is reported as a cover.
func TestParseTagsAndCover(t *testing.T) {
	r, err := Parse([]byte(`{"format":{"format_name":"ogg","tags":{"TITLE":"Opening","ALBUMARTIST":"The Band"}},"streams":[
	 {"index":0,"codec_type":"audio","codec_name":"opus","tags":{"title":"ignored","ARTIST":"Guest","TRACKNUMBER":"3/9"}},
	 {"index":1,"codec_type":"video","codec_name":"mjpeg","disposition":{"attached_pic":1}}]}`), "x.ogg")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"title": "Opening", "albumartist": "The Band", "artist": "Guest", "tracknumber": "3/9"}
	if len(r.Tags) != len(want) || !r.Cover || r.CoverStream != 1 || len(r.Tracks) != 1 {
		t.Fatalf("tags %v cover %v at %d tracks %d", r.Tags, r.Cover, r.CoverStream, len(r.Tracks))
	}
	for k, v := range want {
		if r.Tags[k] != v {
			t.Errorf("tag %s = %q, want %q", k, r.Tags[k], v)
		}
	}
}
