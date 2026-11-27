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
)

// elsewhere adds household 202 beside fixture's 201, with something of its
// own in every table a household's scope limits, a server station 201 sees
// playing a collection of 202's, and a device in each. 201's stations are
// seen apart by its kids and its adult hiding kids' content.
const elsewhere = `
INSERT INTO account (id, kind, email, name, password_hash, created_at, updated_at) VALUES (111, 'adult', 'zed@example.test', 'Zed', 'zed-hash', 0, 0);
INSERT INTO household (id, name, adult_id, created_at) VALUES (202, 'Away', 111, 0);
UPDATE account SET household_id = 202 WHERE id = 111;
INSERT INTO account (id, kind, name, household_id, pin_hash, created_at, updated_at) VALUES (112, 'kid', 'Yan', 202, 'yan-pin', 0, 0);
UPDATE account SET email = 'ada@example.test', password_hash = 'ada-hash' WHERE id = 101;
INSERT INTO genre (id, name) VALUES (1, 'Drama');
INSERT INTO item_genre (item_id, genre_id, source) VALUES (1, 1, 'local');
INSERT INTO person (id, name) VALUES (1, 'Pat');
INSERT INTO credit (id, item_id, person_id, kind, character, position) VALUES (1, 1, 1, 'cast', 'Lead', 0);
INSERT INTO playlist_share (playlist_id, account_id, access, created_at) VALUES (501, 104, 'read', 0);
INSERT INTO watch_state (account_id, item_id, position_ms, updated_at) VALUES (112, 1, 5, 0), (103, 2, 7, 0);
INSERT INTO account_allow (account_id, item_id) VALUES (112, 3);
INSERT INTO collection (id, source, household_id, name, created_at, updated_at) VALUES (402, 'custom', 202, 'Theirs', 0, 0);
INSERT INTO collection_movie (collection_id, item_id, position) VALUES (402, 1, 0);
INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (511, 111, 'Private', 'movies', 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at) VALUES (621, 511, 1, 'a0', 0);
INSERT INTO station (id, account_id, playlist_id, name, visibility, created_at, updated_at) VALUES
  (701, 111, 511, 'Theirs', 'private', 0, 0), (702, 101, 501, 'Ours', 'private', 0, 0);
INSERT INTO station_day (station_id, weekday) VALUES (701, 1), (702, 1);
INSERT INTO playlist (id, account_id, name, kind, station_owned, created_at, updated_at) VALUES
  (512, 111, 'Wide', 'movies', 1, 0, 0), (513, 102, 'Small', 'movies', 0, 0, 0), (514, 101, 'Blank', 'movies', 1, 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at) VALUES (622, 512, 1, 'a0', 0), (623, 512, 2, 'a1', 0), (624, 513, 7, 'a0', 0);
INSERT INTO station (id, account_id, playlist_id, name, visibility, created_at, updated_at) VALUES
  (703, 111, 512, 'Wide', 'server', 0, 0), (704, 102, 513, 'Small', 'household', 0, 0), (705, 111, 511, 'Near', 'household', 0, 0),
  (706, 101, 514, 'Blank', 'server', 0, 0);
INSERT INTO station_day (station_id, weekday) VALUES (703, 2);
INSERT INTO station_time (id, station_id, from_minute, to_minute) VALUES (721, 703, 60, 120);
INSERT INTO station_program (id, station_id, item_id, edition_id, entry_id, loop, position, starts_at, ends_at) VALUES (731, 703, 1, 9, 622, 0, 0, 0, 1000);
INSERT INTO station_loop (station_id, position, entry_id, item_id) VALUES (703, 0, 622, 1);
INSERT INTO cover (id, station_id, item_id, created_at) VALUES (741, 703, 1, 0);
INSERT INTO collection (id, source, household_id, name, created_at, updated_at) VALUES (403, 'custom', 202, 'Lent', 0, 0);
INSERT INTO collection_movie (collection_id, item_id, position) VALUES (403, 1, 0), (403, 4, 1);
INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (515, 111, 'Lent', 'movies', 0, 0);
INSERT INTO playlist_entry (id, playlist_id, item_id, collection_id, rank, added_at) VALUES (626, 515, NULL, 403, 'a0', 0), (627, 512, NULL, 403, 'a2', 0);
INSERT INTO device (id, token_hash, name, platform, account_id, household_id, created_at) VALUES
  (1, 'tok-here', 'Here', 'ios', 101, 201, 0), (2, 'tok-there', 'There', 'ios', 111, 202, 0);
INSERT INTO download (id, device_id, account_id, item_id, media_file_id, quality, created_at) VALUES
  (801, 1, 103, 1, 1, 'original', 0), (802, 2, 112, 1, 1, 'original', 0);
`

// syncOf is d's replica service, its snapshots kept for the test.
func syncOf(t *testing.T, d *sql.DB) *replica.Sync {
	t.Helper()
	s := replica.New(d)
	s.Dir = t.TempDir()
	return s
}

