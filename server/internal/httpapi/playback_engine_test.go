package httpapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", name))
	if _, err := os.Stat(p); err != nil || testing.Short() {
		t.Skipf("fixture %s missing; run make deps", name)
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	return p
}

// probed adds a G-rated movie whose file is a probed fixture, the way a scan
// stores it, in the first movie library. path overrides where the file is
// said to live.
func (e *env) probed(name, path string) uid.ID {
	e.t.Helper()
	src := fixturePath(e.t, name)
	r, err := probe.Run(context.Background(), src)
	must(e.t, err)
	libs, err := e.svc.Libraries.Repo().List()
	must(e.t, err)
	var lib uid.ID
	for _, l := range libs {
		if l.Kind == library.Movies && lib.IsZero() {
			lib = l.ID
		}
	}
	if lib.IsZero() {
		lib = e.library("movies")
	}
	m := e.movie(lib, name)
	e.rate(m, "G")
	if path == "" {
		path = src
	}
	now := time.Now().UTC()
	f := &mediafile.File{ItemID: &m, Path: path, SizeBytes: 1, FileModifiedAt: now, Container: r.Container, DurationMs: r.DurationMs, Bitrate: r.Bitrate, ProbedAt: &now}
	must(e.t, e.svc.MediaFiles.Repo().CreateFile(f))
	must(e.t, e.svc.MediaFiles.Repo().ReplaceTracks(f.ID, r.Tracks))
	return m
}

// sdrWeb is a local browser with hardware H.264 and HEVC, on an SDR display.
func sdrWeb() map[string]any {
	s := snapshot()
	s["decoder"] = map[string]any{
		"protocols":  []string{"http", "hls"},
		"containers": []string{"mp4"},
		"videoCodecs": []map[string]any{
			{"codec": "h264", "maxLevel": 52, "hardware": true, "maxWidth": 3840, "maxHeight": 2160, "maxFrameRate": 60, "maxBitDepth": 8},
			{"codec": "hevc", "hardware": true, "maxWidth": 3840, "maxHeight": 2160, "maxFrameRate": 60, "maxBitDepth": 10, "hdrFormats": []string{"hdr10"}},
		},
		"audioCodecs":     []string{"aac"},
		"subtitleFormats": []string{"webvtt"},
	}
	return s
}

func (e *env) trackID(item uid.ID, codec string) uid.ID {
	e.t.Helper()
	files, err := e.svc.MediaFiles.Repo().FilesForItem(item)
	must(e.t, err)
	ts, err := e.svc.MediaFiles.Repo().Tracks(files[0].ID, "")
	must(e.t, err)
	for _, tr := range ts {
		if tr.Codec == codec {
			return tr.ID
		}
	}
	e.t.Fatalf("no %s track", codec)
	return uid.ID{}
}

func (e *env) get(path string) (*http.Response, []byte) {
	e.t.Helper()
	res, err := http.Get(strings.TrimSuffix(e.url, httpapi.BasePath) + path)
	must(e.t, err)
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

func (e *env) start(item uid.ID, snap map[string]any) api.PlaybackStream {
	e.t.Helper()
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": item, "snapshot": snap}, &st)
	return st
}

func TestDirectPlayServesRanges(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	st := e.start(m, sdrWeb())
	if st.Entry.Verdict != api.DirectPlay || st.StreamKind != api.PlaybackStreamStreamKindFile || st.Negotiated.Width != 640 || st.Rungs[0].Verdict != api.DirectPlay {
		t.Fatalf("%+v", st)
	}
	req, _ := http.NewRequest("GET", strings.TrimSuffix(e.url, httpapi.BasePath)+st.StreamUrl, nil)
	req.Header.Set("Range", "bytes=0-99")
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 206 || len(b) != 100 || res.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("%d %d %q", res.StatusCode, len(b), res.Header.Get("Content-Type"))
	}
	if res, _ := e.get(strings.Replace(st.StreamUrl, "/streams/", "/streams/x", 1)); res.StatusCode != 404 {
		t.Fatal("a wrong stream key must not serve the file")
	}
}

