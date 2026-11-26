package person_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
)

func id64(n int64) *int64 { return &n }

func TestCRUDAndUpsert(t *testing.T) {
	r := person.New(dbtest.OpenPreferLocal(t))
	p := &person.Person{Name: "Keanu Reeves", TMDBID: id64(6384), ProfilePath: "/keanu.jpg"}
	if err := r.Create(p); err != nil || p.ID == 0 {
		t.Fatal(err)
	}
	if err := r.Create(&person.Person{Name: "Someone", TMDBID: id64(6384)}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("provider ids are unique: %v", err)
	}
	if err := r.Create(&person.Person{Name: "Keanu Reeves"}); err != nil {
		t.Fatalf("names need not be unique: %v", err)
	}
	again := &person.Person{Name: "K. Reeves", TMDBID: id64(6384), ProfilePath: "/new.jpg"}
	if err := r.Upsert(again); err != nil || again.ID != p.ID {
		t.Fatalf("upsert by provider id: %+v %v", again, err)
	}
	got, err := r.Get(p.ID)
	if err != nil || got.Name != "K. Reeves" || got.ProfilePath != "/new.jpg" || *got.TMDBID != 6384 {
		t.Fatalf("%+v %v", got, err)
	}
	got.ProfilePath, got.TMDBID = "", nil
	if err := r.Update(got); err != nil {
		t.Fatal(err)
	}
	if got, _ = r.Get(p.ID); got.ProfilePath != "" || got.TMDBID != nil {
		t.Fatalf("%+v", got)
	}
	if err := r.Delete(p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(p.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Update(got); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCreditsSearchAndAudience(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	libs, items, r := library.New(d), item.New(d), person.New(d)
	mv := &library.Library{Name: "mv", Kind: library.Movies}
	if err := libs.Create(mv); err != nil {
		t.Fatal(err)
	}
	movie := func(rating string) int64 {
		it := &item.Item{LibraryID: mv.ID, Kind: item.Movie, ParsedTitle: rating}
		if err := items.Create(it); err != nil {
			t.Fatal(err)
		}
		if err := items.SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, ContentRating: rating}); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	g, rated := movie("G"), movie("R")
	add := func(name string) int64 {
		p := &person.Person{Name: name}
		if err := r.Create(p); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	ann, bob, cy := add("Ann Lee"), add("Bob Annan"), add("Cy Young")
	if err := r.SetCredits(g, []person.Credit{
		{PersonID: bob, Kind: person.Cast, Character: "Hero"},
		{PersonID: ann, Kind: person.Crew, Job: "Director", Department: "Directing"},
		{PersonID: ann, Kind: person.Cast, Character: "Sidekick"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetCredits(rated, []person.Credit{{PersonID: cy, Kind: person.Cast, Character: "Villain"}}); err != nil {
		t.Fatal(err)
	}
	cs, err := r.Credits(g)
	if err != nil || len(cs) != 3 || cs[0].Name != "Bob Annan" || cs[0].Position != 0 || cs[1].Character != "Sidekick" || cs[1].Position != 1 ||
		cs[2].Kind != person.Crew || cs[2].Job != "Director" || cs[2].Position != 0 {
		t.Fatalf("cast in billing order, then crew: %+v %v", cs, err)
	}
	if err := r.SetCredits(g, []person.Credit{{PersonID: ann, Kind: person.Cast}}); err != nil {
		t.Fatal(err)
	}
	if cs, _ := r.Credits(g); len(cs) != 1 {
		t.Fatalf("replaced: %+v", cs)
	}
	if err := r.SetCredits(g, []person.Credit{{PersonID: 999, Kind: person.Cast}}); !errors.Is(err, db.ErrReference) {
		t.Fatalf("unknown person: %v", err)
	}
	if cs, _ := r.Credits(g); len(cs) != 1 {
		t.Fatal("a failed replace keeps the old credits")
	}

	if ps, _ := r.List(person.Filter{Q: "ANN"}); len(ps) != 2 || ps[0].Name != "Ann Lee" || ps[1].Name != "Bob Annan" {
		t.Fatalf("search ignores case: %+v", ps)
	}
	if ps, _ := r.List(person.Filter{Limit: 1, Offset: 2}); len(ps) != 1 || ps[0].Name != "Cy Young" {
		t.Fatalf("paging: %+v", ps)
	}
	kid := item.Audience{Kid: true, Movie: []string{"G"}}
	if ps, _ := r.List(person.Filter{Audience: kid}); len(ps) != 1 || ps[0].ID != ann {
		t.Fatalf("a kid sees people credited on visible items: %+v", ps)
	}
	if ok, _ := r.Seen(cy, kid); ok {
		t.Fatal("Cy is only in an R film")
	}
	if ok, _ := r.Seen(cy, item.Audience{}); !ok {
		t.Fatal("adults see everyone")
	}
	if err := r.Delete(ann); err != nil {
		t.Fatal(err)
	}
	if cs, _ := r.Credits(g); len(cs) != 0 {
		t.Fatal("credits cascade with the person")
	}
}
