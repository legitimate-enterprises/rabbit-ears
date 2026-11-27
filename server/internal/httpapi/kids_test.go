package httpapi_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func itemOf(t *testing.T, e *env, id uid.ID) api.Item {
	t.Helper()
	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(id), e.plain.token, nil, &it)
	return it
}

func kidsContent(it api.Item) (bool, *bool) {
	return it.KidsContent != nil && *it.KidsContent, it.KidsContentOverride
}

// A movie rated G and a show rated TV-Y, TV-Y7 or TV-G are kids' content
// unless an adult says otherwise; clearing the override hands it back to
// the rating.
func TestKidsContentFollowsTheRatingUnlessSet(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	for id, want := range map[uid.ID]bool{c.g: true, c.pg: false, c.unrated: false, c.kidShow: true, c.adultShow: false} {
		if got, over := kidsContent(itemOf(t, e, id)); got != want || over != nil {
			t.Errorf("item %s: kidsContent %v override %v, want %v", id, got, over, want)
		}
	}
	var it api.Item
	e.expect(200, "PUT", "/items/"+itoa(c.g)+"/kids-content", e.plain.token, map[string]any{"kidsContent": false}, &it)
	if got, over := kidsContent(it); got || over == nil || *over {
		t.Fatalf("%+v", it)
	}
	e.expect(200, "PUT", "/items/"+itoa(c.adultShow)+"/kids-content", e.plain.token, map[string]any{"kidsContent": true}, nil)
	if got, over := kidsContent(itemOf(t, e, c.adultShow)); !got || over == nil || !*over {
		t.Fatal("an override makes a show kids' content")
	}
	var page api.ItemPage
	e.expect(200, "GET", "/items?kind=movie", e.plain.token, nil, &page)
	for _, m := range page.Items {
		if m.Id == c.g && (m.KidsContent == nil || *m.KidsContent) {
			t.Fatalf("lists carry it too: %+v", m)
		}
	}
	e.expect(204, "DELETE", "/items/"+itoa(c.g)+"/kids-content", e.plain.token, nil, nil)
	e.expect(204, "DELETE", "/items/"+itoa(c.g)+"/kids-content", e.plain.token, nil, nil)
	if got, over := kidsContent(itemOf(t, e, c.g)); !got || over != nil {
		t.Fatal("cleared, the rating decides again")
	}

	e.expect(404, "PUT", "/items/"+itoa(c.kidEp)+"/kids-content", e.plain.token, map[string]any{"kidsContent": true}, nil)
	e.expect(404, "PUT", "/items/"+itoa(uid.N(999))+"/kids-content", e.plain.token, map[string]any{"kidsContent": true}, nil)
	e.expect(400, "PUT", "/items/"+itoa(c.g)+"/kids-content", e.plain.token, map[string]any{}, nil)
	k := e.kid(e.plain, "Kid")
	kidToken := e.kidDevice(e.plain, k.Id)
	e.expect(403, "PUT", "/items/"+itoa(c.g)+"/kids-content", kidToken, map[string]any{"kidsContent": false}, nil)
	e.expect(403, "DELETE", "/items/"+itoa(c.g)+"/kids-content", kidToken, nil, nil)
}

