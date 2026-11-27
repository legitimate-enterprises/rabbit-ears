package playback_test

import (
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func ip(n int) *int          { return &n }
func id(n int) *uid.ID       { i := uid.N(n); return &i }
func fps(f float64) *float64 { return &f }
func yes() *bool             { b := true; return &b }
func no() *bool              { b := false; return &b }

func h264() *mediafile.Track {
	return &mediafile.Track{ID: uid.N(1), StreamIndex: 0, Kind: mediafile.Video, Codec: "h264", Profile: "High", Level: ip(41),
		Width: ip(1920), Height: ip(1080), FrameRate: fps(24), BitDepth: ip(8)}
}

func hevcHDR() *mediafile.Track {
	return &mediafile.Track{ID: uid.N(1), StreamIndex: 0, Kind: mediafile.Video, Codec: "hevc", Profile: "Main 10", Level: ip(153),
		Width: ip(3840), Height: ip(2160), FrameRate: fps(24), BitDepth: ip(10), HDRFormat: "hdr10"}
}

func audio(trackID int, codec string, ch int, def bool) *mediafile.Track {
	return &mediafile.Track{ID: uid.N(trackID), StreamIndex: trackID, Kind: mediafile.Audio, Codec: codec, Channels: ip(ch), Language: "eng", IsDefault: def}
}

func sub(trackID int, codec string, text, forced bool) *mediafile.Track {
	t := no()
	if text {
		t = yes()
	}
	return &mediafile.Track{ID: uid.N(trackID), StreamIndex: trackID, Kind: mediafile.Subtitle, Codec: codec, Language: "eng", IsForced: forced, SubtitleIsText: t}
}

// web is a local SDR browser: H.264 and HEVC Main 10 in hardware up to 4K,
// MP4 files, AAC only, stereo route, WebVTT subtitles.
func web() playback.Snapshot {
	return playback.Snapshot{
		Protocols: []string{"http", "hls"}, Containers: []string{"mp4"},
		Video: []playback.VideoDecoder{
			{Codec: "h264", Profiles: []string{"High", "Main"}, MaxLevel: 52, Hardware: true, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitDepth: 8},
			{Codec: "hevc", Hardware: true, MaxWidth: 3840, MaxHeight: 2160, MaxFrameRate: 60, MaxBitDepth: 10, HDR: []string{"hdr10"}},
		},
		Audio: []string{"aac"}, Subtitles: []string{"webvtt"},
		Display: playback.Display{Width: 2560, Height: 1440},
		Route:   playback.Route{Channels: 2},
		Network: playback.Network{Local: true},
	}
}

func hdrDisplay(s playback.Snapshot) playback.Snapshot {
	s.Display.HDR = []string{"hdr10"}
	return s
}

type want struct {
	verdicts []string
	check    func(t *testing.T, p []*session.PlanEntry)
}

func verdicts(p []*session.PlanEntry) []string {
	out := []string{}
	for _, e := range p {
		out = append(out, e.Verdict)
	}
	return out
}

const (
	dp = session.DirectPlay
	rx = session.Remux
	at = session.AudioTranscode
	vt = session.VideoTranscode
)

func TestDecide(t *testing.T) {
	mp4 := func(ts ...*mediafile.Track) playback.Media {
		return playback.Media{Container: "mp4", Bitrate: 10e6, Tracks: ts}
	}
	mkv := func(ts ...*mediafile.Track) playback.Media {
		return playback.Media{Container: "mkv", Bitrate: 38e6, Tracks: ts}
	}
	cases := []struct {
		name string
		m    playback.Media
		s    func() playback.Snapshot
		sel  playback.Selection
		want want
	}{
		{"direct play", mp4(h264(), audio(2, "aac", 2, true)), web, playback.Selection{}, want{[]string{dp, rx, at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].VideoAction != "copy" || p[0].AudioAction != "copy" || *p[0].AudioTrackID != uid.N(2) || p[0].TopHeight != 1080 || !p[0].SingleRung {
				t.Errorf("%+v", p[0])
			}
			if p[3].Encoder != session.EncoderHardware || p[4].Encoder != session.EncoderSoftware || p[3].VideoCodec != "h264" {
				t.Errorf("hardware encoder first, software second: %+v %+v", p[3], p[4])
			}
		}}},
		{"remux when the container is refused", mkv(h264(), audio(2, "aac", 2, true)), web, playback.Selection{}, want{[]string{rx, at, vt, vt}, nil}},
		{"remux when remote", mp4(h264(), audio(2, "aac", 2, true)), func() playback.Snapshot { s := web(); s.Network.Local = false; return s }, playback.Selection{},
			want{[]string{rx, at, vt, vt}, nil}},
		{"audio transcode with downmix", mp4(h264(), audio(2, "ac3", 6, true)), web, playback.Selection{}, want{[]string{at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].AudioAction != "convert" || p[0].AudioChannels != 2 {
				t.Errorf("AAC at the route's channel count: %+v", p[0])
			}
		}}},
		{"surround route keeps channels", mp4(h264(), audio(2, "ac3", 6, true)), func() playback.Snapshot { s := web(); s.Route.Channels = 8; return s }, playback.Selection{},
			want{[]string{at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[0].AudioChannels != 6 {
					t.Errorf("never upmix: %+v", p[0])
				}
			}}},
		{"passthrough copies an undecodable bitstream", mp4(h264(), audio(2, "ac3", 6, true)), func() playback.Snapshot { s := web(); s.Route.Passthrough = []string{"ac3"}; return s },
			playback.Selection{}, want{[]string{dp, rx, at, vt, vt}, nil}},
		{"alternative audio track before transcode", mkv(h264(), audio(2, "truehd", 8, true), audio(3, "aac", 2, false)), web, playback.Selection{},
			want{[]string{rx, at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if *p[0].AudioTrackID != uid.N(3) || p[0].AudioAction != "copy" || *p[1].AudioTrackID != uid.N(2) || p[1].AudioAction != "convert" {
					t.Errorf("copy the AAC, then convert the TrueHD: %+v %+v", p[0], p[1])
				}
			}}},
		{"MP3 converts for HLS even when the client decodes it", playback.Media{Container: "avi", Bitrate: 1e6, Tracks: []*mediafile.Track{
			{ID: uid.N(1), Kind: mediafile.Video, Codec: "mpeg4", Width: ip(576), Height: ip(432)}, audio(2, "mp3", 2, true)}},
			func() playback.Snapshot { s := web(); s.Audio = []string{"aac", "mp3"}; return s }, playback.Selection{},
			want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[0].AudioAction != "convert" {
					t.Errorf("MP3 in fMP4 is refused by Chrome and Safari: %+v", p[0])
				}
			}}},
		{"video transcode for an undecodable codec", mp4(&mediafile.Track{ID: uid.N(1), Kind: mediafile.Video, Codec: "vc1", Width: ip(1920), Height: ip(1080)}, audio(2, "aac", 2, true)), web,
			playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[0].VideoCodec != "h264" || p[0].AudioAction != "copy" || p[0].TopHeight != 1080 || p[0].ToneMap {
					t.Errorf("%+v", p[0])
				}
			}}},
		{"video transcode for a level above the decoder", mp4(func() *mediafile.Track { v := h264(); v.Level = ip(62); return v }(), audio(2, "aac", 2, true)), web,
			playback.Selection{}, want{[]string{vt, vt}, nil}},
		{"software decode is not good enough to copy", mp4(h264(), audio(2, "aac", 2, true)), func() playback.Snapshot { s := web(); s.Video[0].Hardware = false; return s },
			playback.Selection{}, want{[]string{vt, vt}, nil}},
		{"tone-map HDR on an SDR display", mkv(hevcHDR(), audio(2, "aac", 2, true)), web, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if !p[0].ToneMap || p[0].VideoCodec != "h264" {
				t.Errorf("%+v", p[0])
			}
		}}},
		{"HDR copies to an HDR display", mkv(hevcHDR(), audio(2, "aac", 2, true)), func() playback.Snapshot { return hdrDisplay(web()) }, playback.Selection{},
			want{[]string{rx, at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[2].ToneMap || p[2].VideoCodec != "hevc" {
					t.Errorf("a convert for an HDR client keeps HDR10: %+v", p[2])
				}
			}}},
		{"Dolby Vision profile 7 converts for an HDR10 client", mkv(func() *mediafile.Track {
			v := hevcHDR()
			v.HDRFormat, v.DVProfile = "dolby_vision", ip(7)
			return v
		}(), audio(2, "aac", 2, true)), func() playback.Snapshot { return hdrDisplay(web()) }, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].ToneMap || p[0].VideoCodec != "hevc" {
				t.Errorf("strip the enhancement layer, keep HDR10: %+v", p[0])
			}
		}}},
		{"Dolby Vision profile 8 plays as HDR10", mkv(func() *mediafile.Track {
			v := hevcHDR()
			v.HDRFormat, v.DVProfile = "dolby_vision", ip(8)
			return v
		}(), audio(2, "aac", 2, true)), func() playback.Snapshot { return hdrDisplay(web()) }, playback.Selection{}, want{[]string{rx, at, vt, vt}, nil}},
		{"Dolby Vision on an HLG base, as an iPhone records, converts as HLG does", mkv(func() *mediafile.Track {
			v := hevcHDR()
			v.HDRFormat, v.DVProfile, v.ColorTransfer = "dolby_vision", ip(8), "arib-std-b67"
			return v
		}(), audio(2, "aac", 2, true)), func() playback.Snapshot {
			s := hdrDisplay(web())
			s.Network.MaxBitrate = 8e6
			return s
		}, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if !p[0].ToneMap || p[0].VideoCodec != "h264" {
				t.Errorf("HLG pixels are never tagged PQ: %+v", p[0])
			}
		}}},
		{"forced subtitle selected as a sidecar", mp4(h264(), audio(2, "aac", 2, true), sub(3, "subrip", true, false), sub(4, "subrip", true, true)), web, playback.Selection{},
			want{[]string{dp, rx, at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				for _, e := range p {
					if e.SubtitleAction != session.SubtitleSidecar || *e.SubtitleTrackID != uid.N(4) {
						t.Errorf("%+v", e)
					}
				}
			}}},
		{"no forced track means no subtitle", mp4(h264(), audio(2, "aac", 2, true), sub(3, "subrip", true, false)), web, playback.Selection{},
			want{[]string{dp, rx, at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[0].SubtitleAction != session.SubtitleNone || p[0].SubtitleTrackID != nil {
					t.Errorf("%+v", p[0])
				}
			}}},
		{"chosen subtitle overrides forced", mp4(h264(), audio(2, "aac", 2, true), sub(3, "subrip", true, false), sub(4, "subrip", true, true)), web,
			playback.Selection{SubtitleTrackID: id(3)}, want{[]string{dp, rx, at, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if *p[0].SubtitleTrackID != uid.N(3) {
					t.Errorf("%+v", p[0])
				}
			}}},
		{"bitmap subtitle burns in", mkv(h264(), audio(2, "aac", 2, true), sub(3, "hdmv_pgs_subtitle", false, true)), web, playback.Selection{},
			want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[0].SubtitleAction != session.SubtitleBurn || *p[0].SubtitleTrackID != uid.N(3) {
					t.Errorf("%+v", p[0])
				}
			}}},
		{"bitmap burn-in is the last rung for a client that renders it", mp4(h264(), audio(2, "aac", 2, true), sub(3, "hdmv_pgs_subtitle", false, true)),
			func() playback.Snapshot { s := web(); s.Subtitles = []string{"webvtt", "pgs"}; return s }, playback.Selection{},
			want{[]string{dp, vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[0].SubtitleAction != session.SubtitleNone || *p[0].SubtitleTrackID != uid.N(3) || p[2].SubtitleAction != session.SubtitleBurn {
					t.Errorf("%+v %+v", p[0], p[2])
				}
			}}},
		{"quality cap transcodes down", mkv(h264(), audio(2, "aac", 2, true)), func() playback.Snapshot {
			s := web()
			s.Network = playback.Network{MaxBitrate: 4e6}
			return s
		}, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].TopHeight != 720 || p[0].TopBitrate != 4e6 || p[0].SingleRung {
				t.Errorf("%+v", p[0])
			}
		}}},
		{"metered starts at 720p", mkv(h264(), audio(2, "aac", 2, true)), func() playback.Snapshot {
			s := web()
			s.Network = playback.Network{Metered: true}
			return s
		}, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].TopHeight != 720 {
				t.Errorf("%+v", p[0])
			}
		}}},
		{"low data starts at 480p", mkv(h264(), audio(2, "aac", 2, true)), func() playback.Snapshot {
			s := web()
			s.Network = playback.Network{LowData: true, Metered: true}
			return s
		}, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].TopHeight != 480 {
				t.Errorf("%+v", p[0])
			}
		}}},
		{"never upscale past the source", mp4(&mediafile.Track{ID: uid.N(1), Kind: mediafile.Video, Codec: "mpeg4", Width: ip(640), Height: ip(360)}, audio(2, "aac", 2, true)), web,
			playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
				if p[0].TopHeight != 360 || !p[0].SingleRung {
					t.Errorf("%+v", p[0])
				}
			}}},
		{"client maximum caps the top rung", mkv(hevcHDR(), audio(2, "aac", 2, true)), func() playback.Snapshot {
			s := web()
			s.Video = s.Video[:1]
			s.Video[0].MaxHeight, s.Video[0].MaxWidth = 1080, 1920
			return s
		}, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].TopHeight != 1080 || !p[0].ToneMap {
				t.Errorf("%+v", p[0])
			}
		}}},
		{"client maximum width caps a wide source's top rung", mkv(func() *mediafile.Track { v := hevcHDR(); v.Height = ip(1600); return v }(), audio(2, "aac", 2, true)), func() playback.Snapshot {
			s := web()
			s.Video = s.Video[:1]
			s.Video[0].MaxHeight, s.Video[0].MaxWidth = 1080, 1920
			return s
		}, playback.Selection{}, want{[]string{vt, vt}, func(t *testing.T, p []*session.PlanEntry) {
			if p[0].TopHeight != 800 {
				t.Errorf("%+v", p[0])
			}
		}}},
		{"no HLS and no direct play leaves nothing", mkv(h264(), audio(2, "aac", 2, true)), func() playback.Snapshot { s := web(); s.Protocols = []string{"http"}; return s },
			playback.Selection{}, want{[]string{}, nil}},
		{"an unprobed file tries direct play only", playback.Media{Container: ""}, web, playback.Selection{}, want{[]string{dp}, nil}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := playback.Decide(c.m, c.s(), c.sel)
			if got := verdicts(p); !reflect.DeepEqual(got, c.want.verdicts) {
				t.Fatalf("verdicts = %v, want %v\n%+v", got, c.want.verdicts, p)
			}
			if c.want.check != nil {
				c.want.check(t, p)
			}
		})
	}
}

