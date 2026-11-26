package httpapi_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

// fakeImages stands in for the provider's image cache: it writes the size
// and path it was asked for, so a reply names the image that was served.
type fakeImages struct{ calls int }

func (f *fakeImages) CacheImage(_ context.Context, dir, size, path string) (string, error) {
	f.calls++
	dest := filepath.Join(dir, size, path)
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", err
	}
	return dest, os.WriteFile(dest, []byte(size+path), 0o600)
}

func TestItemArtworkFallsBackToAncestors(t *testing.T) {
	e := newEnv(t)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, t.TempDir()
	var lib api.Library
	e.expect(201, "POST", "/libraries", e.admin.token, map[string]any{"name": "TV", "kind": "tv"}, &lib)
	items := e.svc.Items.Repo()
	add := func(kind string, parent *int64) *item.Item {
		it := &item.Item{LibraryID: lib.Id, Kind: kind, ParentID: parent, ParsedTitle: kind}
		must(t, items.Create(it))
		return it
	}
	show := add(item.Show, nil)
	season := add(item.Season, &show.ID)
	ep := add(item.Episode, &season.ID)
	bare := add(item.Show, nil)
	must(t, items.SetMetadata(&item.Metadata{ItemID: show.ID, Status: item.Matched, PosterPath: "/show.jpg", BackdropPath: "/wide.jpg"}))
	must(t, items.SetMetadata(&item.Metadata{ItemID: ep.ID, Status: item.Matched, PosterPath: "/still.jpg"}))

	get := func(id int64, kind string) string {
		t.Helper()
		status, raw := e.do("GET", "/items/"+itoa(id)+"/artwork/"+kind, e.plain.token, nil, nil)
		if status != 200 {
			t.Fatalf("%d %s: %d %s", id, kind, status, raw)
		}
		return string(raw)
	}
	for _, c := range []struct {
		id         int64
		kind, want string
	}{
		{ep.ID, "poster", "w500/still.jpg"},
		{ep.ID, "backdrop", "w1280/wide.jpg"},
		{season.ID, "poster", "w500/show.jpg"},
		{show.ID, "backdrop", "w1280/wide.jpg"},
	} {
		if got := get(c.id, c.kind); got != c.want {
			t.Errorf("%d %s = %q, want %q", c.id, c.kind, got, c.want)
		}
	}
	if !strings.Contains(string(e.expect(404, "GET", "/items/"+itoa(bare.ID)+"/artwork/poster", e.plain.token, nil, nil)), "not_found") {
		t.Error("an item without artwork answers not_found")
	}
	// A tile the client has keeps it a day, then revalidates without a body.
	req, err := http.NewRequest("GET", e.url+"/items/"+itoa(ep.ID)+"/artwork/poster", nil)
	must(t, err)
	req.Header.Set("Authorization", "Bearer "+e.plain.token)
	res, err := http.DefaultClient.Do(req)
	must(t, err)
	_ = res.Body.Close()
	tag := res.Header.Get("ETag")
	if tag != `"w500/still.jpg"` || res.Header.Get("Cache-Control") != "private, max-age=86400" || res.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("headers: %v", res.Header)
	}
	req.Header.Set("If-None-Match", tag)
	res, err = http.DefaultClient.Do(req)
	must(t, err)
	_ = res.Body.Close()
	if res.StatusCode != 304 || res.Header.Get("ETag") != tag {
		t.Errorf("revalidation: %d %v", res.StatusCode, res.Header)
	}
	req.Header.Set("If-None-Match", `"w500/old.jpg"`)
	if res, err = http.DefaultClient.Do(req); err != nil || res.StatusCode != 200 {
		t.Errorf("a changed image is sent again: %v %v", res, err)
	}
	_ = res.Body.Close()

	e.expect(400, "GET", "/items/"+itoa(show.ID)+"/artwork/logo", e.plain.token, nil, nil)
	e.expect(401, "GET", "/items/"+itoa(show.ID)+"/artwork/poster", "", nil, nil)
}
