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
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Sources of a collection.
const (
	TMDB   = "tmdb"
	Custom = "custom"
)

// Collection is a set of movies. A TMDB one is server-wide and carries
// TMDBID and provider fields; a custom one carries HouseholdID only.
type Collection struct {
	ID           uid.ID
	Source       string
	HouseholdID  *uid.ID
	TMDBID       *int64
	Name         string
	Overview     string
	PosterPath   string
	BackdropPath string
	CreatedAt    time.Time
	ModifiedAt   time.Time
	CreatedBy    *uid.ID
	ModifiedBy   *uid.ID
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

const cols = `c.id, c.source, c.household_id, c.tmdb_id, c.name, c.overview, c.poster_path, c.backdrop_path, c.created_at, c.modified_at,
	c.created_by, c.modified_by`

func scanInto(s db.Scanner, c *Collection, more ...any) error {
	var tmdb sql.NullInt64
	var overview, poster, backdrop sql.NullString
	var created, modified int64
	if err := s.Scan(append([]any{&c.ID, &c.Source, &c.HouseholdID, &tmdb, &c.Name, &overview, &poster, &backdrop, &created, &modified,
		&c.CreatedBy, &c.ModifiedBy}, more...)...); err != nil {
		return db.Wrap(err)
	}
	if tmdb.Valid {
		c.TMDBID = &tmdb.Int64
	}
	c.Overview, c.PosterPath, c.BackdropPath = overview.String, poster.String, backdrop.String
	c.CreatedAt, c.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return nil
}

// Create inserts a custom collection, by the acting profile by, and fills
// ID (unless set), Source and the audit fields.
func (r *Repo) Create(c *Collection, by *uid.ID) error {
	if c.ID.IsZero() {
		c.ID = uid.New()
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	c.Source, c.CreatedAt, c.ModifiedAt, c.CreatedBy, c.ModifiedBy = Custom, now, now, by, by
	_, err := r.d.Exec(`INSERT INTO collection (id, source, household_id, name, created_at, modified_at, created_by, modified_by)
		VALUES (?,?,?,?,?,?,?,?)`, c.ID, Custom, c.HouseholdID, c.Name, db.Ms(now), db.Ms(now), by, by)
	return db.Wrap(err)
}

// PutTMDB inserts or updates the TMDB collection c.TMDBID names and fills
// ID. An empty overview keeps the stored one. The metadata writes it, so no
// profile is its author.
func (r *Repo) PutTMDB(c *Collection) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	c.Source, c.ModifiedAt, c.ModifiedBy = TMDB, now, nil
	err := r.d.QueryRow(`INSERT INTO collection (id, source, tmdb_id, name, overview, poster_path, backdrop_path, created_at, modified_at)
		VALUES (?,'tmdb',?,?,?,?,?,?,?)
		ON CONFLICT(tmdb_id) DO UPDATE SET name=excluded.name, overview=COALESCE(excluded.overview, collection.overview),
			poster_path=excluded.poster_path, backdrop_path=excluded.backdrop_path, modified_at=excluded.modified_at, modified_by=NULL
		RETURNING id`, uid.New(), c.TMDBID, c.Name, db.NullStr(c.Overview), db.NullStr(c.PosterPath), db.NullStr(c.BackdropPath),
		db.Ms(now), db.Ms(now)).Scan(&c.ID)
	return db.Wrap(err)
}

// Get returns one collection, whoever may see it, or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Collection, error) {
	var c Collection
	if err := scanInto(r.d.QueryRow(`SELECT `+cols+` FROM collection c WHERE c.id = ?`, id), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Update writes c's name, by the acting profile by.
func (r *Repo) Update(c *Collection, by *uid.ID) error {
	c.ModifiedAt, c.ModifiedBy = time.Now().UTC().Truncate(time.Millisecond), by
	return db.ExecOne(r.d, `UPDATE collection SET name=?, modified_at=?, modified_by=? WHERE id=?`, c.Name, db.Ms(c.ModifiedAt), by, c.ID)
}

// Delete removes a collection; its memberships and the playlist entries
// holding it cascade.
func (r *Repo) Delete(id uid.ID) error {
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
// ?5 to those holding one movie, NULL meaning any.
var seen = `(c.household_id = ?1 OR c.source = 'tmdb' AND (SELECT use_remote FROM metadata_setting)
		AND (SELECT COUNT(*) FROM collection_movie n WHERE n.collection_id = c.id) >= 2)
	AND (?6 = 0 OR ` + visibleCount + ` > 0 OR ?6 = 2 AND NOT EXISTS (SELECT 1 FROM collection_movie e WHERE e.collection_id = c.id))
	AND (?2 IS NULL OR c.id = ?2)
	AND (?5 IS NULL OR EXISTS (SELECT 1 FROM collection_movie x WHERE x.collection_id = c.id AND x.item_id = ?5))`

// Count is how many collections List matches, ignoring limit and offset.
func (r *Repo) Count(householdID uid.ID, a item.Audience) (n int, err error) {
	return n, db.Wrap(r.d.QueryRow(`SELECT COUNT(*) FROM collection c WHERE `+seen, append([]any{householdID, nil, 0, 0, nil}, a.Args()...)...).Scan(&n))
}

func (r *Repo) views(householdID uid.ID, id, movieID *uid.ID, a item.Audience, limit, offset int) ([]*View, error) {
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
func (r *Repo) List(householdID uid.ID, a item.Audience, limit, offset int) ([]*View, error) {
	return r.views(householdID, nil, nil, a, limit, offset)
}

// Seen returns one collection the household and audience may see, else
// db.ErrNotFound.
func (r *Repo) Seen(householdID, id uid.ID, a item.Audience) (*View, error) {
	vs, err := r.views(householdID, &id, nil, a, 1, 0)
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
func (r *Repo) ForMovie(householdID, movieID uid.ID, a item.Audience) ([]*View, error) {
	return r.views(householdID, nil, &movieID, a, -1, 0)
}

// franchise is the candidates of NextMovie and MovieNeighbors: the movies
// of id's TMDB collection while the metadata settings use remote data,
// taken in Order.
const franchise = `FROM collection_movie cm JOIN item ON item.id = cm.item_id
		WHERE cm.collection_id = (SELECT x.collection_id FROM collection_movie x JOIN collection xc ON xc.id = x.collection_id
			WHERE x.item_id = ?1 AND xc.source = 'tmdb' AND (SELECT use_remote FROM metadata_setting))`

// NextMovie is, as item.Repo.Next finds it, the movie after id in its TMDB
// collection; nil for a movie in none.
func (r *Repo) NextMovie(id uid.ID, a item.Audience) (*uid.ID, error) {
	return item.New(r.d).Next(franchise, Order, id, a)
}

// MovieNeighbors are, as item.Repo.Neighbors finds them, the movies before
// and after id in its TMDB collection; nil for a movie in none.
func (r *Repo) MovieNeighbors(id uid.ID, a item.Audience) (prev, next *uid.ID, err error) {
	return item.New(r.d).Neighbors(franchise, Order, id, a)
}

// Movies pages the ids of a collection's movies the audience may see, in
// Order.
func (r *Repo) Movies(id uid.ID, a item.Audience, limit, offset int) ([]uid.ID, error) {
	rows, err := r.d.Query(`SELECT item.id FROM collection_movie cm JOIN item ON item.id = cm.item_id
		WHERE cm.collection_id = ?1 AND `+item.VisibleWhere+`
		ORDER BY `+Order+` LIMIT ?3 OFFSET ?4`,
		append([]any{id, nil, limit, offset, nil}, a.Args()...)...)
	return db.Collect(rows, err, func(s db.Scanner) (uid.ID, error) {
		var v uid.ID
		return v, db.Wrap(s.Scan(&v))
	})
}

// Around is up to n of a collection's movies other than movieID that the
// audience may see, in Movies order: those nearest movieID, half before it
// and half after, a side short of its half giving the rest to the other.
// movieID places the window even when the audience may not see it.
func (r *Repo) Around(id, movieID uid.ID, a item.Audience, n int) ([]uid.ID, error) {
	all, err := r.Movies(id, item.Audience{}, -1, 0)
	if err != nil {
		return nil, err
	}
	seen, err := r.Movies(id, a, -1, 0)
	if err != nil {
		return nil, err
	}
	var others []uid.ID
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
func (r *Repo) AddMovie(id, movieID uid.ID) error {
	_, err := r.d.Exec(`INSERT OR IGNORE INTO collection_movie (collection_id, item_id, position)
		VALUES (?1, ?2, (SELECT MAX(position) + 1 FROM collection_movie WHERE collection_id = ?1))`, id, movieID)
	return db.Wrap(err)
}

// MoveMovie moves a movie to position at among the collection's movies,
// shifting the others; nil or past the end moves it last. A collection
// without positions first takes them from its Order. db.ErrNotFound for a
// movie not in it.
func (r *Repo) MoveMovie(id, movieID uid.ID, at *int) error {
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
		ids, err := db.Collect(rows, err, func(s db.Scanner) (uid.ID, error) {
			var v uid.ID
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
			WHERE collection_id = ?1 AND item_id = unhex(j.value, '-')`, id, string(list))
		return db.Wrap(err)
	})
}

// ResetOrder returns a collection to release order.
func (r *Repo) ResetOrder(id uid.ID) error {
	_, err := r.d.Exec(`UPDATE collection_movie SET position = NULL WHERE collection_id = ?`, id)
	return db.Wrap(err)
}

// RemoveMovie takes a movie out of a collection, else db.ErrNotFound.
func (r *Repo) RemoveMovie(id, movieID uid.ID) error {
	return db.ExecOne(r.d, `DELETE FROM collection_movie WHERE collection_id = ? AND item_id = ?`, id, movieID)
}

// SetTMDBMovie makes collectionID the movie's only TMDB collection, or
// leaves it in none for nil. A TMDB collection left empty is deleted.
func (r *Repo) SetTMDBMovie(movieID uid.ID, collectionID *uid.ID) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		if _, err := d.Exec(`DELETE FROM collection_movie WHERE item_id = ? AND collection_id IS NOT ?
			AND collection_id IN (SELECT id FROM collection WHERE source = 'tmdb')`, movieID, collectionID); err != nil {
			return db.Wrap(err)
		}
		if collectionID == nil {
			return nil
		}
		return (&Repo{d}).AddMovie(*collectionID, movieID)
	})
}
