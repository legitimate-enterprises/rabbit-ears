package playback

import (
	"fmt"
	"slices"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// Download qualities besides the smaller video sizes, which are named by
// their rung's height, such as 720p.
const (
	Original = "original"
	AAC256   = "aac256"
)

// smaller is the quality menu's rungs below Original, by name.
func smaller() map[string]Rung {
	out := map[string]Rung{}
	for _, r := range ladder[2:] {
		out[fmt.Sprintf("%dp", r.height)] = Rung{Height: r.height, Bitrate: r.bitrate}
	}
	return out
}

// DownloadPlan is the one file a device keeps of a title: the file itself
// when Original, else Entry converted into an MP4, or an M4A for a song.
// Bytes is the file's size when Original and a prediction otherwise.
// Quality is the quality the file is at: the size asked for, or Original
// when that is no smaller, or AAC256.
type DownloadPlan struct {
	Original  bool
	Entry     *session.PlanEntry
	Container string
	Bytes     int64
	Quality   string
}

// Kept is s as a downloaded file meets it: a plain file read from the
// device itself, later, through whatever route then plays it, which
// downmixes what it decodes.
func Kept(s Snapshot) Snapshot {
	if !has(s.Protocols, "http") {
		s.Protocols = append(slices.Clone(s.Protocols), "http")
	}
	s.Network, s.Route, s.Segments, s.Floor = Network{Local: true}, Route{Channels: 8}, nil, false
	return s
}

// fileVideo is what a converted file's video may be copied as.
var fileVideo = []string{"h264", "hevc"}

// DecideDownload is the file a device keeps of m at quality q. Original is
// the file itself when the device plays it, else the cheapest conversion it
// plays. A smaller size converts video to that rung, never above the
// source, and audio to stereo AAC; a rung no smaller than the source is
// Original. For a song any quality but Original is AAC 256: lossless
// converts and lossy stays itself, as away from home. A forced bitmap
// subtitle is burned in; text subtitles go beside the file. It reports
// false when the device plays nothing m could become.
func DecideDownload(m Media, s Snapshot, sel Selection, q string) (DownloadPlan, bool) {
	k := Kept(s)
	sel.SubtitleTrackID = nil
	orig := DownloadPlan{Original: true, Container: m.Container, Bytes: m.SizeBytes, Quality: Original}
	if len(m.Tracks) == 0 {
		return orig, true
	}
	if Song(m) {
		if q != Original {
			k.Network.Local = false
		}
		plan := decideSong(m, k, sel)
		if len(plan) > 0 && plan[0].Verdict == session.DirectPlay {
			return orig, true
		}
		a := AudioTrack(m, sel.AudioTrackID)
		e := &session.PlanEntry{Verdict: session.AudioTranscode, VideoAction: session.ActionCopy, AudioAction: session.ActionConvert,
			SubtitleAction: session.SubtitleNone, AudioTrackID: &a.ID, AudioChannels: min(deref(a.Channels, 2), 2), TopBitrate: SongBitrate, SingleRung: true}
		return DownloadPlan{Entry: e, Container: "m4a", Bytes: predict(m, e), Quality: AAC256}, true
	}
	rung, small := smaller()[q]
	small = small && (m.Bitrate == 0 || m.Bitrate > rung.Bitrate)
	if !small {
		if plan := Decide(m, k, sel); len(plan) > 0 && plan[0].Verdict == session.DirectPlay {
			return orig, true
		}
	}
	d := &decision{m: m, s: k, video: VideoTrack(m)}
	if d.video == nil {
		return DownloadPlan{}, false
	}
	var audios, subs []*mediafile.Track
	for _, t := range m.Tracks {
		switch t.Kind {
		case mediafile.Audio:
			audios = append(audios, t)
		case mediafile.Subtitle:
			subs = append(subs, t)
		}
	}
	a := pickAudio(audios, sel.AudioTrackID)
	forced := pickSubtitle(subs, nil, a)
	burn := forced != nil && !deref(forced.SubtitleIsText, true)
	e := &session.PlanEntry{Verdict: session.Remux, VideoAction: session.ActionCopy, AudioAction: session.ActionCopy, SubtitleAction: session.SubtitleNone,
		TopHeight: deref(d.video.Height, 0), SingleRung: true}
	if small || burn || !has(fileVideo, d.video.Codec) || !d.videoCopyable() {
		opts := d.convertOptions()
		if len(opts) == 0 {
			return DownloadPlan{}, false
		}
		o := opts[0]
		e.Verdict, e.VideoAction, e.VideoCodec, e.Encoder, e.ToneMap, e.TopHeight, e.TopBitrate =
			session.VideoTranscode, session.ActionConvert, o.codec, o.encoder, o.toneMap, o.height, o.bitrate
		if small {
			e.TopHeight, e.TopBitrate = min(o.height, rung.Height), rung.Bitrate
		}
		if burn {
			e.SubtitleAction, e.SubtitleTrackID = session.SubtitleBurn, &forced.ID
		}
	}
	if a != nil {
		e.AudioTrackID = &a.ID
		ch := deref(a.Channels, 2)
		if a.Codec != "aac" || !has(k.Audio, "aac") || small && ch > 2 {
			e.AudioAction, e.AudioChannels = session.ActionConvert, min(ch, 6)
			if small {
				e.AudioChannels = min(ch, 2)
			}
			if e.Verdict == session.Remux {
				e.Verdict = session.AudioTranscode
			}
		}
	}
	q = Original
	if small {
		q = fmt.Sprintf("%dp", rung.Height)
	}
	return DownloadPlan{Entry: e, Container: "mp4", Bytes: predict(m, e), Quality: q}, true
}

// AudioBitrate is what converted audio of entry e is encoded at: a song's
// entry names it, and other audio scales with its channels up to a cap.
func AudioBitrate(e *session.PlanEntry, song bool) int64 {
	if song && e.TopBitrate > 0 {
		return e.TopBitrate
	}
	return int64(min(64000*max(e.AudioChannels, 1), 512000))
}

// predict is how big converting m as e makes it: its duration at the
// bitrates e encodes at and the bitrates of what it copies. Copied video
// runs at the file's bitrate less its audio tracks'.
func predict(m Media, e *session.PlanEntry) int64 {
	video := VideoTrack(m)
	var bps int64
	switch {
	case video == nil:
	case e.VideoAction == session.ActionConvert:
		bps += e.TopBitrate
	case video.Bitrate != nil:
		bps += *video.Bitrate
	default:
		v := m.Bitrate
		for _, t := range m.Tracks {
			if t.Kind == mediafile.Audio {
				v -= deref(t.Bitrate, 0)
			}
		}
		bps += max(v, 0)
	}
	if a := AudioTrack(m, e.AudioTrackID); a != nil {
		if e.AudioAction == session.ActionConvert {
			bps += AudioBitrate(e, video == nil)
		} else {
			bps += deref(a.Bitrate, 0)
		}
	}
	return m.DurationMs * bps / 8000
}
