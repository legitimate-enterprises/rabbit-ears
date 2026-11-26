// Package joincode holds the single-use codes an admin gives a new adult, and
// its repository.
package joincode

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// JoinCode lets one pending adult account be accepted until it expires, is
// used or is revoked.
type JoinCode struct {
	ID        int64
	Code      string
	AccountID int64
	ExpiresAt time.Time
	UsedAt    *time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

// Usable reports whether the code can still be redeemed at now.
func (j *JoinCode) Usable(now time.Time) bool {
	return j.UsedAt == nil && j.RevokedAt == nil && now.Before(j.ExpiresAt)
}

// Repo persists join codes.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, code, account_id, expires_at, used_at, revoked_at, created_at`

func scan(s db.Scanner) (*JoinCode, error) {
	var j JoinCode
	var exp, created int64
	var used, revoked sql.NullInt64
	if err := s.Scan(&j.ID, &j.Code, &j.AccountID, &exp, &used, &revoked, &created); err != nil {
		return nil, db.Wrap(err)
	}
	j.ExpiresAt, j.UsedAt, j.RevokedAt, j.CreatedAt = db.FromMs(exp), db.FromNullMs(used), db.FromNullMs(revoked), db.FromMs(created)
	return &j, nil
}

// Create inserts j; the code is unique.
func (r *Repo) Create(j *JoinCode) error {
	j.CreatedAt = time.Now().UTC()
	res, err := r.d.Exec(`INSERT INTO join_code (code, account_id, expires_at, used_at, revoked_at, created_at) VALUES (?,?,?,?,?,?)`,
		j.Code, j.AccountID, db.Ms(j.ExpiresAt), db.NullMs(j.UsedAt), db.NullMs(j.RevokedAt), db.Ms(j.CreatedAt))
	if err != nil {
		return db.Wrap(err)
	}
	j.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one code row or db.ErrNotFound.
func (r *Repo) Get(id int64) (*JoinCode, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM join_code WHERE id = ?`, id))
}

// ByCode finds a code by its text.
func (r *Repo) ByCode(code string) (*JoinCode, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM join_code WHERE code = ?`, code))
}

// ByAccount lists an account's codes, newest first.
func (r *Repo) ByAccount(accountID int64) ([]*JoinCode, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM join_code WHERE account_id = ? ORDER BY created_at DESC, id DESC`, accountID)
	return db.Collect(rows, err, scan)
}

// Update writes expiry, used and revoked instants.
func (r *Repo) Update(j *JoinCode) error {
	return db.ExecOne(r.d, `UPDATE join_code SET expires_at=?, used_at=?, revoked_at=? WHERE id=?`,
		db.Ms(j.ExpiresAt), db.NullMs(j.UsedAt), db.NullMs(j.RevokedAt), j.ID)
}

// Redeem marks a code used if, and only if, it is still usable at now. It
// returns db.ErrNotFound when it was already used, revoked or expired, so two
// concurrent accepts cannot both succeed.
func (r *Repo) Redeem(code string, now time.Time) (*JoinCode, error) {
	return scan(r.d.QueryRow(`UPDATE join_code SET used_at = ?1 WHERE code = ?2 AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?1
		RETURNING `+cols, db.Ms(now), code))
}

// RevokeUnused revokes every unused code of an account.
func (r *Repo) RevokeUnused(accountID int64, at time.Time) error {
	_, err := r.d.Exec(`UPDATE join_code SET revoked_at = ? WHERE account_id = ? AND used_at IS NULL AND revoked_at IS NULL`, db.Ms(at), accountID)
	return db.Wrap(err)
}
