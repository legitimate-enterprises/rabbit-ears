package httpapi_test

import (
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func favoritesOf(profile uid.ID) string { return "/profiles/" + itoa(profile) + "/favorites" }

// at is t as an `at` query.
func at(t time.Time) string { return "?at=" + url.QueryEscape(t.Format(time.RFC3339Nano)) }

func (e *env) favorites(u user, profile uid.ID, query string) api.FavoritePage {
	e.t.Helper()
	var page api.FavoritePage
	e.expect(200, "GET", favoritesOf(profile)+query, u.token, nil, &page)
	return page
}

func favoriteIDs(page api.FavoritePage) []uid.ID {
	out := []uid.ID{}
	for _, f := range page.Items {
		out = append(out, f.ItemId)
	}
	return out
}

// A star and an unstar are idempotent, so a queued one can replay: the
// first star's instant stays, and an unstar older than the star leaves it.
func TestAFavoriteIsSetAndUnsetIdempotently(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Kite")
	me := e.plain.account.ID
	p := favoritesOf(me) + "/" + itoa(m)
	first := time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC)
	e.expect(204, "PUT", p+at(first), e.plain.token, nil, nil)
	e.expect(204, "PUT", p+at(first.Add(time.Hour)), e.plain.token, nil, nil)
	page := e.favorites(e.plain, me, "")
	if len(page.Items) != 1 || page.Items[0].ItemId != m || page.Items[0].Kind != api.FavoriteKindMovie ||
		!page.Items[0].CreatedAt.Equal(first) || page.NextOffset != nil {
		t.Fatalf("after two stars %+v", page)
	}
	e.expect(204, "DELETE", p+at(first.Add(-time.Minute)), e.plain.token, nil, nil)
	if got := favoriteIDs(e.favorites(e.plain, me, "")); !slices.Equal(got, []uid.ID{m}) {
		t.Fatalf("an unstar older than the star removed it: %v", got)
	}
	e.expect(204, "DELETE", p+at(first.Add(time.Minute)), e.plain.token, nil, nil)
	e.expect(204, "DELETE", p, e.plain.token, nil, nil)
	if got := e.favorites(e.plain, me, ""); len(got.Items) != 0 {
		t.Fatalf("after unstarring %+v", got)
	}
	// A time ahead of the server's is now; absent is now.
	before := time.Now().UTC().Truncate(time.Millisecond)
	e.expect(204, "PUT", p+at(time.Now().Add(24*time.Hour)), e.plain.token, nil, nil)
	if c := e.favorites(e.plain, me, "").Items[0].CreatedAt; c.Before(before) || c.After(time.Now()) {
		t.Fatalf("a future star at %v", c)
	}
	e.expect(204, "DELETE", p, e.plain.token, nil, nil)
	e.expect(204, "PUT", p, e.plain.token, nil, nil)
	if c := e.favorites(e.plain, me, "").Items[0].CreatedAt; c.Before(before) || c.After(time.Now()) {
		t.Fatalf("a star without at at %v", c)
	}
	e.expect(400, "PUT", p+"?at=yesterday", e.plain.token, nil, nil)
	e.expect(404, "PUT", favoritesOf(me)+"/"+itoa(uid.N(999)), e.plain.token, nil, nil)
	// An unstar of what is gone still answers 204, so a queued one never fails.
	e.expect(204, "DELETE", favoritesOf(me)+"/"+itoa(uid.N(999)), e.plain.token, nil, nil)
}

// Movies, shows, seasons, episodes, artists, albums, songs and videos are
// favorites; a channel is not. The list is newest first, by kind on request.
func TestTheEightKindsAreFavorites(t *testing.T) {
	e := newEnv(t)
	tb := e.youtubeLibrary()
	music := e.library("music")
	items := e.svc.Items.Repo()
	artist := &item.Item{LibraryID: music, Kind: item.Artist, ParsedTitle: "Band"}
	must(t, items.Create(artist))
	album := &item.Item{LibraryID: music, ParentID: &artist.ID, Kind: item.Album, ParsedTitle: "Album"}
	must(t, items.Create(album))
	song := &item.Item{LibraryID: music, ParentID: &album.ID, Kind: item.Song, ParsedTitle: "Song", ArtistID: &artist.ID}
	must(t, items.Create(song))
	show, ep := e.show(e.library("tv"), "Show", "TV-PG")
	m := e.movie(e.library("movies"), "Kite")
	season := itemOf(t, e, ep).ParentId

	me := e.plain.account.ID
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	order := []uid.ID{m, show, *season, ep, artist.ID, album.ID, song.ID, tb.newer}
	for i, id := range order {
		e.expect(204, "PUT", favoritesOf(me)+"/"+itoa(id)+at(base.Add(time.Duration(i)*time.Second)), e.plain.token, nil, nil)
	}
	e.expect(400, "PUT", favoritesOf(me)+"/"+itoa(tb.channel), e.plain.token, nil, nil)

	page := e.favorites(e.plain, me, "")
	want := slices.Clone(order)
	slices.Reverse(want)
	if got := favoriteIDs(page); !slices.Equal(got, want) {
		t.Fatalf("newest first %v, want %v", got, want)
	}
	kinds := []api.FavoriteKind{}
	for _, f := range page.Items {
		kinds = append(kinds, f.Kind)
	}
	if !slices.Equal(kinds, []api.FavoriteKind{"video", "song", "album", "artist", "episode", "season", "show", "movie"}) {
		t.Fatalf("kinds %v", kinds)
	}
	if got := favoriteIDs(e.favorites(e.plain, me, "?kind=song")); !slices.Equal(got, []uid.ID{song.ID}) {
		t.Fatalf("songs %v", got)
	}
	e.expect(400, "GET", favoritesOf(me)+"?kind=channel", e.plain.token, nil, nil)
	paged := e.favorites(e.plain, me, "?limit=3&offset=3")
	if got := favoriteIDs(paged); !slices.Equal(got, want[3:6]) || paged.NextOffset == nil || *paged.NextOffset != 6 {
		t.Fatalf("a page %v next %v", got, paged.NextOffset)
	}
}

