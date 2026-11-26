package httpapi_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
)

// stationClock fixes the stations' clock in the server's local time and
// returns a setter.
func (e *env) stationClock() func(h, m int) time.Time {
	now := time.Date(2026, 11, 13, 15, 5, 0, 0, time.Local)
	e.svc.Stations.Now = func() time.Time { return now }
	return func(h, m int) time.Time {
		now = time.Date(2026, 11, 13, h, m, 0, 0, time.Local)
		return now
	}
}

// neighbor adds a second adult to u's household, signed in on a device.
func (e *env) neighbor(u user) string {
	e.t.Helper()
	a := &account.Account{Kind: account.Adult, Email: "neighbor@example.test", Name: "Neighbor", PasswordHash: "x", HouseholdID: &u.household.ID}
	must(e.t, e.svc.Accounts.Repo().Create(a))
	token, _, err := e.svc.Devices.Register(a.ID, u.household.ID, "Phone", "ios")
	must(e.t, err)
	return token
}

// nightsPlaylist is a movies playlist of plain's holding films, each with
// a file of minutes.
func (e *env) nightsPlaylist(films map[int64]int64) int64 {
	e.t.Helper()
	var pl api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "Nights", "kind": "movies"}, &pl)
	for id, minutes := range films {
		e.file(id, minutes*60000)
		e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(pl.Id)+"/entries", e.plain.token, map[string]any{"itemId": id}, nil)
	}
	return pl.Id
}

func stationIDs(page api.StationPage) (out []int64) {
	for _, s := range page.Items {
		out = append(out, s.Id)
	}
	return out
}

