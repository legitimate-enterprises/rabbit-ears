package replica_test

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"slices"
	"strings"
	"testing"

	"modernc.org/sqlite"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// The fixtures' SQL names fixture id n as uid(n), the bytes of uid.N(n), on
// every connection the tests open.
func init() {
	err := sqlite.RegisterDeterministicScalarFunction("uid", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		n, ok := args[0].(int64)
		if !ok {
			return nil, fmt.Errorf("uid(%v)", args[0])
		}
		id := uid.N(int(n))
		return id[:], nil
	})
	if err != nil {
		panic(err)
	}
}

// u is fixture id n as the wire carries it.
func u(n int) string { return uid.N(n).String() }

func strs(t *testing.T, d db.DBTX, q string, args ...any) []string {
	t.Helper()
	rows, err := d.Query(q, args...)
	out, err := db.Collect(rows, err, db.Column[string])
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sources() map[string][]replica.Table {
	out := map[string][]replica.Table{}
	for _, tb := range replica.V1.Tables {
		out[tb.Source] = append(out[tb.Source], tb)
	}
	return out
}

// Every server table is synced, watched for scope alone or not synced, so a
// new table is a decision.
func TestEveryTableIsDecided(t *testing.T) {
	d := dbtest.Open(t)
	src := sources()
	decided := map[string]string{}
	for name := range src {
		decided[name] = "synced"
	}
	for name := range replica.V1.Defines {
		if decided[name] == "" {
			decided[name] = "scope"
		}
	}
	for _, name := range replica.V1.NotSynced {
		if decided[name] != "" {
			t.Errorf("%s is both %s and not synced", name, decided[name])
		}
		decided[name] = "not synced"
	}
	tables := strs(t, d, `SELECT name FROM sqlite_schema WHERE type = 'table'`)
	for _, name := range tables {
		if decided[name] == "" {
			t.Errorf("table %s is neither synced nor listed in V1.NotSynced", name)
		}
	}
	for name := range decided {
		if !slices.Contains(tables, name) {
			t.Errorf("V1 names %s, which is no table", name)
		}
	}
}

// Every column of a synced table is read by the replica or listed unsynced,
// so a new column, a secret among them, is a decision.
func TestEveryColumnIsDecided(t *testing.T) {
	d := dbtest.Open(t)
	for name, tbs := range sources() {
		t.Run(name, func(t *testing.T) {
			cols := strs(t, d, `SELECT name FROM pragma_table_info(?) WHERE name != 'sync_version'`, name)
			pk := strs(t, d, `SELECT name FROM pragma_table_info(?) WHERE pk > 0 ORDER BY pk`, name)
			read := map[string]bool{}
			for _, tb := range tbs {
				if !slices.Equal(tb.Key, pk) {
					t.Errorf("%s key %v, primary key %v", tb.Name, tb.Key, pk)
				}
				for _, c := range tb.Cols {
					for _, w := range c.Watch {
						if !slices.Contains(cols, w) {
							t.Errorf("%s.%s reads %s, which %s lacks", tb.Name, c.Name, w, name)
						}
						read[w] = true
					}
				}
			}
			unsynced := replica.V1.Unsynced[name]
			for _, c := range unsynced {
				if read[c] {
					t.Errorf("%s is read and unsynced", c)
				}
				if !slices.Contains(cols, c) {
					t.Errorf("unsynced %s is no column", c)
				}
			}
			for _, c := range cols {
				if !read[c] && !slices.Contains(unsynced, c) {
					t.Errorf("column %s.%s is neither read by the replica nor listed in V1.Unsynced", name, c)
				}
			}
			if idx := strs(t, d, `SELECT name FROM pragma_index_list(?) l
				WHERE (SELECT group_concat(name) FROM pragma_index_info(l.name)) = 'sync_version'`, name); len(idx) != 1 {
				t.Errorf("sync_version indexes %v", idx)
			}
		})
	}
	for name := range replica.V1.Unsynced {
		if sources()[name] == nil {
			t.Errorf("V1.Unsynced lists %s, which is not synced", name)
		}
	}
}

// Nothing a member may not see, nor any secret, is copied into a replica.
func TestNothingSecretIsProjected(t *testing.T) {
	never := map[string][]string{
		"account":     {"password_hash", "pin_hash", "pin_failures", "pin_locked_until"},
		"item":        {"path"},
		"media_file":  {"path", "probe_error"},
		"chapter":     {"thumbnail_path"},
		"server_info": {"tmdb_key"},
		"cover":       {"jpeg"},
	}
	for _, tb := range replica.V1.Tables {
		for _, c := range tb.Cols {
			for _, secret := range never[tb.Source] {
				if c.Expr == secret || c.Name == secret {
					t.Errorf("%s.%s copies %s", tb.Name, c.Name, secret)
				}
			}
		}
	}
	for _, name := range []string{"device", "device_link", "join_code", "sso_provider", "account_identity", "library_folder",
		"seek_preview", "identity_review", "identity_evidence", "playback_session", "watch_together", "conversion"} {
		if sources()[name] != nil {
			t.Errorf("%s is synced", name)
		}
	}
}

// Server paths reach a replica as a member sees them in a response: a
// TMDB image path whole, one in a store as its address, any other by its
// file name.
func TestImagePathsAreAddressesOrFileNames(t *testing.T) {
	d := dbtest.Open(t)
	mustExec(t, d, `INSERT INTO library (id, name, kind, created_at, modified_at) VALUES (uid(1), 'L', 'movies', 0, 0)`)
	mustExec(t, d, `INSERT INTO item (id, library_id, kind, parsed_title, added_at, created_at, modified_at) VALUES (uid(1), uid(1), 'movie', 'M', 0, 0, 0)`)
	mustExec(t, d, `INSERT INTO item_metadata (item_id, source, provider, poster_path, backdrop_path)
		VALUES (uid(1), 'remote', 'tmdb', '/abc.jpg', NULL), (uid(1), 'local', 'tmdb', '/Volumes/Films/M (2001)/poster.jpg', 'thumbnails/local/7.jpg')`)
	var tb replica.Table
	for _, x := range replica.V1.Tables {
		if x.Name == "item_metadata" {
			tb = x
		}
	}
	got := map[string][2]sql.NullString{}
	q := `SELECT source, ` + expr(tb, "poster_path") + `, ` + expr(tb, "backdrop_path") + ` FROM item_metadata` //nolint:gosec // V1's own SQL
	rows, err := d.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var source string
		var p [2]sql.NullString
		if err := rows.Scan(&source, &p[0], &p[1]); err != nil {
			t.Fatal(err)
		}
		got[source] = p
	}
	if r, l := got["remote"], got["local"]; r[0].String != "/abc.jpg" || r[1].Valid || l[0].String != "poster.jpg" || l[1].String != "local/7.jpg" {
		t.Errorf("remote %v, local %v", r, l)
	}
}

