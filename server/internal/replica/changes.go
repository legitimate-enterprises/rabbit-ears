package replica

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// Sync serves households' replicas of d: the changes since a version, a
// snapshot, and whether there is anything to read.
type Sync struct {
	d *sql.DB
	// Dir keeps the snapshots built, one file per household at most.
	Dir    string
	builds sync.Map // household id to *sync.Mutex
	keys   sync.Map // source table to intKey's answer
}

// New serves the replicas of d; set Dir before asking for a snapshot.
func New(d *sql.DB) *Sync { return &Sync{d: d} }

// Mode is what a client does next with a page.
type Mode string

const (
	// Diff is a page of rows and tombstones to apply.
	Diff Mode = "diff"
	// Snapshot is a replica to replace whole instead.
	Snapshot Mode = "snapshot"
)

// Page is one page of a pass of changes. Version is the watermark: the
// pass's target on its last page, since on every earlier one.
type Page struct {
	Mode       Mode
	Version    int64
	Cursor     string
	Tables     []Rows
	Tombstones []Keys
}

// Rows are rows of one replica table, each a value per column.
type Rows struct {
	Name    string
	Columns []string
	Rows    [][]any
}

// Keys are the keys of rows deleted from one replica table, each its key
// columns in the table's key order.
type Keys struct {
	Table string
	Keys  [][]any
}

// ErrCursor is a cursor that is not one of this pass's.
var ErrCursor = errors.New("not a cursor of this pass")

// A pass runs through 2n phases for V1's n tables: each table's rows, then
// each table's tombstones, in V1's order. Within a phase it reads in
// version order; each version belongs to one row or tombstone, so the last
// version read is where the next page resumes. A cursor is
// target.phase.last.
func cursorOf(target int64, phase int, last int64) string {
	return fmt.Sprintf("%d.%d.%d", target, phase, last)
}

// Changes reads the page of household's changes since a version, nil for
// none held, that cursor names, empty for a pass's first. A first page is a
// snapshot when more rows changed than half the replica holds.
func (s *Sync) Changes(ctx context.Context, household int64, since *int64, cursor string, limit int) (Page, error) {
	return s.changes(ctx, household, since, cursor, limit, true)
}

// Changed reports whether household has anything to read since after: a
// change it may see or a snapshot to take.
func (s *Sync) Changed(ctx context.Context, household, after int64) (bool, error) {
	p, err := s.changes(ctx, household, &after, "", 1, false)
	return p.Mode == Snapshot || len(p.Tables)+len(p.Tombstones) > 0, err
}

// Version is the server's version now.
func (s *Sync) Version(ctx context.Context) (int64, error) {
	var v int64
	err := s.d.QueryRowContext(ctx, `SELECT version FROM sync_clock`).Scan(&v)
	return v, err
}

