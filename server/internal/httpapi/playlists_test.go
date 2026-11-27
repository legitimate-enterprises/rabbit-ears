package httpapi_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// song adds an artist, album and song to a music library, without a file.
func (e *env) song(lib uid.ID, title string) uid.ID {
	e.t.Helper()
	items := e.svc.Items.Repo()
	artist := &item.Item{LibraryID: lib, Kind: item.Artist, ParsedTitle: "Band " + title}
	must(e.t, items.Create(artist))
	album := &item.Item{LibraryID: lib, ParentID: &artist.ID, Kind: item.Album, ParsedTitle: "Album " + title}
	must(e.t, items.Create(album))
	song := &item.Item{LibraryID: lib, ParentID: &album.ID, Kind: item.Song, ParsedTitle: title}
	must(e.t, items.Create(song))
	return song.ID
}

func (e *env) file(itemID uid.ID, durationMs int64) {
	e.t.Helper()
	e.expect(201, "POST", "/media-files", e.admin.token, map[string]any{"itemId": itemID, "path": e.onDisk("/media/" + itoa(itemID) + ".mp4"),
		"sizeBytes": 1, "fileModifiedAt": "2026-01-01T00:00:00Z", "durationMs": durationMs}, nil)
}

func entryIDs(page api.PlaylistEntryPage) (ids []uid.ID) {
	for _, en := range page.Items {
		ids = append(ids, *en.ItemId)
	}
	return ids
}

func playIDs(page api.PlaylistPlayOrderPage) (items, entries []uid.ID) {
	for _, r := range page.Items {
		items, entries = append(items, r.ItemId), append(entries, r.EntryId)
	}
	return items, entries
}

