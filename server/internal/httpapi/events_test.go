package httpapi_test

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/scan"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// quiet makes a poll with nothing to hear answer at once.
func (e *env) quiet() { e.svc.EventHold = 20 * time.Millisecond }

// cursor is where a device starts following from.
func (e *env) cursor(token string) string {
	e.t.Helper()
	var p api.EventPage
	e.expect(200, "GET", "/events", token, nil, &p)
	if p.Cursor == "" || p.Resync || len(p.Events) != 0 {
		e.t.Fatalf("a first ask answers only the cursor: %+v", p)
	}
	return p.Cursor
}

// heard polls once from *cursor, with any further query, moves it on and
// returns what came.
func (e *env) heard(token string, cursor *string, query ...string) []api.Event {
	e.t.Helper()
	var p api.EventPage
	raw := e.expect(200, "GET", "/events?after="+url.QueryEscape(*cursor)+strings.Join(query, ""), token, nil, &p)
	if p.Resync {
		e.t.Fatalf("resync from %q", *cursor)
	}
	for _, word := range []string{"title", "name"} {
		if strings.Contains(strings.ToLower(string(raw)), word) {
			e.t.Fatalf("an event carries more than ids: %s", raw)
		}
	}
	*cursor = p.Cursor
	return p.Events
}

func is(ev api.Event, kind api.EventKind, change string) bool {
	return ev.Kind == kind && (change == "" && ev.Change == nil || ev.Change != nil && *ev.Change == change)
}

func (e *env) secondDevice(u user, name, platform string) (string, uid.ID) {
	e.t.Helper()
	token, d, err := e.svc.Devices.Register(u.account.ID, u.household.ID, name, platform)
	must(e.t, err)
	return token, d.ID
}

func (e *env) startOn(token string, profile, item uid.ID) api.PlaybackStream {
	e.t.Helper()
	var st api.PlaybackStream
	e.expect(201, "POST", "/playback/sessions", token, map[string]any{"profileId": profile, "itemId": item, "snapshot": snapshot()}, &st)
	return st
}

func (e *env) position(token string, session uid.ID, ms int64) {
	e.t.Helper()
	e.expect(204, "PUT", "/playback/sessions/"+itoa(session)+"/position", token, map[string]any{"positionMs": ms}, nil)
}

func TestEventsFollowTheProfilesWatchStateAndPlayback(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	m, _ := e.playable()
	me := e.plain.account.ID
	tv, _ := e.secondDevice(e.plain, "TV", "roku")
	at, other := e.cursor(tv), e.cursor(e.admin.token)

	st := e.startOn(e.plain.token, me, m)
	got := e.heard(tv, &at)
	if len(got) != 1 || !is(got[0], api.EventKindPlayback, live.Started) || *got[0].ItemId != m || *got[0].SessionId != st.SessionId ||
		*got[0].DeviceId != e.plain.deviceID {
		t.Fatalf("a start on the laptop, heard on the TV: %+v", got)
	}

	e.position(e.plain.token, st.SessionId, 0)
	if got = e.heard(tv, &at); len(got) != 1 || !is(got[0], api.EventKindWatchState, "") || *got[0].ItemId != m {
		t.Fatalf("the first position: %+v", got)
	}
	e.position(e.plain.token, st.SessionId, 5_000)
	if got = e.heard(tv, &at); len(got) != 1 || !is(got[0], api.EventKindWatchState, "") {
		t.Fatalf("a position past the start puts the title in Continue watching: %+v", got)
	}
	e.position(e.plain.token, st.SessionId, 15_000)
	if got = e.heard(tv, &at); len(got) != 0 {
		t.Fatalf("playing on says nothing: %+v", got)
	}
	e.position(e.plain.token, st.SessionId, 600_000)
	if got = e.heard(tv, &at); len(got) != 1 || !is(got[0], api.EventKindWatchState, "") {
		t.Fatalf("a seek: %+v", got)
	}

	e.expect(204, "DELETE", "/playback/sessions/"+itoa(st.SessionId), e.plain.token, nil, nil)
	if got = e.heard(tv, &at); len(got) != 1 || !is(got[0], api.EventKindPlayback, live.Ended) || *got[0].SessionId != st.SessionId {
		t.Fatalf("the end: %+v", got)
	}

	ws := "/profiles/" + itoa(me) + "/watch-state/" + itoa(m)
	e.expect(200, "PUT", ws, e.plain.token, map[string]any{"watched": true}, nil)
	e.expect(200, "PUT", ws, e.plain.token, map[string]any{"watched": true}, nil)
	e.expect(204, "DELETE", ws, e.plain.token, nil, nil)
	if got = e.heard(tv, &at); len(got) != 2 || !is(got[0], api.EventKindWatchState, "") || !is(got[1], api.EventKindWatchState, "") {
		t.Fatalf("marked watched once, then forgotten: %+v", got)
	}

	if got = e.heard(e.admin.token, &other); len(got) != 0 {
		t.Fatalf("another household heard %+v", got)
	}
}