// A converted stream tells the player how far ahead to buffer, the server's
// read-ahead; a file stream leaves the player its own buffer.
func TestStreamCarriesTheBufferGoal(t *testing.T) {
	e := newEnv(t)
	must(t, e.svc.Server.SetReadAhead(120))
	if st := e.start(e.probed("hdr10-main10.mkv", ""), sdrWeb()); st.StreamKind != api.PlaybackStreamStreamKindHls || st.BufferGoalSeconds == nil || *st.BufferGoalSeconds != 120 {
		t.Fatalf("%+v", st)
	}
	if st := e.start(e.probed("h264-high41.mp4", ""), sdrWeb()); st.StreamKind != api.PlaybackStreamStreamKindFile || st.BufferGoalSeconds != nil {
		t.Fatalf("%+v", st)
	}
}

func TestHLSTranscodePlays(t *testing.T) {
	e := newEnv(t)
	m := e.probed("hdr10-main10.mkv", "")
	st := e.start(m, sdrWeb())
	negotiated, encoder := api.NegotiatedEncoderSoftware, api.SessionEncoderSoftware
	if transcode.Hardware() {
		negotiated, encoder = api.NegotiatedEncoderHardware, api.SessionEncoderVideotoolbox
	}
	if st.Entry.Verdict != api.VideoTranscode || st.StreamKind != api.PlaybackStreamStreamKindHls || st.Entry.SubtitleAction != api.PlanEntrySubtitleActionBurn ||
		st.Negotiated.HdrFormat != nil || st.Negotiated.Encoder == nil || *st.Negotiated.Encoder != negotiated {
		t.Fatalf("HDR10 with forced PGS on an SDR browser burns in and tone-maps on the host's encoder: %+v", st)
	}
	var sess api.Session
	e.expect(200, "GET", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, &sess)
	if sess.Encoder != encoder || sess.TranscodeReason == nil || !strings.Contains(*sess.TranscodeReason, "hevc") {
		t.Fatalf("now playing names the encoder and why: %+v", sess)
	}
	res, master := e.get(st.StreamUrl)
	if res.StatusCode != 200 || !strings.Contains(string(master), "#EXT-X-STREAM-INF") || res.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("%d %s", res.StatusCode, master)
	}
	base := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1]
	variant := strings.TrimSpace(strings.Split(strings.TrimSpace(string(master)), "\n")[len(strings.Split(strings.TrimSpace(string(master)), "\n"))-1])
	res, media := e.get(base + variant)
	if res.StatusCode != 200 || strings.Count(string(media), "#EXTINF") != 2 || !strings.Contains(string(media), "#EXT-X-MAP") || !strings.Contains(string(media), "#EXT-X-ENDLIST") {
		t.Fatalf("%d %s", res.StatusCode, media)
	}
	rungBase := base + variant[:strings.LastIndex(variant, "/")+1]
	_, init := e.get(rungBase + "init.mp4")
	res, seg := e.get(rungBase + "1.m4s")
	if res.StatusCode != 200 || len(seg) == 0 {
		t.Fatalf("segment: %d", res.StatusCode)
	}
	p := filepath.Join(t.TempDir(), "s.mp4")
	must(t, os.WriteFile(p, append(init, seg...), 0o600))
	r, err := probe.Run(context.Background(), p)
	must(t, err)
	if r.Tracks[0].Codec != "h264" || r.Tracks[0].ColorTransfer != "bt709" {
		t.Fatalf("%+v", r.Tracks[0])
	}
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(st.SessionId), e.plain.token, nil, nil)
	if res, _ := e.get(rungBase + "1.m4s"); res.StatusCode != 404 {
		t.Fatal("an ended session's stream stops")
	}
}

func TestSidecarSubtitle(t *testing.T) {
	e := newEnv(t)
	m := e.probed("hdr10-main10.mkv", "")
	srt := e.trackID(m, "subrip")
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": sdrWeb(), "subtitleTrackId": srt}, &st)
	if st.Entry.SubtitleAction != api.PlanEntrySubtitleActionSidecar || len(st.Sidecars) != 1 || st.Sidecars[0].TrackId != srt {
		t.Fatalf("%+v", st)
	}
	res, b := e.get(st.Sidecars[0].Url)
	if res.StatusCode != 200 || !strings.HasPrefix(string(b), "WEBVTT") || res.Header.Get("Content-Type") != "text/vtt" {
		t.Fatalf("%d %q", res.StatusCode, b)
	}
}

