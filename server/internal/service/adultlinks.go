package service

import (
	"database/sql"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/adultlink"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/offer"
)

// AdultLinkService manages the links between adults of different
// households.
type AdultLinkService struct {
	repo *adultlink.Repo
	d    *sql.DB
}

// NewAdultLinkService returns an AdultLinkService over d.
func NewAdultLinkService(d *sql.DB) *AdultLinkService { return &AdultLinkService{adultlink.New(d), d} }

// Repo exposes the repository.
func (s *AdultLinkService) Repo() *adultlink.Repo { return s.repo }

// Delete unlinks two adults and deletes the offers between them.
func (s *AdultLinkService) Delete(a, b int64) error {
	return db.InTx(s.d, func(d db.DBTX) error {
		if err := adultlink.New(d).Delete(a, b); err != nil {
			return err
		}
		return offer.New(d).DeleteBetween(a, b)
	})
}