// What follows playback shows videos; a song's start, progress, end and
// being played through say nothing.
func TestASongPlaysUnheard(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	m := e.music()
	tv, _ := e.secondDevice(e.plain, "TV", "roku")
	at := e.cursor(tv)

	first := e.start(m.songs[0].Id, songSnapshot(true, false))
	e.position(e.plain.token, first.SessionId, 0)
	e.position(e.plain.token, first.SessionId, 5_000)
	e.position(e.plain.token, first.SessionId, *m.songs[0].DurationMs)
	second := e.start(m.songs[1].Id, songSnapshot(true, false))
	e.position(e.plain.token, second.SessionId, 5_000)
	e.expect(204, "DELETE", "/playback/sessions/"+itoa(second.SessionId), e.plain.token, nil, nil)
	if got := e.heard(tv, &at); len(got) != 0 {
		t.Fatalf("songs played on the laptop, heard on the TV: %+v", got)
	}
}

func TestEventsAreHeldUntilSomethingHappens(t *testing.T) {
	e := newEnv(t)
	e.svc.EventHold = 10 * time.Second
	m, _ := e.playable()
	at := e.cursor(e.plain.token)
	began := time.Now()
	go func() {
		time.Sleep(150 * time.Millisecond)
		e.svc.Events.Publish(live.Event{Kind: live.WatchState, AccountIDs: []uid.ID{e.admin.account.ID}, ItemID: &m})
		e.svc.Events.Publish(live.Event{Kind: live.WatchState, AccountIDs: []uid.ID{e.plain.account.ID}, ItemID: &m})
	}()
	got := e.heard(e.plain.token, &at)
	if took := time.Since(began); len(got) != 1 || took < 100*time.Millisecond || took > 5*time.Second {
		t.Fatalf("%+v after %s", got, took)
	}
	if at != e.svc.Events.Cursor() {
		t.Fatalf("the cursor %q is not past what was skipped, %q", at, e.svc.Events.Cursor())
	}
}

func TestEventsResync(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	at := e.cursor(e.plain.token)
	for _, after := range []string{"junk", "00000000.4"} {
		var p api.EventPage
		e.expect(200, "GET", "/events?after="+after, e.plain.token, nil, &p)
		if !p.Resync || p.Cursor != at || len(p.Events) != 0 {
			t.Fatalf("after=%s: %+v", after, p)
		}
	}
	for range 513 {
		e.svc.Events.Publish(live.Event{Kind: live.Playlist})
	}
	var p api.EventPage
	e.expect(200, "GET", "/events?after="+at, e.plain.token, nil, &p)
	if !p.Resync || p.Cursor == at {
		t.Fatalf("a follower further back than the backlog: %+v", p)
	}
	e.expect(401, "GET", "/events", "", nil, nil)
	e.expect(400, "GET", "/events?after="+strings.Repeat("x", 65), e.plain.token, nil, nil)
}