func TestClientFailureWalksThePlan(t *testing.T) {
	e := newEnv(t)
	e.svc.Playback.Hardware = true // for a hardware and a software video transcode
	m := e.probed("h264-high41.mp4", "")
	first := e.start(m, sdrWeb())
	var up api.PlaybackUpdate
	e.expect(200, "POST", "/playback/sessions/"+itoa(first.SessionId)+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 0}, &up)
	if up.Action != api.Restart || up.Stream.Entry.Verdict != api.Remux {
		t.Fatalf("%+v", up)
	}
	e.expect(200, "PUT", "/playback/sessions/"+itoa(first.SessionId)+"/audio-route", e.plain.token, map[string]any{"channels": 6, "passthroughCodecs": []string{}}, &up)
	if up.Action != api.Continue {
		t.Fatalf("a change mid-session does not return to the entry the client refused: %+v", up)
	}
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(first.SessionId), e.plain.token, nil, nil)

	st := e.start(m, sdrWeb())
	if st.Entry.Verdict != api.DirectPlay {
		t.Fatalf("a refusal stays with its session; the next one starts from the full plan: %+v", st.Entry)
	}
	sid := "/playback/sessions/" + itoa(st.SessionId)
	want := []api.Verdict{api.Remux, api.AudioTranscode, api.VideoTranscode, api.VideoTranscode}
	for i, v := range want {
		e.expect(200, "POST", sid+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 4000, "reason": "decode"}, &up)
		if up.Action != api.Restart || up.Stream.Entry.Verdict != v || up.Stream.PositionMs != 4000 || up.Stream.StreamKind != api.PlaybackStreamStreamKindHls {
			t.Fatalf("failure %d: %+v", i, up)
		}
	}
	e.expect(409, "POST", sid+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 4000}, nil)
}

func TestServerFailureMovesOnce(t *testing.T) {
	e := newEnv(t)
	e.svc.Playback.Hardware = true                                 // for a second entry to move to
	m := e.probed("hdr10-main10.mkv", e.onDisk("/films/film.mkv")) // an empty file, which ffmpeg cannot read
	st := e.start(m, sdrWeb())
	res, master := e.get(st.StreamUrl)
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	lines := strings.Split(strings.TrimSpace(string(master)), "\n")
	base := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1]
	rung := base + lines[len(lines)-1][:strings.LastIndex(lines[len(lines)-1], "/")+1]
	if res, _ := e.get(rung + "0.m4s"); res.StatusCode != 503 {
		t.Fatalf("ffmpeg failure: %d", res.StatusCode)
	}
	var sess api.Session
	e.expect(200, "GET", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, &sess)
	if !sess.Plan[0].Bad || sess.Plan[1].Bad || !strings.HasPrefix(*sess.Plan[0].BadReason, "server") {
		t.Fatalf("%+v", sess.Plan)
	}
	var up api.PlaybackUpdate
	e.expect(200, "POST", "/playback/sessions/"+itoa(st.SessionId)+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 0}, &up)
	e.expect(200, "GET", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, &sess)
	if up.Action != api.Restart || up.Stream.Entry.Position != 1 || sess.Plan[1].Bad {
		t.Fatalf("the client's report of the server's failure moves no further: %+v %+v", up.Stream.Entry, sess.Plan)
	}
}

