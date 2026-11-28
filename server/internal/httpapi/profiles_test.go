package httpapi_test

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

func (e *env) kid(u user, name string) api.Account {
	e.t.Helper()
	var k api.Account
	e.expect(201, "POST", "/profiles", u.token, map[string]any{"name": name, "birthday": "2018-05-01", "movieCeiling": "PG", "tvCeiling": "TV-Y7"}, &k)
	return k
}

// A client may choose a new profile's id, as an offline client does: a
// malformed one is refused, one in use is a conflict, and a replace keeps
// the path's. The profile names who made and last changed it.
func TestAClientChoosesAProfileID(t *testing.T) {
	e := newEnv(t)
	body := func(id any) map[string]any { return map[string]any{"id": id, "name": "Kid", "birthday": "2018-05-01"} }
	id := uid.New()
	var k api.Account
	e.expect(201, "POST", "/profiles", e.plain.token, body(id), &k)
	me := e.plain.account.ID
	if k.Id != id || k.CreatedBy == nil || *k.CreatedBy != me || k.ModifiedBy == nil || *k.ModifiedBy != me {
		t.Fatalf("%+v", k)
	}
	raw := e.expect(409, "POST", "/profiles", e.plain.token, body(id), nil)
	if errorCode(t, raw) != "conflict" {
		t.Fatalf("%s", raw)
	}
	for _, bad := range []string{"1", strings.ToUpper(uid.New().String()), "0190a1b2-c3d4-4e5f-8a6b-7c8d9e0f1a2b"} {
		if raw := e.expect(400, "POST", "/profiles", e.plain.token, body(bad), nil); errorCode(t, raw) != "bad_request" {
			t.Fatalf("%s: %s", bad, raw)
		}
	}
	e.expect(400, "GET", "/profiles/1", e.plain.token, nil, nil)
	e.expect(400, "PUT", "/profiles/"+itoa(id), e.plain.token, body(uid.New()), nil)
	e.expect(200, "PUT", "/profiles/"+itoa(id), e.plain.token, body(id), &k)
	if k.Id != id || k.ModifiedAt.Before(k.CreatedAt) {
		t.Fatalf("%+v", k)
	}
}

func TestProfilesCRUDStaysInTheHousehold(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	if k.Kind != api.Kid || k.HouseholdId == nil || *k.HouseholdId != e.plain.household.ID || k.MovieCeiling == nil || *k.MovieCeiling != api.PG {
		t.Fatalf("%+v", k)
	}
	e.expect(400, "POST", "/profiles", e.plain.token, map[string]any{"name": "No birthday"}, nil)
	e.expect(400, "POST", "/profiles", e.plain.token, map[string]any{"name": "K", "birthday": "2018-05-01", "movieCeiling": "X"}, nil)

	var page api.AccountPage
	e.expect(200, "GET", "/profiles", e.plain.token, nil, &page)
	if len(page.Items) != 2 || page.Items[0].Id != e.plain.account.ID {
		t.Fatalf("adult first, then kids: %+v", page)
	}
	id := "/profiles/" + itoa(k.Id)
	e.expect(200, "PUT", id, e.plain.token, map[string]any{"name": "Renamed", "birthday": "2018-05-01", "filterMode": "allow_list"}, &k)
	if k.Name != "Renamed" || k.FilterMode != api.FilterModeAllowList || k.MovieCeiling != nil {
		t.Fatalf("%+v", k)
	}
	e.expect(200, "GET", id, e.plain.token, nil, nil)

	// Another household's device cannot see, edit or delete this kid.
	e.expect(404, "GET", id, e.admin.token, nil, nil)
	e.expect(404, "PUT", id, e.admin.token, map[string]any{"name": "X", "birthday": "2018-05-01"}, nil)
	e.expect(404, "DELETE", id, e.admin.token, nil, nil)
	e.expect(404, "GET", id+"/watch-state", e.admin.token, nil, nil)

	// The adult profile is not edited or deleted here.
	adult := "/profiles/" + itoa(e.plain.account.ID)
	e.expect(403, "PUT", adult, e.plain.token, map[string]any{"name": "X", "birthday": "1980-01-01"}, nil)
	e.expect(403, "DELETE", adult, e.plain.token, nil, nil)

	e.expect(204, "DELETE", id, e.plain.token, nil, nil)
	e.expect(404, "GET", id, e.plain.token, nil, nil)
}

