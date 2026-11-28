package mediafile_test

import (
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Absorbing a second record of a show keeps every favorite of it and of its
// seasons: on the show kept, on the twin season a season folds into, and on
// a season that moves over whole; a favorite already there wins.
func TestAbsorbKeepsFavorites(t *testing.T) {
	d := dbtest.Open(t)
	l := &library.Library{Name: "tv", Kind: library.TV}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	items := item.New(d)
	add := func(parent *uid.ID, kind string, season int) uid.ID {
		it := &item.Item{LibraryID: l.ID, ParentID: parent, Kind: kind, ParsedTitle: "Lantern"}
		if kind == item.Season {
			it.SeasonNumber = &season
		}
		if err := items.Create(it); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	into, from := add(nil, item.Show, 0), add(nil, item.Show, 0)
	kept := add(&into, item.Season, 1)
	twin, moved := add(&from, item.Season, 1), add(&from, item.Season, 2)
	one, two := uid.N(1), uid.N(2)
	if _, err := d.Exec(`INSERT INTO account (id, kind, name, created_at, modified_at) VALUES (?1,'adult','A',0,0), (?2,'adult','B',0,0)`,
		one, two); err != nil {
		t.Fatal(err)
	}
	for _, f := range []struct {
		account, item uid.ID
		at            int
	}{{one, from, 1}, {one, twin, 2}, {one, moved, 3}, {two, into, 4}, {two, from, 5}} {
		if _, err := d.Exec(`INSERT INTO favorite (account_id, item_id, created_at) VALUES (?,?,?)`, f.account, f.item, f.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := mediafile.New(d).Absorb(into, from); err != nil {
		t.Fatal(err)
	}
	want := map[[2]uid.ID]int{{one, into}: 1, {one, kept}: 2, {one, moved}: 3, {two, into}: 4}
	rows, err := d.Query(`SELECT account_id, item_id, created_at FROM favorite`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	got := map[[2]uid.ID]int{}
	for rows.Next() {
		var a, it uid.ID
		var at int
		if err := rows.Scan(&a, &it, &at); err != nil {
			t.Fatal(err)
		}
		got[[2]uid.ID{a, it}] = at
	}
	if len(got) != len(want) {
		t.Fatalf("favorites %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("favorite %v at %d, want %d", k, got[k], v)
		}
	}
}
