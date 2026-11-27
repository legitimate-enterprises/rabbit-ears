package httpapi_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func (e *env) film(lib uid.ID, title, date, rating string) uid.ID {
	e.t.Helper()
	id := e.movie(lib, title)
	must(e.t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: id, Status: item.Matched, Title: title, ReleaseDate: date, ContentRating: rating}))
	return id
}

// kidDevice signs u in on a second device and switches it to kid.
func (e *env) kidDevice(u user, kid uid.ID) string {
	e.t.Helper()
	token, _, err := e.svc.Devices.Register(u.account.ID, u.household.ID, "Tablet", "web")
	must(e.t, err)
	e.expect(204, "POST", "/profiles/"+itoa(kid)+"/unlock", token, map[string]any{}, nil)
	return token
}

// tmdbCollection records a TMDB collection holding movies, as a scan does.
func (e *env) tmdbCollection(tmdbID int64, name string, movies ...uid.ID) uid.ID {
	e.t.Helper()
	c := &collection.Collection{TMDBID: &tmdbID, Name: name, Overview: "From TMDB.", PosterPath: "/saga.jpg"}
	must(e.t, e.svc.Collections.Repo().PutTMDB(c))
	for _, m := range movies {
		must(e.t, e.svc.Collections.Repo().SetTMDBMovie(m, &c.ID))
	}
	return c.ID
}

func collectionIDs(page api.CollectionPage) (out []uid.ID) {
	for _, c := range page.Items {
		out = append(out, c.Id)
	}
	return out
}

func itemIDs(page api.ItemPage) (out []uid.ID) {
	for _, it := range page.Items {
		out = append(out, it.Id)
	}
	return out
}

// every route a collection id reaches, with a body where one is needed.
func collectionRoutes(id, movie uid.ID) []struct {
	method, path string
	body         any
} {
	c := "/collections/" + itoa(id)
	return []struct {
		method, path string
		body         any
	}{
		{"GET", c, nil},
		{"PUT", c, map[string]any{"name": "Taken"}},
		{"GET", c + "/movies", nil},
		{"PUT", c + "/movies/" + itoa(movie), nil},
		{"DELETE", c + "/movies/" + itoa(movie), nil},
		{"PUT", c + "/movies/" + itoa(movie) + "/position", map[string]any{"position": 0}},
		{"DELETE", c + "/order", nil},
		{"GET", c + "/artwork/poster", nil},
		{"DELETE", c, nil},
	}
}

// A custom collection belongs to one household: every route answers 404 to
// any other, it is never listed there, a movie's details there do not name
// it, and no playlist there can hold it.
func TestCollectionsStayInTheirHousehold(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	m := e.film(films, "Paper Kite", "2001-01-01", "PG")
	var c api.Collection
	e.expect(201, "POST", "/collections", e.plain.token, map[string]any{"name": "Kites"}, &c)
	e.expect(204, "PUT", "/collections/"+itoa(c.Id)+"/movies/"+itoa(m), e.plain.token, nil, nil)

	other := e.admin.token
	for _, r := range collectionRoutes(c.Id, m) {
		e.expect(404, r.method, r.path, other, r.body, nil)
	}
	var page api.CollectionPage
	e.expect(200, "GET", "/collections", other, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("listed to another household: %+v", page)
	}
	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(m), other, nil, &it)
	if it.Collections == nil || len(*it.Collections) != 0 {
		t.Fatalf("named to another household: %+v", it.Collections)
	}
	var pl api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.admin.account.ID)+"/playlists", other, map[string]any{"name": "P", "kind": "movies"}, &pl)
	e.expect(404, "POST", "/profiles/"+itoa(e.admin.account.ID)+"/playlists/"+itoa(pl.Id)+"/entries", other, map[string]any{"collectionId": c.Id}, nil)

	if _, err := e.svc.Collections.Repo().Get(c.Id); err != nil {
		t.Fatal("another household deleted it")
	}
	var mine api.ItemPage
	e.expect(200, "GET", "/collections/"+itoa(c.Id)+"/movies", e.plain.token, nil, &mine)
	if !slices.Equal(itemIDs(mine), []uid.ID{m}) {
		t.Fatalf("another household changed it: %+v", mine)
	}
	e.expect(401, "GET", "/collections", "", nil, nil)
}