func TestRouteChangeResumesAtSamePosition(t *testing.T) {
	e := newEnv(t)
	m := e.probed("hdr10-main10.mkv", "")
	hdr := sdrWeb()
	hdr["display"] = map[string]any{"width": 3840, "height": 2160, "refreshRate": 60, "hdrFormats": []string{"hdr10"}}
	// Without a forced bitmap subtitle the HDR display can take the HEVC as is.
	srt := e.trackID(m, "subrip")
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": hdr, "subtitleTrackId": srt}, &st)
	if st.Entry.Verdict != api.Remux || st.Negotiated.HdrFormat == nil || *st.Negotiated.HdrFormat != api.Hdr10 {
		t.Fatalf("%+v", st)
	}
	sid := "/playback/sessions/" + itoa(st.SessionId)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 5000}, nil)
	var up api.PlaybackUpdate
	e.expect(200, "PUT", sid+"/display", e.plain.token, map[string]any{"width": 3840, "height": 2160, "refreshRate": 60, "hdrFormats": []string{"hdr10"}}, &up)
	if up.Action != api.Continue {
		t.Fatalf("same display continues: %+v", up)
	}
	e.expect(200, "PUT", sid+"/display", e.plain.token, map[string]any{"width": 1920, "height": 1080, "refreshRate": 60, "hdrFormats": []string{}}, &up)
	if up.Action != api.Restart || up.Stream.Entry.Verdict != api.VideoTranscode || up.Stream.PositionMs != 5000 || up.Stream.StreamUrl == st.StreamUrl {
		t.Fatalf("an SDR display restarts tone-mapped at the same position: %+v", up)
	}
	e.expect(200, "PUT", sid+"/network", e.plain.token, map[string]any{"discoveredLocally": true, "metered": false, "lowData": false, "vpn": false, "maxBitrate": 500000}, &up)
	if up.Action != api.Restart || up.Stream.Negotiated.Bitrate > 500000 {
		t.Fatalf("a lower quality cap restarts lower: %+v", up.Stream.Negotiated)
	}
	e.expect(200, "PUT", sid+"/throughput", e.plain.token, map[string]any{"measuredBitrate": 9000000, "bufferMs": 20000, "stallCount": 0, "currentRung": 0}, &up)
	if up.Action != api.Continue {
		t.Fatal("telemetry never restarts in v1")
	}
	if samples, _ := e.svc.Sessions.Repo().Samples(st.SessionId, 100); len(samples) != 1 {
		t.Fatal("throughput is stored")
	}
}

// hdrCopy starts the HDR10 fixture's item m on an HDR display, which takes
// its HEVC as is.
func (e *env) hdrCopy(m uid.ID) api.PlaybackStream {
	e.t.Helper()
	hdr := sdrWeb()
	hdr["display"] = map[string]any{"width": 3840, "height": 2160, "refreshRate": 60, "hdrFormats": []string{"hdr10"}}
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": hdr, "subtitleTrackId": e.trackID(m, "subrip")}, &st)
	if st.Entry.VideoAction != api.PlanEntryVideoActionCopy || st.Negotiated.HdrFormat == nil {
		e.t.Fatalf("%+v", st)
	}
	return st
}

// AVFoundation reads a master playlist whose HDR variant states no frame
// rate as no playlist at all and fails it as an unsupported URL.
func TestHDRVariantStatesItsFrameRate(t *testing.T) {
	e := newEnv(t)
	st := e.hdrCopy(e.probed("hdr10-main10.mkv", ""))
	if _, master := e.get(st.StreamUrl); !strings.Contains(string(master), ",FRAME-RATE=24.000,VIDEO-RANGE=PQ\n") {
		t.Fatalf("%s", master)
	}
}

// Copied HLG video is announced as HLG: PQ names another transfer.
func TestHLGVariantStatesHLG(t *testing.T) {
	e := newEnv(t)
	m := e.probed("hdr10-main10.mkv", "")
	files, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	ts, err := e.svc.MediaFiles.Repo().Tracks(files[0].ID, "")
	must(t, err)
	for _, tr := range ts {
		if tr.Kind == mediafile.Video {
			tr.HDRFormat, tr.ColorTransfer = "hlg", "arib-std-b67"
		}
	}
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(files[0].ID, ts))
	hlg := sdrWeb()
	hlg["decoder"].(map[string]any)["videoCodecs"].([]map[string]any)[1]["hdrFormats"] = []string{"hdr10", "hlg"}
	hlg["display"] = map[string]any{"width": 3840, "height": 2160, "refreshRate": 60, "hdrFormats": []string{"hdr10", "hlg"}}
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": hlg, "subtitleTrackId": e.trackID(m, "subrip")}, &st)
	if st.Entry.VideoAction != api.PlanEntryVideoActionCopy || st.StreamKind != api.PlaybackStreamStreamKindHls {
		t.Fatalf("%+v", st)
	}
	if _, master := e.get(st.StreamUrl); !strings.Contains(string(master), ",FRAME-RATE=24.000,VIDEO-RANGE=HLG\n") {
		t.Fatalf("%s", master)
	}
}

// A file of unknown length streams only as itself, so its quality menu
// offers no smaller size to convert to.
func TestMenuOfAFileOfUnknownLengthIsOriginalAlone(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	fs, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	fs[0].DurationMs = nil
	must(t, e.svc.MediaFiles.Repo().UpdateFile(fs[0]))
	ts, err := e.svc.MediaFiles.Repo().Tracks(fs[0].ID, "")
	must(t, err)
	for _, tr := range ts {
		if tr.Kind == mediafile.Video {
			w, h := 1920, 1080
			tr.Width, tr.Height = &w, &h
		}
	}
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(fs[0].ID, ts))
	if st := e.start(m, sdrWeb()); st.Entry.Verdict != api.DirectPlay || len(st.Rungs) != 1 {
		t.Fatalf("%+v %+v", st.Entry, st.Rungs)
	}
}

