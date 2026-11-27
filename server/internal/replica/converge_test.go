package replica_test

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
)

// copyOf is a replica as the minimal client below keeps it: per table, rows
// by key.
type copyOf map[string]map[string]string

// pull brings household's c up to date as a client does, from since, nil
// for nothing held: page by page of a pass, or a snapshot when told to take
// one. It returns the version c then holds.
func pull(ctx context.Context, s *replica.Sync, household int64, c copyOf, since *int64) (*int64, error) {
	cursor := ""
	for {
		p, err := s.Changes(ctx, household, since, cursor, 7)
		if err != nil {
			return nil, err
		}
		if p.Mode == replica.Snapshot {
			f, err := s.Snapshot(ctx, household)
			if err != nil {
				return nil, err
			}
			_ = f.Close()
			if err := load(ctx, f.Path, c); err != nil {
				return nil, err
			}
			since, cursor = &f.Version, ""
			continue
		}
		for _, tb := range p.Tables {
			key := keyColumns(tb.Name)
			for _, r := range tb.Rows {
				if c[tb.Name] == nil {
					c[tb.Name] = map[string]string{}
				}
				k := make([]any, len(key))
				for i, n := range key {
					k[i] = r[slices.Index(tb.Columns, n)]
				}
				c[tb.Name][fmt.Sprint(k)] = fmt.Sprint(r)
			}
		}
		for _, k := range p.Tombstones {
			for _, key := range k.Keys {
				delete(c[k.Table], fmt.Sprint(key))
			}
		}
		if p.Cursor == "" {
			return &p.Version, nil
		}
		cursor = p.Cursor
	}
}

func keyColumns(table string) []string {
	for _, tb := range replica.V1.Tables {
		if tb.Name == table {
			return tb.Key
		}
	}
	return nil
}

