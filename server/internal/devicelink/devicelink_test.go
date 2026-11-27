package devicelink_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/devicelink"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestDecideCollectAndStatus(t *testing.T) {
	d := dbtest.Open(t)
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A", PasswordHash: "x"}
	if err := account.New(d).Create(a, nil); err != nil {
		t.Fatal(err)
	}
	h := &household.Household{Name: "H", AdultID: a.ID}
	if err := household.New(d).Create(h, nil); err != nil {
		t.Fatal(err)
	}
	dev := &device.Device{TokenHash: "t", Name: "TV", Platform: "roku", AccountID: a.ID, HouseholdID: h.ID}
	if err := device.New(d).Create(dev); err != nil {
		t.Fatal(err)
	}
	r := devicelink.New(d)
	now := time.Now().UTC().Truncate(time.Millisecond)
	l := &devicelink.Link{Code: "7KQ4-MNP2", PollTokenHash: "p", DeviceName: "TV", Platform: "roku", Source: "10.0.0.9", ExpiresAt: now.Add(10 * time.Minute)}
	if err := r.Create(l); err != nil || l.Status != devicelink.Pending {
		t.Fatal(l, err)
	}
	if err := r.Create(&devicelink.Link{Code: "7KQ4-MNP2", PollTokenHash: "q", DeviceName: "TV", Platform: "roku", Source: "x", ExpiresAt: now}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("code unique: %v", err)
	}
	if n, oldest, err := r.PendingFrom("10.0.0.9", now); err != nil || n != 1 || !oldest.Equal(l.ExpiresAt) {
		t.Fatalf("%d %v %v", n, oldest, err)
	}
	if n, _, _ := r.PendingFrom("10.0.0.9", l.ExpiresAt); n != 0 {
		t.Fatal("an expired link is not pending")
	}
	if _, err := r.Pending("7KQ4-MNP2", l.ExpiresAt); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("pending at its expiry: %v", err)
	}
	if got := l.StatusAt(l.ExpiresAt); got != devicelink.Expired {
		t.Fatal(got)
	}
	if _, err := r.Decide("7KQ4-MNP2", devicelink.Approved, a.ID, h.ID, l.ExpiresAt); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("decided after expiry: %v", err)
	}
	ok, err := r.Decide("7KQ4-MNP2", devicelink.Approved, a.ID, h.ID, now)
	if err != nil || ok.Status != devicelink.Approved || *ok.AccountID != a.ID || *ok.HouseholdID != h.ID || !ok.DecidedAt.Equal(now) {
		t.Fatalf("%+v %v", ok, err)
	}
	if _, err := r.Decide("7KQ4-MNP2", devicelink.Denied, a.ID, h.ID, now); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("decided twice: %v", err)
	}
	if ok.StatusAt(ok.ExpiresAt) != devicelink.Expired || ok.StatusAt(now) != devicelink.Approved {
		t.Fatal("approved but never collected expires")
	}
	if err := r.Collect(l.ID, dev.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := r.Collect(l.ID, dev.ID, now); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("collected twice: %v", err)
	}
	got, _ := r.Get(l.ID)
	if got.CollectedAt == nil || *got.DeviceID != dev.ID || got.StatusAt(got.ExpiresAt.Add(time.Hour)) != devicelink.Approved {
		t.Fatalf("%+v", got)
	}
	if hs, _ := r.ByHousehold(h.ID, 100, 0); len(hs) != 1 {
		t.Fatalf("%+v", hs)
	}
	if err := r.DeleteInHousehold(l.ID, uid.N(999)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleted from another household: %v", err)
	}
	if err := device.New(d).Delete(dev.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(l.ID); got.DeviceID != nil || got.CollectedAt == nil {
		t.Fatalf("a deleted device leaves the link collected: %+v", got)
	}
	if err := r.DeleteInHousehold(l.ID, h.ID); err != nil {
		t.Fatal(err)
	}
	if all, _ := r.List(100, 0); len(all) != 0 {
		t.Fatalf("%+v", all)
	}
}
