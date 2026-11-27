package replica

import (
	"cmp"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// File is an open snapshot, at Path, and the version it was taken at.
type File struct {
	*os.File
	Path    string
	Version int64
}

// Snapshot opens household's replica as one SQLite database. A snapshot
// already built is served while the household has nothing to read since
// its version; otherwise one is built, in place of the household's older.
func (s *Sync) Snapshot(ctx context.Context, household uid.ID) (File, error) {
	if s.Dir == "" {
		return File{}, errors.New("replica: no snapshot directory")
	}
	mu, _ := s.builds.LoadOrStore(household, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	prefix := fmt.Sprintf("h%s-s%d-", household, V1.Version)
	built, err := filepath.Glob(filepath.Join(s.Dir, prefix+"v*.sqlite"))
	if err != nil {
		return File{}, err
	}
	var newest File
	for _, p := range built {
		v, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), prefix+"v"), ".sqlite"), 10, 64)
		if err == nil && (newest.Path == "" || v > newest.Version) {
			newest = File{Path: p, Version: v}
		}
	}
	if newest.Path != "" {
		if changed, err := s.Changed(ctx, household, newest.Version); err != nil || !changed {
			if err == nil {
				newest.File, err = os.Open(newest.Path)
			}
			return newest, err
		}
	}
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return File{}, err
	}
	tmp, err := os.CreateTemp(s.Dir, prefix+"*.tmp")
	if err != nil {
		return File{}, err
	}
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := s.define(ctx, tmp.Name()); err != nil {
		return File{}, err
	}
	out := File{}
	if out.Version, err = s.fill(ctx, tmp.Name(), household); err != nil {
		return File{}, err
	}
	out.Path = filepath.Join(s.Dir, fmt.Sprintf("%sv%d.sqlite", prefix, out.Version))
	if err := os.Rename(tmp.Name(), out.Path); err != nil {
		return File{}, err
	}
	if out.File, err = os.Open(out.Path); err != nil {
		return File{}, err
	}
	// A reader of an older file keeps it open until it is done.
	for _, p := range built {
		if p != out.Path {
			_ = os.Remove(p)
		}
	}
	return out, nil
}

// define creates the replica's schema in the empty database at path: V1's
// tables, the holidays, replica_meta, the views and the indexes. It keeps no
// foreign key, CHECK or UNIQUE constraint, so a client applies a diff row by
// row in any order.
func (s *Sync) define(ctx context.Context, path string) error {
	stmts, err := s.schema(ctx)
	if err != nil {
		return err
	}
	f, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	for _, q := range stmts {
		if _, err := f.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("replica schema: %w\n%s", err, q)
		}
	}
	for _, h := range occasion.Holidays() {
		if _, err := f.ExecContext(ctx, `INSERT INTO holiday (key, name) VALUES (?, ?)`, h.Key, h.Name); err != nil {
			return err
		}
		for _, d := range h.Dates {
			if _, err := f.ExecContext(ctx, `INSERT INTO holiday_date (key, day) VALUES (?, ?)`, h.Key, d.Format("2006-01-02")); err != nil {
				return err
			}
		}
		for i, c := range h.Colors {
			if _, err := f.ExecContext(ctx, `INSERT INTO holiday_color (key, position, color) VALUES (?, ?, ?)`, h.Key, i, c); err != nil {
				return err
			}
		}
	}
	return nil
}

// static are the tables a snapshot carries from the server's binary, not
// its database: the built-in holidays, each date a calendar day.
var static = []string{
	`CREATE TABLE holiday (key TEXT, name TEXT, PRIMARY KEY (key))`,
	`CREATE TABLE holiday_date (key TEXT, day TEXT, PRIMARY KEY (key, day))`,
	`CREATE TABLE holiday_color (key TEXT, position INTEGER, color TEXT, PRIMARY KEY (key, position))`,
	`CREATE TABLE replica_meta (schema INTEGER, version INTEGER, household_id BLOB)`,
}

