package httpapi_test

import (
	"net/http"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

func TestArtworkIsKeptForAYearUnderTheGenerationEveryResponseNames(t *testing.T) {
	e := newEnv(t)
	e.svc.ArtworkSettle = 0
	e.svc.Items.Images, e.svc.Items.ImageDir = &fakeImages{}, t.TempDir()
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "Movies", "kind": "movies"}, &lib)
	items := e.svc.Items.Repo()
	movie := &item.Item{LibraryID: lib.Id, Kind: item.Movie, ParsedTitle: "m"}
	must(t, items.Create(movie))
	must(t, items.SetMetadata(&item.Metadata{ItemID: movie.ID, Status: item.Matched, PosterPath: "/a.jpg"}))
	get := func(path, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", e.url+path, nil)
		must(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(t, err)
		_ = res.Body.Close()
		return res
	}
	art := "/items/" + itoa(movie.ID) + "/artwork/poster"
	gen := get("/health", "").Header.Get("Artwork-Generation")
	if gen == "" || get(art, "bad").Header.Get("Artwork-Generation") != gen {
		t.Fatalf("every answer, a refusal too, names the generation: %q", gen)
	}
	if res := get(art, e.plain.token); res.Header.Get("Cache-Control") != "private, max-age=86400" {
		t.Errorf("without a generation an image is revalidated daily: %v", res.Header)
	}
	res := get(art+"?v="+gen, e.plain.token)
	if res.Header.Get("Cache-Control") != "private, max-age=31536000, immutable" || res.Header.Get("ETag") != `"w500/a.jpg"` {
		t.Errorf("under a generation it is kept: %v", res.Header)
	}
	must(t, items.SetMetadata(&item.Metadata{ItemID: movie.ID, Status: item.Matched, PosterPath: "/b.jpg"}))
	if next := get("/health", "").Header.Get("Artwork-Generation"); next == gen {
		t.Error("a replaced image moves the generation")
	}
	e.expect(400, "GET", art+"?v=x", e.plain.token, nil, nil)
}
