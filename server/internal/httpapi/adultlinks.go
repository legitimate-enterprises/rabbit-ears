package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/adultlink"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Adult links: an admin lets two adults of different households see each
// other.

func toAdultLink(l *adultlink.Link) api.AdultLink {
	return api.AdultLink{AccountId: l.AID, OtherAccountId: l.BID, Tv: l.TV, CreatedAt: l.CreatedAt}
}

// linkable resolves an adult who may be linked: one who has joined and is
// not revoked.
func (s *Server) linkable(id uid.ID, field string) (*account.Account, error) {
	a, err := s.svc.Accounts.Repo().Get(id)
	if errors.Is(err, db.ErrNotFound) || err == nil && (a.Kind != account.Adult || a.Pending() || a.RevokedAt != nil) {
		return nil, invalid(field, "not an adult who has joined")
	}
	return a, err
}

// ListAdultLinks pages the links, newest first.
func (s *Server) ListAdultLinks(_ context.Context, r api.ListAdultLinksRequestObject) (api.ListAdultLinksResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.AdultLinks.Repo().List(limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListAdultLinks200JSONResponse{Items: mapAll(rows, toAdultLink), NextOffset: next}, nil
}

// CreateAdultLink links two adults of different households.
func (s *Server) CreateAdultLink(ctx context.Context, r api.CreateAdultLinkRequestObject) (api.CreateAdultLinkResponseObject, error) {
	a, err := s.linkable(r.Body.AccountId, "accountId")
	if err != nil {
		return nil, err
	}
	b, err := s.linkable(r.Body.OtherAccountId, "otherAccountId")
	if err != nil {
		return nil, err
	}
	if a.ID == b.ID {
		return nil, invalid("otherAccountId", "an adult is not linked with themself")
	}
	if a.HouseholdID != nil && b.HouseholdID != nil && *a.HouseholdID == *b.HouseholdID {
		return nil, fail(http.StatusConflict, api.ErrorCodeConflict, "the adults are of one household")
	}
	l := &adultlink.Link{AID: a.ID, BID: b.ID, TV: or(r.Body.Tv, false)}
	if err := s.svc.AdultLinks.Repo().Create(l); err != nil {
		return nil, err
	}
	audit(ctx, "Linked "+a.Name+" with "+b.Name)
	return api.CreateAdultLink201JSONResponse(toAdultLink(l)), nil
}

// GetAdultLink returns the link between two adults.
func (s *Server) GetAdultLink(_ context.Context, r api.GetAdultLinkRequestObject) (api.GetAdultLinkResponseObject, error) {
	l, err := s.svc.AdultLinks.Repo().Get(r.AccountId, r.OtherAccountId)
	if err != nil {
		return nil, err
	}
	return api.GetAdultLink200JSONResponse(toAdultLink(l)), nil
}

// ReplaceAdultLink sets whether a link holds on TVs.
func (s *Server) ReplaceAdultLink(_ context.Context, r api.ReplaceAdultLinkRequestObject) (api.ReplaceAdultLinkResponseObject, error) {
	l, err := s.svc.AdultLinks.Repo().Get(r.AccountId, r.OtherAccountId)
	if err != nil {
		return nil, err
	}
	l.TV = r.Body.Tv
	if err := s.svc.AdultLinks.Repo().SetTV(l); err != nil {
		return nil, err
	}
	return api.ReplaceAdultLink200JSONResponse(toAdultLink(l)), nil
}

// DeleteAdultLink unlinks two adults.
func (s *Server) DeleteAdultLink(ctx context.Context, r api.DeleteAdultLinkRequestObject) (api.DeleteAdultLinkResponseObject, error) {
	if err := s.svc.AdultLinks.Delete(r.AccountId, r.OtherAccountId); err != nil {
		return nil, err
	}
	audit(ctx, "Unlinked two adults")
	return api.DeleteAdultLink204Response{}, nil
}
