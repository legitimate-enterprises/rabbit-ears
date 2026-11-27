package httpapi_test

import (
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// householdAdult is the owner adding an adult who then accepts on their own
// device.
func (e *env) householdAdult(owner user, name, email string) api.SignInResult {
	e.t.Helper()
	var inv api.HouseholdInvite
	e.expect(201, "POST", "/household/adults", owner.token, map[string]any{"name": name, "email": email}, &inv)
	var res api.SignInResult
	e.expect(201, "POST", inv.JoinCode.Link, "", device("name", name, "password", "longenough"), &res)
	return res
}

func profileByID(page api.AccountPage, id uid.ID) *api.Account {
	for i := range page.Items {
		if page.Items[i].Id == id {
			return &page.Items[i]
		}
	}
	return nil
}

func TestOwnerResendsAndCancelsAPendingAdult(t *testing.T) {
	e := newEnv(t)
	var inv api.HouseholdInvite
	e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Bea", "email": "bea@example.test"}, &inv)
	bea := "/profiles/" + itoa(inv.Account.Id)

	var page api.AccountPage
	e.expect(200, "GET", "/profiles", e.plain.token, nil, &page)
	if p := profileByID(page, inv.Account.Id); p == nil || !p.Pending || p.InviteExpiresAt == nil || !p.InviteExpiresAt.Equal(inv.JoinCode.ExpiresAt.Truncate(time.Millisecond)) {
		t.Fatalf("the owner sees the pending adult with the code's expiry: %+v", p)
	}

	var again api.HouseholdInvite
	e.expect(201, "POST", bea+"/join-code", e.plain.token, nil, &again)
	if again.Account.Id != inv.Account.Id || again.JoinCode.Code == inv.JoinCode.Code || again.JoinCode.AccountId != inv.Account.Id {
		t.Fatalf("%+v", again)
	}
	e.expect(404, "GET", inv.JoinCode.Link, "", nil, nil)
	e.expect(200, "GET", again.JoinCode.Link, "", nil, nil)
	e.expect(200, "GET", "/profiles", e.plain.token, nil, &page)
	if p := profileByID(page, inv.Account.Id); p == nil || p.InviteExpiresAt == nil || !p.InviteExpiresAt.Equal(again.JoinCode.ExpiresAt.Truncate(time.Millisecond)) {
		t.Fatalf("expiry follows the new code: %+v", p)
	}

	// Another adult of the household neither sees nor resends it.
	cy := e.householdAdult(e.plain, "Cy", "cy@example.test")
	e.expect(200, "GET", "/profiles", cy.Token, nil, &page)
	if profileByID(page, inv.Account.Id) != nil || profileByID(page, cy.Me.Account.Id) == nil || page.Items[0].Id != e.plain.account.ID {
		t.Fatalf("pending adults are the owner's business: %+v", page)
	}
	var me api.Me
	e.expect(200, "GET", "/me", cy.Token, nil, &me)
	for _, m := range me.Profiles {
		if m.Id == inv.Account.Id {
			t.Fatalf("nor does /me show them: %+v", me.Profiles)
		}
	}
	e.expect(404, "GET", bea, cy.Token, nil, nil)
	e.expect(200, "GET", "/me", e.plain.token, nil, &me)
	if len(me.Profiles) != 3 {
		t.Fatalf("the owner's /me lists them: %+v", me.Profiles)
	}
	e.expect(403, "POST", bea+"/join-code", cy.Token, nil, nil)
	e.expect(403, "DELETE", bea, cy.Token, nil, nil)
	e.expect(404, "POST", bea+"/join-code", e.admin.token, nil, nil)
	e.expect(409, "POST", "/profiles/"+itoa(cy.Me.Account.Id)+"/join-code", e.plain.token, nil, nil)
	k := e.kid(e.plain, "Kid")
	e.expect(409, "POST", "/profiles/"+itoa(k.Id)+"/join-code", e.plain.token, nil, nil)

	// Cancelling deletes the pending adult, so no code of theirs works.
	e.expect(204, "DELETE", bea, e.plain.token, nil, nil)
	e.expect(404, "GET", again.JoinCode.Link, "", nil, nil)
	e.expect(404, "GET", bea, e.plain.token, nil, nil)
}

func TestOwnerMakesAKidAnAdult(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	m := e.movie(e.library("movies"), "Film")
	e.rate(m, "G")
	ws := "/profiles/" + itoa(k.Id) + "/watch-state/" + itoa(m)
	e.expect(200, "PUT", ws, e.plain.token, map[string]any{"watched": false, "positionMs": 5000}, nil)
	e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/pin", e.plain.token, map[string]any{"pin": "1234"}, nil)

	// A second device of the owner is acting as the kid when it changes.
	tok, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "TV", "web")
	must(t, err)
	tv := user{token: tok}
	if st, raw := e.unlock(tv, k.Id, "1234"); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}

	path := "/profiles/" + itoa(k.Id) + "/make-adult"
	e.expect(400, "POST", path, e.plain.token, map[string]any{}, nil)
	e.expect(409, "POST", path, e.plain.token, map[string]any{"email": e.admin.account.Email}, nil)
	e.expect(403, "POST", path, tv.token, map[string]any{"email": "kid@example.test"}, nil)
	e.expect(404, "POST", path, e.admin.token, map[string]any{"email": "kid@example.test"}, nil)
	cy := e.householdAdult(e.plain, "Cy", "cy@example.test")
	e.expect(403, "POST", path, cy.Token, map[string]any{"email": "kid@example.test"}, nil)
	e.expect(403, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/make-adult", e.plain.token, map[string]any{"email": "x@example.test"}, nil)

	var inv api.HouseholdInvite
	e.expect(201, "POST", path, e.plain.token, map[string]any{"email": "kid@example.test"}, &inv)
	a := inv.Account
	if a.Kind != api.Adult || !a.Pending || a.Email == nil || *a.Email != "kid@example.test" || a.HouseholdId == nil || *a.HouseholdId != e.plain.household.ID ||
		a.MovieCeiling != nil || a.TvCeiling != nil || a.HasPin || inv.JoinCode.AccountId != k.Id {
		t.Fatalf("a pending adult in the same household, without kid rules: %+v", inv)
	}
	e.expect(409, "POST", path, e.plain.token, map[string]any{"email": "kid2@example.test"}, nil)
	// A device acting as the kid is signed out, not left acting as its adult.
	e.expect(401, "GET", "/me", tv.token, nil, nil)

	var res api.SignInResult
	e.expect(201, "POST", inv.JoinCode.Link, "", device("name", "Kid", "password", "longenough"), &res)
	if res.Me.Household == nil || res.Me.Household.Id != e.plain.household.ID {
		t.Fatalf("%+v", res.Me)
	}
	var w api.WatchState
	e.expect(200, "GET", ws, res.Token, nil, &w)
	if w.PositionMs != 5000 {
		t.Fatalf("watch state is kept: %+v", w)
	}
}

