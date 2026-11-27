package httpapi_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/scan"
)

// A movie a scan has just found, before any metadata, local metadata or
// thumbnail task has run, is listed under its parsed name without artwork
// in every sort, reaches the sync feed, and plays.
func TestADiscoveredMovieIsListedAndPlaysBeforeItsMetadata(t *testing.T) {
	e := synced(t)
	lib := e.library("movies")
	root := t.TempDir()
	dir := filepath.Join(root, "Lamplight (2011)")
	must(t, os.MkdirAll(dir, 0o750))
	must(t, os.Link(fixturePath(t, "chapters.mkv"), filepath.Join(dir, "Lamplight (2011).mkv")))
	must(t, e.svc.Libraries.Repo().AddFolder(&library.Folder{LibraryID: lib, Path: root}, nil))
	_, v, _ := e.snapshot(e.plain.token)

	sc := scan.New(scan.Config{Libraries: e.svc.Libraries.Repo(), Items: e.svc.Items.Repo(), Files: e.svc.MediaFiles.Repo(),
		Editions: e.svc.Editions.Repo(), Genres: e.svc.Genres.Repo(), People: e.svc.People.Repo(), ThumbDir: t.TempDir(),
		Previews: e.svc.Previews.Repo(), PreviewDir: t.TempDir()})
	l, err := e.svc.Libraries.Repo().Get(lib)
	must(t, err)
	_, err = sc.Scan(context.Background(), l, nil)
	must(t, err)

	var page api.ItemPage
	for _, sort := range []string{"title", "added"} {
		e.expect(200, "GET", "/items?kind=movie&sort="+sort, e.plain.token, nil, &page)
		if len(page.Items) != 1 || page.Items[0].ParsedTitle != "Lamplight" || *page.Items[0].ParsedYear != 2011 {
			t.Fatalf("sort=%s: %+v", sort, page.Items)
		}
		if m := page.Items[0].Metadata; m != nil && (m.PosterPath != nil || m.BackdropPath != nil) {
			t.Fatalf("artwork before any task made it: %+v", m)
		}
	}
	movie := page.Items[0].Id
	var it api.Item
	e.expect(200, "GET", "/items/"+itoa(movie), e.plain.token, nil, &it)
	if it.Editions == nil || len(*it.Editions) != 1 {
		t.Fatalf("editions %+v", it.Editions)
	}

	var c api.SyncChanges
	e.expect(200, "GET", fmt.Sprintf("/sync/changes?schema=1&since=%d", v), e.plain.token, nil, &c)
	i := slices.IndexFunc(*c.Tables, func(tb api.SyncTable) bool { return tb.Name == "item" })
	if c.Mode != api.Diff || i < 0 || !slices.ContainsFunc((*c.Tables)[i].Rows, func(r []any) bool { return r[0] == movie.String() }) {
		t.Fatalf("the sync feed lacks the movie: %+v", c)
	}

	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": movie, "snapshot": snapshot()}, &st)
	if st.StreamUrl == "" {
		t.Fatalf("%+v", st)
	}
}