// The Apple TV HD's stated decoder: H.264 to 1080p60 at level 4.2, HEVC SDR to 1080p30.
func TestDecideForAppleTVHD(t *testing.T) {
	s := web()
	s.Video = []playback.VideoDecoder{
		{Codec: "h264", Profiles: []string{"Main", "High"}, MaxLevel: 42, Hardware: true, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 60, MaxBitDepth: 8},
		{Codec: "hevc", Profiles: []string{"Main", "Main 10"}, Hardware: true, MaxWidth: 1920, MaxHeight: 1080, MaxFrameRate: 30, MaxBitDepth: 10},
	}
	uhd := func(codec, profile string, level int) *mediafile.Track {
		return &mediafile.Track{ID: uid.N(1), Kind: mediafile.Video, Codec: codec, Profile: profile, Level: ip(level),
			Width: ip(3840), Height: ip(2160), FrameRate: fps(24), BitDepth: ip(8)}
	}
	for _, v := range []*mediafile.Track{uhd("h264", "High", 51), uhd("hevc", "Main", 153)} {
		p := playback.Decide(playback.Media{Container: "mp4", Bitrate: 30e6, Tracks: []*mediafile.Track{v, audio(2, "aac", 2, true)}}, s, playback.Selection{})
		if got := verdicts(p); !reflect.DeepEqual(got, []string{vt, vt}) || p[0].TopHeight != 1080 || p[0].VideoCodec != "h264" {
			t.Errorf("4K %s converts to 1080p H.264: %v %+v", v.Codec, got, p)
		}
	}
	hd := h264()
	hd.Level = ip(42)
	p := playback.Decide(playback.Media{Container: "mp4", Bitrate: 10e6, Tracks: []*mediafile.Track{hd, audio(2, "aac", 2, true)}}, s, playback.Selection{})
	if len(p) == 0 || p[0].Verdict != dp {
		t.Errorf("1080p H.264 at level 4.2 plays directly: %v", verdicts(p))
	}
}

