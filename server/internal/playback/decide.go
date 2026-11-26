// Package playback decides how a file reaches a client. It is pure: probed
// tracks and a capability snapshot in, an ordered fallback plan out. Each
// track is copied when the client can play it and converted otherwise; the
// plan lists the cheapest workable delivery first and every costlier one
// after it, so a failure moves down the list instead of re-deciding.
package playback

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// VideoDecoder is one codec the client decodes, with its limits.
type VideoDecoder struct {
	Codec        string
	Profiles     []string // empty means any
	MaxLevel     int      // 0 means any
	Hardware     bool
	MaxWidth     int
	MaxHeight    int
	MaxFrameRate float64
	MaxBitDepth  int
	HDR          []string
}

// Display is group B.
type Display struct {
	Width, Height int
	HDR           []string
}

// Route is group C.
type Route struct {
	Channels    int
	Passthrough []string
}

// Network is group D with the same-LAN rule already applied.
type Network struct {
	Local, Metered, LowData bool
	MaxBitrate              int64 // the viewer's cap; 0 means Original, or Auto
	// Auto is nil from a v1 client, which keeps v1's rules: a cap tops a
	// ladder, and no menu entry is blocked. From a v2 client true asks for
	// Auto, and false for Original or exactly the rung MaxBitrate names.
	Auto *bool
}

func (n Network) auto() bool { return deref(n.Auto, false) && !n.Local }

// Snapshot is groups A to D.
type Snapshot struct {
	Protocols  []string
	Containers []string
	Video      []VideoDecoder
	Audio      []string
	Subtitles  []string
	Display    Display
	Route      Route
	Network    Network
	Segments   []string // HLS segment formats; empty means fmp4 only
	Floor      bool     // Auto ladders gain the floor rung; set once a session needed it
}

// TS reports a client that plays HLS only as MPEG-TS segments.
func (s Snapshot) TS() bool { return len(s.Segments) > 0 && !has(s.Segments, "fmp4") }

// Selection is the viewer's track choice; nil audio picks the default
// track, nil subtitle picks the forced track if any.
type Selection struct {
	AudioTrackID    *int64
	SubtitleTrackID *int64
}

// Media is what the scan stored about one file.
type Media struct {
	Container  string
	Bitrate    int64
	SizeBytes  int64
	DurationMs int64
	Tracks     []*mediafile.Track
}

// Standard rungs below the top. Heights at or above 1440 only ever appear as
// the top rung.
var ladder = []struct {
	height  int
	bitrate int64
}{{2160, 20e6}, {1440, 12e6}, {1080, 8e6}, {720, 4e6}, {480, 1.5e6}, {360, 800e3}}

// Network classes cap the top rung so metered paths start lower.
const (
	meteredCap = 4e6
	lowDataCap = 1.5e6
)

// autoTop is the tallest rung of an Auto ladder.
const autoTop = 1080

// floor is the rung the slow loop adds under an Auto ladder.
var floor = Rung{Height: 240, Bitrate: 400e3}

// fMP4 HLS segments carry only these; anything else converts even when the
// client could decode it. MP3 is absent: Chrome and Safari refuse it in fMP4.
var (
	hlsVideo = []string{"h264", "hevc", "av1", "vp9"}
	hlsAudio = []string{"aac", "ac3", "eac3", "flac", "opus", "alac"}
	tsVideo  = []string{"h264", "hevc"}
	tsAudio  = []string{"aac", "ac3", "eac3", "mp3"}
)

// Bitmap subtitle codecs by the name clients report for them.
var bitmapNames = map[string]string{"hdmv_pgs_subtitle": "pgs", "dvd_subtitle": "vobsub", "dvb_subtitle": "dvbsub"}

var cost = map[string]int{session.DirectPlay: 0, session.Remux: 1, session.AudioTranscode: 2, session.VideoTranscode: 3}

