package httpapi_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// fakeImages stands in for the provider's image cache: it writes the size
// and path it was asked for, so a reply names the image that was served.
type fakeImages struct{ calls int }

func (f *fakeImages) CacheImage(_ context.Context, dir, size, path string) (string, error) {
	f.calls++
	dest := filepath.Join(dir, size, path)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", err
	}
	return dest, os.WriteFile(dest, []byte(size+path), 0o600)
}

func TestItemArtworkFallsBackToAncestors(t *testing.T) {
	e := newEnv(t)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, t.TempDir()
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "TV", "kind": "tv"}, &lib)
	items := e.svc.Items.Repo()
	add := func(kind string, parent *uid.ID) *item.Item {
		it := &item.Item{LibraryID: lib.Id, Kind: kind, ParentID: parent, ParsedTitle: kind}
		must(t, items.Create(it))
		return it
	}
	show := add(item.Show, nil)
	season := add(item.Season, &show.ID)
	ep := add(item.Episode, &season.ID)
	bare := add(item.Show, nil)
	must(t, items.SetMetadata(&item.Metadata{ItemID: show.ID, Status: item.Matched, PosterPath: "/show.jpg", BackdropPath: "/wide.jpg"}))
	must(t, items.SetMetadata(&item.Metadata{ItemID: ep.ID, Status: item.Matched, PosterPath: "/still.jpg"}))

	get := func(id uid.ID, kind string) string {
		t.Helper()
		status, raw := e.do("GET", "/items/"+itoa(id)+"/artwork/"+kind, e.plain.token, nil, nil)
		if status != 200 {
			t.Fatalf("%s %s: %d %s", id, kind, status, raw)
		}
		return string(raw)
	}
	for _, c := range []struct {
		id         uid.ID
		kind, want string
	}{
		{ep.ID, "poster", "w500/still.jpg"},
		{ep.ID, "backdrop", "w1280/wide.jpg"},
		{season.ID, "poster", "w500/show.jpg"},
		{show.ID, "backdrop", "w1280/wide.jpg"},
	} {
		if got := get(c.id, c.kind); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.id, c.kind, got, c.want)
		}
	}
	if !strings.Contains(string(e.expect(404, "GET", "/items/"+itoa(bare.ID)+"/artwork/poster", e.plain.token, nil, nil)), "not_found") {
		t.Error("an item without artwork answers not_found")
	}
	// A tile the client has keeps it a day, then revalidates without a body.
	req, err := http.NewRequest("GET", e.url+"/items/"+itoa(ep.ID)+"/artwork/poster", nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+e.plain.token)
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	_ = res.Body.Close()
	tag := res.Header.Get("ETag")
	if tag != `"w500/still.jpg"` || res.Header.Get("Cache-Control") != "private, max-age=86400" || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("headers: %v", res.Header)
	}
	req.Header.Set("If-None-Match", tag)
	res, err = http.DefaultClient.Do(req)
	must(t, err)
	_ = res.Body.Close()
	if res.StatusCode != 304 || res.Header.Get("ETag") != tag {
		t.Errorf("revalidation: %d %v", res.StatusCode, res.Header)
	}
	req.Header.Set("If-None-Match", `"w500/old.jpg"`)
	if res, err = http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Errorf("a changed image is sent again: %v %v", res, err)
	}
	_ = res.Body.Close()

	e.expect(400, "GET", "/items/"+itoa(show.ID)+"/artwork/logo", e.plain.token, nil, nil)
	e.expect(401, "GET", "/items/"+itoa(show.ID)+"/artwork/poster", "", nil, nil)
}

