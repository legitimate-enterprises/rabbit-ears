package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func downloadsOf(profile uid.ID) string { return "/profiles/" + itoa(profile) + "/downloads" }

func (e *env) fetch(url, token string, header map[string]string) (*http.Response, []byte) {
	e.t.Helper()
	req, err := http.NewRequest("GET", strings.TrimSuffix(e.url, httpapi.BasePath)+url, nil)
	must(e.t, err)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res, b
}

// work runs the conversion worker, over a cache of the test's own unless
// one is set, until the test ends.
func (e *env) work() {
	e.t.Helper()
	if e.svc.Downloads.Dir == "" {
		e.svc.Downloads.Dir = e.t.TempDir()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.svc.Downloads.Run(ctx) }()
	e.t.Cleanup(func() { cancel(); <-done })
}

func (e *env) ready(profile, id uid.ID, token string) api.Download {
	e.t.Helper()
	var d api.Download
	for end := time.Now().Add(90 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		e.expect(200, "GET", downloadsOf(profile)+"/"+itoa(id), token, nil, &d)
		if d.Status == api.DownloadStatusReady || d.Status == api.DownloadStatusFailed {
			break
		}
	}
	if d.Status != api.DownloadStatusReady || d.Url == nil || d.Estimated || d.Progress != 1 {
		e.t.Fatalf("not ready: %+v %s", d, deref(d.Error))
	}
	return d
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

type probed struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Height    int    `json:"height"`
		Channels  int    `json:"channels"`
	} `json:"streams"`
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
	} `json:"format"`
}

// ffprobe reads what a fetched file holds.
func ffprobe(t *testing.T, b []byte) probed {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fetched")
	must(t, os.WriteFile(p, b, 0o600))
	//nolint:gosec // a temp path
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name,height,channels:format=format_name,duration",
		"-of", "json", p).Output()
	must(t, err)
	var v probed
	must(t, json.Unmarshal(out, &v))
	return v
}

// probedSong adds a song whose file is a probed fixture.
func (e *env) probedSong(name string) uid.ID {
	e.t.Helper()
	src := fixturePath(e.t, name)
	r, err := probe.Run(context.Background(), src)
	must(e.t, err)
	song := e.song(e.library("music"), name)
	now := time.Now().UTC()
	f := &mediafile.File{ItemID: &song, Path: src, SizeBytes: 1, FileModifiedAt: now, Container: r.Container, DurationMs: r.DurationMs, Bitrate: r.Bitrate, ProbedAt: &now}
	must(e.t, e.svc.MediaFiles.Repo().CreateFile(f))
	must(e.t, e.svc.MediaFiles.Repo().ReplaceTracks(f.ID, r.Tracks))
	return song
}