// A device acting as a kid is signed out before the kid is deleted, by any
// delete that takes the kid: left signed in it would act as its adult, with
// no filter and no PIN asked. Its session ends and it leaves its Watch
// together, as on sign-out.
func TestDeletingAKidSignsOutDevicesActingAsThem(t *testing.T) {
	for name, c := range map[string]struct {
		target    func(e *env, kid uid.ID) (path, token string)
		household bool // the household outlives the delete
	}{
		"their adult removes them": {func(e *env, kid uid.ID) (string, string) { return "/profiles/" + itoa(kid), e.plain.token }, true},
		"an admin deletes them":    {func(e *env, kid uid.ID) (string, string) { return "/accounts/" + itoa(kid), e.admin.token }, true},
		"an admin deletes their household's owner": {func(e *env, _ uid.ID) (string, string) {
			return "/accounts/" + itoa(e.plain.account.ID), e.admin.token
		}, false},
		"an admin deletes their household": {func(e *env, _ uid.ID) (string, string) {
			return "/households/" + itoa(e.plain.household.ID), e.admin.token
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			m, _ := e.playable()
			k := e.kid(e.plain, "Kid")
			tablet, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "Tablet", "web")
			must(t, err)
			var w api.WatchTogether
			e.expect(201, "POST", "/watch-together", tablet, map[string]any{"itemId": m}, &w)
			e.expect(204, "POST", "/profiles/"+itoa(k.Id)+"/unlock", tablet, map[string]any{}, nil)
			var st api.PlaybackStream
			e.expect(201, "POST", "/playback/sessions", tablet, map[string]any{"profileId": k.Id, "itemId": m, "snapshot": snapshot()}, &st)

			path, token := c.target(e, k.Id)
			e.expect(204, "DELETE", path, token, nil, nil)

			e.expect(401, "GET", "/me", tablet, nil, nil)
			if _, _, live := e.svc.Playback.Live(st.SessionId); live {
				t.Fatal("the device's session is still open")
			}
			if !c.household {
				return
			}
			e.expect(200, "GET", togetherPath(w), e.plain.token, nil, &w)
			if w.Status != api.Ended {
				t.Fatalf("the device is still in its Watch together: %+v", w)
			}
		})
	}
}

func TestAllowList(t *testing.T) {
	e := newEnv(t)
	k := e.kid(e.plain, "Kid")
	m := e.movie(e.library("movies"), "Film")
	base := "/profiles/" + itoa(k.Id) + "/allowed-items"
	e.expect(204, "PUT", base+"/"+itoa(m), e.plain.token, nil, nil)
	e.expect(204, "PUT", base+"/"+itoa(m), e.plain.token, nil, nil)
	var al api.AllowList
	e.expect(200, "GET", base, e.plain.token, nil, &al)
	if len(al.ItemIds) != 1 || al.ItemIds[0] != m {
		t.Fatalf("%+v", al)
	}
	e.expect(404, "PUT", base+"/"+itoa(uid.N(999)), e.plain.token, nil, nil)
	e.expect(403, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/allowed-items/"+itoa(m), e.plain.token, nil, nil)
	e.expect(404, "GET", base, e.admin.token, nil, nil)
	e.expect(204, "DELETE", base+"/"+itoa(m), e.plain.token, nil, nil)
	e.expect(404, "DELETE", base+"/"+itoa(m), e.plain.token, nil, nil)
}

func TestWatchStateCRUD(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	a, b := e.movie(lib, "A"), e.movie(lib, "B")
	e.rate(a, "G")
	e.rate(b, "G")
	k := e.kid(e.plain, "Kid")
	base := "/profiles/" + itoa(k.Id) + "/watch-state/"
	var ws api.WatchState
	e.expect(404, "GET", base+itoa(a), e.plain.token, nil, nil)
	e.expect(200, "PUT", base+itoa(a), e.plain.token, map[string]any{"watched": false, "positionMs": 60000}, &ws)
	if ws.PositionMs != 60000 || ws.Watched || ws.LastPlayedAt == nil {
		t.Fatalf("%+v", ws)
	}
	time.Sleep(5 * time.Millisecond)
	e.expect(200, "PUT", base+itoa(b), e.plain.token, map[string]any{"watched": true}, &ws)
	if !ws.Watched {
		t.Fatalf("%+v", ws)
	}
	e.expect(400, "PUT", base+itoa(a), e.plain.token, map[string]any{"positionMs": -1, "watched": false}, nil)
	e.expect(404, "PUT", base+itoa(uid.N(999)), e.plain.token, map[string]any{"watched": true}, nil)

	var page api.WatchStatePage
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/watch-state?limit=1", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].ItemId != b || page.NextOffset == nil {
		t.Fatalf("most recent first: %+v", page)
	}
	// Watch state is per profile.
	e.expect(404, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(a), e.plain.token, nil, nil)

	e.expect(204, "DELETE", base+itoa(a), e.plain.token, nil, nil)
	e.expect(404, "DELETE", base+itoa(a), e.plain.token, nil, nil)
}

