// Package dbtest gives tests a migrated database in a temp file.
package dbtest

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
)

// Open returns a fully migrated database, sync triggers installed, that is
// closed with the test.
func Open(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := replica.Migrate(d, ""); err != nil {
		t.Fatal(err)
	}
	return d
}

// DataDir sets db.DataDir to dir until the test ends.
func DataDir(t *testing.T, dir string) {
	t.Helper()
	was := db.DataDir
	db.DataDir = dir
	t.Cleanup(func() { db.DataDir = was })
}

// Planner records the query plan of each query it runs, one step per line.
type Planner struct {
	db.DBTX
	Plans []string
}

// Query plans q, then runs it.
func (p *Planner) Query(q string, args ...any) (*sql.Rows, error) {
	if err := p.plan(q, args); err != nil {
		return nil, err
	}
	return p.DBTX.Query(q, args...)
}

// QueryRow plans q, then runs it; a failed plan is left for the query to
// report.
func (p *Planner) QueryRow(q string, args ...any) *sql.Row {
	_ = p.plan(q, args)
	return p.DBTX.QueryRow(q, args...)
}

func (p *Planner) plan(q string, args []any) error {
	rows, err := p.DBTX.Query(`EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			return err
		}
		plan = append(plan, detail)
	}
	p.Plans = append(p.Plans, strings.Join(plan, "\n"))
	return rows.Err()
}

// OpenPreferLocal is Open with the server row made and its metadata
// settings Local preferred, so a remote row shows wherever no local row
// says otherwise: the setting for tests that are not about the settings.
func OpenPreferLocal(t *testing.T) *sql.DB {
	t.Helper()
	d := Open(t)
	Sources(t, d, "local", "preferred")
	return d
}

// Sources makes the server row if there is none and sets its metadata
// settings.
func Sources(t *testing.T, d *sql.DB, source, mode string) {
	t.Helper()
	if _, err := d.Exec(`INSERT OR IGNORE INTO server_info (id, name, created_at, updated_at) VALUES (1, 'Test', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`UPDATE server_info SET metadata_source = ?, metadata_mode = ? WHERE id = 1`, source, mode); err != nil {
		t.Fatal(err)
	}
}
