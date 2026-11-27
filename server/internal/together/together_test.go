package together_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/together"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

type person struct{ account, device uid.ID }

func TestWatchTogether(t *testing.T) {
	d := dbtest.Open(t)
	owner := &account.Account{Kind: account.Adult, Email: "h@example.test", Name: "Heidi"}
	if err := account.New(d).Create(owner, nil); err != nil {
		t.Fatal(err)
	}
	h := &household.Household{Name: "Home", AdultID: owner.ID}
	if err := household.New(d).Create(h, nil); err != nil {
		t.Fatal(err)
	}
	other := &account.Account{Kind: account.Adult, Email: "c@example.test", Name: "Claire", HouseholdID: &h.ID}
	if err := account.New(d).Create(other, nil); err != nil {
		t.Fatal(err)
	}
	var people []person
	for _, a := range []uid.ID{owner.ID, other.ID} {
		dev := &device.Device{TokenHash: "t" + a.String(), Name: "TV", Platform: "tvos", AccountID: a, HouseholdID: h.ID}
		if err := device.New(d).Create(dev); err != nil {
			t.Fatal(err)
		}
		people = append(people, person{a, dev.ID})
	}
	l := &library.Library{Name: "Movies", Kind: library.Movies}
	if err := library.New(d).Create(l, nil); err != nil {
		t.Fatal(err)
	}
	movie := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "Dune"}
	if err := item.New(d).Create(movie); err != nil {
		t.Fatal(err)
	}

	r := together.New(d)
	now := time.Now().UTC().Truncate(time.Millisecond)
	w := &together.Together{HouseholdID: h.ID, ItemID: movie.ID, PositionAt: now, OpenedAt: now, JoinBy: now.Add(together.JoinWindow)}
	host := &together.Member{AccountID: people[0].account, DeviceID: people[0].device, Role: together.Host, JoinedAt: now, LastSeenAt: now}
	if err := r.Create(w, host); err != nil || w.ID.IsZero() || w.Version != 1 {
		t.Fatalf("%+v %v", w, err)
	}
	if open, err := r.Open(&h.ID, &movie.ID); err != nil || len(open) != 1 || open[0].ID != w.ID {
		t.Fatalf("%v %v", open, err)
	}
	if open, _ := r.Open(&h.ID, nil); len(open) != 1 {
		t.Fatalf("any item: %v", open)
	}
	if open, _ := r.Open(nil, nil); len(open) != 1 {
		t.Fatalf("any household: %v", open)
	}
	if open, _ := r.Open(new(uid.N(999)), nil); len(open) != 0 {
		t.Fatalf("another household: %v", open)
	}
	guest := &together.Member{WatchTogetherID: w.ID, AccountID: people[1].account, DeviceID: people[1].device, Role: together.Guest, JoinedAt: now, LastSeenAt: now}
	if err := r.AddMember(guest); err != nil {
		t.Fatal(err)
	}
	third := *guest
	third.AccountID = people[0].account
	if err := r.AddMember(&third); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("a second guest: %v", err)
	}
	later := now.Add(3 * time.Second)
	if err := r.Touch(w.ID, people[1].account, later); err != nil {
		t.Fatal(err)
	}
	ms, err := r.Members(w.ID)
	if err != nil || len(ms) != 2 || ms[0].Role != together.Host || !ms[1].LastSeenAt.Equal(later) {
		t.Fatalf("%+v %v", ms, err)
	}
	if mine, _ := r.ForAccount(people[1].account); len(mine) != 1 || mine[0].ID != w.ID {
		t.Fatalf("%v", mine)
	}
	if on, _ := r.ForDevice(people[1].device); len(on) != 1 || on[0].ID != w.ID {
		t.Fatalf("%v", on)
	}

	w.Playing, w.PositionMs, w.PositionAt, w.PausedBy = false, 72_000, later, &people[1].account
	w.Version++
	if err := r.Update(w); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(w.ID)
	if err != nil || got.PositionMs != 72_000 || *got.PausedBy != people[1].account || got.Version != 2 || got.EndedAt != nil {
		t.Fatalf("%+v %v", got, err)
	}
	if got.SharedAt(later.Add(time.Hour)) != 72_000 {
		t.Fatal("paused moved")
	}
	got.Playing = true
	if got.SharedAt(later.Add(1500*time.Millisecond)) != 73_500 || got.SharedAt(later.Add(-time.Second)) != 72_000 {
		t.Fatal("playing")
	}
	got.EndedAt = &later
	if err := r.Update(got); err != nil {
		t.Fatal(err)
	}
	if open, _ := r.Open(&h.ID, &movie.ID); len(open) != 0 {
		t.Fatalf("ended still open: %v", open)
	}
}