// An address answers its image for a year, immutable, to any device, and
// is the image the item's own endpoint answers. A TMDB image no data names
// is never downloaded.
func TestArtworkIsServedAtTheAddressTheDataCarries(t *testing.T) {
	e := newEnv(t)
	data := t.TempDir()
	dbtest.DataDir(t, data)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, filepath.Join(data, "images")
	lib := e.library("movies")
	items := e.svc.Items.Repo()
	movie := &item.Item{LibraryID: lib, Kind: item.Movie, ParsedTitle: "Lamplight"}
	must(t, items.Create(movie))
	must(t, items.SetMetadata(&item.Metadata{ItemID: movie.ID, Status: item.Matched, PosterPath: "/lamp.jpg"}))
	frame := filepath.Join(data, "thumbnails", "local", "0123abcd.jpg")
	must(t, os.MkdirAll(filepath.Dir(frame), 0o750))
	must(t, os.WriteFile(frame, []byte("frame"), 0o600))
	must(t, items.SetLocal(&item.Metadata{ItemID: movie.ID, BackdropPath: frame}))

	var got api.Item
	e.expect(200, "GET", "/items/"+itoa(movie.ID), e.plain.token, nil, &got)
	if got.PosterUrl == nil || *got.PosterUrl != httpapi.BasePath+"/artwork/w500/lamp.jpg" ||
		got.BackdropUrl == nil || *got.BackdropUrl != httpapi.BasePath+"/artwork/local/0123abcd.jpg" {
		t.Fatalf("urls %v %v", got.PosterUrl, got.BackdropUrl)
	}
	for kind, u := range map[string]string{"poster": *got.PosterUrl, "backdrop": *got.BackdropUrl} {
		res, body := e.raw("GET", strings.TrimPrefix(u, httpapi.BasePath), e.plain.token)
		_, old := e.raw("GET", "/items/"+itoa(movie.ID)+"/artwork/"+kind, e.plain.token)
		if res.StatusCode != 200 || string(body) != string(old) || res.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
			t.Errorf("%s: %d %q, the item's endpoint %q, %v", kind, res.StatusCode, body, old, res.Header)
		}
	}

	req, err := http.NewRequest("GET", e.url+"/artwork/local/0123abcd.jpg", nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+e.admin.token)
	req.Header.Set("If-None-Match", `"local/0123abcd.jpg"`)
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	_ = res.Body.Close()
	if res.StatusCode != 304 || res.Header.Get("ETag") != `"local/0123abcd.jpg"` || res.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" {
		t.Errorf("revalidation: %d %v", res.StatusCode, res.Header)
	}

	calls := images.calls
	e.expect(404, "GET", "/artwork/w500/nobody.jpg", e.plain.token, nil, nil)
	if images.calls != calls {
		t.Error("a TMDB image no data names was downloaded")
	}
	e.expect(404, "GET", "/artwork/local/ffffffff.jpg", e.plain.token, nil, nil)
	e.expect(400, "GET", "/artwork/other/x.jpg", e.plain.token, nil, nil)
	e.expect(400, "GET", "/artwork/local/x.gif", e.plain.token, nil, nil)
	e.expect(401, "GET", "/artwork/local/0123abcd.jpg", "", nil, nil)
}

