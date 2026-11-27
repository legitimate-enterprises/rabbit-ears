package device_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func household1(t *testing.T, d db.DBTX, email string) (uid.ID, uid.ID) {
	t.Helper()
	ar := account.New(d)
	a := &account.Account{Kind: account.Adult, Email: email, Name: email, PasswordHash: "x"}
	if err := ar.Create(a, nil); err != nil {
		t.Fatal(err)
	}
	h := &household.Household{Name: email, AdultID: a.ID}
	if err := household.New(d).Create(h, nil); err != nil {
		t.Fatal(err)
	}
	return a.ID, h.ID
}

func TestDevice(t *testing.T) {
	d := dbtest.Open(t)
	r := device.New(d)
	acc, hh := household1(t, d, "a@example.test")
	acc2, hh2 := household1(t, d, "b@example.test")
	v := &device.Device{TokenHash: "t1", Name: "Mac", Platform: "macos", AccountID: acc, HouseholdID: hh}
	if err := r.Create(v); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&device.Device{TokenHash: "t1", Name: "dup", Platform: "web", AccountID: acc, HouseholdID: hh}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("token hash unique: %v", err)
	}
	if err := r.Create(&device.Device{TokenHash: "t2", Name: "x", Platform: "web", AccountID: uid.N(999), HouseholdID: hh}); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
	w := &device.Device{TokenHash: "t3", Name: "Other", Platform: "web", AccountID: acc2, HouseholdID: hh2}
	_ = r.Create(w)

	g, err := r.ByTokenHash("t1")
	if err != nil || g.ID != v.ID || g.HouseholdID != hh || g.LastSeenAt != nil {
		t.Fatalf("%+v %v", g, err)
	}
	if _, err := r.ByTokenHash("nope"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	seen := time.Now().Add(time.Hour).Truncate(time.Millisecond).In(time.FixedZone("x", -5*3600))
	if err := r.Touch(v.ID, seen); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(v.ID); !g.LastSeenAt.Equal(seen) || g.LastSeenAt.Location() != time.UTC {
		t.Fatalf("%v", g.LastSeenAt)
	}
	if l, _ := r.List(); len(l) != 2 || l[0].ID != v.ID {
		t.Fatalf("most recently seen first: %+v", l)
	}
	if err := r.Revoke(v.ID, seen.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.ByTokenHash("t1"); g.RevokedAt == nil {
		t.Fatal("not revoked")
	}
	if g, _ := r.Get(w.ID); g.RevokedAt != nil {
		t.Fatal("other household's device revoked")
	}
	g.Name = "Renamed"
	if err := r.Update(g); err != nil {
		t.Fatal(err)
	}
	if l, _ := r.ByAccount(acc); len(l) != 1 || l[0].Name != "Renamed" {
		t.Fatalf("%+v", l)
	}
	if err := r.Revoke(uid.N(999), seen); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Delete(w.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReachingListsSignedInDevicesOfTheAccountOrActingAsIt(t *testing.T) {
	d := dbtest.Open(t)
	r := device.New(d)
	acc, hh := household1(t, d, "a@example.test")
	kid := &account.Account{Kind: account.Kid, Name: "Kid", HouseholdID: &hh}
	if err := account.New(d).Create(kid, nil); err != nil {
		t.Fatal(err)
	}
	var made []*device.Device
	for _, token := range []string{"as-adult", "as-kid", "as-kid-revoked"} {
		v := &device.Device{TokenHash: token, Name: token, Platform: "web", AccountID: acc, HouseholdID: hh}
		if err := r.Create(v); err != nil {
			t.Fatal(err)
		}
		made = append(made, v)
	}
	for _, v := range made[1:] {
		if err := r.SetProfile(v.ID, &kid.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Revoke(made[2].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if l, err := r.Reaching(kid.ID); err != nil || len(l) != 1 || l[0].ID != made[1].ID {
		t.Fatalf("the kid's: %+v %v", l, err)
	}
	if l, err := r.Reaching(acc); err != nil || len(l) != 2 || l[0].ID != made[0].ID || l[1].ID != made[1].ID {
		t.Fatalf("the adult's: %+v %v", l, err)
	}
}

func TestTVPlatforms(t *testing.T) {
	for p, want := range map[string]bool{"tvos": true, "roku": true, "web": false, "macos": false, "ios": false, "android": false} {
		if device.TV(p) != want {
			t.Fatalf("%s: want %v", p, want)
		}
	}
}
