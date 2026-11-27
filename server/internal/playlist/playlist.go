// Package playlist holds a profile's own playlists and their ordered
// entries, and their repository.
package playlist

import (
	"database/sql"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Kinds of playlist, the library kinds: movies holds movies; tv holds shows,
// seasons and episodes; music holds artists, albums and songs.
const (
	Movies = "movies"
	TV     = "tv"
	Music  = "music"
)

// Playlist belongs to one adult's profile.
type Playlist struct {
	ID        uid.ID
	AccountID uid.ID
	Name      string
	Kind      string
	// StationOwned marks a station's own playlist: listed nowhere, and
	// deleted with its station.
	StationOwned bool
	// Shared reports a share of it with any profile or household, as read.
	Shared       bool
	LastPlayedAt *time.Time
	CreatedAt    time.Time
	ModifiedAt   time.Time
	CreatedBy    *uid.ID
	ModifiedBy   *uid.ID
}

// Entry is one item, or one collection, ordered by Rank, ties by ID;
// Position is its place in that order from 0, as read. Exactly one of
// ItemID and CollectionID is set, the other nil. Either may appear more than
// once. EditionID is the edition a title's entry plays, nil for its default;
// a container's is nil.
type Entry struct {
	ID           uid.ID
	PlaylistID   uid.ID
	ItemID       *uid.ID
	CollectionID *uid.ID
	EditionID    *uid.ID
	Rank         string
	Position     int
	AddedAt      time.Time
	CreatedAt    time.Time
	ModifiedAt   time.Time
	CreatedBy    *uid.ID
	ModifiedBy   *uid.ID
}

// Repo persists playlists and their entries.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `id, account_id, name, kind, station_owned,
	EXISTS (SELECT 1 FROM playlist_share s WHERE s.playlist_id = playlist.id)
		OR EXISTS (SELECT 1 FROM playlist_household_share s WHERE s.playlist_id = playlist.id),
	last_played_at, created_at, modified_at, created_by, modified_by`

func scan(s db.Scanner) (*Playlist, error) {
	var p Playlist
	var played sql.NullInt64
	var created, modified int64
	if err := s.Scan(&p.ID, &p.AccountID, &p.Name, &p.Kind, &p.StationOwned, &p.Shared, &played, &created, &modified,
		&p.CreatedBy, &p.ModifiedBy); err != nil {
		return nil, db.Wrap(err)
	}
	p.LastPlayedAt, p.CreatedAt, p.ModifiedAt = db.FromNullMs(played), db.FromMs(created), db.FromMs(modified)
	return &p, nil
}

// Create inserts p, by its ID when set (an existing one is db.ErrConflict),
// and fills ID and the audit fields.
func (r *Repo) Create(p *Playlist, by *uid.ID) error {
	if p.ID.IsZero() {
		p.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	p.CreatedAt, p.ModifiedAt, p.CreatedBy, p.ModifiedBy = now, now, by, by
	_, err := r.d.Exec(`INSERT INTO playlist (id, account_id, name, kind, station_owned, last_played_at, created_at, modified_at, created_by, modified_by)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, p.ID, p.AccountID, p.Name, p.Kind, p.StationOwned, db.NullMs(p.LastPlayedAt), db.Ms(now), db.Ms(now), by, by)
	return db.Wrap(err)
}

// Get returns one playlist or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Playlist, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM playlist WHERE id = ?`, id))
}

// listed is the rows List pages and Count counts, bound at ?1 and ?2.
const listed = ` (account_id = ?1 OR id IN (SELECT playlist_id FROM playlist_reach WHERE account_id = ?1)) AND (?2 = '' OR kind = ?2) AND NOT station_owned`

// Count is how many playlists List matches, ignoring limit and offset.
func (r *Repo) Count(accountID uid.ID, kind string) (n int, err error) {
	return n, db.Wrap(r.d.QueryRow(`SELECT COUNT(*) FROM playlist WHERE`+listed, accountID, kind).Scan(&n))
}

