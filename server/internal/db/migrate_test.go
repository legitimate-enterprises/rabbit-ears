package db_test

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func migrated(t *testing.T) *sql.DB {
	t.Helper()
	d := open(t)
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestMigrateFromEmpty(t *testing.T) {
	d := migrated(t)
	if v, _ := db.Version(d); v != db.LatestVersion() || v < 1 {
		t.Fatalf("version = %d", v)
	}
	var fk int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatalf("second Migrate must be a no-op: %v", err)
	}
}

func TestMigrateRefusesNewerDatabase(t *testing.T) {
	d := migrated(t)
	if _, err := d.Exec(`INSERT INTO schema_migration VALUES (999,'x',0)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err == nil {
		t.Fatal("want error for newer database")
	}
}

func TestTimeIsUTCInstant(t *testing.T) {
	loc := time.FixedZone("x", -7*3600)
	in := time.Date(2026, 1, 2, 3, 4, 5, 6000000, loc)
	out := db.FromMs(db.Ms(in))
	if !out.Equal(in) || out.Location() != time.UTC {
		t.Fatalf("got %v", out)
	}
}

func TestWrapConstraints(t *testing.T) {
	d := migrated(t)
	ins := `INSERT INTO library (id, name, kind, created_at, modified_at) VALUES (?, ?, 'movies', 1, 1)`
	a := uid.New()
	if _, err := d.Exec(ins, a, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(ins, uid.New(), "a"); !isErr(db.Wrap(err), db.ErrConflict) {
		t.Fatalf("unique: %v", db.Wrap(err))
	}
	// A client that sends an id already taken.
	if _, err := d.Exec(ins, a, "b"); !isErr(db.Wrap(err), db.ErrConflict) {
		t.Fatalf("primary key: %v", db.Wrap(err))
	}
	_, err := d.Exec(`INSERT INTO library_folder (id, library_id, path, created_at, modified_at) VALUES (?, ?, '/x', 1, 1)`, uid.New(), uid.N(99))
	if !isErr(db.Wrap(err), db.ErrReference) {
		t.Fatalf("fk: %v", db.Wrap(err))
	}
}

func isErr(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}

func TestIn(t *testing.T) {
	d := migrated(t)
	ids := []uid.ID{uid.N(1), uid.N(2), uid.N(3)}
	for i, id := range ids {
		if _, err := d.Exec(`INSERT INTO library (id, name, kind, created_at, modified_at) VALUES (?, ?, 'movies', 1, 1)`, id, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	in, args := db.In([]uid.ID{uid.N(3), uid.N(1)})
	rows, err := d.Query(`SELECT id FROM library WHERE id IN `+in+` ORDER BY id`, args...) //nolint:gosec // placeholders only
	got, err := db.Collect(rows, err, db.Column[uid.ID])
	if err != nil || !slices.Equal(got, []uid.ID{uid.N(1), uid.N(3)}) {
		t.Fatalf("%v %v", got, err)
	}
}

// The table classes of the v2 schema: U people create (audit timestamps and
// who), S the system keeps (audit timestamps), C keyed by other rows, B
// bookkeeping.
var (
	userTables = []string{"library", "library_folder", "household", "account", "join_code", "playlist",
		"playlist_entry", "collection", "station", "station_time", "sso_provider", "cover", "download",
		"profile_offer", "occasion", "friend_request", "smart_list"}
	systemTables = []string{"track", "chapter", "device", "playback_session", "task", "task_run",
		"throughput_sample", "log_entry", "genre", "person", "credit", "seek_preview", "device_link", "item",
		"watch_together", "edition", "media_file", "station_program", "account_identity", "conversion",
		"identity_review", "identity_evidence", "smart_group", "smart_condition"}
	keyedTables = []string{"account_allow", "session_plan_entry", "watch_state", "collection_movie",
		"edition_file", "station_day", "station_loop", "playlist_share", "watch_together_member",
		"item_metadata", "item_genre", "household_member", "adult_link", "playlist_household_share",
		"item_keyword", "occasion_keyword", "occasion_item", "occasion_exclusion", "occasion_phrase",
		"song_release", "favorite", "friendship", "friend_request_decline"}
	bookTables = []string{"server_info", "occasion_seed", "sync_clock",
		"sync_tombstone", "sync_action", "schema_migration"}
	syncedTables = []string{"account", "account_allow", "adult_link", "chapter", "collection",
		"collection_movie", "cover", "credit", "download", "edition", "edition_file", "favorite", "genre", "household",
		"household_member", "item", "item_genre", "item_keyword", "item_metadata", "library", "media_file",
		"occasion", "occasion_exclusion", "occasion_item", "occasion_keyword", "occasion_phrase", "person",
		"playlist", "playlist_entry", "playlist_household_share", "playlist_share", "profile_offer",
		"server_info", "station", "station_day", "station_loop", "station_program", "station_time", "track",
		"watch_state", "friendship", "friend_request", "friend_request_decline", "smart_list"}
	// Id columns without a foreign key, and BLOB columns that are not ids.
	unkeyedIDs = []string{"log_entry.session_id", "log_entry.item_id", "station_loop.entry_id",
		"station_loop.item_id", "conversion.audio_track_id", "conversion.burn_track_id"}
	notIDBlobs = []string{"cover.jpeg", "sync_tombstone.k1", "sync_tombstone.k2", "sync_tombstone.k3"}
	// Columns named like ids that hold something else.
	notIDs = map[string]string{"genre.tmdb_id": "INTEGER", "person.tmdb_id": "INTEGER",
		"collection.tmdb_id": "INTEGER", "item_metadata.provider_id": "INTEGER",
		"item_keyword.keyword_id": "INTEGER", "occasion_keyword.keyword_id": "INTEGER",
		"sso_provider.client_id": "TEXT", "sync_action.uuid": "TEXT", "item_metadata.external_id": "TEXT",
		"identity_evidence.external_id": "TEXT", "song_release.release_id": "TEXT",
		"song_release.release_group_id": "TEXT", "server_info.id": "INTEGER",
		"sync_clock.id": "INTEGER"}
)

type column struct {
	name, typ     string
	notNull       bool
	pk            int
	dflt          sql.NullString
	parent, onDel string
}

func columns(t *testing.T, d *sql.DB, table string) []column {
	t.Helper()
	rows, err := d.Query(`SELECT name, type, "notnull", pk, dflt_value FROM pragma_table_info(?) ORDER BY cid`, table)
	cs, err := db.Collect(rows, err, func(s db.Scanner) (column, error) {
		var c column
		return c, s.Scan(&c.name, &c.typ, &c.notNull, &c.pk, &c.dflt)
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = d.Query(`SELECT "from", "table", "to", on_delete FROM pragma_foreign_key_list(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var from, parent, to, onDel string
		if err := rows.Scan(&from, &parent, &to, &onDel); err != nil {
			t.Fatal(err)
		}
		if to != "id" {
			t.Errorf("%s.%s references %s.%s, not an id", table, from, parent, to)
		}
		for i := range cs {
			if cs[i].name == from {
				cs[i].parent, cs[i].onDel = parent, onDel
			}
		}
	}
	return cs
}

func names(t *testing.T, d *sql.DB, typ string) []string {
	t.Helper()
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type = ? AND name NOT LIKE 'sqlite_%' ORDER BY name`, typ)
	ns, err := db.Collect(rows, err, db.Column[string])
	if err != nil {
		t.Fatal(err)
	}
	return ns
}

func sorted(ss ...[]string) []string {
	out := slices.Concat(ss...)
	slices.Sort(out)
	return out
}

// The baseline follows the v2 table rules: UUIDv7 BLOB keys, audit columns by
// class, no AUTOINCREMENT, ids BLOB everywhere they are held.
func TestBaselineFollowsTheTableRules(t *testing.T) {
	d := migrated(t)
	if got, want := names(t, d, "table"), sorted(userTables, systemTables, keyedTables, bookTables); !slices.Equal(got, want) {
		t.Fatalf("tables\n%v\nwant\n%v", got, want)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'sqlite_sequence'
		OR sql LIKE '%AUTOINCREMENT%' OR sql LIKE '%WITHOUT ROWID%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("AUTOINCREMENT or WITHOUT ROWID: %d %v", n, err)
	}
	ids := map[string]bool{} // tables keyed by a UUIDv7
	for _, tb := range slices.Concat(userTables, systemTables) {
		ids[tb] = true
	}
	for _, tb := range names(t, d, "table") {
		cs := columns(t, d, tb)
		has := map[string]column{}
		for _, c := range cs {
			has[c.name] = c
		}
		_, synced := has["sync_version"]
		if synced != slices.Contains(syncedTables, tb) {
			t.Errorf("%s: sync_version %v", tb, synced)
		}
		if ids[tb] {
			if c := cs[0]; c.name != "id" || c.typ != "BLOB" || !c.notNull || c.pk != 1 {
				t.Errorf("%s: first column %+v, want id BLOB NOT NULL PRIMARY KEY", tb, c)
			}
			for _, c := range cs[1:] {
				if c.pk != 0 {
					t.Errorf("%s: %s is in the primary key", tb, c.name)
				}
			}
			for _, a := range []string{"created_at", "modified_at"} {
				if c, ok := has[a]; !ok || c.typ != "INTEGER" || !c.notNull || c.dflt.Valid {
					t.Errorf("%s: %s %+v, want INTEGER NOT NULL without a default", tb, a, c)
				}
			}
		}
		user := slices.Contains(userTables, tb)
		for _, a := range []string{"created_by", "modified_by"} {
			c, ok := has[a]
			if ok != user {
				t.Errorf("%s: %s present %v", tb, a, ok)
			} else if ok && (c.typ != "BLOB" || c.notNull || c.parent != "account" || c.onDel != "SET NULL") {
				t.Errorf("%s: %s %+v, want a nullable BLOB to account ON DELETE SET NULL", tb, a, c)
			}
		}
		if _, ok := has["modified_at"]; !ids[tb] && ok && tb != "server_info" {
			t.Errorf("%s: modified_at on a keyed table", tb)
		}
		for _, c := range cs {
			q := tb + "." + c.name
			switch {
			case c.name == "updated_at":
				t.Errorf("%s: updated_at", q)
			case notIDs[q] != "":
				if c.typ != notIDs[q] {
					t.Errorf("%s: %s, want %s", q, c.typ, notIDs[q])
				}
			case c.parent != "":
				if !ids[c.parent] {
					t.Errorf("%s references %s, which has no UUID key", q, c.parent)
				}
				if c.typ != "BLOB" {
					t.Errorf("%s: %s, want BLOB", q, c.typ)
				}
			case c.name == "id" && ids[tb], slices.Contains(unkeyedIDs, q), slices.Contains(notIDBlobs, q):
				if c.typ != "BLOB" {
					t.Errorf("%s: %s, want BLOB", q, c.typ)
				}
			case c.typ == "BLOB":
				t.Errorf("%s: BLOB but neither an id nor listed", q)
			case strings.HasSuffix(c.name, "_id") || c.name == "id":
				t.Errorf("%s: %s, looks like an id that is not one", q, c.typ)
			}
		}
	}
	for _, q := range []string{"conversion.audio_track_id", "conversion.burn_track_id"} {
		tb, col, _ := strings.Cut(q, ".")
		for _, c := range columns(t, d, tb) {
			if c.name == col && (!c.notNull || c.dflt.String != "x''") {
				t.Errorf("%s: %+v, want NOT NULL DEFAULT x''", q, c)
			}
		}
	}
	for tb, col := range map[string]string{"watch_state": "changed_at", "media_file": "file_modified_at", "conversion": "source_modified_at"} {
		if err := d.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, tb, col).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s.%s missing", tb, col)
		}
	}
	idx := names(t, d, "index")
	for _, tb := range syncedTables {
		if !slices.Contains(idx, tb+"_sync") {
			t.Errorf("index %s_sync missing", tb)
		}
	}
	if got := names(t, d, "view"); !slices.Equal(got, []string{"credit_shown", "item_file", "item_genre_shown",
		"item_keyword_shown", "item_shown", "item_sort", "membership", "metadata_setting", "playlist_reach"}) {
		t.Errorf("views %v", got)
	}
	// Sync triggers are installed by internal/replica, never by a migration.
	if got := names(t, d, "trigger"); !slices.Equal(got, []string{"collection_tmdb_empty", "edition_file_orphan",
		"item_sort_insert", "item_sort_metadata_delete", "item_sort_metadata_insert", "item_sort_metadata_update",
		"item_sort_setting_update", "item_sort_update", "smart_credit_delete", "smart_credit_insert", "smart_credit_update",
		"smart_edition_delete", "smart_edition_file_delete", "smart_edition_file_insert", "smart_edition_insert",
		"smart_edition_update", "smart_favorite_delete", "smart_favorite_insert", "smart_genre_update", "smart_item_delete",
		"smart_item_genre_delete", "smart_item_genre_insert", "smart_item_genre_update", "smart_item_insert", "smart_item_update",
		"smart_media_file_update", "smart_metadata_delete", "smart_metadata_insert", "smart_metadata_update",
		"smart_person_update", "smart_playlist_owner", "smart_server_info_update", "smart_track_delete", "smart_track_insert",
		"smart_track_update", "smart_watch_delete", "smart_watch_insert", "smart_watch_update"}) {
		t.Errorf("triggers %v", got)
	}
}

func TestBaselineSeedsItsRows(t *testing.T) {
	d := migrated(t)
	var clock string
	if err := d.QueryRow(`SELECT id || version || scope_version || purged_version || '[' || release || ']' FROM sync_clock`).Scan(&clock); err != nil {
		t.Fatal(err)
	}
	if clock != "1000[]" {
		t.Fatalf("sync_clock %s", clock)
	}
	rows, err := d.Query(`SELECT o.id, o.name || ':' || o.holiday || ':' || o.days_before || ':' || o.days_after || ':' ||
			o.created_at || ':' || o.modified_at || ':' || COALESCE(o.created_by, o.modified_by, '-') || ':' ||
			(SELECT group_concat(keyword_id) FROM occasion_keyword k WHERE k.occasion_id = o.id) || ':' ||
			(SELECT COUNT(*) FROM occasion_seed s WHERE s.occasion_id = o.id)
		FROM occasion o ORDER BY o.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var id uid.ID
		var s string
		if err := rows.Scan(&id, &s); err != nil {
			t.Fatal(err)
		}
		got = append(got, id.String()+" "+s)
	}
	want := []string{
		"00000000-0000-7000-9000-000000000001 Christmas:christmas:30:5:0:0:-:207317:1",
		"00000000-0000-7000-9000-000000000002 Halloween:halloween:30:1:0:0:-:3335:1",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("occasions\n%q\nwant\n%q", got, want)
	}
	for _, s := range want {
		id, err := uid.Parse(s[:36])
		if err != nil {
			t.Fatal(err)
		}
		if id.Compare(uid.N(1<<48-1)) <= 0 || id.Compare(uid.New()) >= 0 {
			t.Errorf("%s collides with the ids tests or the server mint", id)
		}
	}
}

