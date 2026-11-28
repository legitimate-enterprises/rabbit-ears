// Package account holds adult and kid accounts, their PIN and rating rules,
// and the manual allow list, with their repository.
package account

import (
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Account kinds.
const (
	Adult = "adult"
	Kid   = "kid"
)

// Kid filter modes.
const (
	FilterRatings   = "ratings"
	FilterAllowList = "allow_list"
)

const dateLayout = "2006-01-02"

// Palette is the profile colors, in the order new profiles take them.
var Palette = []string{"#3C4170", "#2E6B5B", "#6B3A2E", "#5B4A8A", "#8A5A2B", "#2E5B8A", "#6B2E5A", "#4A6B2E"}

// ValidColor reports a color of the palette, as written there.
func ValidColor(c string) bool { return slices.Contains(Palette, c) }

// Account is an adult (email, password, maybe admin) or a kid (none of
// those; reachable only through an adult's device). A pending adult has no
// password hash and no household yet.
type Account struct {
	ID             uid.ID
	Kind           string
	Email          string
	Name           string
	IsAdmin        bool
	HouseholdID    *uid.ID
	PasswordHash   string
	Birthday       *time.Time // a calendar date at UTC midnight
	PINHash        string
	PINFailures    int
	PINLockedUntil *time.Time
	FilterMode     string
	MovieCeiling   string // MPAA rating, kids only; empty means none set
	TVCeiling      string // TV Parental Guidelines rating, kids only
	Color          string // a Palette color; empty for none stored
	// HideKidsContent keeps kids' content out of an adult's browsing.
	HideKidsContent bool
	// NoAutoplay keeps Up next from playing the next item by itself.
	NoAutoplay bool
	RevokedAt  *time.Time
	CreatedAt  time.Time
	ModifiedAt time.Time
	CreatedBy  *uid.ID
	ModifiedBy *uid.ID
}

// Pending reports an adult who has not yet accepted their join link.
func (a *Account) Pending() bool { return a.Kind == Adult && a.PasswordHash == "" }

// ShownColor is the stored color, else the palette color the id picks.
func (a *Account) ShownColor() string {
	if a.Color != "" {
		return a.Color
	}
	return Palette[int(a.ID[len(a.ID)-1])%len(Palette)]
}

// Repo persists accounts and allow lists.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, kind, email, name, is_admin, household_id, password_hash, birthday, pin_hash, pin_failures, pin_locked_until,
	filter_mode, movie_ceiling, tv_ceiling, color, hide_kids_content, no_autoplay, revoked_at, created_at, modified_at, created_by, modified_by`

func scan(s db.Scanner) (*Account, error) {
	var a Account
	var email, pw, bday, pin, movie, tv, color sql.NullString
	var locked, revoked sql.NullInt64
	var created, modified int64
	if err := s.Scan(&a.ID, &a.Kind, &email, &a.Name, &a.IsAdmin, &a.HouseholdID, &pw, &bday, &pin, &a.PINFailures, &locked,
		&a.FilterMode, &movie, &tv, &color, &a.HideKidsContent, &a.NoAutoplay, &revoked, &created, &modified, &a.CreatedBy, &a.ModifiedBy); err != nil {
		return nil, db.Wrap(err)
	}
	a.Email, a.PasswordHash, a.PINHash, a.MovieCeiling, a.TVCeiling = email.String, pw.String, pin.String, movie.String, tv.String
	a.Color = color.String
	if bday.Valid {
		if t, err := time.Parse(dateLayout, bday.String); err == nil {
			a.Birthday = &t
		}
	}
	a.PINLockedUntil, a.RevokedAt = db.FromNullMs(locked), db.FromNullMs(revoked)
	a.CreatedAt, a.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &a, nil
}

func (a *Account) args() []any {
	var bday any
	if a.Birthday != nil {
		bday = a.Birthday.Format(dateLayout)
	}
	mode := a.FilterMode
	if mode == "" {
		mode = FilterRatings
	}
	return []any{a.Kind, db.NullStr(strings.TrimSpace(a.Email)), a.Name, a.IsAdmin, a.HouseholdID, db.NullStr(a.PasswordHash), bday,
		db.NullStr(a.PINHash), a.PINFailures, db.NullMs(a.PINLockedUntil), mode, db.NullStr(a.MovieCeiling), db.NullStr(a.TVCeiling),
		db.NullStr(a.Color), a.HideKidsContent, a.NoAutoplay, db.NullMs(a.RevokedAt)}
}

// Create inserts a, minting its ID unless set, and fills the audit fields.
// Emails are unique ignoring case. Without a color it stores the one
// leastUsedColor picks.
func (r *Repo) Create(a *Account, by *uid.ID) error {
	if a.ID.IsZero() {
		a.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	a.CreatedAt, a.ModifiedAt, a.CreatedBy, a.ModifiedBy = now, now, by, by
	if a.FilterMode == "" {
		a.FilterMode = FilterRatings
	}
	if a.Color == "" {
		c, err := r.leastUsedColor(a.HouseholdID)
		if err != nil {
			return err
		}
		a.Color = c
	}
	_, err := r.d.Exec(`INSERT INTO account (kind, email, name, is_admin, household_id, password_hash, birthday, pin_hash, pin_failures,
		pin_locked_until, filter_mode, movie_ceiling, tv_ceiling, color, hide_kids_content, no_autoplay, revoked_at,
		created_at, modified_at, created_by, modified_by, id)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		append(a.args(), db.Ms(now), db.Ms(now), by, by, a.ID)...)
	return db.Wrap(err)
}

