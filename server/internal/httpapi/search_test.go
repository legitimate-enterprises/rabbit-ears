package httpapi_test

import (
	"net/url"
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func (e *env) search(token, q string) api.SearchResult {
	e.t.Helper()
	var out api.SearchResult
	e.expect(200, "GET", "/search?q="+url.QueryEscape(q), token, nil, &out)
	return out
}

func found(items []api.Item) []uid.ID { return itemIDs(api.ItemPage{Items: items}) }

func TestUniversalSearch(t *testing.T) {
	e := newEnv(t)
	m := e.music()
	tb := e.youtubeLibrary()
	movies := e.library("movies")
	twilight := e.film(movies, "A Twilight", "2001-01-01", "R")
	years := e.film(movies, "Light Years", "2001-01-01", "G")
	show, ep := e.show(e.library("tv"), "Lighthouse", "TV-MA")
	must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: ep, Status: item.Matched, Title: "Lights Out"}))
	var foot api.Person
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": "Gordon Lightfoot"}, &foot)

	got := e.search(e.plain.token, "light")
	if !slices.Equal(found(got.Movies), []uid.ID{years, twilight}) || !slices.Equal(found(got.Shows), []uid.ID{show}) || !slices.Equal(found(got.Episodes), []uid.ID{ep}) {
		t.Fatalf("titles, a prefix first: %+v %+v %+v", got.Movies, got.Shows, got.Episodes)
	}
	if len(got.People) != 1 || got.People[0].Id != foot.Id || len(got.Albums) != 1 || got.Albums[0].Id != m.album.Id ||
		got.Artists == nil || len(got.Artists) != 0 || got.Songs == nil || got.Channels == nil || got.Videos == nil {
		t.Fatalf("%+v", got)
	}
	got = e.search(e.plain.token, "e")
	if len(got.Artists) != 3 || len(got.Songs) != 3 || len(got.Channels) != 2 || len(got.Videos) != 3 {
		t.Fatalf("music and YouTube: %+v", got)
	}
	got = e.search(e.plain.token, "call")
	if len(got.Songs) != 1 || got.Songs[0].Id != m.songs[2].Id {
		t.Fatalf("%+v", got.Songs)
	}
	got = e.search(e.plain.token, "bench")
	if len(got.Videos) != 1 || got.Videos[0].Id != tb.newer || len(got.Channels) != 1 || got.Channels[0].Id != tb.channel {
		t.Fatalf("%+v %+v", got.Videos, got.Channels)
	}
	var capped api.SearchResult
	e.expect(200, "GET", "/search?q=light&limit=1", e.plain.token, nil, &capped)
	if !slices.Equal(found(capped.Movies), []uid.ID{years}) {
		t.Fatalf("%+v", capped.Movies)
	}
	e.expect(400, "GET", "/search?q=", e.plain.token, nil, nil)
	e.expect(400, "GET", "/search?q=x&limit=51", e.plain.token, nil, nil)
	e.expect(401, "GET", "/search?q=x", "", nil, nil)

	// A kid finds only what their filter lets them see.
	k := e.kid(e.plain, "Kid")
	kidToken := e.kidDevice(e.plain, k.Id)
	got = e.search(kidToken, "light")
	if !slices.Equal(found(got.Movies), []uid.ID{years}) || len(got.Shows)+len(got.Episodes)+len(got.People) != 0 || len(got.Albums) != 1 {
		t.Fatalf("%+v", got)
	}
	got = e.search(kidToken, "bench")
	if len(got.Videos)+len(got.Channels) != 0 {
		t.Fatalf("%+v", got)
	}
}