// The file itself is ready at once and served in ranges, resumable with
// If-Range, to the device that claimed it alone.
func TestDownloadTheFileItself(t *testing.T) {
	e := newEnv(t)
	m := e.probed("h264-high41.mp4", "")
	me, tok := e.plain.account.ID, e.plain.token
	var res api.DownloadResolution
	e.expect(200, "POST", downloadsOf(me)+"/resolve", tok, map[string]any{"targets": []map[string]any{{"itemId": m}}, "quality": "720p",
		"snapshot": sdrWeb()}, &res)
	if len(res.Titles) != 1 || !res.Titles[0].Available || *res.Titles[0].Verdict != api.DirectPlay || *res.Titles[0].Estimated ||
		*res.Titles[0].Quality != api.DownloadQuality("original") || res.Titles[0].Kind != api.DownloadTitleKindMovie || res.SizeBytes != 1 {
		t.Fatalf("a 640x360 file is its own 720p: %+v", res)
	}
	var d, again api.Download
	body := map[string]any{"itemId": m, "quality": "original", "snapshot": sdrWeb()}
	e.expect(201, "POST", downloadsOf(me), tok, body, &d)
	if d.Status != api.DownloadStatusReady || d.Url == nil || d.Verdict != api.DirectPlay || d.Container != "mp4" || d.ExpiresAt == nil ||
		!d.ExpiresAt.After(time.Now().Add(71*time.Hour)) {
		t.Fatalf("%+v", d)
	}
	if d.CreatedBy == nil || *d.CreatedBy != me || !d.ModifiedAt.Equal(d.CreatedAt) {
		t.Fatalf("audit fields: %+v", d)
	}
	if e.expect(201, "POST", downloadsOf(me), tok, body, &again); again.Id != d.Id {
		t.Fatalf("asked again: %s, then %s", d.Id, again.Id)
	}
	src, err := os.ReadFile(fixturePath(t, "h264-high41.mp4"))
	must(t, err)
	head, first := e.fetch(*d.Url, tok, map[string]string{"Range": "bytes=0-99"})
	tag := head.Header.Get("ETag")
	if head.StatusCode != 206 || len(first) != 100 || tag == "" || head.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("%d %d %q %q", head.StatusCode, len(first), tag, head.Header.Get("Content-Type"))
	}
	rest, tail := e.fetch(*d.Url, tok, map[string]string{"Range": "bytes=100-", "If-Range": tag})
	if rest.StatusCode != 206 || !bytes.Equal(append(first, tail...), src) {
		t.Fatalf("resumed: %d %d of %d", rest.StatusCode, len(tail), len(src))
	}
	if res, b := e.fetch(*d.Url, tok, map[string]string{"Range": "bytes=100-", "If-Range": `"stale"`}); res.StatusCode != 200 || len(b) != len(src) {
		t.Fatalf("a changed file starts over: %d %d", res.StatusCode, len(b))
	}
	if res, _ := e.fetch(*d.Url, "", nil); res.StatusCode != 401 {
		t.Fatalf("no token: %d", res.StatusCode)
	}
	if res, _ := e.fetch(*d.Url, e.admin.token, nil); res.StatusCode != 404 {
		t.Fatalf("another device: %d", res.StatusCode)
	}
	var page api.DownloadPage
	if e.expect(200, "GET", downloadsOf(me), tok, nil, &page); len(page.Items) != 1 || page.Items[0].Id != d.Id {
		t.Fatalf("%+v", page)
	}
	e.expect(404, "GET", downloadsOf(e.admin.account.ID)+"/"+itoa(d.Id), e.admin.token, nil, nil)

	e.expect(204, "POST", "/me/lock", tok, nil, nil)
	if res, b := e.fetch(*d.Url, tok, nil); res.StatusCode != 403 || !strings.Contains(string(b), "device_locked") {
		t.Fatalf("locked: %d %s", res.StatusCode, b)
	}
	e.expect(204, "POST", "/profiles/"+itoa(me)+"/unlock", tok, map[string]any{}, nil)
	e.expect(204, "DELETE", downloadsOf(me)+"/"+itoa(d.Id), tok, nil, nil)
	e.expect(404, "GET", downloadsOf(me)+"/"+itoa(d.Id), tok, nil, nil)
	if res, _ := e.fetch(*d.Url, tok, nil); res.StatusCode != 404 {
		t.Fatalf("forgotten: %d", res.StatusCode)
	}
}

// A device switched to a kid fetches only the kid's own downloads, as the
// spec's routes reach no other profile while a kid is active.
func TestDownloadFilesKeepToTheActiveKid(t *testing.T) {
	e := newEnv(t)
	m, sub := e.probed("h264-high41.mp4", ""), e.probed("hdr10-main10.mkv", "")
	me, tok := e.plain.account.ID, e.plain.token
	var d, s, own api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "original", "snapshot": sdrWeb()}, &d)
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": sub, "quality": "original", "snapshot": sdrWeb()}, &s)
	if res, _ := e.fetch(*d.Url, tok, nil); res.StatusCode != 200 {
		t.Fatalf("the adult's file: %d", res.StatusCode)
	}
	if res, _ := e.fetch(s.Subtitles[0].Url, tok, nil); res.StatusCode != 200 {
		t.Fatalf("the adult's subtitle: %d", res.StatusCode)
	}

	k := e.kid(e.plain, "Kid")
	e.expect(204, "POST", "/profiles/"+itoa(k.Id)+"/unlock", tok, map[string]any{}, nil)
	if res, _ := e.fetch(*d.Url, tok, nil); res.StatusCode != 404 {
		t.Fatalf("the adult's file to the kid: %d", res.StatusCode)
	}
	if res, _ := e.fetch(s.Subtitles[0].Url, tok, nil); res.StatusCode != 404 {
		t.Fatalf("the adult's subtitle to the kid: %d", res.StatusCode)
	}
	e.expect(201, "POST", downloadsOf(k.Id), tok, map[string]any{"itemId": m, "quality": "original", "snapshot": sdrWeb()}, &own)
	if res, _ := e.fetch(*own.Url, tok, nil); res.StatusCode != 200 {
		t.Fatalf("the kid's own file: %d", res.StatusCode)
	}
}

