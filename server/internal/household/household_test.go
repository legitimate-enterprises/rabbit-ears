package household_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
)

func TestCRUDAndCascade(t *testing.T) {
	d := dbtest.Open(t)
	ar, r := account.New(d), household.New(d)
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A", PasswordHash: "x"}
	_ = ar.Create(a)
	h := &household.Household{Name: "Home", AdultID: a.ID}
	if err := r.Create(h); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&household.Household{Name: "Second", AdultID: a.ID}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("one household per adult: %v", err)
	}
	if err := r.Create(&household.Household{Name: "x", AdultID: 999}); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
	if g, err := r.Get(h.ID); err != nil || g.ID != h.ID || g.CreatedAt.IsZero() {
		t.Fatalf("%+v %v", g, err)
	}
	h.Name = "Renamed"
	if err := r.Update(h); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(h.ID); g.Name != "Renamed" {
		t.Fatal(g.Name)
	}
	if l, _ := r.List(); len(l) != 1 {
		t.Fatal(len(l))
	}
	k := &account.Account{Kind: account.Kid, Name: "k", HouseholdID: &h.ID}
	_ = ar.Create(k)
	if err := ar.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(h.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("household should cascade from its adult")
	}
	if _, err := ar.Get(k.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("kids should cascade from the household")
	}
	if err := r.Delete(h.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}
