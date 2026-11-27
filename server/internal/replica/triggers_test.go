package replica_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
)

// seeded is a migrated database with household 1 of adult 1 and kid 2,
// device 1, and movies 1 and 2.
func seeded(t *testing.T) *sql.DB {
	t.Helper()
	d := dbtest.Open(t)
	for _, q := range []string{
		`INSERT INTO account (id, kind, email, name, password_hash, created_at, updated_at) VALUES (1, 'adult', 'a@example.test', 'A', 'x', 0, 0)`,
		`INSERT INTO household (id, name, adult_id, created_at) VALUES (1, 'H', 1, 0)`,
		`INSERT INTO household_member (household_id, account_id, created_at) VALUES (1, 1, 0)`,
		`INSERT INTO account (id, kind, name, household_id, created_at, updated_at) VALUES (2, 'kid', 'K', 1, 0, 0)`,
		`INSERT INTO device (id, token_hash, name, platform, account_id, household_id, created_at) VALUES (1, 't', 'D', 'ios', 1, 1, 0)`,
		`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1, 'L', 'movies', 0, 0)`,
		`INSERT INTO item (id, library_id, kind, parsed_title, path, added_at, updated_at) VALUES
			(1, 1, 'movie', 'M', '/m/1', 0, 0), (2, 1, 'movie', 'N', '/m/2', 0, 0)`,
	} {
		mustExec(t, d, q)
	}
	return d
}

func clock(t *testing.T, d db.DBTX) (version, scope int64) {
	t.Helper()
	if err := d.QueryRow(`SELECT version, scope_version FROM sync_clock`).Scan(&version, &scope); err != nil {
		t.Fatal(err)
	}
	return version, scope
}

func stamp(t *testing.T, d db.DBTX, q string, args ...any) int64 {
	t.Helper()
	var v int64
	if err := d.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return v
}

// tombstone is the version of the tombstone of tbl's key k, 0 for none.
func tombstone(t *testing.T, d db.DBTX, tbl string, k ...any) int64 {
	t.Helper()
	k = append(k, 0, 0)
	var v int64
	err := d.QueryRow(`SELECT version FROM sync_tombstone WHERE tbl = ? AND k1 = ? AND k2 = ? AND k3 = ?`, tbl, k[0], k[1], k[2]).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const watchStamp = `SELECT sync_version FROM watch_state WHERE account_id = ? AND item_id = ?`

func TestAutocommitWriteIsStamped(t *testing.T) {
	d := seeded(t)
	before, _ := clock(t, d)
	mustExec(t, d, `INSERT INTO watch_state (account_id, item_id, position_ms, updated_at) VALUES (2, 1, 5, 0)`)
	after, _ := clock(t, d)
	if got := stamp(t, d, watchStamp, 2, 1); got <= before || got != after {
		t.Errorf("stamped %d, clock %d then %d", got, before, after)
	}
}

// A reader that saw the clock at v must find every later change above v,
// whichever connection makes it.
func TestWriteOnAnotherConnectionIsAboveTheObservedClock(t *testing.T) {
	d := seeded(t)
	ctx := context.Background()
	c1, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c1.Close() }()
	c2, err := d.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.Close() }()
	if _, err := c1.ExecContext(ctx, `INSERT INTO watch_state (account_id, item_id, updated_at) VALUES (2, 1, 0)`); err != nil {
		t.Fatal(err)
	}
	var seen int64
	if err := c1.QueryRowContext(ctx, `SELECT version FROM sync_clock`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.ExecContext(ctx, `INSERT INTO watch_state (account_id, item_id, updated_at) VALUES (2, 2, 0)`); err != nil {
		t.Fatal(err)
	}
	if got := stamp(t, d, watchStamp, 2, 2); got <= seen {
		t.Errorf("stamped %d, at or below the clock %d seen before it", got, seen)
	}
}

