// Package edition holds a title's editions, each one cut of it, and the
// files that are interchangeable copies of each, and their repository.
package edition

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// CutGapMs is how far apart, in milliseconds, the runtimes of two untagged
// files of one movie may be and still be copies of one cut.
const CutGapMs = 3 * 60 * 1000

// Edition is one cut of a title (an item).
type Edition struct {
	ID         uid.ID
	ItemID     uid.ID
	CreatedAt  time.Time
	ModifiedAt time.Time
	// Tag is the {edition-Name} of its files; "" for an untagged cut.
	Tag       string
	RuntimeMs *int64
	Files     int
	onDisk    int // Files not marked missing
	// Name and Default are filled on every read. An untagged cut is
	// "Standard", or "N-minute cut" while the title has several with files;
	// the default is the first untagged edition with a file on disk, else
	// the first with one, and when none has one the same choice among those
	// with files.
	Name    string
	Default bool
}

// Copy is one file of an edition and its first video track's size.
type Copy struct {
	MediaFileID uid.ID
	Width       *int
	Height      *int
	HDRFormat   string
	Missing     bool // the file is marked gone from disk
}

// Quality labels a copy by its picture: 4K, 1080p, 720p or SD, and "" for
// a file without video.
func (c Copy) Quality() string {
	w, h := 0, 0
	if c.Width != nil {
		w = *c.Width
	}
	if c.Height != nil {
		h = *c.Height
	}
	switch {
	case w >= 3200 || h >= 2000:
		return "4K"
	case w >= 1600 || h >= 1000:
		return "1080p"
	case w >= 1200 || h >= 700:
		return "720p"
	case w > 0 || h > 0:
		return "SD"
	}
	return ""
}

// DefaultOf is the SQL for the default edition of the item whose id is the
// expression it; NULL when no edition of it has a file. It agrees with
// Edition.Default.
func DefaultOf(it string) string {
	return `(SELECT de.id FROM edition de WHERE de.item_id = ` + it + `
		AND EXISTS (SELECT 1 FROM edition_file def WHERE def.edition_id = de.id)
		ORDER BY NOT EXISTS (SELECT 1 FROM edition_file dp JOIN media_file dpf ON dpf.id = dp.media_file_id
			WHERE dp.edition_id = de.id AND dpf.missing_at IS NULL), de.tag IS NOT NULL, de.id LIMIT 1)`
}

