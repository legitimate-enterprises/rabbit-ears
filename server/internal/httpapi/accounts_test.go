package httpapi_test

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/httpapi"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// bareEnv is a server over an empty database, before first run.
func bareEnv(t *testing.T) *env {
	t.Helper()
	svc := service.New(dbtest.Open(t))
	h, err := httpapi.NewHandler("1.2.3", svc)
	must(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &env{t: t, url: srv.URL + httpapi.BasePath, svc: svc}
}

func device(kv ...any) map[string]any {
	m := map[string]any{"deviceName": "Mac", "platform": "macos"}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

func TestFirstRunCreatesTheOnlyAdmin(t *testing.T) {
	e := bareEnv(t)
	var h api.Health
	e.expect(200, "GET", "/health", "", nil, &h)
	if !h.SetupRequired || h.ServerName != nil {
		t.Fatalf("%+v", h)
	}
	e.expect(400, "POST", "/setup", "", device("serverName", "Den", "name", "Ada", "email", "ada@example.test", "password", "short"), nil)

	var res api.SignInResult
	e.expect(201, "POST", "/setup", "", device("serverName", "Den", "name", "Ada", "email", "ada@example.test", "password", "longenough"), &res)
	if res.Token == "" || !res.Me.Account.IsAdmin || res.Me.Household == nil || res.Me.ActiveProfileId != res.Me.Account.Id {
		t.Fatalf("%+v", res)
	}
	e.expect(200, "GET", "/accounts", res.Token, nil, nil)
	e.expect(200, "GET", "/health", "", nil, &h)
	if h.SetupRequired || h.ServerName == nil || *h.ServerName != "Den" {
		t.Fatalf("%+v", h)
	}
	raw := e.expect(409, "POST", "/setup", "", device("serverName", "X", "name", "Eve", "email", "eve@example.test", "password", "longenough"), nil)
	if errorCode(t, raw) != "conflict" {
		t.Fatal(string(raw))
	}

	// The last admin cannot remove themselves and reopen first run.
	self := "/accounts/" + itoa(res.Me.Account.Id)
	e.expect(409, "PUT", self, res.Token, map[string]any{"kind": "adult", "name": "Ada", "email": "ada@example.test", "isAdmin": false}, nil)
	e.expect(409, "PUT", self, res.Token, map[string]any{"kind": "adult", "name": "Ada", "email": "ada@example.test", "isAdmin": true, "revoked": true}, nil)
	e.expect(409, "DELETE", self, res.Token, nil, nil)
}

// addAdult is the admin's Add adult flow: a pending account and its code.
func (e *env) addAdult(email string, days int) (api.Account, api.JoinCode) {
	e.t.Helper()
	var a api.Account
	e.expect(201, "POST", "/accounts", e.admin.token, map[string]any{"kind": "adult", "name": "Bea", "email": email}, &a)
	body := map[string]any{"accountId": a.Id}
	if days != 0 {
		body["expiresInDays"] = days
	}
	var j api.JoinCode
	e.expect(201, "POST", "/join-codes", e.admin.token, body, &j)
	return a, j
}

func TestJoinLinkAcceptSignIn(t *testing.T) {
	e := newEnv(t)
	a, j := e.addAdult("bea@example.test", 0)
	if j.Link != "/join/"+j.Code || time.Until(j.ExpiresAt) < 6*24*time.Hour || time.Until(j.ExpiresAt) > 7*24*time.Hour+time.Minute {
		t.Fatalf("default expiry is 7 days: %+v", j)
	}
	var p api.JoinPreview
	e.expect(200, "GET", j.Link, "", nil, &p)
	if p.Name != "Bea" || p.InvitedBy == nil || *p.InvitedBy != e.admin.account.Name {
		t.Fatalf("%+v", p)
	}
	e.expect(401, "POST", "/auth/sign-in", "", device("email", "bea@example.test", "password", "longenough"), nil)

	var res api.SignInResult
	e.expect(201, "POST", j.Link, "", device("name", "Beatrice", "password", "longenough"), &res)
	if res.Me.Account.Id != a.Id || res.Me.Account.Name != "Beatrice" || res.Me.Account.Pending || res.Me.Household == nil ||
		res.Me.Device.HouseholdId != res.Me.Household.Id || len(res.Me.Profiles) != 1 {
		t.Fatalf("%+v", res.Me)
	}
	e.expect(404, "GET", j.Link, "", nil, nil)
	e.expect(404, "POST", j.Link, "", device("name", "Again", "password", "longenough"), nil)

	var in api.SignInResult
	e.expect(201, "POST", "/auth/sign-in", "", device("email", "BEA@example.test", "password", "longenough"), &in)
	if in.Token == res.Token || in.Me.Household.Id != res.Me.Household.Id || in.Me.ActiveProfileId != a.Id {
		t.Fatalf("%+v", in.Me)
	}
	e.expect(401, "POST", "/auth/sign-in", "", device("email", "bea@example.test", "password", "wrongwrong"), nil)
	e.expect(401, "POST", "/auth/sign-in", "", device("email", "nobody@example.test", "password", "longenough"), nil)

	// Revoking the adult signs every device out and refuses sign in.
	e.expect(200, "PUT", "/accounts/"+itoa(a.Id), e.admin.token, map[string]any{"kind": "adult", "name": "Beatrice", "email": "bea@example.test", "revoked": true}, nil)
	e.expect(401, "GET", "/me", in.Token, nil, nil)
	e.expect(401, "POST", "/auth/sign-in", "", device("email", "bea@example.test", "password", "longenough"), nil)
}

func TestJoinCodeExpiryAndRevoke(t *testing.T) {
	e := newEnv(t)
	_, j := e.addAdult("cy@example.test", 30)
	if d := time.Until(j.ExpiresAt); d < 29*24*time.Hour {
		t.Fatalf("%v", d)
	}
	e.expect(204, "DELETE", "/join-codes/"+itoa(j.Id), e.admin.token, nil, nil)
	e.expect(404, "GET", j.Link, "", nil, nil)
	e.expect(404, "POST", j.Link, "", device("name", "Cy", "password", "longenough"), nil)

	var j2 api.JoinCode
	e.expect(201, "POST", "/join-codes", e.admin.token, map[string]any{"accountId": j.AccountId, "expiresInDays": 1}, &j2)
	row, err := e.svc.JoinCodes.Repo().Get(j2.Id)
	must(t, err)
	row.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	must(t, e.svc.JoinCodes.Repo().Update(row))
	e.expect(404, "GET", j2.Link, "", nil, nil)
	e.expect(404, "POST", j2.Link, "", device("name", "Cy", "password", "longenough"), nil)
}

func TestCeilingSuggestionAndDefaults(t *testing.T) {
	e := newEnv(t)
	bday := time.Now().UTC().AddDate(-9, 0, -1).Format(time.DateOnly)
	var s api.CeilingSuggestion
	e.expect(200, "GET", "/ratings/suggestion?birthday="+bday, e.plain.token, nil, &s)
	if s.Age != 9 || s.MovieCeiling != api.PG || s.TvCeiling != api.TVY7 {
		t.Fatalf("%+v", s)
	}
	e.expect(400, "GET", "/ratings/suggestion", e.plain.token, nil, nil)
	var k api.Account
	e.expect(201, "POST", "/profiles", e.plain.token, map[string]any{"name": "Kid", "birthday": bday}, &k)
	if k.FilterMode != api.FilterModeRatings || k.MovieCeiling == nil || *k.MovieCeiling != api.PG || *k.TvCeiling != api.TVY7 || k.HasPin {
		t.Fatalf("%+v", k)
	}
}

func (e *env) unlock(u user, id int64, pin string) (int, []byte) {
	e.t.Helper()
	body := map[string]any{}
	if pin != "" {
		body["pin"] = pin
	}
	return e.do("POST", "/profiles/"+itoa(id)+"/unlock", u.token, body, nil)
}

func (e *env) locked(token string) bool {
	e.t.Helper()
	var me api.Me
	e.expect(200, "GET", "/me", token, nil, &me)
	return me.Locked != nil && *me.Locked
}

// A device locked at Who's watching reaches nothing until a profile is
// picked, so the profile active before, and its PIN, are out of reach.
// Picking clears the lock only when the profile's PIN is right; the
// household's other devices are unaffected, and an admin's loses admin too.
func TestLockedDeviceWaitsForAPick(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	adult := "/profiles/" + itoa(e.plain.account.ID)
	e.expect(204, "PUT", adult+"/pin", e.plain.token, map[string]any{"pin": "1234"}, nil)
	tablet, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "Tablet", "web")
	must(t, err)
	if e.locked(e.plain.token) {
		t.Fatal("a signed-in device starts locked")
	}

	e.expect(204, "POST", "/me/lock", e.plain.token, nil, nil)
	if !e.locked(e.plain.token) {
		t.Fatal("/me does not say locked")
	}
	refused := func() {
		t.Helper()
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", "/profiles", nil},
			{"POST", "/profiles", map[string]any{"name": "Sneaky", "birthday": "2015-01-01"}},
			{"PUT", adult + "/pin", map[string]any{"pin": "0000"}},
			{"POST", "/household/adults", map[string]any{"name": "Cy", "email": "cy@example.test"}},
		} {
			if st, raw := e.do(c.method, c.path, e.plain.token, c.body, nil); st != 403 || errorCode(t, raw) != "device_locked" {
				t.Fatalf("%s %s while locked = %d %s", c.method, c.path, st, raw)
			}
		}
	}
	refused()
	e.expect(200, "GET", "/profiles", tablet, nil, nil)

	if st, raw := e.unlock(e.plain, e.plain.account.ID, "0000"); st != 403 || errorCode(t, raw) != "forbidden" {
		t.Fatalf("wrong PIN: %d %s", st, raw)
	}
	refused()
	if st, raw := e.unlock(e.plain, e.plain.account.ID, "1234"); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	if e.locked(e.plain.token) {
		t.Fatal("still locked after the right PIN")
	}
	e.expect(200, "GET", "/profiles", e.plain.token, nil, nil)

	e.expect(204, "POST", "/me/lock", e.plain.token, nil, nil)
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("a kid without a PIN: %d %s", st, raw)
	}
	if e.locked(e.plain.token) {
		t.Fatal("still locked after picking a kid")
	}
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/watch-state", e.plain.token, nil, nil)

	e.expect(200, "GET", "/accounts", e.admin.token, nil, nil)
	e.expect(204, "POST", "/me/lock", e.admin.token, nil, nil)
	if st, raw := e.do("GET", "/accounts", e.admin.token, nil, nil); st != 403 || errorCode(t, raw) != "device_locked" {
		t.Fatalf("admin API while locked = %d %s", st, raw)
	}
}

