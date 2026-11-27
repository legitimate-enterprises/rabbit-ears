package replica_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
)

// Purging forgets tombstones and actions past the retention, and raises the
// purged version over the forgotten tombstones, so a client behind it
// snapshots rather than miss a deletion.
func TestPurge(t *testing.T) {
	d := seeded(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	old, fresh := db.Ms(now.Add(-replica.Retention-time.Minute)), db.Ms(now.Add(-replica.Retention+time.Minute))
	mustExec(t, d, `INSERT INTO sync_tombstone (tbl, k1, version, at) VALUES ('item', 7, 40, ?), ('item', 8, 30, ?), ('item', 9, 50, ?)`,
		old, old, fresh)
	mustExec(t, d, `INSERT INTO sync_action (device_id, uuid, result, processed_at) VALUES
		(1, '00000000-0000-0000-0000-000000000001', 'accepted', ?), (1, '00000000-0000-0000-0000-000000000002', 'rejected', ?)`, old, fresh)
	sum, err := replica.Purge(d, now)
	if err != nil {
		t.Fatal(err)
	}
	if sum != "2 tombstones and 1 action purged" {
		t.Errorf("summary %q", sum)
	}
	if got := strs(t, d, `SELECT k1 FROM sync_tombstone UNION ALL SELECT uuid FROM sync_action`); len(got) != 2 ||
		got[0] != "9" || got[1] != "00000000-0000-0000-0000-000000000002" {
		t.Errorf("kept %v", got)
	}
	if got := stamp(t, d, `SELECT purged_version FROM sync_clock`); got != 40 {
		t.Errorf("purged version %d", got)
	}
	// A purge that finds nothing old never lowers it.
	if _, err := replica.Purge(d, now); err != nil {
		t.Fatal(err)
	}
	if got := stamp(t, d, `SELECT purged_version FROM sync_clock`); got != 40 {
		t.Errorf("purged version %d after an empty purge", got)
	}
}
