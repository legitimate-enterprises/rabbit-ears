// Package devicelink holds the links by which a TV is signed in with a code
// an adult approves elsewhere, and their repository.
package devicelink

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Statuses. Expired is never stored: StatusAt derives it.
const (
	Pending  = "pending"
	Approved = "approved"
	Denied   = "denied"
	Expired  = "expired"
)

// Link is one device's request to be signed in. It is decided once, and an
// approved link hands its device token over once (CollectedAt).
type Link struct {
	ID            int64
	Code          string
	PollTokenHash string
	DeviceName    string
	Platform      string
	Source        string // the asking device's address, for the per-address limit
	Status        string // as decided: Pending, Approved or Denied
	AccountID     *int64 // the adult who decided
	HouseholdID   *int64 // that adult's household
	DeviceID      *int64 // the device it signed in, while that device exists
	DecidedAt     *time.Time
	CollectedAt   *time.Time
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

// StatusAt is the status a client sees at now: a pending link, or an
// approved one never collected, is Expired from ExpiresAt on.
func (l *Link) StatusAt(now time.Time) string {
	if now.Before(l.ExpiresAt) || l.Status == Denied || l.CollectedAt != nil {
		return l.Status
	}
	return Expired
}

// Repo persists device links.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, code, poll_token_hash, device_name, platform, source, status, account_id, household_id, device_id,
	decided_at, collected_at, expires_at, created_at`

func ip64(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func scan(s db.Scanner) (*Link, error) {
	var l Link
	var acct, hh, dev, decided, collected sql.NullInt64
	var exp, created int64
	if err := s.Scan(&l.ID, &l.Code, &l.PollTokenHash, &l.DeviceName, &l.Platform, &l.Source, &l.Status, &acct, &hh, &dev,
		&decided, &collected, &exp, &created); err != nil {
		return nil, db.Wrap(err)
	}
	l.AccountID, l.HouseholdID, l.DeviceID = ip64(acct), ip64(hh), ip64(dev)
	l.DecidedAt, l.CollectedAt, l.ExpiresAt, l.CreatedAt = db.FromNullMs(decided), db.FromNullMs(collected), db.FromMs(exp), db.FromMs(created)
	return &l, nil
}

func (r *Repo) list(q string, args ...any) ([]*Link, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// Create inserts a pending link and fills ID, Status and CreatedAt; codes
// and poll tokens are unique.
func (r *Repo) Create(l *Link) error {
	l.Status, l.CreatedAt = Pending, time.Now().UTC()
	res, err := r.d.Exec(`INSERT INTO device_link (code, poll_token_hash, device_name, platform, source, expires_at, created_at) VALUES (?,?,?,?,?,?,?)`,
		l.Code, l.PollTokenHash, l.DeviceName, l.Platform, l.Source, db.Ms(l.ExpiresAt), db.Ms(l.CreatedAt))
	if err != nil {
		return db.Wrap(err)
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one link or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Link, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM device_link WHERE id = ?`, id))
}

// Pending returns the link behind a code still pending at now, else
// db.ErrNotFound.
func (r *Repo) Pending(code string, now time.Time) (*Link, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM device_link WHERE code = ? AND status = 'pending' AND expires_at > ?`, code, db.Ms(now)))
}

// PendingFrom counts the links from source still pending at now, and when
// the first of them expires; source "" counts links from every source.
func (r *Repo) PendingFrom(source string, now time.Time) (int, time.Time, error) {
	var n int
	var first sql.NullInt64
	err := r.d.QueryRow(`SELECT COUNT(*), MIN(expires_at) FROM device_link WHERE (?1 = '' OR source = ?1) AND status = 'pending' AND expires_at > ?2`,
		source, db.Ms(now)).Scan(&n, &first)
	return n, db.FromMs(first.Int64), db.Wrap(err)
}

// PruneUncollected deletes links that expired before cutoff without handing
// a device over; links that signed a device in stay.
func (r *Repo) PruneUncollected(cutoff time.Time) error {
	_, err := r.d.Exec(`DELETE FROM device_link WHERE collected_at IS NULL AND expires_at < ?`, db.Ms(cutoff))
	return db.Wrap(err)
}

// Decide approves or denies the link behind a code only while it is pending
// at now; else db.ErrNotFound, so a code is decided once.
func (r *Repo) Decide(code, status string, accountID, householdID int64, now time.Time) (*Link, error) {
	return scan(r.d.QueryRow(`UPDATE device_link SET status = ?1, account_id = ?2, household_id = ?3, decided_at = ?4
		WHERE code = ?5 AND status = 'pending' AND expires_at > ?4 RETURNING `+cols, status, accountID, householdID, db.Ms(now), code))
}

// Collect records that an approved link handed its device over at now; it
// fails with db.ErrNotFound when already collected or expired, so the token
// goes out once.
func (r *Repo) Collect(id, deviceID int64, now time.Time) error {
	return db.ExecOne(r.d, `UPDATE device_link SET device_id = ?1, collected_at = ?2
		WHERE id = ?3 AND status = 'approved' AND collected_at IS NULL AND expires_at > ?2`, deviceID, db.Ms(now), id)
}

// List pages every link, newest first.
func (r *Repo) List(limit, offset int) ([]*Link, error) {
	return r.list(`SELECT `+cols+` FROM device_link ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
}

// ByHousehold pages the links adults of a household decided, newest first.
func (r *Repo) ByHousehold(householdID int64, limit, offset int) ([]*Link, error) {
	return r.list(`SELECT `+cols+` FROM device_link WHERE household_id = ? ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, householdID, limit, offset)
}

// Delete removes a link; a device it signed in is untouched.
func (r *Repo) Delete(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM device_link WHERE id = ?`, id)
}

// DeleteInHousehold removes a link only if the household decided it.
func (r *Repo) DeleteInHousehold(id, householdID int64) error {
	return db.ExecOne(r.d, `DELETE FROM device_link WHERE id = ? AND household_id = ?`, id, householdID)
}