// A file the device does not play is converted, by the worker, into one
// MP4 it does: here HDR HEVC in Matroska with a forced bitmap subtitle,
// burned in, and a text subtitle beside it.
func TestDownloadConvertsWhatTheDeviceDoesNotPlay(t *testing.T) {
	e := newEnv(t)
	e.work()
	m := e.probed("hdr10-main10.mkv", "")
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "original", "snapshot": sdrWeb()}, &d)
	if d.Status == api.DownloadStatusReady || d.Url != nil || !d.Estimated || d.ExpiresAt != nil || d.Verdict != api.VideoTranscode || d.Container != "mp4" {
		t.Fatalf("%+v", d)
	}
	if len(d.Subtitles) != 1 || d.Subtitles[0].IsForced {
		t.Fatalf("only the text subtitle goes beside: %+v", d.Subtitles)
	}
	d = e.ready(me, d.Id, tok)
	res, b := e.fetch(*d.Url, tok, nil)
	if res.StatusCode != 200 || int64(len(b)) != d.SizeBytes || res.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("%d %d of %d", res.StatusCode, len(b), d.SizeBytes)
	}
	p := ffprobe(t, b)
	if len(p.Streams) != 2 || p.Streams[0].CodecType != "video" || p.Streams[0].Height != 360 || p.Streams[1].CodecName != "aac" ||
		!strings.Contains(p.Format.FormatName, "mp4") || !strings.HasPrefix(p.Format.Duration, "10.") {
		t.Fatalf("%+v", p)
	}
	res, vtt := e.fetch(d.Subtitles[0].Url, tok, nil)
	if res.StatusCode != 200 || !strings.HasPrefix(string(vtt), "WEBVTT") {
		t.Fatalf("%d %q", res.StatusCode, vtt)
	}
	var conv api.ConversionPage
	if e.expect(200, "GET", "/conversions", e.admin.token, nil, &conv); len(conv.Items) != 1 || conv.Items[0].Status != api.ConversionStatusReady ||
		conv.Items[0].Downloads != 1 || conv.Items[0].SizeBytes != d.SizeBytes {
		t.Fatalf("%+v", conv)
	}
	e.expect(403, "GET", "/conversions", tok, nil, nil)
	path := filepath.Join(e.svc.Downloads.Dir, itoa(conv.Items[0].Id)+".mp4")
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	e.expect(204, "DELETE", downloadsOf(me)+"/"+itoa(d.Id), tok, nil, nil)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a conversion no download needs goes: %v", err)
	}
	e.expect(404, "GET", "/conversions/"+itoa(conv.Items[0].Id), e.admin.token, nil, nil)
}

// A smaller size asked for is the size and bitrate the file converts to.
func TestDownloadConvertsToTheSizeAskedFor(t *testing.T) {
	if testing.Short() {
		t.Skip("needs ffmpeg")
	}
	src := filepath.Join(t.TempDir(), "wide.mp4")
	//nolint:gosec // a temp path
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=s=1280x544:r=24:d=3", "-c:v", "libx264", "-qp", "0",
		src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	e := newEnv(t)
	e.work()
	r, err := probe.Run(context.Background(), src)
	must(t, err)
	m := e.movie(e.library("movies"), "Wide")
	e.rate(m, "G")
	now := time.Now().UTC()
	f := &mediafile.File{ItemID: &m, Path: src, SizeBytes: 1, FileModifiedAt: now, Container: r.Container, DurationMs: r.DurationMs, Bitrate: r.Bitrate, ProbedAt: &now}
	must(t, e.svc.MediaFiles.Repo().CreateFile(f))
	must(t, e.svc.MediaFiles.Repo().ReplaceTracks(f.ID, r.Tracks))
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "480p", "snapshot": sdrWeb()}, &d)
	d = e.ready(me, d.Id, tok)
	_, b := e.fetch(*d.Url, tok, nil)
	p := filepath.Join(t.TempDir(), "fetched.mp4")
	must(t, os.WriteFile(p, b, 0o600))
	//nolint:gosec // a temp path
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v", "-show_entries", "stream=width,height,bit_rate", "-of", "csv=p=0", p).Output()
	must(t, err)
	var w, h, bps int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d,%d,%d", &w, &h, &bps); err != nil || w != 1130 || h != 480 || bps > 2e6 {
		t.Fatalf("%dx%d at %d b/s: %v", w, h, bps, err)
	}
}