// A kid of the household sees its collections, only the movies they may
// see, and cannot write; a collection holding nothing they may see does not
// exist for them on any route.
func TestCollectionsForKids(t *testing.T) {
	e := newEnv(t)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, t.TempDir()
	films := e.library("movies")
	pg, r := e.film(films, "Paper Kite", "2001-01-01", "PG"), e.film(films, "Iron Kite", "1999-01-01", "R")
	r2 := e.film(films, "Steel Kite", "2003-01-01", "R")
	must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: r, Status: item.Matched, ContentRating: "R", ReleaseDate: "1999-01-01", PosterPath: "/iron.jpg"}))
	must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: pg, Status: item.Matched, ContentRating: "PG", ReleaseDate: "2001-01-01", PosterPath: "/paper.jpg"}))
	tok := e.plain.token
	var mixed, adultOnly, empty api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Kites"}, &mixed)
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Late kites"}, &adultOnly)
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Empty"}, &empty)
	for _, m := range []uid.ID{pg, r} {
		e.expect(204, "PUT", "/collections/"+itoa(mixed.Id)+"/movies/"+itoa(m), tok, nil, nil)
	}
	e.expect(204, "PUT", "/collections/"+itoa(adultOnly.Id)+"/movies/"+itoa(r2), tok, nil, nil)
	saga := e.tmdbCollection(41, "Kite Saga", pg, r)
	hidden := e.tmdbCollection(42, "Iron Saga", r2, e.film(films, "Wire Kite", "2005-01-01", "R"))

	k := e.kid(e.plain, "Kid") // PG
	kt := e.kidDevice(e.plain, k.Id)

	var page api.CollectionPage
	e.expect(200, "GET", "/collections", kt, nil, &page)
	if !slices.Equal(collectionIDs(page), []uid.ID{saga, mixed.Id}) || page.Items[0].MovieCount != 1 || page.Items[1].MovieCount != 1 {
		t.Fatalf("a kid sees only collections holding a movie they may see, counting those: %+v", page)
	}
	var got api.Collection
	e.expect(200, "GET", "/collections/"+itoa(mixed.Id), kt, nil, &got)
	if got.MovieCount != 1 || got.Source != api.CollectionSourceCustom {
		t.Fatalf("%+v", got)
	}
	var movies api.ItemPage
	e.expect(200, "GET", "/collections/"+itoa(mixed.Id)+"/movies", kt, nil, &movies)
	if !slices.Equal(itemIDs(movies), []uid.ID{pg}) {
		t.Fatalf("a kid sees only movies they may see: %+v", movies)
	}
	status, raw := e.do("GET", "/collections/"+itoa(mixed.Id)+"/artwork/poster", kt, nil, nil)
	if status != 200 || string(raw) != "w500/paper.jpg" {
		t.Fatalf("a custom poster is the first movie the kid may see: %d %s", status, raw)
	}
	if status, raw = e.do("GET", "/collections/"+itoa(mixed.Id)+"/artwork/poster", tok, nil, nil); status != 200 || string(raw) != "w500/iron.jpg" {
		t.Fatalf("the adult's first movie: %d %s", status, raw)
	}
	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(pg), kt, nil, &it)
	if it.Collections == nil || len(*it.Collections) != 2 {
		t.Fatalf("details name the collections a kid may see: %+v", it.Collections)
	}

	// No writes from a kid, whatever the collection.
	e.expect(403, "POST", "/collections", kt, map[string]any{"name": "Mine"}, nil)
	for _, id := range []uid.ID{mixed.Id, saga} {
		c := "/collections/" + itoa(id)
		e.expect(403, "PUT", c, kt, map[string]any{"name": "Mine"}, nil)
		e.expect(403, "PUT", c+"/movies/"+itoa(pg), kt, nil, nil)
		e.expect(403, "DELETE", c+"/movies/"+itoa(pg), kt, nil, nil)
		e.expect(403, "PUT", c+"/movies/"+itoa(pg)+"/position", kt, map[string]any{"position": 0}, nil)
		e.expect(403, "DELETE", c+"/order", kt, nil, nil)
		e.expect(403, "DELETE", c, kt, nil, nil)
	}

	// Collections holding nothing the kid may see do not exist for them.
	for _, id := range []uid.ID{adultOnly.Id, empty.Id, hidden} {
		for _, rt := range collectionRoutes(id, pg) {
			e.expect(404, rt.method, rt.path, kt, rt.body, nil)
		}
	}
	e.expect(200, "GET", "/items/"+itoa(pg), kt, nil, &it)
	if len(*it.Collections) != 2 {
		t.Fatalf("%+v", it.Collections)
	}

	// The adult still sees everything.
	e.expect(200, "GET", "/collections", tok, nil, &page)
	if len(page.Items) != 5 {
		t.Fatalf("%+v", page)
	}
	for _, id := range []uid.ID{adultOnly.Id, empty.Id, mixed.Id} {
		e.expect(200, "GET", "/collections/"+itoa(id), tok, nil, nil)
	}
}

