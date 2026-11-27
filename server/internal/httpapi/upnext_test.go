package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// upNext answers what follows item for the profile, a zero ItemId when nothing does.
func (e *env) upNext(token string, profile, item uid.ID, query string) api.UpNext {
	e.t.Helper()
	var out api.UpNext
	code, _ := e.do("GET", "/profiles/"+itoa(profile)+"/up-next/"+itoa(item)+query, token, nil, &out)
	if code == 204 {
		return api.UpNext{}
	}
	if code != 200 {
		e.t.Fatalf("up next of %s: %d", item, code)
	}
	return out
}

// gone marks every file of the item missing.
func (e *env) gone(itemID uid.ID) {
	e.t.Helper()
	files := e.svc.MediaFiles.Repo()
	fs, err := files.FilesForItem(itemID)
	must(e.t, err)
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, f := range fs {
		must(e.t, files.SetMissing(f, &at))
	}
}

func TestUpNextEpisodes(t *testing.T) {
	e := newEnv(t)
	tv := e.library("tv")
	show, s1e1 := e.show(tv, "Alpha", "TV-PG")
	var s1 uid.ID
	{
		var page api.ItemPage
		e.expect(200, "GET", "/items?parentId="+itoa(show), e.admin.token, nil, &page)
		s1 = page.Items[0].Id
	}
	specials := e.child(tv, show, "season", 0)
	s0e1 := e.child(tv, specials, "episode", 1)
	s2 := e.child(tv, show, "season", 2)
	s2e1 := e.child(tv, s2, "episode", 1)
	s1e3 := e.child(tv, s1, "episode", 3)
	s1e2 := e.child(tv, s1, "episode", 2)
	for _, ep := range []uid.ID{s1e1, s1e2, s1e3, s2e1, s0e1} {
		e.file(ep, 60000)
	}
	e.expect(200, "PUT", "/items/"+itoa(s2e1)+"/metadata", e.admin.token, map[string]any{"status": "matched", "title": "Homecoming"}, nil)
	me, tok := e.plain.account.ID, e.plain.token

	got := e.upNext(tok, me, s1e1, "")
	if got.ItemId != s1e2 || got.Kind != api.UpNextKindEpisode || got.Item == nil || got.Item.Id != s1e2 || got.Video != nil {
		t.Fatalf("the next episode by number: %+v", got)
	}
	e.gone(s1e3)
	got = e.upNext(tok, me, s1e2, "")
	if got.ItemId != s2e1 || got.Title != "Alpha · S2 E1 · Homecoming" {
		t.Fatalf("an unavailable episode is skipped, across seasons: %+v", got)
	}
	if got := e.upNext(tok, me, s2e1, ""); got.ItemId != s0e1 {
		t.Fatalf("specials come last: %+v", got)
	}
	if got := e.upNext(tok, me, s0e1, ""); !got.ItemId.IsZero() {
		t.Fatalf("nothing after the last: %+v", got)
	}

	// A kid on an allow list is only offered the episodes on it.
	k := e.kid(e.plain, "Kid")
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id), tok, map[string]any{"name": "Kid", "birthday": "2018-05-01", "filterMode": "allow_list"}, nil)
	for _, ep := range []uid.ID{s1e1, s2e1} {
		e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/allowed-items/"+itoa(ep), tok, nil, nil)
	}
	kid := e.kidDevice(e.plain, k.Id)
	if got := e.upNext(kid, k.Id, s1e1, ""); got.ItemId != s2e1 {
		t.Fatalf("a kid skips what they may not see: %+v", got)
	}
	e.expect(404, "GET", "/profiles/"+itoa(k.Id)+"/up-next/"+itoa(s1e2), kid, nil, nil)
}

