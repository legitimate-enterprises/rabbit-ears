package httpapi_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/scan"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// scannedMusic scans a copy of the music fixtures into a new Music library
// the way the nightly task and the Local metadata task after it do, without
// a metadata provider.
func (e *env) scannedMusic() uid.ID {
	e.t.Helper()
	src := fixturePath(e.t, "music")
	lib := e.library("music")
	root := e.t.TempDir()
	must(e.t, os.CopyFS(root, os.DirFS(src)))
	must(e.t, e.svc.Libraries.Repo().AddFolder(&library.Folder{LibraryID: lib, Path: root}, nil))
	l, err := e.svc.Libraries.Repo().Get(lib)
	must(e.t, err)
	sc := scan.New(scan.Config{Libraries: e.svc.Libraries.Repo(), Items: e.svc.Items.Repo(), Files: e.svc.MediaFiles.Repo(),
		Editions: e.svc.Editions.Repo(), Genres: e.svc.Genres.Repo(), People: e.svc.People.Repo(), ThumbDir: e.t.TempDir()})
	_, err = sc.Scan(context.Background(), l, nil)
	must(e.t, err)
	_, _, err = sc.MakeLocal(context.Background(), nil, nil)
	must(e.t, err)
	return lib
}

type music struct {
	band, guest, folder api.MusicArtist
	album, loose        api.MusicAlbum
	songs               []api.MusicSong // First Light in track order
}

func (e *env) music() music {
	e.t.Helper()
	e.scannedMusic()
	var m music
	var artists api.MusicArtistPage
	e.expect(200, "GET", "/music/artists", e.plain.token, nil, &artists)
	if len(artists.Items) != 3 {
		e.t.Fatalf("artists %+v", artists)
	}
	m.band, m.folder, m.guest = artists.Items[0], artists.Items[1], artists.Items[2]
	var albums api.MusicAlbumPage
	e.expect(200, "GET", "/music/albums", e.plain.token, nil, &albums)
	m.album, m.loose = albums.Items[0], albums.Items[1]
	var songs api.MusicSongPage
	e.expect(200, "GET", "/music/songs?albumId="+itoa(m.album.Id), e.plain.token, nil, &songs)
	m.songs = songs.Items
	return m
}

