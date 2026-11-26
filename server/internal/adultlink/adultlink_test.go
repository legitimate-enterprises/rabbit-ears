package adultlink_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/adultlink"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
)

func TestAdultLink(t *testing.T) {
	d := dbtest.Open(t)
	var ids []int64
	for _, email := range []string{"a@example.test", "b@example.test", "c@example.test"} {
		a := &account.Account{Kind: account.Adult, Email: email, Name: email}
		if err := account.New(d).Create(a); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, a.ID)
	}
	r := adultlink.New(d)
	l := &adultlink.Link{AID: ids[1], BID: ids[0]}
	if err := r.Create(l); err != nil || l.AID != ids[0] || l.BID != ids[1] {
		t.Fatalf("stored lower id first: %+v %v", l, err)
	}
	if err := r.Create(&adultlink.Link{AID: ids[0], BID: ids[1]}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("a pair is linked once: %v", err)
	}
	if err := r.Create(&adultlink.Link{AID: ids[0], BID: 999}); !errors.Is(err, db.ErrReference) {
		t.Fatalf("an unknown adult: %v", err)
	}
	if err := r.Create(&adultlink.Link{AID: ids[2], BID: ids[0], TV: true}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ids[1], ids[0])
	if err != nil || got.TV {
		t.Fatalf("either order finds it, off TVs by default: %+v %v", got, err)
	}
	got.TV = true
	if err := r.SetTV(got); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Get(ids[0], ids[1]); err != nil || !got.TV {
		t.Fatalf("%+v %v", got, err)
	}
	if all, err := r.List(10, 0); err != nil || len(all) != 2 {
		t.Fatalf("%+v %v", all, err)
	}
	if err := r.Delete(ids[1], ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ids[0], ids[1]); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if err := r.Delete(ids[1], ids[0]); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
