package httpapi_test

import (
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

func TestLibrariesCRUDAndPaging(t *testing.T) {
	e := newEnv(t)
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "Films", "kind": "movies"}, &lib)
	if lib.ScanHour != 3 || !lib.WatchFiles || len(lib.Folders) != 0 {
		t.Fatalf("defaults not applied: %+v", lib)
	}
	id := "/libraries/" + itoa(lib.Id)
	e.expect(409, "POST", "/libraries", e.admin.token, map[string]any{"name": "Films", "kind": "tv"}, nil)

	var f api.Folder
	e.expect(201, "POST", id+"/folders", e.admin.token, map[string]any{"path": "/media/films"}, &f)
	e.expect(409, "POST", id+"/folders", e.admin.token, map[string]any{"path": "/media/films"}, nil)
	e.expect(400, "POST", id+"/folders", e.admin.token, map[string]any{"path": "relative"}, nil)
	e.expect(404, "POST", "/libraries/999/folders", e.admin.token, map[string]any{"path": "/x"}, nil)

	e.expect(200, "PUT", id, e.admin.token, map[string]any{"name": "Movies", "kind": "movies", "scanHour": 5, "watchFiles": false}, &lib)
	if lib.Name != "Movies" || lib.ScanHour != 5 || lib.WatchFiles {
		t.Fatalf("%+v", lib)
	}
	e.expect(200, "GET", id, e.admin.token, nil, &lib)
	if len(lib.Folders) != 1 || lib.Folders[0].Path != "/media/films" {
		t.Fatalf("folders: %+v", lib.Folders)
	}
	// Server paths are the admin's.
	e.expect(200, "GET", id, e.plain.token, nil, &lib)
	if len(lib.Folders) != 0 {
		t.Fatalf("folders shown to a viewer: %+v", lib.Folders)
	}
	e.expect(403, "GET", id+"/folders", e.plain.token, nil, nil)
	e.expect(403, "GET", id+"/folders/"+itoa(f.Id), e.plain.token, nil, nil)
	e.expect(200, "GET", id+"/folders/"+itoa(f.Id), e.admin.token, nil, nil)
	e.expect(400, "PUT", id, e.admin.token, map[string]any{"name": "Movies", "kind": "movies", "scanHour": 24}, nil)
	e.expect(404, "DELETE", id+"/folders/999", e.admin.token, nil, nil)
	e.expect(204, "DELETE", id+"/folders/"+itoa(f.Id), e.admin.token, nil, nil)

	for _, n := range []string{"B", "C"} {
		e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": n, "kind": "tv"}, nil)
	}
	var page api.LibraryPage
	e.expect(200, "GET", "/libraries?limit=2", e.plain.token, nil, &page)
	if len(page.Items) != 2 || page.NextOffset == nil || *page.NextOffset != 2 {
		t.Fatalf("first page: %+v", page)
	}
	page = api.LibraryPage{}
	e.expect(200, "GET", "/libraries?limit=2&offset=2", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.NextOffset != nil {
		t.Fatalf("last page: %+v", page)
	}

	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.admin.token, nil, nil)
	e.expect(404, "DELETE", id, e.admin.token, nil, nil)
}

