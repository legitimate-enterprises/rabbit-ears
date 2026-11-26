package httpapi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
)

// copyOf adds a probed fixture to movie m as the scanner would, in the
// edition its tag names ("" for the untagged one), and returns its file id.
func (e *env) copyOf(m int64, name, tag string) int64 {
	e.t.Helper()
	src := fixturePath(e.t, name)
	r, err := probe.Run(context.Background(), src)
	must(e.t, err)
	now := time.Now().UTC()
	f := &mediafile.File{Path: e.onDisk("/films/" + tag + "/" + name), SizeBytes: 1, ModifiedAt: now, Container: r.Container, DurationMs: r.DurationMs,
		Bitrate: r.Bitrate, ProbedAt: &now}
	must(e.t, e.svc.MediaFiles.Repo().CreateFile(f))
	must(e.t, e.svc.MediaFiles.Repo().ReplaceTracks(f.ID, r.Tracks))
	_, err = e.svc.Editions.Repo().Place(f.ID, m, tag, f.DurationMs, true)
	must(e.t, err)
	return f.ID
}

func (e *env) startEdition(item, edition int64, snap map[string]any) api.PlaybackStream {
	e.t.Helper()
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": item,
		"editionId": edition, "snapshot": snap}, &st)
	return st
}

func (e *env) sessionFile(id int64) int64 {
	e.t.Helper()
	s, err := e.svc.Sessions.Repo().Get(id)
	must(e.t, err)
	return s.MediaFileID
}

// Details list a movie's editions with their copies; playback plays the
// default edition unless told otherwise and picks the copy that direct
// plays; the resume position belongs to the edition it was played in.
func TestEditionsInDetailsPlaybackAndResume(t *testing.T) {
	e := newEnv(t)
	m := e.probed("hdr10-main10.mkv", e.onDisk("/films/a.mkv")) // HEVC HDR in MKV: not direct play on this browser
	mp4 := e.copyOf(m, "h264-high41.mp4", "")
	cut := e.copyOf(m, "chapters.mkv", "Extended")

	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(m), e.plain.token, nil, &it)
	if it.Editions == nil || len(*it.Editions) != 2 {
		t.Fatalf("editions %+v", it.Editions)
	}
	std, ext := (*it.Editions)[0], (*it.Editions)[1]
	if std.Name != "Standard" || !std.IsDefault || len(std.Copies) != 2 || ext.Name != "Extended" || ext.IsDefault ||
		len(ext.Copies) != 1 || ext.Copies[0].MediaFileId != cut || *ext.Copies[0].Quality != "SD" || ext.RuntimeMs == nil {
		t.Fatalf("standard %+v extended %+v", std, ext)
	}

	st := e.start(m, sdrWeb())
	if st.Entry.Verdict != api.DirectPlay || e.sessionFile(st.SessionId) != mp4 {
		t.Fatalf("default edition played %d at %s", e.sessionFile(st.SessionId), st.Entry.Verdict)
	}
	st = e.startEdition(m, ext.Id, sdrWeb())
	if e.sessionFile(st.SessionId) != cut {
		t.Fatalf("extended played file %d", e.sessionFile(st.SessionId))
	}
	e.expect(204, "PUT", "/playback/sessions/"+itoa(st.SessionId)+"/position", e.plain.token, map[string]any{"positionMs": 3000}, nil)
	var ws api.WatchState
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(m), e.plain.token, nil, &ws)
	if ws.EditionId == nil || *ws.EditionId != ext.Id || ws.PositionMs != 3000 {
		t.Fatalf("watch state %+v", ws)
	}
	if st := e.start(m, sdrWeb()); st.PositionMs != 0 {
		t.Fatalf("the default edition resumed the extended one's position: %d", st.PositionMs)
	}
	if st := e.startEdition(m, ext.Id, sdrWeb()); st.PositionMs != 3000 {
		t.Fatalf("the extended edition did not resume: %d", st.PositionMs)
	}
	w, err := e.svc.WatchStates.Repo().Get(e.plain.account.ID, m)
	must(t, err)
	w.Watched = true
	must(t, e.svc.WatchStates.Repo().Save(w))
	if st := e.startEdition(m, ext.Id, sdrWeb()); st.PositionMs != 3000 {
		t.Fatalf("a watched title did not resume its position: %d", st.PositionMs)
	}

	other := e.probed("h264-high41.mp4", e.onDisk("/films/other.mp4"))
	e.expect(404, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": other,
		"editionId": ext.Id, "snapshot": sdrWeb()}, nil)
}

