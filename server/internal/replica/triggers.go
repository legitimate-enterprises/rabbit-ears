package replica

import (
	"database/sql"
	"slices"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
)

// Every change to a synced column moves sync_clock by one and stamps its row
// with the new value, or leaves a tombstone at it. SQLite runs one write
// transaction at a time and a reader sees the clock in the same snapshot as
// the rows, so any change a reader has not seen is above the clock it read.
// The stamp sets only sync_version, which no trigger watches, so it fires
// none.

// bump is the statement that moves the clock. scope, unless empty, is when
// the change also alters some household's scope.
func bump(scope string) string {
	if scope == "" {
		return `UPDATE sync_clock SET version = version + 1;`
	}
	// shortcut: one scope version for the server, so a change to one
	// household's scope makes every household snapshot; keep a scope version
	// per household when that proves too costly.
	return `UPDATE sync_clock SET version = version + 1, scope_version = CASE WHEN ` + scope + ` THEN version + 1 ELSE scope_version END;`
}

// Triggers returns the CREATE TRIGGER statement of every sync trigger, by
// name.
func (s Schema) Triggers() map[string]string {
	out := map[string]string{}
	keys, watch := map[string][]string{}, map[string][]string{}
	for _, tb := range s.Tables {
		keys[tb.Source] = tb.Key
		for _, c := range tb.Cols {
			watch[tb.Source] = union(watch[tb.Source], c.Watch)
		}
	}
	for name, def := range s.Defines {
		watch[name] = union(watch[name], def.Update)
		if keys[name] == nil {
			out["sync_"+name+"_update"] = `CREATE TRIGGER sync_` + name + `_update AFTER UPDATE OF ` + strings.Join(def.Update, ", ") +
				` ON ` + name + ` WHEN ` + changed(def.Update) + ` BEGIN ` + bump(changed(def.Update)) + ` END`
		}
	}
	for name, key := range keys {
		def := s.Defines[name]
		rows, gone, scope := "", "", def.Update
		if def.Rows {
			rows, gone, scope = "1", "1", union(scope, key)
			if def.When != nil {
				rows, gone = def.When("NEW"), def.When("OLD")
			}
		}
		stampNew := `UPDATE ` + name + ` SET sync_version = (SELECT version FROM sync_clock) WHERE rowid = NEW.rowid;`
		clearNew := `DELETE FROM sync_tombstone WHERE ` + match(name, "NEW", key) + `;`
		clearOld := `DELETE FROM sync_tombstone WHERE ` + match(name, "OLD", key)
		bury := `INSERT INTO sync_tombstone (tbl, k1, k2, k3, version, at) SELECT '` + name + `', ` + parts("OLD", key) +
			`, (SELECT version FROM sync_clock), CAST(strftime('%s', 'now') AS INTEGER) * 1000`
		var scopeWhen string
		if len(scope) > 0 {
			scopeWhen = changed(scope)
			if def.When != nil {
				scopeWhen = `(` + scopeWhen + `) AND (` + def.When("OLD") + ` OR ` + def.When("NEW") + `)`
			}
		}
		out["sync_"+name+"_insert"] = `CREATE TRIGGER sync_` + name + `_insert AFTER INSERT ON ` + name + ` BEGIN ` +
			bump(rows) + ` ` + stampNew + ` ` + clearNew + ` END`
		out["sync_"+name+"_update"] = `CREATE TRIGGER sync_` + name + `_update AFTER UPDATE OF ` + strings.Join(watch[name], ", ") +
			` ON ` + name + ` WHEN ` + changed(watch[name]) + ` BEGIN ` + bump(scopeWhen) + ` ` + stampNew + ` ` +
			clearOld + ` AND (` + changed(key) + `); ` + bury + ` WHERE ` + changed(key) + `; ` + clearNew + ` END`
		out["sync_"+name+"_delete"] = `CREATE TRIGGER sync_` + name + `_delete AFTER DELETE ON ` + name + ` BEGIN ` +
			bump(gone) + ` ` + clearOld + `; ` + bury + `; END`
	}
	return out
}

func union(a, b []string) []string {
	for _, c := range b {
		if !slices.Contains(a, c) {
			a = append(a, c)
		}
	}
	return a
}

// changed is whether any of cols differs between OLD and NEW. BINARY, since
// a NOCASE column would compare a change of case as no change.
func changed(cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = `OLD.` + c + ` IS NOT NEW.` + c + ` COLLATE BINARY`
	}
	return strings.Join(parts, " OR ")
}

// parts is row's key as k1 to k3, the unused parts 0. The unary + strips a
// column's affinity: compared with an INTEGER column, k1 to k3 would take
// its affinity and lose their index, a scan of every tombstone per write.
func parts(row string, key []string) string {
	out := []string{"0", "0", "0"}
	for i, c := range key {
		out[i] = `+` + row + `.` + c
	}
	return strings.Join(out, ", ")
}

func match(tbl, row string, key []string) string {
	p := strings.Split(parts(row, key), ", ")
	return `tbl = '` + tbl + `' AND k1 = ` + p[0] + ` AND k2 = ` + p[1] + ` AND k3 = ` + p[2]
}

// Migrate applies every pending migration and installs V1's triggers,
// release being the server's version. Migrations run with no sync trigger,
// so one may drop or rebuild a synced column, and the rows they write are
// not stamped: after any, after a new release and whenever the installed
// triggers differ from V1's, every client snapshots.
func Migrate(d *sql.DB, release string) error {
	var applied int
	err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name = 'schema_migration'`).Scan(&applied)
	if err != nil {
		return err
	}
	if applied > 0 {
		if applied, err = db.Version(d); err != nil {
			return err
		}
	}
	if applied < db.LatestVersion() {
		if err := db.InTx(d, func(tx db.DBTX) error { return drop(tx) }); err != nil {
			return err
		}
	}
	if err := db.Migrate(d); err != nil {
		return err
	}
	want := V1.Triggers()
	return db.InTx(d, func(tx db.DBTX) error {
		have, err := installed(tx)
		if err != nil {
			return err
		}
		var was string
		if err := tx.QueryRow(`SELECT release FROM sync_clock`).Scan(&was); err != nil {
			return err
		}
		same := len(have) == len(want)
		for name, q := range want {
			same = same && have[name] == q
		}
		if !same {
			if err := drop(tx); err != nil {
				return err
			}
			for _, q := range want {
				if _, err := tx.Exec(q); err != nil {
					return err
				}
			}
		}
		if same && was == release {
			return nil
		}
		_, err = tx.Exec(`UPDATE sync_clock SET version = version + 1, scope_version = version + 1, release = ?`, release)
		return err
	})
}

func installed(d db.DBTX) (map[string]string, error) {
	rows, err := d.Query(`SELECT name, sql FROM sqlite_schema WHERE type = 'trigger' AND name LIKE 'sync\_%' ESCAPE '\'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var name, q string
		if err := rows.Scan(&name, &q); err != nil {
			return nil, err
		}
		out[name] = q
	}
	return out, rows.Err()
}

func drop(d db.DBTX) error {
	have, err := installed(d)
	if err != nil {
		return err
	}
	for name := range have {
		if _, err := d.Exec(`DROP TRIGGER ` + name); err != nil {
			return err
		}
	}
	return nil
}