func TestPINSwitchLockoutAndReset(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	kp := "/profiles/" + itoa(k.Id)
	e.expect(204, "PUT", kp+"/pin", e.plain.token, map[string]any{"pin": "4321"}, nil)
	var acc api.Account
	e.expect(200, "GET", kp, e.plain.token, nil, &acc)
	if !acc.HasPin || acc.PinLocked {
		t.Fatalf("%+v", acc)
	}

	if st, raw := e.unlock(e.plain, k.Id, ""); st != 403 || errorCode(t, raw) != "pin_required" {
		t.Fatalf("%d %s", st, raw)
	}
	for i := 1; i <= 5; i++ {
		st, raw := e.unlock(e.plain, k.Id, "0000")
		want := "forbidden"
		if i == 5 {
			want = "pin_locked"
		}
		if st != 403 || errorCode(t, raw) != want {
			t.Fatalf("failure %d: %d %s", i, st, raw)
		}
	}
	if st, raw := e.unlock(e.plain, k.Id, "4321"); st != 403 || errorCode(t, raw) != "pin_locked" {
		t.Fatalf("right PIN while locked: %d %s", st, raw)
	}
	e.expect(200, "GET", kp, e.plain.token, nil, &acc)
	if !acc.PinLocked {
		t.Fatalf("%+v", acc)
	}
	// An adult of the household resets it, which clears the lock.
	e.expect(204, "PUT", kp+"/pin", e.plain.token, map[string]any{"pin": "1111"}, nil)
	if st, raw := e.unlock(e.plain, k.Id, "1111"); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	var me api.Me
	e.expect(200, "GET", "/me", e.plain.token, nil, &me)
	if me.ActiveProfileId != k.Id || len(me.Profiles) != 2 {
		t.Fatalf("%+v", me)
	}

	// As the kid, adult-only operations are refused and the adult is out of reach.
	adult := "/profiles/" + itoa(e.plain.account.ID)
	e.expect(403, "POST", "/profiles", e.plain.token, map[string]any{"name": "Sneaky", "birthday": "2015-01-01"}, nil)
	e.expect(403, "PUT", kp, e.plain.token, map[string]any{"name": "Kid", "birthday": "2018-05-01", "movieCeiling": "R"}, nil)
	e.expect(403, "PUT", kp+"/pin", e.plain.token, map[string]any{"pin": "2222"}, nil)
	e.expect(403, "DELETE", kp+"/pin", e.plain.token, nil, nil)
	e.expect(403, "GET", adult+"/watch-state", e.plain.token, nil, nil)
	e.expect(200, "GET", kp+"/watch-state", e.plain.token, nil, nil)

	// Switching back to an adult without a PIN is free; with one it is not.
	if st, raw := e.unlock(e.plain, e.plain.account.ID, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(204, "PUT", adult+"/pin", e.plain.token, map[string]any{"pin": "9999"}, nil)
	e.expect(204, "DELETE", kp+"/pin", e.plain.token, nil, nil)
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("a kid without a PIN is free: %d %s", st, raw)
	}
	if st, raw := e.unlock(e.plain, e.plain.account.ID, ""); st != 403 || errorCode(t, raw) != "pin_required" {
		t.Fatalf("%d %s", st, raw)
	}
	if st, raw := e.unlock(e.plain, e.plain.account.ID, "9999"); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}

	// Another household cannot touch this kid's PIN.
	e.expect(404, "PUT", kp+"/pin", e.admin.token, map[string]any{"pin": "1234"}, nil)
	if st, _ := e.unlock(e.admin, k.Id, "1111"); st != 404 {
		t.Fatalf("%d", st)
	}
}

