package playback

import (
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// AAC bitrates a song converts at; the capped one on a metered or low-data
// path.
const (
	SongBitrate       = 256e3
	SongCappedBitrate = 128e3
)

// decideSong is the plan of a file with audio and no video. At home a
// track the client plays is copied, as the file or remuxed into HLS; away
// a lossless track converts and a lossy one is copied; on a metered or
// low-data path everything converts at the lower bitrate. Every plan ends
// on a conversion, when the client takes HLS.
func decideSong(m Media, s Snapshot, sel Selection) []*session.PlanEntry {
	var audios []*mediafile.Track
	for _, t := range m.Tracks {
		if t.Kind == mediafile.Audio {
			audios = append(audios, t)
		}
	}
	a := pickAudio(audios, sel.AudioTrackID)
	capped := s.Network.Metered || s.Network.LowData
	copyOK := !capped && (s.Network.Local || !mediafile.Lossless(a.Codec)) &&
		(has(s.Route.Passthrough, a.Codec) || has(s.Audio, a.Codec) && deref(a.Channels, 2) <= s.Route.Channels)
	ts := s.TS()
	segment := hlsAudio
	if ts {
		segment = tsAudio
	}
	var out []*session.PlanEntry
	add := func(verdict, action string, bitrate int64) {
		e := &session.PlanEntry{Verdict: verdict, VideoAction: session.ActionCopy, AudioAction: action, SubtitleAction: session.SubtitleNone,
			AudioTrackID: &a.ID, TopBitrate: bitrate, SingleRung: true, TS: ts && verdict != session.DirectPlay}
		if action == session.ActionConvert {
			e.AudioChannels = min(deref(a.Channels, 2), max(s.Route.Channels, 1), 2)
		}
		out = append(out, e)
	}
	if copyOK && has(s.Protocols, "http") && has(s.Containers, m.Container) {
		add(session.DirectPlay, session.ActionCopy, m.Bitrate)
	}
	if !has(s.Protocols, "hls") {
		return out
	}
	if copyOK && has(segment, a.Codec) {
		add(session.Remux, session.ActionCopy, m.Bitrate)
	}
	bitrate := int64(SongBitrate)
	if capped {
		bitrate = SongCappedBitrate
	}
	add(session.AudioTranscode, session.ActionConvert, bitrate)
	return out
}