func TestPlaylistsMoviesLifecycle(t *testing.T) {
	e := newEnv(t)
	tok, base := e.plain.token, "/profiles/"+itoa(e.plain.account.ID)+"/playlists"
	films := e.library("movies")
	movie, other := e.movie(films, "Heat"), e.movie(films, "Ronin")
	e.file(other, 612000)
	_, ep := e.show(e.library("tv"), "Show", "TV-PG")
	tb := e.youtubeLibrary()
	songs := e.song(e.library("music"), "Opening")

	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Weekend", "kind": "movies"}, &pl)
	if pl.Name != "Weekend" || pl.Kind != api.PlaylistKindMovies || pl.ProfileId != e.plain.account.ID || pl.ItemCount != 0 || pl.ArtItemId != nil {
		t.Fatalf("%+v", pl)
	}
	e.expect(400, "POST", base, tok, map[string]any{"name": "   ", "kind": "movies"}, nil)
	e.expect(400, "POST", base, tok, map[string]any{"name": "Old", "kind": "video"}, nil)
	one := base + "/" + itoa(pl.Id)

	var en api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": movie}, &en)
	if en.Position != 0 || *en.ItemId != movie || en.Kind != api.PlaylistEntryKindMovie || en.ItemCount != 1 || en.DurationMs != 0 ||
		en.Item == nil || en.Item.ParsedTitle != "Heat" || en.Show != nil || en.Song != nil || en.Album != nil || en.Artist != nil {
		t.Fatalf("a movie fills item only: %+v", en)
	}
	var vid api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": other}, &vid)
	if vid.Position != 1 || vid.DurationMs != 612000 || vid.Item == nil || vid.Item.ParsedTitle != "Ronin" {
		t.Fatalf("%+v", vid)
	}
	var again api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": movie, "position": 0}, &again)
	if again.Position != 0 || again.Id == en.Id {
		t.Fatalf("duplicates are allowed and position inserts: %+v", again)
	}

	var page api.PlaylistEntryPage
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	if !slices.Equal(entryIDs(page), []uid.ID{movie, movie, other}) || page.Items[1].Id != en.Id || page.Items[2].Position != 2 {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "GET", one+"/entries?limit=1&offset=1", tok, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != en.Id || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("paging %+v", page)
	}
	var order api.PlaylistPlayOrderPage
	e.expect(200, "GET", one+"/play-order", tok, nil, &order)
	if items, entries := playIDs(order); !slices.Equal(items, []uid.ID{movie, movie, other}) || !slices.Equal(entries, []uid.ID{again.Id, en.Id, vid.Id}) ||
		order.Items[0].Kind != api.PlaylistEntryKindMovie || order.Items[0].Item == nil || order.Items[0].Show != nil ||
		order.Items[2].DurationMs == nil || *order.Items[2].DurationMs != 612000 {
		t.Fatalf("play order %+v", order)
	}
	e.expect(200, "GET", one, tok, nil, &pl)
	if pl.ItemCount != 3 || pl.DurationMs != 612000 || pl.ArtItemId == nil || *pl.ArtItemId != movie {
		t.Fatalf("%+v", pl)
	}

	// Only movies fit: no episode, show, song, YouTube channel or video.
	for _, id := range []uid.ID{ep, songs, tb.channel, tb.newer} {
		e.expect(400, "POST", one+"/entries", tok, map[string]any{"itemId": id}, nil)
	}
	e.expect(404, "POST", one+"/entries", tok, map[string]any{"itemId": uid.N(999999)}, nil)

	// Reorder: an absent position moves the entry last.
	e.expect(200, "PUT", one+"/entries/"+itoa(again.Id), tok, map[string]any{"itemId": movie}, &again)
	if again.Position != 2 {
		t.Fatalf("%+v", again)
	}
	e.expect(200, "PUT", one+"/entries/"+itoa(vid.Id), tok, map[string]any{"itemId": other, "position": 0}, &vid)
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	if !slices.Equal(entryIDs(page), []uid.ID{other, movie, movie}) || page.Items[1].Id != en.Id || page.Items[2].Id != again.Id {
		t.Fatalf("%+v", page)
	}
	e.expect(400, "PUT", one+"/entries/"+itoa(vid.Id), tok, map[string]any{"itemId": movie, "position": 1}, nil)
	e.expect(200, "GET", one+"/entries/"+itoa(en.Id), tok, nil, &en)
	if en.Position != 1 {
		t.Fatalf("%+v", en)
	}

	e.expect(204, "DELETE", one+"/entries/"+itoa(vid.Id), tok, nil, nil)
	e.expect(404, "GET", one+"/entries/"+itoa(vid.Id), tok, nil, nil)
	e.expect(200, "GET", one+"/entries/"+itoa(en.Id), tok, nil, &en)
	if en.Position != 0 {
		t.Fatalf("later entries move up: %+v", en)
	}

	var spare api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Other", "kind": "movies"}, &spare)
	e.expect(404, "GET", base+"/"+itoa(spare.Id)+"/entries/"+itoa(en.Id), tok, nil, nil)

	// Rename freely; the kind is fixed once there are entries.
	e.expect(200, "PUT", one, tok, map[string]any{"name": "Friday", "kind": "movies"}, &pl)
	if pl.Name != "Friday" || pl.ItemCount != 2 {
		t.Fatalf("%+v", pl)
	}
	e.expect(409, "PUT", one, tok, map[string]any{"name": "Friday", "kind": "tv"}, nil)
	e.expect(200, "PUT", base+"/"+itoa(spare.Id), tok, map[string]any{"name": "Other", "kind": "music"}, &spare)
	if spare.Kind != api.PlaylistKindMusic {
		t.Fatalf("%+v", spare)
	}

	var list api.PlaylistPage
	e.expect(200, "GET", base, tok, nil, &list)
	if len(list.Items) != 2 || list.Items[0].Name != "Friday" || list.Items[1].Name != "Other" {
		t.Fatalf("%+v", list)
	}
	e.expect(200, "GET", base+"?kind=music", tok, nil, &list)
	if len(list.Items) != 1 || list.Items[0].Id != spare.Id {
		t.Fatalf("%+v", list)
	}
	e.expect(400, "GET", base+"?kind=video", tok, nil, nil)

	e.expect(204, "DELETE", one, tok, nil, nil)
	e.expect(404, "GET", one, tok, nil, nil)
	e.expect(404, "GET", one+"/entries", tok, nil, nil)
	e.expect(404, "GET", one+"/play-order", tok, nil, nil)
	if _, err := e.svc.Playlists.Repo().Entry(en.Id); err == nil {
		t.Fatal("entries outlived the playlist")
	}
}

