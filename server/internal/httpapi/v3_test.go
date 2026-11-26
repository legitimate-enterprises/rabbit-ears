package httpapi_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

func TestV3GuardRejectsBadInput(t *testing.T) {
	e := newEnv(t)
	e.expect(400, "GET", "/music/artists?letter=ab", e.plain.token, nil, nil)
	e.expect(400, "GET", "/music/letters", e.plain.token, nil, nil)
	e.expect(400, "GET", "/music/search", e.plain.token, nil, nil)
	e.expect(400, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "X", "kind": "song"}, nil)
	e.expect(400, "PUT", "/watch-together/1/state", e.plain.token, map[string]any{"playing": true, "positionMs": -1}, nil)
	e.expect(400, "GET", "/items?resolution=hd", e.plain.token, nil, nil)
}

func TestYouTubeLibraryStats(t *testing.T) {
	e := newEnv(t)
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "YouTube", "kind": "youtube"}, &lib)
	if lib.Stats.Channels == nil || *lib.Stats.Channels != 0 || lib.Stats.Videos == nil {
		t.Fatalf("stats %+v", lib.Stats)
	}
	var music api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "Music", "kind": "music"}, &music)
	if music.Stats.Albums == nil || music.Stats.Channels != nil {
		t.Fatalf("music library %+v", music.Stats)
	}
}

// Item-returning /items operations serve only the four catalog kinds.
func TestItemsServeCatalogKindsOnly(t *testing.T) {
	e := newEnv(t)
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "Music", "kind": "music"}, &lib)
	artist := &item.Item{LibraryID: lib.Id, Kind: item.Artist, ParsedTitle: "Artist"}
	must(t, e.svc.Items.Repo().Create(artist))
	id := "/items/" + itoa(artist.ID)
	var page api.ItemPage
	e.expect(200, "GET", "/items?libraryId="+itoa(lib.Id), e.plain.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("listed %+v", page.Items)
	}
	e.expect(200, "GET", "/items?resolution=uhd", e.plain.token, nil, &page)
	e.expect(404, "GET", id, e.plain.token, nil, nil)
	e.expect(404, "PUT", id, e.admin.token, map[string]any{"libraryId": lib.Id, "kind": "movie", "parsedTitle": "X"}, nil)
	e.expect(404, "PUT", id+"/metadata", e.admin.token, map[string]any{"status": "unmatched"}, nil)
	e.expect(404, "DELETE", id+"/metadata", e.admin.token, nil, nil)
	e.expect(404, "DELETE", id, e.admin.token, nil, nil)
	if _, err := e.svc.Items.Repo().Get(artist.ID); err != nil {
		t.Fatal("artist deleted through /items")
	}
}