// A lossless song becomes AAC at 256 kb/s in an M4A.
func TestDownloadConvertsASong(t *testing.T) {
	e := newEnv(t)
	e.work()
	song := e.probedSong("sample.flac")
	me, tok := e.plain.account.ID, e.plain.token
	var res api.DownloadResolution
	e.expect(200, "POST", downloadsOf(me)+"/resolve", tok, map[string]any{"targets": []map[string]any{{"itemId": song}}, "quality": "aac256",
		"snapshot": sdrWeb()}, &res)
	if len(res.Titles) != 1 || *res.Titles[0].Container != "m4a" || *res.Titles[0].Quality != api.DownloadQuality("aac256") || res.Titles[0].Kind != api.DownloadTitleKindSong {
		t.Fatalf("%+v", res)
	}
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": song, "quality": "aac256", "snapshot": sdrWeb()}, &d)
	d = e.ready(me, d.Id, tok)
	r, b := e.fetch(*d.Url, tok, nil)
	if r.Header.Get("Content-Type") != "audio/mp4" {
		t.Fatal(r.Header.Get("Content-Type"))
	}
	if p := ffprobe(t, b); len(p.Streams) != 1 || p.Streams[0].CodecName != "aac" || !strings.Contains(p.Format.FormatName, "m4a") {
		t.Fatalf("%+v", p)
	}
}

// A conversion's start and end reach the admin Logs page as the title's,
// naming the conversion.
func TestConversionsAreLoggedAsTheirTitles(t *testing.T) {
	e := newEnv(t)
	logs := logstore.NewWriter(e.svc.Logs)
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(logstore.NewHandler(slog.DiscardHandler, logs, slog.LevelInfo)))
	e.work()
	song := e.probedSong("sample.flac")
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": song, "quality": "aac256", "snapshot": sdrWeb()}, &d)
	e.ready(me, d.Id, tok)
	logs.Close()
	var page api.LogEntryPage
	e.expect(200, "GET", "/logs?source=transcode", e.admin.token, nil, &page)
	seen := map[string]bool{}
	for _, l := range page.Items {
		if l.ItemId != nil && *l.ItemId == song && strings.Contains(l.Message, "conversion_id=") {
			seen[strings.SplitN(l.Message, " conversion_id=", 2)[0]] = true
		}
	}
	if !seen["Conversion started"] || !seen["Conversion ready"] {
		t.Fatalf("%v of %+v", seen, page.Items)
	}
}

// A conversion waits while a live video transcode runs, and never takes
// its slot.
func TestConversionLeavesLiveTranscodesTheirSlots(t *testing.T) {
	e := newEnv(t)
	e.svc.Playback.Engine = transcode.New(transcode.Config{Root: t.TempDir(), MaxVideo: 1})
	e.work()
	m := e.probed("hdr10-main10.mkv", "")
	live := e.start(m, sdrWeb())
	if live.Entry.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", live.Entry)
	}
	me, tok := e.admin.account.ID, e.admin.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "360p", "snapshot": sdrWeb()}, &d)
	time.Sleep(2 * time.Second)
	if e.expect(200, "GET", downloadsOf(me)+"/"+itoa(d.Id), tok, nil, &d); d.Status == api.DownloadStatusReady || d.Progress != 0 {
		t.Fatalf("converted beside a live transcode: %+v", d)
	}
	if held := e.svc.Playback.Engine.Transcoding(); len(held) != 1 || held[0] != live.SessionId {
		t.Fatalf("slots %v", held)
	}
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(live.SessionId), e.plain.token, nil, nil)
	e.ready(me, d.Id, tok)
	var again api.PlaybackStream
	if e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": sdrWeb()},
		&again); again.Entry.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", again.Entry)
	}
}