// A tv playlist takes shows, seasons and episodes; the server expands the
// containers into the play order, and playing a row from it stamps the
// playlist.
func TestPlaylistsTV(t *testing.T) {
	e := newEnv(t)
	tok, base := e.plain.token, "/profiles/"+itoa(e.plain.account.ID)+"/playlists"
	lib := e.library("tv")
	show, s1e1 := e.show(lib, "Show", "TV-PG")
	var s1 api.Item
	e.expect(200, "GET", "/items/"+itoa(s1e1), e.admin.token, nil, &s1)
	season := *s1.ParentId
	s1e2 := e.child(lib, season, "episode", 2)
	s2 := e.child(lib, show, "season", 2)
	s2e1 := e.child(lib, s2, "episode", 1)
	e.file(s1e1, 1000)
	e.file(s1e2, 2000)
	movie := e.movie(e.library("movies"), "Heat")

	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Binge", "kind": "tv"}, &pl)
	one := base + "/" + itoa(pl.Id)
	var eShow, eSeason, eEp api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": show}, &eShow)
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": season}, &eSeason)
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": s1e2}, &eEp)
	e.expect(400, "POST", one+"/entries", tok, map[string]any{"itemId": movie}, nil)

	if eShow.Kind != api.PlaylistEntryKindShow || eShow.ItemCount != 3 || eShow.DurationMs != 3000 || eShow.Item == nil || eShow.Item.Id != show || eShow.Show != nil {
		t.Fatalf("show entry %+v", eShow)
	}
	if eSeason.Kind != api.PlaylistEntryKindSeason || eSeason.ItemCount != 2 || eSeason.Item == nil || eSeason.Show == nil || eSeason.Show.Id != show {
		t.Fatalf("season entry %+v", eSeason)
	}
	if eEp.Kind != api.PlaylistEntryKindEpisode || eEp.ItemCount != 1 || eEp.DurationMs != 2000 || eEp.Show == nil || eEp.Show.ParsedTitle != "Show" {
		t.Fatalf("episode entry %+v", eEp)
	}

	var order api.PlaylistPlayOrderPage
	e.expect(200, "GET", one+"/play-order", tok, nil, &order)
	items, entries := playIDs(order)
	if !slices.Equal(items, []uid.ID{s1e1, s1e2, s2e1, s1e1, s1e2, s1e2}) ||
		!slices.Equal(entries, []uid.ID{eShow.Id, eShow.Id, eShow.Id, eSeason.Id, eSeason.Id, eEp.Id}) {
		t.Fatalf("play order %v from %v", items, entries)
	}
	if r := order.Items[2]; r.Kind != api.PlaylistEntryKindEpisode || r.Item == nil || r.Item.Id != s2e1 || r.Show == nil || r.Show.Id != show || r.Song != nil || r.DurationMs != nil {
		t.Fatalf("an episode row %+v", r)
	}
	e.expect(200, "GET", one+"/play-order?limit=2&offset=2", tok, nil, &order)
	if items, _ := playIDs(order); !slices.Equal(items, []uid.ID{s2e1, s1e1}) || order.NextOffset == nil || *order.NextOffset != 4 {
		t.Fatalf("paged %v %v", items, order.NextOffset)
	}
	e.expect(200, "GET", one, tok, nil, &pl)
	if pl.ItemCount != 6 || pl.DurationMs != 8000 || pl.ArtItemId == nil || *pl.ArtItemId != show {
		t.Fatalf("summary counts the play order %+v", pl)
	}

	e.expect(201, "POST", "/playback/sessions", tok, map[string]any{"profileId": e.plain.account.ID, "itemId": s1e2,
		"playlistId": pl.Id, "snapshot": snapshot()}, nil)
	e.expect(200, "GET", one, tok, nil, &pl)
	if pl.LastPlayedAt == nil {
		t.Fatalf("not stamped %+v", pl)
	}
}