func has(list []string, s string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

type decision struct {
	m       Media
	s       Snapshot
	video   *mediafile.Track
	cap     int64
	ts      bool
	entries []*session.PlanEntry
}

// Decide builds the ordered plan. An empty plan means nothing the client
// reported can play this file.
func Decide(m Media, s Snapshot, sel Selection) []*session.PlanEntry {
	if len(m.Tracks) == 0 {
		// Unprobed: there are no facts to decide on, so hand the file over.
		return []*session.PlanEntry{{Verdict: session.DirectPlay, VideoAction: session.ActionCopy, AudioAction: session.ActionCopy,
			SubtitleAction: session.SubtitleNone, SingleRung: true}}
	}
	if Song(m) {
		return decideSong(m, s, sel)
	}
	d := &decision{m: m, s: s, cap: bitrateCap(s.Network), ts: s.TS()}
	video, audioCodecs := hlsVideo, hlsAudio
	if d.ts {
		video, audioCodecs = tsVideo, tsAudio
	}
	d.video = VideoTrack(m)
	var audios, subs []*mediafile.Track
	for _, t := range m.Tracks {
		switch t.Kind {
		case mediafile.Audio:
			audios = append(audios, t)
		case mediafile.Subtitle:
			subs = append(subs, t)
		}
	}
	audio := pickAudio(audios, sel.AudioTrackID)
	subtitle := pickSubtitle(subs, sel.SubtitleTrackID, audio)
	bitmap := subtitle != nil && !deref(subtitle.SubtitleIsText, true)
	subAction := session.SubtitleNone
	if subtitle != nil {
		subAction = session.SubtitleSidecar
		if bitmap {
			subAction = session.SubtitleBurn
		}
	}
	videoCopy := d.videoCopyable()

	// Direct play hands over the file itself, so every track must play as
	// stored and the client must render a bitmap subtitle itself.
	directSub := subAction
	if bitmap {
		directSub = session.SubtitleNone
	}
	if videoCopy && (!bitmap || has(s.Subtitles, bitmapNames[subtitle.Codec])) && has(s.Protocols, "http") &&
		has(s.Containers, m.Container) && s.Network.Local {
		if a := d.audioOptions(audios, audio, nil); len(a) > 0 && a[0].action == session.ActionCopy {
			d.add(session.DirectPlay, d.copyVideo(), a[0], subtitle, directSub)
		}
	}
	if has(s.Protocols, "hls") {
		opts := d.audioOptions(audios, audio, audioCodecs)
		if videoCopy && subAction != session.SubtitleBurn && d.video != nil && has(video, d.video.Codec) {
			for _, a := range opts {
				v := session.Remux
				if a.action == session.ActionConvert {
					v = session.AudioTranscode
				}
				d.add(v, d.copyVideo(), a, subtitle, subAction)
			}
		}
		for _, v := range d.convertOptions() {
			d.add(session.VideoTranscode, v, opts[0], subtitle, subAction)
		}
	}
	// Auto's ladder comes first; copies stay behind it for when the encoder fails.
	rank := func(e *session.PlanEntry) int {
		if s.Network.auto() && e.VideoAction == session.ActionConvert {
			return -1
		}
		return cost[e.Verdict]
	}
	slices.SortStableFunc(d.entries, func(a, b *session.PlanEntry) int { return cmp.Compare(rank(a), rank(b)) })
	return d.entries
}

// VideoTrack is the video track every playback decision and stream is about:
// the first default-flagged one, else the first.
func VideoTrack(m Media) *mediafile.Track {
	var v *mediafile.Track
	for _, t := range m.Tracks {
		if t.Kind == mediafile.Video && (v == nil || t.IsDefault && !v.IsDefault) {
			v = t
		}
	}
	return v
}

// AudioTrack is the audio track a decision picks and its stream plays: the
// one with id, else the first default-flagged one, else the first.
func AudioTrack(m Media, id *int64) *mediafile.Track {
	var audios []*mediafile.Track
	for _, t := range m.Tracks {
		if t.Kind == mediafile.Audio {
			audios = append(audios, t)
		}
	}
	return pickAudio(audios, id)
}

// Song reports a file with audio and no video, which plays as an audio-only
// session.
func Song(m Media) bool {
	audio := false
	for _, t := range m.Tracks {
		if t.Kind == mediafile.Video {
			return false
		}
		audio = audio || t.Kind == mediafile.Audio
	}
	return audio
}

// Auto reports whether entry e plays the Auto ladder for this snapshot.
func Auto(s Snapshot, e *session.PlanEntry) bool {
	return s.Network.auto() && e.VideoAction == session.ActionConvert && !e.SingleRung
}

func bitrateCap(n Network) int64 {
	c := n.MaxBitrate
	for _, class := range []struct {
		on  bool
		cap int64
	}{{n.Metered, meteredCap}, {n.LowData, lowDataCap}} {
		if class.on && (c == 0 || class.cap < c) {
			c = class.cap
		}
	}
	return c
}

func pickAudio(audios []*mediafile.Track, want *int64) *mediafile.Track {
	for _, t := range audios {
		if want != nil && t.ID == *want {
			return t
		}
	}
	for _, t := range audios {
		if t.IsDefault {
			return t
		}
	}
	if len(audios) > 0 {
		return audios[0]
	}
	return nil
}

// pickSubtitle honors the viewer's choice, else the forced track, preferring
// one in the audio's language.
func pickSubtitle(subs []*mediafile.Track, want *int64, audio *mediafile.Track) *mediafile.Track {
	var forced *mediafile.Track
	for _, t := range subs {
		if want != nil && t.ID == *want {
			return t
		}
		if t.IsForced && (forced == nil || audio != nil && t.Language == audio.Language && forced.Language != audio.Language) {
			forced = t
		}
	}
	if want != nil {
		return nil
	}
	return forced
}

func (d *decision) add(verdict string, v videoOption, a audioOption, sub *mediafile.Track, subAction string) {
	e := &session.PlanEntry{Verdict: verdict, VideoAction: v.action, AudioAction: a.action, SubtitleAction: subAction,
		AudioChannels: a.channels, VideoCodec: v.codec, Encoder: v.encoder, ToneMap: v.toneMap,
		TopHeight: v.height, TopBitrate: v.bitrate, SingleRung: v.single, Floor: v.floor, TS: d.ts && verdict != session.DirectPlay}
	if a.track != nil {
		e.AudioTrackID = &a.track.ID
	}
	if sub != nil {
		e.SubtitleTrackID = &sub.ID
	}
	d.entries = append(d.entries, e)
}

type videoOption struct {
	action, codec, encoder string
	toneMap, single, floor bool
	height                 int
	bitrate                int64
}

func (d *decision) copyVideo() videoOption {
	o := videoOption{action: session.ActionCopy, single: true, bitrate: d.m.Bitrate}
	if d.video != nil {
		o.height = deref(d.video.Height, 0)
	}
	return o
}

// hdrCompatible reports whether the client shows this track's HDR as is.
// HDR10+ and Dolby Vision profile 8 carry an HDR10 base layer; profile 7's
// enhancement layer does not survive on an HDR10-only path.
func hdrCompatible(t *mediafile.Track, formats []string) bool {
	switch {
	case t.HDRFormat == "" || has(formats, t.HDRFormat):
		return true
	case t.HDRFormat == "hdr10plus":
		return has(formats, "hdr10")
	case t.HDRFormat == "dolby_vision":
		return deref(t.DVProfile, 8) == 8 && has(formats, "hdr10")
	}
	return false
}

func intersect(a, b []string) []string {
	var out []string
	for _, x := range a {
		if has(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// videoCopyable is the copy rule: a hardware decoder takes this codec,
// profile, level, size, frame rate, depth and HDR, and the bitrate fits.
func (d *decision) videoCopyable() bool {
	t := d.video
	if t == nil {
		return true
	}
	if d.cap > 0 && d.m.Bitrate > d.cap {
		return false
	}
	for _, dec := range d.s.Video {
		if !strings.EqualFold(dec.Codec, t.Codec) || !dec.Hardware ||
			len(dec.Profiles) > 0 && !has(dec.Profiles, t.Profile) ||
			dec.MaxLevel > 0 && deref(t.Level, 0) > dec.MaxLevel ||
			deref(t.Width, 0) > dec.MaxWidth || deref(t.Height, 0) > dec.MaxHeight ||
			dec.MaxFrameRate > 0 && deref(t.FrameRate, 0) > dec.MaxFrameRate ||
			deref(t.BitDepth, 8) > max(dec.MaxBitDepth, 8) {
			continue
		}
		if hdrCompatible(t, intersect(dec.HDR, d.s.Display.HDR)) {
			return true
		}
	}
	return false
}

// convertOptions picks the output codec and top rung, then offers the
// hardware encoder before the software one. HDR10 survives when the client
// decodes 10-bit HEVC with HDR10 on this display; otherwise the output is
// SDR H.264, tone-mapped from an HDR source.
func (d *decision) convertOptions() []videoOption {
	t := d.video
	if t == nil {
		return nil
	}
	// Dolby Vision profile 8.4 carries an HLG base layer.
	pq := (t.HDRFormat == "hdr10" || t.HDRFormat == "hdr10plus" || t.HDRFormat == "dolby_vision") && t.ColorTransfer != "arib-std-b67"
	var out *VideoDecoder
	codec := ""
	for i, dec := range d.s.Video {
		hdr10 := has(dec.HDR, "hdr10") && has(d.s.Display.HDR, "hdr10")
		switch {
		case pq && strings.EqualFold(dec.Codec, "hevc") && dec.MaxBitDepth >= 10 && hdr10:
			out, codec = &d.s.Video[i], "hevc"
		case strings.EqualFold(dec.Codec, "h264") && codec != "hevc" && (out == nil || dec.Hardware && !out.Hardware):
			out, codec = &d.s.Video[i], "h264"
		}
	}
	if out == nil {
		return nil
	}
	top := min(deref(t.Height, out.MaxHeight), out.MaxHeight)
	if sw, sh := deref(t.Width, 0), deref(t.Height, 0); sw > 0 && sh > 0 {
		// Even on both sides, so the rung's rounded width stays within the client's.
		top = min(top, (out.MaxWidth&^1)*sh/sw&^1)
	}
	auto := d.s.Network.auto()
	if auto {
		top = min(top, autoTop)
	}
	bitrate := rungBitrate(top)
	if d.cap > 0 && bitrate > d.cap {
		limit := top
		top, bitrate = min(360, limit), d.cap
		for _, r := range ladder {
			if r.height <= limit && r.bitrate <= d.cap {
				top, bitrate = r.height, r.bitrate
				break
			}
		}
	}
	base := videoOption{action: session.ActionConvert, codec: codec, toneMap: t.HDRFormat != "" && codec != "hevc",
		height: top, bitrate: bitrate, single: d.s.Network.Local || d.s.Network.Auto != nil && d.s.Network.MaxBitrate > 0, floor: auto && d.s.Floor}
	hw, sw := base, base
	hw.encoder, sw.encoder = session.EncoderHardware, session.EncoderSoftware
	return []videoOption{hw, sw}
}

// rungBitrate is the standard bitrate of the smallest rung at least this tall.
func rungBitrate(height int) int64 {
	b := ladder[0].bitrate
	for _, r := range ladder {
		if r.height >= height {
			b = r.bitrate
		}
	}
	return b
}

type audioOption struct {
	action   string
	track    *mediafile.Track
	channels int
}

// audioOptions copies the chosen track when it plays, else a playable track
// in the same language, then falls back to converting the chosen one to AAC
// at no more channels than the route carries. A non-nil segment list limits
// copies to the codecs those segments carry.
func (d *decision) audioOptions(audios []*mediafile.Track, pick *mediafile.Track, segment []string) []audioOption {
	if pick == nil {
		return []audioOption{{action: session.ActionCopy}}
	}
	playable := func(t *mediafile.Track) bool {
		return (has(d.s.Route.Passthrough, t.Codec) || has(d.s.Audio, t.Codec) && deref(t.Channels, 2) <= d.s.Route.Channels) &&
			(segment == nil || has(segment, t.Codec))
	}
	var out []audioOption
	if playable(pick) {
		out = append(out, audioOption{action: session.ActionCopy, track: pick})
	} else {
		for _, t := range audios {
			if t != pick && (t.Language == pick.Language || t.Language == "" || pick.Language == "") && playable(t) {
				out = append(out, audioOption{action: session.ActionCopy, track: t})
				break
			}
		}
	}
	ch := min(deref(pick.Channels, 2), max(d.s.Route.Channels, 1), 8)
	return append(out, audioOption{action: session.ActionConvert, track: pick, channels: ch})
}

// Without drops the entries of plan that are the same as one in bad. If that
// would leave nothing, the full plan is kept: a failure must not make a file
// unplayable.
func Without(plan, bad []*session.PlanEntry) []*session.PlanEntry {
	out := slices.DeleteFunc(slices.Clone(plan), func(e *session.PlanEntry) bool {
		return slices.ContainsFunc(bad, e.Same)
	})
	if len(out) == 0 {
		return plan
	}
	return out
}

// Rung is one quality of a stream.
type Rung struct {
	Width, Height int
	Bitrate       int64
	Verdict       string // set only on Menu rungs
	Blocked       bool   // a Menu rung above the network path's cap
}

func width(src *mediafile.Track, h int) int {
	sw, sh := deref(src.Width, 0), deref(src.Height, 0)
	if sh == 0 {
		return 0
	}
	return 2 * int(math.Round(float64(sw*h)/float64(sh)/2))
}

// Rungs lists an entry's HLS variants, best first: the source alone when
// video copies, else the top rung and the standard rungs below it.
func Rungs(e *session.PlanEntry, src *mediafile.Track) []Rung {
	if src == nil {
		return []Rung{{Bitrate: e.TopBitrate}}
	}
	out := []Rung{{Width: width(src, e.TopHeight), Height: e.TopHeight, Bitrate: e.TopBitrate}}
	if e.VideoAction == session.ActionCopy || e.SingleRung {
		return out
	}
	for _, r := range ladder[2:] {
		if r.height < e.TopHeight {
			out = append(out, Rung{Width: width(src, r.height), Height: r.height, Bitrate: r.bitrate})
		}
	}
	if e.Floor && floor.Height < e.TopHeight {
		out = append(out, Rung{Width: width(src, floor.Height), Height: floor.Height, Bitrate: floor.Bitrate})
	}
	return out
}

// Menu is the quality menu: Original first, at the verdict it would get with
// no cap, then the standard rungs below the source, each a transcode. For a
// v2 client Original ignores the network class too, and every entry above
// that class's cap is blocked; Auto is not an entry.
func Menu(m Media, s Snapshot, sel Selection) []Rung {
	s.Network.MaxBitrate = 0
	var limit int64
	if s.Network.Auto != nil {
		limit = bitrateCap(s.Network)
		s.Network.Auto, s.Network.Metered, s.Network.LowData = new(bool), false, false
	}
	plan := Decide(m, s, sel)
	src := VideoTrack(m)
	if src == nil || len(plan) == 0 {
		return []Rung{}
	}
	orig := Rung{Width: deref(src.Width, 0), Height: deref(src.Height, 0), Bitrate: m.Bitrate, Verdict: plan[0].Verdict}
	if plan[0].VideoAction == session.ActionConvert {
		orig = Rung{Width: width(src, plan[0].TopHeight), Height: plan[0].TopHeight, Bitrate: plan[0].TopBitrate, Verdict: plan[0].Verdict}
	}
	out := []Rung{orig}
	for _, r := range ladder[2:] {
		if r.height < orig.Height {
			out = append(out, Rung{Width: width(src, r.height), Height: r.height, Bitrate: r.bitrate, Verdict: session.VideoTranscode})
		}
	}
	for i := range out {
		out[i].Blocked = limit > 0 && out[i].Bitrate > limit
	}
	return out
}

// Reason says in words why entry e is not direct play, for the server log;
// it is empty for direct play.
func Reason(m Media, s Snapshot, sel Selection, e *session.PlanEntry) string {
	if e.Verdict == session.DirectPlay {
		return ""
	}
	video := VideoTrack(m)
	var audios []*mediafile.Track
	for _, t := range m.Tracks {
		if t.Kind == mediafile.Audio {
			audios = append(audios, t)
		}
	}
	if a := pickAudio(audios, sel.AudioTrackID); video == nil && a != nil && e.AudioAction == session.ActionConvert {
		switch {
		case s.Network.Metered || s.Network.LowData:
			return "a metered or low-data path converts to AAC"
		case !s.Network.Local && mediafile.Lossless(a.Codec):
			return "lossless audio away from home converts to AAC"
		}
	}
	var why []string
	if cap := bitrateCap(s.Network); Auto(s, e) {
		why = append(why, "Auto away from home")
	} else if e.VideoAction == session.ActionConvert && cap > 0 && m.Bitrate > cap {
		why = append(why, fmt.Sprintf("bitrate %d Mb/s exceeds the %d Mb/s cap", m.Bitrate/1e6, cap/1e6))
	} else if e.VideoAction == session.ActionConvert && video != nil {
		why = append(why, fmt.Sprintf("video %s %s %dx%d %d-bit %s is not hardware-decodable here", video.Codec, video.Profile,
			deref(video.Width, 0), deref(video.Height, 0), deref(video.BitDepth, 8), cmp.Or(video.HDRFormat, "SDR")))
	}
	if a := pickAudio(audios, sel.AudioTrackID); e.AudioAction == session.ActionConvert && a != nil {
		why = append(why, fmt.Sprintf("audio %s %dch is not playable on this route", a.Codec, deref(a.Channels, 2)))
	}
	if e.SubtitleAction == session.SubtitleBurn {
		why = append(why, "a bitmap subtitle ("+bitmapSubtitleCodec(m)+") is burned in")
	}
	if len(why) == 0 {
		switch {
		case !has(s.Containers, m.Container):
			why = append(why, "container "+m.Container+" is not supported for direct play")
		case !s.Network.Local:
			why = append(why, "the client is not on the local network")
		default:
			why = append(why, "the client cannot take the file over HTTP as stored")
		}
	}
	return strings.Join(why, "; ")
}

func bitmapSubtitleCodec(m Media) string {
	for _, t := range m.Tracks {
		if t.Kind == mediafile.Subtitle && !deref(t.SubtitleIsText, true) {
			return t.Codec
		}
	}
	return "unknown"
}
