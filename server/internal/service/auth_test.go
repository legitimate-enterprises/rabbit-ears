package service_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func adultWithHousehold(t *testing.T, s *service.Services, email string) (*account.Account, *household.Household) {
	t.Helper()
	a := &account.Account{Kind: account.Adult, Email: email, Name: email, PasswordHash: "x"}
	if err := s.Accounts.Repo().Create(a, nil); err != nil {
		t.Fatal(err)
	}
	h := &household.Household{Name: email, AdultID: a.ID}
	if err := s.Households.Repo().Create(h, nil); err != nil {
		t.Fatal(err)
	}
	a.HouseholdID = &h.ID
	if err := s.Accounts.Repo().Update(a, nil); err != nil {
		t.Fatal(err)
	}
	return a, h
}

func TestRegisterThenAuthenticate(t *testing.T) {
	s := service.New(dbtest.Open(t))
	a, h := adultWithHousehold(t, s, "a@example.test")
	token, dev, err := s.Devices.Register(a.ID, h.ID, "Laptop", "web")
	if err != nil || token == "" || dev.TokenHash == token {
		t.Fatalf("%q %+v %v", token, dev, err)
	}
	gotDev, gotAcct, err := s.Devices.Authenticate(token)
	if err != nil || gotDev.ID != dev.ID || gotAcct.ID != a.ID || gotDev.LastSeenAt == nil {
		t.Fatalf("%+v %+v %v", gotDev, gotAcct, err)
	}
	if _, _, err := s.Devices.Authenticate("nope"); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("unknown token: %v", err)
	}
	_ = s.Devices.Repo().Revoke(dev.ID, time.Now())
	if _, _, err := s.Devices.Authenticate(token); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("revoked device: %v", err)
	}
}

func TestAuthenticateRefusesRevokedAccount(t *testing.T) {
	s := service.New(dbtest.Open(t))
	a, h := adultWithHousehold(t, s, "a@example.test")
	token, _, _ := s.Devices.Register(a.ID, h.ID, "Laptop", "web")
	now := time.Now()
	a.RevokedAt = &now
	_ = s.Accounts.Repo().Update(a, nil)
	if _, _, err := s.Devices.Authenticate(token); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("revoked account: %v", err)
	}
}

func TestProfileOnlyWithinHousehold(t *testing.T) {
	s := service.New(dbtest.Open(t))
	a, ha := adultWithHousehold(t, s, "a@example.test")
	_, hb := adultWithHousehold(t, s, "b@example.test")
	kid := &account.Account{Kind: account.Kid, Name: "K", HouseholdID: &hb.ID}
	_ = s.Accounts.Repo().Create(kid, nil)
	if p, err := s.Accounts.Profile(ha.ID, a.ID); err != nil || p.ID != a.ID {
		t.Fatalf("own adult: %v", err)
	}
	if _, err := s.Accounts.Profile(ha.ID, kid.ID); err == nil {
		t.Fatal("kid of another household must be unreachable")
	}
	if _, err := s.Accounts.Profile(hb.ID, kid.ID); err != nil {
		t.Fatal(err)
	}
}

// A household made for an adult takes the id its creator chose and records
// that creator on both rows it writes; the same id again is a conflict.
func TestCreateForTakesTheChosenIDAndTheActor(t *testing.T) {
	s := service.New(dbtest.Open(t))
	admin, _ := adultWithHousehold(t, s, "admin@example.test")
	a := &account.Account{Kind: account.Adult, Email: "b@example.test", Name: "B", PasswordHash: "x"}
	must(t, s.Accounts.Repo().Create(a, nil))
	id := uid.New()
	h, err := s.Households.CreateFor("B", a, &id, &admin.ID)
	if err != nil || h.ID != id {
		t.Fatalf("%+v %v", h, err)
	}
	got, err := s.Households.Repo().Get(id)
	must(t, err)
	moved, err := s.Accounts.Repo().Get(a.ID)
	must(t, err)
	if got.CreatedBy == nil || *got.CreatedBy != admin.ID || moved.ModifiedBy == nil || *moved.ModifiedBy != admin.ID || moved.CreatedBy != nil {
		t.Fatalf("household by %v, adult created by %v modified by %v", got.CreatedBy, moved.CreatedBy, moved.ModifiedBy)
	}
	other := &account.Account{Kind: account.Adult, Email: "c@example.test", Name: "C", PasswordHash: "x"}
	must(t, s.Accounts.Repo().Create(other, nil))
	if _, err := s.Households.CreateFor("C", other, &id, &admin.ID); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("a taken id: %v", err)
	}
}