// A kid's watch state reaches only items in their filter; anything else
// answers as if it did not exist, for reads, writes and the listing.
func TestKidWatchStateStaysInTheFilter(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	ok, hidden := e.movie(lib, "Up"), e.movie(lib, "Heat")
	e.rate(ok, "G")
	e.rate(hidden, "R")
	k := e.kid(e.plain, "Kid")
	// A state the kid could not have made, as left from before a ceiling
	// was lowered.
	must(t, e.svc.WatchStates.Repo().Save(&watchstate.State{AccountID: k.Id, ItemID: hidden, PositionMs: 5000}))
	if st, raw := e.unlock(e.plain, k.Id, ""); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	base := "/profiles/" + itoa(k.Id) + "/watch-state/"
	e.expect(200, "PUT", base+itoa(ok), e.plain.token, map[string]any{"watched": true}, nil)
	e.expect(404, "GET", base+itoa(hidden), e.plain.token, nil, nil)
	e.expect(404, "PUT", base+itoa(hidden), e.plain.token, map[string]any{"watched": true}, nil)
	e.expect(404, "DELETE", base+itoa(hidden), e.plain.token, nil, nil)
	var page api.WatchStatePage
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/watch-state", e.plain.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].ItemId != ok {
		t.Fatalf("%+v", page)
	}
	if w, _ := e.svc.WatchStates.Repo().Get(k.Id, hidden); w == nil || w.Watched {
		t.Fatal("the hidden item's state changed")
	}
}

// A profile's own data answers its device's active profile, and a kid's any
// adult of the household. Another adult's is not found, as their playlists
// are, until the device switches to them, which is where their PIN is asked.
func TestAnotherAdultsDataIsNotFound(t *testing.T) {
	e := newEnv(t)
	m, _ := e.playable()
	tb := e.youtubeLibrary()
	cy := e.householdAdult(e.plain, "Cy", "cy@example.test")
	k := e.kid(e.plain, "Kid")
	owner := "/profiles/" + itoa(e.plain.account.ID)
	state := owner + "/watch-state/" + itoa(m)
	e.expect(200, "PUT", state, e.plain.token, map[string]any{"watched": false, "positionMs": 5000}, nil)
	must(t, e.svc.Accounts.Repo().Allow(e.plain.account.ID, m))

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", owner + "/watch-state", nil},
		{"GET", state, nil},
		{"PUT", state, map[string]any{"watched": true}},
		{"DELETE", state, nil},
		{"GET", owner + "/show-progress", nil},
		{"GET", owner + "/allowed-items", nil},
		{"DELETE", owner + "/allowed-items/" + itoa(m), nil},
		{"GET", owner + "/music/recently-played", nil},
		{"PUT", owner + "/youtube/channels/" + itoa(tb.channel) + "/watch-state", map[string]any{"watched": true}},
		{"POST", "/playback/sessions", map[string]any{"profileId": e.plain.account.ID, "itemId": m, "snapshot": snapshot()}},
	} {
		e.expect(404, c.method, c.path, cy.Token, c.body, nil)
	}
	if ws := e.watchState(m); ws.Watched || ws.PositionMs != 5000 {
		t.Fatalf("another adult changed it: %+v", ws)
	}
	if ids, err := e.svc.Accounts.Repo().Allowed(e.plain.account.ID); err != nil || len(ids) != 1 {
		t.Fatalf("another adult changed the allow list: %v %v", ids, err)
	}

	// The kid's data, and managing the household, are within reach.
	e.expect(200, "GET", "/profiles/"+itoa(k.Id)+"/watch-state", cy.Token, nil, nil)
	e.expect(200, "GET", owner, cy.Token, nil, nil)

	e.expect(204, "PUT", owner+"/pin", e.plain.token, map[string]any{"pin": "1234"}, nil)
	if st, raw := e.unlock(user{token: cy.Token}, e.plain.account.ID, ""); st != 403 {
		t.Fatalf("%d %s", st, raw)
	}
	if st, raw := e.unlock(user{token: cy.Token}, e.plain.account.ID, "1234"); st != 204 {
		t.Fatalf("%d %s", st, raw)
	}
	var page api.WatchStatePage
	e.expect(200, "GET", owner+"/watch-state", cy.Token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].ItemId != m {
		t.Fatalf("%+v", page)
	}
	e.expect(404, "GET", "/profiles/"+itoa(cy.Me.Account.Id)+"/watch-state", cy.Token, nil, nil)
}

