// Package household holds the household model (adults and their kids) and
// its repository. Membership is the membership view: account.household_id
// and household_member.
package household

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Household is owned by exactly one adult.
type Household struct {
	ID         uid.ID
	Name       string
	AdultID    uid.ID
	CreatedAt  time.Time
	ModifiedAt time.Time
	CreatedBy  *uid.ID
	ModifiedBy *uid.ID
}

// Repo persists households.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, name, adult_id, created_at, modified_at, created_by, modified_by`

func scan(s db.Scanner, extra ...any) (*Household, error) {
	var h Household
	var created, modified int64
	if err := s.Scan(append([]any{&h.ID, &h.Name, &h.AdultID, &created, &modified, &h.CreatedBy, &h.ModifiedBy}, extra...)...); err != nil {
		return nil, db.Wrap(err)
	}
	h.CreatedAt, h.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &h, nil
}

// Create inserts h, minting its ID unless set; an adult owns at most one
// household.
func (r *Repo) Create(h *Household, by *uid.ID) error {
	if h.ID.IsZero() {
		h.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	h.CreatedAt, h.ModifiedAt, h.CreatedBy, h.ModifiedBy = now, now, by, by
	_, err := r.d.Exec(`INSERT INTO household (id, name, adult_id, created_at, modified_at, created_by, modified_by) VALUES (?,?,?,?,?,?,?)`,
		h.ID, h.Name, h.AdultID, db.Ms(now), db.Ms(now), by, by)
	return db.Wrap(err)
}

// Get returns one household or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Household, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM household WHERE id = ?`, id))
}

// OwnedBy returns the household an adult owns or db.ErrNotFound.
func (r *Repo) OwnedBy(adultID uid.ID) (*Household, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM household WHERE adult_id = ?`, adultID))
}

// List returns all households by name.
func (r *Repo) List() ([]*Household, error) {
	rows, err := r.d.Query(`SELECT ` + cols + ` FROM household ORDER BY name COLLATE NOCASE, id`)
	return db.Collect(rows, err, func(s db.Scanner) (*Household, error) { return scan(s) })
}

// Sized is a household with how many profiles are in it: its kids and the
// adults who have joined it, none revoked.
type Sized struct {
	Household
	Profiles int
}

// Of lists the households a profile is in, its own first, each with its
// size.
func (r *Repo) Of(accountID uid.ID) ([]*Sized, error) {
	rows, err := r.d.Query(`SELECT h.id, h.name, h.adult_id, h.created_at, h.modified_at, h.created_by, h.modified_by,
			(SELECT COUNT(*) FROM membership m JOIN account a ON a.id = m.account_id WHERE m.household_id = h.id
				AND a.revoked_at IS NULL AND (a.kind = 'kid' OR a.password_hash IS NOT NULL))
		FROM household h JOIN membership mine ON mine.household_id = h.id AND mine.account_id = ?1
		ORDER BY h.id IS NOT (SELECT household_id FROM account WHERE id = ?1), h.created_at, h.id`, accountID)
	return db.Collect(rows, err, func(s db.Scanner) (*Sized, error) {
		var n int
		h, err := scan(s, &n)
		if err != nil {
			return nil, err
		}
		return &Sized{*h, n}, nil
	})
}

// Update renames a household.
func (r *Repo) Update(h *Household, by *uid.ID) error {
	h.ModifiedAt, h.ModifiedBy = time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE household SET name = ?, modified_at = ?, modified_by = ? WHERE id = ?`,
		h.Name, db.Ms(h.ModifiedAt), by, h.ID)
}

// SetAdult hands a household to another of its adults.
func (r *Repo) SetAdult(id, adultID uid.ID, by *uid.ID) error {
	return db.ExecOne(r.d, `UPDATE household SET adult_id = ?, modified_at = ?, modified_by = ? WHERE id = ?`,
		adultID, db.Ms(time.Now().UTC()), by, id)
}

// Delete removes a household; every member still in it cascades, as do its
// devices.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM household WHERE id = ?`, id)
}