func TestAdminScopeNeedsTheAdultActive(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.admin, "Kid")
	e.expect(200, "GET", "/accounts", e.admin.token, nil, nil)
	if st, raw := e.unlock(e.admin, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(403, "GET", "/accounts", e.admin.token, nil, nil)
	e.expect(403, "POST", "/libraries", e.admin.token, map[string]any{"name": "L", "kind": "movies"}, nil)
}

// Each device sees its own adult and kids and nothing of the other
// household, whichever profile it addresses.
func TestTwoHouseholdsAreIsolated(t *testing.T) {
	e := newEnv(t)
	m, _ := e.playable()
	ka, kb := e.kid(e.admin, "Ann"), e.kid(e.plain, "Ben")
	for _, c := range []struct {
		u      user
		mine   []int64
		theirs []int64
	}{
		{e.admin, []int64{e.admin.account.ID, ka.Id}, []int64{e.plain.account.ID, kb.Id}},
		{e.plain, []int64{e.plain.account.ID, kb.Id}, []int64{e.admin.account.ID, ka.Id}},
	} {
		var me api.Me
		e.expect(200, "GET", "/me", c.u.token, nil, &me)
		var page api.AccountPage
		e.expect(200, "GET", "/profiles", c.u.token, nil, &page)
		if len(me.Profiles) != 2 || len(page.Items) != 2 {
			t.Fatalf("%+v %+v", me.Profiles, page.Items)
		}
		for i, id := range c.mine {
			if me.Profiles[i].Id != id || page.Items[i].Id != id {
				t.Fatalf("profile %d: %+v", i, me.Profiles)
			}
			e.expect(200, "GET", "/profiles/"+itoa(id), c.u.token, nil, nil)
		}
		for _, id := range c.theirs {
			p := "/profiles/" + itoa(id)
			e.expect(404, "GET", p, c.u.token, nil, nil)
			e.expect(404, "GET", p+"/watch-state", c.u.token, nil, nil)
			e.expect(404, "PUT", p+"/watch-state/"+itoa(m), c.u.token, map[string]any{"watched": true}, nil)
			e.expect(404, "GET", p+"/allowed-items", c.u.token, nil, nil)
			e.expect(404, "PUT", p+"/pin", c.u.token, map[string]any{"pin": "1234"}, nil)
			e.expect(404, "DELETE", p+"/pin", c.u.token, nil, nil)
			e.expect(404, "POST", p+"/unlock", c.u.token, map[string]any{}, nil)
			e.expect(404, "POST", "/playback/sessions", c.u.token, map[string]any{"profileId": id, "itemId": m, "snapshot": snapshot()}, nil)
		}
	}
	// Watch state is per profile even within a household.
	e.expect(200, "PUT", "/profiles/"+itoa(kb.Id)+"/watch-state/"+itoa(m), e.plain.token, map[string]any{"watched": true}, nil)
	e.expect(404, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(m), e.plain.token, nil, nil)
}