func TestStationLifecycle(t *testing.T) {
	e := newEnv(t)
	at := e.stationClock()
	films := e.library("movies")
	lantern := e.film(films, "Lantern", "2001-01-01", "PG")
	pl := e.nightsPlaylist(map[int64]int64{lantern: 90})
	neighbor := e.neighbor(e.plain)

	var st api.Station
	in := map[string]any{"name": "Late Show", "playlistId": pl, "visibility": "household", "days": []string{"friday", "saturday"},
		"times": []map[string]string{{"from": "16:00", "to": "02:00"}}}
	e.expect(201, "POST", "/stations", e.plain.token, in, &st)
	if st.Name != "Late Show" || st.Kind != api.PlaylistKindMovies || st.OwnerId != e.plain.account.ID || st.HouseholdId != e.plain.household.ID ||
		st.Order != api.StationOrderPlaylist || st.Visibility != api.StationVisibilityHousehold || len(st.Days) != 2 || st.Days[0] != api.Friday ||
		len(st.Times) != 1 || st.Times[0].From != "16:00" || st.Times[0].To != "02:00" || st.Rating == nil || *st.Rating != "PG" ||
		st.Timing != api.Hour {
		t.Fatalf("%+v", st)
	}
	if on := st.OnAir; on.Status != api.OffSchedule || on.Now != nil || on.Next == nil || on.Next.ItemId != lantern ||
		!on.Next.StartsAt.Equal(at(16, 0)) || on.Next.Item == nil || on.Next.Item.ParsedTitle != "Lantern" || on.Next.DurationMs != 5400000 {
		t.Fatalf("%+v", st.OnAir)
	}
	one := "/stations/" + itoa(st.Id)

	// Refusals on create: another adult's playlist, a kid active, server visibility.
	var theirs api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.admin.account.ID)+"/playlists", e.admin.token, map[string]any{"name": "Theirs", "kind": "movies"}, &theirs)
	e.expect(400, "POST", "/stations", e.plain.token, map[string]any{"name": "X", "playlistId": theirs.Id}, nil)
	e.expect(400, "POST", "/stations", e.plain.token, map[string]any{"name": "X", "playlistId": pl, "visibility": "server"}, nil)
	e.expect(400, "POST", "/stations", e.plain.token, map[string]any{"name": " ", "playlistId": pl}, nil)
	e.expect(400, "POST", "/stations", e.plain.token, map[string]any{"name": "X", "playlistId": pl, "times": []map[string]string{{"from": "25:00", "to": "02:00"}}}, nil)
	kid := e.kid(e.plain, "Kid")
	kidToken := e.kidDevice(e.plain, kid.Id)
	e.expect(403, "POST", "/stations", kidToken, map[string]any{"name": "X", "playlistId": pl}, nil)

	// The household sees it; another household does not.
	var page api.StationPage
	e.expect(200, "GET", "/stations", neighbor, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != st.Id || page.Items[0].OnAir.Next == nil {
		t.Fatalf("%+v", page)
	}
	e.expect(200, "GET", "/stations?kind=music", e.plain.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("kind filter %+v", page)
	}
	e.expect(200, "GET", "/stations", e.admin.token, nil, &page)
	if len(page.Items) != 0 {
		t.Fatalf("%+v", page)
	}
	e.expect(404, "GET", one, e.admin.token, nil, nil)
	e.expect(200, "GET", one, neighbor, nil, &st)

	// Only the owner edits.
	edit := map[string]any{"name": "Matinee", "playlistId": pl, "order": "random"}
	e.expect(403, "PUT", one, neighbor, edit, nil)
	e.expect(404, "PUT", one, e.admin.token, edit, nil)
	e.expect(403, "POST", one+"/reschedule", neighbor, nil, nil)
	e.expect(403, "DELETE", one, neighbor, nil, nil)
	e.expect(200, "PUT", one, e.plain.token, edit, &st)
	if st.Name != "Matinee" || st.Order != api.StationOrderRandom || st.Visibility != api.StationVisibilityHousehold || len(st.Days) != 0 || len(st.Times) != 0 ||
		st.Timing != api.Hour {
		t.Fatalf("absent visibility and timing keep them; absent days and times are continuous: %+v", st)
	}
	e.expect(400, "PUT", one, e.plain.token, map[string]any{"name": "Matinee", "playlistId": pl, "timing": "daily"}, nil)
	e.expect(200, "PUT", one, e.plain.token, map[string]any{"name": "Matinee", "playlistId": pl, "order": "random", "timing": "half_hour"}, &st)
	if st.Timing != api.HalfHour {
		t.Fatalf("%+v", st)
	}
	e.expect(200, "PUT", one, e.plain.token, map[string]any{"name": "Matinee", "playlistId": pl, "order": "random", "timing": "hour"}, &st)

	// A window that holds no timing boundary never airs, so it is refused;
	// one that crosses midnight to reach a boundary, or a continuous
	// station, is not.
	quiet := func(timing, from, to string) map[string]any {
		return map[string]any{"name": "Matinee", "playlistId": pl, "timing": timing, "times": []map[string]string{{"from": from, "to": to}}}
	}
	e.expect(400, "POST", "/stations", e.plain.token, quiet("hour", "20:05", "20:25"), nil)
	e.expect(400, "PUT", one, e.plain.token, quiet("hour", "20:05", "20:25"), nil)
	e.expect(400, "PUT", one, e.plain.token, quiet("half_hour", "20:05", "20:25"), nil)
	e.expect(200, "PUT", one, e.plain.token, quiet("half_hour", "20:25", "20:35"), nil)
	e.expect(200, "PUT", one, e.plain.token, quiet("hour", "23:45", "00:10"), nil)
	e.expect(200, "PUT", one, e.plain.token, quiet("continuous", "20:05", "20:25"), nil)
	e.expect(200, "PUT", one, e.plain.token, map[string]any{"name": "Matinee", "playlistId": pl, "order": "random", "timing": "hour"}, &st)
	e.expect(200, "PUT", one, e.plain.token, map[string]any{"name": "Matinee", "playlistId": pl, "visibility": "private"}, &st)
	e.expect(404, "GET", one, neighbor, nil, nil)

	var day api.StationProgramPage
	e.expect(200, "GET", one+"/schedule", e.plain.token, nil, &day)
	if len(day.Items) < 2 || !day.Items[0].StartsAt.Equal(at(16, 0)) || day.Items[0].EditionId == 0 || day.Items[0].Kind != api.PlaylistEntryKindMovie {
		t.Fatalf("%+v", day)
	}
	e.expect(200, "GET", one+"/schedule?date=2026-11-20", e.plain.token, nil, &day)
	if len(day.Items) != 0 {
		t.Fatalf("written a day ahead only: %+v", day)
	}

	// Reschedule now keeps what is on.
	at(16, 10)
	e.expect(200, "POST", one+"/reschedule", e.plain.token, nil, &st)
	if st.OnAir.Status != api.OnAir || st.OnAir.Now == nil || !st.OnAir.Now.StartsAt.Equal(at(16, 0)) || st.OnAir.PositionMs == nil ||
		*st.OnAir.PositionMs != 600000 || st.OnAir.Next == nil || !st.OnAir.Next.StartsAt.Equal(at(18, 0)) {
		t.Fatalf("%+v", st.OnAir)
	}
	// What follows, in the order it plays.
	if up := st.OnAir.UpNext; len(up) != 10 || up[0].ItemId != st.OnAir.Next.ItemId || !up[0].StartsAt.Equal(at(18, 0)) ||
		!up[1].StartsAt.After(up[0].StartsAt) || up[1].Item == nil || !up[9].StartsAt.After(up[8].StartsAt) {
		t.Fatalf("up next %+v", up)
	}

	// A station's playlist cannot be deleted under it.
	e.expect(409, "DELETE", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(pl), e.plain.token, nil, nil)
	e.expect(204, "DELETE", one, e.plain.token, nil, nil)
	e.expect(404, "GET", one, e.plain.token, nil, nil)
	e.expect(204, "DELETE", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(pl), e.plain.token, nil, nil)
}

