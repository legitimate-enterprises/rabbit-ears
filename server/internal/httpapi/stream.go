package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/thumbs"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// streams serves media bytes outside the spec: the session's stream key in
// the path is the authorization, so players fetch without headers. A URL of
// an entry the session has left answers 404. Under /streams/{key}/{entry}:
// file, master.m3u8, subtitles/{name}, and per rung {rung}/index.m3u8,
// {rung}/init.mp4 and {rung}/{n}.m4s, or {rung}/{n}.ts for a client that
// plays only MPEG-TS; seek previews are previews/{n}.jpg and previews.bif.
type streams struct{ svc *service.Services }

func (h *streams) register(mux *http.ServeMux) {
	p := BasePath + "/streams/{key}/{entry}"
	mux.HandleFunc("GET "+p+"/file", h.with(h.file))
	mux.HandleFunc("GET "+p+"/master.m3u8", h.with(h.master))
	mux.HandleFunc("GET "+p+"/subtitles/{name}", h.with(h.subtitle))
	mux.HandleFunc("GET "+p+"/previews/{name}", h.with(h.preview))
	mux.HandleFunc("GET "+p+"/previews.bif", h.with(h.preview))
	mux.HandleFunc("GET "+p+"/{rung}/{name}", h.with(h.rung))
	// A station's stream: its live playlist, and under each piece of it,
	// a program or an ident, that piece's init segment and segments.
	c := BasePath + "/channels/{key}"
	mux.HandleFunc("GET "+c+"/live.m3u8", h.live)
	mux.HandleFunc("GET "+c+"/{piece}/{name}", h.piece)
}

