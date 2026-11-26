package service

import (
	"cmp"
	"database/sql"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/adultlink"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/offer"
)

// Offer errors.
var (
	ErrNotLinked    = errors.New("an offer is between adults an admin linked, on a TV by a link set for TVs")
	ErrNotOfferable = errors.New("the profile is in that adult's own household already, or they have none")
)

// OfferService makes and settles the offers of a household's profiles to
// adults linked with its adults.
type OfferService struct {
	repo *offer.Repo
	d    *sql.DB
}

// NewOfferService returns an OfferService over d.
func NewOfferService(d *sql.DB) *OfferService { return &OfferService{offer.New(d), d} }

// Repo exposes the repository.
func (s *OfferService) Repo() *offer.Repo { return s.repo }

// linked fails with ErrNotLinked unless an admin linked two adults, for a TV
// by a link set for TVs.
func linked(d db.DBTX, a, b int64, tv bool) error {
	l, err := adultlink.New(d).Get(a, b)
	if errors.Is(err, db.ErrNotFound) || err == nil && tv && !l.TV {
		return ErrNotLinked
	}
	return err
}

// offerable resolves the profile and the own household of the adult it is
// offered to, which must not hold it yet.
func offerable(d db.DBTX, profileID, toID int64) (*account.Account, int64, error) {
	accounts := account.New(d)
	p, err := accounts.Get(profileID)
	if err != nil {
		return nil, 0, err
	}
	if p.Pending() || p.RevokedAt != nil {
		return nil, 0, ErrNotJoined
	}
	to, err := accounts.Get(toID)
	if err != nil || to.HouseholdID == nil {
		return nil, 0, cmp.Or(err, ErrNotOfferable)
	}
	if in, err := accounts.In(p.ID, *to.HouseholdID); err != nil || in {
		return nil, 0, cmp.Or(err, ErrNotOfferable)
	}
	return p, *to.HouseholdID, nil
}

// Create offers a profile of householdID, from an adult on a device there,
// to an adult linked with them; tv is whether the device is a TV. A
// profile not in the household is db.ErrNotFound.
func (s *OfferService) Create(from *account.Account, householdID, profileID, toID int64, tv bool) (*offer.Offer, error) {
	o := &offer.Offer{ProfileID: profileID, HouseholdID: householdID, FromID: from.ID, ToID: toID}
	return o, db.InTx(s.d, func(d db.DBTX) error {
		if in, err := account.New(d).In(profileID, householdID); err != nil || !in {
			return cmp.Or(err, db.ErrNotFound)
		}
		if err := linked(d, from.ID, toID, tv); err != nil {
			return err
		}
		if _, _, err := offerable(d, profileID, toID); err != nil {
			return err
		}
		return offer.New(d).Create(o)
	})
}

// Accept puts an offer's profile into the own household of the adult it was
// made to, who accepts it on a device that is a TV or not, and deletes the
// offer. It returns that household.
func (s *OfferService) Accept(o *offer.Offer, tv bool) (int64, error) {
	var h int64
	return h, db.InTx(s.d, func(d db.DBTX) error {
		if err := linked(d, o.FromID, o.ToID, tv); err != nil {
			return err
		}
		p, into, err := offerable(d, o.ProfileID, o.ToID)
		if err != nil {
			return err
		}
		if err := join(d, p, into); err != nil {
			return err
		}
		h = into
		return offer.New(d).Delete(o.ID)
	})
}
