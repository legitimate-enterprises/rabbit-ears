// Package cover holds the custom image an owner sets on a playlist or a
// station, and its repository: a JPEG the server made of an upload or a
// fetched link, or a title's artwork by reference.
package cover

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Owner names what a cover belongs to: exactly one of a playlist and a
// station, the other 0.
type Owner struct {
	PlaylistID int64
	StationID  int64
}

// Cover is one playlist's or station's custom image. ID is never reused,
// so a client may cache by it. Exactly one of ItemID, with Backdrop for
// which of its artwork, and JPEG is set.
type Cover struct {
	ID        int64
	Owner     Owner
	ItemID    int64
	Backdrop  bool
	JPEG      []byte
	CreatedAt time.Time
}

// Repo persists covers.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// where selects o's row; it binds the one id o names.
func where(o Owner) (string, int64) {
	if o.StationID != 0 {
		return `station_id = ?`, o.StationID
	}
	return `playlist_id = ?`, o.PlaylistID
}

// Of returns o's cover without its JPEG, or db.ErrNotFound.
func (r *Repo) Of(o Owner) (*Cover, error) {
	w, id := where(o)
	c := Cover{Owner: o}
	var created int64
	err := r.d.QueryRow(`SELECT id, COALESCE(item_id, 0), backdrop, created_at FROM cover WHERE `+w, id).
		Scan(&c.ID, &c.ItemID, &c.Backdrop, &created)
	c.CreatedAt = db.FromMs(created)
	return &c, db.Wrap(err)
}

// JPEG returns the stored image of cover id, or db.ErrNotFound.
func (r *Repo) JPEG(id int64) ([]byte, error) {
	var b []byte
	err := r.d.QueryRow(`SELECT jpeg FROM cover WHERE id = ? AND jpeg IS NOT NULL`, id).Scan(&b)
	return b, db.Wrap(err)
}

// Set replaces c.Owner's cover with c and fills ID and CreatedAt.
func (r *Repo) Set(c *Cover) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		w, id := where(c.Owner)
		if _, err := d.Exec(`DELETE FROM cover WHERE `+w, id); err != nil {
			return db.Wrap(err)
		}
		c.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
		var jpeg any
		if c.ItemID == 0 {
			jpeg = c.JPEG
		}
		res, err := d.Exec(`INSERT INTO cover (playlist_id, station_id, item_id, backdrop, jpeg, created_at) VALUES (?,?,?,?,?,?)`,
			db.NullZeroID(c.Owner.PlaylistID), db.NullZeroID(c.Owner.StationID), db.NullZeroID(c.ItemID), c.Backdrop, jpeg, db.Ms(c.CreatedAt))
		if err != nil {
			return db.Wrap(err)
		}
		c.ID, _ = res.LastInsertId()
		return nil
	})
}

// Delete removes o's cover, if it has one.
func (r *Repo) Delete(o Owner) error {
	w, id := where(o)
	_, err := r.d.Exec(`DELETE FROM cover WHERE `+w, id)
	return db.Wrap(err)
}
