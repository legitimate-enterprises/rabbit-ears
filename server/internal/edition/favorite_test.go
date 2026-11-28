package edition_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// A fold keeps every profile's favorite of either movie on the movie kept,
// the one already there winning.
func TestFoldKeepsFavorites(t *testing.T) {
	x := setup(t)
	a, b := x.movie(), x.movie()
	one, two := uid.N(1), uid.N(2)
	for _, q := range []string{
		`INSERT INTO account (id, kind, name, created_at, modified_at) VALUES (?1,'adult','A',0,0), (?2,'adult','B',0,0)`,
		`INSERT INTO favorite (account_id, item_id, created_at) VALUES (?1,?4,5), (?2,?3,1), (?2,?4,9)`,
	} {
		if _, err := x.d.Exec(q, one, two, a, b); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.eds.Fold(a, b); err != nil {
		t.Fatal(err)
	}
	rows, err := x.d.Query(`SELECT account_id, item_id, created_at FROM favorite ORDER BY account_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	type fav struct {
		account, item uid.ID
		at            int64
	}
	var got []fav
	for rows.Next() {
		var f fav
		if err := rows.Scan(&f.account, &f.item, &f.at); err != nil {
			t.Fatal(err)
		}
		got = append(got, f)
	}
	if len(got) != 2 || got[0] != (fav{one, a, 5}) || got[1] != (fav{two, a, 1}) {
		t.Fatalf("favorites %+v", got)
	}
}