// A title with several editions joins a playlist only with one named; a
// title with one is stored with it. Rows carry the edition and its runtime,
// and its name only while the title has several.
func TestPlaylistEntriesNameTheirEdition(t *testing.T) {
	e := newEnv(t)
	tok, base := e.plain.token, "/profiles/"+itoa(e.plain.account.ID)+"/playlists"
	m := e.probed("h264-high41.mp4", e.onDisk("/films/a.mp4"))
	single := e.probed("chapters.mkv", e.onDisk("/films/single.mkv"))
	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "P", "kind": "movies"}, &pl)
	one := base + "/" + itoa(pl.Id)

	var lone api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": single}, &lone)
	if lone.EditionId == nil || lone.EditionName != nil {
		t.Fatalf("single-edition entry %+v", lone)
	}
	e.copyOf(m, "chapters.mkv", "Extended")
	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(m), tok, nil, &it)
	ext := (*it.Editions)[1]

	raw := e.expect(400, "POST", one+"/entries", tok, map[string]any{"itemId": m}, nil)
	if code := errorCode(t, raw); code != "bad_request" || !strings.Contains(string(raw), `"editionId"`) {
		t.Fatalf("code %s: %s", code, raw)
	}
	e.expect(400, "POST", one+"/entries", tok, map[string]any{"itemId": single, "editionId": ext.Id}, nil)
	var en api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": m, "editionId": ext.Id}, &en)
	if en.EditionId == nil || *en.EditionId != ext.Id || *en.EditionName != "Extended" || en.DurationMs != *ext.RuntimeMs {
		t.Fatalf("entry %+v", en)
	}
	e.expect(400, "PUT", one+"/entries/"+itoa(en.Id), tok, map[string]any{"itemId": m, "editionId": (*it.Editions)[0].Id}, nil)
	e.expect(200, "PUT", one+"/entries/"+itoa(en.Id), tok, map[string]any{"itemId": m, "position": 0}, &en)

	var order api.PlaylistPlayOrderPage
	e.expect(200, "GET", one+"/play-order", tok, nil, &order)
	if len(order.Items) != 2 || *order.Items[0].EditionId != ext.Id || *order.Items[0].EditionName != "Extended" ||
		*order.Items[0].DurationMs != *ext.RuntimeMs || *order.Items[1].EditionId != *lone.EditionId || order.Items[1].EditionName != nil {
		t.Fatalf("play order %+v", order.Items)
	}
}

// Playback trusts the disk over the missing mark, which is only as fresh
// as the last scan: a title with no file on disk answers 409 and its file
// is marked; the file back on disk plays and loses its mark; and when the
// copy that plays best is gone, the copy on disk plays.
func TestPlaybackTrustsTheDiskOverTheMark(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(t.TempDir(), "a.mkv")
	must(t, os.Link(fixturePath(t, "hdr10-main10.mkv"), path))
	m := e.probed("hdr10-main10.mkv", path)
	files := e.svc.MediaFiles.Repo()
	mkv, err := files.FileByPath(path)
	must(t, err)
	missing := func(id int64) bool {
		t.Helper()
		f, err := files.File(id)
		must(t, err)
		return f.MissingAt != nil
	}
	req := map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": sdrWeb()}

	away := path + ".away"
	must(t, os.Rename(path, away))
	for _, c := range []struct {
		path string
		body any
	}{{"/playback/sessions", req}, {"/items/" + itoa(m) + "/playability", sdrWeb()}} {
		if raw := e.expect(409, "POST", c.path, e.plain.token, c.body, nil); errorCode(t, raw) != "conflict" || !strings.Contains(string(raw), "unavailable") {
			t.Fatalf("%s with no file on disk: %s", c.path, raw)
		}
	}
	if !missing(mkv.ID) {
		t.Fatal("a file found gone is not marked")
	}

	must(t, os.Rename(away, path))
	e.expect(200, "POST", "/items/"+itoa(m)+"/playability", e.plain.token, sdrWeb(), nil)
	if missing(mkv.ID) {
		t.Fatal("a file found back is still marked")
	}

	mp4 := e.copyOf(m, "h264-high41.mp4", "")
	if st := e.start(m, sdrWeb()); e.sessionFile(st.SessionId) != mp4 {
		t.Fatalf("with both on disk the direct play copy plays, not file %d", e.sessionFile(st.SessionId))
	}
	f, err := files.File(mp4)
	must(t, err)
	must(t, os.Remove(f.Path))
	if st := e.start(m, sdrWeb()); e.sessionFile(st.SessionId) != mkv.ID || !missing(mp4) || missing(mkv.ID) {
		t.Fatalf("played file %d; mp4 marked %v, mkv marked %v", e.sessionFile(st.SessionId), missing(mp4), missing(mkv.ID))
	}
}

