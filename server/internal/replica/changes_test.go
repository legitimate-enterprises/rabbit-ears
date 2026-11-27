package replica_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/replica"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func changes(t *testing.T, s *replica.Sync, household uid.ID, since *int64, cursor string, limit int) replica.Page {
	t.Helper()
	p, err := s.Changes(context.Background(), household, since, cursor, limit)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// pass reads every page of a pass from since, limit at a time, between
// pages calling between, and returns what it read as table:key=row and
// table:key=deleted, and the watermark. Every page but the last must report
// since, and no row may come twice.
func pass(t *testing.T, s *replica.Sync, household uid.ID, since int64, limit int, between func()) ([]string, int64) {
	t.Helper()
	var got []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > 1000 {
			t.Fatalf("a pass of %d pages: %v", pages, got)
		}
		p := changes(t, s, household, &since, cursor, limit)
		if p.Mode != replica.Diff {
			t.Fatalf("mode %s", p.Mode)
		}
		n := 0
		for _, tb := range p.Tables {
			for _, r := range tb.Rows {
				got = append(got, fmt.Sprintf("%s:%v=%v", tb.Name, keyOf(t, tb, r), r))
				n++
			}
		}
		for _, k := range p.Tombstones {
			for _, key := range k.Keys {
				got = append(got, fmt.Sprintf("%s:%v=deleted", k.Table, key))
				n++
			}
		}
		if n > limit {
			t.Fatalf("%d on a page of %d", n, limit)
		}
		if p.Cursor == "" {
			seen := map[string]bool{}
			for _, g := range got {
				k := g[:slices.Index([]byte(g), '=')]
				if seen[k] {
					t.Errorf("%s came twice in a pass: %v", k, got)
				}
				seen[k] = true
			}
			return got, p.Version
		}
		if p.Version != since {
			t.Fatalf("a page with more to come reports %d, since %d", p.Version, since)
		}
		cursor = p.Cursor
		if between != nil {
			between()
		}
	}
}

// keyOf is row's key, its key columns in tb's key order.
func keyOf(t *testing.T, tb replica.Rows, r []any) []any {
	t.Helper()
	var def replica.Table
	for _, x := range replica.V1.Tables {
		if x.Name == tb.Name {
			def = x
		}
	}
	var out []any
	for _, k := range def.Key {
		i := slices.Index(tb.Columns, k)
		if i < 0 {
			t.Fatalf("%s rows lack key column %s", tb.Name, k)
		}
		out = append(out, r[i])
	}
	return out
}

func TestChangesSnapshotWhen(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v, _ := clock(t, d)
	if p := changes(t, s, uid.N(201), nil, "", 100); p.Mode != replica.Snapshot {
		t.Errorf("with nothing held: %s", p.Mode)
	}
	if p := changes(t, s, uid.N(201), &v, "", 100); p.Mode != replica.Diff || p.Version != v {
		t.Errorf("with everything held: %s at %d", p.Mode, p.Version)
	}
	ahead := v + 1
	if p := changes(t, s, uid.N(201), &ahead, "", 100); p.Mode != replica.Snapshot {
		t.Errorf("ahead of the server: %s", p.Mode)
	}
	mustExec(t, d, `UPDATE sync_clock SET purged_version = ?`, v)
	before := v - 1
	if p := changes(t, s, uid.N(201), &before, "", 100); p.Mode != replica.Snapshot {
		t.Errorf("behind the purge: %s", p.Mode)
	}
	if p := changes(t, s, uid.N(201), &v, "", 100); p.Mode != replica.Diff {
		t.Errorf("at the purge: %s", p.Mode)
	}
	// Another household's membership changes which rows some household
	// holds, and is told to every household.
	mustExec(t, d, `INSERT INTO household_member (household_id, account_id, created_at) VALUES (uid(202), uid(101), 0)`)
	if p := changes(t, s, uid.N(201), &v, "", 100); p.Mode != replica.Snapshot {
		t.Errorf("after a scope change: %s", p.Mode)
	}
	if p := changes(t, s, uid.N(201), &v, "1.2.3", 100); p.Mode != replica.Snapshot {
		t.Errorf("after a scope change, on a later page: %s", p.Mode)
	}
}

// More rows changed than half the replica holds is a snapshot, on a
// pass's first page; a pass already begun goes on.
func TestChangesSnapshotWhenMostChanged(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v, _ := clock(t, d)
	mustExec(t, d, `UPDATE item SET parsed_title = parsed_title || '!' WHERE id <= uid(20)`)
	first := changes(t, s, uid.N(201), &v, "", 1)
	if first.Mode != replica.Diff || first.Cursor == "" {
		t.Fatalf("a few changes: %s, cursor %q", first.Mode, first.Cursor)
	}
	for _, q := range []string{`UPDATE item SET parsed_title = parsed_title || '!'`, `UPDATE track SET title = 'T' || hex(id)`,
		`UPDATE item_metadata SET title = 'T'`, `UPDATE playlist_entry SET added_at = 1`, `UPDATE edition SET tag = 'T' || hex(id)`,
		`UPDATE media_file SET size_bytes = 2`, `UPDATE account SET name = name || '!'`} {
		mustExec(t, d, q)
	}
	if p := changes(t, s, uid.N(201), &v, "", 1000); p.Mode != replica.Snapshot {
		t.Errorf("most changed: %s", p.Mode)
	}
	if p := changes(t, s, uid.N(201), &v, first.Cursor, 1000); p.Mode != replica.Diff {
		t.Errorf("most changed, on a later page: %s", p.Mode)
	}
}