// live serves a station session's playlist as it is now.
func (h *streams) live(w http.ResponseWriter, r *http.Request) {
	sess, err := h.svc.Sessions.Repo().ByStreamKey(r.PathValue("key"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	list, err := h.svc.Channels.Playlist(sess)
	switch {
	case errors.Is(err, service.ErrNotTuned):
		http.NotFound(w, r)
	case err != nil:
		logstore.Log("playback").Warn(fmt.Sprintf("Session %s: no station playlist", sess.ID), "session_id", sess.ID, "err", err)
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
	default:
		w.Header().Set("Content-Type", playlistType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(list)) //nolint:gosec // a playlist of the server's own making
	}
}

// piece serves one file of a piece the session's playlist lists.
func (h *streams) piece(w http.ResponseWriter, r *http.Request) {
	sess, err := h.svc.Sessions.Repo().ByStreamKey(r.PathValue("key"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	path, err := h.svc.Channels.File(r.Context(), sess, r.PathValue("piece"), name)
	switch {
	case errors.Is(err, service.ErrNotTuned), errors.Is(err, db.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		if r.Context().Err() == nil {
			logstore.Log("playback").Error(fmt.Sprintf("Session %s: station stream failed", sess.ID), "session_id", sess.ID, "err", err)
		}
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	t := "video/iso.segment"
	switch filepath.Ext(name) {
	case ".mp4":
		t = "video/mp4"
	case ".ts":
		t = "video/mp2t"
	}
	w.Header().Set("Content-Type", t)
	http.ServeFile(w, r, path) //nolint:gosec // a path the channel made for a segment its playlist lists
}

type streamReq struct {
	sess  *session.Session
	entry *session.PlanEntry
	file  *mediafile.File
	media playback.Media
}

func (h *streams) with(fn func(http.ResponseWriter, *http.Request, *streamReq)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := h.svc.Sessions.Repo().ByStreamKey(r.PathValue("key"))
		if err != nil || r.PathValue("entry") != strconv.Itoa(sess.CurrentEntry) {
			http.NotFound(w, r)
			return
		}
		plan, err := h.svc.Sessions.Repo().Plan(sess.ID)
		if err != nil || sess.CurrentEntry >= len(plan) {
			http.NotFound(w, r)
			return
		}
		f, m, err := h.svc.Playback.Media(sess.MediaFileID)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		fn(w, r, &streamReq{sess, plan[sess.CurrentEntry], f, m})
	}
}

var fileTypes = map[string]string{"mp4": "video/mp4", "mov": "video/quicktime", "mkv": "video/x-matroska", "webm": "video/webm",
	"ts": "video/mp2t", "avi": "video/x-msvideo", "flac": "audio/flac", "mp3": "audio/mpeg", "ogg": "audio/ogg", "wav": "audio/wav",
	"aiff": "audio/aiff"}

// file is direct play: the file itself with range support.
func (h *streams) file(w http.ResponseWriter, r *http.Request, s *streamReq) {
	if s.entry.Verdict != session.DirectPlay {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(s.file.Path)
	if err != nil {
		http.Error(w, "file unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = f.Close() }()
	if t, ok := fileTypes[s.file.Container]; ok {
		if s.file.Container == "mp4" && playback.Song(s.media) {
			t = "audio/mp4"
		}
		w.Header().Set("Content-Type", t)
	}
	http.ServeContent(w, r, "", s.file.FileModifiedAt, f)
}

const playlistType = "application/vnd.apple.mpegurl"

func (s *streamReq) rungs() []playback.Rung {
	return playback.Rungs(s.entry, playback.VideoTrack(s.media))
}

func (s *streamReq) audio() *mediafile.Track {
	return playback.AudioTrack(s.media, s.entry.AudioTrackID)
}

// codecs is the variant's CODECS attribute, or "" when a copied track's
// codec string is unknown.
func (s *streamReq) codecs() string {
	var out []string
	if v := playback.VideoTrack(s.media); v != nil {
		codec, profile, level := v.Codec, v.Profile, or(v.Level, 0)
		if s.entry.VideoAction == session.ActionConvert {
			codec, profile, level = s.entry.VideoCodec, "High", 40
			if codec == "hevc" {
				profile, level = "Main 10", 150
			}
		}
		c := videoCodecString(codec, profile, level)
		if c == "" {
			return ""
		}
		out = append(out, c)
	}
	if a := s.audio(); a != nil {
		codec := a.Codec
		if s.entry.AudioAction == session.ActionConvert {
			codec = "aac"
		}
		c, ok := map[string]string{"aac": "mp4a.40.2", "ac3": "ac-3", "eac3": "ec-3", "flac": "fLaC", "opus": "Opus", "alac": "alac", "mp3": "mp4a.40.34"}[codec]
		if !ok {
			return ""
		}
		out = append(out, c)
	}
	return strings.Join(out, ",")
}

func videoCodecString(codec, profile string, level int) string {
	switch codec {
	case "h264":
		idc, ok := map[string]int{"Constrained Baseline": 0x4240, "Baseline": 0x4200, "Main": 0x4d00, "High": 0x6400, "High 10": 0x6e00}[profile]
		if !ok || level == 0 {
			return ""
		}
		return fmt.Sprintf("avc1.%04x%02x", idc, level)
	case "hevc":
		p := 1
		if strings.Contains(profile, "10") {
			p = 2
		}
		return fmt.Sprintf("hvc1.%d.4.L%d.B0", p, level)
	}
	return ""
}

func (h *streams) master(w http.ResponseWriter, r *http.Request, s *streamReq) {
	if s.entry.Verdict == session.DirectPlay {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-INDEPENDENT-SEGMENTS\n")
	codecs := s.codecs()
	video := playback.VideoTrack(s.media)
	hdr := s.entry.VideoAction == session.ActionCopy && video != nil && video.HDRFormat != "" ||
		s.entry.VideoCodec == "hevc" && !s.entry.ToneMap
	rangeName := "PQ"
	if s.entry.VideoAction == session.ActionCopy && video != nil && video.ColorTransfer == "arib-std-b67" {
		rangeName = "HLG"
	}
	for i, rg := range s.rungs() {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d", max(rg.Bitrate, 1)+192000)
		if rg.Width > 0 {
			fmt.Fprintf(&b, ",RESOLUTION=%dx%d", rg.Width, rg.Height)
		}
		if codecs != "" {
			fmt.Fprintf(&b, ",CODECS=%q", codecs)
		}
		// AVFoundation refuses the whole playlist when an HDR variant states
		// no frame rate, so the range goes only with one.
		if hdr && video != nil && video.FrameRate != nil {
			fmt.Fprintf(&b, ",FRAME-RATE=%.3f,VIDEO-RANGE=%s", *video.FrameRate, rangeName)
		}
		fmt.Fprintf(&b, "\n%d/index.m3u8\n", i)
	}
	w.Header().Set("Content-Type", playlistType)
	_, _ = w.Write([]byte(b.String()))
}

// rung serves a variant's playlist, init segment and media segments.
func (h *streams) rung(w http.ResponseWriter, r *http.Request, s *streamReq) {
	rungs := s.rungs()
	ri, err := strconv.Atoi(r.PathValue("rung"))
	if s.entry.Verdict == session.DirectPlay || err != nil || ri < 0 || ri >= len(rungs) {
		http.NotFound(w, r)
		return
	}
	dur := or(s.file.DurationMs, 0)
	video := playback.VideoTrack(s.media)
	// Copied video cuts only on source keyframes, so its segments follow them.
	var bounds []transcode.Bound
	if video != nil && s.entry.VideoAction == session.ActionCopy {
		if bounds, err = h.svc.Playback.Engine.Bounds(r.Context(), s.file.Path, dur); err != nil {
			if r.Context().Err() == nil {
				h.svc.Playback.ServerFailed(s.sess, s.sess.CurrentEntry, err)
			}
			http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	durs := transcode.Durations(bounds, dur)
	if video != nil && s.entry.VideoAction == session.ActionConvert {
		durs = transcode.FoldTail(durs)
	}
	segs, at := len(durs), transcode.SegmentAt(bounds, s.sess.PositionMs)
	name := r.PathValue("name")
	if name == "index.m3u8" {
		w.Header().Set("Content-Type", playlistType)
		_, _ = w.Write([]byte(transcode.Playlist(durs, s.entry.TS)))
		return
	}
	ahead, err := h.svc.Server.ReadAhead()
	if err != nil {
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	st := transcode.Stream{SessionID: s.sess.ID, Key: fmt.Sprintf("%d-%d", s.sess.CurrentEntry, ri), Job: transcode.Job{
		Input: s.file.Path, Entry: s.entry, Video: video, Audio: s.audio(),
		Subtitle: trackByID(s.media.Tracks, s.entry.SubtitleTrackID), Rung: rungs[ri], Bounds: bounds}}
	st.Ahead = (ahead + transcode.SegmentSeconds - 1) / transcode.SegmentSeconds
	ext, segType := ".m4s", "video/iso.segment"
	if s.entry.TS {
		ext, segType = ".ts", "video/mp2t"
	}
	var path string
	switch n, isSeg := strings.CutSuffix(name, ext); {
	case name == transcode.InitName && !s.entry.TS:
		path, err = h.svc.Playback.Engine.Init(r.Context(), st, at)
		w.Header().Set("Content-Type", "video/mp4")
	case isSeg:
		i, cerr := strconv.Atoi(n)
		if cerr != nil || i < 0 || i >= segs {
			http.NotFound(w, r)
			return
		}
		path, err = h.svc.Playback.Engine.Segment(r.Context(), st, i)
		w.Header().Set("Content-Type", segType)
	default:
		http.NotFound(w, r)
		return
	}
	// A player that gave up has said nothing about the encoder. A session
	// that lost its slot while paused and finds none free moves down its
	// plan, as one that cannot start does.
	if (errors.Is(err, transcode.ErrEncoder) || errors.Is(err, transcode.ErrBusy)) && r.Context().Err() == nil {
		h.svc.Playback.ServerFailed(s.sess, s.sess.CurrentEntry, err)
	}
	if err != nil {
		http.Error(w, "stream unavailable", http.StatusServiceUnavailable)
		return
	}
	http.ServeFile(w, r, path)
}

// subtitle serves a text track as a WebVTT sidecar.
func (h *streams) subtitle(w http.ResponseWriter, r *http.Request, s *streamReq) {
	id, err := uid.Parse(strings.TrimSuffix(r.PathValue("name"), ".vtt"))
	t := trackByID(s.media.Tracks, &id)
	if err != nil || t == nil || t.Kind != mediafile.Subtitle || t.SubtitleIsText == nil || !*t.SubtitleIsText {
		http.NotFound(w, r)
		return
	}
	path, err := h.svc.Playback.Engine.Sidecar(r.Context(), s.file.ID, s.file.Path, t.StreamIndex, s.media.Tracks)
	if err != nil {
		http.Error(w, "subtitle unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/vtt")
	http.ServeFile(w, r, path)
}

// preview serves one seek preview frame, or with no frame name the BIF of
// them all.
func (h *streams) preview(w http.ResponseWriter, r *http.Request, s *streamReq) {
	p, err := h.svc.Previews.Repo().ByMediaFile(s.file.ID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if name == "" {
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, filepath.Join(p.Dir, thumbs.BIFName))
		return
	}
	n, err := strconv.Atoi(strings.TrimSuffix(name, ".jpg"))
	if err != nil || n < 0 || n >= p.Count || !strings.HasSuffix(name, ".jpg") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeFile(w, r, thumbs.FramePath(p.Dir, n))
}