// schema is every statement that defines V1's replica, read from the
// server's own schema: each column's declared type and collation, the
// views, and the indexes on what the replica holds.
func (s *Sync) schema(ctx context.Context) ([]string, error) {
	var out []string
	cols := map[string][]string{}
	for _, tb := range V1.Tables {
		var create string
		if err := s.d.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, tb.Source).Scan(&create); err != nil {
			return nil, fmt.Errorf("table %s: %w", tb.Source, err)
		}
		types := map[string]string{}
		rows, err := s.d.QueryContext(ctx, `SELECT name, type FROM pragma_table_info(?)`, tb.Source)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var n, t string
			if err := rows.Scan(&n, &t); err != nil {
				_ = rows.Close()
				return nil, err
			}
			types[n] = t
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		coll := collations(create)
		defs := make([]string, len(tb.Cols))
		for i, c := range tb.Cols {
			defs[i] = strings.TrimSpace(c.Name + " " + cmp.Or(c.Type, types[c.Name]))
			if coll[c.Name] != "" {
				defs[i] += " COLLATE " + coll[c.Name]
			}
		}
		out = append(out, `CREATE TABLE `+tb.Name+` (`+strings.Join(defs, ", ")+`, PRIMARY KEY (`+strings.Join(tb.Key, ", ")+`))`)
		if tb.Name == tb.Source {
			cols[tb.Name] = columns(tb)
		}
	}
	out = append(out, static...)
	views, err := Views(s.d)
	if err != nil {
		return nil, err
	}
	out = append(out, views...)
	rows, err := s.d.QueryContext(ctx, `SELECT s.name, s.tbl_name, s.sql, group_concat(COALESCE(x.name, ''), ',')
		FROM sqlite_schema s, pragma_index_xinfo(s.name) x
		WHERE s.type = 'index' AND s.sql IS NOT NULL AND x.key GROUP BY s.name ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, tbl, q, keys string
		if err := rows.Scan(&name, &tbl, &q, &keys); err != nil {
			return nil, err
		}
		// An index on an expression names no column; one on a column the
		// replica lacks has no place in it.
		if cols[tbl] == nil || slices.ContainsFunc(strings.Split(keys, ","), func(k string) bool { return !slices.Contains(cols[tbl], k) }) {
			continue
		}
		out = append(out, unique.ReplaceAllString(q, "CREATE INDEX"))
	}
	return out, rows.Err()
}

var unique = regexp.MustCompile(`(?i)^CREATE\s+UNIQUE\s+INDEX`)

// collations reads each column's collation from a CREATE TABLE statement.
// A COLLATE inside a column's CHECK would be taken for the column's own;
// the server's schema has none.
func collations(create string) map[string]string {
	out := map[string]string{}
	body := create[strings.Index(create, "(")+1 : strings.LastIndex(create, ")")]
	var defs []string
	depth, start := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				defs, start = append(defs, body[start:i]), i+1
			}
		}
	}
	for _, def := range append(defs, body[start:]) {
		f := strings.Fields(def)
		for i := 1; i+1 < len(f); i++ {
			if strings.EqualFold(f[i], "COLLATE") {
				out[strings.Trim(f[0], "\"`[]")] = f[i+1]
			}
		}
	}
	return out
}

// fill copies household's rows into the replica at path in one read of the
// server's database, and returns the version that read saw. The replica is
// attached to a connection of its own, its writes unjournalled: a failed
// build is thrown away whole.
func (s *Sync) fill(ctx context.Context, path string, household uid.ID) (version int64, err error) {
	conn, err := s.d.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `ATTACH ? AS snap`, path); err != nil {
		return 0, err
	}
	defer func() {
		// A connection still attached must not go back to the pool.
		if _, err := conn.ExecContext(context.Background(), `DETACH snap`); err != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	for _, q := range []string{`PRAGMA snap.journal_mode = OFF`, `PRAGMA snap.synchronous = OFF`} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			return 0, err
		}
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `SELECT version FROM main.sync_clock`).Scan(&version); err != nil {
		return 0, err
	}
	for _, tb := range V1.Tables {
		exprs := make([]string, len(tb.Cols))
		for i, c := range tb.Cols {
			exprs[i] = c.Expr
		}
		q := `INSERT INTO snap.` + tb.Name + ` (` + strings.Join(columns(tb), ", ") + `) SELECT ` + strings.Join(exprs, ", ") + //nolint:gosec // V1's own SQL
			` FROM main.` + tb.Source + ` WHERE ` + scoped(tb.Scope)
		if _, err := tx.ExecContext(ctx, q, household); err != nil {
			return 0, fmt.Errorf("%s: %w", tb.Name, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO snap.replica_meta (schema, version, household_id) VALUES (?, ?, ?)`,
		V1.Version, version, household); err != nil {
		return 0, err
	}
	return version, tx.Commit()
}
