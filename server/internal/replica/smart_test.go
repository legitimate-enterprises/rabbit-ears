package replica_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// smartLists is the smart_list table's part of what a pass read, sorted.
func smartLists(got []string) []string {
	var out []string
	for _, g := range got {
		if strings.HasPrefix(g, "smart_list:") {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

// Which playlists and collections are smart reaches the households that
// hold them and no other; the rules reach none, and editing them changes
// no synced row.
func TestASmartListReachesWhoHoldsIt(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v, _ := clock(t, d)
	mustExec(t, d, `INSERT INTO smart_list (id, playlist_id, match, created_at, modified_at) VALUES (uid(901), uid(501), 'all', 5, 5),
		(uid(902), uid(511), 'any', 6, 6)`)
	mustExec(t, d, `INSERT INTO smart_list (id, collection_id, match, created_at, modified_at) VALUES (uid(903), uid(401), 'all', 7, 7),
		(uid(904), uid(402), 'all', 8, 8)`)
	mustExec(t, d, `INSERT INTO smart_group (id, list_id, position, match, created_at, modified_at) VALUES (uid(911), uid(901), 0, 'any', 5, 5)`)
	mustExec(t, d, `INSERT INTO smart_condition (id, group_id, position, field, operator, text_value, created_at, modified_at)
		VALUES (uid(921), uid(911), 0, 'title', 'contains', 'x', 5, 5)`)
	got, at := pass(t, s, uid.N(201), v, 1000, nil)
	want := []string{
		"smart_list:[" + u(901) + "]=[" + u(901) + " " + u(501) + " <nil> 5]",
		"smart_list:[" + u(903) + "]=[" + u(903) + " <nil> " + u(401) + " 7]",
	}
	if f := smartLists(got); !slices.Equal(f, want) {
		t.Errorf("household 201 smart lists\n got %v\nwant %v", f, want)
	}
	for _, g := range got {
		if strings.HasPrefix(g, "smart_group:") || strings.HasPrefix(g, "smart_condition:") {
			t.Errorf("rules synced: %s", g)
		}
	}
	r, _ := snapshotOf(t, s, uid.N(201))
	if got := rowsOf(t, r, `SELECT id FROM smart_list ORDER BY id`); !slices.Equal(got, []string{row(uid.N(901)), row(uid.N(903))}) {
		t.Errorf("snapshot smart lists %v", got)
	}

	mustExec(t, d, `UPDATE smart_list SET match = 'any', modified_at = 9 WHERE id = uid(901)`)
	if f := smartLists(first(pass(t, s, uid.N(201), at, 1000, nil))); len(f) != 0 {
		t.Errorf("a rule edit synced %v", f)
	}
	mustExec(t, d, `DELETE FROM collection WHERE id = uid(401)`)
	if f := smartLists(first(pass(t, s, uid.N(201), at, 1000, nil))); !slices.Equal(f, []string{"smart_list:[" + u(903) + "]=deleted"}) {
		t.Errorf("after the collection went %v", f)
	}
}

func first[A, B any](a A, _ B) A { return a }