func TestWithout(t *testing.T) {
	p := playback.Decide(playback.Media{Container: "mp4", Tracks: []*mediafile.Track{h264(), audio(2, "aac", 2, true)}}, web(), playback.Selection{})
	bad := []*session.PlanEntry{{Verdict: dp, VideoAction: "copy", AudioAction: "copy", SubtitleAction: "none", AudioTrackID: id(2), TopHeight: 1080, SingleRung: true}}
	if got := verdicts(playback.Without(p, bad)); !reflect.DeepEqual(got, []string{rx, at, vt, vt}) {
		t.Fatal(got)
	}
	all := append([]*session.PlanEntry{}, p...)
	if got := playback.Without(p, all); len(got) != len(p) {
		t.Fatal("excluding everything keeps the plan rather than leave nothing to try")
	}
}

func TestRungs(t *testing.T) {
	v := h264()
	copyEntry := &session.PlanEntry{VideoAction: "copy", TopHeight: 1080, TopBitrate: 10e6, SingleRung: true}
	if r := playback.Rungs(copyEntry, v); len(r) != 1 || r[0].Width != 1920 || r[0].Bitrate != 10e6 {
		t.Fatalf("%+v", r)
	}
	conv := &session.PlanEntry{VideoAction: "convert", TopHeight: 1080, TopBitrate: 8e6}
	r := playback.Rungs(conv, v)
	want := []playback.Rung{{Width: 1920, Height: 1080, Bitrate: 8e6}, {Width: 1280, Height: 720, Bitrate: 4e6}, {Width: 854, Height: 480, Bitrate: 1.5e6}, {Width: 640, Height: 360, Bitrate: 800e3}}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("%+v", r)
	}
	conv.SingleRung = true
	if r := playback.Rungs(conv, v); len(r) != 1 {
		t.Fatalf("local sessions get a single rung: %+v", r)
	}
}