func TestWriteAfterRollbackIsAboveTheObservedClock(t *testing.T) {
	d := seeded(t)
	seen, _ := clock(t, d)
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, tx, `INSERT INTO watch_state (account_id, item_id, updated_at) VALUES (2, 1, 0)`)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO watch_state (account_id, item_id, updated_at) VALUES (2, 2, 0)`)
	if got := stamp(t, d, watchStamp, 2, 2); got <= seen {
		t.Errorf("stamped %d, at or below %d", got, seen)
	}
}

func TestRewritingTheSameValuesMovesNothing(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO watch_state (account_id, item_id, position_ms, updated_at) VALUES (2, 1, 5, 0)`)
	before, _ := clock(t, d)
	mustExec(t, d, `UPDATE watch_state SET position_ms = 5, updated_at = 0 WHERE account_id = 2`)
	// A rescan writes every file-derived field and a new updated_at.
	items := item.New(d)
	it, err := items.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := items.Update(it); err != nil {
		t.Fatal(err)
	}
	if after, _ := clock(t, d); after != before {
		t.Errorf("clock moved from %d to %d", before, after)
	}
	mustExec(t, d, `UPDATE watch_state SET position_ms = 6 WHERE account_id = 2`)
	if after, _ := clock(t, d); after != before+1 {
		t.Errorf("one changed row moved the clock from %d to %d", before, after)
	}
}

func TestUnsyncedColumnsDoNotMoveTheClock(t *testing.T) {
	d := seeded(t)
	before, _ := clock(t, d)
	if err := device.New(d).Touch(1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := account.New(d).RecordPINFailure(1, 5, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := account.New(d).ClearPINFailures(1); err != nil {
		t.Fatal(err)
	}
	if after, _ := clock(t, d); after != before {
		t.Errorf("clock moved from %d to %d", before, after)
	}
}

// A NOCASE column compares case-blind; a rename that changes only case is
// still a change a client must see.
func TestCaseOnlyChangeMovesTheClock(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO genre (id, name) VALUES (1, 'Drama')`)
	before, _ := clock(t, d)
	mustExec(t, d, `UPDATE genre SET name = 'drama' WHERE id = 1`)
	if got := stamp(t, d, `SELECT sync_version FROM genre WHERE id = 1`); got <= before {
		t.Errorf("stamped %d, clock was %d", got, before)
	}
}

func TestCascadeDeleteTombstonesChildren(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO watch_state (account_id, item_id, updated_at) VALUES (2, 1, 0)`)
	mustExec(t, d, `INSERT INTO account_allow (account_id, item_id) VALUES (2, 1)`)
	before, _ := clock(t, d)
	mustExec(t, d, `DELETE FROM item WHERE id = 1`)
	for _, k := range []struct {
		tbl string
		key []any
	}{{"item", []any{1}}, {"watch_state", []any{2, 1}}, {"account_allow", []any{2, 1}}} {
		if got := tombstone(t, d, k.tbl, k.key...); got <= before {
			t.Errorf("%s %v tombstone at %d, clock was %d", k.tbl, k.key, got, before)
		}
	}
}

// Fold moves a title's rows to the title it merges into by rewriting their
// keys; the client must drop the rows under the old key.
func TestFoldTombstonesTheOldKey(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO watch_state (account_id, item_id, position_ms, updated_at) VALUES (2, 1, 9, 0)`)
	before, _ := clock(t, d)
	if err := edition.New(d).Fold(2, 1); err != nil {
		t.Fatal(err)
	}
	if got := tombstone(t, d, "watch_state", 2, 1); got <= before {
		t.Errorf("old key tombstone at %d, clock was %d", got, before)
	}
	if got := stamp(t, d, watchStamp, 2, 2); got <= before {
		t.Errorf("moved row stamped %d, clock was %d", got, before)
	}
	if got := tombstone(t, d, "watch_state", 2, 2); got != 0 {
		t.Errorf("new key tombstoned at %d", got)
	}
}

func TestReinsertClearsTheTombstone(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO account_allow (account_id, item_id) VALUES (2, 1)`)
	mustExec(t, d, `DELETE FROM account_allow`)
	if tombstone(t, d, "account_allow", 2, 1) == 0 {
		t.Fatal("no tombstone")
	}
	mustExec(t, d, `INSERT INTO account_allow (account_id, item_id) VALUES (2, 1)`)
	if got := tombstone(t, d, "account_allow", 2, 1); got != 0 {
		t.Errorf("tombstone at %d survived the insert", got)
	}
	mustExec(t, d, `DELETE FROM account_allow`)
	if tombstone(t, d, "account_allow", 2, 1) == 0 {
		t.Fatal("no tombstone after the second delete")
	}
}

