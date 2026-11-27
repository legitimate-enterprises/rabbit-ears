package httpapi_test

import (
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// related answers getItemRelated for the token.
func (e *env) related(token string, id uid.ID) api.ItemRelated {
	e.t.Helper()
	var out api.ItemRelated
	e.expect(200, "GET", "/items/"+itoa(id)+"/related", token, nil, &out)
	return out
}

// credit gives the movie its cast in billing order and its directors.
func (e *env) credit(movie uid.ID, cast []uid.ID, directors ...uid.ID) {
	e.t.Helper()
	body := map[string]any{"cast": []map[string]any{}, "crew": []map[string]any{}}
	for _, p := range cast {
		body["cast"] = append(body["cast"].([]map[string]any), map[string]any{"personId": p})
	}
	for _, p := range directors {
		body["crew"] = append(body["crew"].([]map[string]any), map[string]any{"personId": p, "job": "Director", "department": "Directing"})
	}
	e.expect(200, "PUT", "/items/"+itoa(movie)+"/credits", e.admin.token, body, nil)
}

func (e *env) person(name string) uid.ID {
	e.t.Helper()
	var p api.Person
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": name}, &p)
	return p.Id
}

func relatedIDs(items []api.Item) []uid.ID {
	out := []uid.ID{}
	for _, it := range items {
		out = append(out, it.Id)
	}
	return out
}

func neighbor(it *api.Item) uid.ID {
	if it == nil {
		return uid.ID{}
	}
	return it.Id
}

// shelves is each related collection's id and its movies' ids.
func shelves(r api.ItemRelated) (out [][]uid.ID) {
	for _, c := range r.Collections {
		out = append(out, append([]uid.ID{c.Collection.Id}, relatedIDs(c.Items)...))
	}
	return out
}

func TestRelatedMovies(t *testing.T) {
	e := newEnv(t)
	films := e.library("movies")
	origin := e.film(films, "Origin", "2000-05-01", "PG")
	prequel, sequel, finale := e.film(films, "Prequel", "1998-05-01", "PG"), e.film(films, "Sequel", "2003-05-01", "R"), e.film(films, "Finale", "2006-05-01", "G")
	saga := e.tmdbCollection(10, "Saga", finale, sequel, origin, prequel)
	pal := e.film(films, "Pal", "2010-05-01", "PG")
	var favorites api.Collection
	e.expect(201, "POST", "/collections", e.plain.token, map[string]any{"name": "Favorites"}, &favorites)
	for _, m := range []uid.ID{origin, sequel, pal} {
		e.expect(204, "PUT", "/collections/"+itoa(favorites.Id)+"/movies/"+itoa(m), e.plain.token, nil, nil)
	}

	// Sharing cast and crew relates nothing.
	lead := e.person("Lead")
	e.credit(origin, []uid.ID{lead}, lead)
	topBilled := e.film(films, "Top billed", "2001-05-01", "PG")
	e.credit(topBilled, []uid.ID{lead}, lead)
	stranger := e.film(films, "Stranger", "2011-05-01", "PG")
	tv := e.library("tv")
	show, _ := e.show(tv, "Lead's show", "TV-G")
	e.credit(show, []uid.ID{lead})

	got := e.related(e.plain.token, origin)
	if want := [][]uid.ID{{saga, prequel, sequel, finale}, {favorites.Id, sequel, pal}}; !slices.EqualFunc(shelves(got), want, slices.Equal) {
		t.Fatalf("the TMDB collection first, each with its own other films: %v, want %v", shelves(got), want)
	}
	if got.Similar == nil || len(got.Similar) != 0 {
		t.Fatalf("similar is always empty: %v", relatedIDs(got.Similar))
	}
	if neighbor(got.Previous) != prequel || neighbor(got.Next) != sequel || got.Next.Metadata == nil || *got.Next.Metadata.Title != "Sequel" {
		t.Fatalf("franchise neighbors: %v %v", neighbor(got.Previous), neighbor(got.Next))
	}
	if got := e.related(e.plain.token, prequel); !neighbor(got.Previous).IsZero() || neighbor(got.Next) != origin {
		t.Fatalf("nothing before the first film: %+v", got)
	}
	if got := e.related(e.plain.token, pal); !neighbor(got.Previous).IsZero() || !neighbor(got.Next).IsZero() || !slices.EqualFunc(shelves(got), [][]uid.ID{{favorites.Id, origin, sequel}}, slices.Equal) {
		t.Fatalf("a custom collection gives no neighbors: %+v", got)
	}
	if got := e.related(e.plain.token, stranger); len(got.Collections) != 0 || len(got.Similar) != 0 {
		t.Fatalf("a film in no collection has nothing related: %+v", got)
	}
	if got := e.related(e.admin.token, origin); !slices.EqualFunc(shelves(got), [][]uid.ID{{saga, prequel, sequel, finale}}, slices.Equal) {
		t.Fatalf("another household's collection is not listed: %v", shelves(got))
	}
	if got := e.related(e.plain.token, show); got.Collections == nil || got.Similar == nil || len(got.Collections)+len(got.Similar) != 0 || got.Previous != nil || got.Next != nil {
		t.Fatalf("a show has nothing related: %+v", got)
	}
	var marathon api.Collection
	e.expect(201, "POST", "/collections", e.plain.token, map[string]any{"name": "Marathon"}, &marathon)
	var reels []uid.ID
	for i := range 13 {
		reels = append(reels, e.film(films, "Reel "+itoa(int64(i)), itoa(int64(1980+i))+"-01-01", "PG"))
		e.expect(204, "PUT", "/collections/"+itoa(marathon.Id)+"/movies/"+itoa(reels[i]), e.plain.token, nil, nil)
	}
	if got, want := shelves(e.related(e.plain.token, reels[6])), [][]uid.ID{slices.Concat([]uid.ID{marathon.Id}, reels[1:6], reels[7:12])}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Fatalf("the ten nearest others: %v, want %v", got, want)
	}

	// A kid by rating: what is above their ceiling is absent, a hidden
	// neighbor is skipped, and asking about a hidden film is not found.
	k := e.kid(e.plain, "Kid")
	kid := e.kidDevice(e.plain, k.Id)
	got = e.related(kid, origin)
	if want := [][]uid.ID{{saga, prequel, finale}, {favorites.Id, pal}}; !slices.EqualFunc(shelves(got), want, slices.Equal) {
		t.Fatalf("kid collections %v, want %v", shelves(got), want)
	}
	if neighbor(got.Previous) != prequel || neighbor(got.Next) != finale {
		t.Fatalf("kid neighbors: %v %v", neighbor(got.Previous), neighbor(got.Next))
	}
	e.expect(404, "GET", "/items/"+itoa(sequel)+"/related", kid, nil, nil)

	// A kid on an allow list sees only what is on it; a collection left
	// with nothing they may see is left out.
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id), e.plain.token, map[string]any{"name": "Kid", "birthday": "2018-05-01", "filterMode": "allow_list"}, nil)
	for _, m := range []uid.ID{origin, prequel, topBilled} {
		e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/allowed-items/"+itoa(m), e.plain.token, nil, nil)
	}
	got = e.related(kid, origin)
	if !slices.EqualFunc(shelves(got), [][]uid.ID{{saga, prequel}}, slices.Equal) || len(got.Similar) != 0 ||
		neighbor(got.Previous) != prequel || !neighbor(got.Next).IsZero() {
		t.Fatalf("allow list: %v %v %v %v", shelves(got), relatedIDs(got.Similar), neighbor(got.Previous), neighbor(got.Next))
	}

	// An adult hiding kids' content browses related titles without it.
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/preferences", e.plain.token, map[string]any{"hideKidsContent": true}, nil)
	if got := e.related(e.plain.token, origin); !slices.EqualFunc(shelves(got), [][]uid.ID{{saga, prequel, sequel}, {favorites.Id, sequel, pal}}, slices.Equal) {
		t.Fatalf("kids' content stays out of an adult's browsing: %v", shelves(got))
	}
	e.expect(401, "GET", "/items/"+itoa(origin)+"/related", "", nil, nil)
	e.expect(404, "GET", "/items/"+itoa(uid.N(999999))+"/related", e.plain.token, nil, nil)
}