// List pages by name a profile's own playlists and those that reach it, of
// one kind or any (""), leaving out those stations own.
func (r *Repo) List(accountID uid.ID, kind string, limit, offset int) ([]*Playlist, error) {
	rows, err := r.d.Query(`SELECT `+cols+` FROM playlist WHERE`+listed+`
		ORDER BY name COLLATE NOCASE, id LIMIT ?3 OFFSET ?4`, accountID, kind, limit, offset)
	return db.Collect(rows, err, scan)
}

// Share access: read sees, plays and makes a station of the playlist;
// write also changes its entries.
const (
	Read  = "read"
	Write = "write"
)

// Share is a playlist its owner shares with another profile.
type Share struct {
	PlaylistID uid.ID
	AccountID  uid.ID
	Access     string
	CreatedAt  time.Time
}

const shareCols = `playlist_id, account_id, access, created_at`

func scanShare(s db.Scanner) (*Share, error) {
	var v Share
	var created int64
	if err := s.Scan(&v.PlaylistID, &v.AccountID, &v.Access, &created); err != nil {
		return nil, db.Wrap(err)
	}
	v.CreatedAt = db.FromMs(created)
	return &v, nil
}

// CreateShare inserts s and fills CreatedAt; a second share with one
// profile is db.ErrConflict.
func (r *Repo) CreateShare(s *Share) error {
	s.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	_, err := r.d.Exec(`INSERT INTO playlist_share (`+shareCols+`) VALUES (?,?,?,?)`, s.PlaylistID, s.AccountID, s.Access, db.Ms(s.CreatedAt))
	return db.Wrap(err)
}

// Share returns the playlist's share with a profile or db.ErrNotFound.
func (r *Repo) Share(playlistID, accountID uid.ID) (*Share, error) {
	return scanShare(r.d.QueryRow(`SELECT `+shareCols+` FROM playlist_share WHERE playlist_id = ? AND account_id = ?`, playlistID, accountID))
}

// Shares pages a playlist's shares by the name of whom they are with.
func (r *Repo) Shares(playlistID uid.ID, limit, offset int) ([]*Share, error) {
	rows, err := r.d.Query(`SELECT s.playlist_id, s.account_id, s.access, s.created_at FROM playlist_share s JOIN account a ON a.id = s.account_id
		WHERE s.playlist_id = ? ORDER BY a.name COLLATE NOCASE, a.id LIMIT ? OFFSET ?`, playlistID, limit, offset)
	return db.Collect(rows, err, scanShare)
}

// UpdateShare writes s's access.
func (r *Repo) UpdateShare(s *Share) error {
	return db.ExecOne(r.d, `UPDATE playlist_share SET access = ? WHERE playlist_id = ? AND account_id = ?`, s.Access, s.PlaylistID, s.AccountID)
}

