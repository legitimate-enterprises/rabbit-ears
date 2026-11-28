package httpapi

import (
	"context"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/favorite"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// ListFavorites pages a profile's favorites it browses, newest first.
func (s *Server) ListFavorites(ctx context.Context, r api.ListFavoritesRequestObject) (api.ListFavoritesResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Favorites.Repo().List(r.ProfileId, string(or(r.Params.Kind, "")), service.BrowseAudienceFor(who), limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListFavorites200JSONResponse{Items: mapAll(rows, toFavorite), NextOffset: next}, nil
}

// PutFavorite stars an item the profile may see.
func (s *Server) PutFavorite(ctx context.Context, r api.PutFavoriteRequestObject) (api.PutFavoriteResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	it, err := s.visibleTo(service.AudienceFor(who), r.ItemId)
	if err != nil {
		return nil, err
	}
	if !favorite.Takes(it.Kind) {
		return nil, invalid("itemId", "a "+it.Kind+" is not a favorite")
	}
	at := time.Now().UTC()
	if v := watchedAt(r.Params.At); v != nil {
		at = *v
	}
	return api.PutFavorite204Response{}, s.svc.Favorites.Repo().Put(r.ProfileId, r.ItemId, at)
}

// DeleteFavorite unstars an item. It reads only the profile, so an unstar
// queued for an item since gone or hidden still succeeds.
func (s *Server) DeleteFavorite(ctx context.Context, r api.DeleteFavoriteRequestObject) (api.DeleteFavoriteResponseObject, error) {
	if _, err := s.profile(ctx, r.ProfileId); err != nil {
		return nil, err
	}
	return api.DeleteFavorite204Response{}, s.svc.Favorites.Repo().Delete(r.ProfileId, r.ItemId, watchedAt(r.Params.At))
}

func toFavorite(f *favorite.Favorite) api.Favorite {
	return api.Favorite{ItemId: f.ItemID, Kind: api.FavoriteKind(f.Kind), CreatedAt: f.CreatedAt}
}
