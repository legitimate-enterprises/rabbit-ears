package httpapi_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

// Every music list carries the count of all rows it walks: the same on the
// first and a later page, the filtered count under a filter, and for a kid
// only what the kid hears.
func TestMusicTotals(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	var all api.MusicSongPage
	e.expect(200, "GET", "/music/songs", e.plain.token, nil, &all)
	n := len(all.Items)
	var first, later api.MusicSongPage
	e.expect(200, "GET", "/music/songs?limit=2", e.plain.token, nil, &first)
	e.expect(200, "GET", "/music/songs?limit=2&offset=2", e.plain.token, nil, &later)
	if n <= 2 || first.Total != n || later.Total != n || all.Total != n {
		t.Fatalf("songs %d: %d %d %d", n, first.Total, later.Total, all.Total)
	}
	var byAlbum api.MusicSongPage
	e.expect(200, "GET", "/music/songs?limit=1&albumId="+itoa(m.album.Id), e.plain.token, nil, &byAlbum)
	if byAlbum.Total != 3 {
		t.Fatalf("filtered songs %d", byAlbum.Total)
	}
	var artists api.MusicArtistPage
	e.expect(200, "GET", "/music/artists?limit=1&offset=1", e.plain.token, nil, &artists)
	var found api.MusicArtistPage
	e.expect(200, "GET", "/music/artists?letter=G", e.plain.token, nil, &found)
	if artists.Total != 3 || found.Total != 1 {
		t.Fatalf("artists %d, letter %d", artists.Total, found.Total)
	}
	var albums api.MusicAlbumPage
	e.expect(200, "GET", "/music/albums?limit=1", e.plain.token, nil, &albums)
	var hit api.MusicAlbumPage
	e.expect(200, "GET", "/music/albums?limit=1&q=fold", e.plain.token, nil, &hit)
	if albums.Total != 2 || hit.Total != 1 {
		t.Fatalf("albums %d, query %d", albums.Total, hit.Total)
	}

	yes := true
	for _, id := range []int64{m.songs[1].Id, all.Items[0].Id} {
		s, err := e.svc.Items.Repo().Get(id)
		must(t, err)
		s.Explicit = &yes
		must(t, e.svc.Items.Repo().Update(s))
	}
	tok := e.kidDevice(e.plain, e.kid(e.plain, "Kid").Id)
	var kidSongs api.MusicSongPage
	e.expect(200, "GET", "/music/songs?limit=1", tok, nil, &kidSongs)
	var kidAll api.MusicSongPage
	e.expect(200, "GET", "/music/songs", tok, nil, &kidAll)
	if kidSongs.Total != len(kidAll.Items) || kidSongs.Total != n-2 {
		t.Fatalf("a kid's songs %d of %d", kidSongs.Total, n)
	}
	var kidArtists, kidArtistsAll api.MusicArtistPage
	e.expect(200, "GET", "/music/artists?limit=1", tok, nil, &kidArtists)
	e.expect(200, "GET", "/music/artists", tok, nil, &kidArtistsAll)
	var kidAlbums, kidAlbumsAll api.MusicAlbumPage
	e.expect(200, "GET", "/music/albums?limit=1", tok, nil, &kidAlbums)
	e.expect(200, "GET", "/music/albums", tok, nil, &kidAlbumsAll)
	if kidArtists.Total != len(kidArtistsAll.Items) || kidArtists.Total >= 3 || kidAlbums.Total != len(kidAlbumsAll.Items) {
		t.Fatalf("a kid's artists %d albums %d", kidArtists.Total, kidAlbums.Total)
	}
}

func TestPlaylistTotals(t *testing.T) {
	e := newEnv(t)
	base := "/profiles/" + itoa(e.plain.account.ID) + "/playlists"
	for _, in := range []map[string]any{{"name": "A", "kind": "music"}, {"name": "B", "kind": "movies"}, {"name": "C", "kind": "movies"}} {
		e.expect(201, "POST", base, e.plain.token, in, nil)
	}
	var first, later, movies api.PlaylistPage
	e.expect(200, "GET", base+"?limit=2", e.plain.token, nil, &first)
	e.expect(200, "GET", base+"?limit=2&offset=2", e.plain.token, nil, &later)
	e.expect(200, "GET", base+"?limit=1&kind=movies", e.plain.token, nil, &movies)
	if first.Total != 3 || later.Total != 3 || len(later.Items) != 1 || movies.Total != 2 {
		t.Fatalf("%d %d %d", first.Total, later.Total, movies.Total)
	}
}

func TestCollectionTotals(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	g, r := e.film(films, "Meadow", "2001-01-01", "G"), e.film(films, "Undertow", "2001-01-01", "R")
	for name, movie := range map[string]int64{"A": g, "B": r, "C": g} {
		var c api.Collection
		e.expect(201, "POST", "/collections", e.plain.token, map[string]any{"name": name}, &c)
		e.expect(204, "PUT", "/collections/"+itoa(c.Id)+"/movies/"+itoa(movie), e.plain.token, nil, nil)
	}
	var first, later api.CollectionPage
	e.expect(200, "GET", "/collections?limit=2", e.plain.token, nil, &first)
	e.expect(200, "GET", "/collections?limit=2&offset=2", e.plain.token, nil, &later)
	if first.Total != 3 || later.Total != 3 || len(later.Items) != 1 {
		t.Fatalf("%d %d", first.Total, later.Total)
	}
	var kid api.CollectionPage
	e.expect(200, "GET", "/collections?limit=1", e.kidDevice(e.plain, e.kid(e.plain, "Kid").Id), nil, &kid)
	if kid.Total != 2 {
		t.Fatalf("a kid counts only collections holding what they may see: %d", kid.Total)
	}
}
