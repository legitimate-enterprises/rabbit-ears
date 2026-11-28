package replica_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// elsewhere adds household 202 beside fixture's 201, with something of its
// own in every table a household's scope limits, a server station 201 sees
// playing a collection of 202's, and a device in each. 201's stations are
// seen apart by its kids and its adult hiding kids' content.
const elsewhere = `
INSERT INTO account (id, kind, email, name, password_hash, created_at, modified_at) VALUES (uid(111), 'adult', 'zed@example.test', 'Zed', 'zed-hash', 0, 0);
INSERT INTO household (id, name, adult_id, created_at, modified_at) VALUES (uid(202), 'Away', uid(111), 0, 0);
UPDATE account SET household_id = uid(202) WHERE id = uid(111);
INSERT INTO account (id, kind, name, household_id, pin_hash, created_at, modified_at) VALUES (uid(112), 'kid', 'Yan', uid(202), 'yan-pin', 0, 0);
UPDATE account SET email = 'ada@example.test', password_hash = 'ada-hash' WHERE id = uid(101);
INSERT INTO genre (id, name, created_at, modified_at) VALUES (uid(1), 'Drama', 0, 0);
INSERT INTO item_genre (item_id, genre_id, source) VALUES (uid(1), uid(1), 'local');
INSERT INTO person (id, name, created_at, modified_at) VALUES (uid(1), 'Pat', 0, 0);
INSERT INTO credit (id, item_id, person_id, kind, character, position, created_at, modified_at) VALUES (uid(1), uid(1), uid(1), 'cast', 'Lead', 0, 0, 0);
INSERT INTO playlist_share (playlist_id, account_id, access, created_at) VALUES (uid(501), uid(104), 'read', 0);
INSERT INTO watch_state (account_id, item_id, position_ms, changed_at) VALUES (uid(112), uid(1), 5, 0), (uid(103), uid(2), 7, 0);
INSERT INTO account_allow (account_id, item_id) VALUES (uid(112), uid(3));
INSERT INTO collection (id, source, household_id, name, created_at, modified_at) VALUES (uid(402), 'custom', uid(202), 'Theirs', 0, 0);
INSERT INTO collection_movie (collection_id, item_id, position) VALUES (uid(402), uid(1), 0);
INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES (uid(511), uid(111), 'Private', 'movies', 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at, created_at, modified_at) VALUES (uid(621), uid(511), uid(1), 'a0', 0, 0, 0);
INSERT INTO station (id, account_id, playlist_id, name, visibility, created_at, modified_at) VALUES
  (uid(701), uid(111), uid(511), 'Theirs', 'private', 0, 0), (uid(702), uid(101), uid(501), 'Ours', 'private', 0, 0);
INSERT INTO station_day (station_id, weekday) VALUES (uid(701), 1), (uid(702), 1);
INSERT INTO playlist (id, account_id, name, kind, station_owned, created_at, modified_at) VALUES
  (uid(512), uid(111), 'Wide', 'movies', 1, 0, 0), (uid(513), uid(102), 'Small', 'movies', 0, 0, 0), (uid(514), uid(101), 'Blank', 'movies', 1, 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at, created_at, modified_at) VALUES
  (uid(622), uid(512), uid(1), 'a0', 0, 0, 0), (uid(623), uid(512), uid(2), 'a1', 0, 0, 0), (uid(624), uid(513), uid(7), 'a0', 0, 0, 0);
INSERT INTO station (id, account_id, playlist_id, name, visibility, created_at, modified_at) VALUES
  (uid(703), uid(111), uid(512), 'Wide', 'server', 0, 0), (uid(704), uid(102), uid(513), 'Small', 'household', 0, 0),
  (uid(705), uid(111), uid(511), 'Near', 'household', 0, 0), (uid(706), uid(101), uid(514), 'Blank', 'server', 0, 0);
INSERT INTO station_day (station_id, weekday) VALUES (uid(703), 2);
INSERT INTO station_time (id, station_id, from_minute, to_minute, created_at, modified_at) VALUES (uid(721), uid(703), 60, 120, 0, 0);
INSERT INTO station_program (id, station_id, item_id, edition_id, entry_id, loop, position, starts_at, ends_at, created_at, modified_at) VALUES
  (uid(731), uid(703), uid(1), uid(9), uid(622), 0, 0, 0, 1000, 0, 0);
INSERT INTO station_loop (station_id, position, entry_id, item_id) VALUES (uid(703), 0, uid(622), uid(1));
INSERT INTO cover (id, station_id, item_id, created_at, modified_at) VALUES (uid(741), uid(703), uid(1), 0, 0);
INSERT INTO collection (id, source, household_id, name, created_at, modified_at) VALUES (uid(403), 'custom', uid(202), 'Lent', 0, 0);
INSERT INTO collection_movie (collection_id, item_id, position) VALUES (uid(403), uid(1), 0), (uid(403), uid(4), 1);
INSERT INTO playlist (id, account_id, name, kind, created_at, modified_at) VALUES (uid(515), uid(111), 'Lent', 'movies', 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, collection_id, rank, added_at, created_at, modified_at) VALUES
  (uid(626), uid(515), NULL, uid(403), 'a0', 0, 0, 0), (uid(627), uid(512), NULL, uid(403), 'a2', 0, 0, 0);
INSERT INTO device (id, token_hash, name, platform, account_id, household_id, created_at, modified_at) VALUES
  (uid(1), 'tok-here', 'Here', 'ios', uid(101), uid(201), 0, 0), (uid(2), 'tok-there', 'There', 'ios', uid(111), uid(202), 0, 0);
INSERT INTO download (id, device_id, account_id, item_id, media_file_id, quality, created_at, modified_at) VALUES
  (uid(801), uid(1), uid(103), uid(1), uid(1), 'original', 0, 0), (uid(802), uid(2), uid(112), uid(1), uid(1), 'original', 0, 0);
`