// A conversion a stop left converting starts over, leaving no part of the
// first try, and cleaning up takes expired downloads, the conversions they
// needed and files that are no conversion's.
func TestConversionsRestartAndAreCleanedUp(t *testing.T) {
	e := newEnv(t)
	e.svc.Downloads.Dir = t.TempDir()
	m := e.probed("hdr10-main10.mkv", "")
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "original", "snapshot": snapshot()}, &d)
	repo := e.svc.Downloads.Repo()
	convs, err := repo.Conversions(10, 0)
	must(t, err)
	if len(convs) != 1 || convs[0].Status != download.Queued {
		t.Fatalf("%+v", convs)
	}
	must(t, repo.Start(convs[0].ID, time.Now()))
	part := filepath.Join(e.svc.Downloads.Dir, itoa(convs[0].ID)+".mp4.part")
	stray := filepath.Join(e.svc.Downloads.Dir, "999.mp4")
	must(t, os.WriteFile(part, []byte("half"), 0o600))
	must(t, os.WriteFile(stray, []byte("x"), 0o600))

	dir := e.svc.Downloads.Dir
	e.work()
	e.ready(me, d.Id, tok)
	for _, p := range []string{part, stray} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s stays: %v", p, err)
		}
	}
	file := filepath.Join(dir, itoa(convs[0].ID)+".mp4")
	must(t, e.svc.Downloads.Clean(time.Now()))
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a claimed conversion stays: %v", err)
	}
	must(t, e.svc.Downloads.Clean(time.Now().Add(73*time.Hour)))
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("expired: %v", err)
	}
	e.expect(404, "GET", downloadsOf(me)+"/"+itoa(d.Id), tok, nil, nil)
}

// Every download operation keeps to the profile's kid filters: a title the
// kid may not see answers as one that does not exist, and sync says to
// remove it.
func TestDownloadsKeepToTheAudience(t *testing.T) {
	e := newEnv(t)
	g := e.probed("h264-high41.mp4", "")
	it, err := e.svc.Items.Repo().Get(g)
	must(t, err)
	r := e.movie(it.LibraryID, "R")
	e.rate(r, "R")
	k := e.kid(e.plain, "Kid")
	kid := e.kidDevice(e.plain, k.Id)
	base := downloadsOf(k.Id)
	target := func(id uid.ID) map[string]any {
		return map[string]any{"targets": []map[string]any{{"itemId": id}}, "quality": "original", "snapshot": sdrWeb()}
	}
	e.expect(404, "POST", base+"/resolve", kid, target(r), nil)
	e.expect(404, "POST", base, kid, map[string]any{"itemId": r, "quality": "original", "snapshot": sdrWeb()}, nil)
	var res api.DownloadResolution
	if e.expect(200, "POST", base+"/resolve", kid, target(g), &res); len(res.Titles) != 1 {
		t.Fatalf("%+v", res)
	}
	var d api.Download
	e.expect(201, "POST", base, kid, map[string]any{"itemId": g, "quality": "original", "snapshot": sdrWeb()}, &d)
	var sync api.DownloadSync
	if e.expect(200, "POST", base+"/sync", kid, map[string]any{"itemIds": []uid.ID{g, r, uid.N(999999)}}, &sync); len(sync.Keep) != 1 || sync.Keep[0] != g ||
		len(sync.Remove) != 2 {
		t.Fatalf("%+v", sync)
	}

	e.rate(g, "R")
	e.expect(404, "GET", base+"/"+itoa(d.Id), kid, nil, nil)
	e.expect(404, "DELETE", base+"/"+itoa(d.Id), kid, nil, nil)
	var page api.DownloadPage
	if e.expect(200, "GET", base, kid, nil, &page); len(page.Items) != 0 {
		t.Fatalf("%+v", page)
	}
	if res, _ := e.fetch(*d.Url, kid, nil); res.StatusCode != 404 {
		t.Fatalf("file: %d", res.StatusCode)
	}
	if e.expect(200, "POST", base+"/sync", kid, map[string]any{"itemIds": []uid.ID{g}}, &sync); len(sync.Remove) != 1 {
		t.Fatalf("%+v", sync)
	}
	// A collection or playlist yields only what the kid may see.
	e.rate(g, "G")
	var pl api.Playlist
	lists := "/profiles/" + itoa(e.plain.account.ID) + "/playlists"
	e.expect(201, "POST", lists, e.plain.token, map[string]any{"name": "Night", "kind": "movies"}, &pl)
	for _, id := range []uid.ID{r, g} {
		e.expect(201, "POST", lists+"/"+itoa(pl.Id)+"/entries", e.plain.token, map[string]any{"itemId": id}, nil)
	}
	e.expect(201, "POST", lists+"/"+itoa(pl.Id)+"/shares", e.plain.token, map[string]any{"profileId": k.Id, "access": "read"}, nil)
	p := pl.Id
	if e.expect(200, "POST", base+"/resolve", kid, map[string]any{"targets": []map[string]any{{"playlistId": p}}, "quality": "original",
		"snapshot": sdrWeb()}, &res); len(res.Titles) != 1 || res.Titles[0].ItemId != g {
		t.Fatalf("%+v", res)
	}
	e.expect(400, "POST", base+"/resolve", kid, map[string]any{"targets": []map[string]any{{"playlistId": p, "itemId": g}}, "quality": "original",
		"snapshot": sdrWeb()}, nil)
}

