package httpapi_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
)

var actionCount atomic.Int64

// action is one queued call acting as profile, with a fresh id and no
// touched rows.
func action(op, method, path string, profile int64, body any) map[string]any {
	a := map[string]any{"id": fmt.Sprintf("00000000-0000-4000-8000-%012d", actionCount.Add(1)), "operationId": op,
		"method": method, "path": path, "actingProfileId": profile, "at": time.Now().UTC(), "touched": []any{}}
	if body != nil {
		a["body"] = body
	}
	return a
}

func touched(table string, key ...any) map[string]any {
	return map[string]any{"table": table, "key": key}
}

func batch(actions ...map[string]any) map[string]any {
	for i, a := range actions {
		if _, ok := a["seq"]; !ok {
			a["seq"] = i + 1
		}
	}
	return map[string]any{"apiVersion": "0.21.0", "actions": actions}
}

// replay sends actions as one batch from u's device.
func (e *env) replay(u user, actions ...map[string]any) []api.SyncActionResult {
	e.t.Helper()
	var out api.SyncActionResults
	e.expect(200, "POST", "/sync/actions", u.token, batch(actions...), &out)
	return out.Results
}

func (e *env) accepted(u user, actions ...map[string]any) {
	e.t.Helper()
	res := e.replay(u, actions...)
	if len(res) != len(actions) {
		e.t.Fatalf("%d results for %d actions: %+v", len(res), len(actions), res)
	}
	for i, a := range actions {
		if r := res[i]; r.Result != api.Accepted || r.Code != nil || r.Id.String() != a["id"] {
			e.t.Fatalf("action %d: %+v %s %s", i, r, deref(r.Code), deref(r.Message))
		}
	}
}

func rejected(t *testing.T, r api.SyncActionResult, code string) {
	t.Helper()
	if r.Result != api.Rejected || r.Code == nil || *r.Code != code {
		t.Fatalf("want rejected %s: %+v", code, r)
	}
}

func (e *env) count(q string, args ...any) int64 {
	e.t.Helper()
	var n int64
	must(e.t, e.db.QueryRow(q, args...).Scan(&n))
	return n
}

func (e *env) playCount(profile, item int64) int64 {
	return e.count(`SELECT COALESCE(SUM(play_count), 0) FROM watch_state WHERE account_id = ? AND item_id = ?`, profile, item)
}

// withoutInstants drops what a write stamps with the server's clock.
func withoutInstants(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if k == "createdAt" || k == "updatedAt" || k == "addedAt" {
				delete(t, k)
			} else {
				t[k] = withoutInstants(x)
			}
		}
	case []any:
		for i, x := range t {
			t[i] = withoutInstants(x)
		}
	}
	return v
}

