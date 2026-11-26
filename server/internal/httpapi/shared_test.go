package httpapi_test

import (
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
)

// putIn puts a profile into another household as well, as an admin does.
func (e *env) putIn(profile int64, h *household.Household) {
	e.t.Helper()
	e.expect(204, "PUT", memberPath(h, profile), e.admin.token, nil, nil)
}

func profileIDs(e *env, token string) []int64 {
	e.t.Helper()
	var page api.AccountPage
	e.expect(200, "GET", "/profiles", token, nil, &page)
	var out []int64
	for _, a := range page.Items {
		out = append(out, a.Id)
	}
	return out
}

// A shared kid and a present adult are in Who's watching on the devices of
// every household they are in, and a device there can switch to them.
func TestASharedKidAppearsInBothHouseholdsWhosWatching(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Mia")
	e.putIn(k.Id, e.admin.household)
	e.putIn(e.plain.account.ID, e.admin.household)
	for _, u := range []user{e.plain, e.admin} {
		if got := profileIDs(e, u.token); !slices.Contains(got, k.Id) {
			t.Fatalf("%s's household lists %v, not the kid", u.account.Name, got)
		}
	}
	if got := profileIDs(e, e.admin.token); !slices.Contains(got, e.plain.account.ID) {
		t.Fatalf("a present adult is not in Who's watching: %v", got)
	}
	if got := profileIDs(e, e.plain.token); slices.Contains(got, e.admin.account.ID) {
		t.Fatalf("the other household's adult is listed: %v", got)
	}
	var me api.Me
	e.expect(200, "GET", "/me", e.admin.token, nil, &me)
	if !slices.ContainsFunc(me.Profiles, func(a api.Account) bool { return a.Id == k.Id }) {
		t.Fatalf("getMe leaves the shared kid out: %+v", me.Profiles)
	}
	e.expect(204, "POST", "/profiles/"+itoa(k.Id)+"/unlock", e.admin.token, map[string]any{}, nil)
	e.expect(200, "GET", "/me", e.admin.token, nil, &me)
	if me.ActiveProfileId != k.Id {
		t.Fatalf("the admin's device acts as %d", me.ActiveProfileId)
	}
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/watch-state", e.admin.token, nil, nil)

	tablet, _ := e.secondDevice(e.admin, "Tablet", "web")
	e.expect(204, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/unlock", tablet, map[string]any{}, nil)
	e.expect(200, "GET", "/me", tablet, nil, &me)
	if me.ActiveProfileId != e.plain.account.ID {
		t.Fatalf("a device of the household cannot switch to a present adult: %d", me.ActiveProfileId)
	}
}

// Profiles say how many households they are in and whether an adult is only
// present in the device's.
func TestProfilesSayTheyAreSharedOrPresent(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Mia")
	if k.Households == nil || *k.Households != 1 || k.Present == nil || *k.Present {
		t.Fatalf("a new kid is in one household: %+v", k)
	}
	e.putIn(k.Id, e.admin.household)
	e.putIn(e.plain.account.ID, e.admin.household)
	type want struct {
		households int
		present    bool
	}
	check := func(as []api.Account, wants map[int64]want) {
		t.Helper()
		for _, a := range as {
			w, ok := wants[a.Id]
			if !ok {
				continue
			}
			if a.Households == nil || *a.Households != w.households || a.Present == nil || *a.Present != w.present {
				t.Fatalf("%s: households %v present %v, want %+v", a.Name, a.Households, a.Present, w)
			}
			delete(wants, a.Id)
		}
		if len(wants) > 0 {
			t.Fatalf("not listed: %v", wants)
		}
	}
	var page api.AccountPage
	e.expect(200, "GET", "/profiles", e.admin.token, nil, &page)
	check(page.Items, map[int64]want{k.Id: {2, false}, e.plain.account.ID: {2, true}, e.admin.account.ID: {1, false}})
	e.expect(200, "GET", "/profiles", e.plain.token, nil, &page)
	check(page.Items, map[int64]want{k.Id: {2, false}, e.plain.account.ID: {2, false}})
	var me api.Me
	e.expect(200, "GET", "/me", e.admin.token, nil, &me)
	check(append(me.Profiles, me.Account), map[int64]want{k.Id: {2, false}, e.plain.account.ID: {2, true}})
	var one api.Account
	e.expect(200, "GET", "/profiles/"+itoa(k.Id), e.admin.token, nil, &one)
	check([]api.Account{one}, map[int64]want{k.Id: {2, false}})
}

// presentDevice signs u in on a device put in another household they are
// present in.
func (e *env) presentDevice(u user, h *household.Household) string {
	e.t.Helper()
	token, _, err := e.svc.Devices.Register(u.account.ID, h.ID, "Phone", "ios")
	must(e.t, err)
	return token
}

// An adult present in another household watches there but manages
// nothing of it; their own profile stays theirs.
func TestAPresentAdultManagesNothingThere(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Film")
	k := e.kid(e.admin, "Ned")
	e.putIn(e.plain.account.ID, e.admin.household)
	var c api.Collection
	e.expect(201, "POST", "/collections", e.admin.token, map[string]any{"name": "Kites"}, &c)
	link := e.newLink()
	kid, self := "/profiles/"+itoa(k.Id), "/profiles/"+itoa(e.plain.account.ID)
	there := e.presentDevice(e.plain, e.admin.household)
	switched, _ := e.secondDevice(e.admin, "Tablet", "web")
	e.expect(204, "POST", self+"/unlock", switched, map[string]any{}, nil)
	for _, token := range []string{there, switched} {
		for _, r := range []struct {
			method, path string
			body         any
		}{
			{"POST", "/profiles", map[string]any{"name": "New", "birthday": "2018-05-01"}},
			{"PUT", kid, map[string]any{"name": "Renamed", "birthday": "2018-05-01"}},
			{"DELETE", kid, nil},
			{"PUT", kid + "/pin", map[string]any{"pin": "1234"}},
			{"DELETE", kid + "/pin", nil},
			{"PUT", kid + "/allowed-items/" + itoa(m), nil},
			{"DELETE", kid + "/allowed-items/" + itoa(m), nil},
			{"PUT", kid + "/preferences", map[string]any{"autoplay": false}},
			{"POST", kid + "/make-adult", map[string]any{"email": "ned@example.test"}},
			{"POST", "/household/adults", map[string]any{"name": "Dee", "email": "dee@example.test"}},
			{"GET", "/household/device-links", nil},
			{"DELETE", "/household/device-links/1", nil},
			{"GET", "/link/" + link.Code, nil},
			{"POST", "/link/" + link.Code + "/approve", nil},
			{"POST", "/link/" + link.Code + "/deny", nil},
			{"POST", "/collections", map[string]any{"name": "Mine"}},
			{"PUT", "/collections/" + itoa(c.Id), map[string]any{"name": "Theirs"}},
			{"PUT", "/collections/" + itoa(c.Id) + "/movies/" + itoa(m), nil},
			{"DELETE", "/collections/" + itoa(c.Id), nil},
		} {
			if got, raw := e.do(r.method, r.path, token, r.body, nil); got != 403 {
				t.Fatalf("%s %s = %d, want 403: %s", r.method, r.path, got, raw)
			}
		}
	}
	e.expect(204, "PUT", self+"/pin", there, map[string]any{"pin": "1234"}, nil)
	e.expect(200, "PUT", self+"/preferences", there, map[string]any{"autoplay": false}, nil)
	e.expect(200, "PUT", self+"/watch-state/"+itoa(m), there, map[string]any{"watched": true}, nil)
	e.expect(200, "GET", "/collections/"+itoa(c.Id), there, nil, nil)
	e.expect(201, "POST", "/profiles", e.plain.token, map[string]any{"name": "Own", "birthday": "2018-05-01"}, nil)
}

// Deleting a profile another household holds only takes it out of the
// device's; deleting it from its last household deletes it.
func TestDeletingASharedProfileOnlyTakesItOutOfTheHousehold(t *testing.T) {
	e := newEnv(t)
	k, l := e.kid(e.plain, "Mia"), e.kid(e.plain, "Leo")
	for _, id := range []int64{k.Id, l.Id, e.plain.account.ID} {
		e.putIn(id, e.admin.household)
	}
	e.expect(204, "DELETE", "/profiles/"+itoa(k.Id), e.admin.token, nil, nil)
	if e.accountGone(k.Id) || slices.Contains(profileIDs(e, e.admin.token), k.Id) || !slices.Contains(profileIDs(e, e.plain.token), k.Id) {
		t.Fatal("the kid did not just leave the household it was deleted from")
	}
	e.expect(204, "DELETE", "/profiles/"+itoa(k.Id), e.plain.token, nil, nil)
	if !e.accountGone(k.Id) {
		t.Fatal("a kid deleted from its last household stays")
	}

	e.expect(204, "DELETE", "/profiles/"+itoa(l.Id), e.plain.token, nil, nil)
	if got := e.adminAccount(l.Id); got.HouseholdId == nil || *got.HouseholdId != e.admin.household.ID {
		t.Fatalf("the kid's other household is not its own now: %+v", got)
	}

	e.expect(204, "DELETE", "/profiles/"+itoa(e.plain.account.ID), e.admin.token, nil, nil)
	if slices.Contains(profileIDs(e, e.admin.token), e.plain.account.ID) {
		t.Fatal("a present adult removed is still there")
	}
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)

	e.putIn(e.plain.account.ID, e.admin.household)
	there := e.presentDevice(e.plain, e.admin.household)
	e.expect(403, "DELETE", "/profiles/"+itoa(e.admin.account.ID), there, nil, nil)
	e.expect(204, "DELETE", "/profiles/"+itoa(e.plain.account.ID), there, nil, nil)
	e.expect(401, "GET", "/me", there, nil, nil)
	if e.accountGone(e.plain.account.ID) || slices.Contains(profileIDs(e, e.admin.token), e.plain.account.ID) {
		t.Fatal("a present adult did not just take themselves out")
	}
}