// DeleteShare stops sharing the playlist with a profile.
func (r *Repo) DeleteShare(playlistID, accountID uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM playlist_share WHERE playlist_id = ? AND account_id = ?`, playlistID, accountID)
}

// HouseholdShare is a playlist its owner shares with everyone in a
// household.
type HouseholdShare struct {
	PlaylistID  uid.ID
	HouseholdID uid.ID
	Access      string
	CreatedAt   time.Time
}

const householdShareCols = `playlist_id, household_id, access, created_at`

func scanHouseholdShare(s db.Scanner) (*HouseholdShare, error) {
	var v HouseholdShare
	var created int64
	if err := s.Scan(&v.PlaylistID, &v.HouseholdID, &v.Access, &created); err != nil {
		return nil, db.Wrap(err)
	}
	v.CreatedAt = db.FromMs(created)
	return &v, nil
}

// CreateHouseholdShare inserts s and fills CreatedAt; a second share with
// one household is db.ErrConflict.
func (r *Repo) CreateHouseholdShare(s *HouseholdShare) error {
	s.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
	_, err := r.d.Exec(`INSERT INTO playlist_household_share (`+householdShareCols+`) VALUES (?,?,?,?)`,
		s.PlaylistID, s.HouseholdID, s.Access, db.Ms(s.CreatedAt))
	return db.Wrap(err)
}

// HouseholdShare returns the playlist's share with a household or
// db.ErrNotFound.
func (r *Repo) HouseholdShare(playlistID, householdID uid.ID) (*HouseholdShare, error) {
	return scanHouseholdShare(r.d.QueryRow(`SELECT `+householdShareCols+` FROM playlist_household_share
		WHERE playlist_id = ? AND household_id = ?`, playlistID, householdID))
}

// HouseholdShares pages a playlist's household shares by household name.
func (r *Repo) HouseholdShares(playlistID uid.ID, limit, offset int) ([]*HouseholdShare, error) {
	rows, err := r.d.Query(`SELECT s.playlist_id, s.household_id, s.access, s.created_at FROM playlist_household_share s
		JOIN household h ON h.id = s.household_id WHERE s.playlist_id = ? ORDER BY h.name COLLATE NOCASE, h.id LIMIT ? OFFSET ?`,
		playlistID, limit, offset)
	return db.Collect(rows, err, scanHouseholdShare)
}

// UpdateHouseholdShare writes s's access.
func (r *Repo) UpdateHouseholdShare(s *HouseholdShare) error {
	return db.ExecOne(r.d, `UPDATE playlist_household_share SET access = ? WHERE playlist_id = ? AND household_id = ?`,
		s.Access, s.PlaylistID, s.HouseholdID)
}

// DeleteHouseholdShare stops sharing the playlist with a household.
func (r *Repo) DeleteHouseholdShare(playlistID, householdID uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM playlist_household_share WHERE playlist_id = ? AND household_id = ?`, playlistID, householdID)
}

// Access is the strongest access the playlist's shares give a profile,
// directly or through a household it is in, Read for a kid whatever they
// say, and "" when none reaches it.
func (r *Repo) Access(playlistID, accountID uid.ID) (string, error) {
	var v string
	err := r.d.QueryRow(`SELECT CASE WHEN COUNT(*) = 0 THEN ''
			WHEN MAX(access = 'write') AND (SELECT kind FROM account WHERE id = ?2) <> 'kid' THEN 'write' ELSE 'read' END
		FROM playlist_reach WHERE playlist_id = ?1 AND account_id = ?2`, playlistID, accountID).Scan(&v)
	return v, db.Wrap(err)
}

// Audience lists by id who sees the playlist: its owner and every profile
// a share reaches.
func (r *Repo) Audience(playlistID uid.ID) ([]uid.ID, error) {
	rows, err := r.d.Query(`SELECT account_id FROM playlist WHERE id = ?1
		UNION SELECT account_id FROM playlist_reach WHERE playlist_id = ?1 ORDER BY 1`, playlistID)
	return db.Collect(rows, err, db.Column[uid.ID])
}

// Update writes p's name and kind.
func (r *Repo) Update(p *Playlist, by *uid.ID) error {
	p.ModifiedAt, p.ModifiedBy = time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE playlist SET name=?, kind=?, modified_at=?, modified_by=? WHERE id=?`, p.Name, p.Kind, db.Ms(p.ModifiedAt), by, p.ID)
}

// MarkPlayed stamps when playback last started from the playlist, which is
// no edit of it.
func (r *Repo) MarkPlayed(id uid.ID, at time.Time) error {
	return db.ExecOne(r.d, `UPDATE playlist SET last_played_at=? WHERE id=?`, db.Ms(at), id)
}

// Delete removes a playlist with its entries.
func (r *Repo) Delete(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM playlist WHERE id = ?`, id)
}

