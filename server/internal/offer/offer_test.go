package offer_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/offer"
)

func TestOffer(t *testing.T) {
	d := dbtest.Open(t)
	var ids []int64
	for _, name := range []string{"a", "b", "kid", "other"} {
		a := &account.Account{Kind: account.Adult, Email: name + "@example.test", Name: name}
		if err := account.New(d).Create(a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	from, to, kid, other := ids[0], ids[1], ids[2], ids[3]
	h := &household.Household{Name: "Home", AdultID: from}
	if err := household.New(d).Create(h); err != nil {
		t.Fatal(err)
	}
	r := offer.New(d)
	o := &offer.Offer{ProfileID: kid, HouseholdID: h.ID, FromID: from, ToID: to}
	if err := r.Create(o); err != nil || o.ID == 0 {
		t.Fatal(o, err)
	}
	if err := r.Create(&offer.Offer{ProfileID: kid, HouseholdID: h.ID, FromID: from, ToID: to}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("one offer of a profile to one adult: %v", err)
	}
	if err := r.Create(&offer.Offer{ProfileID: kid, HouseholdID: 999, FromID: from, ToID: from}); !errors.Is(err, db.ErrReference) {
		t.Fatalf("an unknown household: %v", err)
	}
	if err := r.Create(&offer.Offer{ProfileID: from, HouseholdID: h.ID, FromID: from, ToID: to}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&offer.Offer{ProfileID: kid, HouseholdID: h.ID, FromID: from, ToID: other}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Get(o.ID); err != nil || *got != *o {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := r.To(to); err != nil || len(got) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	if got, err := r.From(h.ID); err != nil || len(got) != 3 {
		t.Fatalf("%+v %v", got, err)
	}
	if err := r.DeleteBetween(to, from); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.From(h.ID); len(got) != 1 || got[0].ToID != other {
		t.Fatalf("only the offers between the two go: %+v", got)
	}
	if err := r.DeleteOf(kid, 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.From(h.ID); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if err := r.Delete(o.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted already: %v", err)
	}
}