// copies lists the database copies beside path, oldest schema first.
func copies(t *testing.T, path string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(filepath.Dir(path), "backups", "*.db"))
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		names[i] = filepath.Base(n)
	}
	return names
}

// needs gives the runner, for the test, a set of that many migrations, each
// adding a table.
func needs(t *testing.T, versions int) {
	t.Helper()
	set := fstest.MapFS{}
	for v := 1; v <= versions; v++ {
		set[fmt.Sprintf("migrations/%04d_step.sql", v)] = &fstest.MapFile{Data: fmt.Appendf(nil, "CREATE TABLE step%d (x INTEGER);", v)}
	}
	db.UseMigrations(t, set)
}

// A server refuses a database a newer one has migrated, so the copy from
// before a migration is the only way back to the older server.
func TestMigrateCopiesTheDatabaseBeforeChangingIt(t *testing.T) {
	needs(t, 2)
	path := filepath.Join(t.TempDir(), "rabbit-ears.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	latest := db.LatestVersion()
	if err := db.MigrateTo(d, latest-1); err != nil {
		t.Fatal(err)
	}
	if got := copies(t, path); len(got) != 0 {
		t.Fatalf("an empty database has nothing to copy, got %v", got)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("rabbit-ears-v%04d.db", latest-1)
	if got := copies(t, path); len(got) != 1 || got[0] != want {
		t.Fatalf("copies = %v, want [%s]", got, want)
	}
	c, err := db.Open(filepath.Join(filepath.Dir(path), "backups", want))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if v, err := db.Version(c); err != nil || v != latest-1 {
		t.Fatalf("the copy is at schema version %d (%v), want %d", v, err, latest-1)
	}
	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	if got := copies(t, path); len(got) != 1 {
		t.Fatalf("a database already up to date was copied again: %v", got)
	}
}

func TestMigrateKeepsTheNewestFiveCopies(t *testing.T) {
	needs(t, 8)
	path := filepath.Join(t.TempDir(), "rabbit-ears.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	latest := db.LatestVersion()
	for v := latest - 7; v <= latest; v++ {
		if err := db.MigrateTo(d, v); err != nil {
			t.Fatal(err)
		}
	}
	var want []string
	for v := latest - 5; v < latest; v++ {
		want = append(want, fmt.Sprintf("rabbit-ears-v%04d.db", v))
	}
	if got := copies(t, path); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("copies = %v, want %v", got, want)
	}
}

func TestMigrateRefusesWithoutACopy(t *testing.T) {
	needs(t, 2)
	path := filepath.Join(t.TempDir(), "rabbit-ears.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	latest := db.LatestVersion()
	if err := db.MigrateTo(d, latest-1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "backups"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(d); err == nil {
		t.Fatal("migrated a database it could not copy first")
	}
	if v, err := db.Version(d); err != nil || v != latest-1 {
		t.Fatalf("schema version %d (%v), want %d untouched", v, err, latest-1)
	}
}

// itemRows is every item row as text, each column but those left out.
func itemRows(t *testing.T, d *sql.DB, leave ...string) []string {
	t.Helper()
	rows, err := d.Query(`SELECT name FROM pragma_table_info('item') ORDER BY cid`)
	cols, err := db.Collect(rows, err, db.Column[string])
	if err != nil {
		t.Fatal(err)
	}
	cols = slices.DeleteFunc(cols, func(c string) bool { return slices.Contains(leave, c) })
	q := `SELECT quote(` + strings.Join(cols, `) || ',' || quote(`) + `) FROM item ORDER BY id` //nolint:gosec // column names
	rows, err = d.Query(q)
	got, err := db.Collect(rows, err, db.Column[string])
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Migrating a database whose libraries were scanned before sort titles
// files every movie and show by the title it shows and changes no other
// column or row; its statements run again change nothing.
func TestMigrationFillsSortTitlesOfMoviesAndShows(t *testing.T) {
	d := open(t)
	if err := db.MigrateTo(d, 1); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO server_info (id, name, metadata_source, metadata_mode, created_at, modified_at) VALUES (1, 'S', 'local', 'preferred', 0, 0)`)
	for i, kind := range []string{"movies", "tv", "music"} {
		exec(`INSERT INTO library (id, name, kind, created_at, modified_at) VALUES (?, ?, ?, 0, 0)`, uid.N(i+1), kind, kind)
	}
	add := func(id, lib int, parent *uid.ID, kind, parsed string, sort any) uid.ID {
		exec(`INSERT INTO item (id, library_id, parent_id, kind, parsed_title, sort_title, added_at, created_at, modified_at)
			VALUES (?, ?, ?, ?, ?, ?, 5, 6, 7)`, uid.N(id), uid.N(lib), parent, kind, parsed, sort)
		return uid.N(id)
	}
	meta := func(id uid.ID, source, title string) {
		exec(`INSERT INTO item_metadata (item_id, source, provider, status, title) VALUES (?, ?, 'tmdb', 'matched', ?)`, id, source, title)
	}
	meta(add(10, 1, nil, "movie", "Matrix", nil), "remote", "The Matrix")
	quiet := add(11, 1, nil, "movie", "quiet place", nil)
	meta(quiet, "remote", "Quiet")
	meta(quiet, "local", "A Quiet Place")
	add(12, 1, nil, "movie", "Die Hard", nil)
	add(13, 1, nil, "movie", "An Education", "zzz")
	show := add(20, 2, nil, "show", "The Office", nil)
	season := add(21, 2, &show, "season", "The Season", nil)
	add(22, 2, &season, "episode", "A Pilot", nil)
	add(30, 3, nil, "artist", "The Beatles", "Beatles")
	add(31, 3, nil, "artist", "A Tribe Called Quest", nil)
	before := itemRows(t, d, "sort_title")

	if err := db.Migrate(d); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Query(`SELECT parsed_title || '=' || COALESCE(sort_title, '-') FROM item ORDER BY id`)
	got, err := db.Collect(rows, err, db.Column[string])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Matrix=Matrix", "quiet place=Quiet Place", "Die Hard=Die Hard", "An Education=Education", "The Office=Office",
		"The Season=-", "A Pilot=-", "The Beatles=Beatles", "A Tribe Called Quest=-"}
	if !slices.Equal(got, want) {
		t.Errorf("sort titles\n%q\nwant\n%q", got, want)
	}
	if after := itemRows(t, d, "sort_title"); !slices.Equal(after, before) {
		t.Errorf("other columns changed\n%q\nwas\n%q", after, before)
	}
	migrated := itemRows(t, d)
	body, err := os.ReadFile(filepath.Join("migrations", "0002_sort_title.sql"))
	if err != nil {
		t.Fatal(err)
	}
	exec(string(body))
	if again := itemRows(t, d); !slices.Equal(again, migrated) {
		t.Errorf("run again\n%q\nwas\n%q", again, migrated)
	}
}