func TestAnyAdultEditsAKid(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	cy := e.householdAdult(e.plain, "Cy", "cy@example.test")
	id := "/profiles/" + itoa(k.Id)
	e.expect(200, "PUT", id, cy.Token, map[string]any{"name": "Older", "birthday": "2010-03-04", "filterMode": "ratings", "movieCeiling": "PG-13", "tvCeiling": "TV-14"}, &k)
	if k.Name != "Older" || k.Birthday == nil || k.Birthday.String() != "2010-03-04" || *k.MovieCeiling != api.PG13 || *k.TvCeiling != api.TV14 {
		t.Fatalf("%+v", k)
	}
	e.expect(200, "PUT", id, cy.Token, map[string]any{"name": "Older", "birthday": "2024-01-01"}, &k)
	if *k.MovieCeiling != api.G || *k.TvCeiling != api.TVY {
		t.Fatalf("ceilings left out are suggested from the new birthday: %+v", k)
	}
	var got api.Account
	e.expect(200, "GET", id, e.plain.token, nil, &got)
	if got.Name != "Older" || *got.MovieCeiling != api.G {
		t.Fatalf("%+v", got)
	}
}

// A delete that would take the only active admin with it is refused, so
// first run never reopens. Here that admin is an adult of another's
// household, who goes when its owner removes them and when the owner or the
// household is deleted. With a second admin the same delete goes through.
func TestNoDeleteTakesTheLastAdmin(t *testing.T) {
	for name, target := range map[string]func(e *env, cy api.SignInResult) (path, token string){
		"the owner removes them": func(e *env, cy api.SignInResult) (string, string) {
			return "/profiles/" + itoa(cy.Me.Account.Id), e.plain.token
		},
		"an admin deletes their owner": func(e *env, cy api.SignInResult) (string, string) {
			return "/accounts/" + itoa(e.plain.account.ID), cy.Token
		},
		"an admin deletes their household": func(e *env, cy api.SignInResult) (string, string) {
			return "/households/" + itoa(e.plain.household.ID), cy.Token
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			cy := e.householdAdult(e.plain, "Cy", "cy@example.test")
			first := map[string]any{"kind": "adult", "name": "Admin", "email": e.admin.account.Email, "isAdmin": true, "revoked": true}
			e.expect(200, "PUT", "/accounts/"+itoa(cy.Me.Account.Id), e.admin.token, map[string]any{"kind": "adult", "name": "Cy", "email": "cy@example.test", "isAdmin": true}, nil)
			// An invited admin who has not joined cannot sign in, so is no admin left.
			var pending api.Account
			e.expect(201, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "name": "Dee", "email": "dee@example.test", "isAdmin": true}, &pending)
			e.expect(200, "PUT", "/accounts/"+itoa(e.admin.account.ID), e.admin.token, first, nil)
			path, token := target(e, cy)

			e.expect(409, "DELETE", path, token, nil, nil)
			e.expect(200, "GET", "/accounts", cy.Token, nil, nil)
			e.expect(204, "DELETE", "/accounts/"+itoa(pending.Id), cy.Token, nil, nil)

			first["revoked"] = false
			e.expect(200, "PUT", "/accounts/"+itoa(e.admin.account.ID), cy.Token, first, nil)
			e.expect(204, "DELETE", path, token, nil, nil)
			e.expect(401, "GET", "/me", cy.Token, nil, nil)
			var h api.Health
			e.expect(200, "GET", "/health", "", nil, &h)
			if h.SetupRequired {
				t.Fatal("first run reopened")
			}
		})
	}
}