// A watch state write that says when it happened wins only over an older
// one, single or batch, so offline play and later changes settle on the
// newer either way round.
func TestWatchStateNewerWins(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	m := e.movie(lib, "Heat")
	e.file(m, 600_000)
	me, tok := e.plain.account.ID, e.plain.token
	path := "/profiles/" + itoa(me) + "/watch-state/" + itoa(m)
	hourAgo, minuteAgo := time.Now().UTC().Add(-time.Hour), time.Now().UTC().Add(-time.Minute)
	var w api.WatchState
	e.expect(200, "PUT", path, tok, map[string]any{"watched": false, "positionMs": 5000, "at": minuteAgo}, &w)
	if w.PositionMs != 5000 || !w.ChangedAt.Equal(minuteAgo.Truncate(time.Millisecond)) || !w.LastPlayedAt.Equal(w.ChangedAt) {
		t.Fatalf("%+v", w)
	}
	if e.expect(200, "PUT", path, tok, map[string]any{"watched": true, "at": hourAgo}, &w); w.Watched || w.PositionMs != 5000 {
		t.Fatalf("an older change is dropped: %+v", w)
	}
	if e.expect(200, "PUT", path, tok, map[string]any{"watched": true, "at": time.Now().Add(time.Hour)}, &w); !w.Watched ||
		w.ChangedAt.After(time.Now()) {
		t.Fatalf("a later change wins, never from the future: %+v", w)
	}
	e.expect(204, "PUT", "/profiles/"+itoa(me)+"/watch-state/batch", tok, map[string]any{"itemIds": []uid.ID{m}, "watched": false, "at": hourAgo}, nil)
	if e.expect(200, "GET", path, tok, nil, &w); !w.Watched {
		t.Fatalf("an older batch is dropped: %+v", w)
	}
	e.expect(204, "PUT", "/profiles/"+itoa(me)+"/watch-state/batch", tok, map[string]any{"itemIds": []uid.ID{m}, "watched": false}, nil)
	if e.expect(200, "GET", path, tok, nil, &w); w.Watched {
		t.Fatalf("a batch now wins: %+v", w)
	}
	other := e.movie(lib, "Ronin")
	e.file(other, 600_000)
	eds, err := e.svc.Editions.Repo().Default(other)
	must(t, err)
	e.expect(404, "PUT", path, tok, map[string]any{"watched": false, "positionMs": 1, "editionId": eds.ID}, nil)
}