// Every object that shows an image carries its URL, which serves what its
// deprecated id endpoint does.
func TestEachObjectCarriesItsArtURL(t *testing.T) {
	e := newEnv(t)
	data := t.TempDir()
	dbtest.DataDir(t, data)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, filepath.Join(data, "images")
	e.svc.People.Images, e.svc.People.ImageDir = images, e.svc.Items.ImageDir
	items, tok := e.svc.Items.Repo(), e.plain.token
	stored := func(rel string) string {
		p := filepath.Join(data, rel)
		must(t, os.MkdirAll(filepath.Dir(p), 0o750))
		must(t, os.WriteFile(p, []byte(rel), 0o600))
		return p
	}
	same := func(what string, url *string, old string) {
		t.Helper()
		if url == nil {
			t.Errorf("%s: no URL", what)
			return
		}
		res, body := e.raw("GET", strings.TrimPrefix(*url, httpapi.BasePath), tok)
		_, want := e.raw("GET", old, tok)
		if res.StatusCode != 200 || string(body) != string(want) {
			t.Errorf("%s: %s %d %q, the id endpoint %q", what, *url, res.StatusCode, body, want)
		}
	}

	song := e.song(e.library("music"), "Opening")
	s, err := items.Get(song)
	must(t, err)
	must(t, items.SetLocal(&item.Metadata{ItemID: *s.ParentID, PosterPath: stored("thumbnails/music/beef.jpg")}))
	al, err := items.Get(*s.ParentID)
	must(t, err)
	var gotSong api.MusicSong
	var album api.MusicAlbum
	var artist api.MusicArtist
	e.expect(200, "GET", "/music/songs/"+itoa(song), tok, nil, &gotSong)
	e.expect(200, "GET", "/music/albums/"+itoa(al.ID), tok, nil, &album)
	e.expect(200, "GET", "/music/artists/"+itoa(*al.ParentID), tok, nil, &artist)
	same("song", gotSong.PosterUrl, "/items/"+itoa(song)+"/artwork/poster")
	same("album", album.PosterUrl, "/items/"+itoa(al.ID)+"/artwork/poster")
	same("artist", artist.PosterUrl, "/items/"+itoa(*al.ParentID)+"/artwork/poster")

	tb := e.youtubeLibrary()
	must(t, items.SetLocal(&item.Metadata{ItemID: tb.channel, Status: item.Matched, PosterPath: stored("cache/youtube/UCabc.jpg")}))
	var video api.YouTubeVideo
	var channel api.YouTubeChannel
	e.expect(200, "GET", "/youtube/videos/"+itoa(tb.newer), tok, nil, &video)
	e.expect(200, "GET", "/youtube/channels/"+itoa(tb.channel), tok, nil, &channel)
	same("video", video.PosterUrl, "/items/"+itoa(tb.newer)+"/artwork/poster")
	same("video's channel", video.ChannelPosterUrl, "/items/"+itoa(tb.channel)+"/artwork/poster")
	same("channel", channel.PosterUrl, "/items/"+itoa(tb.channel)+"/artwork/poster")

	films := e.library("movies")
	m := e.film(films, "Paper Kite", "2001-01-01", "PG")
	must(t, items.SetMetadata(&item.Metadata{ItemID: m, Status: item.Matched, PosterPath: "/kite.jpg", BackdropPath: "/kite-wide.jpg"}))
	var c api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Kites"}, &c)
	e.expect(204, "PUT", "/collections/"+itoa(c.Id)+"/movies/"+itoa(m), tok, nil, nil)
	e.expect(200, "GET", "/collections/"+itoa(c.Id), tok, nil, &c)
	same("collection poster", c.PosterUrl, "/collections/"+itoa(c.Id)+"/artwork/poster")
	same("collection backdrop", c.BackdropUrl, "/collections/"+itoa(c.Id)+"/artwork/backdrop")

	var p api.Person
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": "Ann Lee", "tmdbId": 1, "profilePath": "/ann.jpg"}, &p)
	same("person", p.HeadshotUrl, "/people/"+itoa(p.Id)+"/artwork")
	credits := map[string]any{"cast": []map[string]any{{"personId": p.Id, "character": "Hero"}}, "crew": []any{}}
	var cr api.ItemCredits
	e.expect(200, "PUT", "/items/"+itoa(m)+"/credits", e.admin.token, credits, &cr)
	same("credit", cr.Cast[0].HeadshotUrl, "/people/"+itoa(p.Id)+"/artwork")

	base := "/profiles/" + itoa(e.plain.account.ID) + "/playlists"
	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Weekend", "kind": "movies"}, &pl)
	e.expect(201, "POST", base+"/"+itoa(pl.Id)+"/entries", tok, map[string]any{"itemId": m}, nil)
	e.expect(200, "GET", base+"/"+itoa(pl.Id), tok, nil, &pl)
	same("playlist", pl.PosterUrl, "/items/"+itoa(m)+"/artwork/poster")
}