func expr(tb replica.Table, name string) string {
	for _, c := range tb.Cols {
		if c.Name == name {
			return c.Expr
		}
	}
	return "NULL"
}

// Every table's columns and scopes compile against the server's schema.
func TestScopesCompile(t *testing.T) {
	d := dbtest.Open(t)
	for _, tb := range replica.V1.Tables {
		exprs := make([]string, len(tb.Cols))
		for i, c := range tb.Cols {
			exprs[i] = c.Expr
		}
		q := `SELECT ` + strings.Join(exprs, ", ") + ` FROM ` + tb.Source + ` WHERE ?1 IS NOT NULL` //nolint:gosec // V1's own SQL
		if tb.Scope != "" {
			q += ` AND (` + tb.Scope + `)`
		}
		if _, err := d.Exec(q, uid.N(1)); err != nil {
			t.Errorf("%s: %v", tb.Name, err)
		}
		q = `SELECT k1, k2, k3 FROM sync_tombstone WHERE tbl = ?2 AND ?1 IS NOT NULL`
		if tb.TombScope != "" {
			q += ` AND (` + tb.TombScope + `)`
		}
		if _, err := d.Exec(q, uid.N(1), tb.Source); err != nil {
			t.Errorf("%s tombstones: %v", tb.Name, err)
		}
	}
}

func mustExec(t *testing.T, d db.DBTX, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