// A music playlist takes artists, albums and songs; a movie, episode or
// YouTube video does not fit.
func TestPlaylistsMusic(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	tok, base := e.plain.token, "/profiles/"+itoa(e.plain.account.ID)+"/playlists"
	movie := e.movie(e.library("movies"), "Heat")
	tb := e.youtubeLibrary()

	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Road trip", "kind": "music"}, &pl)
	one := base + "/" + itoa(pl.Id)
	var eAlbum, eSong, eArtist api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": m.album.Id}, &eAlbum)
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": m.songs[0].Id}, &eSong)
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": m.band.Id}, &eArtist)
	for _, id := range []uid.ID{movie, tb.newer, tb.channel} {
		e.expect(400, "POST", one+"/entries", tok, map[string]any{"itemId": id}, nil)
	}
	if eAlbum.Kind != api.PlaylistEntryKindAlbum || eAlbum.Album == nil || eAlbum.Album.Title != m.album.Title || eAlbum.ItemCount != m.album.SongCount ||
		eAlbum.DurationMs != m.album.DurationMs || eAlbum.Item != nil || eAlbum.Song != nil {
		t.Fatalf("album entry %+v", eAlbum)
	}
	if eSong.Kind != api.PlaylistEntryKindSong || eSong.Song == nil || eSong.Song.Title != m.songs[0].Title || eSong.ItemCount != 1 || eSong.Album != nil {
		t.Fatalf("song entry %+v", eSong)
	}
	if eArtist.Kind != api.PlaylistEntryKindArtist || eArtist.Artist == nil || eArtist.Artist.Name != m.band.Name || eArtist.ItemCount != m.band.SongCount {
		t.Fatalf("artist entry %+v", eArtist)
	}

	var order api.PlaylistPlayOrderPage
	e.expect(200, "GET", one+"/play-order", tok, nil, &order)
	n := len(m.songs)
	if len(order.Items) != n+1+m.band.SongCount {
		t.Fatalf("rows %d", len(order.Items))
	}
	for i, s := range m.songs {
		if r := order.Items[i]; r.ItemId != s.Id || r.EntryId != eAlbum.Id || r.Kind != api.PlaylistEntryKindSong || r.Song == nil || r.Song.Title != s.Title || r.Item != nil {
			t.Fatalf("row %d %+v, want %s", i, r, s.Title)
		}
	}
	if r := order.Items[n]; r.ItemId != m.songs[0].Id || r.EntryId != eSong.Id || r.DurationMs == nil || *r.DurationMs != *m.songs[0].DurationMs {
		t.Fatalf("song row %+v", r)
	}
	for _, r := range order.Items[n+1:] {
		if r.EntryId != eArtist.Id || r.Song == nil || r.Song.AlbumArtistId != m.band.Id && r.Song.ArtistId != m.band.Id {
			t.Fatalf("artist row %+v", r)
		}
	}

	var got api.Playlist
	e.expect(200, "GET", one, tok, nil, &got)
	if got.ItemCount != len(order.Items) || got.ArtItemId == nil || *got.ArtItemId != m.album.Id {
		t.Fatalf("%+v", got)
	}
	e.expect(201, "POST", "/playback/sessions", tok, map[string]any{"profileId": e.plain.account.ID, "itemId": order.Items[n].ItemId,
		"playlistId": pl.Id, "snapshot": songSnapshot(true, false)}, nil)
	var recent api.MusicRecentPage
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/music/recently-played?kind=playlist", tok, nil, &recent)
	if len(recent.Items) != 1 || recent.Items[0].Playlist == nil || recent.Items[0].Playlist.Id != pl.Id {
		t.Fatalf("recently played %+v", recent)
	}
}

// Recently played lists music playlists only, never a movies or tv one.
func TestRecentlyPlayedSkipsVideoPlaylists(t *testing.T) {
	e := newEnv(t)
	repo := e.svc.Playlists.Repo()
	for _, kind := range []string{playlist.Movies, playlist.TV} {
		p := &playlist.Playlist{AccountID: e.plain.account.ID, Name: kind, Kind: kind}
		must(t, repo.Create(p, nil))
		must(t, repo.MarkPlayed(p.ID, p.CreatedAt))
	}
	var recent api.MusicRecentPage
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/music/recently-played", e.plain.token, nil, &recent)
	if len(recent.Items) != 0 {
		t.Fatalf("%+v", recent)
	}
}

// Playing a movie from a movies playlist stamps it as an episode or song does.
func TestStartPlaybackFromMoviesPlaylist(t *testing.T) {
	e := newEnv(t)
	m, _ := e.playable()
	p := &playlist.Playlist{AccountID: e.plain.account.ID, Name: "Films", Kind: playlist.Movies}
	must(t, e.svc.Playlists.Repo().Create(p, nil))
	must(t, e.svc.Playlists.Repo().AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: &m}, nil, nil))
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": m,
		"playlistId": p.ID, "snapshot": snapshot()}, nil)
	got, err := e.svc.Playlists.Repo().Get(p.ID)
	if err != nil || got.LastPlayedAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
}