func TestServerWideStations(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	pl := e.nightsPlaylist(map[int64]int64{e.film(e.library("movies"), "Lantern", "2001-01-01", "PG"): 90})
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl}, &st)
	if st.Visibility != api.StationVisibilityPrivate {
		t.Fatalf("private by default: %+v", st)
	}
	path := "/admin/stations/" + itoa(st.Id) + "/server-wide"

	e.expect(403, "GET", "/admin/stations", e.plain.token, nil, nil)
	e.expect(403, "PUT", path, e.plain.token, map[string]any{"serverWide": true}, nil)
	var page api.StationPage
	e.expect(200, "GET", "/admin/stations", e.admin.token, nil, &page)
	if len(page.Items) != 1 || page.Items[0].Id != st.Id {
		t.Fatalf("every station, private ones included: %+v", page)
	}
	e.expect(404, "PUT", "/admin/stations/999/server-wide", e.admin.token, map[string]any{"serverWide": true}, nil)
	e.expect(200, "PUT", path, e.admin.token, map[string]any{"serverWide": true}, &st)
	if st.Visibility != api.StationVisibilityServer {
		t.Fatalf("%+v", st)
	}
	e.expect(200, "GET", "/stations", e.admin.token, nil, &page)
	if len(page.Items) != 1 {
		t.Fatalf("another household sees a server station: %+v", page)
	}
	e.expect(403, "PUT", "/stations/"+itoa(st.Id), e.admin.token, map[string]any{"name": "Mine", "playlistId": pl}, nil)

	// The owner edits all but its visibility.
	e.expect(403, "PUT", "/stations/"+itoa(st.Id), e.plain.token, map[string]any{"name": "Nights", "playlistId": pl, "visibility": "household"}, nil)
	e.expect(200, "PUT", "/stations/"+itoa(st.Id), e.plain.token, map[string]any{"name": "Late", "playlistId": pl}, &st)
	if st.Name != "Late" || st.Visibility != api.StationVisibilityServer {
		t.Fatalf("%+v", st)
	}
	e.expect(200, "PUT", path, e.admin.token, map[string]any{"serverWide": false}, &st)
	if st.Visibility != api.StationVisibilityHousehold {
		t.Fatalf("off returns it to household: %+v", st)
	}
}