// syncOf is d's replica service, its snapshots kept for the test.
func syncOf(t *testing.T, d *sql.DB) *replica.Sync {
	t.Helper()
	s := replica.New(d)
	s.Dir = t.TempDir()
	return s
}

// snapshotOf builds household's snapshot and opens it.
func snapshotOf(t *testing.T, s *replica.Sync, household uid.ID) (*sql.DB, replica.File) {
	t.Helper()
	f, err := s.Snapshot(context.Background(), household)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	r, err := sql.Open("sqlite", "file:"+f.Path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, f
}

func twoHouseholds(t *testing.T) *sql.DB {
	t.Helper()
	d := dbtest.OpenPreferLocal(t)
	mustExec(t, d, fixture)
	mustExec(t, d, washed)
	mustExec(t, d, elsewhere)
	return d
}

func count(t *testing.T, d *sql.DB, q string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestSnapshotHoldsTheHouseholdAndNoOther(t *testing.T) {
	d := twoHouseholds(t)
	r, f := snapshotOf(t, syncOf(t, d), uid.N(201))
	v, _ := clock(t, d)
	if f.Version != v {
		t.Errorf("snapshot at %d, clock %d", f.Version, v)
	}
	if got := rowsOf(t, r, `SELECT schema, version, household_id FROM replica_meta`); !slices.Equal(got, []string{row(1, v, uid.N(201))}) {
		t.Errorf("replica_meta %v", got)
	}
	for q, want := range map[string]int64{
		`SELECT count(*) FROM account WHERE id IN (uid(111), uid(112))`:        0,
		`SELECT count(*) FROM account_card WHERE id IN (uid(111), uid(112))`:   0,
		`SELECT count(*) FROM household WHERE id = uid(202)`:                   0,
		`SELECT count(*) FROM watch_state WHERE account_id = uid(112)`:         0,
		`SELECT count(*) FROM account_allow WHERE account_id = uid(112)`:       0,
		`SELECT count(*) FROM collection WHERE id = uid(402)`:                  0,
		`SELECT count(*) FROM collection_movie WHERE collection_id = uid(402)`: 0,
		`SELECT count(*) FROM playlist WHERE id = uid(511)`:                    0,
		`SELECT count(*) FROM playlist_entry WHERE id = uid(621)`:              0,
		`SELECT count(*) FROM station WHERE id = uid(701)`:                     0,
		`SELECT count(*) FROM station_day WHERE station_id = uid(701)`:         0,
		`SELECT count(*) FROM download WHERE device_id = uid(2)`:               0,
		`SELECT count(*) FROM station WHERE id = uid(705)`:                     0,
		`SELECT count(*) FROM playlist_share WHERE playlist_id = uid(512)`:     0,
		// Another household's server station brings what shows and plays
		// it, and nothing more of its owner.
		`SELECT count(*) FROM station WHERE id = uid(703) AND household_id = uid(202)`: 1,
		`SELECT count(*) FROM playlist WHERE id = uid(512)`:                            1,
		`SELECT count(*) FROM playlist_entry WHERE playlist_id = uid(512)`:             3,
		`SELECT count(*) FROM station_day WHERE station_id = uid(703)`:                 1,
		`SELECT count(*) FROM station_time WHERE station_id = uid(703)`:                1,
		`SELECT count(*) FROM station_program WHERE station_id = uid(703)`:             1,
		`SELECT count(*) FROM station_loop WHERE station_id = uid(703)`:                1,
		`SELECT count(*) FROM cover WHERE station_id = uid(703)`:                       1,
		`SELECT count(*) FROM collection WHERE id = uid(403)`:                          1,
		`SELECT count(*) FROM collection_movie WHERE collection_id = uid(403)`:         2,
		`SELECT count(*) FROM playlist WHERE id = uid(515)`:                            0,
		`SELECT count(*) FROM station WHERE id IN (uid(702), uid(704), uid(706))`:      3,
		`SELECT count(*) FROM account`:                                                 7,
		`SELECT count(*) FROM household WHERE id = uid(201)`:                           1,
		`SELECT count(*) FROM watch_state WHERE account_id = uid(103)`:                 1,
		`SELECT count(*) FROM collection WHERE id = uid(401)`:                          1,
		`SELECT count(*) FROM playlist WHERE id IN (uid(501), uid(502))`:               2,
		`SELECT count(*) FROM station_day WHERE station_id = uid(702)`:                 1,
		`SELECT count(*) FROM download WHERE device_id = uid(1)`:                       1,
		`SELECT count(*) FROM item`:                                                    count(t, d, `SELECT count(*) FROM item`),
		`SELECT count(*) FROM account WHERE has_pin`:                                   2,
		`SELECT count(*) FROM genre WHERE name = 'DRAMA'`:                              1,
		`SELECT count(*) FROM occasion WHERE name = 'snow'`:                            1,
		`SELECT count(*) FROM occasion WHERE name = 'Snow' AND wash_strength = 300 AND wash_brightness = 30 AND wash_sharpness = 20
			AND wash_reach = 140`: 1,
		`SELECT count(*) FROM occasion WHERE name = 'Sun' AND wash_strength = 200 AND wash_brightness = 15 AND wash_sharpness = 0
			AND wash_reach = 100`: 1,
		`SELECT count(*) FROM item_genre WHERE source = 'local'`:     1,
		`SELECT count(*) FROM holiday`:                               int64(len(occasion.Holidays())),
		`SELECT count(*) FROM holiday_date WHERE key = 'christmas'`:  int64(len(holiday(t, "christmas").Dates)),
		`SELECT count(*) FROM holiday_color WHERE key = 'christmas'`: int64(len(holiday(t, "christmas").Colors)),
		`SELECT count(*) FROM holiday_date WHERE day = '2030-12-25'`: 1,
	} {
		if got := count(t, r, q); got != want {
			t.Errorf("%s = %d, want %d", q, got, want)
		}
	}
}

func holiday(t *testing.T, key string) occasion.Holiday {
	t.Helper()
	h, ok := occasion.HolidayByKey(key)
	if !ok {
		t.Fatalf("no holiday %s", key)
	}
	return h
}

// Nothing secret, no path on the server and no image's bytes reach a
// snapshot: by column name across every table and view, and by value.
func TestSnapshotCarriesNoSecret(t *testing.T) {
	d := twoHouseholds(t)
	r, _ := snapshotOf(t, syncOf(t, d), uid.N(201))
	never := []string{"password_hash", "pin_hash", "pin_failures", "pin_locked_until", "token_hash", "poll_token_hash", "tmdb_key",
		"client_secret", "secret", "code", "path", "thumbnail_path", "dir", "jpeg", "probe_error", "fingerprint", "sync_version"}
	secrets := strs(t, d, `SELECT path FROM item WHERE path IS NOT NULL UNION SELECT path FROM media_file
		UNION SELECT password_hash FROM account WHERE password_hash IS NOT NULL UNION SELECT pin_hash FROM account WHERE pin_hash IS NOT NULL
		UNION SELECT token_hash FROM device`)
	if len(secrets) < 10 {
		t.Fatalf("the fixture has only %v to hide", secrets)
	}
	for _, name := range strs(t, r, `SELECT name FROM sqlite_schema WHERE type IN ('table', 'view')`) {
		cols := strs(t, r, `SELECT name FROM pragma_table_info(?)`, name)
		for _, c := range cols {
			if slices.Contains(never, c) {
				t.Errorf("%s.%s is in the snapshot", name, c)
			}
		}
		for _, v := range inOrder(t, r, `SELECT * FROM "`+name+`"`) {
			for _, part := range strings.Split(v, "|") {
				if slices.Contains(secrets, part) {
					t.Errorf("%s holds %q", name, part)
				}
			}
		}
	}
}

// A replica keeps no foreign key, CHECK nor UNIQUE constraint: diffs apply
// row by row in any order. It keeps the server's indexes on what it holds.
func TestSnapshotSchema(t *testing.T) {
	d := twoHouseholds(t)
	r, _ := snapshotOf(t, syncOf(t, d), uid.N(201))
	for _, tb := range replica.V1.Tables {
		q := strs(t, r, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, tb.Name)
		if len(q) != 1 {
			t.Errorf("no table %s", tb.Name)
			continue
		}
		for _, word := range []string{"REFERENCES", "CHECK", "UNIQUE"} {
			if strings.Contains(strings.ToUpper(q[0]), word) {
				t.Errorf("%s has %s: %s", tb.Name, word, q[0])
			}
		}
		var cols []string
		for _, c := range tb.Cols {
			cols = append(cols, c.Name)
		}
		if got := strs(t, r, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, tb.Name); !slices.Equal(got, cols) {
			t.Errorf("%s columns %v, want %v", tb.Name, got, cols)
		}
		if got := strs(t, r, `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, tb.Name); !slices.Equal(got, tb.Key) {
			t.Errorf("%s key %v, want %v", tb.Name, got, tb.Key)
		}
	}
	if got := strs(t, r, `SELECT name FROM pragma_index_list('playlist_entry') WHERE origin = 'c'`); !slices.Contains(got, "playlist_entry_order") {
		t.Errorf("playlist_entry indexes %v", got)
	}
	for _, name := range []string{"item_kind_title", "item_kind_sort", "person_name", "edition_tag"} {
		if count(t, r, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = ?`, name) != 1 {
			t.Errorf("no index %s", name)
		}
	}
	if got := strs(t, r, `SELECT l.name FROM sqlite_schema s, pragma_index_list(s.name) l WHERE s.type = 'table' AND l."unique" AND l.origin != 'pk'`); len(got) > 0 {
		t.Errorf("unique indexes %v", got)
	}
	if got := strs(t, r, `PRAGMA journal_mode`); !slices.Equal(got, []string{"delete"}) {
		t.Errorf("journal mode %v", got)
	}
}

// Every view a snapshot ships compiles there and answers rows.
func TestSnapshotViewsAnswer(t *testing.T) {
	d := twoHouseholds(t)
	r, _ := snapshotOf(t, syncOf(t, d), uid.N(201))
	views := strs(t, r, `SELECT name FROM sqlite_schema WHERE type = 'view'`)
	if len(views) < 15 {
		t.Fatalf("views %v", views)
	}
	for _, v := range views {
		if count(t, r, `SELECT count(*) FROM "`+v+`"`) == 0 {
			t.Errorf("view %s answers nothing", v)
		}
	}
}

// A snapshot is built once for a household while nothing it holds
// changes, whoever asks and however many at once; a change it holds makes
// the next ask build anew and drops the older file.
func TestSnapshotIsCachedUntilTheHouseholdChanges(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	ctx := context.Background()
	files := make([]replica.File, 4)
	var wg sync.WaitGroup
	for i := range files {
		wg.Go(func() {
			f, err := s.Snapshot(ctx, uid.N(201))
			if err != nil {
				t.Error(err)
				return
			}
			_ = f.Close()
			files[i] = f
		})
	}
	wg.Wait()
	for _, f := range files[1:] {
		if f.Path != files[0].Path {
			t.Errorf("built %s and %s", files[0].Path, f.Path)
		}
	}
	first, err := os.ReadFile(files[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `UPDATE watch_state SET position_ms = 99 WHERE account_id = uid(112)`)
	again, err := s.Snapshot(ctx, uid.N(201))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(again.Path)
	_ = again.Close()
	if err != nil || again.Path != files[0].Path || !bytes.Equal(b, first) {
		t.Errorf("another household's write made %s (%v)", again.Path, err)
	}
	mustExec(t, d, `UPDATE watch_state SET position_ms = 99 WHERE account_id = uid(103)`)
	fresh, err := s.Snapshot(ctx, uid.N(201))
	if err != nil {
		t.Fatal(err)
	}
	_ = fresh.Close()
	if fresh.Path == files[0].Path || fresh.Version <= files[0].Version {
		t.Errorf("a change the household sees kept %s at %d", fresh.Path, fresh.Version)
	}
	if left, _ := filepath.Glob(filepath.Join(s.Dir, "*")); !slices.Equal(left, []string{fresh.Path}) {
		t.Errorf("cache holds %v", left)
	}
}

// Every id of a snapshot, and nothing else, is declared BLOB and holds an
// id's 16 bytes, so a client reads a declared BLOB as a UUID.
func TestSnapshotIdsAreBlobs(t *testing.T) {
	d := twoHouseholds(t)
	r, _ := snapshotOf(t, syncOf(t, d), uid.N(201))
	notIDs := []string{"tmdb_id", "provider_id", "keyword_id", "external_id"}
	for _, tb := range replica.V1.Tables {
		for _, c := range tb.Cols {
			// server_info's id is the constant 1, not an id.
			id := c.Name == "id" && tb.Name != "server_info" || c.Name == "created_by" || c.Name == "modified_by" ||
				strings.HasSuffix(c.Name, "_id") && !slices.Contains(notIDs, c.Name)
			if typ := strs(t, r, `SELECT type FROM pragma_table_info(?) WHERE name = ?`, tb.Name, c.Name); (typ[0] == "BLOB") != id {
				t.Errorf("%s.%s declared %s", tb.Name, c.Name, typ[0])
			}
			if n := count(t, r, `SELECT count(*) FROM "`+tb.Name+`" WHERE `+c.Name+` IS NOT NULL AND
				(typeof(`+c.Name+`) = 'blob' AND length(`+c.Name+`) = 16) != ?`, id); n > 0 {
				t.Errorf("%s.%s: %d values not as declared", tb.Name, c.Name, n)
			}
		}
	}
	if count(t, r, `SELECT count(*) FROM station WHERE typeof(household_id) = 'blob'`) == 0 {
		t.Error("no station carries its household's id")
	}
}
