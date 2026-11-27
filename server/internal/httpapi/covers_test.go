package httpapi_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	must(t, png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))))
	return b.Bytes()
}

func (e *env) send(method, path, token, contentType string, body []byte, header ...string) (int, http.Header, []byte) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.url+path, bytes.NewReader(body))
	must(e.t, err)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	must(e.t, err)
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, raw
}

func (e *env) upload(path, token string, body []byte, out any) {
	e.t.Helper()
	status, _, raw := e.send("PUT", path, token, "application/octet-stream", body)
	if status != 200 {
		e.t.Fatalf("upload %s = %d: %s", path, status, raw)
	}
	e.expect(200, "GET", strings.TrimSuffix(path, "/image"), token, nil, out)
}

// image fetches a server-relative imageUrl as token.
func (e *env) image(url *string, token string, header ...string) (int, http.Header, []byte) {
	e.t.Helper()
	if url == nil {
		e.t.Fatal("no imageUrl")
	}
	return e.send("GET", strings.TrimPrefix(*url, httpapi.BasePath), token, "", nil, header...)
}

func fieldOf(t *testing.T, raw []byte) string {
	t.Helper()
	var er struct{ Fields []struct{ Field string } }
	if err := json.Unmarshal(raw, &er); err != nil || len(er.Fields) == 0 {
		return ""
	}
	return er.Fields[0].Field
}

func TestPlaylistImageUpload(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	if s.pl.ImageUrl != nil {
		t.Fatalf("a new playlist has no image: %+v", s.pl)
	}
	var pl api.Playlist
	e.upload(s.owner+"/image", e.plain.token, pngOf(t, 3200, 1600), &pl)
	if pl.ImageUrl == nil || pl.ArtItemId == nil {
		t.Fatalf("%+v", pl)
	}
	status, h, raw := e.image(pl.ImageUrl, e.plain.token)
	img, format, err := image.Decode(bytes.NewReader(raw))
	if status != 200 || err != nil || format != "jpeg" || h.Get("Content-Type") != "image/jpeg" ||
		img.Bounds().Dx() != cover.MaxSide || h.Get("Cache-Control") != "private, max-age=86400" {
		t.Fatalf("%d %s %v %v %v", status, format, err, img, h)
	}
	// The address and the validator name the cover by its id.
	_, v, _ := strings.Cut(*pl.ImageUrl, "/image?v=")
	if _, err := uid.Parse(v); err != nil || h.Get("ETag") != `"cover/`+v+`"` {
		t.Fatalf("%s %s", *pl.ImageUrl, h.Get("ETag"))
	}
	if status, _, _ := e.send("GET", strings.TrimPrefix(*pl.ImageUrl, httpapi.BasePath), e.plain.token, "", nil,
		"If-None-Match", h.Get("ETag")); status != 304 {
		t.Fatalf("revalidated: %d", status)
	}

	// A new image has a new address.
	first := *pl.ImageUrl
	e.upload(s.owner+"/image", e.plain.token, pngOf(t, 10, 10), &pl)
	if *pl.ImageUrl == first {
		t.Fatalf("same address %s", first)
	}
	e.expect(204, "DELETE", s.owner+"/image", e.plain.token, nil, nil)
	e.expect(200, "GET", s.owner, e.plain.token, nil, &pl)
	if pl.ImageUrl != nil {
		t.Fatalf("removed: %+v", pl)
	}
	e.expect(404, "GET", s.owner+"/image", e.plain.token, nil, nil)
	e.expect(204, "DELETE", s.owner+"/image", e.plain.token, nil, nil)
}

// What is not an accepted image, or too much of one, is refused before
// anything is kept.
func TestPlaylistImageUploadValidation(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	path := s.owner + "/image"
	for name, c := range map[string]struct {
		kind   string
		body   []byte
		status int
		field  string
	}{
		"text":         {"application/octet-stream", []byte("<svg></svg>"), 400, "body"},
		"empty":        {"application/octet-stream", nil, 400, ""},
		"image type":   {"image/png", pngOf(t, 4, 4), 400, ""},
		"json neither": {"application/json", []byte(`{}`), 400, "itemId"},
		"too big":      {"application/octet-stream", make([]byte, 16<<20+1), 413, ""},
	} {
		status, _, raw := e.send("PUT", path, e.plain.token, c.kind, c.body)
		if status != c.status || c.field != "" && fieldOf(t, raw) != c.field {
			t.Errorf("%s: %d %s", name, status, raw)
		}
	}
	if status, _, raw := e.send("PUT", path, e.plain.token, "application/octet-stream", make([]byte, 16<<20+1)); errorCode(t, raw) != "payload_too_large" {
		t.Errorf("%d %s", status, raw)
	}
	if _, err := e.svc.Covers.Repo().Of(cover.Owner{PlaylistID: &s.pl.Id}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("kept: %v", err)
	}
}

