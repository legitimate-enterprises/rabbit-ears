package playback

import (
	"slices"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// Option is one copy of an edition and the plan decided for it.
type Option struct {
	Media Media
	Plan  []*session.PlanEntry
}

// rank orders options: earlier fields weigh more, and higher is better.
type rank [6]int64

// Best is the index of the copy to play, -1 when no plan has an entry. A
// copy holding a track the viewer chose wins outright, as track ids belong
// to one file. Then the cheapest first verdict; within one, the tallest
// picture the display shows, then HDR the display keeps, then the least
// picture beyond what the display shows, then the higher bitrate. Ties keep
// the earlier copy.
func Best(opts []Option, s Snapshot, sel Selection) int {
	best, top := -1, rank{}
	for i, o := range opts {
		if len(o.Plan) == 0 {
			continue
		}
		r := rankOf(o, s, sel)
		if best < 0 || slices.Compare(r[:], top[:]) > 0 {
			best, top = i, r
		}
	}
	return best
}

func rankOf(o Option, s Snapshot, sel Selection) rank {
	e := o.Plan[0]
	var r rank
	for _, t := range o.Media.Tracks {
		if sel.AudioTrackID != nil && t.ID == *sel.AudioTrackID || sel.SubtitleTrackID != nil && t.ID == *sel.SubtitleTrackID {
			r[0] = 1
		}
	}
	r[1] = -int64(cost[e.Verdict])
	shown := e.TopHeight
	if s.Display.Height > 0 {
		shown = min(shown, s.Display.Height)
	}
	r[2] = int64(shown)
	if src := VideoTrack(o.Media); src != nil {
		if src.HDRFormat != "" && !e.ToneMap && (e.VideoAction == session.ActionCopy || e.VideoCodec == "hevc") {
			r[3] = 1
		}
		r[4] = -int64(max(deref(src.Height, 0)-shown, 0))
	}
	r[5] = o.Media.Bitrate
	return r
}
