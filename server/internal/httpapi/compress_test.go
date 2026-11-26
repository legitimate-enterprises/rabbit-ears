package httpapi_test

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

// encoded asks for path with the Accept-Encoding given, so Go's client leaves
// any compressed body as it came.
func (e *env) encoded(path, acceptEncoding string) *http.Response {
	e.t.Helper()
	req, err := http.NewRequest("GET", e.url+path, nil)
	must(e.t, err)
	req.Header.Set("Authorization", "Bearer "+e.plain.token)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	e.t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestJSONIsGzippedWhenAccepted(t *testing.T) {
	e := newEnv(t)
	images := &fakeImages{}
	e.svc.Items.Images, e.svc.Items.ImageDir = images, t.TempDir()
	lib := e.library("movies")
	it := &item.Item{LibraryID: lib, Kind: item.Movie, ParsedTitle: "Heat"}
	must(t, e.svc.Items.Repo().Create(it))
	must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, PosterPath: "/heat.jpg"}))

	res := e.encoded("/items?kind=movie", "gzip")
	if res.Header.Get("Content-Encoding") != "gzip" || res.Header.Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers: %v", res.Header)
	}
	zr, err := gzip.NewReader(res.Body)
	must(t, err)
	var page struct {
		Items []struct{ ParsedTitle string }
	}
	must(t, json.NewDecoder(zr).Decode(&page))
	if len(page.Items) != 1 || page.Items[0].ParsedTitle != "Heat" {
		t.Errorf("page: %+v", page)
	}

	if res := e.encoded("/items?kind=movie", "identity"); res.Header.Get("Content-Encoding") != "" {
		t.Errorf("a client without gzip got %q", res.Header.Get("Content-Encoding"))
	}
	// Images are compressed already, and a 304 has no body to compress.
	res = e.encoded("/items/"+itoa(it.ID)+"/artwork/poster", "gzip")
	body, _ := io.ReadAll(res.Body)
	if res.Header.Get("Content-Encoding") != "" || string(body) != "w500/heat.jpg" {
		t.Errorf("artwork: %v %q", res.Header, body)
	}
}