// Deleting a household, or the only adult of one, deletes only the
// profiles no other household holds.
func TestDeletingAHouseholdKeepsProfilesHeldElsewhere(t *testing.T) {
	for name, del := range map[string]func(e *env, owner user) []api.AccountDeletion{
		"an admin deletes the household": func(e *env, owner user) []api.AccountDeletion {
			e.expect(204, "DELETE", "/households/"+itoa(owner.household.ID), e.admin.token, nil, nil)
			return nil
		},
		"an admin deletes its owner": func(e *env, owner user) []api.AccountDeletion {
			e.expect(204, "DELETE", "/accounts/"+itoa(owner.account.ID), e.admin.token, nil, nil)
			return nil
		},
		"its owner deletes their account": func(e *env, owner user) []api.AccountDeletion {
			e.withPassword(owner)
			plan := e.deletion(owner.token)
			e.deleteSelf(204, owner.token, password)
			return []api.AccountDeletion{plan}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			owner := e.plain
			alone, shared := e.kid(owner, "Ivy"), e.kid(owner, "Ava")
			e.putIn(shared.Id, e.admin.household)
			e.putIn(e.admin.account.ID, owner.household)
			visiting := e.presentDevice(e.admin, owner.household)
			for _, plan := range del(e, owner) {
				if plan.Outcome != api.AccountDeletionOutcomeHousehold || !slices.Equal(plan.KidNames, []string{"Ivy"}) {
					t.Fatalf("only the kid no other household holds is named: %+v", plan)
				}
			}
			if !e.accountGone(alone.Id) {
				t.Fatal("a kid only that household held stays")
			}
			if e.accountGone(shared.Id) || e.accountGone(e.admin.account.ID) {
				t.Fatal("a profile another household holds was deleted")
			}
			if got := e.adminAccount(shared.Id); got.HouseholdId == nil || *got.HouseholdId != e.admin.household.ID {
				t.Fatalf("the shared kid's other household is not its own: %+v", got)
			}
			if got := *e.adminAccount(e.admin.account.ID).HouseholdIds; !slices.Equal(got, []int64{e.admin.household.ID}) {
				t.Fatalf("the present adult keeps only their own household: %v", got)
			}
			e.expect(401, "GET", "/me", visiting, nil, nil)
			if !slices.Contains(profileIDs(e, e.admin.token), shared.Id) {
				t.Fatal("the shared kid left its other household")
			}
		})
	}
}