// A kid sees a station when they may watch or hear everything it plays
// now, whatever its rating says.
func TestStationsForKids(t *testing.T) {
	e := newEnv(t)
	at := e.stationClock()
	films := e.library("movies")
	meadow := e.film(films, "Meadow", "2001-01-01", "G")
	undertow := e.film(films, "Undertow", "2001-01-01", "R")
	gentle := e.nightsPlaylist(map[int64]int64{meadow: 90})
	strong := e.nightsPlaylist(map[int64]int64{undertow: 90})
	mixed := e.nightsPlaylist(map[int64]int64{e.film(films, "Pasture", "2001-01-01", "G"): 90, e.film(films, "Unrated", "2001-01-01", ""): 90})
	music := e.library("music")
	var songs api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "Songs", "kind": "music"}, &songs)
	song := e.song(music, "Opening")
	s, err := e.svc.Items.Repo().Get(song)
	must(t, err)
	clean := false
	s.Explicit = &clean
	must(t, e.svc.Items.Repo().Update(s))
	e.file(song, 180000)
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(songs.Id)+"/entries", e.plain.token, map[string]any{"itemId": song}, nil)
	var g, r, x, m api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "A gentle", "playlistId": gentle, "visibility": "household"}, &g)
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "B strong", "playlistId": strong, "visibility": "household"}, &r)
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "C mixed", "playlistId": mixed, "visibility": "household"}, &x)
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "D music", "playlistId": songs.Id, "visibility": "household"}, &m)
	if m.Kind != api.PlaylistKindMusic || m.Rating != nil || m.OnAir.Status != api.OnAir || m.OnAir.Now == nil || m.OnAir.Now.Song == nil {
		t.Fatalf("a music station has no rating and plays at once: %+v", m)
	}
	if x.Rating != nil {
		t.Fatalf("anything unrated leaves no rating to show: %+v", x)
	}

	kid := e.kid(e.plain, "Kid") // PG ceiling
	kidToken := e.kidDevice(e.plain, kid.Id)
	var page api.StationPage
	e.expect(200, "GET", "/stations", kidToken, nil, &page)
	if ids := stationIDs(page); len(ids) != 2 || ids[0] != g.Id || ids[1] != m.Id {
		t.Fatalf("a kid sees the stations of things they may watch or hear: %v", ids)
	}
	e.expect(200, "GET", "/stations?limit=1&offset=1", kidToken, nil, &page)
	if ids := stationIDs(page); len(ids) != 1 || ids[0] != m.Id || page.NextOffset != nil {
		t.Fatalf("paging counts only what the kid sees: %+v", page)
	}
	if page.Total != 2 {
		t.Fatalf("the total counts only what the kid sees: %d", page.Total)
	}
	e.expect(200, "GET", "/stations?limit=1", e.plain.token, nil, &page)
	if page.Total != 4 {
		t.Fatalf("an adult's total counts every shared station: %d", page.Total)
	}
	e.expect(200, "GET", "/stations?limit=1&kind=music", e.plain.token, nil, &page)
	if page.Total != 1 {
		t.Fatalf("the total follows the kind filter: %d", page.Total)
	}
	e.expect(200, "GET", "/admin/stations?limit=1", e.admin.token, nil, &page)
	if page.Total != 4 {
		t.Fatalf("the admin's total counts every station: %d", page.Total)
	}
	e.expect(404, "GET", "/stations/"+itoa(r.Id), kidToken, nil, nil)
	e.expect(404, "GET", "/stations/"+itoa(x.Id), kidToken, nil, nil)
	e.expect(404, "POST", "/stations/"+itoa(r.Id)+"/playback", kidToken, map[string]any{"snapshot": snapshot()}, nil)

	// A song turned explicit takes its station away at once.
	yes := true
	s.Explicit = &yes
	must(t, e.svc.Items.Repo().Update(s))
	e.expect(404, "GET", "/stations/"+itoa(m.Id), kidToken, nil, nil)

	// So does a film rated above the ceiling, with no wait for the rating.
	at(16, 10)
	e.rate(meadow, "R")
	e.expect(404, "GET", "/stations/"+itoa(g.Id), kidToken, nil, nil)
	e.expect(404, "POST", "/stations/"+itoa(g.Id)+"/playback", kidToken, map[string]any{"snapshot": snapshot()}, nil)

	// On an allow list, a station of only allowed titles.
	e.expect(200, "PUT", "/profiles/"+itoa(kid.Id), e.plain.token, map[string]any{"name": "Kid", "birthday": "2018-05-01", "filterMode": "allow_list"}, nil)
	e.expect(204, "PUT", "/profiles/"+itoa(kid.Id)+"/allowed-items/"+itoa(undertow), e.plain.token, nil, nil)
	e.expect(200, "GET", "/stations", kidToken, nil, &page)
	if ids := stationIDs(page); len(ids) != 1 || ids[0] != r.Id {
		t.Fatalf("%v", ids)
	}
}

