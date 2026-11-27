package httpapi

import (
	"context"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
)

func toIdentityReview(r *review.Review) api.IdentityReview {
	out := api.IdentityReview{Id: r.ID, LibraryId: r.LibraryID, ItemId: r.ItemID, Reason: api.IdentityReviewReason(r.Reason),
		RaisedAt: r.RaisedAt, DismissedAt: r.DismissedAt, Evidence: []api.IdentityEvidence{}}
	out.ItemKind, out.Title, out.Path = text(r.ItemKind), text(r.Title), text(r.Path)
	for _, e := range r.Evidence {
		ev := api.IdentityEvidence{Role: api.IdentityEvidenceRole(e.Role), ExternalId: text(e.ExternalID), Title: text(e.Title),
			Year: e.Year, Country: text(e.Country), Path: text(e.Path)}
		if e.Provider != "" {
			p := api.IdentityEvidenceProvider(e.Provider)
			ev.Provider = &p
		}
		out.Evidence = append(out.Evidence, ev)
	}
	return out
}

// text is s for an optional property, absent when empty.
func text(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListIdentityReviews lists reviews newest first.
func (s *Server) ListIdentityReviews(_ context.Context, r api.ListIdentityReviewsRequestObject) (api.ListIdentityReviewsResponseObject, error) {
	all, err := s.svc.Reviews.Repo().List(review.Filter{LibraryID: r.Params.LibraryId, Reason: string(or(r.Params.Reason, "")),
		Dismissed: r.Params.Dismissed})
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	return api.ListIdentityReviews200JSONResponse{Items: mapAll(rows, toIdentityReview), NextOffset: next}, nil
}

// GetIdentityReview returns one review.
func (s *Server) GetIdentityReview(_ context.Context, r api.GetIdentityReviewRequestObject) (api.GetIdentityReviewResponseObject, error) {
	rv, err := s.svc.Reviews.Repo().Get(r.IdentityReviewId)
	if err != nil {
		return nil, err
	}
	return api.GetIdentityReview200JSONResponse(toIdentityReview(rv)), nil
}

// UpdateIdentityReview dismisses or reopens a review. One already
// dismissed keeps when it was.
func (s *Server) UpdateIdentityReview(_ context.Context, r api.UpdateIdentityReviewRequestObject) (api.UpdateIdentityReviewResponseObject, error) {
	rv, err := s.svc.Reviews.Repo().Get(r.IdentityReviewId)
	if err != nil {
		return nil, err
	}
	if r.Body.Dismissed != (rv.DismissedAt != nil) {
		rv.DismissedAt = nil
		if r.Body.Dismissed {
			now := time.Now().UTC()
			rv.DismissedAt = &now
		}
		if err := s.svc.Reviews.Repo().SetDismissed(rv.ID, rv.DismissedAt); err != nil {
			return nil, err
		}
	}
	return api.UpdateIdentityReview200JSONResponse(toIdentityReview(rv)), nil
}