// Repo persists editions and which files they hold.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// all lists every edition of an item, files or not, in id order with its
// runtime falling back to its largest file's, and fills Name and Default.
func all(d db.DBTX, itemID uid.ID) ([]*Edition, error) {
	rows, err := d.Query(`SELECT e.id, e.item_id, e.created_at, e.modified_at, COALESCE(e.tag, ''), COALESCE(e.runtime_ms, (SELECT f.duration_ms FROM edition_file ef
			JOIN media_file f ON f.id = ef.media_file_id WHERE ef.edition_id = e.id ORDER BY f.size_bytes DESC, f.id LIMIT 1)),
			(SELECT COUNT(*) FROM edition_file ef WHERE ef.edition_id = e.id),
			(SELECT COUNT(*) FROM edition_file ef JOIN media_file f ON f.id = ef.media_file_id
				WHERE ef.edition_id = e.id AND f.missing_at IS NULL)
		FROM edition e WHERE e.item_id = ? ORDER BY e.id`, itemID)
	if err != nil {
		return nil, db.Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Edition
	for rows.Next() {
		var e Edition
		var rt sql.NullInt64
		var created, modified int64
		if err := rows.Scan(&e.ID, &e.ItemID, &created, &modified, &e.Tag, &rt, &e.Files, &e.onDisk); err != nil {
			return nil, db.Wrap(err)
		}
		e.CreatedAt, e.ModifiedAt = db.FromMs(created), db.FromMs(modified)
		if rt.Valid {
			e.RuntimeMs = &rt.Int64
		}
		out = append(out, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, db.Wrap(err)
	}
	cuts := 0
	var def *Edition
	for _, e := range out {
		if e.Tag == "" && e.Files > 0 {
			cuts++
		}
	}
	for _, needOnDisk := range []bool{true, false} {
		for _, e := range out {
			if e.Files > 0 && (!needOnDisk || e.onDisk > 0) && (def == nil || def.Tag != "" && e.Tag == "") {
				def = e
			}
		}
		if def != nil {
			break
		}
	}
	for _, e := range out {
		e.Default, e.Name = e == def, e.Tag
		if e.Tag == "" {
			e.Name = "Standard"
			if cuts > 1 && e.RuntimeMs != nil {
				e.Name = fmt.Sprintf("%d-minute cut", (*e.RuntimeMs+30000)/60000)
			}
		}
	}
	return out, nil
}

// ForItem lists the editions of an item that have files, the default first,
// then by name.
func (r *Repo) ForItem(itemID uid.ID) ([]*Edition, error) {
	es, err := all(r.d, itemID)
	if err != nil {
		return nil, err
	}
	out := es[:0]
	for _, e := range es {
		if e.Files > 0 {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Default != out[j].Default {
			return out[i].Default
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Get returns one edition, with or without files, or db.ErrNotFound.
func (r *Repo) Get(id uid.ID) (*Edition, error) {
	var itemID uid.ID
	if err := r.d.QueryRow(`SELECT item_id FROM edition WHERE id = ?`, id).Scan(&itemID); err != nil {
		return nil, db.Wrap(err)
	}
	es, err := all(r.d, itemID)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.ID == id {
			return e, nil
		}
	}
	return nil, db.ErrNotFound
}

// Default returns the item's default edition, or db.ErrNotFound when no
// edition of it has a file.
func (r *Repo) Default(itemID uid.ID) (*Edition, error) {
	es, err := all(r.d, itemID)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.Default {
			return e, nil
		}
	}
	return nil, db.ErrNotFound
}

// Of returns the edition of the item that holds the file, or db.ErrNotFound.
func (r *Repo) Of(itemID, fileID uid.ID) (uid.ID, error) {
	var id uid.ID
	err := r.d.QueryRow(`SELECT edition_id FROM item_file WHERE item_id = ? AND media_file_id = ?`, itemID, fileID).Scan(&id)
	return id, db.Wrap(err)
}

// ItemIDs lists the titles a file belongs to, in id order.
func (r *Repo) ItemIDs(fileID uid.ID) ([]uid.ID, error) {
	rows, err := r.d.Query(`SELECT DISTINCT item_id FROM item_file WHERE media_file_id = ? ORDER BY item_id`, fileID)
	return db.Collect(rows, err, db.Column[uid.ID])
}

// Copies lists an edition's files, the tallest picture first.
func (r *Repo) Copies(editionID uid.ID) ([]Copy, error) {
	rows, err := r.d.Query(`SELECT f.id, t.width, t.height, COALESCE(t.hdr_format, ''), f.missing_at IS NOT NULL FROM edition_file ef
		JOIN media_file f ON f.id = ef.media_file_id
		LEFT JOIN track t ON t.id = (SELECT v.id FROM track v WHERE v.media_file_id = f.id AND v.kind = 'video' ORDER BY v.stream_index LIMIT 1)
		WHERE ef.edition_id = ? ORDER BY COALESCE(t.height, 0) DESC, COALESCE(t.width, 0) DESC, f.size_bytes DESC, f.id`, editionID)
	return db.Collect(rows, err, func(s db.Scanner) (Copy, error) {
		var c Copy
		var w, h sql.NullInt64
		if err := s.Scan(&c.MediaFileID, &w, &h, &c.HDRFormat, &c.Missing); err != nil {
			return Copy{}, db.Wrap(err)
		}
		c.Width, c.Height = db.FromNullInt(w), db.FromNullInt(h)
		return c, nil
	})
}

// pick is the edition of es a file goes in, nil for a new one. A tag matches
// its edition case-insensitively. Without cuts an untagged file joins the
// first untagged edition; with cuts, the untagged edition whose runtime is
// nearest and within CutGapMs, one of unknown runtime matching any, and a
// file of unknown duration the first.
func pick(es []*Edition, tag string, durationMs *int64, cuts bool) *Edition {
	var best *Edition
	bestGap := int64(CutGapMs + 1)
	for _, e := range es {
		switch {
		case tag != "":
			if strings.EqualFold(e.Tag, tag) {
				return e
			}
		case e.Tag != "":
		case !cuts || durationMs == nil:
			return e
		default:
			gap := int64(0)
			if e.RuntimeMs != nil {
				gap = max(*durationMs-*e.RuntimeMs, *e.RuntimeMs-*durationMs)
			}
			if gap < bestGap {
				best, bestGap = e, gap
			}
		}
	}
	return best
}

// Place files a file under the item's edition for its tag and duration
// (see pick), making the edition when there is none, and takes it out of
// the item's other editions. It returns the edition's id.
func (r *Repo) Place(fileID, itemID uid.ID, tag string, durationMs *int64, cuts bool) (uid.ID, error) {
	var id uid.ID
	err := db.InTx(r.d, func(d db.DBTX) error {
		es, err := all(d, itemID)
		if err != nil {
			return err
		}
		if e := pick(es, tag, durationMs, cuts); e == nil {
			var runtime any
			if durationMs != nil {
				runtime = *durationMs
			}
			id = uid.New()
			now := db.Ms(time.Now())
			if _, err := d.Exec(`INSERT INTO edition (id, item_id, tag, runtime_ms, created_at, modified_at) VALUES (?,?,?,?,?,?)`,
				id, itemID, db.NullStr(tag), runtime, now, now); err != nil {
				return db.Wrap(err)
			}
		} else {
			id = e.ID
			if _, err := d.Exec(`UPDATE edition SET runtime_ms = ?, modified_at = ? WHERE id = ? AND runtime_ms IS NULL`,
				durationMs, db.Ms(time.Now()), id); err != nil {
				return db.Wrap(err)
			}
		}
		// The new link goes in before the old ones go, so the file is
		// never without an edition and outlives the move.
		if _, err := d.Exec(`INSERT OR IGNORE INTO edition_file (edition_id, media_file_id) VALUES (?,?)`, id, fileID); err != nil {
			return db.Wrap(err)
		}
		_, err = d.Exec(`DELETE FROM edition_file WHERE media_file_id = ? AND edition_id != ?
			AND edition_id IN (SELECT id FROM edition WHERE item_id = ?)`, fileID, id, itemID)
		return db.Wrap(err)
	})
	return id, err
}

// Unlink takes a file out of every edition of the item. A file left in no
// edition is deleted.
func (r *Repo) Unlink(fileID, itemID uid.ID) error {
	_, err := r.d.Exec(`DELETE FROM edition_file WHERE media_file_id = ? AND edition_id IN (SELECT id FROM edition WHERE item_id = ?)`,
		fileID, itemID)
	return db.Wrap(err)
}

// Fold makes the movie from part of the movie into and deletes it, in one
// transaction. Each of from's editions joins the edition of into that a
// file of its tag and runtime would, or moves over whole when none would.
// Rows that named from or its editions name into's, a row into already has
// winning; per account the more recently played watch state is kept,
// watched if either was. from's metadata, genres and credits go with it:
// into holds the same match's.
func (r *Repo) Fold(into, from uid.ID) error {
	now := db.Ms(time.Now())
	return db.InTx(r.d, func(d db.DBTX) error {
		es, err := all(d, from)
		if err != nil {
			return err
		}
		for _, e := range es {
			targets, err := all(d, into)
			if err != nil {
				return err
			}
			t := pick(targets, e.Tag, e.RuntimeMs, true)
			if t == nil {
				if _, err := d.Exec(`UPDATE edition SET item_id = ?, modified_at = ? WHERE id = ?`, into, now, e.ID); err != nil {
					return db.Wrap(err)
				}
				continue
			}
			// The files join t before e goes, so none is left in no edition.
			for _, q := range []string{
				`INSERT OR IGNORE INTO edition_file (edition_id, media_file_id) SELECT ?1, media_file_id FROM edition_file WHERE edition_id = ?2`,
				`UPDATE playlist_entry SET edition_id = ?1 WHERE edition_id = ?2`,
				`UPDATE station_program SET edition_id = ?1 WHERE edition_id = ?2`,
				`UPDATE watch_state SET edition_id = ?1 WHERE edition_id = ?2`,
				`UPDATE download SET edition_id = ?1 WHERE edition_id = ?2`,
				`DELETE FROM edition WHERE id = ?2`,
			} {
				if _, err := d.Exec(q, t.ID, e.ID); err != nil {
					return db.Wrap(err)
				}
			}
		}
		for _, q := range []string{
			`UPDATE watch_state AS k SET position_ms = g.position_ms, play_count = g.play_count, last_played_at = g.last_played_at,
				changed_at = g.changed_at, edition_id = g.edition_id, watched = max(k.watched, g.watched)
				FROM watch_state g WHERE k.item_id = ?1 AND g.item_id = ?2 AND g.account_id = k.account_id
				AND COALESCE(g.last_played_at, g.changed_at) > COALESCE(k.last_played_at, k.changed_at)`,
			`UPDATE watch_state AS k SET watched = 1 FROM watch_state g
				WHERE k.item_id = ?1 AND g.item_id = ?2 AND g.account_id = k.account_id AND g.watched = 1`,
			`UPDATE OR IGNORE watch_state SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE OR IGNORE account_allow SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE OR IGNORE collection_movie SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE playlist_entry SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE station_program SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE station_loop SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE playback_session SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE watch_together SET item_id = ?1, modified_at = ?3 WHERE item_id = ?2`,
			`UPDATE download SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE cover SET item_id = ?1 WHERE item_id = ?2`,
			`UPDATE log_entry SET item_id = ?1, modified_at = ?3 WHERE item_id = ?2`,
			`DELETE FROM item WHERE id = ?2`,
		} {
			if _, err := d.Exec(q, into, from, now); err != nil {
				return db.Wrap(err)
			}
		}
		return nil
	})
}