// A file of unknown length converts whole; only its progress cannot be
// told until it is done.
func TestDownloadConvertsAFileOfUnknownLength(t *testing.T) {
	e := newEnv(t)
	e.work()
	m := e.probed("hdr10-main10.mkv", "")
	fs, err := e.svc.MediaFiles.Repo().FilesForItem(m)
	must(t, err)
	fs[0].DurationMs = nil
	must(t, e.svc.MediaFiles.Repo().UpdateFile(fs[0]))
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "original", "snapshot": sdrWeb()}, &d)
	if d.Verdict != api.VideoTranscode {
		t.Fatalf("%+v", d)
	}
	d = e.ready(me, d.Id, tok)
	if _, b := e.fetch(*d.Url, tok, nil); !strings.HasPrefix(ffprobe(t, b).Format.Duration, "10.") {
		t.Fatalf("%+v", ffprobe(t, b))
	}
}

// fileOf is the path of a download's file, asked to wait for it.
func fileOf(id uid.ID) string { return httpapi.BasePath + "/downloads/" + itoa(id) + "/file?wait=1" }

// fetchLater starts a fetch and hands over its response, nil if it failed.
func (e *env) fetchLater(url, token string, header map[string]string) <-chan *http.Response {
	ch := make(chan *http.Response, 1)
	go func() {
		req, err := http.NewRequest("GET", strings.TrimSuffix(e.url, httpapi.BasePath)+url, nil)
		if err != nil {
			ch <- nil
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		for k, v := range header {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			ch <- nil
			return
		}
		ch <- res
	}()
	return ch
}

// A file asked for with wait before its conversion is ready is served, in
// the range asked, once it is; asked without, it is not ready.
func TestDownloadFileWaitsForItsConversion(t *testing.T) {
	e := newEnv(t)
	m := e.probed("hdr10-main10.mkv", "")
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "360p", "snapshot": sdrWeb()}, &d)
	if res, _ := e.fetch(strings.TrimSuffix(fileOf(d.Id), "?wait=1"), tok, nil); res.StatusCode != 409 {
		t.Fatalf("not waiting: %d", res.StatusCode)
	}
	held := e.fetchLater(fileOf(d.Id), tok, map[string]string{"Range": "bytes=0-99"})
	select {
	case res := <-held:
		t.Fatalf("answered before the conversion ran: %+v", res)
	case <-time.After(500 * time.Millisecond):
	}
	e.work()
	var res *http.Response
	select {
	case res = <-held:
	case <-time.After(90 * time.Second):
		t.Fatal("still held")
	}
	if res == nil {
		t.Fatal("the held request failed")
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 206 || len(b) != 100 || res.Header.Get("ETag") == "" || res.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("%d %d %q", res.StatusCode, len(b), res.Header.Get("ETag"))
	}
	d = e.ready(me, d.Id, tok)
	if whole, all := e.fetch(fileOf(d.Id), tok, nil); whole.StatusCode != 200 || int64(len(all)) != d.SizeBytes || !bytes.Equal(all[:100], b) {
		t.Fatalf("%d %d of %d", whole.StatusCode, len(all), d.SizeBytes)
	}
}

