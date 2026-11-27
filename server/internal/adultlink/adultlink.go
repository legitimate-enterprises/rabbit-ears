// Package adultlink holds the links an admin makes between two adults of
// different households, who then see each other, and their repository.
package adultlink

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Link lets two adults see each other, on TVs only when TV is set. It is
// stored once, AID the bytewise lower account id.
type Link struct {
	AID       uid.ID
	BID       uid.ID
	TV        bool
	CreatedAt time.Time
}

// Pair orders two account ids as a link stores them, as the table's
// CHECK (a_id < b_id) compares BLOBs.
func Pair(a, b uid.ID) (uid.ID, uid.ID) {
	if a.Compare(b) > 0 {
		return b, a
	}
	return a, b
}

// Repo persists links.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `a_id, b_id, tv, created_at`

func scan(s db.Scanner) (*Link, error) {
	var l Link
	var created int64
	if err := s.Scan(&l.AID, &l.BID, &l.TV, &created); err != nil {
		return nil, db.Wrap(err)
	}
	l.CreatedAt = db.FromMs(created)
	return &l, nil
}

// Create inserts l, ordering its pair; a pair linked already is
// db.ErrConflict.
func (r *Repo) Create(l *Link) error {
	l.AID, l.BID = Pair(l.AID, l.BID)
	l.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	_, err := r.d.Exec(`INSERT INTO adult_link (a_id, b_id, tv, created_at) VALUES (?,?,?,?)`, l.AID, l.BID, l.TV, db.Ms(l.CreatedAt))
	return db.Wrap(err)
}

// Get returns the link between two adults, in either order, or
// db.ErrNotFound.
func (r *Repo) Get(a, b uid.ID) (*Link, error) {
	a, b = Pair(a, b)
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM adult_link WHERE a_id = ? AND b_id = ?`, a, b))
}

// List pages links, newest first.
func (r *Repo) List(limit, offset int) ([]*Link, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM adult_link ORDER BY created_at DESC, a_id, b_id LIMIT ? OFFSET ?`, limit, offset)
	return db.Collect(rows, err, scan)
}

// SetTV sets whether the link holds on TVs.
func (r *Repo) SetTV(l *Link) error {
	return db.ExecOne(r.d, `UPDATE adult_link SET tv = ? WHERE a_id = ? AND b_id = ?`, l.TV, l.AID, l.BID)
}

// Delete removes the link between two adults, in either order.
func (r *Repo) Delete(a, b uid.ID) error {
	a, b = Pair(a, b)
	return db.ExecOne(r.d, `DELETE FROM adult_link WHERE a_id = ? AND b_id = ?`, a, b)
}