// A playlist belongs to one adult; kids have none and cannot make any.
func TestPlaylistsAreOwnedAndNotForKids(t *testing.T) {
	e := newEnv(t)
	repo := e.svc.Playlists.Repo()
	mine := &playlist.Playlist{AccountID: e.plain.account.ID, Name: "Mine", Kind: playlist.Movies}
	theirs := &playlist.Playlist{AccountID: e.admin.account.ID, Name: "Theirs", Kind: playlist.Movies}
	must(t, repo.Create(mine, nil))
	must(t, repo.Create(theirs, nil))
	tok := e.plain.token
	path := func(account, id uid.ID) string { return "/profiles/" + itoa(account) + "/playlists/" + itoa(id) }

	e.expect(404, "GET", "/profiles/"+itoa(e.admin.account.ID)+"/playlists", tok, nil, nil)
	e.expect(404, "GET", path(e.plain.account.ID, theirs.ID), tok, nil, nil)
	e.expect(404, "GET", path(e.plain.account.ID, theirs.ID)+"/play-order", tok, nil, nil)
	e.expect(404, "DELETE", path(e.plain.account.ID, theirs.ID), tok, nil, nil)
	e.expect(404, "POST", path(e.plain.account.ID, theirs.ID)+"/entries", tok, map[string]any{"itemId": uid.N(1)}, nil)
	e.expect(404, "GET", "/profiles/"+itoa(e.admin.account.ID)+"/playlists/"+itoa(theirs.ID), tok, nil, nil)
	e.expect(401, "GET", path(e.plain.account.ID, mine.ID), "", nil, nil)

	k := e.kid(e.plain, "Kid")
	var list api.PlaylistPage
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/playlists", tok, nil, &list)
	if list.Items == nil || len(list.Items) != 0 {
		t.Fatalf("a kid's page is empty: %+v", list)
	}
	e.expect(403, "POST", "/profiles/"+itoa(k.Id)+"/playlists", tok, map[string]any{"name": "X", "kind": "movies"}, nil)
	e.expect(404, "GET", path(k.Id, mine.ID), tok, nil, nil)
	e.expect(404, "GET", path(k.Id, mine.ID)+"/play-order", tok, nil, nil)

	if code, raw := e.unlock(e.plain, k.Id, ""); code != 204 {
		t.Fatalf("%d %s", code, raw)
	}
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/playlists", tok, nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("%+v", list)
	}
	e.expect(403, "POST", "/profiles/"+itoa(k.Id)+"/playlists", tok, map[string]any{"name": "X", "kind": "movies"}, nil)
	e.expect(403, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", tok, nil, nil)
	e.expect(403, "GET", path(e.plain.account.ID, mine.ID)+"/play-order", tok, nil, nil)
	e.expect(403, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", tok, map[string]any{"name": "X", "kind": "movies"}, nil)
	e.expect(403, "DELETE", path(e.plain.account.ID, mine.ID), tok, nil, nil)
	e.expect(403, "POST", path(e.plain.account.ID, mine.ID)+"/entries", tok, map[string]any{"itemId": uid.N(1)}, nil)
	if _, err := repo.Get(mine.ID); err != nil {
		t.Fatal("a kid deleted an adult's playlist")
	}
}

// An entry carries its rank; a client that ranks an entry itself places it
// there, adding or moving, and a key that is not one answers 400.
func TestPlaylistEntryRanks(t *testing.T) {
	e := newEnv(t)
	tok := e.plain.token
	films := e.library("movies")
	a, b, c := e.movie(films, "A"), e.movie(films, "B"), e.movie(films, "C")
	var pl api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", tok, map[string]any{"name": "P", "kind": "movies"}, &pl)
	one := "/profiles/" + itoa(e.plain.account.ID) + "/playlists/" + itoa(pl.Id)
	e.expect(204, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": a}, {"itemId": b}}}, nil)
	var en api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": c, "rank": "a0V"}, &en)
	if en.Position != 1 || en.Rank == nil || *en.Rank != "a0V" {
		t.Fatalf("%+v", en)
	}
	e.expect(200, "PUT", one+"/entries/"+itoa(en.Id), tok, map[string]any{"itemId": c, "rank": "Zz"}, &en)
	if en.Position != 0 || *en.Rank != "Zz" {
		t.Fatalf("%+v", en)
	}
	var page api.PlaylistEntryPage
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	var ranks []string
	for _, x := range page.Items {
		ranks = append(ranks, *x.Rank)
	}
	if !slices.Equal(entryIDs(page), []uid.ID{c, a, b}) || !slices.Equal(ranks, []string{"Zz", "a0", "a1"}) {
		t.Fatalf("%v %v", entryIDs(page), ranks)
	}
	for _, body := range []map[string]any{
		{"itemId": c, "rank": "a0V", "position": 1},
		{"itemId": c, "rank": "a!"},
		{"itemId": c, "rank": "a00"},
		{"itemId": c, "rank": "b0"},
	} {
		e.expect(400, "POST", one+"/entries", tok, body, nil)
		e.expect(400, "PUT", one+"/entries/"+itoa(en.Id), tok, body, nil)
	}
}

// A playlist and its entries take the id a client chose, else get one; an
// id that is not a UUIDv7 in canonical form answers 400, and one taken 409.
func TestPlaylistsTakeClientIDs(t *testing.T) {
	e := newEnv(t)
	tok, me := e.plain.token, e.plain.account.ID
	base := "/profiles/" + itoa(me) + "/playlists"
	films := e.library("movies")
	m, n := e.movie(films, "Heat"), e.movie(films, "Ronin")
	bad := []any{"12", 12, strings.ToUpper(uid.New().String()), "00000000-0000-4000-8000-000000000001"}

	var minted api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Minted", "kind": "movies"}, &minted)
	if minted.Id.IsZero() || minted.CreatedBy == nil || *minted.CreatedBy != me || minted.ModifiedBy == nil || *minted.ModifiedBy != me {
		t.Fatalf("%+v", minted)
	}
	id := uid.New()
	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"id": id, "name": "Chosen", "kind": "movies"}, &pl)
	if pl.Id != id {
		t.Fatalf("%+v", pl)
	}
	one := base + "/" + itoa(id)
	e.expect(200, "GET", one, tok, nil, nil)
	e.expect(409, "POST", base, tok, map[string]any{"id": id, "name": "Again", "kind": "movies"}, nil)
	for _, v := range bad {
		e.expect(400, "POST", base, tok, map[string]any{"id": v, "name": "Bad", "kind": "movies"}, nil)
	}
	e.expect(400, "PUT", one, tok, map[string]any{"id": minted.Id, "name": "Renamed", "kind": "movies"}, nil)
	e.expect(200, "PUT", one, tok, map[string]any{"id": id, "name": "Renamed", "kind": "movies"}, &pl)
	if pl.Name != "Renamed" || pl.ModifiedAt.Before(pl.CreatedAt) {
		t.Fatalf("%+v", pl)
	}

	var en, auto api.PlaylistEntry
	eid := uid.New()
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"id": eid, "itemId": m}, &en)
	if en.Id != eid || en.CreatedBy == nil || *en.CreatedBy != me {
		t.Fatalf("%+v", en)
	}
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": n}, &auto)
	if auto.Id.IsZero() || auto.Id == eid {
		t.Fatalf("%+v", auto)
	}
	e.expect(409, "POST", one+"/entries", tok, map[string]any{"id": eid, "itemId": n}, nil)
	for _, v := range bad {
		e.expect(400, "POST", one+"/entries", tok, map[string]any{"id": v, "itemId": n}, nil)
		e.expect(400, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"id": v, "itemId": n}}}, nil)
	}
	e.expect(400, "PUT", one+"/entries/"+itoa(eid), tok, map[string]any{"id": auto.Id, "itemId": m}, nil)
	e.expect(200, "PUT", one+"/entries/"+itoa(eid), tok, map[string]any{"id": eid, "itemId": m, "position": 1}, &en)
	if en.Position != 1 {
		t.Fatalf("%+v", en)
	}

	b1, b2 := uid.New(), uid.New()
	e.expect(204, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"id": b1, "itemId": m}, {"id": b2, "itemId": n}, {"itemId": m}}}, nil)
	e.expect(200, "GET", one+"/entries/"+itoa(b1), tok, nil, nil)
	e.expect(200, "GET", one+"/entries/"+itoa(b2), tok, nil, nil)
	e.expect(409, "POST", one+"/entries/batch", tok, map[string]any{"entries": []map[string]any{{"itemId": m}, {"id": b1, "itemId": n}}}, nil)
	var page api.PlaylistEntryPage
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	if len(page.Items) != 5 {
		t.Fatalf("a batch with a taken id adds nothing: %d entries", len(page.Items))
	}
}
