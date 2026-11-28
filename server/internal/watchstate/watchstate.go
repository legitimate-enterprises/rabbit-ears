// Package watchstate holds per-account, per-item watch progress and its
// repository.
package watchstate

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// State is one account's progress on one item.
type State struct {
	AccountID  uid.ID
	ItemID     uid.ID
	PositionMs int64
	// EditionID is the edition PositionMs was reached in, nil for none;
	// the position resumes only that edition.
	EditionID    *uid.ID
	Watched      bool
	PlayCount    int
	LastPlayedAt *time.Time
	// ChangedAt is the instant of the last change, the client's for a
	// change a client made: the later instant wins.
	ChangedAt time.Time
}

// AutoWatched is the share of a file's duration a recorded playback
// position must reach for the item to count as watched.
const AutoWatched = 0.9

// Mark sets whether the item is watched and where to resume, counting a
// play when it becomes watched.
func (w *State) Mark(watched bool, positionMs int64) {
	if watched && !w.Watched {
		w.PlayCount++
	}
	w.Watched, w.PositionMs = watched, positionMs
}

// Progress records a playback position in a file of durationMs (nil or 0
// when unknown). From AutoWatched of the duration it marks the item watched
// from the start; before that an item already watched stays watched.
func (w *State) Progress(positionMs int64, durationMs *int64) {
	if durationMs != nil && *durationMs > 0 && float64(positionMs) >= AutoWatched*float64(*durationMs) {
		w.Mark(true, 0)
		return
	}
	w.PositionMs = positionMs
}

// Repo persists watch state.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// InTx runs fn with a repository whose writes commit together, or not at
// all when fn fails.
func (r *Repo) InTx(fn func(*Repo) error) error {
	return db.InTx(r.d, func(d db.DBTX) error { return fn(New(d)) })
}

const cols = `account_id, item_id, position_ms, watched, play_count, last_played_at, changed_at, edition_id`

func scan(s db.Scanner) (*State, error) {
	var v State
	var last sql.NullInt64
	var changed int64
	if err := s.Scan(&v.AccountID, &v.ItemID, &v.PositionMs, &v.Watched, &v.PlayCount, &last, &changed, &v.EditionID); err != nil {
		return nil, db.Wrap(err)
	}
	v.LastPlayedAt, v.ChangedAt = db.FromNullMs(last), db.FromMs(changed)
	return &v, nil
}

// Save inserts or replaces the state for (AccountID, ItemID).
func (r *Repo) Save(v *State) error {
	v.ChangedAt = time.Now().UTC()
	_, err := r.d.Exec(`INSERT INTO watch_state (`+cols+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id, item_id) DO UPDATE SET position_ms=excluded.position_ms, watched=excluded.watched,
			play_count=excluded.play_count, last_played_at=excluded.last_played_at, changed_at=excluded.changed_at,
			edition_id=excluded.edition_id`,
		v.AccountID, v.ItemID, v.PositionMs, v.Watched, v.PlayCount, db.NullMs(v.LastPlayedAt), db.Ms(v.ChangedAt), v.EditionID)
	return db.Wrap(err)
}

// SaveAt saves v as changed at instant at, unless the stored state changed
// later, and reports whether it saved.
func (r *Repo) SaveAt(v *State, at time.Time) (bool, error) {
	res, err := r.d.Exec(`INSERT INTO watch_state (`+cols+`) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id, item_id) DO UPDATE SET position_ms=excluded.position_ms, watched=excluded.watched,
			play_count=excluded.play_count, last_played_at=excluded.last_played_at, changed_at=excluded.changed_at,
			edition_id=excluded.edition_id
		WHERE watch_state.changed_at <= excluded.changed_at`,
		v.AccountID, v.ItemID, v.PositionMs, v.Watched, v.PlayCount, db.NullMs(v.LastPlayedAt), db.Ms(at), v.EditionID)
	if err != nil {
		return false, db.Wrap(err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		v.ChangedAt = at.UTC()
	}
	return n > 0, nil
}

// Get returns the state or db.ErrNotFound (never played).
func (r *Repo) Get(accountID, itemID uid.ID) (*State, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM watch_state WHERE account_id = ? AND item_id = ?`, accountID, itemID))
}