// A folder's path can change; every change to a library's folders asks for
// a scan of that library, which reads the new path and marks what the old
// one held missing.
func TestLibraryFolderChangesRequestAScan(t *testing.T) {
	e := newEnv(t)
	lib, other := e.library("movies"), e.library("tv")
	id := "/libraries/" + itoa(lib)
	var scan, otherScan api.Task
	e.expect(201, "POST", "/tasks", e.admin.token, map[string]any{"name": "Scan", "kind": "scan:" + itoa(lib)}, &scan)
	e.expect(201, "POST", "/tasks", e.admin.token, map[string]any{"name": "Scan other", "kind": "scan:" + itoa(other)}, &otherScan)
	requested := func(step string) {
		t.Helper()
		var tk api.Task
		e.expect(200, "GET", "/tasks/"+itoa(scan.Id), e.admin.token, nil, &tk)
		if tk.RunRequestedAt == nil {
			t.Fatalf("%s: no scan requested", step)
		}
		must(t, e.svc.Tasks.Repo().Reschedule(scan.Id, nil, nil))
	}

	var f, taken api.Folder
	e.expect(201, "POST", id+"/folders", e.admin.token, map[string]any{"path": "/media/wrong"}, &f)
	requested("add")
	e.expect(201, "POST", "/libraries/"+itoa(other)+"/folders", e.admin.token, map[string]any{"path": "/media/tv"}, &taken)
	must(t, e.svc.Tasks.Repo().Reschedule(otherScan.Id, nil, nil))

	folder := id + "/folders/" + itoa(f.Id)
	var got api.Folder
	e.expect(200, "PUT", folder, e.admin.token, map[string]any{"path": "/media/films"}, &got)
	if got.Id != f.Id || got.LibraryId != lib || got.Path != "/media/films" {
		t.Fatalf("%+v", got)
	}
	requested("change")
	e.expect(200, "GET", folder, e.admin.token, nil, &got)
	if got.Path != "/media/films" {
		t.Fatalf("stored path: %+v", got)
	}
	e.expect(409, "PUT", folder, e.admin.token, map[string]any{"path": "/media/tv"}, nil)
	e.expect(400, "PUT", folder, e.admin.token, map[string]any{"path": "relative"}, nil)
	e.expect(400, "PUT", folder, e.admin.token, map[string]any{"path": "/"}, nil)
	e.expect(404, "PUT", "/libraries/"+itoa(other)+"/folders/"+itoa(f.Id), e.admin.token, map[string]any{"path": "/media/x"}, nil)
	e.expect(404, "PUT", id+"/folders/999", e.admin.token, map[string]any{"path": "/media/x"}, nil)
	e.expect(403, "PUT", folder, e.plain.token, map[string]any{"path": "/media/x"}, nil)
	e.expect(401, "PUT", folder, "", map[string]any{"path": "/media/x"}, nil)

	e.expect(204, "DELETE", folder, e.admin.token, nil, nil)
	requested("remove")
	var tk api.Task
	e.expect(200, "GET", "/tasks/"+itoa(otherScan.Id), e.admin.token, nil, &tk)
	if tk.RunRequestedAt != nil {
		t.Fatal("another library's scan requested")
	}
}

func (e *env) library(kind string) int64 {
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "L" + kind, "kind": kind}, &lib)
	return lib.Id
}

func (e *env) movie(lib int64, title string) int64 {
	var it api.Item
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": lib, "kind": "movie", "parsedTitle": title}, &it)
	return it.Id
}