// Only the owner sets or removes the image; whoever the playlist is shared
// with sees it.
func TestPlaylistImageIsTheOwners(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	e.link(e.plain.account.ID, e.admin.account.ID, false)
	e.share(s, e.admin.account.ID, "write")
	theirs := s.under(e.admin.account.ID)
	if status, _, _ := e.send("PUT", theirs+"/image", e.admin.token, "application/octet-stream", pngOf(t, 4, 4)); status != 403 {
		t.Fatalf("a read-write sharee set it: %d", status)
	}
	e.upload(s.owner+"/image", e.plain.token, pngOf(t, 4, 4), nil)
	e.expect(403, "DELETE", theirs+"/image", e.admin.token, nil, nil)
	var pl api.Playlist
	e.expect(200, "GET", theirs, e.admin.token, nil, &pl)
	if status, _, _ := e.image(pl.ImageUrl, e.admin.token); status != 200 || !strings.Contains(*pl.ImageUrl, "/profiles/"+itoa(e.admin.account.ID)+"/") {
		t.Fatalf("the sharee sees it at their own address: %d %v", status, *pl.ImageUrl)
	}
	e.expect(204, "DELETE", s.sharesPath+"/"+itoa(e.admin.account.ID), e.plain.token, nil, nil)
	e.expect(404, "GET", theirs+"/image", e.admin.token, nil, nil)
}

// A title's artwork shows only while the title is in the playlist, and
// only to a profile that may see it; anyone else gets the default.
func TestPlaylistImageFromATitle(t *testing.T) {
	e := newEnv(t)
	e.svc.Items.Images, e.svc.Items.ImageDir = &fakeImages{}, t.TempDir()
	s := e.sharedSetup()
	for _, id := range []uid.ID{s.g, s.r} {
		must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: id, Status: item.Matched, PosterPath: "/p" + itoa(id) + ".jpg",
			ContentRating: map[bool]string{true: "G", false: "R"}[id == s.g]}))
	}
	path := s.owner + "/image"
	g, err := e.svc.Items.Repo().Get(s.g)
	must(t, err)
	other := e.film(g.LibraryID, "Elsewhere", "2003-01-01", "G")
	e.expect(400, "PUT", path, e.plain.token, map[string]any{"itemId": other}, nil)
	e.expect(400, "PUT", path, e.plain.token, map[string]any{"itemId": s.r, "kind": "backdrop"}, nil)
	e.expect(400, "PUT", path, e.plain.token, map[string]any{"itemId": s.r, "url": "https://example.com/a.jpg"}, nil)

	var pl api.Playlist
	e.expect(200, "PUT", path, e.plain.token, map[string]any{"itemId": s.r}, &pl)
	status, h, raw := e.image(pl.ImageUrl, e.plain.token)
	if status != 200 || string(raw) != "w500/p"+itoa(s.r)+".jpg" {
		t.Fatalf("%d %s", status, raw)
	}
	// The address names the cover, not the title's art, which can change
	// under it: a client asks again each time.
	if cc := h.Get("Cache-Control"); cc != "private, no-cache" || h.Get("ETag") == "" {
		t.Fatalf("a picked title's image is kept by %q", cc)
	}
	if status, _, _ := e.image(pl.ImageUrl, e.plain.token, "If-None-Match", h.Get("ETag")); status != 304 {
		t.Fatalf("revalidated: %d", status)
	}

	k := e.kid(e.plain, "Kid")
	e.share(s, k.Id, "read")
	kid := e.kidDevice(e.plain, k.Id)
	e.expect(200, "GET", s.under(k.Id), kid, nil, &pl)
	if pl.ImageUrl != nil {
		t.Fatalf("a kid sees an R title's poster: %+v", pl)
	}
	e.expect(404, "GET", s.under(k.Id)+"/image", kid, nil, nil)
	e.expect(200, "PUT", path, e.plain.token, map[string]any{"itemId": s.g}, nil)
	e.expect(200, "GET", s.under(k.Id), kid, nil, &pl)
	if status, _, _ := e.image(pl.ImageUrl, kid); status != 200 {
		t.Fatalf("a kid's own title: %d", status)
	}

	// The title leaving the playlist leaves the default.
	var entries api.PlaylistEntryPage
	e.expect(200, "GET", s.owner+"/entries", e.plain.token, nil, &entries)
	for _, en := range entries.Items {
		if *en.ItemId == s.g {
			e.expect(204, "DELETE", s.owner+"/entries/"+itoa(en.Id), e.plain.token, nil, nil)
		}
	}
	e.expect(200, "GET", s.owner, e.plain.token, nil, &pl)
	if pl.ImageUrl != nil {
		t.Fatalf("a removed title's poster: %+v", pl)
	}
	e.expect(404, "GET", path, e.plain.token, nil, nil)
}