func TestMenu(t *testing.T) {
	m := playback.Media{Container: "mp4", Bitrate: 10e6, Tracks: []*mediafile.Track{h264(), audio(2, "aac", 2, true)}}
	s := web()
	s.Network.MaxBitrate = 4e6
	r := playback.Menu(m, s, playback.Selection{})
	if len(r) != 4 || r[0].Height != 1080 || r[0].Verdict != dp || r[0].Bitrate != 10e6 || r[1].Height != 720 || r[1].Verdict != vt {
		t.Fatalf("Original first at what it would be without the cap, then lower rungs: %+v", r)
	}
	small := h264()
	small.Width, small.Height = ip(1280), ip(720)
	def := h264()
	def.ID, def.StreamIndex, def.IsDefault = uid.N(3), 2, true
	m.Tracks = []*mediafile.Track{small, audio(2, "aac", 2, true), def}
	if r := playback.Menu(m, s, playback.Selection{}); r[0].Height != 1080 {
		t.Fatalf("Original is the default video track the plan was decided for: %+v", r[0])
	}
}

func TestSameLAN(t *testing.T) {
	_, home, _ := net.ParseCIDR("192.168.1.0/24")
	nets := []*net.IPNet{home}
	cases := []struct {
		addr       string
		discovered bool
		want       bool
	}{
		{"192.168.1.20", true, true},
		{"192.168.1.20", false, false},
		{"10.8.0.2", true, false},
		{"203.0.113.9", false, false},
	}
	for _, c := range cases {
		if got := playback.SameLAN(net.ParseIP(c.addr), nets, c.discovered); got != c.want {
			t.Errorf("%s discovered=%v: got %v", c.addr, c.discovered, got)
		}
	}
	if len(playback.InterfaceSubnets()) == 0 {
		t.Error("at least loopback is an interface subnet")
	}
}

