package service

import (
	"errors"
	"slices"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/offer"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
)

// Membership errors.
var (
	ErrNotJoined     = errors.New("only a kid or an adult who has joined and is not revoked joins a household")
	ErrLastHousehold = errors.New("a profile never leaves its only household; delete it instead")
	ErrOwnerStays    = errors.New("a household's owner never leaves it")
)

// Join puts a, a kid or a joined adult who is not revoked, into a household:
// a kid stands in it as in any other, an adult is only present there. One in
// it already stays as it is.
func (s *HouseholdService) Join(a *account.Account, householdID int64) error {
	return db.InTx(s.d, func(d db.DBTX) error {
		if _, err := household.New(d).Get(householdID); err != nil {
			return err
		}
		if a.Pending() || a.RevokedAt != nil {
			return ErrNotJoined
		}
		return join(d, a, householdID)
	})
}

func join(d db.DBTX, a *account.Account, householdID int64) error {
	accounts := account.New(d)
	if in, err := accounts.In(a.ID, householdID); err != nil || in {
		return err
	}
	return accounts.AddMember(householdID, a.ID)
}

// Leave takes a out of a household that is not its only one.
func (s *HouseholdService) Leave(a *account.Account, householdID int64) (*SignedOut, error) {
	out := &SignedOut{}
	return out, db.InTx(s.d, func(d db.DBTX) error { return leave(d, a, householdID, out) })
}

// leave takes a out of a household. Devices there acting as a go back to
// Who's watching, a's own devices there sign out, and a's stations playing
// what only that household shared with it go. A kid stands equally
// in each household, so leaving the one account.household_id names puts
// another there; an adult's is their own household, which no other becomes.
func leave(d db.DBTX, a *account.Account, householdID int64, out *SignedOut) error {
	accounts := account.New(d)
	hs, err := accounts.Households(a)
	switch {
	case err != nil:
		return err
	case !slices.Contains(hs, householdID):
		return db.ErrNotFound
	case len(hs) == 1:
		return ErrLastHousehold
	}
	if err := rehomeOut(d, a, householdID, hs); err != nil {
		return err
	}
	if err := station.New(d).DeleteUnseen(0, a.ID); err != nil {
		return err
	}
	if err := offer.New(d).DeleteOf(a.ID, householdID); err != nil {
		return err
	}
	devs, err := device.New(d).ReachingIn(a.ID, householdID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var theirs []*device.Device
	for _, v := range devs {
		if v.AccountID == a.ID {
			theirs = append(theirs, v)
			continue
		}
		if err := device.New(d).Reset(v.ID, now); err != nil {
			return err
		}
		out.Reset = append(out.Reset, v.ID)
		if err := endSessions(d, v.ID, now, out); err != nil {
			return err
		}
	}
	return signOutDevices(d, theirs, out)
}

// rehomeOut takes a's membership of a household away, hs being every
// household a is in, own first.
func rehomeOut(d db.DBTX, a *account.Account, householdID int64, hs []int64) error {
	accounts := account.New(d)
	if a.HouseholdID == nil || *a.HouseholdID != householdID {
		return accounts.RemoveMember(householdID, a.ID)
	}
	h, err := household.New(d).Get(householdID)
	switch {
	case err != nil:
		return err
	case h.AdultID == a.ID:
		return ErrOwnerStays
	case a.Kind == account.Kid:
		return accounts.Rehome(a, &hs[1])
	}
	return accounts.Rehome(a, nil)
}

// CreateFor makes a household owned by adult and moves the adult into it.
func (s *HouseholdService) CreateFor(name string, adult *account.Account) (*household.Household, error) {
	var h *household.Household
	err := db.InTx(s.d, func(d db.DBTX) (err error) {
		h, err = newHousehold(d, name, adult)
		return err
	})
	return h, err
}

func newHousehold(d db.DBTX, name string, adult *account.Account) (*household.Household, error) {
	h := &household.Household{Name: name, AdultID: adult.ID}
	if err := household.New(d).Create(h); err != nil {
		return nil, err
	}
	adult.HouseholdID = &h.ID
	return h, account.New(d).Update(adult)
}

// Delete removes a household with its devices and everyone only it holds
// but its adult, signing out the devices acting as them; who stays is as
// vacate leaves them. Deleting the last active admin with it is
// ErrLastAdmin.
func (s *HouseholdService) Delete(id int64) (*SignedOut, error) {
	out := &SignedOut{}
	return out, db.InTx(s.d, func(d db.DBTX) error {
		if _, err := household.New(d).Get(id); err != nil {
			return err
		}
		gone, err := account.New(d).DeletedWithHousehold(id)
		if err != nil {
			return err
		}
		if err := removing(d, gone, out); err != nil {
			return err
		}
		if err := vacate(d, id, out); err != nil {
			return err
		}
		return keepingAnAdmin(d, func() error {
			if err := household.New(d).Delete(id); err != nil {
				return err
			}
			return station.New(d).DeleteUnseen(0, 0)
		})
	})
}

// vacate readies a household for deleting in the same transaction, since
// account.household_id cascades: its devices sign out, and those it names
// there that stay move out first. A kid another household holds takes that
// one as account.household_id; its owner, and an adult of it present
// elsewhere, are left without a household.
func vacate(d db.DBTX, householdID int64, out *SignedOut) error {
	devs, err := device.New(d).InHousehold(householdID)
	if err != nil {
		return err
	}
	if err := signOutDevices(d, devs, out); err != nil {
		return err
	}
	accounts := account.New(d)
	staying, err := accounts.Staying(householdID)
	if err != nil {
		return err
	}
	for _, a := range staying {
		var to *int64
		if a.Kind == account.Kid {
			hs, err := accounts.Households(a)
			if err != nil {
				return err
			}
			to = &hs[1]
		}
		if err := accounts.Rehome(a, to); err != nil {
			return err
		}
	}
	return nil
}
