package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var built = fstest.MapFS{
	"index.html":       {Data: []byte("viewer")},
	"admin/index.html": {Data: []byte("admin")},
	"assets/app.js":    {Data: []byte("js")},
}

func get(t *testing.T, h http.Handler, method, path string) (*http.Response, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	b, _ := io.ReadAll(w.Result().Body)
	return w.Result(), string(b)
}

// The viewer's path routes and the admin UI's have no file behind them, so
// every path that is not a file loads its page.
func TestServesPagesForPathsAndFilesAsTheyAre(t *testing.T) {
	h := Handler(built)
	for path, want := range map[string]string{
		"/": "viewer", "/link?code=7KQ4-MNP2": "viewer", "/join/abc": "viewer",
		"/admin": "admin", "/admin/": "admin", "/admin/users": "admin", "/assets/app.js": "js",
	} {
		res, body := get(t, h, "GET", path)
		if res.StatusCode != 200 || body != want {
			t.Errorf("GET %s = %d %q, want %q", path, res.StatusCode, body, want)
		}
	}
	if res, _ := get(t, h, "GET", "/assets/gone.js"); res.StatusCode != 404 {
		t.Errorf("a missing asset = %d, want 404", res.StatusCode)
	}
	if res, _ := get(t, h, "POST", "/"); res.StatusCode != 405 {
		t.Errorf("POST / = %d, want 405", res.StatusCode)
	}
}

// Pages must be refetched after an upgrade while hashed assets never change,
// and no page may be framed: Link a TV approves with one click.
func TestCachingAndFramingHeaders(t *testing.T) {
	h := Handler(built)
	page, _ := get(t, h, "GET", "/link")
	if page.Header.Get("Cache-Control") != "no-cache" || page.Header.Get("X-Frame-Options") != "DENY" ||
		page.Header.Get("Referrer-Policy") != "no-referrer" || page.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("page headers %v", page.Header)
	}
	asset, _ := get(t, h, "GET", "/assets/app.js")
	if !strings.Contains(asset.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset headers %v", asset.Header)
	}
}

// A page loads scripts from this server only; hls.js plays through blob
// URLs and a worker made from one.
func TestPagesCarryAContentSecurityPolicy(t *testing.T) {
	page, _ := get(t, Handler(built), "GET", "/admin/users")
	csp := page.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "media-src 'self' blob:", "worker-src 'self' blob:", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("policy %q lacks %q", csp, want)
		}
	}
}

// A binary built without the web client embeds an empty site.
func TestAnEmptySiteAnswersNotFound(t *testing.T) {
	h := Handler(fstest.MapFS{".gitignore": {Data: []byte("*")}})
	for _, path := range []string{"/", "/admin/", "/link", "/.gitignore"} {
		if res, _ := get(t, h, "GET", path); res.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404", path, res.StatusCode)
		}
	}
}

func TestTheEmbeddedSiteOpens(t *testing.T) {
	if _, err := Site().Open("."); err != nil {
		t.Fatal(err)
	}
}