// Get returns one account or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Account, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM account WHERE id = ?`, id))
}

// ByEmail finds an adult by email, ignoring case.
func (r *Repo) ByEmail(email string) (*Account, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM account WHERE email = ?`, strings.TrimSpace(email)))
}

// Update writes every mutable field of a.
func (r *Repo) Update(a *Account, by *uid.ID) error {
	a.ModifiedAt, a.ModifiedBy = time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE account SET kind=?, email=?, name=?, is_admin=?, household_id=?, password_hash=?, birthday=?, pin_hash=?,
		pin_failures=?, pin_locked_until=?, filter_mode=?, movie_ceiling=?, tv_ceiling=?, color=?, hide_kids_content=?, no_autoplay=?, revoked_at=?,
		modified_at=?, modified_by=? WHERE id=?`,
		append(a.args(), db.Ms(a.ModifiedAt), by, a.ID)...)
}

// Delete removes an account; its household (if it owns one), kids, devices,
// join codes, sessions and watch state cascade.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM account WHERE id = ?`, id)
}

func (r *Repo) query(q string, args ...any) ([]*Account, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// List returns accounts of a kind ("" for all) ordered by name, paged.
func (r *Repo) List(kind string, limit, offset int) ([]*Account, error) {
	if limit <= 0 {
		limit = 100
	}
	return r.query(`SELECT `+cols+` FROM account WHERE (?1 = '' OR kind = ?1) ORDER BY name COLLATE NOCASE, id LIMIT ?2 OFFSET ?3`, kind, limit, offset)
}

// Households lists the households a is in: its own first, then those it was
// shared into, oldest first.
func (r *Repo) Households(a *Account) ([]uid.ID, error) {
	rows, err := r.d.Query(`SELECT m.household_id FROM membership m JOIN household h ON h.id = m.household_id WHERE m.account_id = ?1
		ORDER BY m.household_id IS NOT ?2, h.created_at, m.household_id`, a.ID, a.HouseholdID)
	return db.Collect(rows, err, db.Column[uid.ID])
}

// Label is the household a profile is shown with to another.
type Label struct {
	HouseholdID   uid.ID
	HouseholdName string
}

// Labels maps each of ids to the household viewer sees it labeled with: the
// first of viewer's Households it is in, else its own. A profile with
// neither is left out.
func (r *Repo) Labels(viewer *Account, ids []uid.ID) (map[uid.ID]Label, error) {
	list, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := r.d.Query(`SELECT a.id, h.id, h.name FROM json_each(?2) j JOIN account a ON a.id = unhex(j.value, '-')
		JOIN household h ON h.id = COALESCE((SELECT m.household_id FROM membership m
			JOIN membership mine ON mine.household_id = m.household_id AND mine.account_id = ?1
			JOIN household mh ON mh.id = m.household_id
			WHERE m.account_id = a.id ORDER BY m.household_id IS NOT ?3, mh.created_at, m.household_id LIMIT 1), a.household_id)`,
		viewer.ID, string(list), viewer.HouseholdID)
	type row struct {
		id uid.ID
		Label
	}
	got, err := db.Collect(rows, err, func(s db.Scanner) (row, error) {
		var v row
		err := s.Scan(&v.id, &v.HouseholdID, &v.HouseholdName)
		return v, db.Wrap(err)
	})
	out := make(map[uid.ID]Label, len(got))
	for _, v := range got {
		out[v.id] = v.Label
	}
	return out, err
}

// seen selects the profiles ?1 sees: who has joined any household it is in,
// the adults an admin linked it with, on a TV (?2) only the links set for
// TVs, and off a TV its friends.
const seen = ` FROM account WHERE id != ?1 AND revoked_at IS NULL AND (kind = 'kid' OR password_hash IS NOT NULL) AND (
	EXISTS (SELECT 1 FROM membership mine JOIN membership theirs ON theirs.household_id = mine.household_id
		WHERE mine.account_id = ?1 AND theirs.account_id = account.id)
	OR EXISTS (SELECT 1 FROM adult_link l WHERE (NOT ?2 OR l.tv)
		AND (l.a_id = ?1 AND l.b_id = account.id OR l.b_id = ?1 AND l.a_id = account.id))
	OR NOT ?2 AND EXISTS (SELECT 1 FROM friendship f
		WHERE f.a_id = ?1 AND f.b_id = account.id OR f.b_id = ?1 AND f.a_id = account.id))`

// Linked pages by name the joined, unrevoked adults an admin linked a with,
// on a TV (tv) only by the links set for TVs.
func (r *Repo) Linked(a *Account, tv bool, limit, offset int) ([]*Account, error) {
	return r.query(`SELECT `+cols+` FROM account WHERE revoked_at IS NULL AND password_hash IS NOT NULL
		AND EXISTS (SELECT 1 FROM adult_link l WHERE (NOT ?2 OR l.tv)
			AND (l.a_id = ?1 AND l.b_id = account.id OR l.b_id = ?1 AND l.a_id = account.id))
		ORDER BY name COLLATE NOCASE, id LIMIT ?3 OFFSET ?4`, a.ID, tv, limit, offset)
}

// Friends pages by name the joined, unrevoked adults a is friends with.
func (r *Repo) Friends(a *Account, limit, offset int) ([]*Account, error) {
	return r.query(`SELECT `+cols+` FROM account WHERE revoked_at IS NULL AND password_hash IS NOT NULL
		AND EXISTS (SELECT 1 FROM friendship f WHERE f.a_id = ?1 AND f.b_id = account.id OR f.b_id = ?1 AND f.a_id = account.id)
		ORDER BY name COLLATE NOCASE, id LIMIT ?2 OFFSET ?3`, a.ID, limit, offset)
}

// People pages by name the profiles a sees; a limit of 0 is all of them.
func (r *Repo) People(a *Account, tv bool, limit, offset int) ([]*Account, error) {
	if limit <= 0 {
		limit = -1
	}
	return r.query(`SELECT `+cols+seen+` ORDER BY name COLLATE NOCASE, id LIMIT ?3 OFFSET ?4`, a.ID, tv, limit, offset)
}

// Sees reports whether id is one of a's People.
func (r *Repo) Sees(a *Account, id uid.ID, tv bool) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*)`+seen+` AND id = ?3`, a.ID, tv, id).Scan(&n)
	return n > 0, db.Wrap(err)
}

// In reports whether a profile is in a household.
func (r *Repo) In(id, householdID uid.ID) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM membership WHERE account_id = ? AND household_id = ?`, id, householdID).Scan(&n)
	return n > 0, db.Wrap(err)
}