// A shared kid made an adult is an adult of the household that made them,
// and of no other.
func TestASharedKidMadeAnAdultLeavesTheirOtherHouseholds(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Mia")
	e.putIn(k.Id, e.admin.household)
	e.expect(201, "POST", "/profiles/"+itoa(k.Id)+"/make-adult", e.admin.token, map[string]any{"email": "mia@example.test"}, nil)
	got := e.adminAccount(k.Id)
	if got.Kind != api.Adult || got.HouseholdId == nil || *got.HouseholdId != e.admin.household.ID ||
		!slices.Equal(*got.HouseholdIds, []int64{e.admin.household.ID}) {
		t.Fatalf("not an adult of the household that made them alone: %+v", got)
	}
	if slices.Contains(profileIDs(e, e.plain.token), k.Id) {
		t.Fatal("still in their other household")
	}
}

// Signing in starts a device in the adult's own household and lists their
// others; the device moves to any of them and back.
func TestAnAdultInTwoHouseholdsMovesTheirDevice(t *testing.T) {
	e := newEnv(t)
	e.withPassword(e.plain)
	e.withPassword(e.admin)
	e.putIn(e.plain.account.ID, e.admin.household)
	signIn := func(email string) api.SignInResult {
		var res api.SignInResult
		e.expect(201, "POST", "/auth/sign-in", "", device("email", email, "password", password), &res)
		return res
	}
	res := signIn(e.plain.account.Email)
	want := []api.HouseholdChoice{{Id: e.plain.household.ID, Name: e.plain.household.Name}, {Id: e.admin.household.ID, Name: e.admin.household.Name}}
	if res.Households == nil || !slices.Equal(*res.Households, want) || res.Me.Household.Id != e.plain.household.ID {
		t.Fatalf("own household first, the device in it: %+v %+v", res.Households, res.Me.Household)
	}
	if other := signIn(e.admin.account.Email); other.Households != nil {
		t.Fatalf("one household lists none: %+v", other.Households)
	}

	moveTo := func(want int, token string, h int64) {
		t.Helper()
		e.expect(want, "PUT", "/me/household", token, map[string]any{"householdId": h}, nil)
	}
	k := e.kid(e.plain, "Mia")
	e.expect(204, "POST", "/profiles/"+itoa(k.Id)+"/unlock", res.Token, map[string]any{}, nil)
	moveTo(403, res.Token, e.admin.household.ID)
	e.expect(204, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/unlock", res.Token, map[string]any{}, nil)
	third := e.adult("third@example.test", false)
	moveTo(404, res.Token, third.household.ID)

	moveTo(204, res.Token, e.admin.household.ID)
	var me api.Me
	e.expect(200, "GET", "/me", res.Token, nil, &me)
	if me.Household.Id != e.admin.household.ID || me.ActiveProfileId != e.plain.account.ID || me.Account.Present == nil || !*me.Account.Present {
		t.Fatalf("not moved, as the adult, present: %+v", me)
	}
	if !slices.Contains(profileIDs(e, res.Token), e.admin.account.ID) || slices.Contains(profileIDs(e, res.Token), k.Id) {
		t.Fatal("the moved device does not show the household it is in")
	}
	e.expect(403, "POST", "/profiles", res.Token, map[string]any{"name": "New", "birthday": "2018-05-01"}, nil)
	moveTo(204, res.Token, e.plain.household.ID)
	e.expect(201, "POST", "/profiles", res.Token, map[string]any{"name": "New", "birthday": "2018-05-01"}, nil)
}

// Only an admin links adults, and only joined adults of different
// households, once.
func TestAnAdminLinksAdultsOfDifferentHouseholds(t *testing.T) {
	e := newEnv(t)
	plain, admin := e.plain.account.ID, e.admin.account.ID
	body := func(a, b int64) map[string]any { return map[string]any{"accountId": a, "otherAccountId": b} }
	e.expect(403, "POST", "/adult-links", e.plain.token, body(plain, admin), nil)
	var l api.AdultLink
	e.expect(201, "POST", "/adult-links", e.admin.token, body(plain, admin), &l)
	if l.AccountId != min(plain, admin) || l.OtherAccountId != max(plain, admin) || l.Tv {
		t.Fatalf("lower id first, off TVs: %+v", l)
	}
	e.expect(409, "POST", "/adult-links", e.admin.token, body(admin, plain), nil)

	k := e.kid(e.plain, "Mia")
	var inv api.HouseholdInvite
	e.expect(201, "POST", "/household/adults", e.admin.token, map[string]any{"name": "Dee", "email": "dee@example.test"}, &inv)
	cy := e.member(e.plain, "Cy", "cy@example.test")
	for _, other := range []int64{plain, k.Id, inv.Account.Id, 999} {
		e.expect(400, "POST", "/adult-links", e.admin.token, body(plain, other), nil)
	}
	e.expect(409, "POST", "/adult-links", e.admin.token, body(cy.account.ID, plain), nil)

	var page api.AdultLinkPage
	e.expect(200, "GET", "/adult-links", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0] != l {
		t.Fatalf("%+v", page)
	}
	path := "/adult-links/" + itoa(admin) + "/" + itoa(plain)
	e.expect(200, "GET", path, e.admin.token, nil, &l)
	e.expect(200, "PUT", path, e.admin.token, map[string]any{"tv": true}, &l)
	if !l.Tv {
		t.Fatalf("not on TVs: %+v", l)
	}
	e.expect(403, "DELETE", path, e.plain.token, nil, nil)
	e.expect(204, "DELETE", path, e.admin.token, nil, nil)
	e.expect(404, "DELETE", path, e.admin.token, nil, nil)
	e.expect(404, "GET", path, e.admin.token, nil, nil)
}

func (e *env) adminAccount(id int64) api.Account {
	e.t.Helper()
	var a api.Account
	e.expect(200, "GET", "/accounts/"+itoa(id), e.admin.token, nil, &a)
	if a.HouseholdIds == nil {
		e.t.Fatalf("an admin's account has no householdIds: %+v", a)
	}
	return a
}

func memberPath(h *household.Household, id int64) string {
	return "/households/" + itoa(h.ID) + "/members/" + itoa(id)
}

// An admin puts a profile into a household and takes it out again; a kid
// whose household lets it go moves to another it is in, and is never
// taken out of its last.
func TestAnAdminPutsAProfileIntoAHouseholdAndTakesItOut(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Mia")
	into := memberPath
	e.expect(403, "PUT", into(e.admin.household, k.Id), e.plain.token, nil, nil)
	e.expect(404, "PUT", "/households/999/members/"+itoa(k.Id), e.admin.token, nil, nil)
	e.expect(404, "PUT", into(e.admin.household, 999), e.admin.token, nil, nil)
	var inv api.HouseholdInvite
	e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Dee", "email": "dee@example.test"}, &inv)
	e.expect(409, "PUT", into(e.admin.household, inv.Account.Id), e.admin.token, nil, nil)

	e.expect(204, "PUT", into(e.admin.household, k.Id), e.admin.token, nil, nil)
	e.expect(204, "PUT", into(e.admin.household, k.Id), e.admin.token, nil, nil)
	if got := *e.adminAccount(k.Id).HouseholdIds; !slices.Equal(got, []int64{e.plain.household.ID, e.admin.household.ID}) {
		t.Fatalf("own household first, then the other: %v", got)
	}
	if !slices.Contains(profileIDs(e, e.admin.token), k.Id) {
		t.Fatal("the kid is not in the household it was put into")
	}

	tablet, _ := e.secondDevice(e.admin, "Tablet", "web")
	e.expect(204, "POST", "/profiles/"+itoa(k.Id)+"/unlock", tablet, map[string]any{}, nil)
	e.expect(204, "DELETE", into(e.admin.household, k.Id), e.admin.token, nil, nil)
	var me api.Me
	e.expect(200, "GET", "/me", tablet, nil, &me)
	if me.ActiveProfileId != e.admin.account.ID || me.Locked == nil || !*me.Locked {
		t.Fatalf("a device acting as a profile that left goes back to Who's watching: %+v", me)
	}
	if slices.Contains(profileIDs(e, e.admin.token), k.Id) {
		t.Fatal("the kid is still in the household it left")
	}
	e.expect(404, "DELETE", into(e.admin.household, k.Id), e.admin.token, nil, nil)
	e.expect(409, "DELETE", into(e.plain.household, k.Id), e.admin.token, nil, nil)

	e.expect(204, "PUT", into(e.admin.household, k.Id), e.admin.token, nil, nil)
	e.expect(204, "DELETE", into(e.plain.household, k.Id), e.admin.token, nil, nil)
	moved := e.adminAccount(k.Id)
	if moved.HouseholdId == nil || *moved.HouseholdId != e.admin.household.ID || !slices.Equal(*moved.HouseholdIds, []int64{e.admin.household.ID}) {
		t.Fatalf("the kid's other household becomes its own: %+v", moved)
	}
	if slices.Contains(profileIDs(e, e.plain.token), k.Id) {
		t.Fatal("the kid is still in the household it left")
	}

	// A present adult's devices put in the household they leave sign out.
	e.expect(204, "PUT", into(e.admin.household, e.plain.account.ID), e.admin.token, nil, nil)
	there, _, err := e.svc.Devices.Register(e.plain.account.ID, e.admin.household.ID, "Phone", "ios")
	must(t, err)
	e.expect(204, "DELETE", into(e.admin.household, e.plain.account.ID), e.admin.token, nil, nil)
	e.expect(401, "GET", "/me", there, nil, nil)
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)
}
