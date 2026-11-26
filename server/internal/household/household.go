// Package household holds the household model (adults and their kids) and
// its repository. Membership is the membership view: account.household_id
// and household_member.
package household

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Household is owned by exactly one adult.
type Household struct {
	ID        int64
	Name      string
	AdultID   int64
	CreatedAt time.Time
}

// Repo persists households.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, name, adult_id, created_at`

func scan(s db.Scanner) (*Household, error) {
	var h Household
	var created int64
	if err := s.Scan(&h.ID, &h.Name, &h.AdultID, &created); err != nil {
		return nil, db.Wrap(err)
	}
	h.CreatedAt = db.FromMs(created)
	return &h, nil
}

// Create inserts h; an adult owns at most one household.
func (r *Repo) Create(h *Household) error {
	h.CreatedAt = time.Now().UTC()
	res, err := r.d.Exec(`INSERT INTO household (name, adult_id, created_at) VALUES (?,?,?)`, h.Name, h.AdultID, db.Ms(h.CreatedAt))
	if err != nil {
		return db.Wrap(err)
	}
	h.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one household or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Household, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM household WHERE id = ?`, id))
}

// OwnedBy returns the household an adult owns or db.ErrNotFound.
func (r *Repo) OwnedBy(adultID int64) (*Household, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM household WHERE adult_id = ?`, adultID))
}

// List returns all households by name.
func (r *Repo) List() ([]*Household, error) {
	rows, err := r.d.Query(`SELECT ` + cols + ` FROM household ORDER BY name COLLATE NOCASE, id`)
	return db.Collect(rows, err, scan)
}

// Sized is a household with how many profiles are in it: its kids and the
// adults who have joined it, none revoked.
type Sized struct {
	Household
	Profiles int
}

// Of lists the households a profile is in, its own first, each with its
// size.
func (r *Repo) Of(accountID int64) ([]*Sized, error) {
	rows, err := r.d.Query(`SELECT h.id, h.name, h.adult_id, h.created_at,
			(SELECT COUNT(*) FROM membership m JOIN account a ON a.id = m.account_id WHERE m.household_id = h.id
				AND a.revoked_at IS NULL AND (a.kind = 'kid' OR a.password_hash IS NOT NULL))
		FROM household h JOIN membership mine ON mine.household_id = h.id AND mine.account_id = ?1
		ORDER BY h.id <> COALESCE((SELECT household_id FROM account WHERE id = ?1), 0), h.id`, accountID)
	return db.Collect(rows, err, func(s db.Scanner) (*Sized, error) {
		var v Sized
		var created int64
		if err := s.Scan(&v.ID, &v.Name, &v.AdultID, &created, &v.Profiles); err != nil {
			return nil, db.Wrap(err)
		}
		v.CreatedAt = db.FromMs(created)
		return &v, nil
	})
}

// Update renames a household.
func (r *Repo) Update(h *Household) error {
	return db.ExecOne(r.d, `UPDATE household SET name = ? WHERE id = ?`, h.Name, h.ID)
}

// SetAdult hands a household to another of its adults.
func (r *Repo) SetAdult(id, adultID int64) error {
	return db.ExecOne(r.d, `UPDATE household SET adult_id = ? WHERE id = ?`, adultID, id)
}

// Delete removes a household; every member still in it cascades, as do its
// devices.
func (r *Repo) Delete(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM household WHERE id = ?`, id)
}