// A play recorded late counts once whichever way round it and a
// putWatchState arrive; the rest of the state settles on the later
// instant, as putWatchState settles it.
func TestRecordPlayCommutesWithNewerWins(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	me, tok := e.plain.account.ID, e.plain.token
	now := time.Now().UTC().Truncate(time.Millisecond)
	earlier, played, later := now.Add(-time.Hour), now.Add(-30*time.Minute), now.Add(-10*time.Minute)
	for i, c := range []struct {
		putAt    time.Time
		putFirst bool
		watched  bool
		pos      int64
		at       time.Time
	}{
		{earlier, true, true, 0, played},
		{earlier, false, true, 0, played},
		{later, true, false, 7000, later},
		{later, false, false, 7000, later},
	} {
		m := e.movie(lib, "M"+itoa(int64(i)))
		e.file(m, 600_000)
		path := "/profiles/" + itoa(me) + "/watch-state/" + itoa(m)
		e.expect(200, "PUT", path, tok, map[string]any{"watched": false, "positionMs": 5000, "at": now.Add(-2 * time.Hour)}, nil)
		put := func() {
			e.expect(200, "PUT", path, tok, map[string]any{"watched": false, "positionMs": 7000, "at": c.putAt}, nil)
		}
		if c.putFirst {
			put()
		}
		e.expect(200, "POST", path+"/plays", tok, map[string]any{"at": played}, nil)
		if !c.putFirst {
			put()
		}
		var w api.WatchState
		e.expect(200, "GET", path, tok, nil, &w)
		if w.PlayCount != 1 || w.Watched != c.watched || w.PositionMs != c.pos || !w.ChangedAt.Equal(c.at) || !w.LastPlayedAt.Equal(c.at) ||
			w.EditionId == nil {
			t.Errorf("put at %v first %v: %+v", c.putAt, c.putFirst, w)
		}
	}
}

// Every recorded play counts, a watched item's too, and the first makes
// the state.
func TestRecordPlayCountsEachPlay(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	m := e.movie(lib, "Heat")
	e.file(m, 600_000)
	path := "/profiles/" + itoa(e.plain.account.ID) + "/watch-state/" + itoa(m)
	hourAgo := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	var w api.WatchState
	e.expect(200, "POST", path+"/plays", e.plain.token, map[string]any{"at": hourAgo}, &w)
	if w.PlayCount != 1 || !w.Watched || w.PositionMs != 0 || !w.LastPlayedAt.Equal(hourAgo) {
		t.Fatalf("%+v", w)
	}
	e.expect(200, "POST", path+"/plays", e.plain.token, map[string]any{"at": hourAgo.Add(-time.Hour)}, &w)
	if w.PlayCount != 2 || !w.LastPlayedAt.Equal(hourAgo) {
		t.Fatalf("an earlier play counts and keeps the later one: %+v", w)
	}
	e.expect(400, "POST", path+"/plays", e.plain.token, map[string]any{}, nil)
	e.expect(404, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(uid.N(999))+"/plays", e.plain.token, map[string]any{"at": hourAgo}, nil)
	other := e.movie(lib, "Ronin")
	e.file(other, 600_000)
	ed, err := e.svc.Editions.Repo().Default(other)
	must(t, err)
	e.expect(404, "POST", path+"/plays", e.plain.token, map[string]any{"at": hourAgo, "editionId": ed.ID}, nil)
}

// A forget that says when it happened leaves a state changed after it.
func TestDeleteWatchStateAt(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	m := e.movie(lib, "Heat")
	path := "/profiles/" + itoa(e.plain.account.ID) + "/watch-state/" + itoa(m)
	minuteAgo := time.Now().UTC().Add(-time.Minute)
	e.expect(200, "PUT", path, e.plain.token, map[string]any{"watched": true, "at": minuteAgo}, nil)
	e.expect(204, "DELETE", path+"?at="+url.QueryEscape(minuteAgo.Add(-time.Hour).Format(time.RFC3339Nano)), e.plain.token, nil, nil)
	e.expect(200, "GET", path, e.plain.token, nil, nil)
	e.expect(204, "DELETE", path+"?at="+url.QueryEscape(minuteAgo.Format(time.RFC3339Nano)), e.plain.token, nil, nil)
	e.expect(404, "GET", path, e.plain.token, nil, nil)
	e.expect(404, "DELETE", path+"?at="+url.QueryEscape(minuteAgo.Format(time.RFC3339Nano)), e.plain.token, nil, nil)
}