func TestReasonExplainsNonDirectVerdicts(t *testing.T) {
	m := playback.Media{Container: "matroska", Bitrate: 30e6, Tracks: []*mediafile.Track{hevcHDR(), audio(2, "truehd", 8, true), sub(3, "hdmv_pgs_subtitle", false, true)}}
	s := web()
	s.Network.MaxBitrate = 20e6
	plan := playback.Decide(m, s, playback.Selection{})
	got := playback.Reason(m, s, playback.Selection{}, plan[0])
	for _, frag := range []string{"bitrate 30 Mb/s exceeds the 20 Mb/s cap", "audio truehd 8ch", "bitmap subtitle (hdmv_pgs_subtitle)"} {
		if !strings.Contains(got, frag) {
			t.Errorf("%q lacks %q", got, frag)
		}
	}
	direct := playback.Media{Container: "mp4", Tracks: []*mediafile.Track{h264(), audio(2, "aac", 2, true)}}
	if plan := playback.Decide(direct, web(), playback.Selection{}); playback.Reason(direct, web(), playback.Selection{}, plan[0]) != "" {
		t.Error("direct play needs no reason")
	}
	auto := path(false, yes(), 0, false, false)
	if plan := playback.Decide(direct, auto, playback.Selection{}); playback.Reason(direct, auto, playback.Selection{}, plan[0]) != "Auto away from home" {
		t.Errorf("an Auto transcode is no decoder's fault: %q", playback.Reason(direct, auto, playback.Selection{}, plan[0]))
	}
	mkv := playback.Media{Container: "matroska", Tracks: []*mediafile.Track{h264(), audio(2, "aac", 2, true)}}
	plan = playback.Decide(mkv, web(), playback.Selection{})
	if r := playback.Reason(mkv, web(), playback.Selection{}, plan[0]); plan[0].Verdict != session.Remux || !strings.Contains(r, "container matroska") {
		t.Errorf("%s: %q", plan[0].Verdict, r)
	}
}