// An adult hiding kids' content browses without it: library lists,
// Continue watching, show progress, YouTube, search and playlists. Details
// and other adults are untouched.
func TestHideKidsContent(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	tb := e.youtubeLibrary()
	e.expect(200, "PUT", "/youtube/channels/"+itoa(tb.channel)+"/for-kids", e.plain.token, map[string]any{"forKids": true}, nil)
	me := "/profiles/" + itoa(e.plain.account.ID)
	for _, id := range []uid.ID{c.g, c.kidEp, c.adultEp, tb.newer} {
		e.expect(200, "PUT", me+"/watch-state/"+itoa(id), e.plain.token, map[string]any{"watched": false, "positionMs": 60000}, nil)
	}
	var pl api.Playlist
	e.expect(201, "POST", me+"/playlists", e.plain.token, map[string]any{"name": "Films", "kind": "movies"}, &pl)
	e.expect(201, "POST", me+"/playlists/"+itoa(pl.Id)+"/entries", e.plain.token, map[string]any{"itemId": c.g}, nil)

	var acct api.Account
	e.expect(200, "PUT", me+"/preferences", e.plain.token, map[string]any{"hideKidsContent": true}, &acct)
	if acct.HideKidsContent == nil || !*acct.HideKidsContent {
		t.Fatalf("%+v", acct)
	}
	var page api.ItemPage
	e.expect(200, "GET", "/items?limit=200", e.plain.token, nil, &page)
	got := ids(page.Items)
	for _, id := range []uid.ID{c.g, c.kidShow, c.kidEp} {
		if got[id] {
			t.Errorf("kids' item %s listed", id)
		}
	}
	for _, id := range []uid.ID{c.pg, c.r, c.unrated, c.adultShow, c.adultEp} {
		if !got[id] {
			t.Errorf("item %s missing", id)
		}
	}
	e.expect(200, "GET", "/items/"+itoa(c.g), e.plain.token, nil, nil)

	var states api.WatchStatePage
	e.expect(200, "GET", me+"/watch-state", e.plain.token, nil, &states)
	var watched []uid.ID
	for _, w := range states.Items {
		watched = append(watched, w.ItemId)
	}
	slices.SortFunc(watched, uid.ID.Compare)
	if !slices.Equal(watched, []uid.ID{c.adultEp}) {
		t.Fatalf("continue watching %v", watched)
	}
	for _, p := range e.showProgress(e.plain.account.ID, "").Items {
		if p.ShowId == c.kidShow {
			t.Fatal("show progress lists the kids' show")
		}
	}
	var channels api.YouTubeChannelPage
	e.expect(200, "GET", "/youtube/channels", e.plain.token, nil, &channels)
	if len(channels.Items) != 1 || channels.Items[0].Id != tb.playlist {
		t.Fatalf("%+v", channels)
	}
	var videos api.YouTubeVideoPage
	e.expect(200, "GET", "/youtube/videos", e.plain.token, nil, &videos)
	if len(videos.Items) != 1 || videos.Items[0].ChannelId != tb.playlist {
		t.Fatalf("%+v", videos)
	}
	var found api.SearchResult
	e.expect(200, "GET", "/search?q=film", e.plain.token, nil, &found)
	for _, m := range found.Movies {
		if m.Id == c.g {
			t.Fatal("search finds the kids' film")
		}
	}
	var order api.PlaylistPlayOrderPage
	e.expect(200, "GET", me+"/playlists/"+itoa(pl.Id)+"/play-order", e.plain.token, nil, &order)
	if len(order.Items) != 0 {
		t.Fatalf("playlists are filtered: %+v", order)
	}

	// The admin household's adult is untouched.
	e.expect(200, "GET", "/items?limit=200", e.admin.token, nil, &page)
	if !ids(page.Items)[c.g] {
		t.Fatal("another adult hides nothing")
	}
	e.expect(200, "PUT", me+"/preferences", e.plain.token, map[string]any{"hideKidsContent": false}, nil)
	e.expect(200, "GET", "/items?limit=200", e.plain.token, nil, &page)
	if !ids(page.Items)[c.g] {
		t.Fatal("turned off, everything is back")
	}
}

