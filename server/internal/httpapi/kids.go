package httpapi

import (
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// Kids' content: an adult's override of whether a movie or show is kids'
// content, whether a channel is for kids, and which kids on an allow list
// allow an item. Any adult writes them; a kid active is forbidden.

// titleOf resolves a movie or show for an adult active on the device.
func (s *Server) titleOf(ctx context.Context, id uid.ID) (*item.Item, error) {
	if caller(ctx).asKid() {
		return nil, errForbidden
	}
	it, err := s.svc.Items.Repo().Get(id)
	if err != nil || it.Kind != item.Movie && it.Kind != item.Show {
		return nil, orNotFound(err)
	}
	return it, nil
}

// SetKidsContent says whether a movie or show is kids' content, whatever
// its rating.
func (s *Server) SetKidsContent(ctx context.Context, r api.SetKidsContentRequestObject) (api.SetKidsContentResponseObject, error) {
	it, err := s.titleOf(ctx, r.ItemId)
	if err != nil {
		return nil, err
	}
	it.KidsContent = &r.Body.KidsContent
	if err := s.svc.Items.Repo().SetKidsContent(it.ID, it.KidsContent); err != nil {
		return nil, err
	}
	v, err := s.item(ctx, it, caller(ctx).asAdmin())
	return api.SetKidsContent200JSONResponse(v), err
}

// ClearKidsContent hands a movie or show back to its rating.
func (s *Server) ClearKidsContent(ctx context.Context, r api.ClearKidsContentRequestObject) (api.ClearKidsContentResponseObject, error) {
	it, err := s.titleOf(ctx, r.ItemId)
	if err != nil {
		return nil, err
	}
	return api.ClearKidsContent204Response{}, s.svc.Items.Repo().SetKidsContent(it.ID, nil)
}

// SetYouTubeChannelForKids says whether a channel is for kids.
func (s *Server) SetYouTubeChannelForKids(ctx context.Context, r api.SetYouTubeChannelForKidsRequestObject) (api.SetYouTubeChannelForKidsResponseObject, error) {
	if caller(ctx).asKid() {
		return nil, errForbidden
	}
	ch, err := s.svc.Items.Repo().Get(r.ChannelId)
	if err != nil || ch.Kind != item.Channel {
		return nil, orNotFound(err)
	}
	if err := s.svc.Items.Repo().SetKidsContent(ch.ID, &r.Body.ForKids); err != nil {
		return nil, err
	}
	v, err := one(s.svc.YouTube.Repo().Channels(youtube.ChannelFilter{ID: &ch.ID, AccountID: &caller(ctx).profile.ID}))
	if err != nil {
		return nil, err
	}
	return api.SetYouTubeChannelForKids200JSONResponse(toYouTubeChannel(v)), nil
}

// ListAllowedFor lists the kids of the device household on an allow list
// that holds the item itself.
func (s *Server) ListAllowedFor(ctx context.Context, r api.ListAllowedForRequestObject) (api.ListAllowedForResponseObject, error) {
	if caller(ctx).asKid() {
		return nil, errForbidden
	}
	if _, err := s.svc.Items.Repo().Get(r.ItemId); err != nil {
		return nil, err
	}
	ids, err := s.svc.Accounts.Repo().AllowedFor(caller(ctx).device.HouseholdID, r.ItemId)
	if ids == nil {
		ids = []uid.ID{}
	}
	return api.ListAllowedFor200JSONResponse{ProfileIds: ids}, err
}