func TestRelatedEpisodes(t *testing.T) {
	e := newEnv(t)
	tv := e.library("tv")
	show, s1e1 := e.show(tv, "Alpha", "TV-PG")
	var page api.ItemPage
	e.expect(200, "GET", "/items?parentId="+itoa(show), e.admin.token, nil, &page)
	s1 := page.Items[0].Id
	specials := e.child(tv, show, "season", 0)
	s0e1 := e.child(tv, specials, "episode", 1)
	s2 := e.child(tv, show, "season", 2)
	s2e1 := e.child(tv, s2, "episode", 1)
	s1e2 := e.child(tv, s1, "episode", 2)
	e.file(s1e2, 60000)
	e.gone(s1e2)
	tok := e.plain.token

	for _, c := range []struct{ ep, prev, next uid.ID }{{s1e1, uid.ID{}, s1e2}, {s1e2, s1e1, s2e1}, {s2e1, s1e2, s0e1}, {s0e1, s2e1, uid.ID{}}} {
		got := e.related(tok, c.ep)
		if neighbor(got.Previous) != c.prev || neighbor(got.Next) != c.next || len(got.Collections)+len(got.Similar) != 0 {
			t.Fatalf("episode %s: %v %v, want %v %v (specials last, an unavailable episode kept)", c.ep, neighbor(got.Previous), neighbor(got.Next), c.prev, c.next)
		}
	}
	if got := e.related(tok, s1); got.Previous != nil || got.Next != nil {
		t.Fatalf("a season has no neighbors: %+v", got)
	}

	k := e.kid(e.plain, "Kid")
	e.expect(200, "PUT", "/profiles/"+itoa(k.Id), tok, map[string]any{"name": "Kid", "birthday": "2018-05-01", "filterMode": "allow_list"}, nil)
	for _, ep := range []uid.ID{s1e1, s2e1} {
		e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/allowed-items/"+itoa(ep), tok, nil, nil)
	}
	kid := e.kidDevice(e.plain, k.Id)
	if got := e.related(kid, s2e1); neighbor(got.Previous) != s1e1 || !neighbor(got.Next).IsZero() {
		t.Fatalf("a kid's neighbors skip what they may not see: %v %v", neighbor(got.Previous), neighbor(got.Next))
	}
	e.expect(404, "GET", "/items/"+itoa(s1e2)+"/related", kid, nil, nil)
}
