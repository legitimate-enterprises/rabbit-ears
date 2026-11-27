package httpapi_test

import (
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// sorted is each item as parsed title=sort title, in the order given.
func sorted(items []api.Item) []string {
	out := []string{}
	for _, it := range items {
		s := "-"
		if it.SortTitle != nil {
			s = *it.SortTitle
		}
		out = append(out, it.ParsedTitle+"="+s)
	}
	return out
}

// Movies and shows list by the sort title they carry, which clients file
// letters by ("The Matrix" under M, "A Quiet Place" under Q, "Die Hard"
// under D), and which follows the title shown; seasons and episodes carry
// none.
func TestItemsCarryAndListBySortTitle(t *testing.T) {
	e := newEnv(t)
	movies, tv := e.library("movies"), e.library("tv")
	var made api.Item
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": movies, "kind": "movie", "parsedTitle": "The Matrix"}, &made)
	if got := sorted([]api.Item{made}); !slices.Equal(got, []string{"The Matrix=Matrix"}) {
		t.Errorf("created %q", got)
	}
	quiet, die := e.movie(movies, "A Quiet Place"), e.movie(movies, "Die Hard")
	list := func(query string) []string {
		t.Helper()
		var page api.ItemPage
		e.expect(200, "GET", "/items?"+query, e.plain.token, nil, &page)
		return sorted(page.Items)
	}
	if got, want := list("kind=movie"), []string{"Die Hard=Die Hard", "The Matrix=Matrix", "A Quiet Place=Quiet Place"}; !slices.Equal(got, want) {
		t.Errorf("movies %q, want %q", got, want)
	}

	var got api.Item
	e.expect(200, "PUT", "/items/"+itoa(quiet)+"/metadata", e.admin.token, map[string]any{"status": "matched", "title": "An Education"}, &got)
	if s := sorted([]api.Item{got}); !slices.Equal(s, []string{"A Quiet Place=Education"}) {
		t.Errorf("matched by hand %q", s)
	}
	e.expect(200, "PUT", "/items/"+itoa(die), e.admin.token, map[string]any{"libraryId": movies, "kind": "movie", "parsedTitle": "The Abyss"}, &got)
	if s := sorted([]api.Item{got}); !slices.Equal(s, []string{"The Abyss=Abyss"}) {
		t.Errorf("renamed %q", s)
	}
	if got, want := list("kind=movie"), []string{"The Abyss=Abyss", "A Quiet Place=Education", "The Matrix=Matrix"}; !slices.Equal(got, want) {
		t.Errorf("movies %q, want %q", got, want)
	}
	if got, want := list("q=the"), []string{"The Abyss=Abyss", "The Matrix=Matrix"}; !slices.Equal(got, want) {
		t.Errorf("search matches the whole title: %q, want %q", got, want)
	}

	shows := map[uid.ID]string{}
	for _, title := range []string{"The Wire", "Archer", "A Bear"} {
		id, _ := e.show(tv, title, "TV-14")
		shows[id] = title
	}
	var progress []string
	for _, p := range e.showProgress(e.plain.account.ID, "").Items {
		progress = append(progress, shows[p.ShowId])
	}
	if want := []string{"Archer", "A Bear", "The Wire"}; !slices.Equal(progress, want) {
		t.Errorf("show progress %q, want %q", progress, want)
	}
	var page api.ItemPage
	e.expect(200, "GET", "/items?kind=season", e.plain.token, nil, &page)
	if got := sorted(page.Items); len(got) != 3 || got[0] != "A Bear=-" {
		t.Errorf("seasons %q", got)
	}
}
