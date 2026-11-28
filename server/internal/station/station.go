// Package station holds stations, each continuous programming from one
// playlist, the programs written ahead for them, and their repository,
// with the time arithmetic that schedules them.
package station

import (
	"database/sql"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Visibilities: private is its owner's, household every profile of the
// owner's household, server every household.
const (
	Private   = "private"
	Household = "household"
	Server    = "server"
)

// Station belongs to one adult and plays one of their playlists.
type Station struct {
	ID          uid.ID
	AccountID   uid.ID
	PlaylistID  uid.ID
	HouseholdID *uid.ID // the owner's, read only
	Kind        string  // the playlist's; Update writes it to an own playlist only
	Name        string
	Random      bool
	Visibility  string
	Timing      string // Continuous, HalfHour or Hour
	Rating      string // "" for none
	Schedule    Schedule
	CreatedAt   time.Time
	ModifiedAt  time.Time
	CreatedBy   *uid.ID
	ModifiedBy  *uid.ID
	Smart       bool // rules choose its playlist's entries, read only
}

// Viewer is who asks: the active profile, the device's household and the
// profile's audience.
type Viewer struct {
	ProfileID, HouseholdID uid.ID
	Audience               item.Audience
}

// Repo persists stations and their programs.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const from = ` FROM station s JOIN account a ON a.id = s.account_id JOIN playlist p ON p.id = s.playlist_id`

const cols = `SELECT s.id, s.account_id, s.playlist_id, a.household_id, p.kind, s.name, s.shuffled, s.visibility,
	s.timing, COALESCE(s.rating, ''), s.created_at, s.modified_at, s.created_by, s.modified_by,
	EXISTS (SELECT 1 FROM smart_list l WHERE l.playlist_id = s.playlist_id)` + from

// SeenOf keeps the stations s shared with profile, on a device of
// household, owner being the household of s's owner. What a kid may see of
// them the play order decides, which the repository cannot read.
func SeenOf(s, owner, profile, household string) string {
	return ` (` + s + `.account_id = ` + profile + ` OR ` + s + `.visibility = 'server' OR ` + s + `.visibility = 'household' AND ` +
		owner + ` = ` + household + `)`
}

// seen is SeenOf the viewer bound at ?1 (profile) and ?2 (household).
var seen = SeenOf("s", "a.household_id", "?1", "?2")

func (v Viewer) args(third, limit, offset any) []any {
	return []any{v.ProfileID, v.HouseholdID, third, limit, offset}
}

func scan(s db.Scanner) (*Station, error) {
	var v Station
	var created, modified int64
	if err := s.Scan(&v.ID, &v.AccountID, &v.PlaylistID, &v.HouseholdID, &v.Kind, &v.Name, &v.Random, &v.Visibility, &v.Timing, &v.Rating,
		&created, &modified, &v.CreatedBy, &v.ModifiedBy, &v.Smart); err != nil {
		return nil, db.Wrap(err)
	}
	v.CreatedAt, v.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &v, nil
}

// schedules fills each station's days and times.
func (r *Repo) schedules(ss []*Station) ([]*Station, error) {
	for _, s := range ss {
		rows, err := r.d.Query(`SELECT weekday FROM station_day WHERE station_id = ? ORDER BY weekday`, s.ID)
		days, err := db.Collect(rows, err, func(sc db.Scanner) (time.Weekday, error) {
			var d time.Weekday
			return d, db.Wrap(sc.Scan(&d))
		})
		if err != nil {
			return nil, err
		}
		rows, err = r.d.Query(`SELECT id, from_minute, to_minute FROM station_time WHERE station_id = ? ORDER BY from_minute, to_minute, id`, s.ID)
		times, err := db.Collect(rows, err, func(sc db.Scanner) (Range, error) {
			var v Range
			return v, db.Wrap(sc.Scan(&v.ID, &v.From, &v.To))
		})
		if err != nil {
			return nil, err
		}
		s.Schedule = Schedule{Days: days, Times: times}
	}
	return ss, nil
}

func (r *Repo) one(q string, args ...any) (*Station, error) {
	s, err := scan(r.d.QueryRow(q, args...))
	if err != nil {
		return nil, err
	}
	out, err := r.schedules([]*Station{s})
	if err != nil {
		return nil, err
	}
	return out[0], nil
}

func (r *Repo) many(q string, args ...any) ([]*Station, error) {
	rows, err := r.d.Query(q, args...)
	ss, err := db.Collect(rows, err, scan)
	if err != nil {
		return nil, err
	}
	return r.schedules(ss)
}

// writeSchedule replaces a station's days and times, each time by its ID
// when set (one another station has is db.ErrConflict), and fills the IDs
// it mints into s.Times. A time that keeps its ID keeps its created_at and
// created_by.
func writeSchedule(d db.DBTX, id uid.ID, s Schedule, now time.Time, by *uid.ID) error {
	type made struct {
		id uid.ID
		at int64
		by *uid.ID
	}
	rows, err := d.Query(`SELECT id, created_at, created_by FROM station_time WHERE station_id = ?`, id)
	was, err := db.Collect(rows, err, func(s db.Scanner) (made, error) {
		var m made
		return m, db.Wrap(s.Scan(&m.id, &m.at, &m.by))
	})
	if err != nil {
		return err
	}
	kept := map[uid.ID]made{}
	for _, m := range was {
		kept[m.id] = m
	}
	if _, err := d.Exec(`DELETE FROM station_day WHERE station_id = ?`, id); err != nil {
		return db.Wrap(err)
	}
	if _, err := d.Exec(`DELETE FROM station_time WHERE station_id = ?`, id); err != nil {
		return db.Wrap(err)
	}
	for _, day := range s.Days {
		if _, err := d.Exec(`INSERT OR IGNORE INTO station_day (station_id, weekday) VALUES (?,?)`, id, int(day)); err != nil {
			return db.Wrap(err)
		}
	}
	for i := range s.Times {
		t := &s.Times[i]
		if t.ID.IsZero() {
			t.ID = uid.New()
		}
		m, ok := kept[t.ID]
		if !ok {
			m = made{t.ID, db.Ms(now), by}
		}
		if _, err := d.Exec(`INSERT INTO station_time (id, station_id, from_minute, to_minute, created_at, modified_at, created_by, modified_by)
			VALUES (?,?,?,?,?,?,?,?)`, t.ID, id, t.From, t.To, m.at, db.Ms(now), m.by, by); err != nil {
			return db.Wrap(err)
		}
	}
	return nil
}

// Create inserts s with its schedule, by its ID when set (an existing one
// is db.ErrConflict), and fills ID, the audit fields, Kind and HouseholdID.
func (r *Repo) Create(s *Station, by *uid.ID) error {
	if s.ID.IsZero() {
		s.ID = uid.New()
	}
	now := time.Now().UTC()
	err := db.InTx(r.d, func(d db.DBTX) error {
		_, err := d.Exec(`INSERT INTO station (id, account_id, playlist_id, name, shuffled, visibility, timing, created_at, modified_at, created_by, modified_by)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			s.ID, s.AccountID, s.PlaylistID, s.Name, s.Random, s.Visibility, s.Timing, db.Ms(now), db.Ms(now), by, by)
		if err != nil {
			return db.Wrap(err)
		}
		return writeSchedule(d, s.ID, s.Schedule, now, by)
	})
	if err != nil {
		return err
	}
	got, err := r.Get(s.ID)
	if err == nil {
		*s = *got
	}
	return err
}

// Get returns one station or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Station, error) { return r.one(cols+` WHERE s.id = ?`, id) }

// Seen returns one station shared with the viewer, else db.ErrNotFound.
func (r *Repo) Seen(id uid.ID, v Viewer) (*Station, error) {
	return r.one(cols+` WHERE s.id = ?3 AND`+seen, v.args(id, nil, nil)...)
}

// List pages by name the stations shared with the viewer, of one kind or
// any ("").
func (r *Repo) List(v Viewer, kind string, limit, offset int) ([]*Station, error) {
	return r.many(cols+listWhere+` ORDER BY s.name COLLATE NOCASE, s.id LIMIT ?4 OFFSET ?5`,
		v.args(kind, limit, offset)...)
}

// listWhere is the rows List pages and Count counts.
var listWhere = ` WHERE (?3 = '' OR p.kind = ?3) AND` + seen

// Count is how many stations List matches, ignoring limit and offset.
func (r *Repo) Count(v Viewer, kind string) (n int, err error) {
	return n, db.Wrap(r.d.QueryRow(`SELECT COUNT(*)`+from+listWhere, v.args(kind, 0, 0)...).Scan(&n))
}

// CountAll is how many stations All pages in all.
func (r *Repo) CountAll() (n int, err error) {
	return n, db.Wrap(r.d.QueryRow(`SELECT COUNT(*)` + from).Scan(&n))
}

// All pages every station by name.
func (r *Repo) All(limit, offset int) ([]*Station, error) {
	return r.many(cols+` ORDER BY s.name COLLATE NOCASE, s.id LIMIT ? OFFSET ?`, limit, offset)
}

// Update writes s's name, playlist, order, visibility, timing and schedule,
// and its kind to the playlist it owns.
func (r *Repo) Update(s *Station, by *uid.ID) error {
	s.ModifiedAt, s.ModifiedBy = time.Now().UTC().Truncate(time.Millisecond), by
	return db.InTx(r.d, func(d db.DBTX) error {
		if err := db.ExecOne(d, `UPDATE station SET name=?, playlist_id=?, shuffled=?, visibility=?, timing=?, modified_at=?, modified_by=? WHERE id=?`,
			s.Name, s.PlaylistID, s.Random, s.Visibility, s.Timing, db.Ms(s.ModifiedAt), by, s.ID); err != nil {
			return err
		}
		// A station's own playlist takes the station's kind.
		if _, err := d.Exec(`UPDATE playlist SET kind = ?1, modified_at = ?2, modified_by = ?3 WHERE id = ?4 AND station_owned AND kind <> ?1`,
			s.Kind, db.Ms(s.ModifiedAt), by, s.PlaylistID); err != nil {
			return db.Wrap(err)
		}
		return writeSchedule(d, s.ID, s.Schedule, s.ModifiedAt, by)
	})
}

// SetRating records a station's rating, "" for none. The rating follows
// from the playlist's content, so recording it is no edit of the station.
func (r *Repo) SetRating(id uid.ID, rating string) error {
	return db.ExecOne(r.d, `UPDATE station SET rating = ? WHERE id = ?`, db.NullStr(rating), id)
}

// SetVisibility records a station's visibility.
func (r *Repo) SetVisibility(id uid.ID, visibility string, by *uid.ID) error {
	return db.ExecOne(r.d, `UPDATE station SET visibility = ?, modified_at = ?, modified_by = ? WHERE id = ?`, visibility, db.Ms(time.Now()), by, id)
}

// Delete removes a station with its schedule, its programs and the
// playlist it owns.
func (r *Repo) Delete(id uid.ID) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		var pid uid.ID
		if err := d.QueryRow(`SELECT playlist_id FROM station WHERE id = ?`, id).Scan(&pid); err != nil {
			return db.Wrap(err)
		}
		if err := db.ExecOne(d, `DELETE FROM station WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := d.Exec(`DELETE FROM playlist WHERE id = ? AND station_owned`, pid)
		return db.Wrap(err)
	})
}

// UsesPlaylist reports whether a station of the account plays the playlist.
func (r *Repo) UsesPlaylist(playlistID, accountID uid.ID) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM station WHERE playlist_id = ? AND account_id = ?`, playlistID, accountID).Scan(&n)
	return n > 0, db.Wrap(err)
}

// DeletePlaying deletes, with their schedules, the stations of the account
// (nil for any) that play the playlist. None of them owns it: a station's
// own playlist is shared with no one.
func (r *Repo) DeletePlaying(playlistID uid.ID, accountID *uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM station WHERE playlist_id = ?1 AND (?2 IS NULL OR account_id = ?2)`, playlistID, accountID)
	return db.Wrap(err)
}