// A profile stars only what it may see, lists only what it browses, and
// keeps a favorite that is hidden for a while. Favorites are each profile's
// own, and no adult reaches another's.
func TestFavoritesFollowWhatTheProfileMaySee(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	kid := e.kid(e.plain, "Mia").Id
	e.expect(404, "PUT", favoritesOf(kid)+"/"+itoa(c.r), e.plain.token, nil, nil)
	e.expect(204, "PUT", favoritesOf(kid)+"/"+itoa(c.g), e.plain.token, nil, nil)
	e.expect(204, "PUT", favoritesOf(kid)+"/"+itoa(c.kidEp), e.plain.token, nil, nil)
	if got := favoriteIDs(e.favorites(e.plain, kid, "")); len(got) != 2 {
		t.Fatalf("the kid's favorites %v", got)
	}
	if got := e.favorites(e.plain, e.plain.account.ID, ""); len(got.Items) != 0 {
		t.Fatalf("the adult has the kid's favorites %+v", got)
	}

	e.rate(c.g, "R")
	if got := favoriteIDs(e.favorites(e.plain, kid, "")); !slices.Equal(got, []uid.ID{c.kidEp}) {
		t.Fatalf("a favorite rated above the kid's ceiling is listed: %v", got)
	}
	e.rate(c.g, "G")
	if got := favoriteIDs(e.favorites(e.plain, kid, "?kind=movie")); !slices.Equal(got, []uid.ID{c.g}) {
		t.Fatalf("a favorite seen again is not listed: %v", got)
	}

	me := e.plain.account.ID
	e.expect(204, "PUT", favoritesOf(me)+"/"+itoa(c.g), e.plain.token, nil, nil)
	e.expect(204, "PUT", favoritesOf(me)+"/"+itoa(c.r), e.plain.token, nil, nil)
	e.hideKids(e.plain, true)
	if got := favoriteIDs(e.favorites(e.plain, me, "")); !slices.Equal(got, []uid.ID{c.r}) {
		t.Fatalf("hiding kids' content lists %v", got)
	}
	e.expect(204, "DELETE", favoritesOf(me)+"/"+itoa(c.g), e.plain.token, nil, nil)
	e.hideKids(e.plain, false)
	if got := favoriteIDs(e.favorites(e.plain, me, "")); !slices.Equal(got, []uid.ID{c.r}) {
		t.Fatalf("a hidden favorite was not removed: %v", got)
	}

	admin := e.admin.account.ID
	e.expect(404, "GET", favoritesOf(admin), e.plain.token, nil, nil)
	e.expect(404, "PUT", favoritesOf(admin)+"/"+itoa(c.g), e.plain.token, nil, nil)
	e.expect(404, "DELETE", favoritesOf(admin)+"/"+itoa(c.r), e.plain.token, nil, nil)
}

// Queued stars and unstars replay, and mark the row they touched.
func TestFavoritesReplay(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	me, kid := e.plain.account.ID, e.kid(e.plain, "Mia").Id
	when := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	put := action("putFavorite", "PUT", favoritesOf(me)+"/"+itoa(c.pg)+at(when), me, nil)
	put["touched"] = []any{touched("favorite", me, c.pg)}
	e.accepted(e.plain, put)
	page := e.favorites(e.plain, me, "")
	if len(page.Items) != 1 || !page.Items[0].CreatedAt.Equal(when) {
		t.Fatalf("after a replayed star %+v", page)
	}
	if e.count(`SELECT sync_version FROM favorite WHERE account_id = ? AND item_id = ?`, me, c.pg) == 0 {
		t.Fatal("the starred row is not stamped")
	}
	del := action("deleteFavorite", "DELETE", favoritesOf(me)+"/"+itoa(c.pg)+at(when.Add(time.Minute)), me, nil)
	del["touched"] = []any{touched("favorite", me, c.pg)}
	hidden := action("putFavorite", "PUT", favoritesOf(kid)+"/"+itoa(c.r), me, nil)
	hidden["touched"] = []any{touched("favorite", kid, c.r)}
	res := e.replay(e.plain, del, hidden)
	if res[0].Result != api.Accepted {
		t.Fatalf("a replayed unstar %+v", res[0])
	}
	rejected(t, res[1], "not_found")
	if e.count(`SELECT COUNT(*) FROM favorite`) != 0 ||
		e.count(`SELECT COUNT(*) FROM sync_tombstone WHERE tbl = 'favorite' AND k1 = ? AND k2 = ?`, me, c.pg) != 1 {
		t.Fatal("the unstarred row is not marked deleted")
	}
}
