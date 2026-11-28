package httpapi

import (
	"context"
	"errors"
	"mime"
	"path/filepath"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Collections: TMDB's, seen server-wide, and each household's custom ones,
// seen by that household only. Every route first finds the collection as
// the device's household and active profile see it, so another household's
// and one holding nothing a kid may see are not found before any write is
// refused.

// seenCollection finds a collection the device's household and active
// profile may see, else not found.
func (s *Server) seenCollection(ctx context.Context, id uid.ID) (*collection.View, error) {
	return s.svc.Collections.Repo().Seen(caller(ctx).device.HouseholdID, id, audience(ctx))
}

// shownCollection is seenCollection for browsing, which an adult hiding
// kids' content browses without it.
func (s *Server) shownCollection(ctx context.Context, id uid.ID) (*collection.View, error) {
	return s.svc.Collections.Repo().Seen(caller(ctx).device.HouseholdID, id, browse(ctx))
}

// collectionToWrite is seenCollection for a write, which a kid may not make.
func (s *Server) collectionToWrite(ctx context.Context, id uid.ID) (*collection.View, error) {
	v, err := s.seenCollection(ctx, id)
	if err == nil && !caller(ctx).manages() {
		return nil, errForbidden
	}
	return v, err
}

// collectionError answers the service's rules as the spec does.
func collectionError(err error) error {
	switch {
	case errors.Is(err, service.ErrCollectionReadOnly):
		return fail(403, api.ErrorCodeForbidden, err.Error())
	case errors.Is(err, service.ErrNotAMovie):
		return invalid("itemId", err.Error())
	}
	return err
}

func toCollection(v *collection.View) api.Collection {
	return api.Collection{Id: v.ID, Source: api.CollectionSource(v.Source), Name: v.Name, Overview: optStr(v.Overview),
		HouseholdId: v.HouseholdID, TmdbId: v.TMDBID, MovieCount: v.MovieCount, CustomOrder: &v.CustomOrder, CreatedAt: v.CreatedAt, ModifiedAt: v.ModifiedAt,
		CreatedBy: v.CreatedBy, ModifiedBy: v.ModifiedBy, PosterUrl: optStr(v.Art.Poster), BackdropUrl: optStr(v.Art.Backdrop)}
}

// ListCollections pages by name the collections the active profile may see.
func (s *Server) ListCollections(ctx context.Context, r api.ListCollectionsRequestObject) (api.ListCollectionsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	a := browse(ctx)
	rows, err := s.svc.Collections.Repo().List(caller(ctx).device.HouseholdID, a, limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := api.ListCollections200JSONResponse{}
	if out.Total, err = s.svc.Collections.Repo().Count(caller(ctx).device.HouseholdID, a); err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	out.Items = mapAll(rows, toCollection)
	return out, nil
}

// CreateCollection makes an empty custom collection in the device's household.
func (s *Server) CreateCollection(ctx context.Context, r api.CreateCollectionRequestObject) (api.CreateCollectionResponseObject, error) {
	if !caller(ctx).manages() {
		return nil, errForbidden
	}
	name, err := playlistName(r.Body.Name)
	if err != nil {
		return nil, err
	}
	household := caller(ctx).device.HouseholdID
	c := &collection.Collection{ID: or(r.Body.Id, uid.ID{}), HouseholdID: &household, Name: name}
	if err := s.svc.Collections.Repo().Create(c, actor(ctx)); err != nil {
		return nil, err
	}
	return api.CreateCollection201JSONResponse(toCollection(&collection.View{Collection: *c})), nil
}

// GetCollection returns one collection the active profile may see.
func (s *Server) GetCollection(ctx context.Context, r api.GetCollectionRequestObject) (api.GetCollectionResponseObject, error) {
	v, err := s.shownCollection(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	return api.GetCollection200JSONResponse(toCollection(v)), nil
}

// ReplaceCollection renames a custom collection.
func (s *Server) ReplaceCollection(ctx context.Context, r api.ReplaceCollectionRequestObject) (api.ReplaceCollectionResponseObject, error) {
	if err := sameID(r.Body.Id, r.CollectionId); err != nil {
		return nil, err
	}
	v, err := s.collectionToWrite(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	name, err := playlistName(r.Body.Name)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Collections.Rename(&v.Collection, name, actor(ctx)); err != nil {
		return nil, collectionError(err)
	}
	return api.ReplaceCollection200JSONResponse(toCollection(v)), nil
}

// DeleteCollection deletes a custom collection and the entries holding it.
func (s *Server) DeleteCollection(ctx context.Context, r api.DeleteCollectionRequestObject) (api.DeleteCollectionResponseObject, error) {
	v, err := s.collectionToWrite(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	return api.DeleteCollection204Response{}, collectionError(s.svc.Collections.Delete(&v.Collection))
}

// ListCollectionMovies pages the collection's movies the active profile may
// see, in collection order.
func (s *Server) ListCollectionMovies(ctx context.Context, r api.ListCollectionMoviesRequestObject) (api.ListCollectionMoviesResponseObject, error) {
	if _, err := s.shownCollection(ctx, r.CollectionId); err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	ids, err := s.svc.Collections.Repo().Movies(r.CollectionId, browse(ctx), limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := api.ListCollectionMovies200JSONResponse{}
	ids, out.NextOffset = window(ids, limit, offset)
	rows := make([]*item.Item, len(ids))
	for i, id := range ids {
		if rows[i], err = s.svc.Items.Repo().Get(id); err != nil {
			return nil, err
		}
	}
	out.Items, err = s.items(ctx, rows, caller(ctx).asAdmin())
	return out, err
}

// AddCollectionMovie puts a movie the active profile may see into a custom
// collection.
func (s *Server) AddCollectionMovie(ctx context.Context, r api.AddCollectionMovieRequestObject) (api.AddCollectionMovieResponseObject, error) {
	v, err := s.collectionToWrite(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	it, err := s.seen(ctx, r.ItemId)
	if err != nil {
		return nil, err
	}
	return api.AddCollectionMovie204Response{}, collectionError(s.svc.Collections.AddMovie(&v.Collection, it))
}

// RemoveCollectionMovie takes a movie out of a custom collection.
func (s *Server) RemoveCollectionMovie(ctx context.Context, r api.RemoveCollectionMovieRequestObject) (api.RemoveCollectionMovieResponseObject, error) {
	v, err := s.collectionToWrite(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	return api.RemoveCollectionMovie204Response{}, collectionError(s.svc.Collections.RemoveMovie(&v.Collection, r.ItemId))
}

// ReplaceCollectionMoviePosition moves a movie within a custom collection.
func (s *Server) ReplaceCollectionMoviePosition(ctx context.Context, r api.ReplaceCollectionMoviePositionRequestObject) (api.ReplaceCollectionMoviePositionResponseObject, error) {
	v, err := s.collectionToWrite(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Collections.MoveMovie(&v.Collection, r.ItemId, r.Body.Position); err != nil {
		return nil, collectionError(err)
	}
	if v, err = s.seenCollection(ctx, r.CollectionId); err != nil {
		return nil, err
	}
	return api.ReplaceCollectionMoviePosition200JSONResponse(toCollection(v)), nil
}

// ResetCollectionOrder returns a custom collection to release order.
func (s *Server) ResetCollectionOrder(ctx context.Context, r api.ResetCollectionOrderRequestObject) (api.ResetCollectionOrderResponseObject, error) {
	v, err := s.collectionToWrite(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Collections.ResetOrder(&v.Collection); err != nil {
		return nil, collectionError(err)
	}
	if v, err = s.seenCollection(ctx, r.CollectionId); err != nil {
		return nil, err
	}
	return api.ResetCollectionOrder200JSONResponse(toCollection(v)), nil
}

// GetCollectionArtwork serves a collection's image, or its first movie's.
func (s *Server) GetCollectionArtwork(ctx context.Context, r api.GetCollectionArtworkRequestObject) (api.GetCollectionArtworkResponseObject, error) {
	v, err := s.shownCollection(ctx, r.CollectionId)
	if err != nil {
		return nil, err
	}
	path, err := s.svc.Collections.Artwork(ctx, &v.Collection, browse(ctx), r.Kind == api.Backdrop)
	if err != nil {
		return nil, err
	}
	f, size, tag, err := openArtwork(path, r.Params.IfNoneMatch)
	if err != nil || f == nil {
		return api.GetCollectionArtwork304Response{Headers: api.GetCollectionArtwork304ResponseHeaders{CacheControl: artworkCache, ETag: tag}}, err
	}
	return api.GetCollectionArtwork200ImageResponse{Body: f, ContentType: mime.TypeByExtension(filepath.Ext(path)), ContentLength: size,
		Headers: api.GetCollectionArtwork200ResponseHeaders{CacheControl: artworkCache, ETag: tag}}, nil
}

// movieCollections names the collections a movie is in that the active
// profile may see.
func (s *Server) movieCollections(ctx context.Context, movieID uid.ID) (*[]api.Collection, error) {
	vs, err := s.svc.Collections.Repo().ForMovie(caller(ctx).device.HouseholdID, movieID, browse(ctx))
	out := mapAll(vs, toCollection)
	return &out, err
}
