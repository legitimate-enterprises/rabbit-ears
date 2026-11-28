package replica_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// favorites is the favorite table's part of what a pass read, sorted.
func favorites(got []string) []string {
	var out []string
	for _, g := range got {
		if strings.HasPrefix(g, "favorite:") {
			out = append(out, g)
		}
	}
	slices.Sort(out)
	return out
}

// A profile's favorites reach its own household's replica alone, and so does
// their deletion when the item goes.
func TestAFavoriteReachesItsHouseholdAlone(t *testing.T) {
	d := twoHouseholds(t)
	s := syncOf(t, d)
	v, _ := clock(t, d)
	mustExec(t, d, `INSERT INTO favorite (account_id, item_id, created_at) VALUES (uid(103), uid(1), 5), (uid(112), uid(1), 7)`)
	got, at := pass(t, s, uid.N(201), v, 1000, nil)
	if f := favorites(got); !slices.Equal(f, []string{"favorite:[" + u(103) + " " + u(1) + "]=[" + u(103) + " " + u(1) + " 5]"}) {
		t.Errorf("household 201 favorites %v", f)
	}
	theirs, theirsAt := pass(t, s, uid.N(202), v, 1000, nil)
	if f := favorites(theirs); !slices.Equal(f, []string{"favorite:[" + u(112) + " " + u(1) + "]=[" + u(112) + " " + u(1) + " 7]"}) {
		t.Errorf("household 202 favorites %v", f)
	}
	if n := count(t, d, `SELECT count(*) FROM favorite`); n != 2 {
		t.Fatalf("%d favorites", n)
	}
	r, _ := snapshotOf(t, s, uid.N(201))
	if got := rowsOf(t, r, `SELECT account_id, item_id, created_at FROM favorite`); !slices.Equal(got, []string{row(uid.N(103), uid.N(1), 5)}) {
		t.Errorf("snapshot favorites %v", got)
	}

	mustExec(t, d, `DELETE FROM item WHERE id = uid(1)`)
	gone, _ := pass(t, s, uid.N(201), at, 1000, nil)
	if f := favorites(gone); !slices.Equal(f, []string{"favorite:[" + u(103) + " " + u(1) + "]=deleted"}) {
		t.Errorf("household 201 tombstones %v", f)
	}
	theirsGone, _ := pass(t, s, uid.N(202), theirsAt, 1000, nil)
	if f := favorites(theirsGone); !slices.Equal(f, []string{"favorite:[" + u(112) + " " + u(1) + "]=deleted"}) {
		t.Errorf("household 202 tombstones %v", f)
	}
}