func TestCustomCollectionLifecycle(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	late, early := e.film(films, "Kite Two", "2010-05-01", "PG"), e.film(films, "Kite One", "2008-01-01", "PG")
	undated := e.movie(films, "Kite Zero")
	_, ep := e.show(e.library("tv"), "Show", "TV-PG")
	tok := e.plain.token

	var c api.Collection
	e.expect(400, "POST", "/collections", tok, map[string]any{"name": "  "}, nil)
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": " Kites "}, &c)
	if c.Name != "Kites" || c.Source != api.CollectionSourceCustom || c.HouseholdId == nil || *c.HouseholdId != e.plain.household.ID ||
		c.TmdbId != nil || c.Overview != nil || c.MovieCount != 0 {
		t.Fatalf("%+v", c)
	}
	one := "/collections/" + itoa(c.Id)
	for _, m := range []uid.ID{late, undated, early, late} {
		e.expect(204, "PUT", one+"/movies/"+itoa(m), tok, nil, nil)
	}
	e.expect(400, "PUT", one+"/movies/"+itoa(ep), tok, nil, nil)
	e.expect(404, "PUT", one+"/movies/"+itoa(uid.N(999999)), tok, nil, nil)
	var page api.ItemPage
	e.expect(200, "GET", one+"/movies", tok, nil, &page)
	if !slices.Equal(itemIDs(page), []uid.ID{early, late, undated}) {
		t.Fatalf("by release date, undated last: %+v", itemIDs(page))
	}
	e.expect(200, "GET", one+"/movies?limit=1&offset=1", tok, nil, &page)
	if !slices.Equal(itemIDs(page), []uid.ID{late}) || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("paged: %+v", page)
	}

	// Any adult of the household manages it.
	other := e.householdAdult(e.plain, "Second", "second@example.test").Token
	e.expect(200, "PUT", one, other, map[string]any{"name": "Kite nights"}, &c)
	if c.Name != "Kite nights" || c.MovieCount != 3 {
		t.Fatalf("%+v", c)
	}
	e.expect(204, "DELETE", one+"/movies/"+itoa(undated), other, nil, nil)
	e.expect(404, "DELETE", one+"/movies/"+itoa(undated), other, nil, nil)

	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(early), tok, nil, &it)
	if it.Collections == nil || len(*it.Collections) != 1 || (*it.Collections)[0].Id != c.Id || (*it.Collections)[0].MovieCount != 2 {
		t.Fatalf("a movie's details name its collections: %+v", it.Collections)
	}
	var list api.ItemPage
	e.expect(200, "GET", "/items?kind=movie", tok, nil, &list)
	if list.Items[0].Collections != nil {
		t.Fatal("only a movie's details carry collections")
	}

	e.expect(204, "DELETE", one, other, nil, nil)
	e.expect(404, "GET", one, tok, nil, nil)
}