func TestPlaylistImageFromALink(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	mux := http.NewServeMux()
	mux.HandleFunc("/a.png", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(pngOf(t, 20, 10)) })
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html></html>")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	path := s.owner + "/image"

	// The real guard refuses this host.
	raw := e.expect(400, "PUT", path, e.plain.token, map[string]any{"url": srv.URL + "/a.png"}, nil)
	if fieldOf(t, raw) != "url" {
		t.Fatalf("%s", raw)
	}
	e.svc.Covers.Fetcher.Refuse = func(ip net.IP) bool { return !ip.Equal(net.IPv4(127, 0, 0, 1)) && cover.Refused(ip) }
	for _, u := range []string{srv.URL + "/page", "ftp://example.com/a.png", "http://10.0.0.1/a.png"} {
		if raw := e.expect(400, "PUT", path, e.plain.token, map[string]any{"url": u}, nil); fieldOf(t, raw) != "url" {
			t.Errorf("%s: %s", u, raw)
		}
	}
	var pl api.Playlist
	e.expect(200, "PUT", path, e.plain.token, map[string]any{"url": srv.URL + "/a.png"}, &pl)
	srv.Close()
	if status, _, raw := e.image(pl.ImageUrl, e.plain.token); status != 200 || len(raw) == 0 {
		t.Fatalf("kept, not fetched again: %d", status)
	}
}

// Deleting a playlist or a station deletes its image.
func TestImagesGoWithTheirOwners(t *testing.T) {
	e := newEnv(t)
	s := e.sharedSetup()
	e.upload(s.owner+"/image", e.plain.token, pngOf(t, 4, 4), nil)
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Own", "kind": "movies"}, &st)
	e.upload("/stations/"+itoa(st.Id)+"/image", e.plain.token, pngOf(t, 4, 4), nil)
	e.expect(204, "DELETE", s.owner, e.plain.token, nil, nil)
	e.expect(204, "DELETE", "/stations/"+itoa(st.Id), e.plain.token, nil, nil)
	for _, o := range []cover.Owner{{PlaylistID: &s.pl.Id}, {StationID: &st.Id}} {
		if _, err := e.svc.Covers.Repo().Of(o); !errors.Is(err, db.ErrNotFound) {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestStationImage(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	e.svc.Items.Images, e.svc.Items.ImageDir = &fakeImages{}, t.TempDir()
	films := e.library("movies")
	lantern := e.film(films, "Lantern", "2001-01-01", "R")
	must(t, e.svc.Items.Repo().SetMetadata(&item.Metadata{ItemID: lantern, Status: item.Matched, BackdropPath: "/wide.jpg", ContentRating: "R"}))
	pl := e.nightsPlaylist(map[uid.ID]int64{lantern: 90})
	neighbor := e.neighbor(e.plain)
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl, "visibility": "household"}, &st)
	if st.ImageUrl != nil {
		t.Fatalf("%+v", st)
	}
	path := "/stations/" + itoa(st.Id) + "/image"
	if status, _, _ := e.send("PUT", path, neighbor, "application/octet-stream", pngOf(t, 4, 4)); status != 403 {
		t.Fatalf("another adult of the household set it: %d", status)
	}
	e.expect(404, "PUT", path, e.admin.token, map[string]any{"itemId": lantern}, nil)
	e.upload(path, e.plain.token, pngOf(t, 4, 4), &st)
	if status, _, _ := e.image(st.ImageUrl, neighbor); status != 200 {
		t.Fatalf("household: %d", status)
	}
	e.expect(403, "DELETE", path, neighbor, nil, nil)

	e.expect(200, "PUT", path, e.plain.token, map[string]any{"itemId": lantern, "kind": "backdrop"}, &st)
	if status, _, raw := e.image(st.ImageUrl, neighbor); status != 200 || string(raw) != "w1280/wide.jpg" {
		t.Fatalf("%d %s", status, raw)
	}
	other := e.film(films, "Other", "2003-01-01", "G")
	e.expect(400, "PUT", path, e.plain.token, map[string]any{"itemId": other}, nil)
	e.expect(204, "DELETE", path, e.plain.token, nil, nil)
	e.expect(200, "GET", "/stations/"+itoa(st.Id), neighbor, nil, &st)
	if st.ImageUrl != nil {
		t.Fatalf("%+v", st)
	}
}
