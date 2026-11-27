package httpapi_test

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestGenresCRUDAndKidAudience(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	var western api.Genre
	e.expect(201, "POST", "/genres", e.admin.token, map[string]any{"name": "Western"}, &western)
	e.expect(409, "POST", "/genres", e.admin.token, map[string]any{"name": "western"}, nil)
	e.expect(403, "POST", "/genres", e.plain.token, map[string]any{"name": "Noir"}, nil)
	e.expect(400, "POST", "/genres", e.admin.token, map[string]any{"name": ""}, nil)
	must(t, e.svc.Genres.Repo().SetForItem(c.g, genre.Remote, []genre.Genre{{Name: "Family"}}))
	must(t, e.svc.Genres.Repo().SetForItem(c.r, genre.Remote, []genre.Genre{{Name: "Crime"}}))
	must(t, e.svc.Genres.Repo().SetForItem(c.kidShow, genre.Remote, []genre.Genre{{Name: "Kids"}}))

	var page api.GenrePage
	e.expect(200, "GET", "/genres", e.plain.token, nil, &page)
	if len(page.Items) != 4 || page.Items[3].Id != western.Id {
		t.Fatalf("adults see every genre by name: %+v", page)
	}
	var item api.Item
	e.expect(200, "GET", "/items/"+itoa(c.r), e.plain.token, nil, &item)
	if item.Genres == nil || len(*item.Genres) != 1 || (*item.Genres)[0].Name != "Crime" {
		t.Fatalf("items carry their genres: %+v", item.Genres)
	}
	crime := (*item.Genres)[0].Id
	var items api.ItemPage
	e.expect(200, "GET", "/items", e.plain.token, nil, &items)
	for _, listed := range items.Items {
		var one api.Item
		e.expect(200, "GET", "/items/"+itoa(listed.Id), e.plain.token, nil, &one)
		one.Collections, one.Editions = nil, nil // only an item's own read names its collections and editions
		if !reflect.DeepEqual(listed, one) {
			t.Fatalf("a listing shows each item as its own read does:\n%+v\n%+v", listed, one)
		}
	}
	e.expect(200, "GET", "/items?genreId="+itoa(crime), e.plain.token, nil, &items)
	if len(items.Items) != 1 || items.Items[0].Id != c.r {
		t.Fatalf("genre filter: %+v", items)
	}

	k := e.kid(e.plain, "Kid")
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(200, "GET", "/genres", e.plain.token, nil, &page)
	if len(page.Items) != 2 || page.Items[0].Name != "Family" || page.Items[1].Name != "Kids" {
		t.Fatalf("a kid sees genres of visible items: %+v", page)
	}
	e.expect(404, "GET", "/genres/"+itoa(crime), e.plain.token, nil, nil)
	e.expect(200, "GET", "/items?genreId="+itoa(crime), e.plain.token, nil, &items)
	if len(items.Items) != 0 {
		t.Fatalf("%+v", items)
	}

	id := "/genres/" + itoa(western.Id)
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Westerns", "tmdbId": 37}, &western)
	if western.Name != "Westerns" || western.TmdbId == nil || *western.TmdbId != 37 {
		t.Fatalf("%+v", western)
	}
	e.expect(200, "GET", "/genres/"+itoa(crime), e.admin.token, nil, nil)
	e.expect(403, "DELETE", id, e.plain.token, nil, nil)
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.admin.token, nil, nil)
}