// Matroska from a web download can keep its HEVC parameter sets only in the
// stream. The init segment must carry them all the same: AVFoundation
// decodes nothing from an hvc1 track without.
func TestCopiedHEVCInitCarriesInBandParameterSets(t *testing.T) {
	e := newEnv(t)
	b, err := os.ReadFile(fixturePath(t, "hdr10-main10.mkv"))
	must(t, err)
	// CodecPrivate, after its two-byte size, is an HEVC decoder configuration
	// record, whose byte 22 counts the parameter-set arrays.
	i := bytes.Index(b, []byte{0x63, 0xA2}) + 4
	if i < 4 || b[i] != 1 || b[i+22] == 0 {
		t.Fatal("the fixture's CodecPrivate is not where this test patches it")
	}
	b[i+22] = 0
	p := filepath.Join(t.TempDir(), "in-band.mkv")
	must(t, os.WriteFile(p, b, 0o600)) //nolint:gosec // test inputs are fixtures and temp paths
	st := e.hdrCopy(e.probed("hdr10-main10.mkv", p))
	rung := strings.TrimSuffix(st.StreamUrl, "master.m3u8") + "0/"
	_, init := e.get(rung + "init.mp4")
	_, seg := e.get(rung + "0.m4s")
	out := filepath.Join(t.TempDir(), "s.mp4")
	must(t, os.WriteFile(out, append(init, seg...), 0o600))
	if r, err := probe.Run(context.Background(), out); err != nil || r.Tracks[0].Profile != "Main 10" {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestTranscodeLimit(t *testing.T) {
	e := newEnv(t)
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: t.TempDir(), MaxVideo: 1})
	m := e.probed("hdr10-main10.mkv", "")
	first := e.start(m, sdrWeb())
	other := map[string]any{"profileId": e.admin.account.ID, "itemId": m, "snapshot": sdrWeb()}
	raw := e.expect(409, "POST", "/playback/sessions", e.admin.token, other, nil)
	if !strings.Contains(string(raw), "transcode") {
		t.Fatalf("%s", raw)
	}
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(first.SessionId), e.plain.token, nil, nil)
	var st api.PlaybackStream
	if e.expect(201, "POST", "/playback/sessions", e.admin.token, other, &st); st.Entry.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", st.Entry)
	}
	// The device holding the slot starting again hands it to its new session.
	held := st.SessionId
	if e.expect(201, "POST", "/playback/sessions", e.admin.token, other, &st); st.Entry.Verdict != api.VideoTranscode || st.SessionId == held {
		t.Fatalf("%+v", st)
	}
}