func uhd() *mediafile.Track {
	return &mediafile.Track{ID: uid.N(1), StreamIndex: 0, Kind: mediafile.Video, Codec: "h264", Profile: "High", Level: ip(51),
		Width: ip(3840), Height: ip(2160), FrameRate: fps(24), BitDepth: ip(8)}
}

// path is web() on a network path; auto nil is a v1 client.
func path(local bool, auto *bool, maxBitrate int64, metered, lowData bool) playback.Snapshot {
	s := web()
	s.Network = playback.Network{Local: local, Auto: auto, MaxBitrate: maxBitrate, Metered: metered, LowData: lowData}
	return s
}

func heights(r []playback.Rung) []int {
	out := []int{}
	for _, x := range r {
		out = append(out, x.Height)
	}
	return out
}

// Adaptive quality: Auto away from home is the ladder the player moves on,
// Original stays a copy, a fixed pick is one rung, and at home nothing
// changes.
func TestAdaptive(t *testing.T) {
	m := playback.Media{Container: "mkv", Bitrate: 38e6, Tracks: []*mediafile.Track{uhd(), audio(2, "aac", 2, true)}}
	cases := []struct {
		name     string
		s        playback.Snapshot
		verdicts []string
		top      int
		bitrate  int64
		rungs    []int // of the first entry
		auto     bool
	}{
		{"home Original", path(true, no(), 0, false, false), []string{rx, at, vt, vt}, 2160, 38e6, []int{2160}, false},
		{"home Auto plays as Original", path(true, yes(), 0, false, false), []string{rx, at, vt, vt}, 2160, 38e6, []int{2160}, false},
		{"home fixed pick", path(true, no(), 4e6, false, false), []string{vt, vt}, 720, 4e6, []int{720}, false},
		{"away Original stays a remux", path(false, no(), 0, false, false), []string{rx, at, vt, vt}, 2160, 38e6, []int{2160}, false},
		{"away Auto", path(false, yes(), 0, false, false), []string{vt, vt, rx, at}, 1080, 8e6, []int{1080, 720, 480, 360}, true},
		{"away Auto metered", path(false, yes(), 0, true, false), []string{vt, vt}, 720, 4e6, []int{720, 480, 360}, true},
		{"away Auto low data", path(false, yes(), 0, true, true), []string{vt, vt}, 480, 1.5e6, []int{480, 360}, true},
		{"away fixed pick is one rung", path(false, no(), 4e6, false, false), []string{vt, vt}, 720, 4e6, []int{720}, false},
		{"away fixed pick above the metered cap", path(false, no(), 8e6, true, false), []string{vt, vt}, 720, 4e6, []int{720}, false},
		{"v1 cap away keeps the rungs below it", path(false, nil, 4e6, false, false), []string{vt, vt}, 720, 4e6, []int{720, 480, 360}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := playback.Decide(m, c.s, playback.Selection{})
			if got := verdicts(p); !reflect.DeepEqual(got, c.verdicts) {
				t.Fatalf("verdicts = %v, want %v", got, c.verdicts)
			}
			if p[0].TopHeight != c.top || p[0].TopBitrate != c.bitrate {
				t.Errorf("top %dp at %d, want %dp at %d", p[0].TopHeight, p[0].TopBitrate, c.top, c.bitrate)
			}
			if got := heights(playback.Rungs(p[0], uhd())); !reflect.DeepEqual(got, c.rungs) {
				t.Errorf("rungs %v, want %v", got, c.rungs)
			}
			if got := playback.Auto(c.s, p[0]); got != c.auto {
				t.Errorf("auto = %v", got)
			}
		})
	}

	small := playback.Media{Container: "mkv", Bitrate: 3e6, Tracks: []*mediafile.Track{func() *mediafile.Track {
		v := h264()
		v.Width, v.Height = ip(1280), ip(720)
		return v
	}(), audio(2, "aac", 2, true)}}
	if p := playback.Decide(small, path(false, yes(), 0, false, false), playback.Selection{}); p[0].TopHeight != 720 || p[0].TopBitrate != 4e6 {
		t.Errorf("Auto never climbs above the source: %+v", p[0])
	}

	floor := path(false, yes(), 0, false, false)
	floor.Floor = true
	p := playback.Decide(m, floor, playback.Selection{})
	if r := playback.Rungs(p[0], uhd()); !reflect.DeepEqual(heights(r), []int{1080, 720, 480, 360, 240}) || r[4].Bitrate != 400e3 || r[4].Width != 426 {
		t.Errorf("the floor adds 240p at 0.4 Mb/s under the Auto ladder: %+v", r)
	}
	if !p[0].Floor || p[2].Floor {
		t.Errorf("only Auto entries carry the floor: %+v %+v", p[0], p[2])
	}
	tiny := &mediafile.Track{Kind: mediafile.Video, Codec: "mpeg2video", Width: ip(320), Height: ip(180)}
	p = playback.Decide(playback.Media{Container: "mkv", Bitrate: 1e6, Tracks: []*mediafile.Track{tiny, audio(2, "aac", 2, true)}}, floor, playback.Selection{})
	if r := playback.Rungs(p[0], tiny); !reflect.DeepEqual(heights(r), []int{180}) {
		t.Errorf("the floor never upscales: %+v", r)
	}
	fixed := path(false, no(), 4e6, false, false)
	fixed.Floor = true
	if r := playback.Rungs(playback.Decide(m, fixed, playback.Selection{})[0], uhd()); len(r) != 1 {
		t.Errorf("a fixed pick never gets the floor: %+v", r)
	}
}

