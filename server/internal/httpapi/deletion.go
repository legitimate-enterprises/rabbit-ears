package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// ownDeletion plans deleting the active adult's own account.
func (s *Server) ownDeletion(ctx context.Context) (*service.Deletion, bool, error) {
	if caller(ctx).asKid() {
		return nil, false, errForbidden
	}
	plan, err := s.svc.Accounts.PlanDeletion(caller(ctx).profile)
	if err != nil {
		return nil, false, err
	}
	refused, err := s.lastAdmin(plan.Gone)
	return plan, refused, err
}

// GetAccountDeletion tells the active adult what deleting their account
// takes with it, for the confirmation to say.
func (s *Server) GetAccountDeletion(ctx context.Context, _ api.GetAccountDeletionRequestObject) (api.GetAccountDeletionResponseObject, error) {
	plan, refused, err := s.ownDeletion(ctx)
	if err != nil {
		return nil, err
	}
	out := api.AccountDeletion{Outcome: api.AccountDeletionOutcomeAccount, KidNames: []string{}}
	switch {
	case refused:
		out.Outcome = api.AccountDeletionOutcomeRefused
	case plan.NewOwner != nil:
		out.Outcome, out.NewOwnerName = api.AccountDeletionOutcomeTransfer, &plan.NewOwner.Name
	case plan.Household:
		out.Outcome = api.AccountDeletionOutcomeHousehold
		for _, a := range plan.Gone {
			if a.Kind == account.Kid {
				out.KidNames = append(out.KidNames, a.Name)
			}
		}
	}
	return api.GetAccountDeletion200JSONResponse(out), nil
}

// DeleteOwnAccount deletes the active adult's own account once they give
// its password again.
func (s *Server) DeleteOwnAccount(ctx context.Context, r api.DeleteOwnAccountRequestObject) (api.DeleteOwnAccountResponseObject, error) {
	if caller(ctx).asKid() {
		return nil, errForbidden
	}
	a := caller(ctx).profile
	if err := s.svc.Auth.Confirm(service.SourceKey(clientIP(ctx)), a, r.Body.Password); err != nil {
		return nil, err
	}
	plan, refused, err := s.ownDeletion(ctx)
	if err != nil {
		return nil, err
	}
	refusal := fail(http.StatusConflict, api.ErrorCodeConflict,
		"the server's only admin cannot delete their account; make another account an admin first")
	if refused {
		return nil, refusal
	}
	err = s.removed(plan.Gone, func() (*service.SignedOut, error) { return s.svc.Accounts.DeleteOwn(a) })
	if errors.Is(err, service.ErrLastAdmin) {
		return nil, refusal
	}
	if err != nil {
		return nil, err
	}
	audit(ctx, "Deleted their own account "+a.Name)
	return api.DeleteOwnAccount204Response{}, nil
}
