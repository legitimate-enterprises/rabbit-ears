// Package friend holds friendships between two adults and the requests
// that make them, with their repository.
package friend

import (
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/adultlink"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Request is an adult's request to be friends with whoever holds Email.
// Nothing of it changes once sent.
type Request struct {
	ID         uid.ID
	FromID     uid.ID
	Email      string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	ModifiedAt time.Time
	CreatedBy  *uid.ID
	ModifiedBy *uid.ID
}

// Repo persists friendships, requests and declines.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, from_id, email, expires_at, created_at, modified_at, created_by, modified_by`

func scan(s db.Scanner) (*Request, error) {
	var r Request
	var expires, created, modified int64
	if err := s.Scan(&r.ID, &r.FromID, &r.Email, &expires, &created, &modified, &r.CreatedBy, &r.ModifiedBy); err != nil {
		return nil, db.Wrap(err)
	}
	r.ExpiresAt, r.CreatedAt, r.ModifiedAt = db.FromMs(expires), db.FromMs(created), db.FromMs(modified)
	return &r, nil
}

// Send stores q, minting its ID unless set, and returns it; when its
// sender has one to its email that has not expired, it returns that one
// instead. It reads nothing of whoever holds the email. An ID in use is
// db.ErrConflict.
func (r *Repo) Send(q *Request, now time.Time) (*Request, error) {
	var out *Request
	return out, db.InTx(r.d, func(d db.DBTX) error {
		if _, err := d.Exec(`DELETE FROM friend_request WHERE from_id = ? AND email = ? AND expires_at <= ?`, q.FromID, q.Email,
			db.Ms(now)); err != nil {
			return db.Wrap(err)
		}
		got, err := scan(d.QueryRow(`SELECT `+cols+` FROM friend_request WHERE from_id = ? AND email = ?`, q.FromID, q.Email))
		if err == nil {
			out = got
			return nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return err
		}
		if q.ID.IsZero() {
			q.ID = uid.New()
		}
		q.CreatedAt, q.ModifiedAt, q.CreatedBy, q.ModifiedBy = now, now, &q.FromID, &q.FromID
		_, err = d.Exec(`INSERT INTO friend_request (`+cols+`) VALUES (?,?,?,?,?,?,?,?)`, q.ID, q.FromID, q.Email, db.Ms(q.ExpiresAt),
			db.Ms(now), db.Ms(now), q.CreatedBy, q.ModifiedBy)
		out = q
		return db.Wrap(err)
	})
}

// Get returns one request or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Request, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM friend_request WHERE id = ?`, id))
}

// Sent lists an adult's requests that have not expired at now, newest
// first.
func (r *Repo) Sent(fromID uid.ID, now time.Time) ([]*Request, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM friend_request WHERE from_id = ? AND expires_at > ? ORDER BY created_at DESC, id`,
		fromID, db.Ms(now))
	return db.Collect(rows, err, scan)
}

// Received lists the requests to the email of the adult accountID, but
// their own, that have not expired at now and they have not declined,
// newest first.
func (r *Repo) Received(accountID uid.ID, email string, now time.Time) ([]*Request, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM friend_request r WHERE email = ?2 AND from_id <> ?1 AND expires_at > ?3
		AND NOT EXISTS (SELECT 1 FROM friend_request_decline x WHERE x.account_id = ?1 AND x.request_id = r.id)
		ORDER BY created_at DESC, id`, accountID, email, db.Ms(now))
	return db.Collect(rows, err, scan)
}

// Decline records the adult's decline of a request; declining twice is a
// no-op.
func (r *Repo) Decline(accountID, requestID uid.ID, now time.Time) error {
	_, err := r.d.Exec(`INSERT OR IGNORE INTO friend_request_decline (account_id, request_id, created_at) VALUES (?,?,?)`,
		accountID, requestID, db.Ms(now))
	return db.Wrap(err)
}

// Delete removes a request and its declines.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM friend_request WHERE id = ?`, id)
}

// DeleteBetween removes the requests either of two adults sent to the
// other's email.
func (r *Repo) DeleteBetween(a uid.ID, aEmail string, b uid.ID, bEmail string) error {
	_, err := r.d.Exec(`DELETE FROM friend_request WHERE from_id = ? AND email = ? OR from_id = ? AND email = ?`, a, bEmail, b, aEmail)
	return db.Wrap(err)
}

// Expire removes the requests that expired by now, and reports how many.
func (r *Repo) Expire(now time.Time) (int64, error) {
	res, err := r.d.Exec(`DELETE FROM friend_request WHERE expires_at <= ?`, db.Ms(now))
	if err != nil {
		return 0, db.Wrap(err)
	}
	return res.RowsAffected()
}

// Befriend makes two adults friends; friends already stay as they are.
func (r *Repo) Befriend(a, b uid.ID, now time.Time) error {
	a, b = adultlink.Pair(a, b)
	_, err := r.d.Exec(`INSERT OR IGNORE INTO friendship (a_id, b_id, created_at) VALUES (?,?,?)`, a, b, db.Ms(now))
	return db.Wrap(err)
}

// Unfriend ends the friendship of two adults, in either order, or is
// db.ErrNotFound.
func (r *Repo) Unfriend(a, b uid.ID) error {
	a, b = adultlink.Pair(a, b)
	return db.ExecOne(r.d, `DELETE FROM friendship WHERE a_id = ? AND b_id = ?`, a, b)
}