// A pass sends each row the household holds that changed in its state when
// read, every deletion it may see, and nothing of another household.
func TestChangesPageAPass(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v, _ := clock(t, d)
	for _, q := range []string{
		`UPDATE item SET parsed_title = 'One' WHERE id = uid(1)`,
		`UPDATE item SET parsed_title = 'Two' WHERE id = uid(2)`,
		`INSERT INTO watch_state (account_id, item_id, position_ms, changed_at) VALUES (uid(103), uid(3), 30, 0)`,
		`DELETE FROM account_allow WHERE account_id = uid(106) AND item_id = uid(4)`,
		`UPDATE playlist SET name = 'Remix' WHERE id = uid(501)`,
		`DELETE FROM item_genre`,
		`UPDATE watch_state SET position_ms = 6 WHERE account_id = uid(112)`,
		`DELETE FROM account_allow WHERE account_id = uid(112)`,
		`UPDATE playlist SET name = 'Hidden' WHERE id = uid(511)`,
		`DELETE FROM collection_movie WHERE collection_id = uid(402)`,
		`UPDATE account SET name = 'Yo' WHERE id = uid(112)`,
	} {
		mustExec(t, d, q)
	}
	want := []string{
		"item:[" + u(1) + "]", "item:[" + u(2) + "]", "watch_state:[" + u(103) + " " + u(3) + "]", "playlist:[" + u(501) + "]",
		"account_allow:[" + u(106) + " " + u(4) + "]=deleted", "item_genre:[" + u(1) + " " + u(1) + " local]=deleted",
	}
	now, _ := clock(t, d)
	var to int64
	for _, limit := range []int{1, 2, 5, 1000} {
		var got []string
		got, to = pass(t, s, uid.N(201), v, limit, nil)
		if to != now {
			t.Errorf("limit %d: watermark %d, clock %d", limit, to, now)
		}
		if len(got) != len(want) {
			t.Fatalf("limit %d: got %v", limit, got)
		}
		for _, w := range want {
			if !slices.ContainsFunc(got, func(g string) bool { return len(g) >= len(w) && g[:len(w)] == w }) {
				t.Errorf("limit %d: missing %s in %v", limit, w, got)
			}
		}
	}
	if again, at := pass(t, s, uid.N(201), to, 2, nil); len(again) != 0 || at != to {
		t.Errorf("from the watermark: %v at %d", again, at)
	}
}

// A row changed while a pass pages is sent in its newest state by the next
// pass, never twice in one, and the watermark misses nothing.
func TestCursorResumesAcrossAConcurrentUpdate(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v, _ := clock(t, d)
	mustExec(t, d, `UPDATE item SET parsed_title = 'One' WHERE id = uid(1)`)
	mustExec(t, d, `UPDATE item SET parsed_title = 'Two' WHERE id = uid(2)`)
	mustExec(t, d, `UPDATE playlist SET name = 'Remix' WHERE id = uid(501)`)
	once := false
	got, to := pass(t, s, uid.N(201), v, 1, func() {
		if !once {
			mustExec(t, d, `UPDATE item SET parsed_title = 'Uno' WHERE id = uid(1)`)
			mustExec(t, d, `UPDATE playlist SET name = 'Again' WHERE id = uid(501)`)
			mustExec(t, d, `DELETE FROM watch_state WHERE account_id = uid(103)`)
			once = true
		}
	})
	if len(got) != 2 || !slices.ContainsFunc(got, func(g string) bool { return strings.HasPrefix(g, "item:["+u(2)+"]") }) {
		t.Errorf("first pass %v", got)
	}
	next, _ := pass(t, s, uid.N(201), to, 1, nil)
	slices.Sort(next)
	if len(next) != 3 ||
		next[0] != "item:["+u(1)+"]=["+u(1)+" "+u(1)+" <nil> movie Uno <nil> <nil> <nil> <nil> <nil> <nil> Uno 0 <nil> <nil>]" ||
		next[1] != "playlist:["+u(501)+"]=["+u(501)+" "+u(101)+" Again movies <nil> 0 0 <nil> <nil> 0]" ||
		next[2] != "watch_state:["+u(103)+" "+u(2)+"]=deleted" {
		t.Errorf("next pass %v", next)
	}
}

