package httpapi

import (
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/offer"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Profile offers: an adult offers a profile of their household to an adult
// an admin linked them with.

func (s *Server) toOffer(o *offer.Offer) (api.ProfileOffer, error) {
	named := map[uid.ID]*account.Account{}
	for _, id := range []uid.ID{o.ProfileID, o.FromID, o.ToID} {
		a, err := s.svc.Accounts.Repo().Get(id)
		if err != nil {
			return api.ProfileOffer{}, err
		}
		named[id] = a
	}
	h, err := s.svc.Households.Repo().Get(o.HouseholdID)
	if err != nil {
		return api.ProfileOffer{}, err
	}
	p := named[o.ProfileID]
	return api.ProfileOffer{Id: o.ID, ProfileId: p.ID, ProfileName: p.Name, ProfileKind: api.AccountKind(p.Kind), HouseholdId: h.ID,
		HouseholdName: h.Name, FromProfileId: o.FromID, FromName: named[o.FromID].Name, ToProfileId: o.ToID, ToName: named[o.ToID].Name,
		CreatedAt: o.CreatedAt, ModifiedAt: o.ModifiedAt, CreatedBy: o.CreatedBy, ModifiedBy: o.ModifiedBy}, nil
}

func (s *Server) toOffers(os []*offer.Offer) ([]api.ProfileOffer, error) {
	out := []api.ProfileOffer{}
	for _, o := range os {
		v, err := s.toOffer(o)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// householdChanged tells the devices of each household that its profiles
// or offers changed.
func (s *Server) householdChanged(householdIDs ...uid.ID) {
	for _, h := range householdIDs {
		s.svc.Events.Publish(live.Event{Kind: live.Household, HouseholdID: &h})
	}
}

// offerChanged tells both sides of an offer, and any household it put the
// profile in, that it changed.
func (s *Server) offerChanged(o *offer.Offer, households ...uid.ID) {
	s.householdChanged(append(households, o.HouseholdID)...)
	s.svc.Events.Publish(live.Event{Kind: live.Household, AccountIDs: []uid.ID{o.ToID}})
}

// ListLinkedAdults pages the adults the active adult may offer profiles to.
func (s *Server) ListLinkedAdults(ctx context.Context, r api.ListLinkedAdultsRequestObject) (api.ListLinkedAdultsResponseObject, error) {
	p := caller(ctx)
	if p.asKid() {
		return nil, errForbidden
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Accounts.Repo().Linked(p.profile, device.TV(p.device.Platform), limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	items := mapAll(rows, toAccount)
	for i := range items {
		s.withAvatars(ctx, &items[i])
		items[i].Email, items[i].HouseholdId = nil, nil
	}
	return api.ListLinkedAdults200JSONResponse{Items: items, NextOffset: next}, nil
}

// ListProfileOffers lists the offers made to the active adult and, to an
// adult who manages the device household, those of its profiles.
func (s *Server) ListProfileOffers(ctx context.Context, _ api.ListProfileOffersRequestObject) (api.ListProfileOffersResponseObject, error) {
	p := caller(ctx)
	if p.asKid() {
		return nil, errForbidden
	}
	in, err := s.svc.Offers.Repo().To(p.profile.ID)
	if err != nil {
		return nil, err
	}
	var out []*offer.Offer
	if p.manages() {
		if out, err = s.svc.Offers.Repo().From(p.device.HouseholdID); err != nil {
			return nil, err
		}
	}
	incoming, err := s.toOffers(in)
	if err != nil {
		return nil, err
	}
	outgoing, err := s.toOffers(out)
	return api.ListProfileOffers200JSONResponse{Incoming: incoming, Outgoing: outgoing}, err
}

// CreateProfileOffer offers a profile of the device household to a linked
// adult: a kid in it, or an adult whose own household it is.
func (s *Server) CreateProfileOffer(ctx context.Context, r api.CreateProfileOfferRequestObject) (api.CreateProfileOfferResponseObject, error) {
	p := caller(ctx)
	if !p.manages() {
		return nil, errForbidden
	}
	if r.Body.ProfileId != p.profile.ID {
		in, err := s.svc.Accounts.Repo().In(r.Body.ProfileId, p.device.HouseholdID)
		if err != nil || !in {
			return nil, orNotFound(err)
		}
		a, err := s.svc.Accounts.Repo().Get(r.Body.ProfileId)
		if err != nil {
			return nil, err
		}
		if a.Kind == account.Adult && (a.HouseholdID == nil || *a.HouseholdID != p.device.HouseholdID) {
			return nil, errForbidden
		}
	}
	o, err := s.svc.Offers.Create(p.profile, p.device.HouseholdID, r.Body.ProfileId, r.Body.ToProfileId, device.TV(p.device.Platform),
		r.Body.Id, actor(ctx))
	if err != nil {
		return nil, err
	}
	s.offerChanged(o)
	out, err := s.toOffer(o)
	return api.CreateProfileOffer201JSONResponse(out), err
}

// listed returns an offer the active profile lists, and whether it is the
// adult the offer was made to.
func (s *Server) listed(ctx context.Context, id uid.ID) (*offer.Offer, bool, error) {
	p := caller(ctx)
	o, err := s.svc.Offers.Repo().Get(id)
	switch {
	case err != nil:
		return nil, false, err
	case p.asKid():
		return nil, false, errForbidden
	case o.ToID == p.profile.ID:
		return o, true, nil
	case o.HouseholdID == p.device.HouseholdID && p.manages():
		return o, false, nil
	}
	return nil, false, db.ErrNotFound
}

// DeleteProfileOffer declines or withdraws an offer.
func (s *Server) DeleteProfileOffer(ctx context.Context, r api.DeleteProfileOfferRequestObject) (api.DeleteProfileOfferResponseObject, error) {
	o, _, err := s.listed(ctx, r.OfferId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Offers.Repo().Delete(o.ID); err != nil {
		return nil, err
	}
	s.offerChanged(o)
	return api.DeleteProfileOffer204Response{}, nil
}

// AcceptProfileOffer puts an offer's profile into the own household of the
// adult it was made to.
func (s *Server) AcceptProfileOffer(ctx context.Context, r api.AcceptProfileOfferRequestObject) (api.AcceptProfileOfferResponseObject, error) {
	o, mine, err := s.listed(ctx, r.OfferId)
	if err != nil {
		return nil, err
	}
	if !mine {
		return nil, errForbidden
	}
	h, err := s.svc.Offers.Accept(o, device.TV(caller(ctx).device.Platform))
	if err != nil {
		return nil, err
	}
	s.offerChanged(o, h)
	return api.AcceptProfileOffer204Response{}, nil
}