// A viewer paused long enough gives its slot to another; resuming with no
// slot free moves it to its next plan entry rather than ending playback.
// Until then its encoder runs the server's read-ahead past what it fetched,
// whatever the engine's own distance.
func TestIdleTranscodeYieldsItsSlot(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: root, MaxVideo: 1, Ahead: 1, Idle: 300 * time.Millisecond})
	must(t, e.svc.Server.SetReadAhead(60))
	src := fixturePath(t, "h264-high41.mp4")
	looped := filepath.Join(t.TempDir(), "looped.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-stream_loop", "17", "-i", src, "-c", "copy", looped).CombinedOutput(); err != nil { //nolint:gosec // test inputs are fixtures and temp paths
		t.Fatalf("%v %s", err, out)
	}
	m := e.probed("h264-high41.mp4", looped)
	files, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	r, err := probe.Run(context.Background(), looped)
	must(t, err)
	files[0].DurationMs = r.DurationMs
	must(t, e.svc.MediaFiles.Repo().UpdateFile(files[0]))
	st := e.start(m, awayWeb(true, 0))
	if st.Entry.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", st.Entry)
	}
	_, master := e.get(st.StreamUrl)
	lines := strings.Split(strings.TrimSpace(string(master)), "\n")
	rung := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1] + lines[len(lines)-1][:strings.LastIndex(lines[len(lines)-1], "/")+1]
	if res, _ := e.get(rung + "0.m4s"); res.StatusCode != 200 {
		t.Fatalf("segment: %d", res.StatusCode)
	}
	// Reaching the read-ahead takes as long as the host's encoder does; the
	// slot is due within Idle of the pause.
	for end := time.Now().Add(60 * time.Second); e.svc.Playback.Engine.Busy(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the encoder never paused")
		}
	}
	for end := time.Now().Add(5 * time.Second); len(e.svc.Playback.Engine.Transcoding()) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the paused viewer keeps its slot")
		}
	}
	// Segments 0 to 11 take the encoder 60 seconds past the request for 0.
	if segs, _ := filepath.Glob(filepath.Join(root, itoa(st.SessionId), "*", "*.m4s")); len(segs) < 12 {
		t.Fatalf("the encoder paused %d segments in, short of the read-ahead", len(segs))
	}
	other := map[string]any{"profileId": e.admin.account.ID, "itemId": m, "snapshot": awayWeb(true, 0)}
	var second api.PlaybackStream
	if e.expect(201, "POST", "/playback/sessions", e.admin.token, other, &second); second.Entry.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", second.Entry)
	}
	if res, _ := e.get(rung + "29.m4s"); res.StatusCode != 503 {
		t.Fatalf("resuming with no slot free: %d", res.StatusCode)
	}
	var up api.PlaybackUpdate
	e.expect(200, "POST", "/playback/sessions/"+itoa(st.SessionId)+"/events", e.plain.token, map[string]any{"kind": "client_failure", "positionMs": 0}, &up)
	if up.Action != api.Restart || up.Stream.Entry.Verdict != api.Remux {
		t.Fatalf("the paused viewer moves to the remux: %+v", up)
	}
}

// A position report reaches the encoder: it runs the read-ahead past the
// reported playhead, not only past the one segment the player fetched.
func TestPositionReportPacesTheEncoder(t *testing.T) {
	e := newEnv(t)
	root := t.TempDir()
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: root, Ahead: 1})
	must(t, e.svc.Server.SetReadAhead(60))
	looped := filepath.Join(t.TempDir(), "looped.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-stream_loop", "23", "-i", fixturePath(t, "h264-high41.mp4"), "-c", "copy", looped).CombinedOutput(); err != nil { //nolint:gosec // test inputs are fixtures and temp paths
		t.Fatalf("%v %s", err, out)
	}
	m := e.probed("h264-high41.mp4", looped)
	files, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	r, err := probe.Run(context.Background(), looped)
	must(t, err)
	files[0].DurationMs = r.DurationMs
	must(t, e.svc.MediaFiles.Repo().UpdateFile(files[0]))
	st := e.start(m, awayWeb(true, 0))
	if st.Entry.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", st.Entry)
	}
	_, master := e.get(st.StreamUrl)
	lines := strings.Split(strings.TrimSpace(string(master)), "\n")
	rung := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1] + lines[len(lines)-1][:strings.LastIndex(lines[len(lines)-1], "/")+1]
	if res, _ := e.get(rung + "0.m4s"); res.StatusCode != 200 {
		t.Fatalf("segment: %d", res.StatusCode)
	}
	e.expect(204, "PUT", "/playback/sessions/"+itoa(st.SessionId)+"/position", e.plain.token, map[string]any{"positionMs": 120000}, nil)
	// The playhead at segment 20 takes the encoder to 30; from the request
	// for 0 alone it stops at 10.
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		segs, _ := filepath.Glob(filepath.Join(root, itoa(st.SessionId), "*", "*.m4s"))
		if len(segs) >= 25 {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the encoder stopped at %d segments, short of the reported playhead's read-ahead", len(segs))
		}
	}
}

// A file re-probed mid-playback loses the track the plan named; the stream
// then plays the track a decision would, the default, not the first.
func TestStreamPlaysTheDefaultAudioTrackAfterAReprobe(t *testing.T) {
	e := newEnv(t)
	src := fixturePath(t, "h264-high41.mp4")
	two := filepath.Join(t.TempDir(), "two.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-i", src, "-map", "0:v", "-map", "0:a", "-map", "0:a", "-c:v", "copy", //nolint:gosec // test inputs are fixtures and temp paths
		"-c:a:0", "ac3", "-c:a:1", "aac", "-disposition:a:0", "0", "-disposition:a:1", "default", two).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r, err := probe.Run(context.Background(), two)
	must(t, err)
	m := e.probed("h264-high41.mp4", two)
	files, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(files[0].ID, r.Tracks))
	st := e.start(m, awayWeb(false, 0))
	e.probed("h264-high41.mp4", "") // so the re-probe's track ids are new
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(files[0].ID, r.Tracks))
	if _, master := e.get(st.StreamUrl); !strings.Contains(string(master), "mp4a.40.2") || strings.Contains(string(master), "ac-3") {
		t.Fatalf("%s", master)
	}
}