// A conversion failing while its file is waited for ends the wait with the
// failure getDownload reports.
func TestDownloadFileWaitEndsWithAFailedConversion(t *testing.T) {
	e := newEnv(t)
	src := filepath.Join(t.TempDir(), "going.mkv")
	must(t, os.Symlink(fixturePath(t, "hdr10-main10.mkv"), src))
	m := e.probed("hdr10-main10.mkv", src)
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "360p", "snapshot": sdrWeb()}, &d)
	held := e.fetchLater(fileOf(d.Id), tok, nil)
	time.Sleep(300 * time.Millisecond)
	must(t, os.Remove(src))
	e.work()
	var res *http.Response
	select {
	case res = <-held:
	case <-time.After(30 * time.Second):
		t.Fatal("still held")
	}
	if res == nil {
		t.Fatal("the held request failed")
	}
	defer func() { _ = res.Body.Close() }()
	var body api.Error
	must(t, json.NewDecoder(res.Body).Decode(&body))
	var polled api.Download
	e.expect(200, "GET", downloadsOf(me)+"/"+itoa(d.Id), tok, nil, &polled)
	if res.StatusCode != 500 || body.Code != api.ErrorCodeInternal || polled.Status != api.DownloadStatusFailed || polled.Error == nil ||
		body.Message != *polled.Error {
		t.Fatalf("%d %+v, polled %+v", res.StatusCode, body, polled)
	}
	if again, _ := e.fetch(fileOf(d.Id), tok, nil); again.StatusCode != 500 {
		t.Fatalf("asked again: %d", again.StatusCode)
	}
}

// A client that stops waiting leaves nothing held on the server.
func TestDownloadFileWaitEndsWithTheClient(t *testing.T) {
	e := newEnv(t)
	h, err := httpapi.NewHandler("1.2.3", e.svc)
	must(t, err)
	srv := httptest.NewServer(h)
	m := e.probed("hdr10-main10.mkv", "")
	me, tok := e.plain.account.ID, e.plain.token
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": m, "quality": "360p", "snapshot": sdrWeb()}, &d)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+fileOf(d.Id), nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+tok)
	done := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = res.Body.Close()
		}
		done <- err
	}()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err == nil {
		t.Fatal("answered with nothing converted")
	}
	closed := make(chan struct{})
	go func() { srv.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the request is still held after its client went")
	}
}

// batchOf is a createDownloads body for the items at quality q.
func batchOf(q string, ids ...uid.ID) map[string]any {
	titles := []map[string]any{}
	for _, id := range ids {
		titles = append(titles, map[string]any{"itemId": id})
	}
	return map[string]any{"titles": titles, "quality": q, "snapshot": sdrWeb()}
}

// A claim may name the download's id; a taken one is a conflict.
func TestCreateDownloadTakesAClientID(t *testing.T) {
	e := newEnv(t)
	m, other := e.probed("h264-high41.mp4", ""), e.probed("chapters.mkv", "")
	me, tok := e.plain.account.ID, e.plain.token
	id := uid.New()
	var d api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"id": id, "itemId": m, "quality": "original", "snapshot": sdrWeb()}, &d)
	if d.Id != id || d.Url == nil || !strings.Contains(*d.Url, "/downloads/"+id.String()+"/file") {
		t.Fatalf("%+v", d)
	}
	e.expect(200, "GET", downloadsOf(me)+"/"+itoa(id), tok, nil, nil)
	e.expect(409, "POST", downloadsOf(me), tok, map[string]any{"id": id, "itemId": other, "quality": "original", "snapshot": sdrWeb()}, nil)
	var b api.DownloadBatch
	bid := uid.New()
	e.expect(200, "POST", downloadsOf(me)+"/batch", tok, map[string]any{"titles": []map[string]any{{"id": bid, "itemId": other}}, "quality": "original",
		"snapshot": sdrWeb()}, &b)
	if len(b.Results) != 1 || b.Results[0].Download == nil || b.Results[0].Download.Id != bid {
		t.Fatalf("%+v", b.Results)
	}
}

