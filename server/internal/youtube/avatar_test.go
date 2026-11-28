package youtube_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// fakeYouTube answers /channel/{id} with the testdata page, its og:image
// pointing back at itself, for the channels in pages; any other channel
// is 404 and image requests answer picture.
func fakeYouTube(t *testing.T, pages map[string]string, picture []byte) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := strings.CutPrefix(r.URL.Path, "/channel/"); ok {
			page, known := pages[id]
			if !known {
				http.NotFound(w, r)
				return
			}
			b, err := os.ReadFile(filepath.Join("testdata", page)) //nolint:gosec // test data
			if err != nil {
				t.Error(err)
			}
			_, _ = w.Write(bytes.ReplaceAll(b, []byte("BASE"), []byte(srv.URL)))
			return
		}
		if r.URL.Path == "/ytc/AIdro_nAY1rUirI0vhI82=s900-c-k-c0x00ffffff-no-rj" && r.URL.RawQuery == "a=1&b=2" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(picture)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A channel's avatar is its page's og:image, kept on disk by channel id;
// an unknown channel, a page without one and an id that is no channel id
// are not found.
func TestAvatarFromChannelPage(t *testing.T) {
	srv := fakeYouTube(t, map[string]string{"UCo0-VCHchPIG3JtJlIQnFew": "channel.html", "UCbare": "bare.html"}, []byte("jpeg"))
	a := youtube.NewAvatars(t.TempDir())
	a.Base = srv.URL
	p, err := a.Fetch(context.Background(), "UCo0-VCHchPIG3JtJlIQnFew")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p) //nolint:gosec // the test's temp dir
	if err != nil || string(b) != "jpeg" || filepath.Base(p) != "41e5787e9f28562d.jpg" {
		t.Fatalf("avatar %s %q %v", p, b, err)
	}
	for _, id := range []string{"UCmissing", "UCbare", "../etc", ""} {
		if _, err := a.Fetch(context.Background(), id); !errors.Is(err, youtube.ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
	}
}