// Playability answers what starting playback would, without a session or a
// transcode slot.
func TestPlayabilityMatchesPlay(t *testing.T) {
	for _, c := range []struct {
		fixture string
		snap    map[string]any
		want    api.Verdict
	}{
		{"h264-high41.mp4", sdrWeb(), api.DirectPlay},
		{"hdr10-main10.mkv", sdrWeb(), api.VideoTranscode},
		{"hdr10-main10.mkv", snapshot(), api.VideoTranscode},
	} {
		e := newEnv(t)
		e.svc.Playback.Engine = transcode.New(transcode.Config{Root: t.TempDir(), MaxVideo: 1})
		m := e.probed(c.fixture, "")
		var got api.Playability
		path := "/items/" + itoa(m) + "/playability"
		e.expect(200, "POST", path, e.plain.token, c.snap, &got)
		e.expect(200, "POST", path, e.plain.token, c.snap, &got)
		if got.Verdict != c.want || got.Negotiated.Verdict != c.want {
			t.Fatalf("%s: %+v", c.fixture, got)
		}
		st := e.start(m, c.snap)
		if st.Entry.Verdict != got.Verdict || !reflect.DeepEqual(st.Negotiated, got.Negotiated) {
			t.Fatalf("play %+v %+v, playability %+v", st.Entry, st.Negotiated, got)
		}
		e.expect(204, "DELETE", "/playback/sessions/"+itoa(st.SessionId), e.plain.token, nil, nil)
	}
}

// A player abandoning a request, as players do on a seek or a rung switch,
// is not a server failure: the entry stays good and the next request plays.
func TestAbandonedRequestFailsNoEntry(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	real, err := exec.LookPath("ffprobe")
	if err != nil || testing.Short() {
		t.Skip("ffprobe not installed")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nsleep 0.5\nexec '" + real + "' \"$@\"\n"
	must(t, os.WriteFile(filepath.Join(dir, "ffprobe"), []byte(script), 0o700)) //nolint:gosec // a test script
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	st := e.start(m, roku())
	if st.Entry.Verdict != api.Remux {
		t.Fatalf("%+v", st.Entry)
	}
	index := strings.TrimSuffix(e.url, httpapi.BasePath) + st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1] + "0/index.m3u8"
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", index, nil)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
		t.Fatal("the request should have been abandoned")
	}
	time.Sleep(50 * time.Millisecond)
	var sess api.Session
	e.expect(200, "GET", "/sessions/"+itoa(st.SessionId), e.admin.token, nil, &sess)
	if sess.Plan[0].Bad || sess.CurrentEntry != 0 {
		t.Fatalf("%+v", sess.Plan[0])
	}
	if res, b := e.get(strings.TrimPrefix(index, strings.TrimSuffix(e.url, httpapi.BasePath))); res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
}

// A device signed out or revoked loses its streams at once, not when its
// session goes stale.
func TestRevokedDeviceStreamsStop(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	st := e.start(m, sdrWeb())
	if res, _ := e.get(st.StreamUrl); res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	e.expect(204, "POST", "/auth/sign-out", e.plain.token, nil, nil)
	if res, _ := e.get(st.StreamUrl); res.StatusCode != 404 {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
	if g, _ := e.svc.Sessions.Repo().Get(st.SessionId); g.EndedAt == nil {
		t.Fatal("the session is still open")
	}

	var as api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.admin.token, map[string]any{"profileId": e.admin.account.ID, "itemId": m, "snapshot": sdrWeb()}, &as)
	other := e.adult("other@example.test", true)
	e.expect(200, "PUT", "/devices/"+itoa(e.admin.deviceID), other.token, map[string]any{"name": "Laptop", "revoked": true}, nil)
	if res, _ := e.get(as.StreamUrl); res.StatusCode != 404 {
		t.Fatalf("revoked: %d", res.StatusCode)
	}
}