// A replayed action leaves the server as the same call made directly does.
func TestReplayMatchesDirectCalls(t *testing.T) {
	direct, replayed := newEnv(t), newEnv(t)
	type fixture struct{ kid, m1, m2 int64 }
	setup := func(e *env) fixture {
		lib := e.library("movies")
		f := fixture{e.kid(e.plain, "Kid").Id, e.movie(lib, "Heat"), e.movie(lib, "Ronin")}
		e.file(f.m1, 600_000)
		e.file(f.m2, 600_000)
		return f
	}
	f := setup(direct)
	if setup(replayed) != f {
		t.Fatal("the two servers differ before any call")
	}
	me := direct.plain.account.ID
	both := func(op, method, path string, body any) []byte {
		t.Helper()
		status, raw := direct.do(method, path, direct.plain.token, body, nil)
		if status >= 300 {
			t.Fatalf("%s %s = %d: %s", method, path, status, raw)
		}
		replayed.accepted(replayed.plain, action(op, method, path, me, body))
		return raw
	}
	same := func(path string) {
		t.Helper()
		var got [2]any
		for i, e := range []*env{direct, replayed} {
			_, raw := e.do("GET", path, e.plain.token, nil, nil)
			var v any
			must(t, json.Unmarshal(raw, &v))
			got[i] = withoutInstants(v)
		}
		if !reflect.DeepEqual(got[0], got[1]) {
			t.Fatalf("GET %s:\ndirect   %v\nreplayed %v", path, got[0], got[1])
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	ws := "/profiles/" + itoa(me) + "/watch-state/" + itoa(f.m1)
	both("putWatchState", "PUT", ws, map[string]any{"watched": false, "positionMs": 5000, "at": now.Add(-2 * time.Hour)})
	same(ws)
	both("recordPlay", "POST", ws+"/plays", map[string]any{"at": now.Add(-time.Hour)})
	same(ws)

	lists := "/profiles/" + itoa(me) + "/playlists"
	var pl api.Playlist
	must(t, json.Unmarshal(both("createPlaylist", "POST", lists, map[string]any{"name": "Weekend", "kind": "movies"}), &pl))
	one := lists + "/" + itoa(pl.Id)
	both("addPlaylistEntry", "POST", one+"/entries", map[string]any{"itemId": f.m1, "rank": "a0"})
	both("addPlaylistEntry", "POST", one+"/entries", map[string]any{"itemId": f.m2, "rank": "Zz"})
	same(one + "/entries")
	// What is on and when is the station clock's, the same on both.
	at := time.Date(2030, 1, 2, 20, 10, 0, 0, time.Local)
	direct.svc.Stations.Now = func() time.Time { return at }
	replayed.svc.Stations.Now = direct.svc.Stations.Now
	var st api.Station
	must(t, json.Unmarshal(both("createStation", "POST", "/stations", map[string]any{"name": "Late", "playlistId": pl.Id}), &st))
	station := "/stations/" + itoa(st.Id)
	both("replaceStation", "PUT", station, map[string]any{"name": "Later", "timing": "continuous"})
	at = at.Add(20 * time.Minute)
	both("rescheduleStation", "POST", station+"/reschedule", nil)
	same(station)
	same(station + "/schedule?date=2030-01-02")
	must(t, json.Unmarshal(both("createPlaylist", "POST", lists, map[string]any{"name": "Gone", "kind": "movies"}), &pl))
	both("deletePlaylist", "DELETE", lists+"/"+itoa(pl.Id), nil)
	same(lists)

	kid := "/profiles/" + itoa(f.kid)
	both("allowItem", "PUT", kid+"/allowed-items/"+itoa(f.m2), nil)
	same(kid + "/allowed-items")
	both("setProfilePreferences", "PUT", kid+"/preferences", map[string]any{"autoplay": false, "color": "#2E6B5B"})
	same(kid)
}

// An action the operation refuses is rejected with the operation's code
// and changes nothing.
func TestReplayRejectsWithTheOperationsCode(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	m := e.movie(lib, "Heat")
	e.file(m, 600_000)
	me, k := e.plain.account.ID, e.kid(e.plain, "Kid").Id
	ws := "/profiles/" + itoa(me) + "/watch-state/"
	res := e.replay(e.plain,
		action("putWatchState", "PUT", ws+itoa(m), me, map[string]any{"watched": false, "positionMs": -1}),
		action("putWatchState", "PUT", ws+itoa(m), me, nil),
		action("allowItem", "PUT", "/profiles/"+itoa(k)+"/allowed-items/"+itoa(m), k, nil),
		action("putWatchState", "PUT", ws+"999", me, map[string]any{"watched": true}),
		action("putWatchState", "PUT", ws+itoa(m), me, map[string]any{"watched": true}),
	)
	if len(res) != 5 {
		t.Fatalf("%+v", res)
	}
	rejected(t, res[0], "bad_request")
	rejected(t, res[1], "bad_request")
	rejected(t, res[2], "forbidden")
	rejected(t, res[3], "not_found")
	if res[0].Message == nil || *res[0].Message == "" || res[4].Result != api.Accepted {
		t.Fatalf("%+v", res)
	}
	if n := e.count(`SELECT COUNT(*) FROM account_allow`); n != 0 {
		t.Fatalf("a kid allowed itself %d items", n)
	}
}

// An action sent again answers what it answered and does not run again,
// however the batches carrying it race.
func TestReplayAnswersARetryWithoutRunning(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	me := e.plain.account.ID
	ws := "/profiles/" + itoa(me) + "/watch-state/"
	play := action("recordPlay", "POST", ws+itoa(m)+"/plays", me, map[string]any{"at": time.Now().UTC().Add(-time.Hour)})
	missing := action("recordPlay", "POST", ws+"999/plays", me, map[string]any{"at": time.Now().UTC()})
	b := batch(play, missing)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			var out api.SyncActionResults
			status, raw := e.do("POST", "/sync/actions", e.plain.token, b, &out)
			if res := out.Results; status != 200 || len(res) != 2 || res[0].Result != api.Accepted || res[1].Result != api.Rejected ||
				*res[1].Code != "not_found" {
				t.Errorf("%d %s", status, raw)
			}
		})
	}
	wg.Wait()
	if n := e.playCount(me, m); n != 1 {
		t.Fatalf("play counted %d times", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM sync_action WHERE device_id = ?`, e.plain.deviceID); n != 2 {
		t.Fatalf("%d actions remembered", n)
	}
	// Another device's action of the same id is its own.
	other, _ := e.secondDevice(e.plain, "Phone", "ios")
	var out api.SyncActionResults
	e.expect(200, "POST", "/sync/actions", other, batch(play), &out)
	if n := e.playCount(me, m); n != 2 {
		t.Fatalf("play counted %d times", n)
	}
}

// Only an operation marked x-replayable runs, and only at its own path.
func TestReplayRunsOnlyReplayableOperations(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	me, k := e.plain.account.ID, e.kid(e.plain, "Kid").Id
	admin := e.admin.account.ID
	res := e.replay(e.plain,
		action("lockDevice", "POST", "/me/lock", me, nil),
		action("allowItem", "PUT", "/profiles/"+itoa(k)+"/watch-state/"+itoa(m), me, nil),
		action("putWatchState", "PUT", "/profiles/"+itoa(k)+"/allowed-items/"+itoa(m), me, nil),
		action("putWatchState", "PUT", "/nowhere", me, map[string]any{"watched": true}),
		action("putWatchState", "POST", "/profiles/"+itoa(me)+"/watch-state/"+itoa(m), me, map[string]any{"watched": true}),
	)
	res = append(res, e.replay(e.admin, action("createLibrary", "POST", "/libraries", admin, map[string]any{"name": "L", "kind": "movies"}))...)
	if len(res) != 6 {
		t.Fatalf("%+v", res)
	}
	for _, r := range res {
		rejected(t, r, "not_replayable")
	}
	if e.locked(e.plain.token) || e.count(`SELECT COUNT(*) FROM account_allow`) != 0 || e.count(`SELECT COUNT(*) FROM watch_state`) != 0 ||
		e.count(`SELECT COUNT(*) FROM library`) != 1 {
		t.Fatal("an operation not replayable ran")
	}
	// The operation id is matched without regard to case.
	e.accepted(e.plain, action("PutWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/"+itoa(m), me, map[string]any{"watched": true}))
}

// The acting profile is any of the device household's that is not revoked,
// PIN or no PIN and whatever the device is locked at.
func TestReplayActsAsAProfileOfTheHousehold(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	k := e.kid(e.plain, "Kid").Id
	gone := e.kid(e.plain, "Gone").Id
	e.expect(204, "PUT", "/profiles/"+itoa(k)+"/pin", e.plain.token, map[string]any{"pin": "1234"}, nil)
	e.expect(200, "PUT", "/accounts/"+itoa(gone), e.admin.token, map[string]any{"kind": "kid", "name": "Gone", "birthday": "2018-05-01", "revoked": true}, nil)
	stranger := e.admin.account.ID
	put := func(profile int64) map[string]any {
		return action("putWatchState", "PUT", "/profiles/"+itoa(profile)+"/watch-state/"+itoa(m), profile, map[string]any{"watched": true})
	}
	res := e.replay(e.plain, put(stranger), put(gone))
	rejected(t, res[0], "not_found")
	rejected(t, res[1], "not_found")
	if e.count(`SELECT COUNT(*) FROM watch_state`) != 0 {
		t.Fatal("ran as a profile outside the household")
	}
	// Reaching another household's profile through the acting one's path.
	res = e.replay(e.plain, action("putWatchState", "PUT", "/profiles/"+itoa(stranger)+"/watch-state/"+itoa(m), e.plain.account.ID,
		map[string]any{"watched": true}))
	rejected(t, res[0], "not_found")

	e.rate(m, "G")
	e.expect(204, "POST", "/me/lock", e.plain.token, nil, nil)
	e.accepted(e.plain, put(k))
	if e.count(`SELECT COUNT(*) FROM watch_state WHERE account_id = ? AND watched`, k) != 1 || !e.locked(e.plain.token) {
		t.Fatal("a locked device replays as a kid with a PIN and stays locked")
	}
}

// A batch from a client whose API version the server does not replay
// rejects every action and runs and remembers none.
func TestReplayRefusesAnUnsupportedVersion(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	me := e.plain.account.ID
	for _, v := range []string{"0.20.9", "1.2.4", "2.0.0", "0.21", "latest"} {
		b := batch(action("putWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/"+itoa(m), me, map[string]any{"watched": true}),
			action("nothing", "PUT", "/nowhere", me, nil))
		b["apiVersion"] = v
		b["actions"].([]map[string]any)[1]["touched"] = []any{touched("no_such_table", 1)}
		var out api.SyncActionResults
		e.expect(200, "POST", "/sync/actions", e.plain.token, b, &out)
		if len(out.Results) != 2 {
			t.Fatalf("%s: %+v", v, out)
		}
		for _, r := range out.Results {
			rejected(t, r, "unsupported_version")
		}
	}
	if e.count(`SELECT COUNT(*) FROM watch_state`)+e.count(`SELECT COUNT(*) FROM sync_action`) != 0 {
		t.Fatal("an unsupported batch ran")
	}
	b := batch(action("putWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/"+itoa(m), me, map[string]any{"watched": true}))
	b["apiVersion"] = "1.2.3"
	e.expect(200, "POST", "/sync/actions", e.plain.token, b, nil)
	if !watched(t, e, me, m) {
		t.Fatal("the server's own version replays")
	}
}

// A batch out of order, or naming a row the sync schema has not, answers
// 400 and runs nothing.
func TestReplayRefusesAMalformedBatch(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	me := e.plain.account.ID
	put := func() map[string]any {
		return action("putWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/"+itoa(m), me, map[string]any{"watched": true})
	}
	for _, seqs := range [][2]int{{3, 3}, {3, 2}} {
		a, b := put(), put()
		a["seq"], b["seq"] = seqs[0], seqs[1]
		e.expect(400, "POST", "/sync/actions", e.plain.token, batch(a, b), nil)
	}
	for _, rows := range [][]any{
		{touched("no_such_table", 1)},
		{touched("watch_state", me)},
		{touched("watch_state", me, m, 1)},
		{touched("library", 1, 2)},
		{touched("watch_state", me, nil)},
		{touched("watch_state", me, true)},
		{touched("watch_state", me, map[string]any{"id": m})},
		{touched("watch_state", me, []any{m})},
		{touched("sync_action", 1, "x")},
		{touched("watch_state", me, itoa(m))},
		{touched("library", "1")},
		{touched("library", 1.5)},
		{touched("item_genre", m, 1, 5)},
	} {
		a := put()
		a["touched"] = rows
		e.expect(400, "POST", "/sync/actions", e.plain.token, batch(put(), a), nil)
	}
	e.expect(400, "POST", "/sync/actions", e.plain.token, map[string]any{"apiVersion": "0.21.0", "actions": []any{}}, nil)
	if e.count(`SELECT COUNT(*) FROM watch_state`)+e.count(`SELECT COUNT(*) FROM sync_action`) != 0 {
		t.Fatal("a malformed batch ran")
	}
}

// After each action, accepted or rejected, every row it touched is marked
// changed when the household sees it and deleted when it is gone, so the
// client's own copy is brought to the server's.
func TestReplayMarksTouchedRows(t *testing.T) {
	e := newEnv(t)
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	me, admin := e.plain.account.ID, e.admin.account.ID
	now := time.Now().UTC().Truncate(time.Millisecond)
	ws := "/profiles/" + itoa(me) + "/watch-state/" + itoa(m)
	e.expect(200, "PUT", ws, e.plain.token, map[string]any{"watched": false, "positionMs": 9000, "at": now}, nil)
	e.expect(200, "PUT", "/profiles/"+itoa(admin)+"/watch-state/"+itoa(m), e.admin.token, map[string]any{"watched": true}, nil)
	version := func(q string, args ...any) int64 { return e.count(q, args...) }
	stamp := func(account int64) int64 {
		return version(`SELECT sync_version FROM watch_state WHERE account_id = ? AND item_id = ?`, account, m)
	}
	tomb := func(tbl string, k1, k2 any) int64 {
		return version(`SELECT COUNT(*) FROM sync_tombstone WHERE tbl = ? AND k1 = ? AND k2 = ?`, tbl, k1, k2)
	}
	clock := version(`SELECT version FROM sync_clock`)
	scope := version(`SELECT scope_version FROM sync_clock`)
	theirs := stamp(admin)

	older := action("putWatchState", "PUT", ws, me, map[string]any{"watched": false, "positionMs": 1000, "at": now.Add(-time.Hour)})
	older["touched"] = []any{touched("watch_state", me, m), touched("account_card", me)}
	refused := action("putWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/999", me, map[string]any{"watched": true})
	refused["touched"] = []any{touched("watch_state", me, 999), touched("watch_state", admin, m)}
	res := e.replay(e.plain, older, refused)
	if res[0].Result != api.Accepted {
		t.Fatalf("an older put is accepted: %+v", res)
	}
	rejected(t, res[1], "not_found")

	var pos int64
	must(t, e.db.QueryRow(`SELECT position_ms FROM watch_state WHERE account_id = ? AND item_id = ?`, me, m).Scan(&pos))
	if pos != 9000 {
		t.Fatalf("an older put changed the state: %d", pos)
	}
	if stamp(me) <= clock || version(`SELECT sync_version FROM account WHERE id = ?`, me) <= clock {
		t.Fatal("an existing row the household sees is marked changed, a replica table by its source")
	}
	if tomb("watch_state", me, 999) != 1 || version(`SELECT version FROM sync_tombstone WHERE tbl = 'watch_state' AND k2 = 999`) <= clock {
		t.Fatal("a row that is gone is marked deleted")
	}
	// Another household's row is left alone: nothing reaches them, and no
	// client snapshots.
	if tomb("watch_state", admin, m) != 0 || stamp(admin) != theirs || version(`SELECT scope_version FROM sync_clock`) != scope {
		t.Fatal("a row the household may not see is left alone")
	}
	if version(`SELECT COUNT(*) FROM sync_tombstone WHERE typeof(k2) <> 'integer'`) != 0 {
		t.Fatal("a tombstone keys an integer column by an integer")
	}
	if version(`SELECT COUNT(*) FROM watch_state`) != 2 {
		t.Fatal("touching a row changes it")
	}
}

// A rejected action's touched rows come back on the client's next page of
// changes: the server's copy of a row that exists, a tombstone for one
// that does not.
func TestReplayedTouchesReachTheNextChanges(t *testing.T) {
	e := synced(t)
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	me := e.plain.account.ID
	ws := "/profiles/" + itoa(me) + "/watch-state/"
	e.expect(200, "PUT", ws+itoa(m), e.plain.token, map[string]any{"watched": false, "positionMs": 9000}, nil)
	_, v, _ := e.snapshot(e.plain.token)

	invalid := action("putWatchState", "PUT", ws+itoa(m), me, map[string]any{"positionMs": -1})
	invalid["touched"] = []any{touched("watch_state", me, m)}
	missing := action("putWatchState", "PUT", ws+"999", me, map[string]any{"watched": true})
	missing["touched"] = []any{touched("watch_state", me, 999)}
	res := e.replay(e.plain, invalid, missing)
	rejected(t, res[0], "bad_request")
	rejected(t, res[1], "not_found")

	var c api.SyncChanges
	e.expect(200, "GET", fmt.Sprintf("/sync/changes?schema=1&since=%d", v), e.plain.token, nil, &c)
	if c.Mode != api.Diff || len(*c.Tables) != 1 || len(*c.Tombstones) != 1 {
		t.Fatalf("%+v", c)
	}
	rows, tombs := (*c.Tables)[0], (*c.Tombstones)[0]
	pos := slices.Index(rows.Columns, "position_ms")
	if rows.Name != "watch_state" || len(rows.Rows) != 1 || rows.Rows[0][0] != float64(me) || rows.Rows[0][1] != float64(m) ||
		rows.Rows[0][pos] != float64(9000) {
		t.Errorf("the row that exists: %+v", rows)
	}
	if tombs.Table != "watch_state" || !reflect.DeepEqual(tombs.Keys, [][]any{{float64(me), float64(999)}}) {
		t.Errorf("the row that does not: %+v", tombs)
	}
}

// A server failure stops the batch: that action and the rest go
// unanswered, unremembered, for the client to send again.
func TestReplayStopsAtAServerFailure(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	m, n := e.movie(lib, "Heat"), e.movie(lib, "Ronin")
	e.file(m, 600_000)
	e.file(n, 600_000)
	me, k := e.plain.account.ID, e.kid(e.plain, "Kid").Id
	_, err := e.db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON account_allow BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	must(t, err)
	put := func(item int64) map[string]any {
		return action("putWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/"+itoa(item), me, map[string]any{"watched": true})
	}
	first, fails, later := put(m), action("allowItem", "PUT", "/profiles/"+itoa(k)+"/allowed-items/"+itoa(m), me, nil), put(n)
	fails["touched"] = []any{touched("account_allow", k, m)}
	res := e.replay(e.plain, first, fails, later)
	if len(res) != 1 || res[0].Result != api.Accepted {
		t.Fatalf("%+v", res)
	}
	if watched(t, e, me, n) || e.count(`SELECT COUNT(*) FROM sync_action`) != 1 || e.count(`SELECT COUNT(*) FROM sync_tombstone WHERE tbl = 'account_allow'`) != 0 {
		t.Fatal("the batch went on past a server failure")
	}
	_, err = e.db.Exec(`DROP TRIGGER boom`)
	must(t, err)
	e.accepted(e.plain, first, fails, later)
	if !watched(t, e, me, n) || e.count(`SELECT COUNT(*) FROM account_allow`) != 1 {
		t.Fatal("the rest runs when sent again")
	}
}

// A replayed action tells the web client what the same call would.
func TestReplayedActionsAreHeard(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	m := e.movie(e.library("movies"), "Heat")
	e.file(m, 600_000)
	me := e.plain.account.ID
	web, _ := e.secondDevice(e.plain, "Browser", "web")
	cur := e.cursor(web)
	e.accepted(e.plain,
		action("putWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/"+itoa(m), me, map[string]any{"watched": true}),
		action("createPlaylist", "POST", "/profiles/"+itoa(me)+"/playlists", me, map[string]any{"name": "Weekend", "kind": "movies"}))
	kinds := map[api.EventKind]bool{}
	for _, ev := range e.heard(web, &cur) {
		kinds[ev.Kind] = true
	}
	if !kinds[api.EventKindWatchState] || !kinds[api.EventKindPlaylist] {
		t.Fatalf("heard %v", kinds)
	}
}

// One action may touch thousands of rows, as marking a long show watched
// does, half of them gone; a batch of such actions is over the 1 MiB most
// operations take. Each row is marked at a version of its own, so a page
// of changes may end between any two, and the marks take no longer than
// the operation could.
func TestReplayMarksThousandsOfTouchedRows(t *testing.T) {
	e := newEnv(t)
	lib := e.library("movies")
	m := e.movie(lib, "Heat")
	e.file(m, 600_000)
	me := e.plain.account.ID
	_, err := e.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 2500)
		INSERT INTO item (library_id, kind, parsed_title, added_at, updated_at) SELECT ?, 'movie', 'M' || i, 0, 0 FROM n`, lib)
	must(t, err)
	_, err = e.db.Exec(`INSERT INTO watch_state (account_id, item_id, watched, updated_at) SELECT ?, id, 1, 0 FROM item WHERE id <> ?`, me, m)
	must(t, err)
	clock := e.count(`SELECT version FROM sync_clock`)
	var rows []any
	for id := range e.count(`SELECT MAX(id) FROM item`) + 1 {
		if id != 0 && id != m {
			rows = append(rows, touched("watch_state", me, id), touched("watch_state", me, id+1_000_000))
		}
	}
	if len(rows) != 5000 {
		t.Fatalf("%d rows", len(rows))
	}
	put := func() map[string]any {
		a := action("putWatchState", "PUT", "/profiles/"+itoa(me)+"/watch-state/"+itoa(m), me, map[string]any{"watched": true})
		a["touched"] = rows
		return a
	}
	start := time.Now()
	e.accepted(e.plain, put())
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("one action touching 5000 rows took %v", took)
	}
	if n := e.count(`SELECT COUNT(*) FROM watch_state WHERE account_id = ? AND item_id <> ? AND sync_version > ?`, me, m, clock); n != 2500 {
		t.Errorf("%d of 2500 rows that exist marked changed", n)
	}
	if n := e.count(`SELECT COUNT(*) FROM sync_tombstone WHERE tbl = 'watch_state' AND k1 = ? AND k2 > 1000000 AND version > ?`, me, clock); n != 2500 {
		t.Errorf("%d of 2500 rows that are gone marked deleted", n)
	}
	if n := e.count(`SELECT COUNT(*) - COUNT(DISTINCT v) + (MAX(v) > (SELECT version FROM sync_clock))
		FROM (SELECT sync_version AS v FROM watch_state WHERE sync_version > ?1 UNION ALL SELECT version FROM sync_tombstone WHERE version > ?1)`,
		clock); n != 0 {
		t.Error("marks share a version, or one is above the clock")
	}
	b := batch(put(), put(), put(), put(), put(), put())
	raw, err := json.Marshal(b)
	must(t, err)
	if len(raw) <= 1<<20 {
		t.Fatalf("a batch of %d bytes", len(raw))
	}
	var out api.SyncActionResults
	e.expect(200, "POST", "/sync/actions", e.plain.token, b, &out)
	if len(out.Results) != 6 {
		t.Fatalf("%+v", out)
	}
}