func TestTuneIn(t *testing.T) {
	e := newEnv(t)
	at := e.stationClock()
	lantern := e.film(e.library("movies"), "Lantern", "2001-01-01", "PG")
	pl := e.nightsPlaylist(map[int64]int64{lantern: 90})
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl}, &st)
	tune := "/stations/" + itoa(st.Id) + "/playback"

	raw := e.expect(409, "POST", tune, e.plain.token, map[string]any{"snapshot": snapshot()}, nil)
	var off api.Error
	must(t, json.Unmarshal(raw, &off))
	if off.Code != api.ErrorCodeOffAir || off.ResumesAt == nil || !off.ResumesAt.Equal(at(16, 0)) {
		t.Fatalf("%s", raw)
	}
	e.expect(400, "POST", tune, e.plain.token, map[string]any{}, nil)
	e.expect(404, "POST", tune, e.admin.token, map[string]any{"snapshot": snapshot()}, nil)

	at(16, 10)
	// The program on now has no file on disk: unavailable, not off air.
	fs, err := e.svc.MediaFiles.Repo().FilesForItem(lantern)
	must(t, err)
	must(t, os.Rename(fs[0].Path, fs[0].Path+".away"))
	if raw = e.expect(409, "POST", tune, e.plain.token, map[string]any{"snapshot": snapshot()}, nil); errorCode(t, raw) != "conflict" {
		t.Fatalf("%s", raw)
	}
	must(t, os.Rename(fs[0].Path+".away", fs[0].Path))
	var got api.StationPlayback
	e.expect(201, "POST", tune, e.plain.token, map[string]any{"snapshot": snapshot()}, &got)
	if got.Stream.PositionMs != 600000 || got.Stream.Entry.Verdict != api.DirectPlay || got.OnAir.Status != api.OnAir || got.OnAir.Now == nil ||
		got.OnAir.Now.ItemId != lantern || !got.OnAir.Now.EndsAt.Equal(at(17, 30)) || got.OnAir.Next == nil || !got.OnAir.Next.StartsAt.Equal(at(18, 0)) {
		t.Fatalf("%+v", got)
	}
	sid := "/playback/sessions/" + itoa(got.Stream.SessionId)
	e.expect(204, "PUT", sid+"/position", e.plain.token, map[string]any{"positionMs": 5300000}, nil)
	e.expect(404, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(lantern), e.plain.token, nil, nil)
	e.expect(204, "DELETE", sid+"?positionMs=5390000", e.plain.token, nil, nil)
	e.expect(404, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(lantern), e.plain.token, nil, nil)
	var sess api.Session
	e.expect(200, "GET", "/sessions/"+itoa(got.Stream.SessionId), e.admin.token, nil, &sess)
	if sess.ItemId != lantern || sess.AccountId != e.plain.account.ID {
		t.Fatalf("an ordinary session: %+v", sess)
	}
}

func TestMusicStationCountsAsNoPlay(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	var songs api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists", e.plain.token, map[string]any{"name": "Songs", "kind": "music"}, &songs)
	song := e.song(e.library("music"), "Opening")
	e.file(song, 180000)
	e.expect(201, "POST", "/profiles/"+itoa(e.plain.account.ID)+"/playlists/"+itoa(songs.Id)+"/entries", e.plain.token, map[string]any{"itemId": song}, nil)
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Radio", "playlistId": songs.Id}, &st)
	e.expect(201, "POST", "/stations/"+itoa(st.Id)+"/playback", e.plain.token, map[string]any{"snapshot": snapshot()}, nil)
	var recent api.MusicRecentPage
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID)+"/music/recently-played", e.plain.token, nil, &recent)
	if len(recent.Items) != 0 {
		t.Fatalf("%+v", recent)
	}
}

