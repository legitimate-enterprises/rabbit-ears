// Package db opens the SQLite database, runs migrations and holds the helpers
// every repository shares.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

var (
	// ErrNotFound is returned when a row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned when a unique constraint rejects a write.
	ErrConflict = errors.New("conflict")
	// ErrReference is returned when a foreign key rejects a write.
	ErrReference = errors.New("invalid reference")
)

// DBTX is satisfied by *sql.DB and *sql.Tx so repositories run in either.
type DBTX interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Open opens (creating if needed) the database at path with foreign keys on
// and a busy timeout. A file database gets a pool of connections, so reads
// run alongside each other and alongside one write. ":memory:" gets exactly
// one connection, since each connection to it is a database of its own.
//
// A transaction takes the write lock as it begins, so one that reads and
// then writes never fails on the upgrade; a write that finds the lock held
// waits up to the busy timeout, which outlasts the longest write: deleting
// a library cascades through every row under it, seconds for tens of
// thousands of titles. WAL with synchronous NORMAL can lose the last
// commits to a power cut, never consistency.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// Eight is more requests than a household's clients make at once; each
	// connection keeps a page cache of its own.
	conns := 8
	if path == ":memory:" {
		conns = 1
	}
	d.SetMaxOpenConns(conns)
	d.SetMaxIdleConns(conns)
	return d, nil
}

// Wrap maps SQLite constraint failures and missing rows onto the package
// errors, leaving anything else untouched.
func Wrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var se *sqlite.Error
	if errors.As(err, &se) {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return fmt.Errorf("%w: %s", ErrConflict, se.Error())
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			return fmt.Errorf("%w: %s", ErrReference, se.Error())
		}
	}
	return err
}

// Scanner is a *sql.Row or *sql.Rows.
type Scanner interface{ Scan(...any) error }

// Collect scans every row of a query with scan and closes the rows. It takes
// the query's error too, returned wrapped in place of any rows.
func Collect[T any](rows *sql.Rows, err error, scan func(Scanner) (T, error)) ([]T, error) {
	if err != nil {
		return nil, Wrap(err)
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, Wrap(rows.Err())
}

// Column scans a row of one column, for Collect.
func Column[T any](s Scanner) (T, error) {
	var v T
	return v, Wrap(s.Scan(&v))
}

// ExecOne runs a write that must touch exactly one row, else ErrNotFound.
func ExecOne(d DBTX, query string, args ...any) error {
	res, err := d.Exec(query, args...)
	if err != nil {
		return Wrap(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Ms encodes an instant as Unix milliseconds, so the stored value is UTC
// whatever zone the caller's time carries.
func Ms(t time.Time) int64 { return t.UnixMilli() }

// FromMs decodes stored Unix milliseconds into a UTC time.
func FromMs(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// NullMs encodes an optional instant; nil stores NULL.
func NullMs(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

// FromNullMs decodes an optional stored instant.
func FromNullMs(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := FromMs(n.Int64)
	return &t
}

// FromNullInt decodes an optional stored integer.
func FromNullInt(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	v := int(n.Int64)
	return &v
}

// DataDir is the folder the server makes its artwork, thumbnails, seek
// previews and caches in. A path under it is stored relative to it, so the
// database does not depend on where the folder is.
var DataDir string

// DataRel is p as stored: relative to DataDir when it is under it.
func DataRel(p string) string {
	if rel, err := filepath.Rel(DataDir, p); p != "" && err == nil && filepath.IsLocal(rel) {
		return rel
	}
	return p
}

// DataAbs is a stored path as a file, a relative one being under DataDir.
func DataAbs(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(DataDir, p)
}

// NullStr stores the empty string as NULL.
func NullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// InTx runs fn atomically. When d is a *sql.DB it opens a transaction; when d
// is already a *sql.Tx it joins the caller's.
func InTx(d DBTX, fn func(DBTX) error) error {
	sdb, ok := d.(*sql.DB)
	if !ok {
		return fn(d)
	}
	tx, err := sdb.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// In returns "(?, ?, ...)" and its arguments for an IN over ids, which must
// not be empty.
func In(ids []uid.ID) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ") + ")", args
}