func TestKidsHearNothingOfWhatTheirFilterHides(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	e.stationClock()
	films := e.library("movies")
	meadow, undertow := e.film(films, "Meadow", "2001-01-01", "G"), e.film(films, "Undertow", "2001-01-01", "R")
	kid := e.kid(e.plain, "Kid") // PG ceiling
	tablet := e.kidDevice(e.plain, kid.Id)
	at := e.cursor(tablet)

	// Watch together opens on each film; only the one the kid may see is heard.
	var w api.WatchTogether
	e.expect(201, "POST", "/watch-together", e.plain.token, map[string]any{"itemId": undertow}, &w)
	if got := e.heard(tablet, &at); len(got) != 0 {
		t.Fatalf("a Watch together on an R film reached a PG kid: %+v", got)
	}
	e.expect(201, "POST", "/watch-together", e.plain.token, map[string]any{"itemId": meadow}, &w)
	got := e.heard(tablet, &at)
	if len(got) != 1 || !is(got[0], api.EventKindWatchTogether, live.Opened) || *got[0].ItemId != meadow {
		t.Fatalf("the one on a G film (the R one's end stays unheard): %+v", got)
	}

	// A household station is heard only when the kid may see all it plays.
	strong, gentle := e.nightsPlaylist(map[uid.ID]int64{undertow: 90}), e.nightsPlaylist(map[uid.ID]int64{meadow: 90})
	if got := e.heard(tablet, &at); len(got) != 0 {
		t.Fatalf("an adult's playlists reached a kid: %+v", got)
	}
	var r, g api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Strong", "playlistId": strong, "visibility": "household"}, &r)
	if got := e.heard(tablet, &at); len(got) != 0 {
		t.Fatalf("a station playing an R film reached a PG kid: %+v", got)
	}
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Gentle", "playlistId": gentle, "visibility": "household"}, &g)
	if got := e.heard(tablet, &at); len(got) == 0 || !is(got[0], api.EventKindStation, "") || *got[0].StationId != g.Id {
		t.Fatalf("a station the kid may see: %+v", got)
	}
	e.expect(204, "DELETE", "/stations/"+itoa(r.Id), e.plain.token, nil, nil)
	if got := e.heard(tablet, &at); len(got) != 0 {
		t.Fatalf("the hidden station's end reached the kid: %+v", got)
	}

	// The kid's own watch state, set by the adult, is the kid's to hear.
	e.expect(200, "PUT", "/profiles/"+itoa(kid.Id)+"/watch-state/"+itoa(meadow), e.plain.token, map[string]any{"watched": true}, nil)
	if got := e.heard(tablet, &at); len(got) != 1 || !is(got[0], api.EventKindWatchState, "") || *got[0].ItemId != meadow {
		t.Fatalf("%+v", got)
	}
	adult := e.cursor(e.plain.token)
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(undertow), e.plain.token, map[string]any{"watched": true}, nil)
	if got := e.heard(tablet, &at); len(got) != 0 {
		t.Fatalf("the adult's watch state reached the kid: %+v", got)
	}
	if got := e.heard(e.plain.token, &adult); len(got) != 1 {
		t.Fatalf("the adult hears their own: %+v", got)
	}
}