func TestPeopleCreditsSearchAndArtwork(t *testing.T) {
	e := newEnv(t)
	images := &fakeImages{}
	e.svc.People.Images, e.svc.People.ImageDir = images, t.TempDir()
	c := e.catalog()
	var ann, bob api.Person
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": "Ann Lee", "tmdbId": 1, "profilePath": "/ann.jpg"}, &ann)
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": "Bob Annan"}, &bob)
	e.expect(409, "POST", "/people", e.admin.token, map[string]any{"name": "Other", "tmdbId": 1}, nil)
	e.expect(403, "POST", "/people", e.plain.token, map[string]any{"name": "X"}, nil)

	credits := map[string]any{
		"cast": []map[string]any{{"personId": bob.Id, "character": "Hero"}, {"personId": ann.Id, "character": "Sidekick"}},
		"crew": []map[string]any{{"personId": ann.Id, "job": "Director", "department": "Directing"}},
	}
	var got api.ItemCredits
	e.expect(403, "PUT", "/items/"+itoa(c.g)+"/credits", e.plain.token, credits, nil)
	e.expect(200, "PUT", "/items/"+itoa(c.g)+"/credits", e.admin.token, credits, &got)
	e.expect(400, "PUT", "/items/"+itoa(c.r)+"/credits", e.admin.token, map[string]any{"cast": []map[string]any{{"personId": uid.N(999)}}, "crew": []any{}}, nil)
	e.expect(404, "PUT", "/items/"+itoa(uid.N(999))+"/credits", e.admin.token, credits, nil)
	e.expect(200, "PUT", "/items/"+itoa(c.r)+"/credits", e.admin.token, map[string]any{"cast": []map[string]any{{"personId": bob.Id}}, "crew": []any{}}, nil)
	e.expect(200, "GET", "/items/"+itoa(c.g)+"/credits", e.plain.token, nil, &got)
	if len(got.Cast) != 2 || got.Cast[0].Name != "Bob Annan" || *got.Cast[1].Character != "Sidekick" || len(got.Crew) != 1 ||
		*got.Crew[0].Job != "Director" || got.Crew[0].ProfilePath == nil {
		t.Fatalf("%+v", got)
	}
	var none api.ItemCredits
	e.expect(200, "GET", "/items/"+itoa(c.pg)+"/credits", e.plain.token, nil, &none)
	if none.Cast == nil || none.Crew == nil {
		t.Fatalf("empty credits are empty arrays: %+v", none)
	}

	var people api.PersonPage
	e.expect(200, "GET", "/people?q=ANN", e.plain.token, nil, &people)
	if len(people.Items) != 2 {
		t.Fatalf("%+v", people)
	}
	e.expect(200, "GET", "/people?limit=1", e.plain.token, nil, &people)
	if len(people.Items) != 1 || people.NextOffset == nil {
		t.Fatalf("paging: %+v", people)
	}
	var items api.ItemPage
	e.expect(200, "GET", "/items?personId="+itoa(bob.Id), e.plain.token, nil, &items)
	if len(items.Items) != 2 {
		t.Fatalf("person filter: %+v", items)
	}
	e.expect(200, "GET", "/items?q=r%20fi", e.plain.token, nil, &items)
	if len(items.Items) != 1 || items.Items[0].Id != c.r {
		t.Fatalf("title search: %+v", items)
	}

	art := "/people/" + itoa(ann.Id) + "/artwork"
	status, raw := e.do("GET", art, e.plain.token, nil, nil)
	if status != 200 || string(raw) != "w185/ann.jpg" {
		t.Fatalf("%d %s", status, raw)
	}
	req, err := http.NewRequest("GET", e.url+art, nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+e.plain.token)
	req.Header.Set("If-None-Match", `"w185/ann.jpg"`)
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	_ = res.Body.Close()
	if res.StatusCode != 304 || res.Header.Get("Cache-Control") != "private, max-age=86400" {
		t.Errorf("revalidation: %d %v", res.StatusCode, res.Header)
	}
	e.expect(404, "GET", "/people/"+itoa(bob.Id)+"/artwork", e.plain.token, nil, nil)

	// A kid sees only people and credits of titles under their ceilings.
	k := e.kid(e.plain, "Kid")
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(404, "GET", "/items/"+itoa(c.r)+"/credits", e.plain.token, nil, nil)
	e.expect(200, "GET", "/items?personId="+itoa(bob.Id), e.plain.token, nil, &items)
	if len(items.Items) != 1 || items.Items[0].Id != c.g {
		t.Fatalf("%+v", items)
	}
	var bea api.Person
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": "Bea Unseen", "profilePath": "/bea.jpg"}, &bea)
	e.expect(200, "GET", "/people", e.plain.token, nil, &people)
	if len(people.Items) != 2 {
		t.Fatalf("uncredited people are hidden from kids: %+v", people)
	}
	e.expect(404, "GET", "/people/"+itoa(bea.Id), e.plain.token, nil, nil)
	e.expect(404, "GET", "/people/"+itoa(bea.Id)+"/artwork", e.plain.token, nil, nil)

	id := "/people/" + itoa(bob.Id)
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Robert Annan"}, &bob)
	if bob.Name != "Robert Annan" || bob.TmdbId != nil {
		t.Fatalf("%+v", bob)
	}
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.admin.token, nil, nil)
	e.expect(200, "GET", "/items/"+itoa(c.g)+"/credits", e.admin.token, nil, &got)
	if len(got.Cast) != 1 {
		t.Fatalf("credits go with the person: %+v", got)
	}
}
