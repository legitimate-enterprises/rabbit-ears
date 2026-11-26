// Package genre holds genres and which movies and shows carry them.
package genre

import (
	"database/sql"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

// Genre is a provider genre, or one an admin added (TMDBID nil).
type Genre struct {
	ID     int64
	Name   string
	TMDBID *int64
}

// Filter narrows List. Zero values mean no constraint.
type Filter struct {
	LibraryID int64
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
var seen = `((?1 = 0 AND ?6 = 0 AND (genre.tmdb_id IS NULL OR (SELECT use_remote FROM metadata_setting)))
	OR EXISTS (SELECT 1 FROM item_genre_shown ig JOIN item ON item.id = ig.item_id
	WHERE ig.genre_id = genre.id AND (?1 = 0 OR item.library_id = ?1) AND ` + item.VisibleWhere + `))`

func scan(s db.Scanner) (*Genre, error) {
	var g Genre
	var tmdb sql.NullInt64
	if err := s.Scan(&g.ID, &g.Name, &tmdb); err != nil {
		return nil, db.Wrap(err)
	}
	if tmdb.Valid {
		g.TMDBID = &tmdb.Int64
	}
	return &g, nil
}

// Create inserts g and fills its ID.
func (r *Repo) Create(g *Genre) error {
	res, err := r.d.Exec(`INSERT INTO genre (name, tmdb_id) VALUES (?, ?)`, g.Name, g.TMDBID)
	if err != nil {
		return db.Wrap(err)
	}
	g.ID, _ = res.LastInsertId()
	return nil
}

// Get returns one genre or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Genre, error) {
	return scan(r.d.QueryRow(`SELECT id, name, tmdb_id FROM genre WHERE id = ?`, id))
}

// Update rewrites a genre.
func (r *Repo) Update(g *Genre) error {
	return db.ExecOne(r.d, `UPDATE genre SET name = ?, tmdb_id = ? WHERE id = ?`, g.Name, g.TMDBID, g.ID)
}

// Delete removes a genre and its item links.
func (r *Repo) Delete(id int64) error { return db.ExecOne(r.d, `DELETE FROM genre WHERE id = ?`, id) }

func (r *Repo) query(q string, args ...any) ([]*Genre, error) {
	rows, err := r.d.Query(q, args...)
	return db.Collect(rows, err, scan)
}

// List pages genres by name.
func (r *Repo) List(f Filter) ([]*Genre, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	return r.query(`SELECT id, name, tmdb_id FROM genre WHERE `+seen+` ORDER BY name, id LIMIT ?2 OFFSET ?3`,
		append([]any{f.LibraryID, f.Limit, f.Offset, nil, nil}, f.Audience.Args()...)...)
}

// Seen reports whether the genre exists and the audience may see it.
func (r *Repo) Seen(id int64, a item.Audience) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM genre WHERE id = ?4 AND `+seen, append([]any{0, nil, nil, id, nil}, a.Args()...)...).Scan(&n)
	return n > 0, db.Wrap(err)
}

// ForItem lists the genres an item shows under the metadata settings by name.
func (r *Repo) ForItem(itemID int64) ([]*Genre, error) {
	return r.query(`SELECT g.id, g.name, g.tmdb_id FROM genre g JOIN item_genre_shown ig ON ig.genre_id = g.id WHERE ig.item_id = ? ORDER BY g.name`,
		itemID)
}

// From lists an item's genres from one source, Local or Remote, by name.
func (r *Repo) From(itemID int64, source string) ([]*Genre, error) {
	return r.query(`SELECT g.id, g.name, g.tmdb_id FROM genre g JOIN item_genre ig ON ig.genre_id = g.id WHERE ig.item_id = ? AND ig.source = ?
		ORDER BY g.name`, itemID, source)
}

// ForItems lists, as ForItem does, each item's genres, for those ids that
// have any.
func (r *Repo) ForItems(ids []int64) (map[int64][]*Genre, error) {
	out := map[int64][]*Genre{}
	if len(ids) == 0 {
		return out, nil
	}
	in, args := db.In(ids)
	rows, err := r.d.Query(`SELECT ig.item_id, g.id, g.name, g.tmdb_id FROM genre g JOIN item_genre_shown ig ON ig.genre_id = g.id
		WHERE ig.item_id IN `+in+` ORDER BY g.name`, args...)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var itemID int64
		var g Genre
		var tmdb sql.NullInt64
		if err := rows.Scan(&itemID, &g.ID, &g.Name, &tmdb); err != nil {
			return nil, db.Wrap(err)
		}
		if tmdb.Valid {
			g.TMDBID = &tmdb.Int64
		}
		out[itemID] = append(out[itemID], &g)
	}
	return out, rows.Err()
}

// SetForItem replaces an item's genres from one source, Local or Remote,
// with gs, creating genres by provider id, and adopting a hand-made genre
// of the same name. A stored name is kept, so an admin's rename survives
// refreshes.
func (r *Repo) SetForItem(itemID int64, source string, gs []Genre) error {
	return db.InTx(r.d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`DELETE FROM item_genre WHERE item_id = ? AND source = ?`, itemID, source); err != nil {
			return db.Wrap(err)
		}
		for _, g := range gs {
			var id int64
			if err := tx.QueryRow(`INSERT INTO genre (name, tmdb_id) VALUES (?, ?)
				ON CONFLICT(tmdb_id) DO UPDATE SET tmdb_id = excluded.tmdb_id
				ON CONFLICT(name) DO UPDATE SET tmdb_id = COALESCE(genre.tmdb_id, excluded.tmdb_id) RETURNING id`,
				g.Name, g.TMDBID).Scan(&id); err != nil {
				return db.Wrap(err)
			}
			if _, err := tx.Exec(`INSERT OR IGNORE INTO item_genre (item_id, genre_id, source) VALUES (?, ?, ?)`, itemID, id, source); err != nil {
				return db.Wrap(err)
			}
		}
		return nil
	})
}