// catalog is a movies library with one film per rating plus an unrated one,
// a kids' show and an adult show with an episode each, and a music library.
type catalog struct {
	g, pg, r, unrated, kidShow, kidEp, adultShow, adultEp, music int64
}

func (e *env) rate(id int64, rating string) {
	e.t.Helper()
	e.expect(200, "PUT", "/items/"+itoa(id)+"/metadata", e.admin.token, map[string]any{"status": "matched", "contentRating": rating}, nil)
}

func (e *env) show(lib int64, title, rating string) (show, ep int64) {
	e.t.Helper()
	var s, se, ep1 api.Item
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": lib, "kind": "show", "parsedTitle": title}, &s)
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": lib, "parentId": s.Id, "kind": "season", "parsedTitle": title, "seasonNumber": 1}, &se)
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": lib, "parentId": se.Id, "kind": "episode", "parsedTitle": title, "episodeNumber": 1}, &ep1)
	e.rate(s.Id, rating)
	return s.Id, ep1.Id
}

func (e *env) catalog() catalog {
	e.t.Helper()
	movies, tv := e.library("movies"), e.library("tv")
	var c catalog
	c.g, c.pg, c.r, c.unrated = e.movie(movies, "G film"), e.movie(movies, "PG film"), e.movie(movies, "R film"), e.movie(movies, "Unrated film")
	e.rate(c.g, "G")
	e.rate(c.pg, "PG")
	e.rate(c.r, "R")
	c.kidShow, c.kidEp = e.show(tv, "Kid show", "TV-Y7")
	c.adultShow, c.adultEp = e.show(tv, "Adult show", "TV-MA")
	c.music = e.library("music")
	return c
}

