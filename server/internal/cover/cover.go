// Package cover holds the custom image an owner sets on a playlist or a
// station, and its repository: a JPEG the server made of an upload or a
// fetched link, or a title's artwork by reference.
package cover

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Owner names what a cover belongs to: exactly one of a playlist and a
// station, the other nil.
type Owner struct {
	PlaylistID *uid.ID
	StationID  *uid.ID
}

// Cover is one playlist's or station's custom image. ID is never reused,
// so a client may cache by it. Exactly one of ItemID, with Backdrop for
// which of its artwork, and JPEG is set.
type Cover struct {
	ID         uid.ID
	Owner      Owner
	ItemID     *uid.ID
	Backdrop   bool
	JPEG       []byte
	CreatedAt  time.Time
	ModifiedAt time.Time
	CreatedBy  *uid.ID
	ModifiedBy *uid.ID
}

// Repo persists covers.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// where selects o's row; it binds the one id o names.
func where(o Owner) (string, *uid.ID) {
	if o.StationID != nil {
		return `station_id = ?`, o.StationID
	}
	return `playlist_id = ?`, o.PlaylistID
}

// Of returns o's cover without its JPEG, or db.ErrNotFound.
func (r *Repo) Of(o Owner) (*Cover, error) {
	w, id := where(o)
	c := Cover{Owner: o}
	var created, modified int64
	err := r.d.QueryRow(`SELECT id, item_id, backdrop, created_at, modified_at, created_by, modified_by FROM cover WHERE `+w, id).
		Scan(&c.ID, &c.ItemID, &c.Backdrop, &created, &modified, &c.CreatedBy, &c.ModifiedBy)
	c.CreatedAt, c.ModifiedAt = db.FromMs(created), db.FromMs(modified)
	return &c, db.Wrap(err)
}

// JPEG returns the stored image of cover id, or db.ErrNotFound.
func (r *Repo) JPEG(id uid.ID) ([]byte, error) {
	var b []byte
	err := r.d.QueryRow(`SELECT jpeg FROM cover WHERE id = ? AND jpeg IS NOT NULL`, id).Scan(&b)
	return b, db.Wrap(err)
}

// Set replaces c.Owner's cover with c, a new row with a new ID, and fills
// the audit fields.
func (r *Repo) Set(c *Cover, by *uid.ID) error {
	return db.InTx(r.d, func(d db.DBTX) error {
		w, id := where(c.Owner)
		if _, err := d.Exec(`DELETE FROM cover WHERE `+w, id); err != nil {
			return db.Wrap(err)
		}
		c.ID = uid.New()
		c.CreatedAt = time.Now().UTC().Truncate(time.Millisecond)
		c.ModifiedAt, c.CreatedBy, c.ModifiedBy = c.CreatedAt, by, by
		var jpeg any
		if c.ItemID == nil {
			jpeg = c.JPEG
		}
		_, err := d.Exec(`INSERT INTO cover (id, playlist_id, station_id, item_id, backdrop, jpeg, created_at, modified_at, created_by, modified_by)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, c.ID, c.Owner.PlaylistID, c.Owner.StationID, c.ItemID, c.Backdrop, jpeg,
			db.Ms(c.CreatedAt), db.Ms(c.ModifiedAt), by, by)
		return db.Wrap(err)
	})
}

// Delete removes o's cover, if it has one.
func (r *Repo) Delete(o Owner) error {
	w, id := where(o)
	_, err := r.d.Exec(`DELETE FROM cover WHERE `+w, id)
	return db.Wrap(err)
}
