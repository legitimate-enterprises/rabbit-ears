// Package webui serves the web client from inside the server binary, so the
// address a TV or a join link names opens the viewer and the admin UI.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// site holds web/dist as `make build` copied it; a plain `go build` embeds
// whatever is there, which in a fresh checkout is nothing.
//
//go:embed all:site
var site embed.FS

// Site is the embedded web client.
func Site() fs.FS {
	sub, err := fs.Sub(site, "site")
	if err != nil {
		panic(err)
	}
	return sub
}

// Handler serves a built web client: its files as they are, and for every
// other path the page that owns it, since the viewer routes /link and
// /join/<code> itself and the admin UI lives under /admin/. A site with no
// pages answers 404.
func Handler(site fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		// Codes ride in page URLs, and Link a TV approves with one click.
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		// hls.js plays through blob URLs and a worker made from one; the
		// pages set style attributes.
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' blob: data:; media-src 'self' blob:; "+
			"worker-src 'self' blob:; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		hashed := strings.HasPrefix(name, "assets/")
		if st, err := fs.Stat(site, name); err != nil || st.IsDir() || strings.HasPrefix(path.Base(name), ".") {
			if hashed {
				http.NotFound(w, r)
				return
			}
			name = "index.html"
			if r.URL.Path == "/admin" || strings.HasPrefix(r.URL.Path, "/admin/") {
				name = "admin/index.html"
			}
		}
		h.Set("Cache-Control", "no-cache")
		if hashed {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFileFS(w, r, site, name) //nolint:gosec // an fs.FS cannot be escaped, and name is cleaned
	})
}