// Delete forgets one item's state for an account.
func (r *Repo) Delete(accountID, itemID uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM watch_state WHERE account_id = ? AND item_id = ?`, accountID, itemID)
}

// DeleteAt forgets the state as of instant at, unless it changed later;
// one never saved is db.ErrNotFound.
func (r *Repo) DeleteAt(accountID, itemID uid.ID, at time.Time) error {
	res, err := r.d.Exec(`DELETE FROM watch_state WHERE account_id = ? AND item_id = ? AND changed_at <= ?`, accountID, itemID, db.Ms(at))
	if err != nil {
		return db.Wrap(err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = r.Get(accountID, itemID)
	return err
}

// DismissAt forgets where to resume as of instant at, taking the item out
// of progress and keeping the rest, unless the state changed later or is
// not in progress; one never saved is db.ErrNotFound.
func (r *Repo) DismissAt(accountID, itemID uid.ID, at time.Time) error {
	res, err := r.d.Exec(`UPDATE watch_state SET position_ms = 0, changed_at = ?3
		WHERE account_id = ?1 AND item_id = ?2 AND position_ms > 0 AND changed_at <= ?3`, accountID, itemID, db.Ms(at))
	if err != nil {
		return db.Wrap(err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = r.Get(accountID, itemID)
	return err
}

// RecordPlay counts one play of editionID (nil for none) that ended at
// instant at. The count only adds and the rest goes to the later instant,
// so plays and SaveAt commute.
func (r *Repo) RecordPlay(accountID, itemID uid.ID, editionID *uid.ID, at time.Time) (*State, error) {
	_, err := r.d.Exec(`INSERT INTO watch_state (`+cols+`) VALUES (?1, ?2, 0, 1, 1, ?3, ?3, ?4)
		ON CONFLICT (account_id, item_id) DO UPDATE SET
			play_count = play_count + 1,
			last_played_at = MAX(COALESCE(last_played_at, 0), excluded.last_played_at),
			watched = CASE WHEN changed_at <= excluded.changed_at THEN 1 ELSE watched END,
			position_ms = CASE WHEN changed_at <= excluded.changed_at THEN 0 ELSE position_ms END,
			edition_id = CASE WHEN changed_at <= excluded.changed_at THEN excluded.edition_id ELSE edition_id END,
			changed_at = MAX(changed_at, excluded.changed_at)`,
		accountID, itemID, db.Ms(at), editionID)
	if err != nil {
		return nil, db.Wrap(err)
	}
	return r.Get(accountID, itemID)
}

func (r *Repo) list(q string, args ...any) ([]*State, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// List pages through every state of an account, most recently played first
// and never-played last; a parentID other than nil keeps the items under
// it, its children and theirs.
func (r *Repo) List(accountID uid.ID, parentID *uid.ID, a item.Audience, limit, offset int) ([]*State, error) {
	return r.list(`SELECT w.account_id, w.item_id, w.position_ms, w.watched, w.play_count, w.last_played_at, w.changed_at, w.edition_id
		FROM watch_state w JOIN item ON item.id = w.item_id WHERE w.account_id = ?1 AND `+item.VisibleWhere+`
		AND (?2 IS NULL OR item.parent_id = ?2 OR item.parent_id IN (SELECT p.id FROM item p WHERE p.parent_id = ?2))
		ORDER BY w.last_played_at IS NULL, w.last_played_at DESC, w.changed_at DESC, w.item_id LIMIT ?4 OFFSET ?5`,
		append([]any{accountID, parentID, nil, limit, offset}, a.Args()...)...)
}

// ShowProgress is an account's progress through one show, over the seasons
// and episodes its audience sees.
type ShowProgress struct {
	ShowID        uid.ID
	Seasons       int
	Episodes      int
	Unwatched     int
	InProgress    bool
	LastPlayedAt  *time.Time
	NextEpisodeID *uid.ID
}

// showProgress pages the visible shows by sort title, which every show
// has, so the page walks item_kind_sort. Episodes are numbered in
// play order: seasons by number with specials (0 or none) last, then by
// episode number. The next episode follows the clients' rule: of the
// episodes watched or in progress, the latest played (the earliest in play
// order on a tie, never-played last) is next if in progress, else the first
// unwatched one after it; with none, nothing is watched, so the first.
// Seasons and episodes are found from the page's shows by parent; +kind
// keeps the planner off the kind index, which would walk every episode.
var showProgress = `WITH page AS (
	SELECT item.id, item.sort_title FROM item WHERE item.kind = 'show' AND item.sort_title IS NOT NULL AND ` + item.VisibleWhere + `
	ORDER BY item.sort_title COLLATE NOCASE, item.id LIMIT ?4 OFFSET ?5
), seasons AS (
	SELECT item.parent_id AS show_id, COUNT(*) AS n FROM item
	WHERE +item.kind = 'season' AND item.parent_id IN (SELECT id FROM page) AND ` + item.VisibleWhere + `
	GROUP BY item.parent_id
), eps AS MATERIALIZED (
	SELECT se.parent_id AS show_id, item.id,
		ROW_NUMBER() OVER (PARTITION BY se.parent_id ORDER BY COALESCE(se.season_number, 0) = 0, se.season_number, se.id,
			item.episode_number, item.id) AS pos,
		COALESCE(w.watched, 0) AS watched, COALESCE(w.position_ms, 0) > 0 AS going,
		w.last_played_at AS at
	FROM item JOIN item se ON se.id = item.parent_id
	LEFT JOIN watch_state w ON w.account_id = ?1 AND w.item_id = item.id
	WHERE +item.kind = 'episode' AND se.parent_id IN (SELECT id FROM page) AND ` + item.VisibleWhere + `
), latest AS (
	SELECT show_id, pos, going FROM (SELECT show_id, pos, going,
		ROW_NUMBER() OVER (PARTITION BY show_id ORDER BY at IS NULL, at DESC, pos) AS k FROM eps WHERE watched OR going)
	WHERE k = 1
), sums AS (
	SELECT page.id AS show_id, page.sort_title, COUNT(eps.id) AS episodes, COALESCE(SUM(NOT eps.watched), 0) AS unwatched,
		COALESCE(MAX(eps.going), 0) AS going, MAX(eps.at) AS at,
		CASE WHEN latest.going THEN latest.pos
			ELSE MIN(CASE WHEN NOT eps.watched AND eps.pos > COALESCE(latest.pos, 0) THEN eps.pos END) END AS next_pos
	FROM page LEFT JOIN eps ON eps.show_id = page.id LEFT JOIN latest ON latest.show_id = page.id
	GROUP BY page.id
)
SELECT sums.show_id, COALESCE(seasons.n, 0), sums.episodes, sums.unwatched, sums.going, sums.at, nx.id
FROM sums LEFT JOIN seasons ON seasons.show_id = sums.show_id
LEFT JOIN eps nx ON nx.show_id = sums.show_id AND nx.pos = sums.next_pos
ORDER BY sums.sort_title COLLATE NOCASE, sums.show_id`

// ShowProgress pages an account's progress through each show the audience
// sees, by sort title, in a single query.
func (r *Repo) ShowProgress(accountID uid.ID, a item.Audience, limit, offset int) ([]*ShowProgress, error) {
	rows, err := r.d.Query(showProgress, append([]any{accountID, nil, nil, limit, offset}, a.Args()...)...)
	return db.Collect(rows, err, func(s db.Scanner) (*ShowProgress, error) {
		var v ShowProgress
		var at sql.NullInt64
		if err := s.Scan(&v.ShowID, &v.Seasons, &v.Episodes, &v.Unwatched, &v.InProgress, &at, &v.NextEpisodeID); err != nil {
			return nil, db.Wrap(err)
		}
		v.LastPlayedAt = db.FromNullMs(at)
		return &v, nil
	})
}
