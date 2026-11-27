package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var embedded embed.FS

// migrationFS holds the migrations under migrations/; tests swap in a set
// of their own.
var migrationFS fs.FS = embedded

type migration struct {
	version int
	name    string
	sql     string
}

// foreignKeysOff marks a migration that rebuilds a table other tables refer
// to. SQLite ignores the foreign_keys pragma inside a transaction, and with
// keys on, dropping the old table would cascade into its children.
const foreignKeysOff = "-- migrate: foreign_keys off"

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		num, _, ok := strings.Cut(e.Name(), "_")
		v, convErr := strconv.Atoi(num)
		if !ok || convErr != nil {
			return nil, fmt.Errorf("migration %q: name must start with a number and underscore", e.Name())
		}
		body, err := fs.ReadFile(migrationFS, "migrations/"+e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{v, e.Name(), string(body)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1; got %d at position %d", m.version, i+1)
		}
	}
	return ms, nil
}

// LatestVersion is the highest embedded migration version.
func LatestVersion() int {
	ms, err := loadMigrations()
	if err != nil {
		panic(err)
	}
	return len(ms)
}

// Migrate applies every pending migration.
func Migrate(d *sql.DB) error { return MigrateTo(d, LatestVersion()) }

// MigrateTo applies pending migrations up to and including version target.
// Each migration runs in its own transaction together with its version bump;
// migrations are forward-only and a database newer than the binary is refused.
// A database that already holds a schema is copied first, since that copy is
// the only way back to the binary that wrote it, and without one nothing is
// migrated.
// All of them run on one connection, so a migration that turns foreign keys
// off turns them off only where it runs.
func MigrateTo(d *sql.DB, target int) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migration (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	var cur int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migration`).Scan(&cur); err != nil {
		return err
	}
	if cur > len(ms) {
		return fmt.Errorf("database is at schema version %d, newer than this binary (%d)", cur, len(ms))
	}
	if cur > 0 && cur < min(target, len(ms)) {
		if err := backup(ctx, conn, cur); err != nil {
			return fmt.Errorf("copying the database before migrating it: %w", err)
		}
	}
	for _, m := range ms {
		if m.version <= cur || m.version > target {
			continue
		}
		if err := apply(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

// copiesKept is how many copies backup leaves beside the database.
const copiesKept = 5

// backup copies the database conn has open, at schema version v, into
// backups/ beside its file and keeps the copies of the newest copiesKept
// versions. A version has one copy, so a migration that fails on every start
// rewrites it and pushes no older one out. A database in memory has no file
// and is not copied.
func backup(ctx context.Context, conn *sql.Conn, v int) error {
	var file string
	if err := conn.QueryRowContext(ctx, `SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&file); err != nil || file == "" {
		return err
	}
	dir := filepath.Join(filepath.Dir(file), "backups")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	stem := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	dst := filepath.Join(dir, fmt.Sprintf("%s-v%04d.db", stem, v))
	// VACUUM INTO refuses a file that exists, and a copy cut short must never
	// carry the name of a whole one.
	tmp := dst + ".tmp"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := conn.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return err
	}
	f, err := os.Open(tmp) //nolint:gosec // the copy just written beside the database
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	slog.Info("Copied the database before migrating it", "copy", dst)
	old, err := filepath.Glob(filepath.Join(dir, stem+"-v[0-9][0-9][0-9][0-9].db"))
	if err != nil {
		return err
	}
	slices.Sort(old)
	for ; len(old) > copiesKept; old = old[1:] {
		if err := os.Remove(old[0]); err != nil {
			return err
		}
	}
	return nil
}

// apply runs one migration and records it. A migration marked
// foreignKeysOff runs with keys off and must leave no dangling reference;
// keys are on again before the connection returns to the pool.
func apply(ctx context.Context, conn *sql.Conn, m migration) (err error) {
	off := strings.HasPrefix(m.sql, foreignKeysOff)
	if off {
		if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			return err
		}
		defer func() {
			if _, onErr := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err == nil {
				err = onErr
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("migration %s: %w", m.name, err)
	}
	if off {
		var table string
		if err := tx.QueryRowContext(ctx, `SELECT "table" FROM pragma_foreign_key_check`).Scan(&table); err == nil {
			return fmt.Errorf("migration %s: leaves a dangling reference in %s", m.name, table)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migration (version, name, applied_at)
		VALUES (?, ?, CAST(strftime('%s','now') AS INTEGER) * 1000)`, m.version, m.name); err != nil {
		return err
	}
	return tx.Commit()
}

// Version reports the applied schema version (0 for an empty database).
func Version(d *sql.DB) (int, error) {
	var v int
	err := d.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migration`).Scan(&v)
	return v, err
}