// load replaces c with the replica in the snapshot at path.
func load(ctx context.Context, path string, c copyOf) error {
	r, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	clear(c)
	for _, tb := range replica.V1.Tables {
		rows, err := r.QueryContext(ctx, `SELECT * FROM `+tb.Name) //nolint:gosec // V1's own names
		if err != nil {
			return err
		}
		c[tb.Name] = map[string]string{}
		for rows.Next() {
			v := make([]any, len(tb.Cols))
			p := make([]any, len(v))
			for i := range v {
				p[i] = &v[i]
			}
			if err := rows.Scan(p...); err != nil {
				_ = rows.Close()
				return err
			}
			k := make([]any, len(tb.Key))
			for i, n := range tb.Key {
				k[i] = v[slices.IndexFunc(tb.Cols, func(c replica.Col) bool { return c.Name == n })]
			}
			c[tb.Name][fmt.Sprint(k)] = fmt.Sprint(v)
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

// A client that pulls while others write, and once more after they stop,
// holds exactly what a fresh snapshot holds: no change is stamped at or
// below a version a pull already reported, and no page misses one.
func TestConcurrentWritersConverge(t *testing.T) {
	if testing.Short() {
		t.Skip("writes for seconds")
	}
	d := seeded(t)
	ctx := context.Background()
	mustExec(t, d, `INSERT INTO playlist (id, account_id, name, kind, created_at, updated_at) VALUES (1, 1, 'P', 'movies', 0, 0)`)
	writes := []string{
		`INSERT INTO watch_state (account_id, item_id, position_ms, updated_at) VALUES (?1, ?2, ?3, 0)
			ON CONFLICT DO UPDATE SET position_ms = excluded.position_ms`,
		`DELETE FROM watch_state WHERE account_id = ?1 AND item_id = ?2`,
		`INSERT OR IGNORE INTO account_allow (account_id, item_id) VALUES (?1, ?2)`,
		`DELETE FROM account_allow WHERE account_id = ?1 AND item_id = ?2`,
		`INSERT INTO playlist_entry (playlist_id, item_id, rank, added_at) VALUES (1, ?2, 'a' || ?3, 0)`,
		`UPDATE playlist_entry SET rank = 'a' || ?3 WHERE id = (SELECT max(id) FROM playlist_entry WHERE item_id = ?2)`,
		`DELETE FROM playlist_entry WHERE id = (SELECT min(id) FROM playlist_entry WHERE item_id = ?2)`,
		`UPDATE item SET kids_content = ?3 % 2 WHERE id = ?2`,
	}
	var wg sync.WaitGroup
	errs := make(chan error, 9)
	done := make(chan struct{})
	for w := range 8 {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(w), 1)) //nolint:gosec // a repeatable sequence, not a secret
			for range 400 {
				tx, err := d.BeginTx(ctx, nil)
				if err != nil {
					errs <- err
					return
				}
				for range 1 + r.IntN(3) {
					if _, err := tx.ExecContext(ctx, writes[r.IntN(len(writes))], 1+r.IntN(2), 1+r.IntN(2), r.IntN(100)); err != nil {
						_ = tx.Rollback()
						errs <- err
						return
					}
				}
				if r.IntN(5) == 0 {
					err = tx.Rollback()
				} else {
					err = tx.Commit()
				}
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	s := syncOf(t, d)
	c := copyOf{}
	var since *int64
	var pulls int
	go func() { wg.Wait(); close(done) }()
	for running := true; running; pulls++ {
		select {
		case <-done:
			running = false
		default:
		}
		var err error
		if since, err = pull(ctx, s, 1, c, since); err != nil {
			t.Fatal(err)
		}
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	converged(t, d, 1, c, since, fmt.Sprintf("after %d pulls", pulls))
}

// converged fails unless household's c, at since, holds what a fresh
// snapshot of d holds.
func converged(t *testing.T, d *sql.DB, household int64, c copyOf, since *int64, when string) {
	t.Helper()
	f, err := syncOf(t, d).Snapshot(context.Background(), household)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	want := copyOf{}
	if err := load(context.Background(), f.Path, want); err != nil {
		t.Fatal(err)
	}
	for _, tb := range replica.V1.Tables {
		if got := c[tb.Name]; fmt.Sprint(got) != fmt.Sprint(want[tb.Name]) {
			t.Errorf("%s %s:\n got %v\nwant %v", tb.Name, when, got, want[tb.Name])
		}
	}
	if *since != f.Version {
		t.Errorf("%s holds %d, a fresh snapshot is at %d", when, *since, f.Version)
	}
}

// Household 201 holds a collection of 202's while a playlist that reaches
// it, aired by a station 201 sees or shared with 201, names it. As entries
// for it come and go, its movies change and the share ends, 201's client,
// pulling after each change, holds what a fresh snapshot holds.
func TestReachedCollectionsConverge(t *testing.T) {
	d := twoHouseholds(t)
	ctx := context.Background()
	s := syncOf(t, d)
	c := copyOf{}
	since, err := pull(ctx, s, 201, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		q         string
		has, lack []string // collections, as copyOf keys them
	}{
		{`SELECT 1`, []string{"[403]"}, []string{"[402]"}},
		{`INSERT INTO playlist_entry (id, playlist_id, collection_id, rank, added_at) VALUES (628, 512, 402, 'a3', 0)`, []string{"[402]"}, nil},
		{`INSERT INTO collection_movie (collection_id, item_id, position) VALUES (402, 5, 1)`, nil, nil},
		{`DELETE FROM collection_movie WHERE collection_id = 402 AND item_id = 1`, nil, nil},
		{`UPDATE collection SET name = 'Theirs too' WHERE id = 402`, nil, nil},
		{`DELETE FROM playlist_entry WHERE id = 628`, nil, []string{"[402]"}},
		{`UPDATE playlist_entry SET collection_id = 402 WHERE id = 627`, []string{"[402]"}, []string{"[403]"}},
		{`UPDATE playlist_entry SET collection_id = 403 WHERE id = 627`, []string{"[403]"}, []string{"[402]"}},
		{`INSERT INTO playlist_household_share (playlist_id, household_id, access, created_at) VALUES (515, 201, 'read', 0)`, nil, nil},
		{`INSERT INTO playlist_entry (id, playlist_id, collection_id, rank, added_at) VALUES (629, 515, 402, 'a1', 0)`, []string{"[402]"}, nil},
		{`INSERT INTO collection_movie (collection_id, item_id, position) VALUES (402, 2, 2)`, nil, nil},
		{`DELETE FROM playlist_household_share WHERE playlist_id = 515`, []string{"[403]"}, []string{"[402]"}},
		{`UPDATE station SET visibility = 'household' WHERE id = 703`, nil, []string{"[403]"}},
	} {
		mustExec(t, d, step.q)
		if since, err = pull(ctx, s, 201, c, since); err != nil {
			t.Fatal(err)
		}
		converged(t, d, 201, c, since, "after "+step.q)
		for _, k := range step.has {
			if c["collection"][k] == "" {
				t.Errorf("after %s 201 lacks collection %s", step.q, k)
			}
		}
		for _, k := range step.lack {
			if c["collection"][k] != "" {
				t.Errorf("after %s 201 holds collection %s", step.q, k)
			}
		}
	}
}

// Household 201 sees 202's server station 703 and what it plays. As it is
// narrowed, widened, edited, moved to another playlist and deleted, 201's
// client, pulling after each change, holds what a fresh snapshot holds.
func TestStationVisibilityConverges(t *testing.T) {
	d := twoHouseholds(t)
	ctx := context.Background()
	s := syncOf(t, d)
	c := copyOf{}
	since, err := pull(ctx, s, 201, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c["playlist"]["[512]"] == "" {
		t.Fatal("201 lacks the playlist of the server station it sees")
	}
	for _, q := range []string{
		`UPDATE station SET visibility = 'household' WHERE id = 703`,
		`UPDATE station SET visibility = 'server' WHERE id = 703`,
		`INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at) VALUES (625, 512, 3, 'a2', 0)`,
		`DELETE FROM playlist_entry WHERE id = 622`,
		`UPDATE station SET playlist_id = 511 WHERE id = 703`,
		`DELETE FROM station WHERE id = 703`,
	} {
		mustExec(t, d, q)
		if since, err = pull(ctx, s, 201, c, since); err != nil {
			t.Fatal(err)
		}
		converged(t, d, 201, c, since, "after "+q)
	}
	if c["playlist"]["[511]"] != "" || c["playlist"]["[512]"] != "" {
		t.Error("201 keeps the playlists of a station it no longer sees")
	}
}