const entryCols = `id, playlist_id, item_id, collection_id, edition_id, rank,
	(SELECT COUNT(*) FROM playlist_entry o WHERE o.playlist_id = playlist_entry.playlist_id
		AND (o.rank, o.id) < (playlist_entry.rank, playlist_entry.id)), added_at, created_at, modified_at, created_by, modified_by`

func scanEntry(s db.Scanner) (*Entry, error) {
	var e Entry
	var added, created, modified int64
	if err := s.Scan(&e.ID, &e.PlaylistID, &e.ItemID, &e.CollectionID, &e.EditionID, &e.Rank, &e.Position, &added,
		&created, &modified, &e.CreatedBy, &e.ModifiedBy); err != nil {
		return nil, db.Wrap(err)
	}
	e.AddedAt, e.CreatedAt, e.ModifiedAt = db.FromMs(added), db.FromMs(created), db.FromMs(modified)
	return &e, nil
}

// Entry returns one entry or db.ErrNotFound.
func (r *Repo) Entry(id uid.ID) (*Entry, error) {
	return scanEntry(r.d.QueryRow(`SELECT `+entryCols+` FROM playlist_entry WHERE id = ?`, id))
}

// Entries pages a playlist's entries in order. A kid's audience, or an
// adult's hiding kids' content, gets only the entries that stand for a row
// of the play order it may see.
func (r *Repo) Entries(playlistID uid.ID, a item.Audience, limit, offset int) ([]*Entry, error) {
	rows, err := r.d.Query(expansion+`, seen AS (SELECT DISTINCT x.entry_id`+visible+`)
		SELECT `+entryCols+` FROM playlist_entry WHERE playlist_id = ?1 AND (NOT ?5 OR id IN (SELECT entry_id FROM seen))
		ORDER BY rank, id LIMIT ?3 OFFSET ?4`, append([]any{playlistID, nil, limit, offset, a.Kid || a.HideKids}, a.Args()...)...)
	return db.Collect(rows, err, scanEntry)
}

// HasItem reports whether one of the playlist's entries is the item.
func (r *Repo) HasItem(playlistID, itemID uid.ID) (bool, error) {
	var ok bool
	err := r.d.QueryRow(`SELECT EXISTS (SELECT 1 FROM playlist_entry WHERE playlist_id = ? AND item_id = ?)`, playlistID, itemID).Scan(&ok)
	return ok, db.Wrap(err)
}

// CountEntries counts a playlist's entries.
func (r *Repo) CountEntries(playlistID uid.ID) (int, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM playlist_entry WHERE playlist_id = ?`, playlistID).Scan(&n)
	return n, db.Wrap(err)
}

// expansion is x over the entries of playlist ?1 (any for NULL), or of entry
// ?2 alone.
var expansion = `WITH e AS (
	SELECT id, rank, item_id, collection_id, edition_id FROM playlist_entry WHERE (?1 IS NULL OR playlist_id = ?1) AND (?2 IS NULL OR id = ?2)
)` + Branches

// targetExpansion is x over one unwritten entry, id NULL, naming item ?1 or
// collection ?2, and edition ?5 of a title (NULL for its default).
var targetExpansion = `WITH e AS (
	SELECT NULL AS id, '' AS rank, ?1 AS item_id, ?2 AS collection_id, ?5 AS edition_id
)` + Branches

// Branches is x, the play order of the entries e (id, rank, item_id,
// collection_id, edition_id): a movie, episode, song or video stands for
// itself, a show, season, album or artist for its episodes or songs as
// they are now, a channel for its videos by publish
// date, oldest first, a collection for its movies in collection.Order (k1
// and k2 are the position, k3 the date, '~' sorting an undated movie last). A
// title's row plays the edition its entry names, else the default, and a
// container's rows each member's default; edition_id is NULL for a title
// with no edition. k1 to k8 order the rows within one entry and mean
// something different in each branch. An artist's songs are those on its
// albums and those it performs on other albums; a song in both counts once.
// No playlist entry names a channel or video. CROSS JOIN holds SQLite to
// reaching items from the entries, never every item of a kind; the unary
// + keeps it from reaching an artist's songs through every song.
var Branches = `, x AS (
	SELECT e.id AS entry_id, e.rank AS pos, i.id AS item_id, COALESCE(e.edition_id, ` + edition.DefaultOf("i.id") + `) AS edition_id,
		0 AS k1, 0 AS k2, '' AS k3, 0 AS k4, 0 AS k5, 0 AS k6, 0 AS k7, '' AS k8
	FROM e CROSS JOIN item i ON i.id = e.item_id WHERE i.kind IN ('movie','episode','song','video')
	UNION ALL
	SELECT e.id, e.rank, v.id, ` + edition.DefaultOf("v.id") + `, yv.published_at IS NULL, COALESCE(yv.published_at, 0), '', v.added_at, 0, 0, 0, ''
	FROM e CROSS JOIN item v ON v.parent_id = e.item_id AND v.kind = 'video' LEFT JOIN item_metadata yv ON yv.item_id = v.id
	UNION ALL
	SELECT e.id, e.rank, ep.id, ` + edition.DefaultOf("ep.id") + `, COALESCE(se.season_number, 0) = 0, se.season_number, '', se.id, 0, 0,
		ep.episode_number, ''
	FROM e CROSS JOIN item se ON se.parent_id = e.item_id AND se.kind = 'season' CROSS JOIN item ep ON ep.parent_id = se.id AND ep.kind = 'episode'
	UNION ALL
	SELECT e.id, e.rank, ep.id, ` + edition.DefaultOf("ep.id") + `, 0, 0, '', 0, 0, 0, ep.episode_number, ''
	FROM e CROSS JOIN item ep ON ep.parent_id = e.item_id AND ep.kind = 'episode'
	UNION ALL
	SELECT e.id, e.rank, s.id, ` + edition.DefaultOf("s.id") + `, 0, 0, '', 0, COALESCE(s.disc_number, 1), s.track_number IS NULL,
		s.track_number, COALESCE(s.sort_title, s.parsed_title)
	FROM e CROSS JOIN item s ON s.parent_id = e.item_id AND s.kind = 'song'
	UNION ALL
	SELECT e.id, e.rank, s.id, ` + edition.DefaultOf("s.id") + `, al.parsed_year IS NULL, -al.parsed_year,
		COALESCE(al.sort_title, al.parsed_title), al.id, COALESCE(s.disc_number, 1), s.track_number IS NULL, s.track_number,
		COALESCE(s.sort_title, s.parsed_title)
	FROM e CROSS JOIN item al ON al.parent_id = e.item_id AND al.kind = 'album' CROSS JOIN item s ON s.parent_id = al.id AND s.kind = 'song'
	UNION ALL
	SELECT e.id, e.rank, s.id, ` + edition.DefaultOf("s.id") + `, al.parsed_year IS NULL, -al.parsed_year,
		COALESCE(al.sort_title, al.parsed_title), al.id, COALESCE(s.disc_number, 1), s.track_number IS NULL, s.track_number,
		COALESCE(s.sort_title, s.parsed_title)
	FROM e CROSS JOIN item s ON s.artist_id = e.item_id AND +s.kind = 'song' CROSS JOIN item al ON al.id = s.parent_id AND al.parent_id != e.item_id
	UNION ALL
	SELECT e.id, e.rank, item.id, ` + edition.DefaultOf("item.id") + `, cm.position IS NULL, COALESCE(cm.position, 0),
		COALESCE(` + collection.Date + `, '~'), 0, 0, 0, 0, ` + collection.Title + `
	FROM e CROSS JOIN collection_movie cm ON cm.collection_id = e.collection_id CROSS JOIN item ON item.id = cm.item_id
) `

// Runtime is row x's edition's runtime, else that of its largest file.
const Runtime = `(SELECT COALESCE(re.runtime_ms, (SELECT f.duration_ms FROM edition_file rf JOIN media_file f ON f.id = rf.media_file_id
	WHERE rf.edition_id = re.id ORDER BY f.size_bytes DESC, f.id LIMIT 1)) FROM edition re WHERE re.id = x.edition_id)`

// visible keeps the rows of x the audience bound at ?6 to ?10 may see, so a
// container never yields or counts an item its reader could not open, and
// drops a row whose edition has no file left. CROSS JOIN holds SQLite to
// reading x first, never the whole library.
var visible = ` FROM x CROSS JOIN item ON item.id = x.item_id WHERE ` + Playable + ` AND ` + item.VisibleWhere

// Playable keeps the rows of x whose edition, if any, has a file left.
const Playable = `(x.edition_id IS NULL OR EXISTS (SELECT 1 FROM edition_file vf WHERE vf.edition_id = x.edition_id))`

// args binds a playlist or entry as ?1 and ?2 (nil for none), ?3 and ?4
// (paging, if any), and the audience as ?6 to ?10.
func args(playlistID, entryID, limit, offset any, a item.Audience) []any {
	return append([]any{playlistID, entryID, limit, offset, nil}, a.Args()...)
}

// Row is one playable item of a playlist's play order, with the edition it
// plays (nil for none) and that edition's runtime. EntryID is nil in what
// Expand returns.
type Row struct {
	EntryID   *uid.ID
	ItemID    uid.ID
	Kind      string
	LibraryID uid.ID
	EditionID *uid.ID
	RuntimeMs *int64
}

// RowOrder is the play order of x's rows.
const RowOrder = `x.pos, x.entry_id, x.k1, x.k2, x.k3 COLLATE NOCASE, x.k4, x.k5, x.k6, x.k7, x.k8 COLLATE NOCASE, x.item_id`

// playRows selects x's rows in play order, paged by ?3 and ?4.
var playRows = `SELECT x.entry_id, x.item_id, item.kind, item.library_id, x.edition_id, ` + Runtime + visible + `
	ORDER BY ` + RowOrder + `
	LIMIT ?3 OFFSET ?4`

func (r *Repo) rows(q string, args []any) ([]Row, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, func(s db.Scanner) (Row, error) {
		var v Row
		var rt sql.NullInt64
		err := s.Scan(&v.EntryID, &v.ItemID, &v.Kind, &v.LibraryID, &v.EditionID, &rt)
		if rt.Valid {
			v.RuntimeMs = &rt.Int64
		}
		return v, db.Wrap(err)
	})
}

// PlayOrder pages what playing the playlist plays to the audience: its
// entries in order, each container expanded in library order. Nothing is
// deduplicated.
func (r *Repo) PlayOrder(playlistID uid.ID, a item.Audience, limit, offset int) ([]Row, error) {
	return r.rows(expansion+playRows, args(playlistID, nil, limit, offset, a))
}

// Expand is what an entry naming the item, or else the collection, would
// play to the audience, in order: a title plays editionID, nil for its
// default, and a channel its videos as well.
func (r *Repo) Expand(itemID, collectionID, editionID *uid.ID, a item.Audience) ([]Row, error) {
	return r.rows(targetExpansion+playRows, append([]any{itemID, collectionID, -1, 0, editionID}, a.Args()...))
}

// Totals is the length of a playlist's play order for the audience, the
// runtimes of its rows' editions added up, and the item of its first entry
// the audience may see (a collection's first such movie in collection
// order), nil when there is none.
func (r *Repo) Totals(playlistID uid.ID, a item.Audience) (count int, durationMs int64, first *uid.ID, err error) {
	err = r.d.QueryRow(expansion+`SELECT COUNT(*), COALESCE(SUM(`+Runtime+`), 0),
			(SELECT a FROM (SELECT f.rank, f.id, COALESCE((SELECT fi.id FROM item fi WHERE fi.id = f.item_id AND `+item.VisibleOf("fi")+`),
				(SELECT item.id FROM collection_movie cm JOIN item ON item.id = cm.item_id WHERE cm.collection_id = f.collection_id AND `+item.VisibleWhere+`
				ORDER BY `+collection.Order+` LIMIT 1)) AS a
			FROM playlist_entry f WHERE f.playlist_id = ?1) WHERE a IS NOT NULL ORDER BY rank, id LIMIT 1)`+visible,
		args(playlistID, nil, nil, nil, a)...).Scan(&count, &durationMs, &first)
	return count, durationMs, first, db.Wrap(err)
}

// Allows reports whether the audience may see every row of the playlist's
// play order, or for an adult hiding kids' content any row of it; an empty
// one allows anyone.
func (r *Repo) Allows(playlistID uid.ID, a item.Audience) (bool, error) {
	var ok bool
	err := r.d.QueryRow(expansion+`SELECT `+AllowsOf(`?6 = 2`)+` FROM (SELECT COUNT(*) AS total, COALESCE(SUM(`+item.VisibleWhere+`), 0) AS seen
		FROM x CROSS JOIN item ON item.id = x.item_id WHERE `+Playable+`)`, args(playlistID, nil, nil, nil, a)...).Scan(&ok)
	return ok, db.Wrap(err)
}

// AllowsOf is Allows over a play order of total rows, seen of them visible
// to an audience that hides kids' content when hideKids holds.
func AllowsOf(hideKids string) string { return `(seen = total OR ` + hideKids + ` AND seen > 0)` }

// EntryTotals is how many rows of the play order one entry stands for now
// for the audience, and their editions' runtimes added up.
func (r *Repo) EntryTotals(entryID uid.ID, a item.Audience) (count int, durationMs int64, err error) {
	err = r.d.QueryRow(expansion+`SELECT COUNT(*), COALESCE(SUM(`+Runtime+`), 0)`+visible,
		args(nil, entryID, nil, nil, a)...).Scan(&count, &durationMs)
	return count, durationMs, db.Wrap(err)
}

// Total is what EntryTotals counts for one entry.
type Total struct {
	Count      int
	DurationMs int64
}

// EntriesTotals is EntryTotals for every entry of the playlist, by entry id;
// an entry that stands for nothing the audience may see is absent.
func (r *Repo) EntriesTotals(playlistID uid.ID, a item.Audience) (map[uid.ID]Total, error) {
	rows, err := r.d.Query(expansion+`SELECT x.entry_id, COUNT(*), COALESCE(SUM(`+Runtime+`), 0)`+visible+` GROUP BY x.entry_id`,
		args(playlistID, nil, nil, nil, a)...)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[uid.ID]Total{}
	for rows.Next() {
		var id uid.ID
		var t Total
		if err := rows.Scan(&id, &t.Count, &t.DurationMs); err != nil {
			return nil, db.Wrap(err)
		}
		out[id] = t
	}
	return out, db.Wrap(rows.Err())
}

// InTx runs fn with a repository whose writes commit together, or not at
// all when fn fails.
func (r *Repo) InTx(fn func(*Repo) error) error {
	return db.InTx(r.d, func(d db.DBTX) error { return fn(New(d)) })
}

// AddEntry inserts e, by its ID when set (an existing one is
// db.ErrConflict), at its Rank, or with none at position at, nil or past the
// end last, and fills it as read back.
func (r *Repo) AddEntry(e *Entry, at *int, by *uid.ID) error {
	if e.ID.IsZero() {
		e.ID = uid.New()
	}
	return db.InTx(r.d, func(d db.DBTX) error {
		if e.Rank == "" {
			var err error
			if e.Rank, err = rankAt(d, e.PlaylistID, nil, at); err != nil {
				return err
			}
		}
		now := db.Ms(time.Now())
		_, err := d.Exec(`INSERT INTO playlist_entry (id, playlist_id, item_id, collection_id, edition_id, rank, added_at, created_at, modified_at,
			created_by, modified_by) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			e.ID, e.PlaylistID, e.ItemID, e.CollectionID, e.EditionID, e.Rank, now, now, now, by, by)
		if err != nil {
			return db.Wrap(err)
		}
		v, err := scanEntry(d.QueryRow(`SELECT `+entryCols+` FROM playlist_entry WHERE id = ?`, e.ID))
		if err == nil {
			*e = *v
		}
		return err
	})
}