// Play order rows and station programs show an admin an item's whole path
// and anyone else its base name.
func TestPlayOrderAndProgramsShowPathsAsTheCallerMay(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	var it api.Item
	e.expect(201, "POST", "/items", e.admin.token, map[string]any{"libraryId": e.library("movies"), "kind": "movie", "parsedTitle": "Lantern", "path": "/films/Lantern"}, &it)
	e.rate(it.Id, "PG")
	pl := e.nightsPlaylist(map[int64]int64{it.Id: 90})
	var mine api.Playlist
	e.expect(201, "POST", "/profiles/"+itoa(e.admin.account.ID)+"/playlists", e.admin.token, map[string]any{"name": "Mine", "kind": "movies"}, &mine)
	e.expect(201, "POST", "/profiles/"+itoa(e.admin.account.ID)+"/playlists/"+itoa(mine.Id)+"/entries", e.admin.token, map[string]any{"itemId": it.Id}, nil)
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl}, &st)
	e.expect(200, "PUT", "/admin/stations/"+itoa(st.Id)+"/server-wide", e.admin.token, map[string]any{"serverWide": true}, nil)
	for _, c := range []struct {
		token, order, path string
	}{
		{e.plain.token, "/profiles/" + itoa(e.plain.account.ID) + "/playlists/" + itoa(pl) + "/play-order", "Lantern"},
		{e.admin.token, "/profiles/" + itoa(e.admin.account.ID) + "/playlists/" + itoa(mine.Id) + "/play-order", "/films/Lantern"},
	} {
		var order api.PlaylistPlayOrderPage
		e.expect(200, "GET", c.order, c.token, nil, &order)
		var day api.StationProgramPage
		e.expect(200, "GET", "/stations/"+itoa(st.Id)+"/schedule", c.token, nil, &day)
		e.expect(200, "GET", "/stations/"+itoa(st.Id), c.token, nil, &st)
		for _, got := range []*api.Item{order.Items[0].Item, day.Items[0].Item, st.OnAir.Next.Item} {
			if got == nil || got.Path == nil || *got.Path != c.path {
				t.Fatalf("want %q: %+v", c.path, got)
			}
		}
	}
}