func TestScopeDefiningChangesMoveScopeVersion(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO account (id, kind, email, name, password_hash, created_at, updated_at) VALUES (3, 'adult', 'b@example.test', 'B', 'x', 0, 0)`)
	mustExec(t, d, `INSERT INTO household (id, name, adult_id, created_at) VALUES (2, 'G', 3, 0)`)
	mustExec(t, d, `INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (1, 1, 'P', 'movies', 0, 0)`)
	mustExec(t, d, `INSERT INTO station (id, account_id, playlist_id, name, visibility, created_at, updated_at) VALUES (1, 1, 1, 'S', 'private', 0, 0)`)
	mustExec(t, d, `INSERT INTO collection (id, source, household_id, name, created_at, updated_at) VALUES (1, 'custom', 1, 'C', 0, 0)`)
	for _, c := range []struct {
		write         string
		synced, scope bool
	}{
		{`INSERT INTO watch_state (account_id, item_id, updated_at) VALUES (2, 1, 0)`, true, false},
		{`UPDATE account SET name = 'Kid' WHERE id = 2`, true, false},
		{`UPDATE station SET name = 'T' WHERE id = 1`, true, false},
		// A household's collection in a playlist no other household holds.
		{`INSERT INTO playlist_entry (id, playlist_id, collection_id, rank, added_at) VALUES (1, 1, 1, 'a0', 0)`, true, false},
		{`DELETE FROM playlist_entry WHERE id = 1`, true, false},
		{`INSERT INTO household_member (household_id, account_id, created_at) VALUES (2, 1, 0)`, true, true},
		{`DELETE FROM household_member WHERE household_id = 2`, true, true},
		{`INSERT INTO playlist_share (playlist_id, account_id, access, created_at) VALUES (1, 3, 'read', 0)`, true, true},
		{`UPDATE playlist_share SET access = 'write'`, true, false},
		{`INSERT INTO playlist_entry (id, playlist_id, collection_id, rank, added_at) VALUES (2, 1, 1, 'a0', 0)`, true, true},
		{`UPDATE playlist_entry SET rank = 'a1' WHERE id = 2`, true, false},
		{`INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at) VALUES (3, 1, 1, 'a2', 0)`, true, false},
		{`UPDATE playlist_entry SET collection_id = NULL, item_id = 2 WHERE id = 2`, true, true},
		{`UPDATE station SET visibility = 'server' WHERE id = 1`, true, true},
		{`UPDATE account SET household_id = 2 WHERE id = 2`, true, true},
		{`UPDATE device SET household_id = 2 WHERE id = 1`, true, true},
		{`UPDATE device SET name = 'E' WHERE id = 1`, false, false},
	} {
		v0, s0 := clock(t, d)
		mustExec(t, d, c.write)
		v1, s1 := clock(t, d)
		if moved := v1 != v0; moved != c.synced {
			t.Errorf("%s: clock %d to %d", c.write, v0, v1)
		}
		if moved := s1 != s0; moved != c.scope || moved && s1 != v1 {
			t.Errorf("%s: scope version %d to %d, clock %d", c.write, s0, s1, v1)
		}
	}
}

// The triggers written by hand delete synced rows from inside a trigger;
// those deletes leave tombstones, and the hand-written ones still run.
func TestHandWrittenTriggersStillBehave(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO collection (id, source, tmdb_id, name, created_at, updated_at) VALUES (1, 'tmdb', 10, 'C', 0, 0)`)
	mustExec(t, d, `INSERT INTO collection_movie (collection_id, item_id) VALUES (1, 1)`)
	mustExec(t, d, `INSERT INTO media_file (id, path, size_bytes, modified_at) VALUES (1, '/m/1/a.mp4', 1, 0)`)
	mustExec(t, d, `INSERT INTO edition (id, item_id) VALUES (1, 1)`)
	mustExec(t, d, `INSERT INTO edition_file (edition_id, media_file_id) VALUES (1, 1)`)
	art := stamp(t, d, `SELECT generation FROM artwork_generation`)
	mustExec(t, d, `INSERT INTO item_metadata (item_id, source, poster_path) VALUES (1, 'remote', '/p.jpg')`)
	if got := stamp(t, d, `SELECT generation FROM artwork_generation`); got != art+1 {
		t.Errorf("artwork generation %d, was %d", got, art)
	}
	before, _ := clock(t, d)
	mustExec(t, d, `DELETE FROM collection_movie`)
	mustExec(t, d, `DELETE FROM edition_file`)
	if got := tombstone(t, d, "collection", 1); got <= before {
		t.Errorf("emptied TMDB collection tombstone at %d", got)
	}
	if got := tombstone(t, d, "media_file", 1); got <= before {
		t.Errorf("orphaned file tombstone at %d", got)
	}
}

