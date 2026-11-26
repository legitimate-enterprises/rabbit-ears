package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// Custom images: the owner of a playlist or a station sets one by
// uploading a file, picking one of its titles' artwork, or giving a link.
// Whoever sees the playlist or station sees the image, except a title's
// artwork, shown only while the title is in the playlist and to a profile
// that may see it.

// coverURL is the imageUrl of a cover served under base, nil for none.
func coverURL(base string, c *cover.Cover) *string {
	if c == nil {
		return nil
	}
	u := BasePath + base + "/image?v=" + strconv.FormatInt(c.ID, 10)
	return &u
}

// setCover keeps what the request gives as o's cover, a title being one of
// playlistID's entries.
func (s *Server) setCover(ctx context.Context, o cover.Owner, playlistID int64, body io.Reader, in *api.CustomImageInput) error {
	switch {
	case body != nil:
		b, err := io.ReadAll(body)
		if err != nil {
			return err
		}
		_, err = s.svc.Covers.Upload(o, b)
		return coverError(err, "body")
	case in == nil:
		return invalid("body", "send the image as application/octet-stream, or a CustomImageInput as JSON")
	case (in.ItemId == nil) == (in.Url == nil):
		return invalid("itemId", "name exactly one of itemId and url")
	case in.Url != nil:
		_, err := s.svc.Covers.Link(ctx, o, *in.Url)
		return coverError(err, "url")
	}
	it, err := s.seen(ctx, *in.ItemId)
	if errors.Is(err, errNotFound) {
		return invalid("itemId", service.ErrNotInPlaylist.Error())
	} else if err != nil {
		return err
	}
	backdrop := in.Kind != nil && *in.Kind == api.Backdrop
	if _, err := s.svc.Items.Artwork(ctx, it, audience(ctx), backdrop); errors.Is(err, db.ErrNotFound) {
		return invalid("kind", "the title has no such artwork")
	} else if err != nil {
		return err
	}
	_, err = s.svc.Covers.Pick(o, playlistID, it.ID, backdrop)
	return coverError(err, "itemId")
}

// coverError answers a file or link that is not kept as a 400 on field.
func coverError(err error, field string) error {
	for _, e := range []error{cover.ErrNotImage, cover.ErrTooManyPixels, cover.ErrLink, cover.ErrRefused, cover.ErrTooBig, service.ErrNotInPlaylist} {
		if errors.Is(err, e) {
			return invalid(field, e.Error())
		}
	}
	return err
}

// serveCover opens the image of a cover the viewer may see; nil is not
// found. Its body is nil when ifNoneMatch already names it.
func (s *Server) serveCover(ctx context.Context, c *cover.Cover, ifNoneMatch *string) (api.CustomImageImageResponse, error) {
	if c == nil {
		return api.CustomImageImageResponse{}, errNotFound
	}
	if c.ItemID != 0 {
		it, err := s.svc.Items.Repo().Get(c.ItemID)
		if err != nil {
			return api.CustomImageImageResponse{}, err
		}
		path, err := s.svc.Items.Artwork(ctx, it, audience(ctx), c.Backdrop)
		if err != nil {
			return api.CustomImageImageResponse{}, err
		}
		// The cover's address stays while the title's art can change, so
		// every use revalidates.
		f, size, tag, err := openArtwork(path, ifNoneMatch)
		out := api.CustomImageImageResponse{ContentType: mime.TypeByExtension(filepath.Ext(path)), ContentLength: size,
			Headers: api.CustomImageResponseHeaders{CacheControl: "private, no-cache", ETag: tag}}
		if f != nil {
			out.Body = f
		}
		return out, err
	}
	tag := `"cover/` + strconv.FormatInt(c.ID, 10) + `"`
	out := api.CustomImageImageResponse{ContentType: "image/jpeg", Headers: api.CustomImageResponseHeaders{CacheControl: artworkCache, ETag: tag}}
	if ifNoneMatch != nil && strings.Contains(*ifNoneMatch, tag) {
		return out, nil
	}
	b, err := s.svc.Covers.Repo().JPEG(c.ID)
	out.Body, out.ContentLength = bytes.NewReader(b), int64(len(b))
	return out, err
}