// A dismiss forgets only where to resume, in the order instants say, and
// is heard; it takes an episode's show out of progress.
func TestDismissWatchState(t *testing.T) {
	e := newEnv(t)
	tv := e.library("tv")
	show, ep := e.show(tv, "Severance", "TV-MA")
	me := "/profiles/" + itoa(e.plain.account.ID)
	ws := me + "/watch-state/" + itoa(ep)
	q := func(at time.Time) string { return "?at=" + url.QueryEscape(at.Format(time.RFC3339Nano)) }
	e.expect(404, "DELETE", ws+"/position", e.plain.token, nil, nil)
	hourAgo := time.Now().UTC().Add(-time.Hour)
	var w api.WatchState
	e.expect(200, "PUT", ws, e.plain.token, map[string]any{"watched": true, "at": hourAgo}, nil)
	e.expect(200, "PUT", ws, e.plain.token, map[string]any{"watched": true, "positionMs": 60000, "at": hourAgo.Add(time.Minute)}, &w)
	played := *w.LastPlayedAt

	e.expect(204, "DELETE", ws+"/position"+q(hourAgo), e.plain.token, nil, nil)
	e.expect(200, "GET", ws, e.plain.token, nil, &w)
	if w.PositionMs != 60000 {
		t.Fatalf("a dismiss older than the progress must lose: %+v", w)
	}
	at := e.cursor(e.plain.token)
	e.expect(204, "DELETE", ws+"/position", e.plain.token, nil, nil)
	e.expect(200, "GET", ws, e.plain.token, nil, &w)
	if w.PositionMs != 0 || !w.Watched || w.PlayCount != 1 || !w.LastPlayedAt.Equal(played) {
		t.Fatalf("a dismiss keeps watched, plays and when last played: %+v", w)
	}
	if got := e.heard(e.plain.token, &at); len(got) != 1 || !is(got[0], api.EventKindWatchState, "") || *got[0].ItemId != ep {
		t.Fatalf("a dismiss is heard: %+v", got)
	}
	var p api.ShowProgressPage
	e.expect(200, "GET", me+"/show-progress", e.plain.token, nil, &p)
	if len(p.Items) != 1 || p.Items[0].ShowId != show || p.Items[0].InProgress {
		t.Fatalf("a dismissed episode leaves its show out of progress: %+v", p.Items)
	}
	e.expect(204, "DELETE", ws+"/position", e.plain.token, nil, nil)

	k := e.kid(e.plain, "Kid")
	kid := e.kidDevice(e.plain, k.Id)
	e.expect(404, "DELETE", "/profiles/"+itoa(k.Id)+"/watch-state/"+itoa(ep)+"/position", kid, nil, nil)
}

// A show's or season's page reads the states of its own episodes only.
func TestWatchStatesUnderAShowOrSeason(t *testing.T) {
	e := newEnv(t)
	c := e.catalog()
	tv := itemOf(t, e, c.kidShow).LibraryId
	var s2, ep2 api.Item
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "parentId": c.kidShow, "kind": "season", "parsedTitle": "Kid show", "seasonNumber": 2}, &s2)
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": tv, "parentId": s2.Id, "kind": "episode", "parsedTitle": "Kid show", "episodeNumber": 1}, &ep2)
	me := "/profiles/" + itoa(e.plain.account.ID)
	for _, id := range []uid.ID{c.kidEp, ep2.Id, c.adultEp, c.g} {
		e.expect(200, "PUT", me+"/watch-state/"+itoa(id), e.plain.token, map[string]any{"watched": true}, nil)
	}
	under := func(parent uid.ID) []uid.ID {
		var page api.WatchStatePage
		e.expect(200, "GET", me+"/watch-state?parentId="+itoa(parent), e.plain.token, nil, &page)
		var out []uid.ID
		for _, w := range page.Items {
			out = append(out, w.ItemId)
		}
		slices.SortFunc(out, uid.ID.Compare)
		return out
	}
	if got := under(c.kidShow); !slices.Equal(got, []uid.ID{c.kidEp, ep2.Id}) {
		t.Fatalf("a show's episodes in every season: %v", got)
	}
	if got := under(s2.Id); !slices.Equal(got, []uid.ID{ep2.Id}) {
		t.Fatalf("a season's episodes: %v", got)
	}
	if got := under(c.g); len(got) != 0 {
		t.Fatalf("a movie has nothing under it: %v", got)
	}
}
