package playback_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// phone is an SDR phone away from home on a metered path with an AAC-only
// stereo route: none of which a download keeps to.
func phone() playback.Snapshot {
	s := web()
	s.Containers = []string{"mp4", "m4a", "mp3"}
	s.Audio = []string{"aac", "mp3"}
	s.Network = playback.Network{Metered: true, MaxBitrate: 1e6}
	s.Protocols = []string{"hls"}
	return s
}

func TestDecideDownload(t *testing.T) {
	file := func(container string, bitrate int64, ts ...*mediafile.Track) playback.Media {
		return playback.Media{Container: container, Bitrate: bitrate, SizeBytes: 123456789, DurationMs: 600_000, Tracks: ts}
	}
	withBitrate := func(tr *mediafile.Track, b int64) *mediafile.Track { tr.Bitrate = &b; return tr }
	cases := []struct {
		name    string
		m       playback.Media
		s       func() playback.Snapshot
		sel     playback.Selection
		quality string
		check   func(t *testing.T, p playback.DownloadPlan)
	}{
		{"the file itself, whatever the path", file("mp4", 10e6, h264(), audio(2, "aac", 2, true)), phone, playback.Selection{}, playback.Original,
			func(t *testing.T, p playback.DownloadPlan) {
				if !p.Original || p.Bytes != 123456789 || p.Container != "mp4" {
					t.Errorf("%+v", p)
				}
			}},
		{"a container the device refuses is remuxed", file("mkv", 10e6, h264(), withBitrate(audio(2, "aac", 6, true), 384e3)), phone, playback.Selection{}, playback.Original,
			func(t *testing.T, p playback.DownloadPlan) {
				e := p.Entry
				if p.Original || e.Verdict != session.Remux || e.VideoAction != "copy" || e.AudioAction != "copy" || p.Container != "mp4" {
					t.Errorf("%+v %+v", p, e)
				}
				if p.Bytes != 600*10e6/8 {
					t.Errorf("a remux is predicted at the file's bitrate: %d", p.Bytes)
				}
			}},
		{"audio the device cannot play becomes AAC keeping surround", file("mkv", 10e6, h264(), withBitrate(audio(2, "ac3", 6, true), 640e3)), phone,
			playback.Selection{}, playback.Original, func(t *testing.T, p playback.DownloadPlan) {
				e := p.Entry
				if e.Verdict != session.AudioTranscode || e.AudioAction != "convert" || e.AudioChannels != 6 || e.VideoAction != "copy" {
					t.Errorf("%+v", e)
				}
				if p.Bytes != 600*(10e6-640e3+384e3)/8 {
					t.Errorf("copied video plus 384 kb/s of AAC: %d", p.Bytes)
				}
			}},
		{"the chosen audio track", file("mkv", 10e6, h264(), audio(2, "aac", 2, true), audio(3, "ac3", 6, false)), phone, playback.Selection{AudioTrackID: id(3)},
			playback.Original, func(t *testing.T, p playback.DownloadPlan) {
				if *p.Entry.AudioTrackID != uid.N(3) || p.Entry.AudioAction != "convert" {
					t.Errorf("%+v", p.Entry)
				}
			}},
		{"HDR the display cannot show converts to SDR H.264", file("mkv", 40e6, hevcHDR(), audio(2, "aac", 2, true)),
			func() playback.Snapshot { s := phone(); s.Video = s.Video[:1]; return s }, playback.Selection{}, playback.Original,
			func(t *testing.T, p playback.DownloadPlan) {
				e := p.Entry
				if e.Verdict != session.VideoTranscode || e.VideoCodec != "h264" || !e.ToneMap || e.TopHeight != 2160 || e.TopBitrate != 20e6 ||
					e.Encoder != session.EncoderHardware || e.AudioAction != "copy" {
					t.Errorf("%+v", e)
				}
			}},
		{"a forced bitmap subtitle is burned in", file("mkv", 10e6, h264(), audio(2, "aac", 2, true), sub(4, "hdmv_pgs_subtitle", false, true), sub(5, "subrip", true, false)),
			phone, playback.Selection{}, playback.Original, func(t *testing.T, p playback.DownloadPlan) {
				e := p.Entry
				if p.Quality != playback.Original || e.Verdict != session.VideoTranscode || e.SubtitleAction != session.SubtitleBurn || *e.SubtitleTrackID != uid.N(4) || e.TopHeight != 1080 {
					t.Errorf("%+v", e)
				}
			}},
		{"a smaller size converts at its rung", file("mp4", 10e6, h264(), audio(2, "aac", 6, true)), phone, playback.Selection{}, "720p",
			func(t *testing.T, p playback.DownloadPlan) {
				e := p.Entry
				if p.Original || p.Quality != "720p" || e.Verdict != session.VideoTranscode || e.TopHeight != 720 || e.TopBitrate != 4e6 || e.AudioAction != "convert" || e.AudioChannels != 2 {
					t.Errorf("%+v", e)
				}
				if p.Bytes != 600*(4e6+128e3)/8 {
					t.Errorf("4 Mb/s of video and 128 kb/s of stereo AAC: %d", p.Bytes)
				}
			}},
		{"a rung no smaller than the source is the original", file("mp4", 6e6, h264(), audio(2, "aac", 2, true)), phone, playback.Selection{}, "1080p",
			func(t *testing.T, p playback.DownloadPlan) {
				if !p.Original || p.Quality != playback.Original {
					t.Errorf("%+v", p)
				}
			}},
		{"aac256 asked of a video is Original", file("mp4", 10e6, h264(), audio(2, "aac", 2, true)), phone, playback.Selection{}, playback.AAC256,
			func(t *testing.T, p playback.DownloadPlan) {
				if !p.Original {
					t.Errorf("%+v", p)
				}
			}},
		{"a lossless song the device plays is itself", file("flac", 900e3, audio(1, "flac", 2, true)),
			func() playback.Snapshot {
				s := phone()
				s.Containers, s.Audio = []string{"flac"}, []string{"flac"}
				return s
			}, playback.Selection{}, playback.Original,
			func(t *testing.T, p playback.DownloadPlan) {
				if !p.Original || p.Container != "flac" {
					t.Errorf("%+v", p)
				}
			}},
		{"a lossless song at AAC 256 converts", file("flac", 900e3, audio(1, "flac", 2, true)),
			func() playback.Snapshot {
				s := phone()
				s.Containers, s.Audio = []string{"flac"}, []string{"flac", "aac"}
				return s
			}, playback.Selection{}, "720p",
			func(t *testing.T, p playback.DownloadPlan) {
				e := p.Entry
				if p.Original || p.Quality != playback.AAC256 || e.Verdict != session.AudioTranscode || e.TopBitrate != 256e3 || e.AudioChannels != 2 || p.Container != "m4a" || p.Bytes != 600*256e3/8 {
					t.Errorf("%+v %+v", p, e)
				}
			}},
		{"a lossy song stays itself at AAC 256", file("mp3", 320e3, audio(1, "mp3", 2, true)), phone, playback.Selection{}, playback.AAC256,
			func(t *testing.T, p playback.DownloadPlan) {
				if !p.Original {
					t.Errorf("%+v", p)
				}
			}},
		{"a song the device cannot play is AAC 256 even as Original", file("flac", 900e3, audio(1, "flac", 2, true)), phone, playback.Selection{}, playback.Original,
			func(t *testing.T, p playback.DownloadPlan) {
				if p.Original || p.Entry.AudioAction != "convert" || p.Container != "m4a" {
					t.Errorf("%+v", p)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, ok := playback.DecideDownload(c.m, c.s(), c.sel, c.quality)
			if !ok {
				t.Fatal("nothing to keep")
			}
			c.check(t, p)
		})
	}
}

func TestDecideDownloadNothingPlayable(t *testing.T) {
	s := phone()
	s.Video = nil
	m := playback.Media{Container: "mkv", Bitrate: 10e6, DurationMs: 1000, Tracks: []*mediafile.Track{h264(), audio(2, "aac", 2, true)}}
	if p, ok := playback.DecideDownload(m, s, playback.Selection{}, playback.Original); ok {
		t.Fatalf("a device decoding no video keeps nothing: %+v", p)
	}
}