// A deletion reaches every replica table built from the deleted row's
// table, with its key's parts in key order and of their own types, and
// only the households that may see it.
func TestTombstonesReachTheirTables(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	mustExec(t, d, `INSERT INTO watch_state (account_id, item_id, changed_at) VALUES (uid(105), uid(1), 0)`)
	v, _ := clock(t, d)
	// The profile's own rows go with it, though it is no member any more.
	mustExec(t, d, `DELETE FROM account WHERE id = uid(105)`)
	mustExec(t, d, `DELETE FROM item_genre`)
	mustExec(t, d, `DELETE FROM watch_state`)
	mustExec(t, d, `DELETE FROM occasion_phrase`)
	mustExec(t, d, `INSERT INTO occasion_phrase (occasion_id, phrase) VALUES (uid(301), 'Let It Snow')`)
	mustExec(t, d, `DELETE FROM occasion_phrase`)
	p := changes(t, s, uid.N(201), &v, "", 100)
	if p.Mode != replica.Diff {
		t.Fatalf("mode %s", p.Mode)
	}
	got := map[string][][]any{}
	for _, k := range p.Tombstones {
		got[k.Table] = k.Keys
	}
	for table, want := range map[string][][]any{
		"account":         {{u(105)}},
		"account_card":    {{u(105)}},
		"item_genre":      {{u(1), u(1), "local"}},
		"watch_state":     {{u(105), u(1)}, {u(103), u(2)}},
		"occasion_phrase": {{u(301), "Let It Snow"}},
	} {
		if fmt.Sprintf("%#v", got[table]) != fmt.Sprintf("%#v", want) {
			t.Errorf("%s tombstones %#v, want %#v", table, got[table], want)
		}
	}
	if theirs := changes(t, s, uid.N(202), &v, "", 100); !slices.ContainsFunc(theirs.Tombstones, func(k replica.Keys) bool {
		return k.Table == "watch_state" && fmt.Sprint(k.Keys) == "[["+u(105)+" "+u(1)+"] ["+u(112)+" "+u(1)+"]]"
	}) {
		t.Errorf("household 202 tombstones %v", theirs.Tombstones)
	}
}

// Changed is whether the household has anything to read since a version:
// a change it sees or a snapshot to take; never another household's.
func TestChanged(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	ctx := context.Background()
	at := func() int64 { v, _ := clock(t, d); return v }
	changed := func(after int64) bool {
		t.Helper()
		ok, err := s.Changed(ctx, uid.N(201), after)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	v := at()
	if changed(v) {
		t.Error("changed with nothing new")
	}
	for _, q := range []string{
		`UPDATE watch_state SET position_ms = 6 WHERE account_id = uid(112)`,
		`DELETE FROM watch_state WHERE account_id = uid(112)`,
		`UPDATE playlist SET name = 'Hidden' WHERE id = uid(511)`,
		`INSERT INTO playlist_entry (id, playlist_id, item_id, rank, added_at, created_at, modified_at) VALUES (uid(625), uid(511), uid(2), 'a1', 0, 0, 0)`,
		`DELETE FROM station_day WHERE station_id = uid(701)`,
		`DELETE FROM collection_movie WHERE collection_id = uid(402)`,
	} {
		mustExec(t, d, q)
		if changed(v) {
			t.Errorf("another household's %s rang", q)
		}
	}
	if got, err := s.Version(ctx); err != nil || got != at() {
		t.Errorf("version %d, %v", got, err)
	}
	v = at()
	mustExec(t, d, `UPDATE watch_state SET position_ms = 6 WHERE account_id = uid(103)`)
	if !changed(v) {
		t.Error("a change the household sees did not ring")
	}
	v = at()
	mustExec(t, d, `DELETE FROM station_day WHERE station_id = uid(702)`)
	if !changed(v) {
		t.Error("a deletion the household sees did not ring")
	}
	v = at()
	mustExec(t, d, `INSERT INTO household_member (household_id, account_id, created_at) VALUES (uid(202), uid(101), 0)`)
	if !changed(v) {
		t.Error("a scope change did not ring")
	}
}

func TestBadCursor(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	// Five changes past the fixture's last to scope, a window to diff.
	mustExec(t, d, `UPDATE item SET parsed_title = parsed_title || '!' WHERE id <= uid(5)`)
	v, _ := clock(t, d)
	since := v - 5
	for _, c := range []string{"x", "1.2", "a.b.c", fmt.Sprintf("%d.0.%d", v+1, v), fmt.Sprintf("%d.99.%d", v, v),
		fmt.Sprintf("%d.0.%d", v, since-1), fmt.Sprintf("%d.0.%d", since-1, since), fmt.Sprintf("%d.-1.%d", v, v)} {
		if _, err := s.Changes(context.Background(), uid.N(201), &since, c, 10); !errors.Is(err, replica.ErrCursor) {
			t.Errorf("cursor %q: %v", c, err)
		}
	}
}