func installed(t *testing.T, d db.DBTX) map[string]string {
	t.Helper()
	rows, err := d.Query(`SELECT name, sql FROM sqlite_schema WHERE type = 'trigger' AND name LIKE 'sync\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var name, q string
		if err := rows.Scan(&name, &q); err != nil {
			t.Fatal(err)
		}
		out[name] = q
	}
	return out
}

func TestMigrateInstallsTheGeneratedTriggers(t *testing.T) {
	d := dbtest.Open(t)
	want := replica.V1.Triggers()
	have := installed(t, d)
	if len(have) != len(want) {
		t.Errorf("%d triggers installed, %d generated", len(have), len(want))
	}
	for name, q := range want {
		if have[name] != q {
			t.Errorf("%s installed as %q", name, have[name])
		}
	}
	for _, tb := range replica.V1.Tables {
		for _, op := range []string{"insert", "update", "delete"} {
			if want["sync_"+tb.Source+"_"+op] == "" {
				t.Errorf("%s has no %s trigger", tb.Source, op)
			}
		}
	}
	if want["sync_device_update"] == "" {
		t.Error("device's scope is not watched")
	}
}

// Clients snapshot after an upgrade, which may change what rows hold
// without stamping them, and after the triggers were found changed.
func TestMigrateMovesScopeOnUpgradeOnly(t *testing.T) {
	d := dbtest.Open(t)
	v0, s0 := clock(t, d)
	if v0 == 0 || s0 != v0 {
		t.Errorf("a new database at version %d, scope %d", v0, s0)
	}
	if err := replica.Migrate(d, ""); err != nil {
		t.Fatal(err)
	}
	if v, s := clock(t, d); v != v0 || s != s0 {
		t.Errorf("a restart moved the clock to %d, scope %d", v, s)
	}
	if err := replica.Migrate(d, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	v1, s1 := clock(t, d)
	if v1 <= v0 || s1 != v1 || stamp(t, d, `SELECT release = '9.9.9' FROM sync_clock`) != 1 {
		t.Errorf("a new release left the clock at %d, scope %d", v1, s1)
	}
	mustExec(t, d, `DROP TRIGGER sync_watch_state_update`)
	if err := replica.Migrate(d, "9.9.9"); err != nil {
		t.Fatal(err)
	}
	if v, s := clock(t, d); v <= v1 || s != v {
		t.Errorf("a missing trigger left the clock at %d, scope %d", v, s)
	}
	if installed(t, d)["sync_watch_state_update"] == "" {
		t.Error("the missing trigger was not restored")
	}
}

// A database from before sync upgrades with its rows at version 0 and
// every client told to snapshot.
func TestMigrateUpgradesAnOlderDatabase(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateTo(d, 57); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1, 'L', 'movies', 0, 0)`)
	if err := replica.Migrate(d, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if v, s := clock(t, d); v == 0 || s != v || stamp(t, d, `SELECT sync_version FROM library`) != 0 {
		t.Errorf("clock %d, scope %d", v, s)
	}
	if len(installed(t, d)) != len(replica.V1.Triggers()) {
		t.Error("triggers not installed")
	}
}

// entryRanks checks playlist_entry is ranked and versioned with its
// ordering index and synced triggers, and answers its rows as id:rank:version
// in play order.
func entryRanks(t *testing.T, d *sql.DB) []string {
	t.Helper()
	if cols := strs(t, d, `SELECT name FROM pragma_table_info('playlist_entry') WHERE name IN ('position', 'rank', 'sync_version') ORDER BY name`); !slices.Equal(cols, []string{"rank", "sync_version"}) {
		t.Errorf("playlist_entry has %v", cols)
	}
	if cols := strs(t, d, `SELECT name FROM pragma_index_info('playlist_entry_order') ORDER BY seqno`); !slices.Equal(cols, []string{"playlist_id", "rank", "id"}) {
		t.Errorf("playlist_entry_order on %v", cols)
	}
	if have, want := installed(t, d), replica.V1.Triggers(); have["sync_playlist_entry_update"] != want["sync_playlist_entry_update"] || len(have) != len(want) {
		t.Errorf("%d triggers installed, %d generated", len(have), len(want))
	}
	return strs(t, d, `SELECT id || ':' || rank || ':' || sync_version FROM playlist_entry ORDER BY playlist_id, rank, id`)
}

// A rank change is a change a replica sees.
func moveStamps(t *testing.T, d *sql.DB, id int64) {
	t.Helper()
	before, _ := clock(t, d)
	mustExec(t, d, `UPDATE playlist_entry SET rank = 'a0V' WHERE id = ?`, id)
	if got := stamp(t, d, `SELECT sync_version FROM playlist_entry WHERE id = ?`, id); got <= before {
		t.Errorf("a move stamped %d, clock was %d", got, before)
	}
}

func TestNewDatabaseRanksPlaylistEntries(t *testing.T) {
	d := seeded(t)
	mustExec(t, d, `INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (1, 1, 'P', 'movies', 0, 0)`)
	mustExec(t, d, `INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at) VALUES (1, 1, 1, 'a0', 0), (2, 1, 2, 'a1', 0)`)
	v, _ := clock(t, d)
	if got := entryRanks(t, d); !slices.Equal(got, []string{fmt.Sprintf("1:a0:%d", v-1), fmt.Sprintf("2:a1:%d", v)}) {
		t.Errorf("entries %v, clock %d", got, v)
	}
	moveStamps(t, d, 2)
}

// The rank migration runs after the sync one with no sync trigger, those
// of a build that had only the sync one dropped first: the entries keep
// their order as ranks and stay at version 0 for the snapshot every client
// then takes.
func TestMigrateUpgradesPlaylistEntriesToRanks(t *testing.T) {
	for _, from := range []int{57, 58} {
		t.Run(fmt.Sprint(from), func(t *testing.T) {
			d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = d.Close() })
			if err := db.MigrateTo(d, from); err != nil {
				t.Fatal(err)
			}
			for _, q := range []string{
				`INSERT INTO account (id, kind, name, created_at, updated_at) VALUES (1, 'adult', 'A', 0, 0)`,
				`INSERT INTO library (id, name, kind, created_at, updated_at) VALUES (1, 'L', 'movies', 0, 0)`,
				`INSERT INTO item (id, library_id, kind, parsed_title, added_at, updated_at) VALUES (1, 1, 'movie', 'M', 0, 0)`,
				`INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (1, 1, 'P', 'movies', 0, 0)`,
				`INSERT INTO playlist_entry (id, playlist_id, item_id, position, added_at) VALUES (3, 1, 1, 0, 0), (1, 1, 1, 2, 0), (2, 1, 1, 1, 0)`,
			} {
				mustExec(t, d, q)
			}
			if from == 58 {
				// As that build's generator wrote it: dropping position fails
				// while it stands.
				mustExec(t, d, `CREATE TRIGGER sync_playlist_entry_update AFTER UPDATE OF position ON playlist_entry
					WHEN OLD.position IS NOT NEW.position BEGIN UPDATE sync_clock SET version = version + 1; END`)
			}
			if err := replica.Migrate(d, "1.0.0"); err != nil {
				t.Fatal(err)
			}
			if got := entryRanks(t, d); !slices.Equal(got, []string{"3:a0:0", "2:a1:0", "1:a2:0"}) {
				t.Errorf("entries %v", got)
			}
			if v, s := clock(t, d); v == 0 || s != v {
				t.Errorf("clock %d, scope %d", v, s)
			}
			moveStamps(t, d, 1)
		})
	}
}
