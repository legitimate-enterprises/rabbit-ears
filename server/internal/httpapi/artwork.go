package httpapi

import (
	"context"
	"mime"
	"path/filepath"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

// immutable keeps an address's image for good: it never names another.
const immutable = "private, max-age=31536000, immutable"

// GetArtwork serves the image at an address any device's data may carry.
func (s *Server) GetArtwork(ctx context.Context, r api.GetArtworkRequestObject) (api.GetArtworkResponseObject, error) {
	tag := `"` + string(r.Store) + "/" + r.Name + `"`
	if r.Params.IfNoneMatch != nil && *r.Params.IfNoneMatch == tag {
		return api.GetArtwork304Response{Headers: api.GetArtwork304ResponseHeaders{CacheControl: immutable, ETag: tag}}, nil
	}
	path, err := s.svc.Items.Stored(ctx, string(r.Store), r.Name)
	if err != nil {
		return nil, err
	}
	f, size, _, err := openArtwork(path, nil)
	if err != nil {
		return nil, err
	}
	return api.GetArtwork200ImageResponse{Body: f, ContentType: mime.TypeByExtension(filepath.Ext(path)), ContentLength: size,
		Headers: api.GetArtwork200ResponseHeaders{CacheControl: immutable, ETag: tag}}, nil
}
