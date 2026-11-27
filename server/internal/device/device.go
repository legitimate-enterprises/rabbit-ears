// Package device holds the signed-in device model and its repository.
package device

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Device is bound to the signing-in adult and that adult's household. Only a
// hash of its bearer token is stored.
type Device struct {
	ID          uid.ID
	TokenHash   string
	Name        string
	Platform    string
	AccountID   uid.ID
	HouseholdID uid.ID
	ProfileID   *uid.ID // the household profile in use; nil means the adult
	LastSeenAt  *time.Time
	RevokedAt   *time.Time
	CreatedAt   time.Time
	ModifiedAt  time.Time
	Linked      bool       // signed in through a device link, so never admin
	LockedAt    *time.Time // at Who's watching; nil once a profile is unlocked
}

// TV reports whether a platform is a shared screen, not a personal device.
func TV(platform string) bool { return platform == "tvos" || platform == "roku" }

// Repo persists devices.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, token_hash, name, platform, account_id, household_id, profile_id, last_seen_at, revoked_at, created_at, linked, locked_at,
	modified_at`

func scan(s db.Scanner) (*Device, error) {
	var v Device
	var seen, revoked, locked sql.NullInt64
	var created, modified int64
	if err := s.Scan(&v.ID, &v.TokenHash, &v.Name, &v.Platform, &v.AccountID, &v.HouseholdID, &v.ProfileID, &seen, &revoked, &created, &v.Linked, &locked,
		&modified); err != nil {
		return nil, db.Wrap(err)
	}
	v.LastSeenAt, v.RevokedAt, v.CreatedAt, v.LockedAt = db.FromNullMs(seen), db.FromNullMs(revoked), db.FromMs(created), db.FromNullMs(locked)
	v.ModifiedAt = db.FromMs(modified)
	return &v, nil
}

// Create inserts v, minting its ID unless set, and fills the audit fields.
func (r *Repo) Create(v *Device) error {
	if v.ID.IsZero() {
		v.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	v.CreatedAt, v.ModifiedAt = now, now
	_, err := r.d.Exec(`INSERT INTO device (id, token_hash, name, platform, account_id, household_id, last_seen_at, revoked_at, created_at, modified_at,
		linked) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, v.ID, v.TokenHash, v.Name, v.Platform, v.AccountID, v.HouseholdID, db.NullMs(v.LastSeenAt),
		db.NullMs(v.RevokedAt), db.Ms(now), db.Ms(now), v.Linked)
	return db.Wrap(err)
}

// Get returns one device or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Device, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM device WHERE id = ?`, id))
}

// ByTokenHash also returns a revoked device, so the caller can tell revoked
// from unknown.
func (r *Repo) ByTokenHash(hash string) (*Device, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM device WHERE token_hash = ?`, hash))
}

func (r *Repo) list(q string, args ...any) ([]*Device, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// List returns all devices, most recently seen first.
func (r *Repo) List() ([]*Device, error) {
	return r.list(`SELECT ` + cols + ` FROM device ORDER BY COALESCE(last_seen_at, created_at) DESC, id`)
}

// ByAccount lists the devices an adult has signed in on.
func (r *Repo) ByAccount(accountID uid.ID) ([]*Device, error) {
	return r.list(`SELECT `+cols+` FROM device WHERE account_id = ? ORDER BY id`, accountID)
}

// Update writes the mutable fields.
func (r *Repo) Update(v *Device) error {
	v.ModifiedAt = time.Now().UTC().Truncate(time.Millisecond)
	return db.ExecOne(r.d, `UPDATE device SET name=?, platform=?, household_id=?, last_seen_at=?, revoked_at=?, modified_at=? WHERE id=?`,
		v.Name, v.Platform, v.HouseholdID, db.NullMs(v.LastSeenAt), db.NullMs(v.RevokedAt), db.Ms(v.ModifiedAt), v.ID)
}

// SetProfile switches the profile a device acts as and unlocks the device.
func (r *Repo) SetProfile(id uid.ID, profileID *uid.ID) error {
	return db.ExecOne(r.d, `UPDATE device SET profile_id = ?, locked_at = NULL, modified_at = ? WHERE id = ?`, profileID, db.Ms(time.Now()), id)
}

// Move puts a device in another household, acting as its adult, unlocked.
func (r *Repo) Move(id, householdID uid.ID) error {
	return db.ExecOne(r.d, `UPDATE device SET household_id = ?, profile_id = NULL, locked_at = NULL, modified_at = ? WHERE id = ?`,
		householdID, db.Ms(time.Now()), id)
}

// Lock holds a device at Who's watching from the given instant until
// SetProfile.
func (r *Repo) Lock(id uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE device SET locked_at = ?1, modified_at = ?1 WHERE id = ?2`, db.Ms(at), id)
}

// Reaching lists the signed-in devices of an account and those acting as
// it: every device that deleting the account signs out.
func (r *Repo) Reaching(accountID uid.ID) ([]*Device, error) {
	return r.list(`SELECT `+cols+` FROM device WHERE (account_id = ?1 OR profile_id = ?1) AND revoked_at IS NULL ORDER BY id`, accountID)
}

// InHousehold lists the signed-in devices in a household.
func (r *Repo) InHousehold(householdID uid.ID) ([]*Device, error) {
	return r.list(`SELECT `+cols+` FROM device WHERE household_id = ? AND revoked_at IS NULL ORDER BY id`, householdID)
}

// ReachingIn is Reaching within one household.
func (r *Repo) ReachingIn(accountID, householdID uid.ID) ([]*Device, error) {
	return r.list(`SELECT `+cols+` FROM device WHERE (account_id = ?1 OR profile_id = ?1) AND household_id = ?2 AND revoked_at IS NULL
		ORDER BY id`, accountID, householdID)
}

// Reset sends a device back to Who's watching: it acts as its adult again,
// locked from the given instant.
func (r *Repo) Reset(id uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE device SET profile_id = NULL, locked_at = ?1, modified_at = ?1 WHERE id = ?2`, db.Ms(at), id)
}

// Touch records activity; it leaves modified_at alone.
func (r *Repo) Touch(id uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE device SET last_seen_at = ? WHERE id = ?`, db.Ms(at), id)
}

// Revoke marks a device revoked at the given instant.
func (r *Repo) Revoke(id uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE device SET revoked_at = ?1, modified_at = ?1 WHERE id = ?2`, db.Ms(at), id)
}

// Delete removes a device; its sessions cascade.
func (r *Repo) Delete(id uid.ID) error { return db.ExecOne(r.d, `DELETE FROM device WHERE id = ?`, id) }
