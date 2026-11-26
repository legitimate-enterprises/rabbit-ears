// Package collection holds movie collections, TMDB's franchise collections
// and each household's custom ones, with the movies they hold, and their
// repository.
package collection

import (
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

// Sources of a collection.
const (
	TMDB   = "tmdb"
	Custom = "custom"
)

// Collection is a set of movies. A TMDB one is server-wide and carries
// TMDBID and provider fields; a custom one carries HouseholdID only.
type Collection struct {
	ID           int64
	Source       string
	HouseholdID  *int64
	TMDBID       *int64
	Name         string
	Overview     string
	PosterPath   string
	BackdropPath string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// View is a collection as one reader sees it.
type View struct {
	Collection
	// MovieCount is how many of its movies the reader may see.
	MovieCount int
	// CustomOrder is whether a custom collection's movies carry positions.
	CustomOrder bool
}

// Date and Title order a collection's movies, over a table named item: by
// release date, a bare parsed year before the full dates of that year,
// undated (NULL) last; then by title. Each reads item_shown by key; a LEFT
// JOIN onto it builds the whole view.
const (
	Date  = `COALESCE((SELECT md.release_date FROM item_shown md WHERE md.item_id = item.id), CAST(item.parsed_year AS TEXT))`
	Title = `COALESCE((SELECT md.title FROM item_shown md WHERE md.item_id = item.id), item.parsed_title)`
)

// Order is the one order of a collection's movies, over collection_movie cm
// and the tables Date and Title name: by position while they carry one,
// else by Date then Title. Positions are all set or all NULL.
const Order = `cm.position IS NULL, cm.position, ` + Date + ` IS NULL, ` + Date + `, ` + Title + ` COLLATE NOCASE, item.id`

// Repo persists collections and their movies.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

const cols = `c.id, c.source, c.household_id, c.tmdb_id, c.name, c.overview, c.poster_path, c.backdrop_path, c.created_at, c.updated_at`

func scanInto(s db.Scanner, c *Collection, more ...any) error {
	var household, tmdb sql.NullInt64
	var overview, poster, backdrop sql.NullString
	var created, updated int64
	if err := s.Scan(append([]any{&c.ID, &c.Source, &household, &tmdb, &c.Name, &overview, &poster, &backdrop, &created, &updated}, more...)...); err != nil {
		return db.Wrap(err)
	}
	if household.Valid {
		c.HouseholdID = &household.Int64
	}
	if tmdb.Valid {
		c.TMDBID = &tmdb.Int64
	}
	c.Overview, c.PosterPath, c.BackdropPath = overview.String, poster.String, backdrop.String
	c.CreatedAt, c.UpdatedAt = db.FromMs(created), db.FromMs(updated)
	return nil
}

// Create inserts a custom collection and fills ID, Source and timestamps.
func (r *Repo) Create(c *Collection) error {
	now := time.Now().UTC()
	c.Source, c.CreatedAt, c.UpdatedAt = Custom, now, now
	res, err := r.d.Exec(`INSERT INTO collection (source, household_id, name, created_at, updated_at) VALUES (?,?,?,?,?)`,
		Custom, c.HouseholdID, c.Name, db.Ms(now), db.Ms(now))
	if err != nil {
		return db.Wrap(err)
	}
	c.ID, _ = res.LastInsertId()
	return nil
}

// PutTMDB inserts or updates the TMDB collection c.TMDBID names and fills
// ID. An empty overview keeps the stored one.
func (r *Repo) PutTMDB(c *Collection) error {
	now := time.Now().UTC()
	c.Source, c.UpdatedAt = TMDB, now
	err := r.d.QueryRow(`INSERT INTO collection (source, tmdb_id, name, overview, poster_path, backdrop_path, created_at, updated_at)
		VALUES ('tmdb',?,?,?,?,?,?,?)
		ON CONFLICT(tmdb_id) DO UPDATE SET name=excluded.name, overview=COALESCE(excluded.overview, collection.overview),
			poster_path=excluded.poster_path, backdrop_path=excluded.backdrop_path, updated_at=excluded.updated_at
		RETURNING id`, c.TMDBID, c.Name, db.NullStr(c.Overview), db.NullStr(c.PosterPath), db.NullStr(c.BackdropPath),
		db.Ms(now), db.Ms(now)).Scan(&c.ID)
	return db.Wrap(err)
}

// Get returns one collection, whoever may see it, or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Collection, error) {
	var c Collection
	if err := scanInto(r.d.QueryRow(`SELECT `+cols+` FROM collection c WHERE c.id = ?`, id), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Update writes c's name.
func (r *Repo) Update(c *Collection) error {
	c.UpdatedAt = time.Now().UTC()
	return db.ExecOne(r.d, `UPDATE collection SET name=?, updated_at=? WHERE id=?`, c.Name, db.Ms(c.UpdatedAt), c.ID)
}

// Delete removes a collection; its memberships and the playlist entries
// holding it cascade.
func (r *Repo) Delete(id int64) error {
	return db.ExecOne(r.d, `DELETE FROM collection WHERE id = ?`, id)
}

// visibleCount counts collection c's movies the audience bound at ?6 to
// ?10 may see.
var visibleCount = `(SELECT COUNT(*) FROM collection_movie cm JOIN item ON item.id = cm.item_id
	WHERE cm.collection_id = c.id AND ` + item.VisibleWhere + `)`

// handOrdered is whether collection c's movies carry positions.
const handOrdered = `EXISTS (SELECT 1 FROM collection_movie o WHERE o.collection_id = c.id AND o.position IS NOT NULL)`

// seen keeps the collections household ?1 and the audience at ?6 to ?10
// may see: the household's own custom ones, and, while the metadata
// settings use remote data, TMDB ones holding at least two movies; a kid
// sees none without a movie they may see, nor an adult hiding kids'
// content, who still sees one holding none. ?2 narrows to one collection and
// ?5 to those holding one movie, 0 meaning any.
var seen = `(c.household_id = ?1 OR c.source = 'tmdb' AND (SELECT use_remote FROM metadata_setting)
		AND (SELECT COUNT(*) FROM collection_movie n WHERE n.collection_id = c.id) >= 2)
	AND (?6 = 0 OR ` + visibleCount + ` > 0 OR ?6 = 2 AND NOT EXISTS (SELECT 1 FROM collection_movie e WHERE e.collection_id = c.id))
	AND (?2 = 0 OR c.id = ?2)
	AND (?5 = 0 OR EXISTS (SELECT 1 FROM collection_movie x WHERE x.collection_id = c.id AND x.item_id = ?5))`

// Count is how many collections List matches, ignoring limit and offset.
func (r *Repo) Count(householdID int64, a item.Audience) (n int, err error) {
	return n, db.Wrap(r.d.QueryRow(`SELECT COUNT(*) FROM collection c WHERE `+seen, append([]any{householdID, 0, 0, 0, 0}, a.Args()...)...).Scan(&n))
}

func (r *Repo) views(householdID, id, movieID int64, a item.Audience, limit, offset int) ([]*View, error) {
	rows, err := r.d.Query(`SELECT `+cols+`, `+visibleCount+`, c.source = 'custom' AND `+handOrdered+` FROM collection c WHERE `+seen+`
		ORDER BY c.name COLLATE NOCASE, c.id LIMIT ?3 OFFSET ?4`, append([]any{householdID, id, limit, offset, movieID}, a.Args()...)...)
	return db.Collect(rows, err, func(s db.Scanner) (*View, error) {
		var v View
		if err := scanInto(rows, &v.Collection, &v.MovieCount, &v.CustomOrder); err != nil {
			return nil, err
		}
		return &v, nil
	})
}

// List pages by name the collections the household and audience may see.
func (r *Repo) List(householdID int64, a item.Audience, limit, offset int) ([]*View, error) {
	return r.views(householdID, 0, 0, a, limit, offset)
}

// Seen returns one collection the household and audience may see, else
// db.ErrNotFound.
func (r *Repo) Seen(householdID, id int64, a item.Audience) (*View, error) {
	vs, err := r.views(householdID, id, 0, a, 1, 0)
	if err == nil && len(vs) == 0 {
		err = db.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return vs[0], nil
}

// ForMovie lists by name the collections holding the movie that the
// household and audience may see.
func (r *Repo) ForMovie(householdID, movieID int64, a item.Audience) ([]*View, error) {
	return r.views(householdID, 0, movieID, a, -1, 0)
}

// franchise is the candidates of NextMovie and MovieNeighbors: the movies
// of id's TMDB collection while the metadata settings use remote data,
// taken in Order.
const franchise = `FROM collection_movie cm JOIN item ON item.id = cm.item_id
		WHERE cm.collection_id = (SELECT x.collection_id FROM collection_movie x JOIN collection xc ON xc.id = x.collection_id
			WHERE x.item_id = ?1 AND xc.source = 'tmdb' AND (SELECT use_remote FROM metadata_setting))`

// NextMovie is, as item.Repo.Next finds it, the movie after id in its TMDB
// collection; 0 for a movie in none.
func (r *Repo) NextMovie(id int64, a item.Audience) (int64, error) {
	return item.New(r.d).Next(franchise, Order, id, a)
}

// MovieNeighbors are, as item.Repo.Neighbors finds them, the movies before
// and after id in its TMDB collection; 0 for a movie in none.
func (r *Repo) MovieNeighbors(id int64, a item.Audience) (prev, next int64, err error) {
	return item.New(r.d).Neighbors(franchise, Order, id, a)
}

// Movies pages the ids of a collection's movies the audience may see, in
// Order.
func (r *Repo) Movies(id int64, a item.Audience, limit, offset int) ([]int64, error) {
	rows, err := r.d.Query(`SELECT item.id FROM collection_movie cm JOIN item ON item.id = cm.item_id
		WHERE cm.collection_id = ?1 AND `+item.VisibleWhere+`
		ORDER BY `+Order+` LIMIT ?3 OFFSET ?4`,
		append([]any{id, nil, limit, offset, nil}, a.Args()...)...)
	return db.Collect(rows, err, func(s db.Scanner) (int64, error) {
		var v int64
		if err := s.Scan(&v); err != nil {
			return 0, db.Wrap(err)
		}
		return v, nil
	})
}

// Around is up to n of a collection's movies other than movieID that the
// audience may see, in Movies order: those nearest movieID, half before it
// and half after, a side short of its half giving the rest to the other.
// movieID places the window even when the audience may not see it.
func (r *Repo) Around(id, movieID int64, a item.Audience, n int) ([]int64, error) {
	all, err := r.Movies(id, item.Audience{}, -1, 0)
	if err != nil {
		return nil, err
	}
	seen, err := r.Movies(id, a, -1, 0)
	if err != nil {
		return nil, err
	}
	var others []int64
	at := 0
	for _, m := range all {
		if m == movieID {
			at = len(others)
		} else if slices.Contains(seen, m) {
			others = append(others, m)
		}
	}
	lo := max(min(at-n/2, len(others)-n), 0)
	return others[lo:min(lo+n, len(others))], nil
}

// AddMovie puts a movie in a collection, last when the collection is
// hand-ordered; it is idempotent.
func (r *Repo) AddMovie(id, movieID int64) error {
	_, err := r.d.Exec(`INSERT OR IGNORE INTO collection_movie (collection_id, item_id, position)
		VALUES (?1, ?2, (SELECT MAX(position) + 1 FROM collection_movie WHERE collection_id = ?1))`, id, movieID)
	return db.Wrap(err)
}

// MoveMovie moves a movie to position at among the collection's movies,
// shifting the others; nil or past the end moves it last. A collection
// without positions first takes them from its Order. db.ErrNotFound for a
// movie not in it.
func (r *Repo) MoveMovie(id, movieID int64, at *int) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM collection_movie WHERE collection_id = ? AND item_id = ?`, id, movieID).Scan(&n); err != nil {
			return db.Wrap(err)
		}
		if n == 0 {
			return db.ErrNotFound
		}
		rows, err := d.Query(`SELECT item.id FROM collection_movie cm JOIN item ON item.id = cm.item_id
			WHERE cm.collection_id = ?1 AND item.id != ?2 ORDER BY `+Order, id, movieID)
		ids, err := db.Collect(rows, err, func(s db.Scanner) (int64, error) {
			var v int64
			return v, db.Wrap(s.Scan(&v))
		})
		if err != nil {
			return err
		}
		pos := len(ids)
		if at != nil && *at < pos {
			pos = *at
		}
		ids = slices.Insert(ids, pos, movieID)
		list, _ := json.Marshal(ids)
		_, err = d.Exec(`UPDATE collection_movie SET position = j.key FROM json_each(?2) j
			WHERE collection_id = ?1 AND item_id = j.value`, id, string(list))
		return db.Wrap(err)
	})
}

// ResetOrder returns a collection to release order.
func (r *Repo) ResetOrder(id int64) error {
	_, err := r.d.Exec(`UPDATE collection_movie SET position = NULL WHERE collection_id = ?`, id)
	return db.Wrap(err)
}

// RemoveMovie takes a movie out of a collection, else db.ErrNotFound.
func (r *Repo) RemoveMovie(id, movieID int64) error {
	return db.ExecOne(r.d, `DELETE FROM collection_movie WHERE collection_id = ? AND item_id = ?`, id, movieID)
}

// SetTMDBMovie makes collectionID the movie's only TMDB collection, or
// leaves it in none for 0. A TMDB collection left empty is deleted.
func (r *Repo) SetTMDBMovie(movieID, collectionID int64) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		if _, err := d.Exec(`DELETE FROM collection_movie WHERE item_id = ? AND collection_id != ?
			AND collection_id IN (SELECT id FROM collection WHERE source = 'tmdb')`, movieID, collectionID); err != nil {
			return db.Wrap(err)
		}
		if collectionID == 0 {
			return nil
		}
		return (&Repo{d}).AddMovie(collectionID, movieID)
	})
}