// A device locked at Who's watching loses its streams, which their key alone
// would keep serving; another device of the household plays on.
func TestLockedDeviceStreamsStop(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	st := e.start(m, sdrWeb())
	tablet, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "Tablet", "web")
	must(t, err)
	var other api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", tablet, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": sdrWeb()}, &other)

	e.expect(204, "POST", "/me/lock", e.plain.token, nil, nil)
	if res, _ := e.get(st.StreamUrl); res.StatusCode != 404 {
		t.Fatalf("locked: %d", res.StatusCode)
	}
	if g, _ := e.svc.Sessions.Repo().Get(st.SessionId); g.EndedAt == nil {
		t.Fatal("the session is still open")
	}
	if res, _ := e.get(other.StreamUrl); res.StatusCode != 200 {
		t.Fatalf("another device: %d", res.StatusCode)
	}
}

// firstSegment fetches segment 0 of the stream's last listed variant.
func (e *env) firstSegment(st api.PlaybackStream) []byte {
	e.t.Helper()
	_, master := e.get(st.StreamUrl)
	lines := strings.Split(strings.TrimSpace(string(master)), "\n")
	base := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1]
	res, seg := e.get(base + lines[len(lines)-1][:strings.LastIndex(lines[len(lines)-1], "/")+1] + "0.m4s")
	if res.StatusCode != 200 {
		e.t.Fatalf("segment: %d", res.StatusCode)
	}
	return seg
}

// A session removed with its device leaves no id for the next session to
// take; that session plays its own file, not the removed one's segments.
func TestReusedSessionIDServesItsOwnFile(t *testing.T) {
	e := newEnv(t)
	first := e.start(e.probed("hdr10-main10.mkv", ""), sdrWeb())
	gone := e.firstSegment(first)
	must(t, e.svc.Devices.Repo().Delete(e.plain.deviceID))
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.admin.token, map[string]any{"profileId": e.admin.account.ID, "itemId": e.probed("chapters.mkv", ""), "snapshot": sdrWeb()}, &st)
	if st.SessionId == first.SessionId {
		t.Fatalf("the id is reused: %s", st.SessionId)
	}
	if bytes.Equal(e.firstSegment(st), gone) {
		t.Fatal("the new session is served the removed session's segment")
	}
}

// A converted stream lists only segments its encoder writes: a file whose
// length passes a boundary by less than its last frame's span ends with the
// segment before, its sliver of a tail folded in.
func TestConvertedStreamListsOnlyWhatItsEncoderWrites(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	src := filepath.Join(t.TempDir(), "sliver.mp4")
	//nolint:gosec // a temp path
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=320x180:r=24000/1001", "-frames:v", "144", "-c:v", "mpeg4",
		src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	e := newEnv(t)
	r, err := probe.Run(context.Background(), src)
	must(t, err)
	if r.DurationMs == nil || *r.DurationMs <= 6000 || *r.DurationMs > 6050 {
		t.Fatalf("the clip runs %v ms", r.DurationMs)
	}
	m := e.movie(e.library("movies"), "Sliver")
	e.rate(m, "G")
	now := time.Now().UTC()
	f := &mediafile.File{ItemID: &m, Path: src, SizeBytes: 1, FileModifiedAt: now, Container: r.Container, DurationMs: r.DurationMs, Bitrate: r.Bitrate, ProbedAt: &now}
	must(t, e.svc.MediaFiles.Repo().CreateFile(f))
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(f.ID, r.Tracks))
	st := e.start(m, sdrWeb())
	if st.Entry.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", st.Entry)
	}
	base := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1] + "0/"
	res, media := e.get(base + "index.m3u8")
	if res.StatusCode != 200 || strings.Count(string(media), "#EXTINF") != 1 || !strings.Contains(string(media), "#EXTINF:6.0") {
		t.Fatalf("%d %s", res.StatusCode, media)
	}
	if res, seg := e.get(base + "0.m4s"); res.StatusCode != 200 || !bytes.Contains(seg, []byte("mdat")) {
		t.Fatalf("the last listed segment: %d, %d bytes", res.StatusCode, len(seg))
	}
	if res, _ := e.get(base + "1.m4s"); res.StatusCode != 404 {
		t.Fatalf("a segment past the list: %d", res.StatusCode)
	}
}