// DeleteUnseen deletes, with their schedules, the stations of the account
// (nil for any) that play the playlist (nil for any) when it is not their
// owner's and no share of it reaches them.
func (r *Repo) DeleteUnseen(playlistID, accountID *uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM station WHERE (?1 IS NULL OR playlist_id = ?1) AND (?2 IS NULL OR account_id = ?2)
		AND playlist_id NOT IN (SELECT id FROM playlist WHERE account_id = station.account_id)
		AND playlist_id NOT IN (SELECT playlist_id FROM playlist_reach WHERE account_id = station.account_id)`, playlistID, accountID)
	return db.Wrap(err)
}

// DeletePlayingOwnedBy deletes, with their schedules, the stations that play
// a playlist the account owns, as deleting each of those playlists would.
func (r *Repo) DeletePlayingOwnedBy(accountID uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM station WHERE playlist_id IN (SELECT id FROM playlist WHERE account_id = ?)`, accountID)
	return db.Wrap(err)
}

// Write adds programs to a station's schedule and stores loop as its
// randomized loop, in one step. Neither is an edit of the station.
func (r *Repo) Write(id uid.ID, ps []Program, loop *Loop) error {
	now := db.Ms(time.Now())
	return db.InTx(r.d, func(d db.DBTX) error {
		for _, p := range ps {
			var entry *uid.ID
			if !p.EntryID.IsZero() {
				entry = &p.EntryID
			}
			if _, err := d.Exec(`INSERT INTO station_program (id, station_id, item_id, edition_id, entry_id, loop, position, starts_at, ends_at,
				created_at, modified_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
				uid.New(), id, p.ItemID, p.EditionID, entry, p.Loop, p.Position, db.Ms(p.Start), db.Ms(p.End), now, now); err != nil {
				return db.Wrap(err)
			}
		}
		if _, err := d.Exec(`DELETE FROM station_loop WHERE station_id = ?`, id); err != nil {
			return db.Wrap(err)
		}
		for i, k := range loop.Keys {
			if _, err := d.Exec(`INSERT INTO station_loop (station_id, position, entry_id, item_id) VALUES (?,?,?,?)`, id, i, k.EntryID, k.ItemID); err != nil {
				return db.Wrap(err)
			}
		}
		return db.ExecOne(d, `UPDATE station SET loop = ? WHERE id = ?`, loop.No, id)
	})
}

// Last is the cursor of a station's last program and when it ends. The
// cursor is nil when there is none, or when the program came from a
// playlist other than the station's, so the next loop starts afresh.
func (r *Repo) Last(id uid.ID) (*Cursor, time.Time, error) {
	var c Cursor
	var entry *uid.ID
	var end int64
	var foreign bool
	err := r.d.QueryRow(`SELECT sp.entry_id, sp.item_id, sp.loop, sp.position, sp.ends_at,
			sp.entry_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM playlist_entry e WHERE e.id = sp.entry_id AND e.playlist_id = s.playlist_id)
		FROM station_program sp JOIN station s ON s.id = sp.station_id WHERE sp.station_id = ? ORDER BY sp.starts_at DESC LIMIT 1`, id).
		Scan(&entry, &c.ItemID, &c.Loop, &c.Position, &end, &foreign)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, db.Wrap(err)
	}
	if foreign {
		return nil, db.FromMs(end), nil
	}
	if entry != nil {
		c.EntryID = *entry
	}
	return &c, db.FromMs(end), nil
}

// Loop is a station's randomized loop.
func (r *Repo) Loop(id uid.ID) (*Loop, error) {
	l := &Loop{}
	if err := r.d.QueryRow(`SELECT loop FROM station WHERE id = ?`, id).Scan(&l.No); err != nil {
		return nil, db.Wrap(err)
	}
	rows, err := r.d.Query(`SELECT entry_id, item_id FROM station_loop WHERE station_id = ? ORDER BY position`, id)
	keys, err := db.Collect(rows, err, func(s db.Scanner) (Key, error) {
		var k Key
		return k, db.Wrap(s.Scan(&k.EntryID, &k.ItemID))
	})
	l.Keys = keys
	return l, err
}

// ClearLoop drops a station's randomized loop, so the next one is shuffled
// afresh.
func (r *Repo) ClearLoop(id uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM station_loop WHERE station_id = ?`, id)
	return db.Wrap(err)
}

// programs binds a station as ?1, ?2 and ?3 as the query needs, paging as ?4
// and ?5, and the audience as ?6 to ?10, whose VisibleWhere leaves out a
// program the audience may not see.
var programs = `SELECT sp.entry_id, sp.item_id, sp.edition_id, sp.loop, sp.position, sp.starts_at, sp.ends_at
	FROM station_program sp JOIN item ON item.id = sp.item_id WHERE sp.station_id = ?1 AND ` + item.VisibleWhere

func scanProgram(s db.Scanner) (*Program, error) {
	var p Program
	var entry *uid.ID
	var start, end int64
	if err := s.Scan(&entry, &p.ItemID, &p.EditionID, &p.Loop, &p.Position, &start, &end); err != nil {
		return nil, db.Wrap(err)
	}
	if entry != nil {
		p.EntryID = *entry
	}
	p.Start, p.End = db.FromMs(start), db.FromMs(end)
	p.RuntimeMs = end - start
	return &p, nil
}

func (r *Repo) program(q string, id uid.ID, a, b any, au item.Audience) (*Program, error) {
	return scanProgram(r.d.QueryRow(programs+q, append([]any{id, a, b, nil, nil}, au.Args()...)...))
}

// On is the program the audience may see playing at t, else db.ErrNotFound.
func (r *Repo) On(id uid.ID, t time.Time, a item.Audience) (*Program, error) {
	return r.program(` AND sp.starts_at <= ?2 AND sp.ends_at > ?2 ORDER BY sp.starts_at DESC LIMIT 1`, id, db.Ms(t), nil, a)
}

// Ended is the program that ended last at or before t, whoever may see
// it, else db.ErrNotFound.
func (r *Repo) Ended(id uid.ID, t time.Time) (*Program, error) {
	return r.program(` AND sp.ends_at <= ?2 ORDER BY sp.ends_at DESC LIMIT 1`, id, db.Ms(t), nil, item.Audience{})
}

// Upcoming is the first limit programs the audience may see starting after
// t, in the order they play.
func (r *Repo) Upcoming(id uid.ID, t time.Time, a item.Audience, limit int) ([]*Program, error) {
	rows, err := r.d.Query(programs+` AND sp.starts_at > ?2 ORDER BY sp.starts_at LIMIT ?4`,
		append([]any{id, db.Ms(t), nil, limit, nil}, a.Args()...)...)
	return db.Collect(rows, err, scanProgram)
}

// Between pages by start the programs the audience may see that overlap
// from to to.
func (r *Repo) Between(id uid.ID, from, to time.Time, a item.Audience, limit, offset int) ([]*Program, error) {
	rows, err := r.d.Query(programs+` AND sp.ends_at > ?2 AND sp.starts_at < ?3 ORDER BY sp.starts_at LIMIT ?4 OFFSET ?5`,
		append([]any{id, db.Ms(from), db.Ms(to), limit, offset}, a.Args()...)...)
	return db.Collect(rows, err, scanProgram)
}

// Changing lists the stations on which a program starts or ends after
// from and by to.
func (r *Repo) Changing(from, to time.Time) ([]uid.ID, error) {
	rows, err := r.d.Query(`SELECT DISTINCT station_id FROM station_program WHERE starts_at > ?1 AND starts_at <= ?2 OR ends_at > ?1 AND ends_at <= ?2
		ORDER BY station_id`, db.Ms(from), db.Ms(to))
	return db.Collect(rows, err, db.Column[uid.ID])
}

// Unstart removes a station's programs that start after t, leaving the one
// playing at t to its end.
func (r *Repo) Unstart(id uid.ID, t time.Time) error {
	_, err := r.d.Exec(`DELETE FROM station_program WHERE station_id = ? AND starts_at > ?`, id, db.Ms(t))
	return db.Wrap(err)
}

// Prune removes every program that ended before t.
func (r *Repo) Prune(t time.Time) error {
	_, err := r.d.Exec(`DELETE FROM station_program WHERE ends_at < ?`, db.Ms(t))
	return db.Wrap(err)
}
