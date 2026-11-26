package httpapi

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

var gzips = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.BestSpeed); return w }}

// gzipJSON compresses JSON bodies for clients that accept gzip. Everything
// else, images and errors without a body among them, passes as written.
func gzipJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

type gzipWriter struct {
	http.ResponseWriter
	zw      *gzip.Writer
	decided bool
}

func (g *gzipWriter) WriteHeader(status int) {
	if !g.decided {
		g.decided = true
		h := g.Header()
		h.Add("Vary", "Accept-Encoding")
		if status != http.StatusNoContent && status != http.StatusNotModified && h.Get("Content-Encoding") == "" &&
			strings.Contains(h.Get("Content-Type"), "json") {
			h.Set("Content-Encoding", "gzip")
			h.Del("Content-Length")
			g.zw = gzips.Get().(*gzip.Writer)
			g.zw.Reset(g.ResponseWriter)
		}
	}
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipWriter) Write(b []byte) (int, error) {
	if !g.decided {
		g.WriteHeader(http.StatusOK)
	}
	if g.zw != nil {
		return g.zw.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

func (g *gzipWriter) close() {
	if g.zw != nil {
		_ = g.zw.Close()
		gzips.Put(g.zw)
	}
}

// Unwrap lets http.ResponseController reach the connection beneath.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }
