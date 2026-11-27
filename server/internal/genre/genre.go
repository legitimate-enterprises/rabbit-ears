// Package genre holds genres and which movies and shows carry them.
package genre

import (
	"database/sql"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Genre is a provider genre, or one an admin added (TMDBID nil).
type Genre struct {
	ID         uid.ID
	Name       string
	TMDBID     *int64
	CreatedAt  time.Time
	ModifiedAt time.Time
}

// Filter narrows List. Zero values mean no constraint.
type Filter struct {
	LibraryID *uid.ID
	Audience  item.Audience
	Limit     int // 0 means 100
	Offset    int
}

// Sources of a genre link: a tag, or a provider.
const (
	Local  = "local"
	Remote = "remote"
)

// Repo persists genres and item links.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// seen holds a genre to those with an item the audience sees (in library ?1
// when set), by the links the metadata settings show; an adult with no
// library sees every genre, unused ones too, but TMDB's while the settings
// use no remote data.
var seen = `((?1 IS NULL AND ?6 = 0 AND (genre.tmdb_id IS NULL OR (SELECT use_remote FROM metadata_setting)))
	OR EXISTS (SELECT 1 FROM item_genre_shown ig JOIN item ON item.id = ig.item_id
	WHERE ig.genre_id = genre.id AND (?1 IS NULL OR item.library_id = ?1) AND ` + item.VisibleWhere + `))`

const cols = `id, name, tmdb_id, created_at, modified_at`

const gcols = `g.id, g.name, g.tmdb_id, g.created_at, g.modified_at`

func scan(s db.Scanner) (*Genre, error) {
	var g Genre
	if err := scanInto(s, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func scanInto(s db.Scanner, g *Genre, before ...any) error {
	var tmdb sql.NullInt64
	var created, modified int64
	if err := s.Scan(append(before, &g.ID, &g.Name, &tmdb, &created, &modified)...); err != nil {
		return db.Wrap(err)
	}
	if tmdb.Valid {
		g.TMDBID = &tmdb.Int64
	}
	g.CreatedAt, g.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return nil
}

// Create inserts g and fills its ID (unless set) and timestamps.
func (r *Repo) Create(g *Genre) error {
	if g.ID.IsZero() {
		g.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	g.CreatedAt, g.ModifiedAt = now, now
	_, err := r.d.Exec(`INSERT INTO genre (`+cols+`) VALUES (?, ?, ?, ?, ?)`, g.ID, g.Name, g.TMDBID, db.Ms(now), db.Ms(now))
	return db.Wrap(err)
}

// Get returns one genre or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Genre, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM genre WHERE id = ?`, id))
}

// Update rewrites a genre.
func (r *Repo) Update(g *Genre) error {
	g.ModifiedAt = time.Now().UTC().Truncate(time.Millisecond)
	return db.ExecOne(r.d, `UPDATE genre SET name = ?, tmdb_id = ?, modified_at = ? WHERE id = ?`, g.Name, g.TMDBID, db.Ms(g.ModifiedAt), g.ID)
}

// Delete removes a genre and its item links.
func (r *Repo) Delete(id uid.ID) error { return db.ExecOne(r.d, `DELETE FROM genre WHERE id = ?`, id) }

func (r *Repo) query(q string, args ...any) ([]*Genre, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// List pages genres by name.
func (r *Repo) List(f Filter) ([]*Genre, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	return r.query(`SELECT `+cols+` FROM genre WHERE `+seen+` ORDER BY name, id LIMIT ?2 OFFSET ?3`,
		append([]any{f.LibraryID, f.Limit, f.Offset, nil, nil}, f.Audience.Args()...)...)
}

// Seen reports whether the genre exists and the audience may see it.
func (r *Repo) Seen(id uid.ID, a item.Audience) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM genre WHERE id = ?4 AND `+seen, append([]any{nil, nil, nil, id, nil}, a.Args()...)...).Scan(&n)
	return n > 0, db.Wrap(err)
}

// ForItem lists the genres an item shows under the metadata settings by name.
func (r *Repo) ForItem(itemID uid.ID) ([]*Genre, error) {
	return r.query(`SELECT `+gcols+` FROM genre g JOIN item_genre_shown ig ON ig.genre_id = g.id WHERE ig.item_id = ? ORDER BY g.name`,
		itemID)
}

// From lists an item's genres from one source, Local or Remote, by name.
func (r *Repo) From(itemID uid.ID, source string) ([]*Genre, error) {
	return r.query(`SELECT `+gcols+` FROM genre g JOIN item_genre ig ON ig.genre_id = g.id WHERE ig.item_id = ? AND ig.source = ?
		ORDER BY g.name`, itemID, source)
}

// ForItems lists, as ForItem does, each item's genres, for those ids that
// have any.
func (r *Repo) ForItems(ids []uid.ID) (map[uid.ID][]*Genre, error) {
	out := map[uid.ID][]*Genre{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := db.In(ids)
	rows, err := r.d.Query(`SELECT ig.item_id, `+gcols+` FROM genre g JOIN item_genre_shown ig ON ig.genre_id = g.id
		WHERE ig.item_id IN `+in+` ORDER BY g.name`, args...)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var itemID uid.ID
		var g Genre
		if err := scanInto(rows, &g, &itemID); err != nil {
			return nil, err
		}
		out[itemID] = append(out[itemID], &g)
	}
	return out, rows.Err()
}

// SetForItem replaces an item's genres from one source, Local or Remote,
// with gs, creating genres by provider id, and adopting a hand-made genre
// of the same name. A stored name is kept, so an admin's rename survives
// refreshes. Only an adoption that gives a genre its provider id changes
// its modified_at.
func (r *Repo) SetForItem(itemID uid.ID, source string, gs []Genre) error {
	return db.InTx(r.d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`DELETE FROM item_genre WHERE item_id = ? AND source = ?`, itemID, source); err != nil {
			return db.Wrap(err)
		}
		for _, g := range gs {
			var id uid.ID
			now := db.Ms(time.Now())
			if err := tx.QueryRow(`INSERT INTO genre (`+cols+`) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(tmdb_id) DO UPDATE SET tmdb_id = excluded.tmdb_id
				ON CONFLICT(name) DO UPDATE SET tmdb_id = COALESCE(genre.tmdb_id, excluded.tmdb_id),
					modified_at = CASE WHEN genre.tmdb_id IS NULL AND excluded.tmdb_id IS NOT NULL THEN excluded.modified_at ELSE genre.modified_at END
				RETURNING id`,
				uid.New(), g.Name, g.TMDBID, now, now).Scan(&id); err != nil {
				return db.Wrap(err)
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO item_genre (item_id, genre_id, source) VALUES (?, ?, ?)`, itemID, id, source); err != nil {
				return db.Wrap(err)
			}
		}
		return nil
	})
}
