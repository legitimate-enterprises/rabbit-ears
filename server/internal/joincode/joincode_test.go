package joincode_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/joincode"
)

func TestJoinCode(t *testing.T) {
	d := dbtest.Open(t)
	pending := &account.Account{Kind: account.Adult, Email: "p@example.test", Name: "P"}
	if err := account.New(d).Create(pending); err != nil {
		t.Fatal(err)
	}
	r := joincode.New(d)
	now := time.Now().UTC().Truncate(time.Millisecond)
	j := &joincode.JoinCode{Code: "7KQ4-MNP2", AccountID: pending.ID, ExpiresAt: now.Add(7 * 24 * time.Hour)}
	if err := r.Create(j); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&joincode.JoinCode{Code: "7KQ4-MNP2", AccountID: pending.ID, ExpiresAt: now}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("code unique: %v", err)
	}
	if err := r.Create(&joincode.JoinCode{Code: "x", AccountID: 999, ExpiresAt: now}); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
	g, err := r.ByCode("7KQ4-MNP2")
	if err != nil || !g.Usable(now) || !g.ExpiresAt.Equal(j.ExpiresAt) {
		t.Fatalf("%+v %v", g, err)
	}
	if g.Usable(j.ExpiresAt) {
		t.Fatal("usable at the expiry instant")
	}
	if _, err := r.Redeem("7KQ4-MNP2", j.ExpiresAt.Add(time.Second)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expired redeem: %v", err)
	}
	used, err := r.Redeem("7KQ4-MNP2", now)
	if err != nil || used.UsedAt == nil || !used.UsedAt.Equal(now) {
		t.Fatalf("%+v %v", used, err)
	}
	if _, err := r.Redeem("7KQ4-MNP2", now); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("second redeem must fail: %v", err)
	}
}

func TestRevokeListAndExpire(t *testing.T) {
	d := dbtest.Open(t)
	a := &account.Account{Kind: account.Adult, Email: "p@example.test", Name: "P"}
	_ = account.New(d).Create(a)
	r := joincode.New(d)
	now := time.Now().UTC().Truncate(time.Millisecond)
	old := &joincode.JoinCode{Code: "OLD", AccountID: a.ID, ExpiresAt: now.Add(-48 * time.Hour)}
	cur := &joincode.JoinCode{Code: "CUR", AccountID: a.ID, ExpiresAt: now.Add(time.Hour)}
	_ = r.Create(old)
	_ = r.Create(cur)
	if err := r.RevokeUnused(a.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Redeem("CUR", now); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("revoked code redeemed: %v", err)
	}
	g, _ := r.Get(cur.ID)
	if g.RevokedAt == nil || g.Usable(now) {
		t.Fatalf("%+v", g)
	}
	if l, _ := r.ByAccount(a.ID); len(l) != 2 {
		t.Fatal(len(l))
	}
	g.ExpiresAt = now.Add(24 * time.Hour)
	g.RevokedAt = nil
	if err := r.Update(g); err != nil {
		t.Fatal(err)
	}
}