func TestMenuAdaptive(t *testing.T) {
	m := playback.Media{Container: "mkv", Bitrate: 38e6, Tracks: []*mediafile.Track{uhd(), audio(2, "aac", 2, true)}}
	type row struct {
		height  int
		verdict string
		blocked bool
	}
	rows := func(r []playback.Rung) []row {
		out := []row{}
		for _, x := range r {
			out = append(out, row{x.Height, x.Verdict, x.Blocked})
		}
		return out
	}
	cases := []struct {
		name string
		s    playback.Snapshot
		want []row
	}{
		{"home", path(true, yes(), 0, false, false), []row{{2160, rx, false}, {1080, vt, false}, {720, vt, false}, {480, vt, false}, {360, vt, false}}},
		{"away", path(false, yes(), 0, false, false), []row{{2160, rx, false}, {1080, vt, false}, {720, vt, false}, {480, vt, false}, {360, vt, false}}},
		{"away on a fixed pick", path(false, no(), 1.5e6, false, false), []row{{2160, rx, false}, {1080, vt, false}, {720, vt, false}, {480, vt, false}, {360, vt, false}}},
		{"metered blocks above 4 Mb/s", path(false, yes(), 0, true, false), []row{{2160, rx, true}, {1080, vt, true}, {720, vt, false}, {480, vt, false}, {360, vt, false}}},
		{"low data blocks above 1.5 Mb/s", path(false, no(), 0, true, true), []row{{2160, rx, true}, {1080, vt, true}, {720, vt, true}, {480, vt, false}, {360, vt, false}}},
		{"metered at home blocks too", path(true, no(), 0, true, false), []row{{2160, dp, true}, {1080, vt, true}, {720, vt, false}, {480, vt, false}, {360, vt, false}}},
		{"v1 metered shows Original as the capped transcode", path(false, nil, 0, true, false), []row{{720, vt, false}, {480, vt, false}, {360, vt, false}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "metered at home blocks too" {
				m.Container = "mp4"
				defer func() { m.Container = "mkv" }()
			}
			if got := rows(playback.Menu(m, c.s, playback.Selection{})); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("menu %v, want %v", got, c.want)
			}
		})
	}
}

// A client that plays only MPEG-TS gets TS segments for every HLS entry,
// and audio TS HLS cannot carry converts.
func TestDecideTS(t *testing.T) {
	ts := func(s playback.Snapshot) playback.Snapshot { s.Segments = []string{"ts"}; return s }
	s := ts(web())
	s.Audio = []string{"aac", "flac", "opus"}
	p := playback.Decide(playback.Media{Container: "mp4", Bitrate: 5e6, Tracks: []*mediafile.Track{h264(), audio(2, "aac", 2, true)}}, s, playback.Selection{})
	if got := verdicts(p); !reflect.DeepEqual(got, []string{dp, rx, at, vt, vt}) {
		t.Fatal(got)
	}
	if p[0].TS || !p[1].TS || !p[2].TS || !p[3].TS {
		t.Errorf("TS for HLS entries only: %+v", p)
	}
	for _, codec := range []string{"flac", "opus"} {
		m := playback.Media{Container: "mkv", Bitrate: 5e6, Tracks: []*mediafile.Track{h264(), audio(2, codec, 2, true)}}
		if got := verdicts(playback.Decide(m, s, playback.Selection{})); !reflect.DeepEqual(got, []string{at, vt, vt}) {
			t.Errorf("%s in TS converts: %v", codec, got)
		}
		fmp4 := s
		fmp4.Segments = []string{"ts", "fmp4"}
		if got := verdicts(playback.Decide(m, fmp4, playback.Selection{})); !reflect.DeepEqual(got, []string{rx, at, vt, vt}) {
			t.Errorf("%s in fMP4 copies: %v", codec, got)
		}
	}
	for _, codec := range []string{"ac3", "eac3", "mp3"} {
		s := s
		s.Audio = append(s.Audio, codec)
		m := playback.Media{Container: "mkv", Bitrate: 5e6, Tracks: []*mediafile.Track{h264(), audio(2, codec, 2, true)}}
		if got := verdicts(playback.Decide(m, s, playback.Selection{})); !reflect.DeepEqual(got, []string{rx, at, vt, vt}) {
			t.Errorf("%s copies into TS: %v", codec, got)
		}
	}
	if p := playback.Decide(playback.Media{Container: "mkv", Bitrate: 5e6, Tracks: []*mediafile.Track{h264(), audio(2, "aac", 2, true)}}, web(), playback.Selection{}); p[0].TS {
		t.Error("no segment formats means fMP4")
	}
}