func (s *Sync) changes(ctx context.Context, household int64, since *int64, cursor string, limit int, whole bool) (Page, error) {
	tx, err := s.d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Page{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var version, scope, purged int64
	if err := tx.QueryRowContext(ctx, `SELECT version, scope_version, purged_version FROM sync_clock`).Scan(&version, &scope, &purged); err != nil {
		return Page{}, err
	}
	// Ahead of the server is a replica of another database, or of this
	// one before a restore.
	if since == nil || *since < scope || *since < purged || *since > version {
		return Page{Mode: Snapshot}, nil
	}
	from := *since
	target, phase, last := version, 0, from
	if cursor != "" {
		if target, phase, last, err = parseCursor(cursor); err != nil || target > version || phase < 0 || phase >= 2*len(V1.Tables) ||
			last < from || last > target {
			return Page{}, ErrCursor
		}
	}
	page := Page{Mode: Diff, Version: target, Tables: []Rows{}, Tombstones: []Keys{}}
	left := limit
	for p := phase; p < 2*len(V1.Tables); p++ {
		lo := from
		if p == phase {
			lo = last
		}
		// One more than fits tells whether the phase goes on.
		vals, versions, err := read(ctx, tx, p, household, lo, target, left+1)
		if err != nil {
			return Page{}, err
		}
		more := len(vals) > left
		if more {
			vals = vals[:left]
		}
		if len(vals) > 0 {
			tb := V1.Tables[p%len(V1.Tables)]
			if p < len(V1.Tables) {
				page.Tables = append(page.Tables, Rows{Name: tb.Name, Columns: columns(tb), Rows: vals})
			} else {
				page.Tombstones = append(page.Tombstones, Keys{Table: tb.Name, Keys: vals})
			}
			lo = versions[len(vals)-1]
		}
		left -= len(vals)
		if more {
			page.Version, page.Cursor = from, cursorOf(target, p, lo)
			break
		}
	}
	if whole && cursor == "" && len(page.Tables)+len(page.Tombstones) > 0 {
		if most, err := mostChanged(ctx, tx, household, from, target); err != nil || most {
			return Page{Mode: Snapshot}, err
		}
	}
	return page, nil
}

func parseCursor(c string) (target int64, phase int, last int64, err error) {
	parts := strings.Split(c, ".")
	if len(parts) != 3 {
		return 0, 0, 0, ErrCursor
	}
	n := make([]int64, 3)
	for i, s := range parts {
		if n[i], err = strconv.ParseInt(s, 10, 64); err != nil {
			return 0, 0, 0, ErrCursor
		}
	}
	return n[0], int(n[1]), n[2], nil
}

func columns(tb Table) []string {
	out := make([]string, len(tb.Cols))
	for i, c := range tb.Cols {
		out[i] = c.Name
	}
	return out
}

// scoped is tb's scope for the household ?1; ?1 is always bound.
func scoped(scope string) string {
	if scope == "" {
		return `?1 IS NOT NULL`
	}
	return `(` + scope + `)`
}

// read is phase p's rows or tombstones of versions in (lo, hi], at most
// limit, in version order, with the version of each.
// It reads by version, which a page and a doorbell span narrowly, never
// by scope or table, which would read and sort all a household or a
// table ever held.
func read(ctx context.Context, tx *sql.Tx, p int, household, lo, hi int64, limit int) ([][]any, []int64, error) {
	tb := V1.Tables[p%len(V1.Tables)]
	var q string
	var width int
	if p < len(V1.Tables) {
		exprs := make([]string, len(tb.Cols))
		for i, c := range tb.Cols {
			exprs[i] = c.Expr
		}
		width = len(exprs)
		q = `SELECT sync_version, ` + strings.Join(exprs, ", ") + ` FROM ` + tb.Source + ` INDEXED BY ` + tb.Source + `_sync` +
			` WHERE sync_version > ?2 AND sync_version <= ?3 AND ` + scoped(tb.Scope) + ` ORDER BY sync_version LIMIT ?4`
	} else {
		width = len(tb.Key)
		q = `SELECT version, ` + strings.Join([]string{"k1", "k2", "k3"}[:width], ", ") + ` FROM sync_tombstone
			WHERE +tbl = '` + tb.Source + `' AND version > ?2 AND version <= ?3 AND ` + scoped(tb.TombScope) + ` ORDER BY version LIMIT ?4`
	}
	rows, err := tx.QueryContext(ctx, q, household, lo, hi, limit)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", tb.Name, err)
	}
	defer func() { _ = rows.Close() }()
	var vals [][]any
	var versions []int64
	for rows.Next() {
		var v int64
		r := make([]any, width)
		dest := []any{&v}
		for i := range r {
			dest = append(dest, &r[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, err
		}
		vals, versions = append(vals, r), append(versions, v)
	}
	return vals, versions, rows.Err()
}

// mostChanged reports whether more of household's rows changed, deleted
// ones included, in (lo, hi] than half the rows its replica holds. It
// counts the replica only as far as settles that.
func mostChanged(ctx context.Context, tx *sql.Tx, household, lo, hi int64) (bool, error) {
	var changed int64
	for _, tb := range V1.Tables {
		var n, gone int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM `+tb.Source+` INDEXED BY `+tb.Source+`_sync WHERE sync_version > ?2 AND sync_version <= ?3 AND `+
			scoped(tb.Scope), household, lo, hi).Scan(&n); err != nil {
			return false, fmt.Errorf("%s: %w", tb.Name, err)
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_tombstone WHERE +tbl = '`+tb.Source+
			`' AND version > ?2 AND version <= ?3 AND `+scoped(tb.TombScope), household, lo, hi).Scan(&gone); err != nil {
			return false, fmt.Errorf("%s tombstones: %w", tb.Name, err)
		}
		changed += n + gone
	}
	need := 2 * changed
	for _, tb := range V1.Tables {
		var n int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT 1 FROM `+tb.Source+` WHERE `+scoped(tb.Scope)+` LIMIT ?2)`,
			household, need).Scan(&n); err != nil {
			return false, fmt.Errorf("%s: %w", tb.Name, err)
		}
		if need -= n; need <= 0 {
			return false, nil
		}
	}
	return true, nil
}
