package serverinfo

import (
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// ArtworkGeneration is the generation clients cache images under. It
// advances once an image has changed and nothing has changed for settle, so
// a scan that writes images for an hour moves clients once, at its end.
func (r *Repo) ArtworkGeneration(now time.Time, settle time.Duration) (int64, error) {
	var generation, settled, changedAt int64
	if err := r.d.QueryRow(`SELECT generation, settled, changed_at FROM artwork_generation WHERE id = 1`).
		Scan(&generation, &settled, &changedAt); err != nil {
		return 0, db.Wrap(err)
	}
	if generation == settled || changedAt > db.Ms(now.Add(-settle)) {
		return settled, nil
	}
	// A change made since the read leaves its own generation to be settled later.
	_, err := r.d.Exec(`UPDATE artwork_generation SET settled = ? WHERE id = 1 AND generation = ?`, generation, generation)
	return generation, db.Wrap(err)
}