// snapshotOf builds household's snapshot and opens it.
func snapshotOf(t *testing.T, s *replica.Sync, household int64) (*sql.DB, replica.File) {
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
	r, f := snapshotOf(t, syncOf(t, d), 201)
	v, _ := clock(t, d)
	if f.Version != v {
		t.Errorf("snapshot at %d, clock %d", f.Version, v)
	}
	if got := rowsOf(t, r, `SELECT schema, version, household_id FROM replica_meta`); !slices.Equal(got, []string{row(1, v, 201)}) {
		t.Errorf("replica_meta %v", got)
	}
	for q, want := range map[string]int64{
		`SELECT count(*) FROM account WHERE id IN (111, 112)`:             0,
		`SELECT count(*) FROM account_card WHERE id IN (111, 112)`:        0,
		`SELECT count(*) FROM household WHERE id = 202`:                   0,
		`SELECT count(*) FROM watch_state WHERE account_id = 112`:         0,
		`SELECT count(*) FROM account_allow WHERE account_id = 112`:       0,
		`SELECT count(*) FROM collection WHERE id = 402`:                  0,
		`SELECT count(*) FROM collection_movie WHERE collection_id = 402`: 0,
		`SELECT count(*) FROM playlist WHERE id = 511`:                    0,
		`SELECT count(*) FROM playlist_entry WHERE id = 621`:              0,
		`SELECT count(*) FROM station WHERE id = 701`:                     0,
		`SELECT count(*) FROM station_day WHERE station_id = 701`:         0,
		`SELECT count(*) FROM download WHERE device_id = 2`:               0,
		`SELECT count(*) FROM station WHERE id = 705`:                     0,
		`SELECT count(*) FROM playlist_share WHERE playlist_id = 512`:     0,
		// Another household's server station brings what shows and plays
		// it, and nothing more of its owner.
		`SELECT count(*) FROM station WHERE id = 703 AND household_id = 202`: 1,
		`SELECT count(*) FROM playlist WHERE id = 512`:                       1,
		`SELECT count(*) FROM playlist_entry WHERE playlist_id = 512`:        3,
		`SELECT count(*) FROM station_day WHERE station_id = 703`:            1,
		`SELECT count(*) FROM station_time WHERE station_id = 703`:           1,
		`SELECT count(*) FROM station_program WHERE station_id = 703`:        1,
		`SELECT count(*) FROM station_loop WHERE station_id = 703`:           1,
		`SELECT count(*) FROM cover WHERE station_id = 703`:                  1,
		`SELECT count(*) FROM collection WHERE id = 403`:                     1,
		`SELECT count(*) FROM collection_movie WHERE collection_id = 403`:    2,
		`SELECT count(*) FROM playlist WHERE id = 515`:                       0,
		`SELECT count(*) FROM station WHERE id IN (702, 704, 706)`:           3,
		`SELECT count(*) FROM account`:                                       7,
		`SELECT count(*) FROM household WHERE id = 201`:                      1,
		`SELECT count(*) FROM watch_state WHERE account_id = 103`:            1,
		`SELECT count(*) FROM collection WHERE id = 401`:                     1,
		`SELECT count(*) FROM playlist WHERE id IN (501, 502)`:               2,
		`SELECT count(*) FROM station_day WHERE station_id = 702`:            1,
		`SELECT count(*) FROM download WHERE device_id = 1`:                  1,
		`SELECT count(*) FROM item`:                                          count(t, d, `SELECT count(*) FROM item`),
		`SELECT count(*) FROM account WHERE has_pin`:                         2,
		`SELECT count(*) FROM genre WHERE name = 'DRAMA'`:                    1,
		`SELECT count(*) FROM occasion WHERE name = 'snow'`:                  1,
		`SELECT count(*) FROM item_genre WHERE source = 'local'`:             1,
		`SELECT count(*) FROM holiday`:                                       int64(len(occasion.Holidays())),
		`SELECT count(*) FROM holiday_date WHERE key = 'christmas'`:          int64(len(holiday(t, "christmas").Dates)),
		`SELECT count(*) FROM holiday_color WHERE key = 'christmas'`:         int64(len(holiday(t, "christmas").Colors)),
		`SELECT count(*) FROM holiday_date WHERE day = '2030-12-25'`:         1,
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
	r, _ := snapshotOf(t, syncOf(t, d), 201)
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
	r, _ := snapshotOf(t, syncOf(t, d), 201)
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
	r, _ := snapshotOf(t, syncOf(t, d), 201)
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
			f, err := s.Snapshot(ctx, 201)
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
	mustExec(t, d, `UPDATE watch_state SET position_ms = 99 WHERE account_id = 112`)
	again, err := s.Snapshot(ctx, 201)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(again.Path)
	_ = again.Close()
	if err != nil || again.Path != files[0].Path || !bytes.Equal(b, first) {
		t.Errorf("another household's write made %s (%v)", again.Path, err)
	}
	mustExec(t, d, `UPDATE watch_state SET position_ms = 99 WHERE account_id = 103`)
	fresh, err := s.Snapshot(ctx, 201)
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
