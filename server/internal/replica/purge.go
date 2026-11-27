package replica

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Retention is how long tombstones and processed actions are kept. A client
// whose version is older than the newest tombstone purged snapshots.
const Retention = 90 * 24 * time.Hour

// Purge deletes the tombstones and processed actions older than Retention
// at now, and reports how many.
func Purge(d *sql.DB, now time.Time) (string, error) {
	cutoff := db.Ms(now.Add(-Retention))
	var tombs, actions int64
	err := db.InTx(d, func(tx db.DBTX) error {
		if _, err := tx.Exec(`UPDATE sync_clock SET purged_version =
			MAX(purged_version, COALESCE((SELECT MAX(version) FROM sync_tombstone WHERE at < ?1), 0))`, cutoff); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM sync_tombstone WHERE at < ?`, cutoff)
		if err != nil {
			return err
		}
		tombs, _ = res.RowsAffected()
		if res, err = tx.Exec(`DELETE FROM sync_action WHERE processed_at < ?`, cutoff); err != nil {
			return err
		}
		actions, _ = res.RowsAffected()
		return nil
	})
	return fmt.Sprintf("%d %s and %d %s purged", tombs, plural(tombs, "tombstone"), actions, plural(actions, "action")), err
}

func plural(n int64, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}