// player is a local music client: FLAC, MP3 and MP4 files, FLAC, ALAC, AAC
// and MP3, stereo.
func player() playback.Snapshot {
	return playback.Snapshot{Protocols: []string{"http", "hls"}, Containers: []string{"flac", "mp3", "mp4"},
		Audio: []string{"flac", "alac", "aac", "mp3"}, Route: playback.Route{Channels: 2}, Network: playback.Network{Local: true}}
}

// Songs follow the rule decideSong's doc comment gives.
func TestDecideSong(t *testing.T) {
	song := func(container, codec string) playback.Media {
		return playback.Media{Container: container, Bitrate: 900e3, Tracks: []*mediafile.Track{audio(1, codec, 2, true)}}
	}
	away := func(s playback.Snapshot) playback.Snapshot { s.Network.Local = false; return s }
	metered := func(s playback.Snapshot) playback.Snapshot { s.Network.Metered = true; return s }
	lowData := func(s playback.Snapshot) playback.Snapshot { s.Network.LowData = true; return s }
	noFLAC := player()
	noFLAC.Audio = []string{"aac", "mp3"}
	fileOnly := player()
	fileOnly.Protocols = []string{"http"}
	cases := []struct {
		name    string
		m       playback.Media
		s       playback.Snapshot
		want    []string
		bitrate []int64
	}{
		{"home lossless", song("flac", "flac"), player(), []string{dp, rx, at}, []int64{900e3, 900e3, 256e3}},
		{"home undecodable", song("flac", "flac"), noFLAC, []string{at}, []int64{256e3}},
		{"away lossless", song("flac", "flac"), away(player()), []string{at}, []int64{256e3}},
		{"away lossy", song("mp3", "mp3"), away(player()), []string{dp, at}, []int64{900e3, 256e3}},
		{"away aac in mp4", song("mp4", "aac"), away(player()), []string{dp, rx, at}, []int64{900e3, 900e3, 256e3}},
		{"metered home", song("flac", "flac"), metered(player()), []string{at}, []int64{128e3}},
		{"low data away", song("mp3", "mp3"), lowData(away(player())), []string{at}, []int64{128e3}},
		{"file only and lossless away", song("flac", "flac"), away(fileOnly), []string{}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := playback.Decide(c.m, c.s, playback.Selection{})
			if got := verdicts(p); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("verdicts %v, want %v", got, c.want)
			}
			for i, e := range p {
				convert := e.Verdict == at
				if e.TopBitrate != c.bitrate[i] || e.VideoAction != session.ActionCopy || !e.SingleRung || e.AudioTrackID == nil ||
					convert != (e.AudioAction == session.ActionConvert) || convert && e.AudioChannels != 2 || e.SubtitleAction != session.SubtitleNone {
					t.Errorf("entry %d %+v", i, e)
				}
			}
		})
	}
}

func TestReasonForSongs(t *testing.T) {
	flac := playback.Media{Container: "flac", Tracks: []*mediafile.Track{audio(1, "flac", 2, true)}}
	s := player()
	s.Network.Local = false
	p := playback.Decide(flac, s, playback.Selection{})
	if got := playback.Reason(flac, s, playback.Selection{}, p[0]); got != "lossless audio away from home converts to AAC" {
		t.Errorf("away: %q", got)
	}
	s.Network.Metered = true
	if got := playback.Reason(flac, s, playback.Selection{}, p[0]); got != "a metered or low-data path converts to AAC" {
		t.Errorf("metered: %q", got)
	}
	s = player()
	s.Audio = []string{"aac"}
	if got := playback.Reason(flac, s, playback.Selection{}, p[0]); got != "audio flac 2ch is not playable on this route" {
		t.Errorf("undecodable: %q", got)
	}
}