func TestMusicBrowse(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	// Artists sort by name without "The".
	if m.band.Name != "The Example Band" || m.band.SortName != "Example Band" || m.band.AlbumCount != 1 || m.band.SongCount != 3 ||
		m.guest.Name != "Guest Singer" || m.guest.AlbumCount != 0 || m.guest.SongCount != 1 || len(m.band.Genres) != 1 || m.band.Genres[0].Name != "Rock" {
		t.Fatalf("artists %+v %+v", m.band, m.guest)
	}
	if a := m.album; a.Title != "First Light" || a.ArtistId != m.band.Id || a.ArtistName != "The Example Band" || a.SongCount != 3 ||
		a.Lossless || a.DurationMs < 8900 || a.Year == nil || *a.Year != 2024 || a.LastPlayedAt != nil || a.Genres[0].Name != "Rock" {
		t.Fatalf("album %+v", a)
	}
	if !m.loose.Lossless || m.loose.Title != "Folder Album" {
		t.Fatalf("an album of FLAC only is lossless: %+v", m.loose)
	}
	var titles []string
	for _, s := range m.songs {
		titles = append(titles, s.Title)
	}
	if !slices.Equal(titles, []string{"Opening", "Second Wind", "Last Call"}) {
		t.Fatalf("track order %v", titles)
	}
	if s := m.songs[1]; s.ArtistId != m.guest.Id || s.ArtistName != "Guest Singer" || s.AlbumArtistId != m.band.Id || s.AlbumTitle != "First Light" ||
		s.Codec == nil || *s.Codec != "mp3" || s.Lossless || *s.TrackNumber != 2 || *s.DiscNumber != 1 {
		t.Fatalf("song %+v", s)
	}
	if s := m.songs[0]; !s.Lossless || *s.Codec != "flac" || s.DurationMs == nil || s.ArtistId != m.band.Id {
		t.Fatalf("song %+v", s)
	}

	var one api.MusicArtist
	e.expect(200, "GET", "/music/artists/"+itoa(m.band.Id), e.plain.token, nil, &one)
	if one.Id != m.band.Id || one.SongCount != 3 {
		t.Fatalf("%+v", one)
	}
	var al api.MusicAlbum
	e.expect(200, "GET", "/music/albums/"+itoa(m.album.Id), e.plain.token, nil, &al)
	var song api.MusicSong
	e.expect(200, "GET", "/music/songs/"+itoa(m.songs[2].Id), e.plain.token, nil, &song)
	if al.Id != m.album.Id || song.Title != "Last Call" {
		t.Fatalf("%+v %+v", al, song)
	}
	e.expect(404, "GET", "/music/artists/"+itoa(m.album.Id), e.plain.token, nil, nil)
	e.expect(404, "GET", "/music/albums/"+itoa(m.songs[0].Id), e.plain.token, nil, nil)
	e.expect(404, "GET", "/music/songs/"+itoa(m.album.Id), e.plain.token, nil, nil)
	e.expect(404, "GET", "/items/"+itoa(m.album.Id), e.plain.token, nil, nil)

	var page api.MusicArtistPage
	e.expect(200, "GET", "/music/artists?letter=G", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != m.guest.Id {
		t.Fatalf("letter %+v", page)
	}
	e.expect(200, "GET", "/music/artists?limit=2", e.plain.token, nil, &page)
	if len(page.Items) != 2 || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("paging %+v", page)
	}
	var letters api.MusicLetterCountList
	e.expect(200, "GET", "/music/letters?of=artists", e.plain.token, nil, &letters)
	if !slices.Equal(letters.Items, []api.MusicLetterCount{{Letter: "E", Count: 1}, {Letter: "F", Count: 1}, {Letter: "G", Count: 1}}) {
		t.Fatalf("letters %+v", letters)
	}
	var albums api.MusicAlbumPage
	e.expect(200, "GET", "/music/albums?artistId="+itoa(m.band.Id), e.plain.token, nil, &albums)
	if len(albums.Items) != 1 || albums.Items[0].Id != m.album.Id {
		t.Fatalf("by artist %+v", albums)
	}
	var songs api.MusicSongPage
	e.expect(200, "GET", "/music/songs?artistId="+itoa(m.guest.Id), e.plain.token, nil, &songs)
	if len(songs.Items) != 1 || songs.Items[0].Id != m.songs[1].Id {
		t.Fatalf("songs a guest performs %+v", songs)
	}
	var found api.MusicSearchResult
	e.expect(200, "GET", "/music/search?q=fold", e.plain.token, nil, &found)
	if len(found.Artists) != 1 || len(found.Albums) != 1 || len(found.Songs) != 0 {
		t.Fatalf("search %+v", found)
	}
	e.expect(200, "GET", "/music/search?q=wind&limit=1", e.plain.token, nil, &found)
	if len(found.Songs) != 1 || found.Artists == nil || len(found.Albums) != 0 {
		t.Fatalf("search %+v", found)
	}

	// Album art is the FLAC's embedded cover; a song and an artist show
	// their album's.
	for _, id := range []uid.ID{m.album.Id, m.songs[1].Id, m.band.Id, m.loose.Id} {
		res, body := e.raw("GET", "/items/"+itoa(id)+"/artwork/poster", e.plain.token)
		if res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || len(body) < 100 {
			t.Fatalf("art of %s: %d %q", id, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
	e.expect(404, "GET", "/items/"+itoa(m.guest.Id)+"/artwork/poster", e.plain.token, nil, nil)
}

// songSnapshot is a music client at home: FLAC, MP3 and MP4 files, FLAC,
// AAC and MP3 decoding, stereo.
func songSnapshot(local, metered bool) map[string]any {
	s := snapshot()
	s["decoder"] = map[string]any{"protocols": []string{"http", "hls"}, "containers": []string{"flac", "mp3", "mp4"}, "videoCodecs": []any{},
		"audioCodecs": []string{"flac", "aac", "mp3"}, "subtitleFormats": []string{}}
	s["network"] = map[string]any{"discoveredLocally": local, "metered": metered, "lowData": false, "vpn": false, "auto": true}
	return s
}

func TestSongPlayback(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	st := e.start(m.songs[0].Id, songSnapshot(true, false))
	n := st.Negotiated
	if st.Entry.Verdict != api.DirectPlay || st.StreamKind != api.PlaybackStreamStreamKindFile || n.Width != 0 || n.Height != 0 ||
		n.AudioCodec == nil || *n.AudioCodec != "flac" || n.Lossless == nil || !*n.Lossless || len(st.Rungs) != 0 || st.Previews != nil || st.Away != nil {
		t.Fatalf("lossless at home plays as it is: %+v", st)
	}
	res, b := e.get(st.StreamUrl)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "audio/flac" || len(b) < 1000 {
		t.Fatalf("%d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}

	// Away, lossless converts to AAC at 256 kb/s; on a metered path at 128.
	for _, c := range []struct {
		metered bool
		bitrate int64
	}{{false, 256000}, {true, 128000}} {
		st = e.start(m.songs[0].Id, songSnapshot(false, c.metered))
		n = st.Negotiated
		if st.Entry.Verdict != api.AudioTranscode || n.Bitrate != c.bitrate || *n.AudioCodec != "aac" || n.Lossless != nil || len(st.Rungs) != 0 {
			t.Fatalf("away %+v: %+v", c, st)
		}
	}
	res, master := e.get(st.StreamUrl)
	if res.StatusCode != 200 || !strings.Contains(string(master), `CODECS="mp4a.40.2"`) || strings.Contains(string(master), "RESOLUTION") {
		t.Fatalf("%d %s", res.StatusCode, master)
	}
	base := st.StreamUrl[:strings.LastIndex(st.StreamUrl, "/")+1] + "0/"
	if res, media := e.get(base + "index.m3u8"); res.StatusCode != 200 || !strings.Contains(string(media), "#EXTINF") {
		t.Fatalf("%d %s", res.StatusCode, media)
	}
	if res, _ := e.get(base + "init.mp4"); res.StatusCode != 200 {
		t.Fatalf("init %d", res.StatusCode)
	}
	if res, seg := e.get(base + "0.m4s"); res.StatusCode != 200 || len(seg) < 1000 {
		t.Fatalf("segment %d %d", res.StatusCode, len(seg))
	}

	// Away, a lossy song is copied as it is.
	st = e.start(m.songs[1].Id, songSnapshot(false, false))
	if st.Entry.Verdict != api.DirectPlay || *st.Negotiated.AudioCodec != "mp3" || st.Negotiated.Lossless != nil {
		t.Fatalf("lossy away: %+v", st)
	}

	// Playability decides a song the same way, without a session.
	var check api.Playability
	e.expect(200, "POST", "/items/"+itoa(m.songs[0].Id)+"/playability", e.plain.token, songSnapshot(true, false), &check)
	if check.Verdict != api.DirectPlay || check.Negotiated.AudioCodec == nil || *check.Negotiated.AudioCodec != "flac" {
		t.Fatalf("song playability: %+v", check)
	}
}

// The details panel reads the source file and the delivered stream from the
// start response: equal on direct play and remux, different on an audio
// transcode, and absent for video.
func TestSongAudioDetails(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	// The fixtures' sizes and the FLAC's bitrate are as this host's ffmpeg wrote them.
	written := func(name string) (size, bitrate *int64) {
		p := fixturePath(t, filepath.Join("music", "The Example Band", "First Light (2024)", name))
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		r, err := probe.Run(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		return ptr(st.Size()), r.Bitrate
	}
	flacSize, flacBitrate := written("01 Opening.flac")
	mp3Size, _ := written("02 Second Wind.mp3")
	flac := api.AudioSource{Container: ptr("flac"), Codec: ptr("flac"), Bitrate: flacBitrate, SampleRateHz: ptr(44100), Channels: ptr(2),
		SizeBytes: flacSize, Lossless: true}
	mp3 := api.AudioSource{Container: ptr("mp3"), Codec: ptr("mp3"), Bitrate: ptr[int64](128000), SampleRateHz: ptr(44100), Channels: ptr(2),
		SizeBytes: mp3Size, Lossless: false}
	remux := songSnapshot(true, false)
	remux["decoder"].(map[string]any)["containers"] = []string{"mp4"}
	for _, c := range []struct {
		name string
		song int
		snap map[string]any
		want api.AudioDetails
	}{
		{"direct play lossless", 0, songSnapshot(true, false), api.AudioDetails{Source: flac,
			Delivered: api.AudioDelivered{Verdict: api.DirectPlay, Codec: "flac", Bitrate: flacBitrate, Lossless: true}}},
		{"remux", 0, remux, api.AudioDetails{Source: flac,
			Delivered: api.AudioDelivered{Verdict: api.Remux, Codec: "flac", Bitrate: flacBitrate, Lossless: true}}},
		{"transcode", 0, songSnapshot(false, false), api.AudioDetails{Source: flac,
			Delivered: api.AudioDelivered{Verdict: api.AudioTranscode, Codec: "aac", Bitrate: ptr[int64](256000)}}},
		{"metered transcode", 0, songSnapshot(true, true), api.AudioDetails{Source: flac,
			Delivered: api.AudioDelivered{Verdict: api.AudioTranscode, Codec: "aac", Bitrate: ptr[int64](128000)}}},
		{"direct play lossy", 1, songSnapshot(true, false), api.AudioDetails{Source: mp3,
			Delivered: api.AudioDelivered{Verdict: api.DirectPlay, Codec: "mp3", Bitrate: ptr[int64](128000)}}},
		{"lossy transcode", 1, songSnapshot(true, true), api.AudioDetails{Source: mp3,
			Delivered: api.AudioDelivered{Verdict: api.AudioTranscode, Codec: "aac", Bitrate: ptr[int64](128000)}}},
	} {
		st := e.start(m.songs[c.song].Id, c.snap)
		if !reflect.DeepEqual(st.Audio, &c.want) {
			got, _ := json.Marshal(st.Audio)
			want, _ := json.Marshal(c.want)
			t.Errorf("%s: %s, want %s", c.name, got, want)
		}
	}
	if st := e.start(e.probed("h264-high41.mp4", ""), sdrWeb()); st.Audio != nil {
		t.Fatalf("video has no audio details: %+v", st.Audio)
	}
}

// A song's session counts as a play: Recently played lists its album, its
// artists or the playlist it played from, and Popular counts plays across
// profiles.
func TestRecentlyPlayedAndPopular(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	p := "/profiles/" + itoa(e.plain.account.ID) + "/music/recently-played"
	var recent api.MusicRecentPage
	e.expect(200, "GET", p, e.plain.token, nil, &recent)
	if len(recent.Items) != 0 {
		t.Fatalf("%+v", recent)
	}
	mix := &playlist.Playlist{AccountID: e.plain.account.ID, Name: "Mix", Kind: playlist.Music}
	must(t, e.svc.Playlists.Repo().Create(mix, nil))
	must(t, e.svc.Playlists.Repo().AddEntry(&playlist.Entry{PlaylistID: mix.ID, ItemID: &m.songs[2].Id}, nil, nil))
	theirs := &playlist.Playlist{AccountID: e.admin.account.ID, Name: "Theirs", Kind: playlist.Music}
	must(t, e.svc.Playlists.Repo().Create(theirs, nil))

	snap := songSnapshot(true, false)
	e.start(m.songs[1].Id, snap)
	e.start(m.songs[1].Id, snap)
	var loose api.MusicSongPage
	e.expect(200, "GET", "/music/songs?albumId="+itoa(m.loose.Id), e.plain.token, nil, &loose)
	e.start(loose.Items[0].Id, snap)
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m.songs[2].Id,
		"playlistId": mix.ID, "snapshot": snap}, nil)
	e.expect(404, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m.songs[2].Id,
		"playlistId": theirs.ID, "snapshot": snap}, nil)

	e.expect(200, "GET", p, e.plain.token, nil, &recent)
	if len(recent.Items) != 3 || recent.Items[0].Kind != api.MusicRecentKindAlbum && recent.Items[0].Kind != api.MusicRecentKindPlaylist {
		t.Fatalf("mixed %+v", recent)
	}
	kinds := map[api.MusicRecentKind]int{}
	for _, r := range recent.Items {
		kinds[r.Kind]++
		if r.Kind == api.MusicRecentKindPlaylist && (r.Playlist == nil || r.Playlist.ItemCount != 1 || r.Playlist.ArtItemId == nil || r.Playlist.LastPlayedAt == nil) {
			t.Fatalf("playlist %+v", r.Playlist)
		}
		if r.Kind == api.MusicRecentKindAlbum && (r.Album == nil || r.Album.LastPlayedAt == nil) {
			t.Fatalf("album %+v", r.Album)
		}
	}
	if kinds[api.MusicRecentKindAlbum] != 2 || kinds[api.MusicRecentKindPlaylist] != 1 {
		t.Fatalf("kinds %v", kinds)
	}
	e.expect(200, "GET", p+"?kind=artist", e.plain.token, nil, &recent)
	var names []string
	for _, r := range recent.Items {
		names = append(names, r.Artist.Name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Folder Artist", "Guest Singer", "The Example Band"}) {
		t.Fatalf("artists %v", names)
	}
	e.expect(200, "GET", p+"?kind=album&limit=1", e.plain.token, nil, &recent)
	if len(recent.Items) != 1 || recent.NextOffset == nil || recent.Items[0].Album.Id != m.album.Id {
		t.Fatalf("newest album first %+v", recent)
	}

	var songs api.MusicSongPage
	e.expect(200, "GET", "/music/songs?artistId="+itoa(m.band.Id)+"&sort=popular&limit=5", e.plain.token, nil, &songs)
	if len(songs.Items) != 3 || songs.Items[0].Id != m.songs[1].Id || songs.Items[1].Id != m.songs[2].Id {
		t.Fatalf("popular %+v", songs)
	}
	e.expect(404, "GET", "/profiles/"+itoa(e.admin.account.ID)+"/music/recently-played", e.plain.token, nil, nil)
}

// Another adult's Recently played is not found: what a profile played, its
// playlists included, is theirs alone.
func TestRecentlyPlayedKeepsPlaylistsPrivate(t *testing.T) {
	e := newEnv(t)
	joan := e.householdAdult(e.plain, "Joan", "joan@example.test")
	mix := &playlist.Playlist{AccountID: joan.Me.Account.Id, Name: "Joan's mix", Kind: playlist.Music}
	must(t, e.svc.Playlists.Repo().Create(mix, nil))
	must(t, e.svc.Playlists.Repo().MarkPlayed(mix.ID, time.Now().UTC()))
	p := "/profiles/" + itoa(joan.Me.Account.Id) + "/music/recently-played"
	var recent api.MusicRecentPage
	e.expect(200, "GET", p, joan.Token, nil, &recent)
	if len(recent.Items) != 1 || recent.Items[0].Playlist == nil {
		t.Fatalf("her own %+v", recent)
	}
	for _, kind := range []string{"", "?kind=playlist"} {
		e.expect(404, "GET", p+kind, e.plain.token, nil, nil)
	}
}