// A movie, song or video whose files are all marked missing is listed as
// unavailable, alone and in a page. A movie with one copy present is not,
// and its details say which copy is missing.
func TestUnavailableTitles(t *testing.T) {
	e := newEnv(t)
	files := e.svc.MediaFiles.Repo()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	gone := func(itemID int64) int64 {
		t.Helper()
		fs, err := files.FilesForItem(itemID)
		must(t, err)
		must(t, files.SetMissing(fs[0], &at))
		return fs[0].ID
	}
	yes := func(b *bool) bool { return b != nil && *b }
	tok := e.plain.token

	m := e.probed("h264-high41.mp4", e.onDisk("/films/a.mp4"))
	here := e.probed("chapters.mkv", e.onDisk("/films/b.mkv"))
	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(m), tok, nil, &it)
	if it.Unavailable != nil {
		t.Fatalf("a movie whose file is present: %v", *it.Unavailable)
	}
	f := gone(m)
	e.expect(200, "GET", "/items/"+itoa(m), tok, nil, &it)
	if !yes(it.Unavailable) || !yes((*it.Editions)[0].Copies[0].Unavailable) {
		t.Fatalf("details %+v", it)
	}
	var page api.ItemPage
	e.expect(200, "GET", "/items?kind=movie", tok, nil, &page)
	for _, v := range page.Items {
		if yes(v.Unavailable) != (v.Id == m) {
			t.Fatalf("listed movie %d unavailable %v; %d is present", v.Id, v.Unavailable, here)
		}
	}
	var mf api.MediaFile
	e.expect(200, "GET", "/media-files/"+itoa(f), tok, nil, &mf)
	if mf.MissingAt == nil || !mf.MissingAt.Equal(at) {
		t.Fatalf("missing since %v", mf.MissingAt)
	}
	second := e.copyOf(m, "chapters.mkv", "")
	e.expect(200, "GET", "/items/"+itoa(m), tok, nil, &it)
	if it.Unavailable != nil {
		t.Fatal("unavailable with a copy present")
	}
	for _, c := range (*it.Editions)[0].Copies {
		if yes(c.Unavailable) != (c.MediaFileId != second) {
			t.Fatalf("copy %+v", c)
		}
	}

	mu := e.music()
	gone(mu.songs[0].Id)
	var songs api.MusicSongPage
	e.expect(200, "GET", "/music/songs?albumId="+itoa(mu.album.Id), tok, nil, &songs)
	if len(songs.Items) != 3 || !yes(songs.Items[0].Unavailable) || songs.Items[1].Unavailable != nil {
		t.Fatalf("songs %+v", songs.Items)
	}
	var song api.MusicSong
	e.expect(200, "GET", "/music/songs/"+itoa(mu.songs[0].Id), tok, nil, &song)
	if !yes(song.Unavailable) {
		t.Fatal("song alone")
	}

	tb := e.youtubeLibrary()
	gone(tb.older)
	var vids api.YouTubeVideoPage
	e.expect(200, "GET", "/youtube/videos?sort=oldest&channelId="+itoa(tb.channel), tok, nil, &vids)
	if len(vids.Items) != 2 || !yes(vids.Items[0].Unavailable) || vids.Items[1].Unavailable != nil {
		t.Fatalf("videos %+v", vids.Items)
	}
	var vid api.YouTubeVideo
	e.expect(200, "GET", "/youtube/videos/"+itoa(tb.older), tok, nil, &vid)
	if !yes(vid.Unavailable) {
		t.Fatal("video alone")
	}
}
