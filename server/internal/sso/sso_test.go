package sso_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/sso"
)

func TestProvidersAndIdentities(t *testing.T) {
	d := dbtest.Open(t)
	r := sso.New(d)
	p := &sso.Provider{Name: "Keyhole", Issuer: "https://id.example.com", ClientID: "re", ClientSecret: "s", RequiredGroup: "media",
		LinksByEmail: true, Enabled: true}
	if err := r.Create(p); err != nil {
		t.Fatal(err)
	}
	off := &sso.Provider{Name: "Attic", Issuer: "https://attic.example.com", ClientID: "re"}
	if err := r.Create(off); err != nil {
		t.Fatal(err)
	}
	g, err := r.Get(p.ID)
	if err == nil {
		g.CreatedAt, g.UpdatedAt = p.CreatedAt, p.UpdatedAt
	}
	if err != nil || *g != *p {
		t.Fatalf("%+v %v", g, err)
	}
	if l, _ := r.List(); len(l) != 2 || l[0].Name != "Attic" {
		t.Fatalf("%+v", l)
	}
	if l, _ := r.Enabled(); len(l) != 1 || l[0].ID != p.ID {
		t.Fatalf("%+v", l)
	}
	p.ClientSecret, p.RequiredGroup, p.Enabled = "", "", false
	if err := r.Update(p); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(p.ID); g.ClientSecret != "" || g.RequiredGroup != "" || g.Enabled {
		t.Fatalf("%+v", g)
	}

	a := &account.Account{Kind: account.Adult, Email: "ada@example.com", Name: "Ada"}
	if err := account.New(d).Create(a); err != nil {
		t.Fatal(err)
	}
	id := &sso.Identity{AccountID: a.ID, ProviderID: p.ID, Subject: "sub-1", Email: "ada@example.com"}
	if err := r.CreateIdentity(id); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateIdentity(&sso.Identity{AccountID: a.ID, ProviderID: p.ID, Subject: "sub-2"}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("one identity per account and provider: %v", err)
	}
	b := &account.Account{Kind: account.Adult, Email: "bo@example.com", Name: "Bo"}
	_ = account.New(d).Create(b)
	if err := r.CreateIdentity(&sso.Identity{AccountID: b.ID, ProviderID: p.ID, Subject: "sub-1"}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("one account per subject: %v", err)
	}
	if g, err := r.IdentityBySubject(p.ID, "sub-1"); err != nil || g.ID != id.ID || g.AccountID != a.ID || g.Email != id.Email {
		t.Fatalf("%+v %v", g, err)
	}
	if l, _ := r.Identities(a.ID); len(l) != 1 {
		t.Fatalf("%+v", l)
	}
	if err := r.DeleteIdentity(b.ID, id.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("another account's identity: %v", err)
	}
	if err := r.DeleteIdentity(a.ID, id.ID); err != nil {
		t.Fatal(err)
	}

	_ = r.CreateIdentity(&sso.Identity{AccountID: a.ID, ProviderID: off.ID, Subject: "x"})
	if err := r.Delete(off.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ := r.Identities(a.ID); len(l) != 0 {
		t.Fatalf("identities cascade with their provider: %+v", l)
	}
	_ = r.CreateIdentity(&sso.Identity{AccountID: a.ID, ProviderID: p.ID, Subject: "y"})
	if err := account.New(d).Delete(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.IdentityBySubject(p.ID, "y"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("identities cascade with their account: %v", err)
	}
	if err := r.Delete(off.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}
