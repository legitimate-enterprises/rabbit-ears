// Package together holds Watch together: two profiles of one household
// playing one title in sync, and their repository.
package together

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Member roles: the profile that opened it, and the one that joined.
const (
	Host  = "host"
	Guest = "guest"
)

// JoinWindow is how long a second profile has to join after it opens.
const JoinWindow = 5 * time.Minute

// Together is one Watch together. The shared position is PositionMs as of
// PositionAt, advancing from then while Playing. PositionAt may lie ahead:
// it is when the latest play, pause or seek takes effect on every member.
// Version rises on every change members must follow.
type Together struct {
	ID          uid.ID
	HouseholdID uid.ID
	ItemID      uid.ID
	Playing     bool
	PositionMs  int64
	PositionAt  time.Time
	PausedBy    *uid.ID // the account that paused last
	Version     int
	OpenedAt    time.Time
	JoinBy      time.Time
	EndedAt     *time.Time
	CreatedAt   time.Time
	ModifiedAt  time.Time
}

// SharedAt is the shared position at t, which advances only while playing.
func (w *Together) SharedAt(t time.Time) int64 {
	if !w.Playing || t.Before(w.PositionAt) {
		return w.PositionMs
	}
	return w.PositionMs + t.Sub(w.PositionAt).Milliseconds()
}

// Member is a profile in a Watch together on one device; one profile on two
// devices may be both members. LastSeenAt is the member's latest poll.
type Member struct {
	WatchTogetherID uid.ID
	AccountID       uid.ID
	DeviceID        uid.ID
	Role            string
	JoinedAt        time.Time
	LastSeenAt      time.Time
}

// Repo persists Watch togethers and their members.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `w.id, w.household_id, w.item_id, w.playing, w.position_ms, w.position_at, w.paused_by, w.version, w.opened_at, w.join_by,
	w.ended_at, w.created_at, w.modified_at`

func scan(s db.Scanner) (*Together, error) {
	var w Together
	var ended sql.NullInt64
	var at, opened, joinBy, created, modified int64
	if err := s.Scan(&w.ID, &w.HouseholdID, &w.ItemID, &w.Playing, &w.PositionMs, &at, &w.PausedBy, &w.Version, &opened, &joinBy,
		&ended, &created, &modified); err != nil {
		return nil, db.Wrap(err)
	}
	w.PositionAt, w.OpenedAt, w.JoinBy = db.FromMs(at), db.FromMs(opened), db.FromMs(joinBy)
	w.EndedAt, w.CreatedAt, w.ModifiedAt = db.FromNullMs(ended), db.FromMs(created), db.FromMs(modified)
	return &w, nil
}

// Create inserts w with its host, minting its ID unless set, and fills
// Version and the audit fields.
func (r *Repo) Create(w *Together, host *Member) error {
	if w.ID.IsZero() {
		w.ID = uid.New()
	}
	return db.InTx(r.d, func(d db.DBTX) error {
		now := time.Now().UTC().Truncate(time.Millisecond)
		w.Version, w.CreatedAt, w.ModifiedAt = 1, now, now
		_, err := d.Exec(`INSERT INTO watch_together (id, household_id, item_id, playing, position_ms, position_at, paused_by, version,
			opened_at, join_by, ended_at, created_at, modified_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, w.ID, w.HouseholdID, w.ItemID, w.Playing,
			w.PositionMs, db.Ms(w.PositionAt), w.PausedBy, w.Version, db.Ms(w.OpenedAt), db.Ms(w.JoinBy), db.NullMs(w.EndedAt), db.Ms(now), db.Ms(now))
		if err != nil {
			return db.Wrap(err)
		}
		host.WatchTogetherID = w.ID
		return New(d).AddMember(host)
	})
}

// Get returns one Watch together or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Together, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM watch_together w WHERE w.id = ?`, id))
}

// Open lists the Watch togethers of a household, or of any (nil), that have
// not ended, newest first, on one item or any (nil).
func (r *Repo) Open(householdID, itemID *uid.ID) ([]*Together, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM watch_together w WHERE (?1 IS NULL OR w.household_id = ?1) AND w.ended_at IS NULL
		AND (?2 IS NULL OR w.item_id = ?2) ORDER BY w.opened_at DESC, w.id DESC`, householdID, itemID)
	return db.Collect(rows, err, scan)
}

// ForAccount lists the Watch togethers a profile is in that have not ended.
func (r *Repo) ForAccount(accountID uid.ID) ([]*Together, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM watch_together w JOIN watch_together_member m ON m.watch_together_id = w.id
		WHERE m.account_id = ? AND w.ended_at IS NULL ORDER BY w.opened_at DESC, w.id DESC`, accountID)
	return db.Collect(rows, err, scan)
}

// ForDevice lists the Watch togethers that have not ended with a member on
// the device.
func (r *Repo) ForDevice(deviceID uid.ID) ([]*Together, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM watch_together w JOIN watch_together_member m ON m.watch_together_id = w.id
		WHERE m.device_id = ? AND w.ended_at IS NULL ORDER BY w.opened_at DESC, w.id DESC`, deviceID)
	return db.Collect(rows, err, scan)
}

// Update writes w's play state, version and end as given.
func (r *Repo) Update(w *Together) error {
	w.ModifiedAt = time.Now().UTC().Truncate(time.Millisecond)
	return db.ExecOne(r.d, `UPDATE watch_together SET playing=?, position_ms=?, position_at=?, paused_by=?, version=?, join_by=?, ended_at=?,
		modified_at=? WHERE id=?`, w.Playing, w.PositionMs, db.Ms(w.PositionAt), w.PausedBy, w.Version, db.Ms(w.JoinBy),
		db.NullMs(w.EndedAt), db.Ms(w.ModifiedAt), w.ID)
}

// AddMember inserts m; a second member in a role is db.ErrConflict.
func (r *Repo) AddMember(m *Member) error {
	_, err := r.d.Exec(`INSERT INTO watch_together_member (watch_together_id, account_id, device_id, role, joined_at, last_seen_at)
		VALUES (?,?,?,?,?,?)`, m.WatchTogetherID, m.AccountID, m.DeviceID, m.Role, db.Ms(m.JoinedAt), db.Ms(m.LastSeenAt))
	return db.Wrap(err)
}

// Members lists a Watch together's members, host first.
func (r *Repo) Members(id uid.ID) ([]*Member, error) {
	rows, err := r.d.Query(`SELECT watch_together_id, account_id, device_id, role, joined_at, last_seen_at FROM watch_together_member
		WHERE watch_together_id = ? ORDER BY role = 'guest', joined_at`, id)
	return db.Collect(rows, err, func(s db.Scanner) (*Member, error) {
		var m Member
		var joined, seen int64
		if err := s.Scan(&m.WatchTogetherID, &m.AccountID, &m.DeviceID, &m.Role, &joined, &seen); err != nil {
			return nil, db.Wrap(err)
		}
		m.JoinedAt, m.LastSeenAt = db.FromMs(joined), db.FromMs(seen)
		return &m, nil
	})
}

// Touch records a profile's poll at at, on each device they are a member on.
func (r *Repo) Touch(id, accountID uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE watch_together_member SET last_seen_at = ? WHERE watch_together_id = ? AND account_id = ?`,
		db.Ms(at), id, accountID)
}
