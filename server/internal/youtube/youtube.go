// Package youtube reads what a YouTube library's scan recorded about its
// channels and videos, and records each video's publish time and sidecar
// stamp beside its metadata. Avatars fetches channels' pictures from YouTube.
package youtube

import (
	"database/sql"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Repo reads channel and video views and writes video sources.
type Repo struct{ d db.DBTX }

// New returns a repository over d.
func New(d db.DBTX) *Repo { return &Repo{d} }

// SetSource records a video's publish time and the stamp of the sidecars
// it was read from, which order its channel under every metadata setting.
// The video's local row must exist.
func (r *Repo) SetSource(itemID int64, publishedAt time.Time, stamp int64) error {
	return db.ExecOne(r.d, `UPDATE item_metadata SET published_at = ?, sidecar_stamp = ? WHERE item_id = ? AND source = 'local'`, db.Ms(publishedAt), stamp, itemID)
}

// Stamp is the sidecar stamp a video was last read with; -1, which no
// stamp is, when it was never read.
func (r *Repo) Stamp(itemID int64) (int64, error) {
	s := sql.NullInt64{Int64: -1}
	err := r.d.QueryRow(`SELECT sidecar_stamp FROM item_metadata WHERE item_id = ? AND source = 'local'`, itemID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !s.Valid {
		return -1, nil
	}
	return s.Int64, db.Wrap(err)
}