// AddMember puts a profile into a household besides the one
// account.household_id names.
func (r *Repo) AddMember(householdID, id uid.ID) error {
	_, err := r.d.Exec(`INSERT INTO household_member (household_id, account_id, created_at) VALUES (?,?,?)`,
		householdID, id, db.Ms(time.Now().UTC()))
	return db.Wrap(err)
}

// RemoveMember takes a profile out of a household other than the one
// account.household_id names.
func (r *Repo) RemoveMember(householdID, id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM household_member WHERE household_id = ? AND account_id = ?`, householdID, id)
}

// RemoveMemberships takes a profile out of every household but the one
// account.household_id names.
func (r *Repo) RemoveMemberships(id uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM household_member WHERE account_id = ?`, id)
	return db.Wrap(err)
}

// Rehome makes householdID, another household a is in, or none, the one
// account.household_id names.
func (r *Repo) Rehome(a *Account, householdID *uid.ID, by *uid.ID) error {
	if householdID != nil {
		if err := r.RemoveMember(*householdID, a.ID); err != nil {
			return err
		}
	}
	a.HouseholdID, a.ModifiedAt, a.ModifiedBy = householdID, time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE account SET household_id = ?, modified_at = ?, modified_by = ? WHERE id = ?`,
		householdID, db.Ms(a.ModifiedAt), by, a.ID)
}

// ByHousehold lists who is in a household: its owner first, then adults
// before kids, each eldest first and those with no birthday after by name.
func (r *Repo) ByHousehold(householdID uid.ID) ([]*Account, error) {
	return r.query(`SELECT `+cols+` FROM account WHERE id IN (SELECT account_id FROM membership WHERE household_id = ?1)
		ORDER BY id IS NOT (SELECT adult_id FROM household WHERE id = ?1), kind, birthday IS NULL, birthday, name COLLATE NOCASE,
		created_at, id`, householdID)
}

// leastUsedColor is the palette color the fewest members of the household
// show, the earliest in the palette on a tie; the first with no household.
func (r *Repo) leastUsedColor(householdID *uid.ID) (string, error) {
	shown := map[string]int{}
	if householdID != nil {
		members, err := r.ByHousehold(*householdID)
		if err != nil {
			return "", err
		}
		for _, m := range members {
			shown[m.ShownColor()]++
		}
	}
	best := Palette[0]
	for _, c := range Palette {
		if shown[c] < shown[best] {
			best = c
		}
	}
	return best, nil
}

// elsewhere holds when another household than account.household_id's holds
// the account too.
const elsewhere = `EXISTS (SELECT 1 FROM household_member m WHERE m.account_id = account.id)`

// DeletedWith lists the accounts deleting id removes: the account and, when
// it owns a household, everyone only that household holds, since
// account.household_id cascades.
func (r *Repo) DeletedWith(id uid.ID) ([]*Account, error) {
	return r.query(`SELECT `+cols+` FROM account WHERE id = ?1
		OR household_id = (SELECT id FROM household WHERE adult_id = ?1) AND NOT `+elsewhere+` ORDER BY created_at, id`, id)
}

// DeletedWithHousehold lists the accounts deleting a household removes:
// everyone only it holds but its owner.
func (r *Repo) DeletedWithHousehold(householdID uid.ID) ([]*Account, error) {
	return r.query(`SELECT `+cols+` FROM account WHERE household_id = ?1 AND id <> (SELECT adult_id FROM household WHERE id = ?1)
		AND NOT `+elsewhere+` ORDER BY created_at, id`, householdID)
}

// Staying lists those account.household_id puts in a household that its
// deletion must not take: its owner and those another household holds.
func (r *Repo) Staying(householdID uid.ID) ([]*Account, error) {
	return r.query(`SELECT `+cols+` FROM account WHERE household_id = ?1
		AND (id = (SELECT adult_id FROM household WHERE id = ?1) OR `+elsewhere+`) ORDER BY created_at, id`, householdID)
}

// EarliestJoined returns the adult of a household other than except who
// joined it first and may sign in, else db.ErrNotFound. An adult joins by
// redeeming a join code.
func (r *Repo) EarliestJoined(householdID, except uid.ID) (*Account, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM account a WHERE household_id = ? AND id != ? AND kind = 'adult'
		AND password_hash IS NOT NULL AND revoked_at IS NULL
		ORDER BY COALESCE((SELECT MIN(used_at) FROM join_code j WHERE j.account_id = a.id), created_at), id LIMIT 1`, householdID, except))
}