func TestUpNextMoviesAndVideos(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	first, rated, third, last := e.film(films, "One", "2001-05-01", "G"), e.film(films, "Two", "2003-05-01", "R"),
		e.film(films, "Three", "2005-05-01", "PG"), e.film(films, "Four", "2007-05-01", "G")
	alone := e.film(films, "Alone", "2002-01-01", "G")
	for _, m := range []uid.ID{first, rated, third, last, alone} {
		e.file(m, 60000)
	}
	e.tmdbCollection(10, "Saga", last, third, rated, first)
	me, tok := e.plain.account.ID, e.plain.token

	if got := e.upNext(tok, me, first, ""); got.ItemId != rated || got.Kind != api.UpNextKindMovie || got.Title != "Two" {
		t.Fatalf("the next film by release: %+v", got)
	}
	e.gone(third)
	if got := e.upNext(tok, me, rated, ""); got.ItemId != last {
		t.Fatalf("an unavailable film is skipped: %+v", got)
	}
	if got := e.upNext(tok, me, last, ""); !got.ItemId.IsZero() {
		t.Fatalf("nothing after the last film: %+v", got)
	}
	if got := e.upNext(tok, me, alone, ""); !got.ItemId.IsZero() {
		t.Fatalf("nothing after a film in no collection: %+v", got)
	}
	k := e.kid(e.plain, "Kid")
	if got := e.upNext(e.kidDevice(e.plain, k.Id), k.Id, first, ""); got.ItemId != last {
		t.Fatalf("a kid skips a film above their ceiling: %+v", got)
	}

	tb := e.youtubeLibrary()
	got := e.upNext(tok, me, tb.older, "")
	if got.ItemId != tb.newer || got.Kind != api.UpNextKindVideo || got.Video == nil || got.Video.Id != tb.newer || got.Item != nil || got.Title != "Building a bench" {
		t.Fatalf("the next video by publish date: %+v", got)
	}
	if got := e.upNext(tok, me, tb.newer, ""); !got.ItemId.IsZero() {
		t.Fatalf("nothing after the newest video: %+v", got)
	}
}

func TestUpNextPlaylist(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	a, b, c := e.movie(films, "Heat"), e.movie(films, "Ronin"), e.movie(films, "Thief")
	for _, m := range []uid.ID{a, b, c} {
		e.file(m, 60000)
	}
	me, tok := e.plain.account.ID, e.plain.token
	base := "/profiles/" + itoa(me) + "/playlists"
	var pl api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Weekend", "kind": "movies"}, &pl)
	for _, m := range []uid.ID{a, b, a, c} {
		e.expect(201, "POST", base+"/"+itoa(pl.Id)+"/entries", tok, map[string]any{"itemId": m}, nil)
	}
	from := "?playlistId=" + itoa(pl.Id)

	if got := e.upNext(tok, me, a, from); got.ItemId != b || got.Index == nil || *got.Index != 1 {
		t.Fatalf("after the item's first row: %+v", got)
	}
	e.gone(b)
	if got := e.upNext(tok, me, a, from+"&index=0"); got.ItemId != a || *got.Index != 2 {
		t.Fatalf("an unavailable row is skipped: %+v", got)
	}
	if got := e.upNext(tok, me, a, from+"&index=2"); got.ItemId != c || *got.Index != 3 {
		t.Fatalf("index picks among repeats: %+v", got)
	}
	if got := e.upNext(tok, me, c, from+"&index=3"); !got.ItemId.IsZero() {
		t.Fatalf("nothing after the last row: %+v", got)
	}
	e.expect(404, "GET", "/profiles/"+itoa(e.admin.account.ID)+"/up-next/"+itoa(a)+from, e.admin.token, nil, nil)
	e.expect(400, "GET", "/profiles/"+itoa(me)+"/up-next/"+itoa(a)+"?index=1", tok, nil, nil)
}

func TestAutoplayPreference(t *testing.T) {
	e := newEnv(t)
	tok := e.plain.token
	me := "/profiles/" + itoa(e.plain.account.ID)
	var acct api.Account
	e.expect(200, "GET", me, tok, nil, &acct)
	if acct.Autoplay == nil || !*acct.Autoplay {
		t.Fatalf("autoplay is on for a new profile: %+v", acct)
	}
	e.expect(200, "PUT", me+"/preferences", tok, map[string]any{"autoplay": false}, &acct)
	if acct.Autoplay == nil || *acct.Autoplay {
		t.Fatalf("%+v", acct)
	}

	k := e.kid(e.plain, "Kid")
	kp := "/profiles/" + itoa(k.Id) + "/preferences"
	e.expect(200, "PUT", kp, tok, map[string]any{"autoplay": false}, &acct)
	if *acct.Autoplay {
		t.Fatalf("an adult sets a kid's: %+v", acct)
	}
	kid := e.kidDevice(e.plain, k.Id)
	e.expect(200, "PUT", kp, kid, map[string]any{"autoplay": true}, &acct)
	if !*acct.Autoplay {
		t.Fatalf("a kid sets their own: %+v", acct)
	}
	e.expect(403, "PUT", kp, kid, map[string]any{"color": "#2E6B5B"}, nil)
	e.expect(403, "PUT", me+"/preferences", kid, map[string]any{"autoplay": true}, nil)
}
