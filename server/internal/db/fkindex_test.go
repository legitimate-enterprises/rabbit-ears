package db_test

import (
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// unindexed are the foreign keys whose child lookups may scan: tables small
// enough, or parents deleted rarely enough, that an index would cost more.
// Every created_by and modified_by is one too.
var unindexed = map[string]bool{
	"account_allow.item_id": true, "device_link.account_id": true, "device_link.device_id": true, "device.profile_id": true,
	"download.account_id": true, "download.edition_id": true, "download.item_id": true, "download.media_file_id": true,
	"library_folder.library_id": true, "playback_session.account_id": true, "playback_session.moved_to_device_id": true,
	"watch_together_member.device_id": true, "watch_together.household_id": true, "watch_together.item_id": true,
	"watch_together.paused_by": true,
}

// Deleting or re-pointing a parent row makes SQLite look up its children by the
// foreign-key column; without a leading index that is a scan per parent row.
func TestForeignKeyChildLookupsUseAnIndex(t *testing.T) {
	d := open(t)
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(`SELECT m.name, f."from" FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f
		WHERE m.type = 'table' ORDER BY m.name, f."from"`)
	if err != nil {
		t.Fatal(err)
	}
	var fks [][2]string
	for rows.Next() {
		var c [2]string
		if err := rows.Scan(&c[0], &c[1]); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, c)
	}
	if err := rows.Close(); err != nil || len(fks) == 0 {
		t.Fatalf("%d foreign keys: %v", len(fks), err)
	}
	for _, c := range fks {
		name := c[0] + "." + c[1]
		if unindexed[name] || c[1] == "created_by" || c[1] == "modified_by" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			rows, err := d.Query(`EXPLAIN QUERY PLAN SELECT 1 FROM "`+c[0]+`" WHERE "`+c[1]+`" = ?`, 1) //nolint:gosec // names read from the schema
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

// Who is in a household is read in most scope fragments: the membership
// view's union reads both its halves in order, sorting neither.
func TestMembersOfAHouseholdNeedNoSort(t *testing.T) {
	d := open(t)
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(`EXPLAIN QUERY PLAN SELECT account_id FROM membership WHERE household_id = ?`, uid.N(1))
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
		if strings.Contains(detail, "TEMP B-TREE") {
			t.Errorf("plan step %q", detail)
		}
	}
}