func TestStationEvents(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	clock := e.stationClock()
	pl := e.nightsPlaylist(map[uid.ID]int64{e.film(e.library("movies"), "Lantern", "2001-01-01", "PG"): 90})
	neighbor := e.neighbor(e.plain)
	mine, theirs, elsewhere := e.cursor(e.plain.token), e.cursor(neighbor), e.cursor(e.admin.token)
	station := func(got []api.Event, id uid.ID) bool {
		return len(got) > 0 && is(got[0], api.EventKindStation, "") && got[0].StationId != nil && *got[0].StationId == id
	}

	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": "Nights", "playlistId": pl}, &st)
	if got := e.heard(e.plain.token, &mine); !station(got, st.Id) {
		t.Fatalf("the owner: %+v", got)
	}
	if got := e.heard(neighbor, &theirs); len(got) != 0 {
		t.Fatalf("a private station reached the household: %+v", got)
	}

	e.expect(200, "PUT", "/stations/"+itoa(st.Id), e.plain.token, map[string]any{"name": "Nights", "playlistId": pl, "visibility": "household"}, nil)
	if got := e.heard(neighbor, &theirs); !station(got, st.Id) {
		t.Fatalf("shared with the household: %+v", got)
	}
	if got := e.heard(e.admin.token, &elsewhere); len(got) != 0 {
		t.Fatalf("another household: %+v", got)
	}
	e.expect(200, "PUT", "/admin/stations/"+itoa(st.Id)+"/server-wide", e.admin.token, map[string]any{"serverWide": true}, nil)
	if got := e.heard(e.admin.token, &elsewhere); !station(got, st.Id) {
		t.Fatalf("server wide: %+v", got)
	}
	e.expect(200, "PUT", "/admin/stations/"+itoa(st.Id)+"/server-wide", e.admin.token, map[string]any{"serverWide": false}, nil)
	if got := e.heard(e.admin.token, &elsewhere); !station(got, st.Id) {
		t.Fatalf("taken back from every household: %+v", got)
	}

	// The film is on from 16:00 to 17:30: each end of it is a change on air.
	e.heard(e.plain.token, &mine)
	from := clock(15, 5)
	must(t, e.svc.Stations.Aired(from, clock(15, 59)))
	if got := e.heard(e.plain.token, &mine); len(got) != 0 {
		t.Fatalf("nothing started or ended: %+v", got)
	}
	must(t, e.svc.Stations.Aired(clock(15, 59), clock(16, 0)))
	if got := e.heard(e.plain.token, &mine); !station(got, st.Id) || len(got) != 1 {
		t.Fatalf("the film came on: %+v", got)
	}
	must(t, e.svc.Stations.Aired(clock(16, 0), clock(17, 30)))
	if got := e.heard(e.plain.token, &mine); !station(got, st.Id) {
		t.Fatalf("the film ended: %+v", got)
	}

	e.expect(200, "POST", "/stations/"+itoa(st.Id)+"/reschedule", e.plain.token, nil, nil)
	if got := e.heard(e.plain.token, &mine); !station(got, st.Id) {
		t.Fatalf("rescheduled: %+v", got)
	}
	e.heard(neighbor, &theirs)
	e.expect(204, "DELETE", "/stations/"+itoa(st.Id), e.plain.token, nil, nil)
	if got := e.heard(neighbor, &theirs); !station(got, st.Id) {
		t.Fatalf("deleted: %+v", got)
	}
}

func TestPlaylistEventsReachTheOwnerAndThoseSharedWith(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	claire := e.householdAdult(e.plain, "Claire", "claire@example.test")
	film := e.movie(e.library("movies"), "Heat")
	mine, hers := e.cursor(e.plain.token), e.cursor(claire.Token)
	base := "/profiles/" + itoa(e.plain.account.ID) + "/playlists"
	playlist := func(got []api.Event, id uid.ID) bool {
		return len(got) > 0 && is(got[0], api.EventKindPlaylist, "") && *got[0].PlaylistId == id
	}

	var pl api.Playlist
	e.expect(201, "POST", base, e.plain.token, map[string]any{"name": "Nights", "kind": "movies"}, &pl)
	if got := e.heard(e.plain.token, &mine); !playlist(got, pl.Id) || len(got) != 1 {
		t.Fatalf("made: %+v", got)
	}
	one := base + "/" + itoa(pl.Id)
	e.expect(200, "GET", one, e.plain.token, nil, nil)
	e.expect(201, "POST", one+"/entries", e.plain.token, map[string]any{"itemId": film}, nil)
	if got := e.heard(e.plain.token, &mine); !playlist(got, pl.Id) {
		t.Fatalf("an entry added, and reading says nothing: %+v", got)
	}
	e.expect(404, "POST", one+"/entries", e.plain.token, map[string]any{"itemId": uid.N(999)}, nil)
	if got := e.heard(e.plain.token, &mine); len(got) != 0 {
		t.Fatalf("a refused change: %+v", got)
	}
	if got := e.heard(claire.Token, &hers); len(got) != 0 {
		t.Fatalf("an unshared playlist reached another adult: %+v", got)
	}

	e.expect(201, "POST", one+"/shares", e.plain.token, map[string]any{"profileId": claire.Me.Account.Id, "access": "read"}, nil)
	if got := e.heard(claire.Token, &hers); !playlist(got, pl.Id) {
		t.Fatalf("shared: %+v", got)
	}
	e.expect(204, "DELETE", one+"/shares/"+itoa(claire.Me.Account.Id), e.plain.token, nil, nil)
	if got := e.heard(claire.Token, &hers); !playlist(got, pl.Id) {
		t.Fatalf("unshared: %+v", got)
	}
	e.heard(e.plain.token, &mine)
	e.expect(204, "DELETE", one, e.plain.token, nil, nil)
	if got := e.heard(e.plain.token, &mine); !playlist(got, pl.Id) {
		t.Fatalf("deleted: %+v", got)
	}
	// With it may go stations that played it, which anyone may have seen.
	if got := e.heard(claire.Token, &hers); len(got) != 1 || !is(got[0], api.EventKindStation, "") || got[0].StationId != nil {
		t.Fatalf("no longer hers to hear of: %+v", got)
	}
}

