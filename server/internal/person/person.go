// Package person holds cast and crew and their credits on items.
package person

import (
	"database/sql"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
)

// Credit kinds.
const (
	Cast = "cast"
	Crew = "crew"
)

// Person is someone credited on an item. TMDBID is nil for people an admin
// added by hand.
type Person struct {
	ID          int64
	TMDBID      *int64
	Name        string
	ProfilePath string // provider headshot name
}

// Credit is one person's part in an item. Name and ProfilePath are read from
// the person; Position is billing order within the item's cast or crew.
type Credit struct {
	PersonID    int64
	Name        string
	ProfilePath string
	Kind        string
	Character   string
	Job         string
	Department  string
	Position    int
}

// Filter narrows List. Zero values mean no constraint.
type Filter struct {
	Q        string // case-insensitive substring of the name
	Audience item.Audience
	// BestFirst puts names starting with Q before the other matches.
	BestFirst bool
	Limit     int // 0 means 100
	Offset    int
}

// Repo persists people and credits.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// seen holds a person to those credited on an item the audience sees; an
// adult sees everyone, but no one from TMDB while the metadata settings use
// no remote data, which credits come from.
var seen = `(?6 = 0 AND (person.tmdb_id IS NULL OR (SELECT use_remote FROM metadata_setting))
	OR EXISTS (SELECT 1 FROM credit_shown c JOIN item ON item.id = c.item_id WHERE c.person_id = person.id AND ` + item.VisibleWhere + `))`

const cols = `id, tmdb_id, name, profile_path`

func scan(s db.Scanner) (*Person, error) {
	var p Person
	var tmdb sql.NullInt64
	var profile sql.NullString
	if err := s.Scan(&p.ID, &tmdb, &p.Name, &profile); err != nil {
		return nil, db.Wrap(err)
	}
	if tmdb.Valid {
		p.TMDBID = &tmdb.Int64
	}
	p.ProfilePath = profile.String
	return &p, nil
}

// Create inserts p and fills its ID.
func (r *Repo) Create(p *Person) error {
	res, err := r.d.Exec(`INSERT INTO person (tmdb_id, name, profile_path) VALUES (?, ?, ?)`, p.TMDBID, p.Name, db.NullStr(p.ProfilePath))
	if err != nil {
		return db.Wrap(err)
	}
	p.ID, _ = res.LastInsertId()
	return nil
}

// Upsert inserts p, or updates the person with the same TMDBID, and fills its ID.
func (r *Repo) Upsert(p *Person) error {
	return db.Wrap(r.d.QueryRow(`INSERT INTO person (tmdb_id, name, profile_path) VALUES (?, ?, ?)
		ON CONFLICT(tmdb_id) DO UPDATE SET name = excluded.name, profile_path = excluded.profile_path RETURNING id`,
		p.TMDBID, p.Name, db.NullStr(p.ProfilePath)).Scan(&p.ID))
}

// Get returns one person or db.ErrNotFound.
func (r *Repo) Get(id int64) (*Person, error) {
	return scan(r.d.QueryRow(`SELECT `+cols+` FROM person WHERE id = ?`, id))
}

// Update rewrites a person.
func (r *Repo) Update(p *Person) error {
	return db.ExecOne(r.d, `UPDATE person SET tmdb_id = ?, name = ?, profile_path = ? WHERE id = ?`, p.TMDBID, p.Name, db.NullStr(p.ProfilePath), p.ID)
}

// Delete removes a person and their credits.
func (r *Repo) Delete(id int64) error { return db.ExecOne(r.d, `DELETE FROM person WHERE id = ?`, id) }

// List pages people by name.
func (r *Repo) List(f Filter) ([]*Person, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	by := ``
	if f.BestFirst {
		by = `instr(lower(name), lower(?1)) != 1, `
	}
	rows, err := r.d.Query(`SELECT `+cols+` FROM person WHERE (?1 = '' OR instr(lower(name), lower(?1)) > 0) AND `+seen+`
		ORDER BY `+by+`name COLLATE NOCASE, id LIMIT ?2 OFFSET ?3`, append([]any{f.Q, f.Limit, f.Offset, nil, nil}, f.Audience.Args()...)...)
	return db.Collect(rows, err, scan)
}

// Seen reports whether the person exists and the audience may see them.
func (r *Repo) Seen(id int64, a item.Audience) (bool, error) {
	var n int
	err := r.d.QueryRow(`SELECT COUNT(*) FROM person WHERE id = ?1 AND `+seen, append([]any{id, nil, nil, nil, nil}, a.Args()...)...).Scan(&n)
	return n > 0, db.Wrap(err)
}

// Credits lists an item's cast in billing order, then its crew; none while
// the metadata settings use no remote data.
func (r *Repo) Credits(itemID int64) ([]*Credit, error) {
	rows, err := r.d.Query(`SELECT c.person_id, p.name, p.profile_path, c.kind, c.character, c.job, c.department, c.position
		FROM credit_shown c JOIN person p ON p.id = c.person_id WHERE c.item_id = ? ORDER BY c.kind, c.position`, itemID)
	return db.Collect(rows, err, func(s db.Scanner) (*Credit, error) {
		var c Credit
		var profile, character, job, dept sql.NullString
		if err := s.Scan(&c.PersonID, &c.Name, &profile, &c.Kind, &character, &job, &dept, &c.Position); err != nil {
			return nil, db.Wrap(err)
		}
		c.ProfilePath, c.Character, c.Job, c.Department = profile.String, character.String, job.String, dept.String
		return &c, nil
	})
}

// SetCredits replaces an item's credits atomically. Positions follow the
// order of cs within each kind.
func (r *Repo) SetCredits(itemID int64, cs []Credit) error {
	return db.InTx(r.d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`DELETE FROM credit WHERE item_id = ?`, itemID); err != nil {
			return db.Wrap(err)
		}
		next := map[string]int{}
		for _, c := range cs {
			if _, err := tx.Exec(`INSERT INTO credit (item_id, person_id, kind, character, job, department, position) VALUES (?,?,?,?,?,?,?)`,
				itemID, c.PersonID, c.Kind, db.NullStr(c.Character), db.NullStr(c.Job), db.NullStr(c.Department), next[c.Kind]); err != nil {
				return db.Wrap(err)
			}
			next[c.Kind]++
		}
		return nil
	})
}