// Under rating ceilings a kid sees the channels for kids and their videos;
// on an allow list, the channels on it.
func TestYouTubeForKids(t *testing.T) {
	e := newEnv(t)
	tb := e.youtubeLibrary()
	var ch api.YouTubeChannel
	e.expect(200, "GET", "/youtube/channels/"+itoa(tb.channel), e.plain.token, nil, &ch)
	if ch.ForKids == nil || *ch.ForKids {
		t.Fatalf("a channel starts not for kids: %+v", ch)
	}
	e.expect(200, "PUT", "/youtube/channels/"+itoa(tb.channel)+"/for-kids", e.plain.token, map[string]any{"forKids": true}, &ch)
	if ch.ForKids == nil || !*ch.ForKids || ch.VideoCount != 2 {
		t.Fatalf("%+v", ch)
	}
	e.expect(404, "PUT", "/youtube/channels/"+itoa(tb.newer)+"/for-kids", e.plain.token, map[string]any{"forKids": true}, nil)

	k := e.kid(e.plain, "Kid")
	kidToken := e.kidDevice(e.plain, k.Id)
	e.expect(403, "PUT", "/youtube/channels/"+itoa(tb.channel)+"/for-kids", kidToken, map[string]any{"forKids": false}, nil)
	var channels api.YouTubeChannelPage
	e.expect(200, "GET", "/youtube/channels", kidToken, nil, &channels)
	if len(channels.Items) != 1 || channels.Items[0].Id != tb.channel {
		t.Fatalf("%+v", channels)
	}
	var videos api.YouTubeVideoPage
	e.expect(200, "GET", "/youtube/videos", kidToken, nil, &videos)
	if len(videos.Items) != 2 || videos.Items[0].ChannelId != tb.channel || videos.Items[1].ChannelId != tb.channel {
		t.Fatalf("%+v", videos)
	}
	e.expect(200, "GET", "/youtube/videos/"+itoa(tb.newer), kidToken, nil, nil)
	e.expect(404, "GET", "/youtube/channels/"+itoa(tb.playlist), kidToken, nil, nil)
	kp := "/profiles/" + itoa(k.Id)
	e.expect(204, "PUT", kp+"/youtube/channels/"+itoa(tb.channel)+"/watch-state", kidToken, map[string]any{"watched": true}, nil)
	e.expect(404, "PUT", kp+"/youtube/channels/"+itoa(tb.playlist)+"/watch-state", kidToken, map[string]any{"watched": true}, nil)
	e.expect(403, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/youtube/channels/"+itoa(tb.channel)+"/watch-state", kidToken, map[string]any{"watched": true}, nil)

	// On an allow list, the channel on it, whatever forKids says.
	e.expect(200, "PUT", kp, e.plain.token, map[string]any{"name": "Kid", "birthday": "2018-05-01", "filterMode": "allow_list"}, nil)
	e.expect(204, "PUT", kp+"/allowed-items/"+itoa(tb.playlist), e.plain.token, nil, nil)
	e.expect(200, "GET", "/youtube/channels", kidToken, nil, &channels)
	if len(channels.Items) != 1 || channels.Items[0].Id != tb.playlist {
		t.Fatalf("%+v", channels)
	}
	e.expect(200, "GET", "/youtube/videos", kidToken, nil, &videos)
	if len(videos.Items) != 1 || videos.Items[0].ChannelId != tb.playlist {
		t.Fatalf("%+v", videos)
	}
	e.expect(404, "GET", "/youtube/videos/"+itoa(tb.newer), kidToken, nil, nil)
	e.expect(404, "POST", "/playback/sessions", kidToken, map[string]any{"profileId": k.Id, "itemId": tb.newer, "snapshot": snapshot()}, nil)
}

// Every kid hears all music but songs tagged explicit: an album or artist
// with nothing else is hidden, and a mixed album shows its clean songs.
func TestMusicForKids(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	explicit := func(id uid.ID) {
		t.Helper()
		s, err := e.svc.Items.Repo().Get(id)
		must(t, err)
		yes := true
		s.Explicit = &yes
		must(t, e.svc.Items.Repo().Update(s))
	}
	var loose api.MusicSongPage
	e.expect(200, "GET", "/music/songs?albumId="+itoa(m.loose.Id), e.plain.token, nil, &loose)
	explicit(m.songs[1].Id) // Second Wind, the guest's only song
	explicit(loose.Items[0].Id)

	k := e.kid(e.plain, "Kid")
	tok := e.kidDevice(e.plain, k.Id)
	var artists api.MusicArtistPage
	e.expect(200, "GET", "/music/artists", tok, nil, &artists)
	if len(artists.Items) != 1 || artists.Items[0].Id != m.band.Id || artists.Items[0].SongCount != 2 || artists.Items[0].AlbumCount != 1 {
		t.Fatalf("%+v", artists)
	}
	var albums api.MusicAlbumPage
	e.expect(200, "GET", "/music/albums?sort=added", tok, nil, &albums)
	if len(albums.Items) != 1 || albums.Items[0].Id != m.album.Id || albums.Items[0].SongCount != 2 {
		t.Fatalf("%+v", albums)
	}
	var songs api.MusicSongPage
	e.expect(200, "GET", "/music/songs?albumId="+itoa(m.album.Id), tok, nil, &songs)
	if len(songs.Items) != 2 || songs.Items[0].Id != m.songs[0].Id || songs.Items[1].Id != m.songs[2].Id {
		t.Fatalf("%+v", songs)
	}
	var letters api.MusicLetterCountList
	e.expect(200, "GET", "/music/letters?of=albums", tok, nil, &letters)
	if len(letters.Items) != 1 || letters.Items[0].Letter != "F" || letters.Items[0].Count != 1 {
		t.Fatalf("%+v", letters)
	}
	var found api.MusicSearchResult
	e.expect(200, "GET", "/music/search?q=o", tok, nil, &found)
	for _, s := range found.Songs {
		if s.Id == m.songs[1].Id || s.Id == loose.Items[0].Id {
			t.Fatalf("search finds an explicit song: %+v", s)
		}
	}
	for _, path := range []string{"/music/artists/" + itoa(m.guest.Id), "/music/artists/" + itoa(m.folder.Id), "/music/albums/" + itoa(m.loose.Id),
		"/music/songs/" + itoa(m.songs[1].Id), "/items/" + itoa(m.loose.Id) + "/artwork/poster"} {
		e.expect(404, "GET", path, tok, nil, nil)
	}
	e.expect(200, "GET", "/music/songs/"+itoa(m.songs[0].Id), tok, nil, nil)
	e.expect(200, "GET", "/items/"+itoa(m.album.Id)+"/artwork/poster", tok, nil, nil)
	e.expect(404, "POST", "/playback/sessions", tok, map[string]any{"profileId": k.Id, "itemId": m.songs[1].Id, "snapshot": songSnapshot(true, false)}, nil)
	e.expect(201, "POST", "/playback/sessions", tok, map[string]any{"profileId": k.Id, "itemId": m.songs[0].Id, "snapshot": songSnapshot(true, false)}, nil)
	var recent api.MusicRecentPage
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/music/recently-played", tok, nil, &recent)
	if len(recent.Items) != 1 || recent.Items[0].Album == nil || recent.Items[0].Album.Id != m.album.Id {
		t.Fatalf("%+v", recent)
	}
	e.expect(403, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/music/recently-played", tok, nil, nil)

	// An adult hiding kids' content hears everything.
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/preferences", e.plain.token, map[string]any{"hideKidsContent": true}, nil)
	e.expect(200, "GET", "/music/artists", e.plain.token, nil, &artists)
	if len(artists.Items) != 3 {
		t.Fatalf("%+v", artists)
	}
}

// An artist's picture, its newest album's art, is to a kid that of the
// newest album they may hear.
func TestArtistArtForKidsSkipsExplicitAlbums(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	band, err := e.svc.Items.Repo().Get(m.band.Id)
	must(t, err)
	year, yes := 2099, true
	newer := &item.Item{LibraryID: band.LibraryID, ParentID: &band.ID, Kind: item.Album, ParsedTitle: "Loud", ParsedYear: &year}
	must(t, e.svc.Items.Repo().Create(newer))
	must(t, e.svc.Items.Repo().Create(&item.Item{LibraryID: band.LibraryID, ParentID: &newer.ID, Kind: item.Song, ParsedTitle: "Shout", Explicit: &yes}))
	cover := filepath.Join(t.TempDir(), "loud.jpg")
	must(t, os.WriteFile(cover, []byte("explicit cover"), 0o600))
	must(t, e.svc.Items.Repo().SetLocal(&item.Metadata{ItemID: newer.ID, Title: "Loud", PosterPath: cover}))

	poster := "/items/" + itoa(m.band.Id) + "/artwork/poster"
	if _, body := e.raw("GET", poster, e.plain.token); string(body) != "explicit cover" {
		t.Fatalf("an adult sees the newest album's art: %q", body)
	}
	k := e.kid(e.plain, "Kid")
	if res, body := e.raw("GET", poster, e.kidDevice(e.plain, k.Id)); res.StatusCode != 200 || string(body) == "explicit cover" {
		t.Fatalf("a kid sees the art of an album they may not hear: %d %q", res.StatusCode, body)
	}
}

// A song never read for the tag is kept from kids until a scan reads it.
func TestUnreadSongsAreKeptFromKids(t *testing.T) {
	e := newEnv(t)
	song := e.song(e.library("music"), "Unread")
	k := e.kid(e.plain, "Kid")
	tok := e.kidDevice(e.plain, k.Id)
	e.expect(404, "GET", "/music/songs/"+itoa(song), tok, nil, nil)
	s, err := e.svc.Items.Repo().Get(song)
	must(t, err)
	if s.Explicit != nil {
		t.Fatalf("%+v", s)
	}
	no := false
	s.Explicit = &no
	must(t, e.svc.Items.Repo().Update(s))
	e.expect(200, "GET", "/music/songs/"+itoa(song), tok, nil, nil)
}

// An item menu learns which allow-list kids allow a title or channel, and
// any adult of the household edits it one kid at a time.
func TestAllowedFor(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	tb := e.youtubeLibrary()
	var a, b api.Account
	e.expect(201, "POST", "/profiles", e.plain.token, map[string]any{"name": "A", "birthday": "2018-05-01", "filterMode": "allow_list"}, &a)
	e.expect(201, "POST", "/profiles", e.plain.token, map[string]any{"name": "B", "birthday": "2018-05-01", "filterMode": "allow_list"}, &b)
	rated := e.kid(e.plain, "Rated")
	neighbor := e.neighbor(e.plain)
	e.expect(204, "PUT", "/profiles/"+itoa(a.Id)+"/allowed-items/"+itoa(c.r), neighbor, nil, nil)
	e.expect(204, "PUT", "/profiles/"+itoa(b.Id)+"/allowed-items/"+itoa(c.r), e.plain.token, nil, nil)
	e.expect(204, "PUT", "/profiles/"+itoa(b.Id)+"/allowed-items/"+itoa(tb.channel), e.plain.token, nil, nil)
	e.expect(204, "PUT", "/profiles/"+itoa(rated.Id)+"/allowed-items/"+itoa(c.r), e.plain.token, nil, nil)

	allowed := func(token string, id uid.ID) []uid.ID {
		t.Helper()
		var v api.AllowedFor
		e.expect(200, "GET", "/items/"+itoa(id)+"/allowed-for", token, nil, &v)
		return v.ProfileIds
	}
	if got := allowed(e.plain.token, c.r); !slices.Equal(got, []uid.ID{a.Id, b.Id}) {
		t.Fatalf("only kids on an allow list count: %v", got)
	}
	if got := allowed(neighbor, tb.channel); !slices.Equal(got, []uid.ID{b.Id}) {
		t.Fatalf("%v", got)
	}
	e.expect(204, "DELETE", "/profiles/"+itoa(b.Id)+"/allowed-items/"+itoa(c.r), neighbor, nil, nil)
	if got := allowed(e.plain.token, c.r); !slices.Equal(got, []uid.ID{a.Id}) {
		t.Fatalf("%v", got)
	}
	if got := allowed(e.plain.token, c.g); got == nil || len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if got := allowed(e.admin.token, c.r); len(got) != 0 {
		t.Fatalf("another household's kids are not told: %v", got)
	}
	e.expect(404, "GET", "/items/"+itoa(uid.N(999))+"/allowed-for", e.plain.token, nil, nil)
	e.expect(403, "GET", "/items/"+itoa(c.r)+"/allowed-for", e.kidDevice(e.plain, a.Id), nil, nil)
}