// A batch claims each title as a lone claim would, and a title that cannot
// be claimed answers its own error without refusing the rest.
func TestCreateDownloadsClaimsEachTitleOnItsOwn(t *testing.T) {
	e := newEnv(t)
	g := e.probed("h264-high41.mp4", "")
	gone := e.probed("h264-high41.mp4", filepath.Join(t.TempDir(), "gone.mp4"))
	it, err := e.svc.Items.Repo().Get(g)
	must(t, err)
	r := e.movie(it.LibraryID, "R")
	e.rate(r, "R")
	show, _ := e.show(e.library("tv"), "S", "TV-Y")
	me, tok := e.plain.account.ID, e.plain.token
	var one api.Download
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"itemId": g, "quality": "original", "snapshot": sdrWeb()}, &one)
	var b api.DownloadBatch
	ids := []uid.ID{uid.N(999999), g, show, gone, g}
	e.expect(200, "POST", downloadsOf(me)+"/batch", tok, batchOf("original", ids...), &b)
	codes := []string{}
	for i, res := range b.Results {
		if res.ItemId != ids[i] || (res.Download == nil) == (res.Error == nil) {
			t.Fatalf("%d: %+v", i, res)
		}
		code := ""
		if res.Error != nil {
			code = string(res.Error.Code)
		}
		codes = append(codes, code)
	}
	if strings.Join(codes, ",") != "not_found,,bad_request,conflict," || b.Results[1].Download.Id != one.Id || b.Results[4].Download.Id != one.Id {
		t.Fatalf("%v %+v", codes, b.Results)
	}
	e.expect(400, "POST", downloadsOf(me)+"/batch", tok, batchOf("original"), nil)

	k := e.kid(e.plain, "Kid")
	kid := e.kidDevice(e.plain, k.Id)
	if e.expect(200, "POST", downloadsOf(k.Id)+"/batch", kid, batchOf("original", r, g), &b); len(b.Results) != 2 || b.Results[0].Error == nil ||
		b.Results[0].Error.Code != api.ErrorCodeNotFound || b.Results[1].Download == nil || b.Results[1].Download.ProfileId != k.Id {
		t.Fatalf("%+v", b.Results)
	}
	e.expect(403, "POST", downloadsOf(me)+"/batch", kid, batchOf("original", g), nil)
}

// A title whose client id another download holds answers conflict on its
// own; the titles around it are claimed all the same.
func TestCreateDownloadsAnswersATakenIdForThatTitleAlone(t *testing.T) {
	e := newEnv(t)
	m, other, third := e.probed("h264-high41.mp4", ""), e.probed("chapters.mkv", ""), e.probed("hdr10-main10.mkv", "")
	me, tok := e.plain.account.ID, e.plain.token
	taken := uid.New()
	e.expect(201, "POST", downloadsOf(me), tok, map[string]any{"id": taken, "itemId": m, "quality": "original", "snapshot": sdrWeb()}, nil)
	var b api.DownloadBatch
	e.expect(200, "POST", downloadsOf(me)+"/batch", tok, map[string]any{"quality": "original", "snapshot": sdrWeb(),
		"titles": []map[string]any{{"itemId": other}, {"id": taken, "itemId": third}}}, &b)
	if len(b.Results) != 2 || b.Results[0].Download == nil || b.Results[1].Error == nil || b.Results[1].Error.Code != api.ErrorCodeConflict {
		t.Fatalf("%+v", b.Results)
	}
}

// conversionsOf waits until the conversions of the downloads are all ready,
// reading the store alone, and returns them.
func (e *env) conversionsOf(ids ...uid.ID) []*download.Conversion {
	e.t.Helper()
	repo := e.svc.Downloads.Repo()
	for end := time.Now().Add(90 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		var convs []*download.Conversion
		ready := 0
		for _, id := range ids {
			d, err := repo.Get(id)
			must(e.t, err)
			c, err := repo.Conversion(*d.ConversionID)
			must(e.t, err)
			convs = append(convs, c)
			if c.Status == download.Ready {
				ready++
			}
		}
		if ready == len(ids) {
			return convs
		}
		if time.Now().After(end) {
			e.t.Fatalf("not ready: %+v", convs)
		}
	}
}

// Conversions a batch claims run in the order asked, back to back, with no
// client fetching anything in between.
func TestCreateDownloadsConvertsBackToBack(t *testing.T) {
	e := newEnv(t)
	other := filepath.Join(t.TempDir(), "other.mkv")
	must(t, os.Symlink(fixturePath(t, "hdr10-main10.mkv"), other))
	first, second := e.probed("hdr10-main10.mkv", ""), e.probed("hdr10-main10.mkv", other)
	me, tok := e.plain.account.ID, e.plain.token
	var b api.DownloadBatch
	e.expect(200, "POST", downloadsOf(me)+"/batch", tok, batchOf("360p", second, first), &b)
	if len(b.Results) != 2 || b.Results[0].Download == nil || b.Results[1].Download == nil {
		t.Fatalf("%+v", b.Results)
	}
	e.work()
	convs := e.conversionsOf(b.Results[0].Download.Id, b.Results[1].Download.Id)
	if gap := convs[1].StartedAt.Sub(*convs[0].FinishedAt); gap < 0 || gap > 5*time.Second {
		t.Fatalf("the second started %s after the first finished", gap)
	}
}
