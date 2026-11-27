// Package offer holds an adult's offer of a profile of their household to
// an adult linked with them, and its repository.
package offer

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Offer puts ProfileID, of household HouseholdID, into the own household of
// ToID once ToID accepts it. One is pending per profile and adult offered to.
type Offer struct {
	ID          uid.ID
	ProfileID   uid.ID
	HouseholdID uid.ID
	FromID      uid.ID
	ToID        uid.ID
	CreatedAt   time.Time
	ModifiedAt  time.Time
	CreatedBy   *uid.ID
	ModifiedBy  *uid.ID
}

// Repo persists offers.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, profile_id, household_id, from_id, to_id, created_at, modified_at, created_by, modified_by`

func scan(s db.Scanner) (*Offer, error) {
	var o Offer
	var created, modified int64
	if err := s.Scan(&o.ID, &o.ProfileID, &o.HouseholdID, &o.FromID, &o.ToID, &created, &modified, &o.CreatedBy, &o.ModifiedBy); err != nil {
		return nil, db.Wrap(err)
	}
	o.CreatedAt, o.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &o, nil
}

// Create inserts o, minting its ID unless set, and fills the audit fields;
// a second offer of a profile to one adult is db.ErrConflict.
func (r *Repo) Create(o *Offer, by *uid.ID) error {
	if o.ID.IsZero() {
		o.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	o.CreatedAt, o.ModifiedAt, o.CreatedBy, o.ModifiedBy = now, now, by, by
	_, err := r.d.Exec(`INSERT INTO profile_offer (id, profile_id, household_id, from_id, to_id, created_at, modified_at, created_by, modified_by)
		VALUES (?,?,?,?,?,?,?,?,?)`, o.ID, o.ProfileID, o.HouseholdID, o.FromID, o.ToID, db.Ms(now), db.Ms(now), by, by)
	return db.Wrap(err)
}

// Get returns one offer or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Offer, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM profile_offer WHERE id = ?`, id))
}

func (r *Repo) list(q string, args ...any) ([]*Offer, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM profile_offer WHERE `+q+` ORDER BY created_at DESC, id DESC`, args...)
	return db.Collect(rows, err, scan)
}

// To lists the offers made to an adult, newest first.
func (r *Repo) To(accountID uid.ID) ([]*Offer, error) { return r.list(`to_id = ?`, accountID) }

// From lists the offers of a household's profiles, newest first.
func (r *Repo) From(householdID uid.ID) ([]*Offer, error) {
	return r.list(`household_id = ?`, householdID)
}

// Delete removes one offer.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM profile_offer WHERE id = ?`, id)
}

// DeleteBetween removes the offers either of two adults made the other.
func (r *Repo) DeleteBetween(a, b uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM profile_offer WHERE from_id = ?1 AND to_id = ?2 OR from_id = ?2 AND to_id = ?1`, a, b)
	return db.Wrap(err)
}

// DeleteOf removes the offers of a profile, made from one household or, for
// nil, from any.
func (r *Repo) DeleteOf(profileID uid.ID, householdID *uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM profile_offer WHERE profile_id = ?1 AND (?2 IS NULL OR household_id = ?2)`, profileID, householdID)
	return db.Wrap(err)
}
