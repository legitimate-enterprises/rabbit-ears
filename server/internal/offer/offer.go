// Package offer holds an adult's offer of a profile of their household to
// an adult linked with them, and its repository.
package offer

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Offer puts ProfileID, of household HouseholdID, into the own household of
// ToID once ToID accepts it. One is pending per profile and adult offered to.
type Offer struct {
	ID          int64
	ProfileID   int64
	HouseholdID int64
	FromID      int64
	ToID        int64
	CreatedAt   time.Time
}

// Repo persists offers.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, profile_id, household_id, from_id, to_id, created_at`

func scan(s db.Scanner) (*Offer, error) {
	var o Offer
	var created int64
	if err := s.Scan(&o.ID, &o.ProfileID, &o.HouseholdID, &o.FromID, &o.ToID, &created); err != nil {
		return nil, db.Wrap(err)
	}
	o.CreatedAt = db.FromMs(created)
	return &o, nil
}

// Create inserts o and fills ID and CreatedAt; a second offer of a profile
// to one adult is db.ErrConflict.
func (r *Repo) Create(o *Offer) error {
	o.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	res, err := r.d.Exec(`INSERT INTO profile_offer (profile_id, household_id, from_id, to_id, created_at) VALUES (?,?,?,?,?)`,
		o.ProfileID, o.HouseholdID, o.FromID, o.ToID, db.Ms(o.CreatedAt))
	if err != nil {
		return db.Wrap(err)
	}
	o.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one offer or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Offer, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM profile_offer WHERE id = ?`, id))
}

func (r *Repo) list(q string, args ...any) ([]*Offer, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM profile_offer WHERE `+q+` ORDER BY created_at DESC, id DESC`, args...)
	return db.Collect(rows, err, scan)
}

// To lists the offers made to an adult, newest first.
func (r *Repo) To(accountID int64) ([]*Offer, error) { return r.list(`to_id = ?`, accountID) }

// From lists the offers of a household's profiles, newest first.
func (r *Repo) From(householdID int64) ([]*Offer, error) {
	return r.list(`household_id = ?`, householdID)
}

// Delete removes one offer.
func (r *Repo) Delete(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM profile_offer WHERE id = ?`, id)
}

// DeleteBetween removes the offers either of two adults made the other.
func (r *Repo) DeleteBetween(a, b int64) error {
	_, err := r.d.Exec(`DELETE FROM profile_offer WHERE from_id = ?1 AND to_id = ?2 OR from_id = ?2 AND to_id = ?1`, a, b)
	return db.Wrap(err)
}

// DeleteOf removes the offers of a profile, made from one household or, for
// 0, from any.
func (r *Repo) DeleteOf(profileID, householdID int64) error {
	_, err := r.d.Exec(`DELETE FROM profile_offer WHERE profile_id = ?1 AND (?2 = 0 OR household_id = ?2)`, profileID, householdID)
	return db.Wrap(err)
}