func TestItemsCRUDMetadataAndFilters(t *testing.T) {
	e := newEnv(t)
	tv := e.library("tv")
	var show, season, ep api.Item
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "kind": "show", "parsedTitle": "Show", "path": "/tv/Show"}, &show)
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "parentId": show.Id, "kind": "season", "parsedTitle": "Show", "seasonNumber": 1}, &season)
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "parentId": season.Id, "kind": "episode", "parsedTitle": "Show", "episodeNumber": 1}, &ep)
	e.expect(409, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "parentId": season.Id, "kind": "episode", "parsedTitle": "Show", "episodeNumber": 1}, nil)
	e.expect(400, "POST", "/items", e.admin.token, map[string]any{"libraryId": 999, "kind": "movie", "parsedTitle": "X"}, nil)
	e.expect(400, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "kind": "season", "parsedTitle": "No parent"}, nil)

	var page api.ItemPage
	e.expect(200, "GET", "/items?parentId="+itoa(season.Id), e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != ep.Id {
		t.Fatalf("%+v", page)
	}
	page = api.ItemPage{}
	e.expect(200, "GET", "/items?libraryId="+itoa(tv)+"&kind=show", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != show.Id || page.Items[0].Path == nil || *page.Items[0].Path != "Show" {
		t.Fatalf("a viewer sees only the base name: %+v", page)
	}
	page = api.ItemPage{}
	e.expect(200, "GET", "/items?libraryId="+itoa(tv)+"&kind=show", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Path == nil || *page.Items[0].Path != "/tv/Show" {
		t.Fatalf("an admin sees the whole path: %+v", page)
	}
	e.expect(400, "GET", "/items?kind=album", e.plain.token, nil, nil)

	id := "/items/" + itoa(show.Id)
	var got api.Item
	e.expect(200, "PUT", id+"/metadata", e.admin.token, map[string]any{"status": "matched", "providerId": 1399, "title": "The Show",
		"releaseDate": "2011-04-17", "contentRating": "TV-MA"}, &got)
	if got.Metadata == nil || got.Metadata.Title == nil || *got.Metadata.Title != "The Show" || got.Metadata.ReleaseDate == nil {
		t.Fatalf("%+v", got.Metadata)
	}
	e.expect(400, "PUT", id+"/metadata", e.admin.token, map[string]any{"status": "maybe"}, nil)
	got = api.Item{}
	e.expect(200, "GET", id, e.plain.token, nil, &got)
	if got.Metadata == nil || *got.Metadata.ProviderId != 1399 {
		t.Fatalf("%+v", got)
	}
	e.expect(204, "DELETE", id+"/metadata", e.admin.token, nil, nil)
	e.expect(404, "DELETE", id+"/metadata", e.admin.token, nil, nil)
	got = api.Item{}
	e.expect(200, "GET", id, e.plain.token, nil, &got)
	if got.Metadata != nil {
		t.Fatal("metadata must be gone")
	}

	// Only the file-derived fields are written, so the rest must not change.
	e.expect(400, "PUT", id, e.admin.token, map[string]any{"libraryId": tv, "kind": "movie", "parsedTitle": "Renamed"}, nil)
	e.expect(400, "PUT", id, e.admin.token, map[string]any{"libraryId": e.library("movies"), "kind": "show", "parsedTitle": "Renamed"}, nil)
	e.expect(400, "PUT", "/items/"+itoa(ep.Id), e.admin.token, map[string]any{"libraryId": tv, "parentId": show.Id, "kind": "episode", "parsedTitle": "Show"}, nil)
	e.expect(200, "PUT", id, e.admin.token, map[string]any{"libraryId": tv, "kind": "show", "parsedTitle": "Renamed", "parsedYear": 2011}, &got)
	if got.ParsedTitle != "Renamed" || got.ParsedYear == nil || *got.ParsedYear != 2011 {
		t.Fatalf("%+v", got)
	}
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", "/items/"+itoa(ep.Id), e.plain.token, nil, nil)
}

func TestMediaFilesCRUD(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Film")
	body := map[string]any{"itemId": m, "path": "/films/film.mkv", "sizeBytes": 1000, "modifiedAt": "2026-01-02T03:04:05Z", "container": "matroska"}
	var f api.MediaFile
	e.expect(201, "POST", "/media-files", e.admin.token, body, &f)
	if f.Tracks == nil || len(f.Tracks) != 0 || f.Container == nil || *f.Container != "matroska" {
		t.Fatalf("%+v", f)
	}
	e.expect(409, "POST", "/media-files", e.admin.token, body, nil)
	e.expect(400, "POST", "/media-files", e.admin.token, map[string]any{"itemId": m, "path": "/x.mkv"}, nil)

	var page api.MediaFilePage
	e.expect(200, "GET", "/media-files?itemId="+itoa(m), e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != f.Id {
		t.Fatalf("%+v", page)
	}
	e.expect(400, "GET", "/media-files", e.plain.token, nil, nil)

	id := "/media-files/" + itoa(f.Id)
	body["sizeBytes"] = 2000
	e.expect(200, "PUT", id, e.admin.token, body, &f)
	if f.SizeBytes != 2000 {
		t.Fatalf("%+v", f)
	}
	e.expect(200, "GET", id, e.plain.token, nil, &f)
	if f.Path != "film.mkv" {
		t.Fatalf("a viewer sees only the base name: %+v", f)
	}
	e.expect(200, "GET", id, e.admin.token, nil, &f)
	if f.Path != "/films/film.mkv" {
		t.Fatalf("an admin sees the whole path: %+v", f)
	}
	e.expect(204, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id, e.plain.token, nil, nil)
}

func TestLibraryStats(t *testing.T) {
	e := newEnv(t)
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "Films", "kind": "movies"}, &lib)
	zero := api.LibraryStats{}
	if lib.Stats != zero {
		t.Fatalf("empty library: %+v", lib.Stats)
	}
	items, files := e.svc.Items.Repo(), e.svc.MediaFiles.Repo()
	for i, size := range []int64{1_000, 4_000_000_000} {
		m := &item.Item{LibraryID: lib.Id, Kind: item.Movie, ParsedTitle: "M" + itoa(int64(i))}
		must(t, items.Create(m))
		must(t, files.CreateFile(&mediafile.File{ItemID: m.ID, Path: "/films/" + itoa(int64(i)) + ".mkv", SizeBytes: size}))
		if i == 0 {
			must(t, items.SetMetadata(&item.Metadata{ItemID: m.ID, Status: item.Matched}))
		}
	}
	want := api.LibraryStats{Movies: 2, Files: 2, Bytes: 4_000_001_000, Unmatched: 1}
	e.expect(200, "GET", "/libraries/"+itoa(lib.Id), e.admin.token, nil, &lib)
	if lib.Stats != want {
		t.Fatalf("GET: %+v", lib.Stats)
	}
	var page api.LibraryPage
	e.expect(200, "GET", "/libraries", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Stats != want {
		t.Fatalf("list: %+v", page.Items)
	}
}

// The 4K chip: resolution=uhd keeps items, and shows whose episodes, with a
// video track of at least 3200 by 2000.
func TestItemsFilterByUHD(t *testing.T) {
	e := newEnv(t)
	movies, tv := e.library("movies"), e.library("tv")
	addFile := func(id int64, w, h int) {
		f := &mediafile.File{ItemID: id, Path: "/m/" + itoa(id), SizeBytes: 1, ModifiedAt: time.Now()}
		must(t, e.svc.MediaFiles.Repo().CreateFile(f))
		must(t, e.svc.MediaFiles.Repo().ReplaceTracks(f.ID, []*mediafile.Track{{MediaFileID: f.ID, Kind: mediafile.Video, Codec: "hevc", Width: &w, Height: &h}}))
	}
	sdr, uhd := e.movie(movies, "Full HD"), e.movie(movies, "Ultra HD")
	addFile(sdr, 1920, 1080)
	addFile(uhd, 3840, 2160)
	show, ep := e.show(tv, "Ultra Show", "TV-G")
	addFile(ep, 3840, 2160)
	var page api.ItemPage
	e.expect(200, "GET", "/items?resolution=uhd", e.plain.token, nil, &page)
	var got []int64
	for _, it := range page.Items {
		got = append(got, it.Id)
	}
	if len(got) != 4 || slices.Contains(got, sdr) || !slices.Contains(got, uhd) || !slices.Contains(got, show) || !slices.Contains(got, ep) {
		t.Fatalf("movie, show, season and episode in 4K only: %v", got)
	}
	e.expect(200, "GET", "/items", e.plain.token, nil, &page)
	if len(page.Items) != 5 {
		t.Fatalf("without the chip every item is listed: %+v", page)
	}
}

// Recently added rows ask for the newest titles first, under the same
// filters and kid ceilings as every listing.
func TestItemsSortByAdded(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	for at, id := range []int64{c.pg, c.g, c.r, c.unrated} {
		_, err := e.db.Exec(`UPDATE item SET added_at = ? WHERE id = ?`, (at+1)*1000, id)
		must(t, err)
	}
	order := func(token, query string) []int64 {
		var page api.ItemPage
		e.expect(200, "GET", "/items?"+query, token, nil, &page)
		var out []int64
		for _, it := range page.Items {
			out = append(out, it.Id)
		}
		return out
	}
	if got := order(e.plain.token, "kind=movie&sort=added"); !slices.Equal(got, []int64{c.unrated, c.r, c.g, c.pg}) {
		t.Fatalf("newest first: %v", got)
	}
	if got := order(e.plain.token, "kind=movie&sort=added&limit=1&offset=1"); !slices.Equal(got, []int64{c.r}) {
		t.Fatalf("paged: %v", got)
	}
	if got := order(e.plain.token, "kind=movie&sort=title"); !slices.Equal(got, []int64{c.g, c.pg, c.r, c.unrated}) {
		t.Fatalf("by title: %v", got)
	}
	e.expect(400, "GET", "/items?sort=newest", e.plain.token, nil, nil)
	k := e.kid(e.plain, "Kid")
	if got := order(e.kidDevice(e.plain, k.Id), "kind=movie&sort=added"); !slices.Equal(got, []int64{c.g, c.pg}) {
		t.Fatalf("a kid's newest are within the ceiling: %v", got)
	}
}
