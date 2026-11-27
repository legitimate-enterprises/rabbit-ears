package playback_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
)

// file is an mp4 or mkv copy with this video and an AAC stereo track whose
// id is audioID.
func file(container string, v *mediafile.Track, audioID int, bitrate int64) playback.Media {
	return playback.Media{Container: container, Bitrate: bitrate, Tracks: []*mediafile.Track{v, audio(audioID, "aac", 2, true)}}
}

func sized(v *mediafile.Track, w, h int) *mediafile.Track {
	v.Width, v.Height = ip(w), ip(h)
	return v
}

// Best ranks copies in the order its doc comment gives.
func TestBestPicksTheCopyForTheDevice(t *testing.T) {
	hd, uhdMKV := file("mp4", h264(), 11, 8e6), file("mkv", hevcHDR(), 12, 40e6)
	small := file("mp4", sized(h264(), 1280, 720), 13, 4e6)
	uhd := file("mp4", sized(h264(), 3840, 2160), 14, 30e6)
	uhdHDR := file("mp4", hevcHDR(), 15, 30e6)
	tv1080 := web()
	tv1080.Display = playback.Display{Width: 1920, Height: 1080}
	cases := []struct {
		name  string
		media []playback.Media
		snap  playback.Snapshot
		sel   playback.Selection
		want  int
	}{
		{"direct play beats remux", []playback.Media{uhdMKV, hd}, web(), playback.Selection{}, 1},
		{"taller picture", []playback.Media{small, hd}, web(), playback.Selection{}, 1},
		{"no excess over the display", []playback.Media{uhd, hd}, tv1080, playback.Selection{}, 1},
		{"HDR the display keeps", []playback.Media{uhd, uhdHDR}, hdrDisplay(web()), playback.Selection{}, 1},
		{"a chosen track pins its copy", []playback.Media{hd, uhdMKV}, web(), playback.Selection{AudioTrackID: id(12)}, 1},
	}
	for _, c := range cases {
		var opts []playback.Option
		for _, m := range c.media {
			opts = append(opts, playback.Option{Media: m, Plan: playback.Decide(m, c.snap, c.sel)})
		}
		if got := playback.Best(opts, c.snap, c.sel); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if got := playback.Best([]playback.Option{{Media: hd}}, web(), playback.Selection{}); got != -1 {
		t.Errorf("nothing playable: %d", got)
	}
}