// current is the 304 for a cover whose body serveCover left out.
func current(f api.CustomImageImageResponse) api.CustomImageCurrentResponse {
	return api.CustomImageCurrentResponse{Headers: api.CustomImageCurrentResponseHeaders(f.Headers)}
}

func playlistCover(p *playlist.Playlist) cover.Owner { return cover.Owner{PlaylistID: p.ID} }

func playlistBase(profileID int64, p *playlist.Playlist) string {
	return "/profiles/" + strconv.FormatInt(profileID, 10) + "/playlists/" + strconv.FormatInt(p.ID, 10)
}

// GetPlaylistImage serves the playlist's custom image to whoever it is
// shown to.
func (s *Server) GetPlaylistImage(ctx context.Context, r api.GetPlaylistImageRequestObject) (api.GetPlaylistImageResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Read)
	if err != nil {
		return nil, err
	}
	c, err := s.svc.Covers.Shown(playlistCover(p), p.ID, browse(ctx))
	if err != nil {
		return nil, err
	}
	f, err := s.serveCover(ctx, c, r.Params.IfNoneMatch)
	if err != nil || f.Body == nil {
		return current(f), err
	}
	return api.GetPlaylistImage200ImageResponse{CustomImageImageResponse: f}, nil
}

// ReplacePlaylistImage sets the owner's playlist's custom image.
func (s *Server) ReplacePlaylistImage(ctx context.Context, r api.ReplacePlaylistImageRequestObject) (api.ReplacePlaylistImageResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	if err := s.setCover(ctx, playlistCover(p), p.ID, r.Body, r.JSONBody); err != nil {
		return nil, err
	}
	v, err := s.toPlaylist(ctx, p, r.ProfileId)
	return api.ReplacePlaylistImage200JSONResponse(v), err
}

// DeletePlaylistImage removes the owner's playlist's custom image.
func (s *Server) DeletePlaylistImage(ctx context.Context, r api.DeletePlaylistImageRequestObject) (api.DeletePlaylistImageResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	return api.DeletePlaylistImage204Response{}, s.svc.Covers.Repo().Delete(playlistCover(p))
}

// GetStationImage serves the station's custom image to whoever sees it.
func (s *Server) GetStationImage(ctx context.Context, r api.GetStationImageRequestObject) (api.GetStationImageResponseObject, error) {
	st, err := s.seenStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	c, err := s.svc.Covers.Shown(cover.Owner{StationID: st.ID}, st.PlaylistID, browse(ctx))
	if err != nil {
		return nil, err
	}
	f, err := s.serveCover(ctx, c, r.Params.IfNoneMatch)
	if err != nil || f.Body == nil {
		return current(f), err
	}
	return api.GetStationImage200ImageResponse{CustomImageImageResponse: f}, nil
}

// ReplaceStationImage sets the owner's station's custom image.
func (s *Server) ReplaceStationImage(ctx context.Context, r api.ReplaceStationImageRequestObject) (api.ReplaceStationImageResponseObject, error) {
	st, err := s.ownStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	if err := s.setCover(ctx, cover.Owner{StationID: st.ID}, st.PlaylistID, r.Body, r.JSONBody); err != nil {
		return nil, err
	}
	s.svc.Stations.Announce(st)
	v, err := s.toStation(ctx, st)
	return api.ReplaceStationImage200JSONResponse(v), err
}

// DeleteStationImage removes the owner's station's custom image.
func (s *Server) DeleteStationImage(ctx context.Context, r api.DeleteStationImageRequestObject) (api.DeleteStationImageResponseObject, error) {
	st, err := s.ownStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Covers.Repo().Delete(cover.Owner{StationID: st.ID}); err != nil {
		return nil, err
	}
	s.svc.Stations.Announce(st)
	return api.DeleteStationImage204Response{}, nil
}