func TestTMDBCollectionsAreReadOnlyAndNeedTwoMovies(t *testing.T) {
	e := newEnv(t)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, t.TempDir()
	films := e.library("movies")
	a, b := e.film(films, "Gale Runner", "2004-01-01", "PG"), e.film(films, "Gale Runner Returns", "2006-01-01", "PG")
	saga := e.tmdbCollection(51, "Gale Runner Saga", a)
	one := "/collections/" + itoa(saga)
	e.expect(404, "GET", one, e.plain.token, nil, nil)
	var page api.CollectionPage
	e.expect(200, "GET", "/collections", e.plain.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("one movie is not a collection: %+v", page)
	}

	must(t, e.svc.Collections.Repo().SetTMDBMovie(b, &saga))
	for _, u := range []user{e.plain, e.admin} {
		var c api.Collection
		e.expect(200, "GET", one, u.token, nil, &c)
		if c.Source != api.CollectionSourceTmdb || c.TmdbId == nil || *c.TmdbId != 51 || c.HouseholdId != nil || c.Overview == nil || c.MovieCount != 2 {
			t.Fatalf("server-wide: %+v", c)
		}
		e.expect(403, "PUT", one, u.token, map[string]any{"name": "Mine"}, nil)
		e.expect(403, "DELETE", one, u.token, nil, nil)
		e.expect(403, "PUT", one+"/movies/"+itoa(e.movie(films, "Extra")), u.token, nil, nil)
		e.expect(403, "DELETE", one+"/movies/"+itoa(a), u.token, nil, nil)
		e.expect(403, "PUT", one+"/movies/"+itoa(a)+"/position", u.token, map[string]any{"position": 0}, nil)
		e.expect(403, "DELETE", one+"/order", u.token, nil, nil)
	}
	status, raw := e.do("GET", one+"/artwork/poster", e.plain.token, nil, nil)
	if status != 200 || string(raw) != "w500/saga.jpg" {
		t.Fatalf("its own poster: %d %s", status, raw)
	}
	e.expect(404, "GET", one+"/artwork/backdrop", e.plain.token, nil, nil)

	must(t, e.svc.Items.Repo().Delete(b))
	e.expect(404, "GET", one, e.plain.token, nil, nil)
}

func TestMoviesPlaylistHoldsACollection(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	late, early := e.film(films, "Kite Two", "2010-05-01", "PG"), e.film(films, "Kite One", "2008-01-01", "PG")
	solo := e.film(films, "Solo", "2000-01-01", "PG")
	e.file(late, 1000)
	e.file(early, 2000)
	tok := e.plain.token
	var c api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Kites"}, &c)
	for _, m := range []uid.ID{late, early} {
		e.expect(204, "PUT", "/collections/"+itoa(c.Id)+"/movies/"+itoa(m), tok, nil, nil)
	}
	var theirs api.Collection
	e.expect(201, "POST", "/collections", e.admin.token, map[string]any{"name": "Theirs"}, &theirs)
	base := "/profiles/" + itoa(e.plain.account.ID) + "/playlists"
	var pl, tv api.Playlist
	e.expect(201, "POST", base, tok, map[string]any{"name": "Night", "kind": "movies"}, &pl)
	e.expect(201, "POST", base, tok, map[string]any{"name": "Shows", "kind": "tv"}, &tv)
	one := base + "/" + itoa(pl.Id)

	e.expect(400, "POST", base+"/"+itoa(tv.Id)+"/entries", tok, map[string]any{"collectionId": c.Id}, nil)
	e.expect(400, "POST", one+"/entries", tok, map[string]any{}, nil)
	e.expect(400, "POST", one+"/entries", tok, map[string]any{"itemId": solo, "collectionId": c.Id}, nil)
	e.expect(404, "POST", one+"/entries", tok, map[string]any{"collectionId": theirs.Id}, nil)
	e.expect(404, "POST", one+"/entries", tok, map[string]any{"collectionId": uid.N(999999)}, nil)

	var en, single api.PlaylistEntry
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"collectionId": c.Id}, &en)
	if en.Kind != api.PlaylistEntryKindCollection || en.ItemId != nil || en.CollectionId == nil || *en.CollectionId != c.Id ||
		en.Collection == nil || en.Collection.Name != "Kites" || en.Item != nil || en.ItemCount != 2 || en.DurationMs != 3000 {
		t.Fatalf("a collection entry: %+v", en)
	}
	e.expect(201, "POST", one+"/entries", tok, map[string]any{"itemId": solo}, &single)
	var order api.PlaylistPlayOrderPage
	e.expect(200, "GET", one+"/play-order", tok, nil, &order)
	if items, entries := playIDs(order); !slices.Equal(items, []uid.ID{early, late, solo}) || !slices.Equal(entries, []uid.ID{en.Id, en.Id, single.Id}) ||
		order.Items[0].Kind != api.PlaylistEntryKindMovie || order.Items[0].Item == nil {
		t.Fatalf("a collection expands in its order: %+v", order)
	}
	e.expect(200, "GET", one, tok, nil, &pl)
	if pl.ItemCount != 3 || pl.DurationMs != 3000 || pl.ArtItemId == nil || *pl.ArtItemId != early {
		t.Fatalf("counts follow; art is the collection's first movie: %+v", pl)
	}

	// An entry only moves; the body names its own collection.
	e.expect(400, "PUT", one+"/entries/"+itoa(en.Id), tok, map[string]any{"itemId": solo}, nil)
	e.expect(400, "PUT", one+"/entries/"+itoa(en.Id), tok, map[string]any{"collectionId": theirs.Id}, nil)
	e.expect(200, "PUT", one+"/entries/"+itoa(en.Id), tok, map[string]any{"collectionId": c.Id}, &en)
	if en.Position != 1 {
		t.Fatalf("%+v", en)
	}

	// A new movie in the collection plays with no edit; deleting the
	// collection removes the entry.
	solo2 := e.film(films, "Kite Three", "2012-01-01", "PG")
	e.expect(204, "PUT", "/collections/"+itoa(c.Id)+"/movies/"+itoa(solo2), tok, nil, nil)
	e.expect(200, "GET", one+"/entries/"+itoa(en.Id), tok, nil, &en)
	if en.ItemCount != 3 {
		t.Fatalf("%+v", en)
	}
	e.expect(204, "DELETE", "/collections/"+itoa(c.Id), tok, nil, nil)
	e.expect(404, "GET", one+"/entries/"+itoa(en.Id), tok, nil, nil)
	var page api.PlaylistEntryPage
	e.expect(200, "GET", one+"/entries", tok, nil, &page)
	if len(page.Items) != 1 || *page.Items[0].ItemId != solo {
		t.Fatalf("%+v", page)
	}
}

