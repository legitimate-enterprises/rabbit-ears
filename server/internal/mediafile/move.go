package mediafile

import (
	"database/sql"
	"errors"
	"slices"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
)

func ids(d db.DBTX, q string, args ...any) ([]int64, error) {
	rows, err := d.Query(q, args...)
	return db.Collect(rows, err, db.Column[int64])
}

// Outside counts the files of the titles under root, a show or channel,
// that are recorded anywhere but under the folder dir.
func (r *Repo) Outside(root int64, dir string) (int, error) {
	var n int
	under := dir + "/"
	err := r.d.QueryRow(`WITH RECURSIVE t(id) AS (SELECT ?1 UNION ALL SELECT i.id FROM item i JOIN t ON i.parent_id = t.id)
		SELECT COUNT(DISTINCT f.id) FROM item_file x JOIN media_file f ON f.id = x.media_file_id
		WHERE x.item_id IN t AND substr(f.path, 1, length(?2)) != ?2`, root, under).Scan(&n)
	return n, db.Wrap(err)
}

// Absorb moves what is under from, a show or channel, under into, the one
// it proved to be a second record of, in one transaction, and deletes
// from. A season or an episode both have is folded into into's, so what
// was played or listed under either stays; the rest keeps its record.
func (r *Repo) Absorb(into, from int64) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		if err := absorb(d, into, from, `season_number`); err != nil {
			return err
		}
		return db.ExecOne(d, `DELETE FROM item WHERE id = ?`, from)
	})
}

// absorb re-parents from's children under into. A child whose number (a
// season's, then an episode's) one of into's has gives it its own
// children, or is folded into it when it has none to give.
func absorb(d db.DBTX, into, from int64, number string) error {
	kids, err := ids(d, `SELECT id FROM item WHERE parent_id = ? ORDER BY id`, from)
	if err != nil {
		return err
	}
	for _, kid := range kids {
		var twin int64
		err := d.QueryRow(`SELECT t.id FROM item k JOIN item t ON t.parent_id = ?1 AND t.kind = k.kind AND t.`+number+` = k.`+number+`
			WHERE k.id = ?2 AND k.kind IN ('season','episode')`, into, kid).Scan(&twin)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = d.Exec(`UPDATE item SET parent_id = ? WHERE id = ?`, into, kid)
		case err != nil:
		case number == `season_number`:
			if err = absorb(d, twin, kid, `episode_number`); err == nil {
				err = db.ExecOne(d, `DELETE FROM item WHERE id = ?`, kid)
			}
		default:
			err = edition.New(d).Fold(twin, kid)
		}
		if err != nil {
			return db.Wrap(err)
		}
	}
	return nil
}

// Move gives f, whose file was renamed or moved, the path the file is now
// at, in one transaction, and clears its missing mark; reread also marks
// it unprobed, for a file whose path says what it is. A song or video
// filed under f follows it to the path.
//
// A record an earlier scan made for the path, taking the moved file for a
// new one, goes: each title made for it alone is folded into f's title in
// the same place, so what was played or listed under either stays, and f
// stands where the record stood. Move returns the titles that may be left
// empty by that (a title not folded, and the parents and performers of
// those folded) for the caller to prune. Moving f where it is does nothing.
func (r *Repo) Move(f *File, path string, reread bool) (prune []int64, err error) {
	if f.Path == path {
		return nil, nil
	}
	err = db.InTx(r.d, func(d db.DBTX) error {
		var dup int64
		switch err := d.QueryRow(`SELECT id FROM media_file WHERE path = ?`, path).Scan(&dup); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return db.Wrap(err)
		default:
			const titles = `SELECT DISTINCT item_id FROM item_file WHERE media_file_id = ? ORDER BY item_id`
			own, err := ids(d, titles, f.ID)
			if err != nil {
				return err
			}
			made, err := ids(d, titles, dup)
			if err != nil {
				return err
			}
			for i, from := range made {
				if slices.Contains(own, from) {
					continue
				}
				var others int
				if err := d.QueryRow(`SELECT COUNT(*) FROM item_file WHERE item_id = ? AND media_file_id != ?`, from, dup).Scan(&others); err != nil {
					return db.Wrap(err)
				}
				if len(own) != len(made) || others > 0 {
					prune = append(prune, from)
					continue
				}
				above, err := ids(d, `SELECT parent_id FROM item WHERE id = ?1 AND parent_id IS NOT NULL
					UNION SELECT artist_id FROM item WHERE id = ?1 AND artist_id IS NOT NULL`, from)
				if err != nil {
					return err
				}
				prune = append(prune, above...)
				if err := edition.New(d).Fold(own[i], from); err != nil {
					return err
				}
			}
			if err := DeleteMade(d, func() error { return db.ExecOne(d, `DELETE FROM media_file WHERE id = ?`, dup) }, `SELECT ?`, dup); err != nil {
				return err
			}
		}
		if _, err := d.Exec(`UPDATE item SET path = ?1 WHERE path = ?2 AND kind IN ('song','video')
			AND id IN (SELECT item_id FROM item_file WHERE media_file_id = ?3)`, path, f.Path, f.ID); err != nil {
			return db.Wrap(err)
		}
		return db.ExecOne(d, `UPDATE media_file SET path = ?1, missing_at = NULL, probed_at = CASE WHEN ?2 THEN NULL ELSE probed_at END
			WHERE id = ?3`, path, reread, f.ID)
	})
	if err != nil {
		return nil, err
	}
	f.Path, f.MissingAt = path, nil
	if reread {
		f.ProbedAt = nil
	}
	return prune, nil
}