func TestDeletingAHouseholdKeepsItsAdult(t *testing.T) {
	s := service.New(dbtest.Open(t))
	a, h := adultWithHousehold(t, s, "a@example.test")
	kid := &account.Account{Kind: account.Kid, Name: "K", HouseholdID: &h.ID}
	_ = s.Accounts.Repo().Create(kid, nil)
	if _, err := s.Households.Delete(h.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Accounts.Repo().Get(a.ID); err != nil || got.HouseholdID != nil {
		t.Fatalf("adult must survive without a household: %+v %v", got, err)
	}
	if _, err := s.Accounts.Repo().Get(kid.ID); err == nil {
		t.Fatal("kids go with the household")
	}
}

// The delete call itself revokes the devices of a profile it removes and
// those acting as it, in its one transaction: no device can switch to the
// profile between the two and be left acting as its adult, and none it
// deletes keeps playing.
func TestDeletesRevokeDevicesActingAsAProfileTheyRemove(t *testing.T) {
	type profiles struct{ adult, kid, member *account.Account }
	type devices struct{ tablet, phone, laptop uid.ID }
	for name, c := range map[string]struct {
		del  func(s *service.Services, p profiles) (*service.SignedOut, error)
		want func(d devices) []uid.ID
	}{
		"the kid": {
			func(s *service.Services, p profiles) (*service.SignedOut, error) {
				return s.Accounts.Delete(p.kid.ID, nil)
			},
			func(d devices) []uid.ID { return []uid.ID{d.tablet} },
		},
		"another adult of their household": {
			func(s *service.Services, p profiles) (*service.SignedOut, error) {
				return s.Accounts.Delete(p.member.ID, nil)
			},
			func(d devices) []uid.ID { return []uid.ID{d.phone} },
		},
		"their household's owner": {
			func(s *service.Services, p profiles) (*service.SignedOut, error) {
				return s.Accounts.Delete(p.adult.ID, nil)
			},
			func(d devices) []uid.ID { return []uid.ID{d.tablet, d.phone, d.laptop} },
		},
		"their household": {
			func(s *service.Services, p profiles) (*service.SignedOut, error) {
				return s.Households.Delete(*p.adult.HouseholdID, nil)
			},
			func(d devices) []uid.ID { return []uid.ID{d.tablet, d.phone, d.laptop} },
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := service.New(dbtest.Open(t))
			a, h := adultWithHousehold(t, s, "a@example.test")
			kid := &account.Account{Kind: account.Kid, Name: "K", HouseholdID: &h.ID}
			member := &account.Account{Kind: account.Adult, Name: "B", Email: "b@example.test", PasswordHash: "x", HouseholdID: &h.ID}
			for _, p := range []*account.Account{kid, member} {
				if err := s.Accounts.Repo().Create(p, nil); err != nil {
					t.Fatal(err)
				}
			}
			_, tablet, _ := s.Devices.Register(a.ID, h.ID, "Tablet", "web")
			_, phone, _ := s.Devices.Register(a.ID, h.ID, "Phone", "web")
			_, laptop, _ := s.Devices.Register(a.ID, h.ID, "Laptop", "web")
			if err := s.Devices.Repo().SetProfile(tablet.ID, &kid.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.Devices.Repo().SetProfile(phone.ID, &member.ID); err != nil {
				t.Fatal(err)
			}
			out, err := c.del(s, profiles{a, kid, member})
			if err == nil {
				slices.SortFunc(out.Devices, uid.ID.Compare)
			}
			if want := c.want(devices{tablet.ID, phone.ID, laptop.ID}); err != nil || !slices.Equal(out.Devices, want) {
				t.Fatalf("%+v %v, want %v", out, err, want)
			}
			// Where the devices outlive the delete, only those acting as it are revoked.
			for _, id := range out.Devices {
				if d, err := s.Devices.Repo().Get(id); err == nil && d.RevokedAt == nil {
					t.Fatal("a device acting as the deleted profile is still signed in")
				}
			}
			if d, err := s.Devices.Repo().Get(laptop.ID); err == nil && d.RevokedAt != nil {
				t.Fatal("the adult's own device was revoked")
			}
		})
	}
}

// Every write that can take away the last active admin refuses to within
// its transaction, so two admins removing each other at once cannot both
// pass the check made before it and reopen first run.
func TestWritesKeepTheLastAdmin(t *testing.T) {
	for name, remove := range map[string]func(*service.Services, *account.Account) error{
		"delete": func(s *service.Services, a *account.Account) error {
			_, err := s.Accounts.Delete(a.ID, nil)
			return err
		},
		"delete own": func(s *service.Services, a *account.Account) error {
			_, err := s.Accounts.DeleteOwn(a, nil)
			return err
		},
		"demote": func(s *service.Services, a *account.Account) error {
			a.IsAdmin = false
			return s.Accounts.Update(a, nil)
		},
		"revoke": func(s *service.Services, a *account.Account) error {
			now := time.Now().UTC()
			a.RevokedAt = &now
			return s.Accounts.Update(a, nil)
		},
		"delete their household": func(s *service.Services, a *account.Account) error {
			_, err := s.Households.Delete(*a.HouseholdID, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := service.New(dbtest.Open(t))
			_, h := adultWithHousehold(t, s, "owner@example.test")
			a := &account.Account{Kind: account.Adult, Name: "A", Email: "a@example.test", PasswordHash: "x", HouseholdID: &h.ID, IsAdmin: true}
			must(t, s.Accounts.Repo().Create(a, nil))
			if err := remove(s, a); !errors.Is(err, service.ErrLastAdmin) {
				t.Fatalf("the last admin: %v", err)
			}
			if n, err := s.Accounts.Repo().AdminCount(); err != nil || n != 1 {
				t.Fatalf("not rolled back: %d %v", n, err)
			}
			b, _ := adultWithHousehold(t, s, "b@example.test")
			b.IsAdmin = true
			must(t, s.Accounts.Repo().Update(b, nil))
			again, err := s.Accounts.Repo().Get(a.ID)
			must(t, err)
			must(t, remove(s, again))
			if n, err := s.Accounts.Repo().AdminCount(); err != nil || n != 1 {
				t.Fatalf("with another admin: %d %v", n, err)
			}
		})
	}
}

// A password change and the sign-out of the adult's other devices land
// together or not at all.
func TestChangePasswordSignsOutOtherDevicesAtomically(t *testing.T) {
	d := dbtest.Open(t)
	s := service.New(d)
	sg, err := s.Auth.Setup("Den", "Owner", "o@example.test", "right-password", "Laptop", "web")
	must(t, err)
	phone, phoneDev, err := s.Devices.Register(sg.Adult.ID, *sg.Adult.HouseholdID, "Phone", "ios")
	must(t, err)
	_, err = d.Exec(`CREATE TRIGGER refuse_revoke BEFORE UPDATE OF revoked_at ON device BEGIN SELECT RAISE(ABORT, 'refused'); END`)
	must(t, err)
	if _, err := s.Auth.ChangePassword("10.0.0.9", sg.Device.ID, sg.Adult, "right-password", "next-password", nil); err == nil {
		t.Fatal("changed though the sign-out failed")
	}
	a, err := s.Accounts.Repo().Get(sg.Adult.ID)
	must(t, err)
	if err := s.Auth.Confirm("10.0.0.9", a, "right-password"); err != nil {
		t.Fatalf("the password changed without the sign-out: %v", err)
	}
	_, err = d.Exec(`DROP TRIGGER refuse_revoke`)
	must(t, err)

	out, err := s.Auth.ChangePassword("10.0.0.9", sg.Device.ID, a, "right-password", "next-password", nil)
	if err != nil || !slices.Equal(out.Devices, []uid.ID{phoneDev.ID}) {
		t.Fatalf("%+v %v", out, err)
	}
	if _, _, err := s.Devices.Authenticate(phone); !errors.Is(err, service.ErrUnauthenticated) {
		t.Fatalf("the other device: %v", err)
	}
	if _, _, err := s.Devices.Authenticate(sg.Token); err != nil {
		t.Fatalf("the calling device: %v", err)
	}
}

// Last seen is written at most once a minute per device.
func TestLastSeenIsThrottled(t *testing.T) {
	s := service.New(dbtest.Open(t))
	a, h := adultWithHousehold(t, s, "a@example.test")
	token, dev, _ := s.Devices.Register(a.ID, h.ID, "Laptop", "web")
	first, _, err := s.Devices.Authenticate(token)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := s.Devices.Authenticate(token); err != nil {
		t.Fatal(err)
	}
	stored, _ := s.Devices.Repo().Get(dev.ID)
	if !stored.LastSeenAt.Equal(first.LastSeenAt.Truncate(time.Millisecond)) {
		t.Fatalf("rewritten within the minute: %v then %v", first.LastSeenAt, stored.LastSeenAt)
	}
	old := time.Now().UTC().Add(-2 * time.Minute)
	must(t, s.Devices.Repo().Touch(dev.ID, old))
	if _, _, err := s.Devices.Authenticate(token); err != nil {
		t.Fatal(err)
	}
	if stored, _ = s.Devices.Repo().Get(dev.ID); !stored.LastSeenAt.After(old.Add(time.Minute)) {
		t.Fatal("not refreshed after a minute")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A kid leaving the household it lives in moves to the oldest other one it
// is in, whatever the households' ids.
func TestAKidLeavingItsHouseholdMovesToTheOldestOther(t *testing.T) {
	s := service.New(dbtest.Open(t))
	household := func(email string, id uid.ID) uid.ID {
		a := &account.Account{Kind: account.Adult, Email: email, Name: email, PasswordHash: "x"}
		must(t, s.Accounts.Repo().Create(a, nil))
		h := &household.Household{ID: id, Name: email, AdultID: a.ID}
		must(t, s.Households.Repo().Create(h, nil))
		time.Sleep(2 * time.Millisecond) // created_at is kept to the millisecond
		return h.ID
	}
	older := household("older@example.test", uid.New()) // made first, its id sorting last
	home := household("home@example.test", uid.New())
	newer := household("newer@example.test", uid.N(1))
	kid := &account.Account{Kind: account.Kid, Name: "K", HouseholdID: &home}
	must(t, s.Accounts.Repo().Create(kid, nil))
	must(t, s.Accounts.Repo().AddMember(newer, kid.ID))
	must(t, s.Accounts.Repo().AddMember(older, kid.ID))
	if _, err := s.Households.Leave(kid, home, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Accounts.Repo().Get(kid.ID); err != nil || got.HouseholdID == nil || *got.HouseholdID != older {
		t.Fatalf("moved to %v, want %s (%v)", got.HouseholdID, older, err)
	}
}
