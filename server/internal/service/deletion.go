package service

import (
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
)

// Deletion is what deleting an adult's own account takes with it: the
// accounts in Gone. When the adult owns a household, NewOwner takes it
// over; without another adult to, Household is set and it goes too, with
// every member.
type Deletion struct {
	Gone      []*account.Account
	NewOwner  *account.Account
	Household bool
}

// PlanDeletion says what deleting a, an adult, takes with it.
func (s *AccountService) PlanDeletion(a *account.Account) (*Deletion, error) {
	return planDeletion(s.d, a)
}

func planDeletion(d db.DBTX, a *account.Account) (*Deletion, error) {
	out := &Deletion{Gone: []*account.Account{a}}
	if a.HouseholdID == nil {
		return out, nil
	}
	h, err := household.New(d).Get(*a.HouseholdID)
	if err != nil || h.AdultID != a.ID {
		return out, err
	}
	accounts := account.New(d)
	out.NewOwner, err = accounts.EarliestJoined(h.ID, a.ID)
	if !errors.Is(err, db.ErrNotFound) {
		return out, err
	}
	out.NewOwner, out.Household = nil, true
	out.Gone, err = accounts.DeletedWith(a.ID)
	return out, err
}

// DeleteOwn deletes a as PlanDeletion says, signing out the devices of
// every account it takes. Ownership passes in the same transaction before
// the delete, which would otherwise cascade to the household.
func (s *AccountService) DeleteOwn(a *account.Account) (*SignedOut, error) {
	out := &SignedOut{}
	return out, db.InTx(s.d, func(d db.DBTX) error {
		plan, err := planDeletion(d, a)
		if err != nil {
			return err
		}
		if plan.NewOwner != nil {
			if err := household.New(d).SetAdult(*a.HouseholdID, plan.NewOwner.ID); err != nil {
				return err
			}
		}
		return deleteAccount(d, a.ID, out)
	})
}

// Confirm checks an adult's password again before an action it guards,
// counting a wrong one against source as SignIn does.
func (s *AuthService) Confirm(source string, a *account.Account, password string) error {
	settle, err := s.attempt(source)
	if err != nil {
		return err
	}
	ok := checkSecret(a.PasswordHash, password)
	settle(!ok)
	if !ok {
		return ErrPasswordWrong
	}
	return nil
}
