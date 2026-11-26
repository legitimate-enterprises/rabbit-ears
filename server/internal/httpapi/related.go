package httpapi

import (
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

// GetItemRelated answers what relates to an item the active profile may
// see, as the profile browses.
func (s *Server) GetItemRelated(ctx context.Context, r api.GetItemRelatedRequestObject) (api.GetItemRelatedResponseObject, error) {
	it, err := catalogItem(s.seen(ctx, r.ItemId))
	if err != nil {
		return nil, err
	}
	rel, err := s.svc.Related.Related(caller(ctx).device.HouseholdID, it, browse(ctx))
	if err != nil {
		return nil, err
	}
	admin := caller(ctx).asAdmin()
	out := api.GetItemRelated200JSONResponse{Collections: []api.RelatedCollection{}, Similar: []api.Item{}}
	for _, c := range rel.Collections {
		movies, err := s.items(c.Movies, admin)
		if err != nil {
			return nil, err
		}
		out.Collections = append(out.Collections, api.RelatedCollection{Collection: toCollection(c.View), Items: movies})
	}
	if out.Previous, err = s.neighbor(rel.Previous, admin); err != nil {
		return nil, err
	}
	out.Next, err = s.neighbor(rel.Next, admin)
	return out, err
}

// neighbor is it as the list operations give it, nil for nil.
func (s *Server) neighbor(it *item.Item, admin bool) (*api.Item, error) {
	if it == nil {
		return nil, nil
	}
	v, err := s.items([]*item.Item{it}, admin)
	if err != nil {
		return nil, err
	}
	return &v[0], nil
}