// AddEntries appends entries of one playlist in order, ranked as a batch,
// each by its ID when set.
func (r *Repo) AddEntries(es []*Entry, by *uid.ID) error {
	if len(es) == 0 {
		return nil
	}
	return db.InTx(r.d, func(d db.DBTX) error {
		var last string
		err := d.QueryRow(`SELECT rank FROM playlist_entry WHERE playlist_id = ? ORDER BY rank DESC, id DESC LIMIT 1`, es[0].PlaylistID).Scan(&last)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return db.Wrap(err)
		}
		ranks, err := NKeysBetween(last, "", len(es))
		if err != nil {
			return err
		}
		for i, e := range es {
			e.Rank = ranks[i]
			if err := New(d).AddEntry(e, nil, by); err != nil {
				return err
			}
		}
		return nil
	})
}

// MoveEntry ranks an entry at position at among the others, nil or past
// the end last. No other entry changes.
func (r *Repo) MoveEntry(id uid.ID, at *int, by *uid.ID) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		e, err := scanEntry(d.QueryRow(`SELECT `+entryCols+` FROM playlist_entry WHERE id = ?`, id))
		if err != nil {
			return err
		}
		rank, err := rankAt(d, e.PlaylistID, &id, at)
		if err != nil {
			return err
		}
		return New(d).RankEntry(id, rank, by)
	})
}