// A station made without a playlist owns one: its owner fills it through
// the playlist entry operations, no playlist listing shows it, and it goes
// with the station or when the station takes another playlist.
func TestStationOwnsItsPlaylist(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	meadow := e.film(e.library("movies"), "Meadow", "2001-01-01", "G")
	e.file(meadow, 90*60000)
	me := "/profiles/" + itoa(e.plain.account.ID)
	e.expect(400, "POST", "/stations", e.plain.token, map[string]any{"name": "No kind"}, nil)
	var songs api.Playlist
	e.expect(201, "POST", me+"/playlists", e.plain.token, map[string]any{"name": "Songs", "kind": "music"}, &songs)
	e.expect(400, "POST", "/stations", e.plain.token, map[string]any{"name": "X", "playlistId": songs.Id, "kind": "movies"}, nil)

	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Films", "kind": "movies"}, &st)
	pl := me + "/playlists/" + itoa(st.PlaylistId)
	var p api.Playlist
	e.expect(200, "GET", pl, e.plain.token, nil, &p)
	if st.Kind != api.PlaylistKindMovies || p.Kind != api.PlaylistKindMovies || p.ItemCount != 0 || st.OnAir.Next != nil {
		t.Fatalf("%+v %+v", st, p)
	}
	e.expect(201, "POST", pl+"/entries", e.plain.token, map[string]any{"itemId": meadow}, nil)
	var lists api.PlaylistPage
	e.expect(200, "GET", me+"/playlists", e.plain.token, nil, &lists)
	if len(lists.Items) != 1 || lists.Items[0].Id != songs.Id {
		t.Fatalf("a station's own playlist is not listed: %+v", lists)
	}
	e.expect(200, "POST", "/stations/"+itoa(st.Id)+"/reschedule", e.plain.token, nil, &st)
	if st.OnAir.Next == nil || st.OnAir.Next.ItemId != meadow {
		t.Fatalf("it plays what was added: %+v", st.OnAir)
	}
	e.expect(409, "DELETE", pl, e.plain.token, nil, nil)

	// Its kind changes only while it is empty; absent, the playlist stays.
	e.expect(409, "PUT", "/stations/"+itoa(st.Id), e.plain.token, map[string]any{"name": "Films", "kind": "tv"}, nil)
	e.expect(200, "PUT", "/stations/"+itoa(st.Id), e.plain.token, map[string]any{"name": "Renamed"}, &st)
	if st.PlaylistId != p.Id || st.Name != "Renamed" {
		t.Fatalf("%+v", st)
	}
	var empty api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Later", "kind": "movies"}, &empty)
	e.expect(200, "PUT", "/stations/"+itoa(empty.Id), e.plain.token, map[string]any{"name": "Later", "kind": "tv"}, &empty)
	if empty.Kind != api.PlaylistKindTv || empty.Timing != api.HalfHour {
		t.Fatalf("a new kind without a timing takes its own: %+v", empty)
	}
	var music api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Radio", "kind": "music", "timing": "hour"}, &music)
	if music.Timing != api.Hour {
		t.Fatalf("%+v", music)
	}

	// Taking another playlist drops the one it owned; deleting the station
	// deletes the one it owns.
	owned := empty.PlaylistId
	e.expect(200, "PUT", "/stations/"+itoa(empty.Id), e.plain.token, map[string]any{"name": "Later", "playlistId": songs.Id}, &empty)
	if empty.PlaylistId != songs.Id || empty.Kind != api.PlaylistKindMusic {
		t.Fatalf("%+v", empty)
	}
	e.expect(404, "GET", me+"/playlists/"+itoa(owned), e.plain.token, nil, nil)
	e.expect(204, "DELETE", "/stations/"+itoa(st.Id), e.plain.token, nil, nil)
	e.expect(404, "GET", pl, e.plain.token, nil, nil)
	e.expect(200, "GET", me+"/playlists/"+itoa(songs.Id), e.plain.token, nil, nil)
}

// A TV station's programs carry each episode with its show, on air and in
// the schedule alike.
func TestStationProgramsCarryTheirShow(t *testing.T) {
	e := newEnv(t)
	e.stationClock()
	show, ep := e.show(e.library("tv"), "Harbor", "TV-PG")
	e.file(ep, 30*60000)
	me := "/profiles/" + itoa(e.plain.account.ID)
	var pl api.Playlist
	e.expect(201, "POST", me+"/playlists", e.plain.token, map[string]any{"name": "Shows", "kind": "tv"}, &pl)
	e.expect(201, "POST", me+"/playlists/"+itoa(pl.Id)+"/entries", e.plain.token, map[string]any{"itemId": show}, nil)
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Harbor TV", "playlistId": pl.Id}, &st)
	check := func(p *api.StationProgram) {
		t.Helper()
		if p == nil || p.ItemId != ep || p.Kind != api.PlaylistEntryKindEpisode || p.Item == nil || p.Item.Id != ep ||
			p.Show == nil || p.Show.Id != show || p.Show.ParsedTitle != "Harbor" {
			t.Fatalf("%+v", p)
		}
	}
	check(st.OnAir.Next)
	for _, p := range st.OnAir.UpNext {
		check(&p)
	}
	var day api.StationProgramPage
	e.expect(200, "GET", "/stations/"+itoa(st.Id)+"/schedule", e.plain.token, nil, &day)
	if len(day.Items) == 0 {
		t.Fatal("nothing scheduled")
	}
	for _, p := range day.Items {
		check(&p)
	}
}
