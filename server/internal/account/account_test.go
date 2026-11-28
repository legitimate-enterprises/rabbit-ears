package account_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func adultWithHousehold(t *testing.T, ar *account.Repo, hr *household.Repo, email string) (*account.Account, *household.Household) {
	t.Helper()
	a := &account.Account{Kind: account.Adult, Email: email, Name: email}
	if err := ar.Create(a, nil); err != nil {
		t.Fatal(err)
	}
	h := &household.Household{Name: email, AdultID: a.ID}
	if err := hr.Create(h, nil); err != nil {
		t.Fatal(err)
	}
	a.HouseholdID = &h.ID
	if err := ar.Update(a, nil); err != nil {
		t.Fatal(err)
	}
	return a, h
}

func TestAdultCRUDAndEmail(t *testing.T) {
	d := dbtest.Open(t)
	r := account.New(d)
	a := &account.Account{Kind: account.Adult, Email: "Ann@Example.Test", Name: "Ann", IsAdmin: true, PasswordHash: "h"}
	if err := r.Create(a, nil); err != nil {
		t.Fatal(err)
	}
	got, err := r.ByEmail("ann@example.test")
	if err != nil || got.ID != a.ID || !got.IsAdmin || got.Pending() || got.FilterMode != account.FilterRatings {
		t.Fatalf("%+v %v", got, err)
	}
	if err := r.Create(&account.Account{Kind: account.Adult, Email: "ANN@example.test", Name: "dup"}, nil); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("email must be unique ignoring case: %v", err)
	}
	if n, _ := r.AdminCount(); n != 1 {
		t.Fatal(n)
	}
	rev := time.Now().UTC().Truncate(time.Millisecond)
	got.RevokedAt, got.Name = &rev, "Anne"
	if err := r.Update(got, nil); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.AdminCount(); n != 0 {
		t.Fatal("revoked admin should not count")
	}
	if g, _ := r.Get(a.ID); g.Name != "Anne" || !g.RevokedAt.Equal(rev) {
		t.Fatalf("%+v", g)
	}
	if all, _ := r.List(account.Adult, 0, 0); len(all) != 1 {
		t.Fatal(len(all))
	}
	if all, _ := r.List(account.Kid, 0, 0); len(all) != 0 {
		t.Fatal(len(all))
	}
	if err := r.Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(a.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPendingAdultAndKidRules(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr := account.New(d), household.New(d)
	p := &account.Account{Kind: account.Adult, Email: "p@example.test", Name: "P"}
	if err := ar.Create(p, nil); err != nil {
		t.Fatal(err)
	}
	if g, _ := ar.Get(p.ID); !g.Pending() || g.HouseholdID != nil {
		t.Fatalf("%+v", g)
	}
	_, h := adultWithHousehold(t, ar, hr, "a@example.test")
	if err := ar.Create(&account.Account{Kind: account.Kid, Name: "no household"}, nil); err == nil {
		t.Fatal("kid without household accepted")
	}
	if err := ar.Create(&account.Account{Kind: account.Kid, Name: "k", HouseholdID: &h.ID, IsAdmin: true}, nil); err == nil {
		t.Fatal("admin kid accepted")
	}
	if err := ar.Create(&account.Account{Kind: account.Kid, Name: "k", HouseholdID: &h.ID, Email: "k@example.test"}, nil); err == nil {
		t.Fatal("kid with email accepted")
	}
	if err := ar.Create(&account.Account{Kind: account.Adult, Name: "x", MovieCeiling: "PG"}, nil); err == nil {
		t.Fatal("adult with ceiling accepted")
	}
	bday := time.Date(2016, 5, 4, 0, 0, 0, 0, time.UTC)
	k := &account.Account{Kind: account.Kid, Name: "Kit", HouseholdID: &h.ID, Birthday: &bday, MovieCeiling: "PG", TVCeiling: "TV-Y7", PINHash: "pin"}
	if err := ar.Create(k, nil); err != nil {
		t.Fatal(err)
	}
	if err := ar.Create(&account.Account{Kind: account.Kid, Name: "Kit", HouseholdID: &h.ID}, nil); err != nil {
		t.Fatalf("kids need no unique email: %v", err)
	}
	g, _ := ar.Get(k.ID)
	if !g.Birthday.Equal(bday) || g.MovieCeiling != "PG" || g.TVCeiling != "TV-Y7" || g.PINHash != "pin" || g.Email != "" {
		t.Fatalf("%+v", g)
	}
	if m, _ := ar.ByHousehold(h.ID); len(m) != 3 || m[0].Kind != account.Adult {
		t.Fatalf("adult first: %+v", m)
	}
	if err := ar.Create(&account.Account{Kind: account.Kid, Name: "z", HouseholdID: ptr(uid.N(999))}, nil); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
}

func ptr(id uid.ID) *uid.ID { return &id }

func TestByHouseholdOrdersByBirthThenName(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr := account.New(d), household.New(d)
	owner, h := adultWithHousehold(t, ar, hr, "zed@example.test")
	born := func(y int) *time.Time { b := time.Date(y, 3, 1, 0, 0, 0, 0, time.UTC); return &b }
	for _, a := range []*account.Account{
		{Kind: account.Kid, Name: "max"},
		{Kind: account.Kid, Name: "Ben", Birthday: born(2018)},
		{Kind: account.Adult, Email: "amy@example.test", Name: "Amy"},
		{Kind: account.Kid, Name: "Zoe", Birthday: born(2012)},
		{Kind: account.Adult, Email: "bob@example.test", Name: "Bob", Birthday: born(1990)},
		{Kind: account.Kid, Name: "Ann", Birthday: born(2018)},
		{Kind: account.Adult, Email: "cal@example.test", Name: "Cal", Birthday: born(1980)},
		{Kind: account.Kid, Name: "Lou"},
	} {
		a.HouseholdID = &h.ID
		if err := ar.Create(a, nil); err != nil {
			t.Fatal(err)
		}
	}
	m, err := ar.ByHousehold(h.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range m {
		got = append(got, a.Name)
	}
	want := []string{owner.Name, "Cal", "Bob", "Amy", "Zoe", "Ann", "Ben", "Lou", "max"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDeletedWithMatchesTheCascade(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr := account.New(d), household.New(d)
	owner, h := adultWithHousehold(t, ar, hr, "owner@example.test")
	other, _ := adultWithHousehold(t, ar, hr, "other@example.test")
	member := &account.Account{Kind: account.Adult, Email: "member@example.test", Name: "Member", HouseholdID: &h.ID}
	kid := &account.Account{Kind: account.Kid, Name: "Kid", HouseholdID: &h.ID}
	for _, a := range []*account.Account{member, kid} {
		if err := ar.Create(a, nil); err != nil {
			t.Fatal(err)
		}
	}
	if gone, err := ar.DeletedWith(member.ID); err != nil || len(gone) != 1 || gone[0].ID != member.ID {
		t.Fatalf("a member takes nobody along: %+v %v", gone, err)
	}
	gone, err := ar.DeletedWith(owner.ID)
	if err != nil || len(gone) != 3 {
		t.Fatalf("the owner takes the household: %+v %v", gone, err)
	}
	if err := ar.Delete(owner.ID); err != nil {
		t.Fatal(err)
	}
	for _, a := range gone {
		if _, err := ar.Get(a.ID); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("%s survived: %v", a.Name, err)
		}
	}
	if _, err := ar.Get(other.ID); err != nil {
		t.Fatalf("another household's owner was deleted: %v", err)
	}
}

func TestPINLockout(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr := account.New(d), household.New(d)
	_, h := adultWithHousehold(t, ar, hr, "a@example.test")
	k := &account.Account{Kind: account.Kid, Name: "k", HouseholdID: &h.ID, PINHash: "x"}
	_ = ar.Create(k, nil)
	until := time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
	for i := 1; i <= 5; i++ {
		n, err := ar.RecordPINFailure(k.ID, 5, until)
		if err != nil || n != i {
			t.Fatal(n, err)
		}
		g, _ := ar.Get(k.ID)
		if (g.PINLockedUntil != nil) != (i == 5) {
			t.Fatalf("lock at failure %d: %v", i, g.PINLockedUntil)
		}
	}
	if err := ar.ClearPINFailures(k.ID); err != nil {
		t.Fatal(err)
	}
	if g, _ := ar.Get(k.ID); g.PINFailures != 0 || g.PINLockedUntil != nil {
		t.Fatalf("%+v", g)
	}
	if _, err := ar.RecordPINFailure(uid.N(999), 5, until); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAllowListInheritsFromAncestors(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr, ir := account.New(d), household.New(d), item.New(d)
	_, h := adultWithHousehold(t, ar, hr, "a@example.test")
	k := &account.Account{Kind: account.Kid, Name: "k", HouseholdID: &h.ID, FilterMode: account.FilterAllowList}
	_ = ar.Create(k, nil)
	l := &library.Library{Name: "tv", Kind: library.TV}
	_ = library.New(d).Create(l, nil)
	show := &item.Item{LibraryID: l.ID, Kind: item.Show, ParsedTitle: "S"}
	_ = ir.Create(show)
	season := &item.Item{LibraryID: l.ID, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "1", SeasonNumber: new(int)}
	_ = ir.Create(season)
	ep := &item.Item{LibraryID: l.ID, ParentID: &season.ID, Kind: item.Episode, ParsedTitle: "e", EpisodeNumber: new(int)}
	_ = ir.Create(ep)
	other := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "m"}
	_ = ir.Create(other)

	for i := 0; i < 2; i++ {
		if err := ar.Allow(k.ID, show.ID); err != nil {
			t.Fatalf("allow is idempotent: %v", err)
		}
	}
	if err := ar.Allow(k.ID, uid.N(999)); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
	if ids, _ := ar.Allowed(k.ID); len(ids) != 1 || ids[0] != show.ID {
		t.Fatal(ids)
	}
	if err := ar.Disallow(k.ID, show.ID); err != nil {
		t.Fatal(err)
	}
	if err := ar.Disallow(k.ID, show.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	_ = ar.Allow(k.ID, other.ID)
	_ = ir.Delete(other.ID)
	if ids, _ := ar.Allowed(k.ID); len(ids) != 0 {
		t.Fatal("allow list should cascade with the item")
	}
}

// A new profile stores the palette color its household shows least, the
// earliest in the palette on a tie; a row without one shows the palette
// color its id picks, and counts as that color.
func TestProfileColors(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr := account.New(d), household.New(d)
	a, h := adultWithHousehold(t, ar, hr, "ann@example.test")
	if a.Color != account.Palette[0] {
		t.Fatalf("the first adult %q", a.Color)
	}
	kid := func() *account.Account {
		t.Helper()
		k := &account.Account{Kind: account.Kid, Name: "K", HouseholdID: &h.ID}
		if err := ar.Create(k, nil); err != nil {
			t.Fatal(err)
		}
		got, err := ar.Get(k.ID)
		if err != nil || got.Color != k.Color {
			t.Fatalf("stored %+v %v", got, err)
		}
		return k
	}
	if k := kid(); k.Color != account.Palette[1] {
		t.Fatalf("second %q", k.Color)
	}
	k3 := kid()
	if k3.Color != account.Palette[2] {
		t.Fatalf("third %q", k3.Color)
	}
	k3.Color = account.Palette[0]
	if err := ar.Update(k3, nil); err != nil {
		t.Fatal(err)
	}
	if k := kid(); k.Color != account.Palette[2] {
		t.Fatalf("a color freed is used again: %q", k.Color)
	}

	id := uid.N(13)
	if _, err := d.Exec(`INSERT INTO account (id, kind, name, household_id, created_at, modified_at) VALUES (?, 'kid', 'Old', ?, 0, 0)`, id, h.ID); err != nil {
		t.Fatal(err)
	}
	old, err := ar.Get(id)
	if err != nil || old.Color != "" || old.ShownColor() != account.Palette[13%8] {
		t.Fatalf("%+v %v", old, err)
	}
	if !account.ValidColor("#2E6B5B") || account.ValidColor("#2e6b5b") || account.ValidColor("#000000") {
		t.Fatal("only the palette is valid")
	}
}

func names(as []*account.Account) string {
	out := ""
	for _, a := range as {
		out += a.Name + " "
	}
	return out
}

func TestPeopleAreHouseholdsAndLinkedAdults(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr := account.New(d), household.New(d)
	joined := func(email string) (*account.Account, *household.Household) {
		a, h := adultWithHousehold(t, ar, hr, email)
		a.PasswordHash = "h"
		if err := ar.Update(a, nil); err != nil {
			t.Fatal(err)
		}
		return a, h
	}
	dad, home := joined("dad")
	mom, hers := joined("mom")
	friend, _ := joined("friend")
	kid := &account.Account{Kind: account.Kid, Name: "kid", HouseholdID: &home.ID}
	pending := &account.Account{Kind: account.Adult, Name: "pending", Email: "p@example.test", HouseholdID: &home.ID}
	for _, a := range []*account.Account{kid, pending} {
		if err := ar.Create(a, nil); err != nil {
			t.Fatal(err)
		}
	}
	people := func(a *account.Account, tv bool) string {
		t.Helper()
		got, err := ar.People(a, tv, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return names(got)
	}
	if got := people(dad, false); got != "kid " {
		t.Fatalf("an adult sees only who has joined their household: %q", got)
	}
	if got := people(mom, false); got != "" {
		t.Fatalf("another household is never seen: %q", got)
	}

	if _, err := d.Exec(`INSERT INTO household_member (household_id, account_id, created_at) VALUES (?,?,0)`, hers.ID, kid.ID); err != nil {
		t.Fatal(err)
	}
	if hs, err := ar.Households(kid); err != nil || len(hs) != 2 || hs[0] != home.ID || hs[1] != hers.ID {
		t.Fatalf("own household first, then shared: %v %v", hs, err)
	}
	if l, err := ar.Labels(mom, []uid.ID{kid.ID, dad.ID}); err != nil || len(l) != 2 || l[kid.ID].HouseholdID != hers.ID || l[dad.ID].HouseholdID != home.ID {
		t.Fatalf("a shared kid is labeled with the viewer's household, anyone else with their own: %+v %v", l, err)
	}
	if got := people(kid, true); got != "dad mom " {
		t.Fatalf("a shared kid sees both households: %q", got)
	}
	if got := people(mom, false); got != "kid " {
		t.Fatalf("sharing a kid shows the kid, not the kid's other household: %q", got)
	}

	lo, hi := dad.ID, friend.ID
	if _, err := d.Exec(`INSERT INTO adult_link (a_id, b_id, created_at) VALUES (?,?,0)`, lo, hi); err != nil {
		t.Fatal(err)
	}
	if got := people(dad, false); got != "friend kid " {
		t.Fatalf("a linked adult is seen on a personal device: %q", got)
	}
	if got := people(friend, false); got != "dad " {
		t.Fatalf("a link is seen from both ends: %q", got)
	}
	if got := people(dad, true); got != "kid " {
		t.Fatalf("a link is not seen on a TV by default: %q", got)
	}
	linked := func(a *account.Account, tv bool) string {
		t.Helper()
		got, err := ar.Linked(a, tv, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		return names(got)
	}
	if linked(dad, false) != "friend " || linked(friend, false) != "dad " || linked(dad, true) != "" {
		t.Fatalf("Linked is the links alone, on a TV only those set for TVs: %q", linked(dad, false))
	}
	if ok, err := ar.Sees(dad, friend.ID, true); err != nil || ok {
		t.Fatalf("Sees must agree with People: %v %v", ok, err)
	}
	if _, err := d.Exec(`UPDATE adult_link SET tv = 1`); err != nil {
		t.Fatal(err)
	}
	if got := people(dad, true); got != "friend kid " {
		t.Fatalf("a link set for TVs is seen there: %q", got)
	}
	if ok, err := ar.Sees(dad, friend.ID, true); err != nil || !ok {
		t.Fatalf("Sees must agree with People: %v %v", ok, err)
	}
	if ok, _ := ar.Sees(dad, mom.ID, false); ok {
		t.Fatal("an adult never sees their kid's other household")
	}
	if _, err := d.Exec(`INSERT INTO adult_link (a_id, b_id, created_at) VALUES (?,?,0)`, hi, lo); err == nil {
		t.Fatal("a link is stored once, lower id first")
	}
}

// Friends see each other off TVs, and no one else of the other's household;
// Friends lists them alone.
func TestFriendsAreSeenOffTVs(t *testing.T) {
	d := dbtest.Open(t)
	ar, hr := account.New(d), household.New(d)
	joined := func(email string) (*account.Account, *household.Household) {
		a, h := adultWithHousehold(t, ar, hr, email)
		a.PasswordHash = "h"
		if err := ar.Update(a, nil); err != nil {
			t.Fatal(err)
		}
		return a, h
	}
	ann, _ := joined("ann")
	bob, his := joined("bob")
	kid := &account.Account{Kind: account.Kid, Name: "kid", HouseholdID: &his.ID}
	if err := ar.Create(kid, nil); err != nil {
		t.Fatal(err)
	}
	lo, hi := ann.ID, bob.ID
	if lo.Compare(hi) > 0 {
		lo, hi = hi, lo
	}
	if _, err := d.Exec(`INSERT INTO friendship (a_id, b_id, created_at) VALUES (?,?,0)`, lo, hi); err != nil {
		t.Fatal(err)
	}
	people := func(a *account.Account, tv bool) string {
		t.Helper()
		got, err := ar.People(a, tv, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		return names(got)
	}
	if got := people(ann, false); got != "bob " {
		t.Fatalf("a friend is seen, their kid is not: %q", got)
	}
	if got := people(bob, false); got != "ann kid " {
		t.Fatalf("a friendship is seen from both ends: %q", got)
	}
	if got := people(ann, true); got != "" {
		t.Fatalf("a friend is not seen on a TV: %q", got)
	}
	if got := people(kid, false); got != "bob " {
		t.Fatalf("a kid gains nothing from an adult's friends: %q", got)
	}
	if ok, err := ar.Sees(ann, bob.ID, false); err != nil || !ok {
		t.Fatalf("Sees must agree with People: %v %v", ok, err)
	}
	if got, err := ar.Friends(bob, 10, 0); err != nil || names(got) != "ann " {
		t.Fatalf("Friends is the friendships alone: %q %v", names(got), err)
	}
	if _, err := d.Exec(`INSERT INTO friendship (a_id, b_id, created_at) VALUES (?,?,0)`, hi, lo); err == nil {
		t.Fatal("a friendship is stored once, lower id first")
	}
}

// Create keeps a client's id and stamps who made the row; an update stamps
// who changed it, and a PIN attempt changes neither.
func TestAuditColumns(t *testing.T) {
	d := dbtest.Open(t)
	r := account.New(d)
	var people [2]uid.ID
	for i := range people {
		p := &account.Account{Kind: account.Adult, Email: string(rune('m'+i)) + "@example.test", Name: "P"}
		if err := r.Create(p, nil); err != nil {
			t.Fatal(err)
		}
		people[i] = p.ID
	}
	maker, editor := people[0], people[1]
	a := &account.Account{ID: uid.N(50), Kind: account.Adult, Email: "a@example.test", Name: "A"}
	if err := r.Create(a, &maker); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&account.Account{ID: a.ID, Kind: account.Adult, Email: "b@example.test", Name: "B"}, nil); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("a taken id is a conflict: %v", err)
	}
	g, err := r.Get(uid.N(50))
	if err != nil || *g.CreatedBy != maker || *g.ModifiedBy != maker || !g.CreatedAt.Equal(a.CreatedAt) || !g.ModifiedAt.Equal(a.CreatedAt) {
		t.Fatalf("%+v %v", g, err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := r.Update(g, &editor); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordPINFailure(a.ID, 5, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := r.ClearPINFailures(a.ID); err != nil {
		t.Fatal(err)
	}
	u, _ := r.Get(a.ID)
	if *u.CreatedBy != maker || *u.ModifiedBy != editor || !u.ModifiedAt.Equal(g.ModifiedAt) || !u.ModifiedAt.After(u.CreatedAt) {
		t.Fatalf("%+v", u)
	}
	if err := r.Update(u, nil); err != nil {
		t.Fatal(err)
	}
	if u, _ = r.Get(a.ID); u.ModifiedBy != nil || *u.CreatedBy != maker {
		t.Fatalf("an update outside a request clears modified_by: %+v", u)
	}
}
