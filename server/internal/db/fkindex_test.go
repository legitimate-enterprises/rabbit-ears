package db_test

import (
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Deleting or re-pointing a parent row makes SQLite look up its children by the
// foreign-key column; without a leading index that is a scan per parent row.
func TestForeignKeyChildLookupsUseAnIndex(t *testing.T) {
	d := open(t)
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{
		{"session_plan_entry", "audio_track_id"},
		{"session_plan_entry", "subtitle_track_id"},
		{"playback_session", "media_file_id"},
		{"watch_state", "item_id"},
		{"watch_state", "edition_id"},
	} {
		t.Run(c[0]+"."+c[1], func(t *testing.T) {
			rows, err := d.Query(`EXPLAIN QUERY PLAN SELECT 1 FROM `+c[0]+` WHERE `+c[1]+` = ?`, 1) //nolint:gosec // table and column names are literals above
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(detail, "SEARCH ") || !strings.Contains(detail, c[1]) {
					t.Errorf("plan %q is not an index search on %s", detail, c[1])
				}
			}
		})
	}
}