// AdminCount reports how many active admins can sign in.
func (r *Repo) AdminCount() (int, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM account WHERE is_admin = 1 AND revoked_at IS NULL AND password_hash IS NOT NULL`).Scan(&n)
	return n, db.Wrap(err)
}

// FirstAdmin returns the earliest active admin.
func (r *Repo) FirstAdmin() (*Account, error) {
	return scan(r.d.QueryRow(`SELECT ` + cols + ` FROM account WHERE is_admin = 1 AND revoked_at IS NULL ORDER BY created_at, id LIMIT 1`))
}

// RecordPINFailure increments the failure counter and, at max failures,
// locks the account until lockUntil. It returns the new count. Like
// ClearPINFailures it leaves modified_at and modified_by alone.
func (r *Repo) RecordPINFailure(id uid.ID, max int, lockUntil time.Time) (int, error) {
	var n int
	err := r.d.QueryRow(`UPDATE account SET pin_failures = pin_failures + 1,
		pin_locked_until = CASE WHEN pin_failures + 1 >= ? THEN ? ELSE pin_locked_until END
		WHERE id = ? RETURNING pin_failures`, max, db.Ms(lockUntil), id).Scan(&n)
	return n, db.Wrap(err)
}

// ClearPINFailures resets the counter and lock.
func (r *Repo) ClearPINFailures(id uid.ID) error {
	return db.ExecOne(r.d, `UPDATE account SET pin_failures = 0, pin_locked_until = NULL WHERE id = ?`, id)
}

// Allow adds an item to a kid's manual allow list; adding twice is a no-op.
func (r *Repo) Allow(accountID, itemID uid.ID) error {
	_, err := r.d.Exec(`INSERT OR IGNORE INTO account_allow (account_id, item_id) VALUES (?,?)`, accountID, itemID)
	return db.Wrap(err)
}

// Disallow removes an item from the allow list.
func (r *Repo) Disallow(accountID, itemID uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM account_allow WHERE account_id = ? AND item_id = ?`, accountID, itemID)
}

// AllowedFor lists by id the kids in a household on a manual allow list
// whose list holds the item itself.
func (r *Repo) AllowedFor(householdID, itemID uid.ID) ([]uid.ID, error) {
	rows, err := r.d.Query(`SELECT a.id FROM account a JOIN account_allow w ON w.account_id = a.id
		WHERE a.id IN (SELECT account_id FROM membership WHERE household_id = ?) AND a.kind = 'kid' AND a.filter_mode = 'allow_list' AND w.item_id = ? ORDER BY a.id`, householdID, itemID)
	return db.Collect(rows, err, db.Column[uid.ID])
}

// Allowed lists the item IDs on an account's allow list.
func (r *Repo) Allowed(accountID uid.ID) ([]uid.ID, error) {
	rows, err := r.d.Query(`SELECT item_id FROM account_allow WHERE account_id = ? ORDER BY item_id`, accountID)
	return db.Collect(rows, err, db.Column[uid.ID])
}