// RankEntry gives an entry rank.
func (r *Repo) RankEntry(id uid.ID, rank string, by *uid.ID) error {
	return db.ExecOne(r.d, `UPDATE playlist_entry SET rank = ?, modified_at = ?, modified_by = ? WHERE id = ?`, rank, db.Ms(time.Now()), by, id)
}

// RemoveEntry deletes an entry; later entries move up.
func (r *Repo) RemoveEntry(id uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM playlist_entry WHERE id = ?`, id)
}

// rankAt is a rank for position at among the playlist's entries other than
// except (nil for none), nil or past the end last. No key sorts between two
// equal ranks, so a position between them lands after both.
func rankAt(d db.DBTX, playlistID uid.ID, except *uid.ID, at *int) (string, error) {
	rows, err := d.Query(`SELECT rank FROM playlist_entry WHERE playlist_id = ? AND id IS NOT ? ORDER BY rank, id`, playlistID, except)
	ranks, err := db.Collect(rows, err, db.Column[string])
	if err != nil {
		return "", err
	}
	pos := len(ranks)
	if at != nil && *at < pos {
		pos = *at
	}
	var lo, hi string
	if pos > 0 {
		lo = ranks[pos-1]
	}
	for _, k := range ranks[pos:] {
		if k != lo {
			hi = k
			break
		}
	}
	return KeyBetween(lo, hi)
}

// Ratings lists the distinct content ratings of the play order's rows as
// kids' filters read them, from a movie itself or an episode's show; ""
// stands for a row without one.
func (r *Repo) Ratings(playlistID uid.ID) ([]string, error) {
	rows, err := r.d.Query(expansion+`SELECT DISTINCT COALESCE((SELECT m.content_rating FROM item_shown m
			WHERE m.item_id = COALESCE((SELECT p.parent_id FROM item p WHERE p.id = item.parent_id), item.parent_id, item.id)), '')`+visible,
		args(playlistID, nil, nil, nil, item.Audience{})...)
	return db.Collect(rows, err, db.Column[string])
}
