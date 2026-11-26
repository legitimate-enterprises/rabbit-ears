package httpapi

import (
	"net/http"
	"strconv"
	"time"
)

// announceArtwork names the artwork generation on every response, a refusal
// included, so a client learns of a changed image from whatever it asks
// next. A generation that cannot be read is left unsaid; the request goes on.
func (s *Server) announceArtwork(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if g, err := s.svc.Server.ArtworkGeneration(time.Now().UTC(), s.svc.ArtworkSettle); err == nil {
			w.Header().Set("Artwork-Generation", strconv.FormatInt(g, 10))
		}
		next.ServeHTTP(w, r)
	})
}