func TestWatchTogetherEvents(t *testing.T) {
	p := newPair(t)
	e := p.e
	e.quiet()
	hers, mine := e.cursor(p.claire), e.cursor(p.heidi)
	w := p.open(p.heidi, p.movie)
	got := e.heard(p.claire, &hers)
	if len(got) != 1 || !is(got[0], api.EventKindWatchTogether, live.Opened) || *got[0].WatchTogetherId != w.Id || *got[0].ItemId != p.movie ||
		got[0].SessionId != nil {
		t.Fatalf("the invitation: %+v", got)
	}
	e.heard(p.heidi, &mine)
	e.expect(200, "POST", togetherPath(w)+"/join", p.claire, nil, nil)
	if got = e.heard(p.heidi, &mine); len(got) != 1 || !is(got[0], api.EventKindWatchTogether, live.Joined) {
		t.Fatalf("joined: %+v", got)
	}
	p.state(200, p.claire, w, true, 0)
	if got = e.heard(p.heidi, &mine); len(got) != 0 {
		t.Fatalf("play is the held poll's to tell: %+v", got)
	}
	e.expect(204, "DELETE", togetherPath(w), p.claire, nil, nil)
	if got = e.heard(p.heidi, &mine); len(got) != 1 || !is(got[0], api.EventKindWatchTogether, live.Ended) {
		t.Fatalf("ended: %+v", got)
	}
}

// A scan that changes a library is heard once, by every household that
// follows library events, naming the library; one that changes nothing is
// not heard. A client that does not ask for them, as one released before
// they existed, never hears one and follows on past it.
func TestAScanThatChangesALibraryIsHeard(t *testing.T) {
	e := newEnv(t)
	e.quiet()
	lib := e.library("movies")
	film := e.movie(lib, "Film")
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "A (2001)"), 0o750))
	must(t, os.Link(fixturePath(t, "h264-high41.mp4"), filepath.Join(root, "A (2001)", "a.mp4")))
	must(t, e.svc.Libraries.Repo().AddFolder(&library.Folder{LibraryID: lib, Path: root}, nil))
	sc := scan.New(scan.Config{Libraries: e.svc.Libraries.Repo(), Items: e.svc.Items.Repo(), Files: e.svc.MediaFiles.Repo(),
		Editions: e.svc.Editions.Repo(), Genres: e.svc.Genres.Repo(), People: e.svc.People.Repo(), ThumbDir: t.TempDir(),
		Previews: e.svc.Previews.Repo(), PreviewDir: t.TempDir()})
	r := scan.NewRunner(sc, e.svc.Tasks.Repo())
	r.Events = e.svc.Events
	run := func() {
		t.Helper()
		must(t, r.Sync())
		must(t, r.Trigger(lib))
		must(t, r.Tick(context.Background()))
	}
	const follows = "&kinds=library"
	admin, plain := e.cursor(e.admin.token), e.cursor(e.plain.token)
	run()
	got := e.heard(e.admin.token, &admin, follows)
	if len(got) != 1 || got[0].Kind != api.EventKindLibrary || got[0].LibraryId == nil || *got[0].LibraryId != lib {
		t.Fatalf("%+v", got)
	}
	if got := e.heard(e.plain.token, &plain); len(got) != 0 {
		t.Fatalf("not asked for: %+v", got)
	}
	e.expect(200, "PUT", "/profiles/"+itoa(e.plain.account.ID)+"/watch-state/"+itoa(film), e.plain.token, map[string]any{"watched": true}, nil)
	if got := e.heard(e.plain.token, &plain); len(got) != 1 || got[0].Kind != api.EventKindWatchState {
		t.Fatalf("the next event after the skipped one: %+v", got)
	}
	run()
	if got := e.heard(e.admin.token, &admin, follows); len(got) != 0 {
		t.Fatalf("a scan that changed nothing: %+v", got)
	}
}