// A custom collection keeps release order until a movie is moved; then its
// list, a playlist holding it and a movie's related collections follow the
// hand order, a movie added goes last, and a reset returns release order.
func TestCustomCollectionHandOrder(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	a, b, c := e.film(films, "Kite A", "2001-01-01", "PG"), e.film(films, "Kite B", "2002-01-01", "PG"), e.film(films, "Kite C", "2003-01-01", "PG")
	for _, m := range []uid.ID{a, b, c} {
		e.file(m, 1000)
	}
	tok := e.plain.token
	var col api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Kites"}, &col)
	one := "/collections/" + itoa(col.Id)
	for _, m := range []uid.ID{a, b, c} {
		e.expect(204, "PUT", one+"/movies/"+itoa(m), tok, nil, nil)
	}
	e.expect(200, "GET", one, tok, nil, &col)
	if *col.CustomOrder {
		t.Fatal("release order until moved")
	}
	list := func() []uid.ID {
		var page api.ItemPage
		e.expect(200, "GET", one+"/movies", tok, nil, &page)
		return itemIDs(page)
	}

	e.expect(400, "PUT", one+"/movies/"+itoa(c)+"/position", tok, map[string]any{"position": -1}, nil)
	e.expect(404, "PUT", one+"/movies/"+itoa(e.film(films, "Stray", "2004-01-01", "PG"))+"/position", tok, map[string]any{"position": 0}, nil)
	e.expect(200, "PUT", one+"/movies/"+itoa(c)+"/position", tok, map[string]any{"position": 0}, &col)
	if !*col.CustomOrder || !slices.Equal(list(), []uid.ID{c, a, b}) {
		t.Fatalf("%+v %v", col, list())
	}
	var page api.CollectionPage
	e.expect(200, "GET", "/collections", tok, nil, &page)
	if !*page.Items[0].CustomOrder {
		t.Fatalf("listed hand-ordered: %+v", page)
	}

	base := "/profiles/" + itoa(e.plain.account.ID) + "/playlists"
	var pl api.Playlist
	var en api.PlaylistEntry
	var order api.PlaylistPlayOrderPage
	e.expect(201, "POST", base, tok, map[string]any{"name": "Night", "kind": "movies"}, &pl)
	e.expect(201, "POST", base+"/"+itoa(pl.Id)+"/entries", tok, map[string]any{"collectionId": col.Id}, &en)
	e.expect(200, "GET", base+"/"+itoa(pl.Id)+"/play-order", tok, nil, &order)
	if items, _ := playIDs(order); !slices.Equal(items, []uid.ID{c, a, b}) {
		t.Fatalf("a playlist plays the hand order: %v", items)
	}
	e.expect(200, "GET", base+"/"+itoa(pl.Id), tok, nil, &pl)
	if pl.ArtItemId == nil || *pl.ArtItemId != c {
		t.Fatalf("its first movie is the hand order's: %+v", pl.ArtItemId)
	}
	if got := shelves(e.related(tok, b)); !slices.Equal(got[0], []uid.ID{col.Id, c, a}) {
		t.Fatalf("related follows the hand order: %v", got)
	}

	// Past the end, or no position, moves last; anyone of the household may.
	other := e.householdAdult(e.plain, "Second", "second@example.test").Token
	e.expect(200, "PUT", one+"/movies/"+itoa(c)+"/position", other, map[string]any{}, nil)
	if !slices.Equal(list(), []uid.ID{a, b, c}) {
		t.Fatalf("%v", list())
	}
	e.expect(200, "PUT", one+"/movies/"+itoa(a)+"/position", tok, map[string]any{"position": 99}, nil)
	late := e.film(films, "Kite 0", "1990-01-01", "PG")
	e.expect(204, "PUT", one+"/movies/"+itoa(late), tok, nil, nil)
	if !slices.Equal(list(), []uid.ID{b, c, a, late}) {
		t.Fatalf("a movie added to a hand-ordered collection goes last: %v", list())
	}
	e.expect(204, "DELETE", one+"/movies/"+itoa(c), tok, nil, nil)
	if !slices.Equal(list(), []uid.ID{b, a, late}) {
		t.Fatalf("removing keeps the rest in order: %v", list())
	}

	e.expect(200, "DELETE", one+"/order", tok, nil, &col)
	if *col.CustomOrder || !slices.Equal(list(), []uid.ID{late, a, b}) {
		t.Fatalf("reset: %+v %v", col, list())
	}
	e.expect(200, "DELETE", one+"/order", tok, nil, &col)
}