func ids(items []api.Item) map[int64]bool {
	out := map[int64]bool{}
	for _, it := range items {
		out[it.Id] = true
	}
	return out
}

// With a kid active, no listing or detail endpoint answers with anything
// above the kid's ceilings.
func TestKidCeilingsAtTheAPIBoundary(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	var f api.MediaFile
	e.expect(201, "POST", "/media-files", e.admin.token, map[string]any{"itemId": c.r, "path": e.onDisk("/films/r.mp4"), "sizeBytes": 1, "modifiedAt": "2026-01-01T00:00:00Z"}, &f)
	k := e.kid(e.plain, "Kid") // PG and TV-Y7

	var page api.ItemPage
	e.expect(200, "GET", "/items?limit=200", e.plain.token, nil, &page)
	if len(page.Items) != 10 {
		t.Fatalf("the adult sees everything: %d", len(page.Items))
	}
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}

	e.expect(200, "GET", "/items?limit=200", e.plain.token, nil, &page)
	got := ids(page.Items)
	for _, id := range []int64{c.g, c.pg, c.kidShow, c.kidEp} {
		if !got[id] {
			t.Errorf("item %d missing for the kid", id)
		}
	}
	for _, id := range []int64{c.r, c.unrated, c.adultShow, c.adultEp} {
		if got[id] {
			t.Errorf("item %d above the ceiling was listed", id)
		}
		e.expect(404, "GET", "/items/"+itoa(id), e.plain.token, nil, nil)
	}
	if len(page.Items) != 5 { // two films, the kid show, its season and episode
		t.Errorf("%d items: %+v", len(page.Items), got)
	}
	e.expect(200, "GET", "/items?kind=movie&limit=1", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.NextOffset == nil {
		t.Fatalf("paging counts only visible items: %+v", page)
	}
	e.expect(200, "GET", "/items?parentId="+itoa(c.adultShow), e.plain.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "GET", "/items/"+itoa(c.kidEp), e.plain.token, nil, nil)
	e.expect(404, "GET", "/media-files?itemId="+itoa(c.r), e.plain.token, nil, nil)
	e.expect(404, "GET", "/media-files/"+itoa(f.Id), e.plain.token, nil, nil)
	e.expect(404, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": k.Id, "itemId": c.r, "snapshot": snapshot()}, nil)

	var libs api.LibraryPage
	e.expect(200, "GET", "/libraries", e.plain.token, nil, &libs)
	if len(libs.Items) != 3 {
		t.Fatalf("kids see every library: %+v", libs.Items)
	}
	e.expect(200, "GET", "/libraries/"+itoa(c.music), e.plain.token, nil, nil)

	// The adult playing as the kid is held to the kid's ceilings too.
	if st, raw := e.unlock(e.plain, e.plain.account.ID, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(404, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": k.Id, "itemId": c.r, "snapshot": snapshot()}, nil)
	e.expect(201, "POST", "/playback/sessions", e.plain.token, map[string]any{"profileId": e.plain.account.ID, "itemId": c.r, "snapshot": snapshot()}, nil)
}

func TestAllowListModeShowsOnlyChosenTitles(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	var k api.Account
	e.expect(201, "POST", "/profiles", e.plain.token, map[string]any{"name": "Kid", "birthday": "2018-05-01", "filterMode": "allow_list"}, &k)
	e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/allowed-items/"+itoa(c.r), e.plain.token, nil, nil)
	e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/allowed-items/"+itoa(c.kidShow), e.plain.token, nil, nil)
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	var page api.ItemPage
	e.expect(200, "GET", "/items?limit=200", e.plain.token, nil, &page)
	got := ids(page.Items)
	if len(got) != 4 || !got[c.r] || !got[c.kidShow] || !got[c.kidEp] || got[c.g] {
		t.Fatalf("%+v", got)
	}
}

// An adult who forgot their password gets a temporary one from an admin and
// keeps their household; every device of theirs is signed out.
func TestAdminSetsATemporaryPassword(t *testing.T) {
	e := newEnv(t)
	path := "/accounts/" + itoa(e.plain.account.ID) + "/password"
	temp := map[string]any{"password": "temp-4821-pass"} //nolint:gosec // test password
	e.expect(403, "PUT", path, e.plain.token, temp, nil)
	e.expect(400, "PUT", path, e.admin.token, map[string]any{"password": "short"}, nil)
	// bcrypt takes 72 bytes, which 40 characters can pass.
	long := map[string]any{"password": strings.Repeat("é", 40)}
	e.expect(400, "PUT", path, e.admin.token, map[string]any{"password": strings.Repeat("x", 73)}, nil)
	e.expect(400, "PUT", path, e.admin.token, long, nil)
	// Nothing is hashed before the code or first run is known to be usable.
	e.expect(404, "POST", "/join/ZZZZ-ZZZZ", "", device("name", "X", "password", long["password"]), nil)
	e.expect(409, "POST", "/setup", "", map[string]any{"serverName": "S", "name": "X", "email": "x@example.test",
		"password": long["password"], "deviceName": "Mac", "platform": "macos"}, nil)
	e.expect(404, "PUT", "/accounts/999/password", e.admin.token, temp, nil)
	e.expect(409, "PUT", "/accounts/"+itoa(e.kid(e.plain, "Kid").Id)+"/password", e.admin.token, temp, nil)
	pending, _ := e.addAdult("pending@example.test", 0)
	e.expect(409, "PUT", "/accounts/"+itoa(pending.Id)+"/password", e.admin.token, temp, nil)

	m, _ := e.playable()
	phone, _ := e.secondDevice(e.plain, "Phone", "ios")
	st := e.startOn(phone, e.plain.account.ID, m)
	e.expect(204, "PUT", path, e.admin.token, temp, nil)
	for _, token := range []string{e.plain.token, phone} {
		e.expect(401, "GET", "/me", token, nil, nil)
	}
	if _, _, live := e.svc.Playback.Live(st.SessionId); live {
		t.Fatal("the stream is still playing")
	}
	e.expect(201, "POST", "/auth/sign-in", "", device("email", "plain@example.test", "password", "temp-4821-pass"), nil)

	// An admin setting their own keeps the device they set it on.
	laptop, _ := e.secondDevice(e.admin, "Laptop", "web")
	e.expect(204, "PUT", "/accounts/"+itoa(e.admin.account.ID)+"/password", e.admin.token, temp, nil)
	e.expect(401, "GET", "/me", laptop, nil, nil)
	e.expect(200, "GET", "/me", e.admin.token, nil, nil)
}

func TestChangeOwnPassword(t *testing.T) {
	e := newEnv(t)
	_, err := e.svc.Auth.SetPassword(e.plain.account, "first password", e.plain.deviceID)
	must(t, err)

	change := func(current, next string) map[string]any {
		return map[string]any{"currentPassword": current, "newPassword": next}
	}
	e.expect(401, "PUT", "/me/password", "", change("first password", "second password"), nil)
	e.expect(403, "PUT", "/me/password", e.plain.token, change("wrong password", "second password"), nil)
	e.expect(400, "PUT", "/me/password", e.plain.token, change("first password", "short"), nil)
	e.expect(204, "PUT", "/me/password", e.plain.token, change("first password", "second password"), nil)
	e.expect(401, "POST", "/auth/sign-in", "", device("email", "plain@example.test", "password", "first password"), nil)
	e.expect(201, "POST", "/auth/sign-in", "", device("email", "plain@example.test", "password", "second password"), nil)
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)
}

// Changing the password signs out the adult's other devices, ending their
// playback, and keeps the one it was changed on.
func TestChangingPasswordSignsOutOtherDevices(t *testing.T) {
	e := newEnv(t)
	e.withPassword(e.plain)
	m, _ := e.playable()
	phone, _ := e.secondDevice(e.plain, "Phone", "ios")
	st := e.startOn(phone, e.plain.account.ID, m)
	e.expect(204, "PUT", "/me/password", e.plain.token, map[string]any{"currentPassword": password, "newPassword": "second password"}, nil)
	e.expect(401, "GET", "/me", phone, nil, nil)
	if _, _, live := e.svc.Playback.Live(st.SessionId); live {
		t.Fatal("the stream is still playing")
	}
	e.expect(200, "GET", "/me", e.plain.token, nil, nil)
}

// Wrong current passwords count against sign-in's limit, as deleting's do;
// a right one under the limit changes it and does not count.
func TestChangeOwnPasswordIsLimited(t *testing.T) {
	e := newEnv(t)
	e.withPassword(e.plain)
	change := func(current, next string) map[string]any {
		return map[string]any{"currentPassword": current, "newPassword": next}
	}
	for range 9 {
		e.expect(403, "PUT", "/me/password", e.plain.token, change("wrongwrong", "second password"), nil)
	}
	e.expect(204, "PUT", "/me/password", e.plain.token, change(password, "second password"), nil)
	e.expect(403, "PUT", "/me/password", e.plain.token, change("wrongwrong", "third password"), nil)
	e.expect(429, "PUT", "/me/password", e.plain.token, change("second password", "third password"), nil)
	e.expect(429, "POST", "/auth/sign-in", "", device("email", e.plain.account.Email, "password", "second password"), nil)

	e.svc.Auth.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	e.expect(204, "PUT", "/me/password", e.plain.token, change("second password", "third password"), nil)
}

// Wrong join codes, previewed or accepted, count against sign-in's limit; a
// right one does not.
func TestJoinCodeGuessesAreLimited(t *testing.T) {
	e := newEnv(t)
	_, j := e.addAdult("bea@example.test", 0)
	for range 3 {
		e.expect(200, "GET", j.Link, "", nil, nil)
	}
	for range 5 {
		e.expect(404, "GET", "/join/ZZZZ-ZZZZ", "", nil, nil)
		e.expect(404, "POST", "/join/ZZZZ-ZZZZ", "", device("name", "Bea", "password", "longenough"), nil)
	}
	e.expect(429, "GET", j.Link, "", nil, nil)
	e.expect(429, "POST", j.Link, "", device("name", "Bea", "password", "longenough"), nil)
	e.expect(429, "POST", "/auth/sign-in", "", device("email", e.plain.account.Email, "password", "longenough"), nil)

	e.svc.Auth.Now = func() time.Time { return time.Now().Add(11 * time.Minute) }
	e.expect(201, "POST", j.Link, "", device("name", "Bea", "password", "longenough"), nil)
}

// The owner adds an adult to their household and hands over the join code;
// that adult then signs in to the owner's household and may manage kids, but
// only the owner manages adults, and each adult keeps their own PIN.
func TestOwnerAddsAManagedAdult(t *testing.T) {
	e := newEnv(t)
	var inv api.HouseholdInvite
	e.expect(400, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Bea"}, nil)
	e.expect(201, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Bea", "email": "bea@example.test"}, &inv)
	if !inv.Account.Pending || inv.Account.Kind != api.Adult || inv.Account.HouseholdId == nil || *inv.Account.HouseholdId != e.plain.household.ID ||
		inv.JoinCode.AccountId != inv.Account.Id {
		t.Fatalf("%+v", inv)
	}
	e.expect(409, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Bea", "email": "bea@example.test"}, nil)
	var p api.JoinPreview
	e.expect(200, "GET", inv.JoinCode.Link, "", nil, &p)
	if p.InvitedBy == nil || *p.InvitedBy != e.plain.account.Name {
		t.Fatalf("invited by the owner: %+v", p)
	}

	var bea api.SignInResult
	e.expect(201, "POST", inv.JoinCode.Link, "", device("name", "Bea", "password", "longenough"), &bea)
	if bea.Me.Household == nil || bea.Me.Household.Id != e.plain.household.ID || bea.Me.Device.HouseholdId != e.plain.household.ID || len(bea.Me.Profiles) != 2 {
		t.Fatalf("joins the owner's household: %+v", bea.Me)
	}
	var again api.SignInResult
	e.expect(201, "POST", "/auth/sign-in", "", device("email", "bea@example.test", "password", "longenough"), &again)
	if again.Me.Household.Id != e.plain.household.ID {
		t.Fatalf("%+v", again.Me)
	}

	// A managed adult manages kids, and their own PIN, but not other adults.
	tok := bea.Token
	var k api.Account
	e.expect(201, "POST", "/profiles", tok, map[string]any{"name": "Kid", "birthday": "2018-01-01"}, &k)
	e.expect(204, "PUT", "/profiles/"+itoa(k.Id)+"/pin", tok, map[string]any{"pin": "1234"}, nil)
	e.expect(204, "PUT", "/profiles/"+itoa(bea.Me.Account.Id)+"/pin", tok, map[string]any{"pin": "1234"}, nil)
	owner := "/profiles/" + itoa(e.plain.account.ID)
	e.expect(403, "PUT", owner+"/pin", tok, map[string]any{"pin": "1234"}, nil)
	e.expect(403, "DELETE", owner+"/pin", tok, nil, nil)
	e.expect(403, "DELETE", owner, tok, nil, nil)
	e.expect(403, "POST", "/household/adults", tok, map[string]any{"name": "Cy", "email": "cy@example.test"}, nil)

	// The owner switched to Bea acts as Bea, with her powers and not his.
	if st, raw := e.unlock(e.plain, bea.Me.Account.Id, "1234"); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	e.expect(403, "POST", "/household/adults", e.plain.token, map[string]any{"name": "Cy", "email": "cy@example.test"}, nil)
	e.expect(200, "GET", owner, e.plain.token, nil, nil)
	if st, raw := e.unlock(e.plain, e.plain.account.ID, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}

	// Removing a managed adult signs their devices out; the owner stays.
	e.expect(403, "DELETE", owner, e.plain.token, nil, nil)
	e.expect(204, "DELETE", "/profiles/"+itoa(bea.Me.Account.Id), e.plain.token, nil, nil)
	e.expect(401, "GET", "/me", tok, nil, nil)
}
