package replica

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func (s Schema) table(name string) (Table, bool) {
	for _, tb := range s.Tables {
		if tb.Name == name {
			return tb, true
		}
	}
	return Table{}, false
}

// Touchable reports whether key, decoded from JSON, names a row of the
// replica table name: per column of its primary key, an integer for one
// the source declares INTEGER, an id's canonical string for one it declares
// BLOB and a string for any other.
func (s *Sync) Touchable(ctx context.Context, sch Schema, name string, key []any) (bool, error) {
	_, ok, err := s.bind(ctx, sch, name, key)
	return ok, err
}

// bind is key as Touch binds it, ok being whether it is Touchable.
func (s *Sync) bind(ctx context.Context, sch Schema, name string, key []any) (out []any, ok bool, err error) {
	tb, ok := sch.table(name)
	if !ok || len(key) != len(tb.Key) {
		return nil, false, nil
	}
	types, err := s.keyTypes(ctx, tb)
	if err != nil {
		return nil, false, err
	}
	out = make([]any, len(key))
	for i, v := range key {
		str, isStr := v.(string)
		switch types[i] {
		case "INTEGER":
			if out[i], ok = integral(v).(int64); !ok {
				return nil, false, nil
			}
		case "BLOB":
			id, err := uid.Parse(str)
			if !isStr || err != nil {
				return nil, false, nil
			}
			out[i] = id
		default:
			if !isStr {
				return nil, false, nil
			}
			out[i] = str
		}
	}
	return out, true, nil
}

// keyTypes is, per column of tb's key, INTEGER or BLOB for one the source
// declares so and empty for any other, read from the schema once per table.
func (s *Sync) keyTypes(ctx context.Context, tb Table) ([]string, error) {
	if v, ok := s.keys.Load(tb.Source); ok {
		return v.([]string), nil
	}
	types := make([]string, len(tb.Key))
	for i, c := range tb.Key {
		var typ string
		if err := s.d.QueryRowContext(ctx, `SELECT type FROM pragma_table_info(?) WHERE name = ?`, tb.Source, c).Scan(&typ); err != nil {
			return nil, err
		}
		switch typ = strings.ToUpper(typ); {
		case strings.Contains(typ, "INT"):
			types[i] = "INTEGER"
		case typ == "BLOB":
			types[i] = "BLOB"
		}
	}
	s.keys.Store(tb.Source, types)
	return types, nil
}

// Touch makes the next catch-up of household's clients bring their copy of
// each row of replica table name at keys, each bound as bind binds it, to
// the server's: one in the household's scope is stamped changed, one that
// is gone leaves a tombstone. One that exists outside the scope is left
// alone, since a tombstone for it would also reach the households that see
// it. One query finds them all, so the scope is read once; each mark then
// takes a version of its own, as a page of changes may end between any two.
func (s Schema) Touch(tx db.DBTX, household uid.ID, name string, keys [][]any) error {
	tb, _ := s.table(name)
	args, values, on := []any{household}, make([]string, len(keys)), make([]string, len(tb.Key))
	for i, c := range tb.Key {
		on[i] = `r.` + c + ` = t.column` + strconv.Itoa(i+2)
	}
	for i, key := range keys {
		parts := []string{strconv.Itoa(i)}
		for _, v := range key {
			args = append(args, v)
			parts = append(parts, `?`+strconv.Itoa(len(args)))
		}
		values[i] = `(` + strings.Join(parts, ", ") + `)`
	}
	// Each key's index, and its row's rowid, 0 for one outside the scope.
	rows, err := tx.Query(`SELECT t.column1, CASE WHEN COALESCE((`+cmp.Or(tb.Scope, "1")+`), 0) THEN r.rowid ELSE 0 END
		FROM (VALUES `+strings.Join(values, ", ")+`) t JOIN `+tb.Source+` r ON `+strings.Join(on, " AND "), args...)
	pairs, err := db.Collect(rows, err, func(sc db.Scanner) ([2]int64, error) {
		var p [2]int64
		return p, sc.Scan(&p[0], &p[1])
	})
	if err != nil {
		return err
	}
	found := map[int]int64{}
	for _, p := range pairs {
		found[int(p[0])] = p[1]
	}
	for i, key := range keys {
		rowid, ok := found[i]
		if ok && rowid == 0 {
			continue
		}
		if _, err := tx.Exec(bump("")); err != nil {
			return err
		}
		if ok {
			_, err = tx.Exec(`UPDATE `+tb.Source+` SET sync_version = (SELECT version FROM sync_clock) WHERE rowid = ?`, rowid)
		} else {
			k := []any{0, 0, 0}
			copy(k, key)
			_, err = tx.Exec(`INSERT INTO sync_tombstone (tbl, k1, k2, k3, version, at)
				VALUES (?, ?, ?, ?, (SELECT version FROM sync_clock), CAST(strftime('%s', 'now') AS INTEGER) * 1000)
				ON CONFLICT (tbl, k1, k2, k3) DO UPDATE SET version = excluded.version, at = excluded.at`, tb.Source, k[0], k[1], k[2])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// integral is a key part decoded from JSON, where every number is a
// float64, as the integer it stands for, so its tombstone matches the
// triggers'.
func integral(v any) any {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
		return int64(f)
	}
	return v
}

// Answered is the result a device's action was answered with, and its code;
// ok is false for an action not yet answered.
func (s *Sync) Answered(ctx context.Context, device uid.ID, id string) (result string, code *string, ok bool, err error) {
	err = s.d.QueryRowContext(ctx, `SELECT result, code FROM sync_action WHERE device_id = ? AND uuid = ?`, device, id).Scan(&result, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	return result, code, err == nil, err
}

// Row is a Touchable row of a replica table, its key as JSON decodes it.
type Row struct {
	Table string
	Key   []any
}

// Answer records the result a device of household answered its action
// with, and Touches every row the action touched, in one transaction.
func (s *Sync) Answer(household, device uid.ID, id, result string, code *string, touched []Row) error {
	var tables []string
	keys := map[string][][]any{}
	for _, t := range touched {
		key, ok, err := s.bind(context.Background(), V1, t.Table, t.Key)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("replica: %s %v is not Touchable", t.Table, t.Key)
		}
		if keys[t.Table] == nil {
			tables = append(tables, t.Table)
		}
		keys[t.Table] = append(keys[t.Table], key)
	}
	return db.InTx(s.d, func(tx db.DBTX) error {
		for _, name := range tables {
			if err := V1.Touch(tx, household, name, keys[name]); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT INTO sync_action (device_id, uuid, result, code, processed_at)
			VALUES (?, ?, ?, ?, ?)`, device, id, result, code, db.Ms(time.Now()))
		return err
	})
}