// A new collection takes the id its creator gives, else one minted; a
// malformed id is refused and one in use conflicts. The acting profile is
// recorded as its author and as who changed it last.
func TestCollectionsTakeAClientID(t *testing.T) {
	e := newEnv(t)
	tok, me := e.plain.token, e.plain.account.ID
	var minted api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"name": "Minted"}, &minted)
	if _, err := uid.Parse(minted.Id.String()); err != nil || minted.CreatedBy == nil || *minted.CreatedBy != me ||
		minted.ModifiedBy == nil || *minted.ModifiedBy != me {
		t.Fatalf("a minted v7 id, authored by the acting profile: %+v", minted)
	}
	id := uid.New()
	var c api.Collection
	e.expect(201, "POST", "/collections", tok, map[string]any{"id": id, "name": "Kites"}, &c)
	if c.Id != id {
		t.Fatalf("the client's id: %+v", c)
	}
	e.expect(200, "GET", "/collections/"+itoa(id), tok, nil, nil)
	for _, bad := range []any{"12", 12, strings.ToUpper(id.String()), "6ba7b810-9dad-41d1-80b4-00c04fd430c8"} {
		e.expect(400, "POST", "/collections", tok, map[string]any{"id": bad, "name": "Bad"}, nil)
	}
	e.expect(409, "POST", "/collections", tok, map[string]any{"id": id, "name": "Again"}, nil)

	e.expect(400, "PUT", "/collections/"+itoa(id), tok, map[string]any{"id": minted.Id, "name": "Kite nights"}, nil)
	time.Sleep(2 * time.Millisecond) // modifiedAt is kept to the millisecond
	var renamed api.Collection
	e.expect(200, "PUT", "/collections/"+itoa(id), tok, map[string]any{"id": id, "name": "Kite nights"}, &renamed)
	if renamed.Name != "Kite nights" || !renamed.ModifiedAt.After(c.ModifiedAt) || !renamed.CreatedAt.Equal(c.CreatedAt) {
		t.Fatalf("a rename moves modifiedAt only: %+v then %+v", c, renamed)
	}
}
