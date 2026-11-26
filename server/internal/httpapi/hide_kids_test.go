package httpapi_test

import (
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
)

// hideFixture is kids' content of each kind (a G film, a film an adult
// marked for kids, a TV-Y show) beside a PG film and a TV-MA show, spread
// over collections, genres, people, a playlist and stations.
type hideFixture struct {
	g, g2, override, pg, kidShow, adultShow int64
	kidsSaga, mixedSaga, empty              int64
	family, drama, kidActor, both           int64
	playlist                                int64
	kidsStation, mixedStation               int64
}

func (e *env) hideFixture() hideFixture {
	e.t.Helper()
	e.stationClock()
	var f hideFixture
	films, tv := e.library("movies"), e.library("tv")
	f.g, f.override, f.pg = e.film(films, "Kite G", "2001-01-01", "G"), e.film(films, "Kite Marked", "2002-01-01", "R"), e.film(films, "Kite PG", "2003-01-01", "PG")
	f.g2 = e.film(films, "Kite G2", "2004-01-01", "G")
	e.expect(200, "PUT", "/items/"+itoa(f.override)+"/kids-content", e.plain.token, map[string]any{"kidsContent": true}, nil)
	f.kidShow, _ = e.show(tv, "Kite show", "TV-Y")
	f.adultShow, _ = e.show(tv, "Kite drama", "TV-MA")

	f.kidsSaga = e.tmdbCollection(61, "Kids Saga", f.g, f.override)
	f.mixedSaga = e.tmdbCollection(62, "Mixed Saga", f.g2, f.pg)
	var c api.Collection
	e.expect(201, "POST", "/collections", e.plain.token, map[string]any{"name": "Empty"}, &c)
	f.empty = c.Id

	must(e.t, e.svc.Genres.Repo().SetForItem(f.g, genre.Remote, []genre.Genre{{Name: "Family"}}))
	must(e.t, e.svc.Genres.Repo().SetForItem(f.pg, genre.Remote, []genre.Genre{{Name: "Drama"}}))
	var gs api.GenrePage
	e.expect(200, "GET", "/genres", e.plain.token, nil, &gs)
	for _, g := range gs.Items {
		switch g.Name {
		case "Family":
			f.family = g.Id
		case "Drama":
			f.drama = g.Id
		}
	}

	var kidActor, both api.Person
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": "Kid Actor"}, &kidActor)
	e.expect(201, "POST", "/people", e.admin.token, map[string]any{"name": "Both Actor"}, &both)
	f.kidActor, f.both = kidActor.Id, both.Id
	cast := func(ids ...int64) map[string]any {
		var cs []map[string]any
		for _, id := range ids {
			cs = append(cs, map[string]any{"personId": id})
		}
		return map[string]any{"cast": cs, "crew": []any{}}
	}
	e.expect(200, "PUT", "/items/"+itoa(f.g)+"/credits", e.admin.token, cast(f.kidActor, f.both), nil)
	e.expect(200, "PUT", "/items/"+itoa(f.pg)+"/credits", e.admin.token, cast(f.both), nil)

	f.playlist = e.nightsPlaylist(map[int64]int64{f.g: 90, f.pg: 90})
	f.kidsStation = e.station("Cartoons", e.nightsPlaylist(map[int64]int64{f.g: 90, f.override: 90}))
	f.mixedStation = e.station("Mixed", f.playlist)
	return f
}

func (e *env) station(name string, playlist int64) int64 {
	e.t.Helper()
	var st api.Station
	e.expect(201, "POST", "/stations", e.plain.token, map[string]any{"name": name, "playlistId": playlist, "visibility": "household"}, &st)
	return st.Id
}

func (e *env) hideKids(u user, on bool) {
	e.t.Helper()
	e.expect(200, "PUT", "/profiles/"+itoa(u.account.ID)+"/preferences", u.token, map[string]any{"hideKidsContent": on}, nil)
}

// With Hide on, an adult's browse routes leave out kids' content, and the
// Kids filter still lists it; direct routes (an item, playback) are open.
func TestHideKidsContentOnEveryBrowseRoute(t *testing.T) {
	e := newEnv(t)
	f := e.hideFixture()
	tok := e.plain.token
	me := "/profiles/" + itoa(e.plain.account.ID)

	check := func(hidden bool) {
		t.Helper()
		var cols api.CollectionPage
		e.expect(200, "GET", "/collections", tok, nil, &cols)
		got := collectionIDs(cols)
		for id, shown := range map[int64]bool{f.kidsSaga: !hidden, f.mixedSaga: true, f.empty: true} {
			if slices.Contains(got, id) != shown {
				t.Errorf("hidden=%v: collection %d listed=%v in %v", hidden, id, !shown, got)
			}
		}
		for _, c := range cols.Items {
			if c.Id == f.mixedSaga && hidden && c.MovieCount != 1 {
				t.Errorf("the count is of what is shown: %+v", c)
			}
		}
		wantStatus := map[bool]int{true: 404, false: 200}[hidden]
		e.expect(wantStatus, "GET", "/collections/"+itoa(f.kidsSaga), tok, nil, nil)
		var movies api.ItemPage
		e.expect(200, "GET", "/collections/"+itoa(f.mixedSaga)+"/movies", tok, nil, &movies)
		if slices.Contains(itemIDs(movies), f.g2) == hidden {
			t.Errorf("hidden=%v: collection movies %v", hidden, itemIDs(movies))
		}
		var one api.Item
		e.expect(200, "GET", "/items/"+itoa(f.pg), tok, nil, &one)
		if one.Collections == nil || len(*one.Collections) != 1 || (*one.Collections)[0].MovieCount != map[bool]int{true: 1, false: 2}[hidden] {
			t.Errorf("hidden=%v: movie details name collections as browsed: %+v", hidden, one.Collections)
		}

		var gs api.GenrePage
		e.expect(200, "GET", "/genres", tok, nil, &gs)
		if hasGenre := slices.ContainsFunc(gs.Items, func(g api.Genre) bool { return g.Id == f.family }); hasGenre == hidden {
			t.Errorf("hidden=%v: genres %+v", hidden, gs.Items)
		}
		e.expect(wantStatus, "GET", "/genres/"+itoa(f.family), tok, nil, nil)

		var ps api.PersonPage
		e.expect(200, "GET", "/people", tok, nil, &ps)
		if hasKidActor := slices.ContainsFunc(ps.Items, func(p api.Person) bool { return p.Id == f.kidActor }); hasKidActor == hidden {
			t.Errorf("hidden=%v: people %+v", hidden, ps.Items)
		}
		e.expect(wantStatus, "GET", "/people/"+itoa(f.kidActor), tok, nil, nil)
		e.expect(200, "GET", "/people/"+itoa(f.both), tok, nil, nil)
		var credited api.ItemPage
		e.expect(200, "GET", "/items?personId="+itoa(f.both), tok, nil, &credited)
		if slices.Contains(itemIDs(credited), f.g) == hidden {
			t.Errorf("hidden=%v: credits %v", hidden, itemIDs(credited))
		}

		var pl api.Playlist
		e.expect(200, "GET", me+"/playlists/"+itoa(f.playlist), tok, nil, &pl)
		var entries api.PlaylistEntryPage
		e.expect(200, "GET", me+"/playlists/"+itoa(f.playlist)+"/entries", tok, nil, &entries)
		var order api.PlaylistPlayOrderPage
		e.expect(200, "GET", me+"/playlists/"+itoa(f.playlist)+"/play-order", tok, nil, &order)
		want := 2
		if hidden {
			want = 1
		}
		if pl.ItemCount != want || len(entries.Items) != want || len(order.Items) != want {
			t.Errorf("hidden=%v: playlist %d entries %d play order %d", hidden, pl.ItemCount, len(entries.Items), len(order.Items))
		}

		var sts api.StationPage
		e.expect(200, "GET", "/stations", tok, nil, &sts)
		if slices.Contains(stationIDs(sts), f.kidsStation) == hidden || !slices.Contains(stationIDs(sts), f.mixedStation) {
			t.Errorf("hidden=%v: stations %v", hidden, stationIDs(sts))
		}
		if sts.Total != len(sts.Items) {
			t.Errorf("hidden=%v: stations total %d of %d listed", hidden, sts.Total, len(sts.Items))
		}
		e.expect(wantStatus, "GET", "/stations/"+itoa(f.kidsStation), tok, nil, nil)
		var day api.StationProgramPage
		e.expect(200, "GET", "/stations/"+itoa(f.mixedStation)+"/schedule", tok, nil, &day)
		for _, p := range day.Items {
			if hidden && p.ItemId == f.g {
				t.Errorf("the schedule shows a kids' program")
			}
		}

		var found api.SearchResult
		e.expect(200, "GET", "/search?q=kite", tok, nil, &found)
		var movieIDs []int64
		for _, m := range found.Movies {
			movieIDs = append(movieIDs, m.Id)
		}
		if slices.Contains(movieIDs, f.g) == hidden || slices.Contains(movieIDs, f.override) == hidden {
			t.Errorf("hidden=%v: search %v", hidden, movieIDs)
		}
	}

	check(false)
	e.hideKids(e.plain, true)
	check(true)

	// Direct routes stay open: one item, its seasons, playback.
	e.expect(200, "GET", "/items/"+itoa(f.g), tok, nil, nil)
	e.expect(200, "GET", "/items/"+itoa(f.override), tok, nil, nil)
	var seasons api.ItemPage
	e.expect(200, "GET", "/items?parentId="+itoa(f.kidShow), tok, nil, &seasons)
	if len(seasons.Items) != 1 {
		t.Errorf("a show opened from the Kids filter lists its seasons: %+v", seasons)
	}
	e.expect(201, "POST", "/playback/sessions", tok, map[string]any{"profileId": e.plain.account.ID, "itemId": f.g, "snapshot": snapshot()}, nil)

	// Management stays open: its owner still edits a station of only kids' content.
	e.expect(204, "DELETE", "/stations/"+itoa(f.kidsStation), tok, nil, nil)

	// The Kids filter lists only kids' content and ignores Hide.
	var kids api.ItemPage
	e.expect(200, "GET", "/items?kind=movie&kidsContent=true", tok, nil, &kids)
	if got := itemIDs(kids); len(got) != 3 || !slices.Contains(got, f.g) || !slices.Contains(got, f.g2) || !slices.Contains(got, f.override) {
		t.Errorf("kids' films: %v", got)
	}
	e.expect(200, "GET", "/items?kind=show&kidsContent=true", tok, nil, &kids)
	if got := itemIDs(kids); !slices.Equal(got, []int64{f.kidShow}) {
		t.Errorf("kids' shows: %v", got)
	}
	e.expect(200, "GET", "/items?kind=movie", tok, nil, &kids)
	if got := itemIDs(kids); !slices.Equal(got, []int64{f.pg}) {
		t.Errorf("Hide still applies without the filter: %v", got)
	}
}

// The preference belongs to the profile, so every device of it sees it.
func TestHideKidsContentFollowsTheProfileAcrossDevices(t *testing.T) {
	e := newEnv(t)
	f := e.hideFixture()
	phone, _, err := e.svc.Devices.Register(e.plain.account.ID, e.plain.household.ID, "Phone", "web")
	must(t, err)
	e.hideKids(e.plain, true)
	var who api.Account
	e.expect(200, "GET", "/profiles/"+itoa(e.plain.account.ID), phone, nil, &who)
	if who.HideKidsContent == nil || !*who.HideKidsContent {
		t.Fatalf("%+v", who)
	}
	var page api.ItemPage
	e.expect(200, "GET", "/items?kind=movie", phone, nil, &page)
	if slices.Contains(itemIDs(page), f.g) {
		t.Fatalf("the other device hides too: %v", itemIDs(page))
	}
	e.hideKids(e.plain, false)
	e.expect(200, "GET", "/items?kind=movie", phone, nil, &page)
	if !slices.Contains(itemIDs(page), f.g) {
		t.Fatal("and shows again")
	}
}

// The Kids filter narrows an adult who hides nothing, and a kid's own
// ceilings still apply under it.
func TestKidsFilterWithoutHideAndForAKid(t *testing.T) {
	e := newEnv(t)
	f := e.hideFixture()
	var page api.ItemPage
	e.expect(200, "GET", "/items?kind=movie&kidsContent=true", e.plain.token, nil, &page)
	if got := itemIDs(page); len(got) != 3 || !slices.Contains(got, f.g) || !slices.Contains(got, f.override) {
		t.Fatalf("%v", got)
	}
	e.expect(200, "GET", "/items?kind=movie", e.plain.token, nil, &page)
	if len(page.Items) != 4 {
		t.Fatalf("unchanged without the filter: %v", itemIDs(page))
	}
	kt := e.kidDevice(e.plain, e.kid(e.plain, "Kid").Id) // PG
	e.expect(200, "GET", "/items?kind=movie&kidsContent=true", kt, nil, &page)
	if got := itemIDs(page); len(got) != 2 || slices.Contains(got, f.override) || slices.Contains(got, f.pg) {
		t.Fatalf("the ceiling still applies: %v", got)
	}
	e.expect(200, "GET", "/items?kind=movie", kt, nil, &page)
	if len(page.Items) != 3 {
		t.Fatalf("a kid is unchanged: %v", itemIDs(page))
	}
	var cols api.CollectionPage
	e.expect(200, "GET", "/collections", kt, nil, &cols)
	if len(cols.Items) != 2 {
		t.Fatalf("a kid is unchanged: %+v", cols)
	}
}

// A station an adult with Hide on can see starts whatever is on air, kids'
// content included, though its schedule leaves that out.
func TestHideKidsContentDoesNotStopAStationPlaying(t *testing.T) {
	e := newEnv(t)
	f := e.hideFixture()
	sched := "/stations/" + itoa(f.mixedStation) + "/schedule"
	var day api.StationProgramPage
	e.expect(200, "GET", sched, e.plain.token, nil, &day)
	var kids *api.StationProgram
	for i, p := range day.Items {
		if p.ItemId == f.g {
			kids = &day.Items[i]
			break
		}
	}
	if kids == nil {
		t.Fatalf("no kids' program scheduled: %+v", day)
	}
	e.svc.Stations.Now = func() time.Time { return kids.StartsAt.Add(10 * time.Minute) }

	e.hideKids(e.plain, true)
	e.expect(200, "GET", sched, e.plain.token, nil, &day)
	for _, p := range day.Items {
		if p.ItemId == f.g {
			t.Fatalf("the schedule shows a kids' program")
		}
	}
	var got api.StationPlayback
	e.expect(201, "POST", "/stations/"+itoa(f.mixedStation)+"/playback", e.plain.token, map[string]any{"snapshot": snapshot()}, &got)
	if got.Stream.PositionMs != 600000 {
		t.Fatalf("%+v", got)
	}
}

// The admin interface shows the whole library whatever its admin hides
// while browsing; the viewer's routes stay filtered, and a kid or an
// adult who is not admin is refused the admin's view.
func TestAdminInterfaceIgnoresTheActiveProfilesFilter(t *testing.T) {
	e := newEnv(t)
	f := e.hideFixture()
	pg, err := e.svc.Items.Repo().Get(f.pg)
	must(t, err)
	r := e.film(pg.LibraryID, "Kite R", "2005-01-01", "R")
	e.hideKids(e.admin, true)
	kid := e.kidDevice(e.admin, e.kid(e.admin, "Kid").Id)
	movies := func(path, tok string) []int64 {
		t.Helper()
		var res api.SearchResult
		e.expect(200, "GET", path, tok, nil, &res)
		return found(res.Movies)
	}

	if got := movies("/admin/search?q=kite", e.admin.token); !slices.Contains(got, f.g) || !slices.Contains(got, f.override) || !slices.Contains(got, r) {
		t.Errorf("the admin's search finds every title: %v", got)
	}
	var sts api.StationPage
	e.expect(200, "GET", "/admin/stations", e.admin.token, nil, &sts)
	if i := slices.Index(stationIDs(sts), f.kidsStation); i < 0 || sts.Items[i].OnAir.Now == nil && sts.Items[i].OnAir.Next == nil {
		t.Errorf("the admin's Stations page shows what a kids' station has on air: %+v", sts.Items)
	}

	if got := movies("/search?q=kite", e.admin.token); slices.Contains(got, f.g) || !slices.Contains(got, r) {
		t.Errorf("the admin browsing still hides kids' content: %v", got)
	}
	if got := movies("/search?q=kite", kid); !slices.Contains(got, f.g) || slices.Contains(got, r) {
		t.Errorf("a kid's search stays under the ceiling: %v", got)
	}
	e.expect(403, "GET", "/admin/search?q=kite", kid, nil, nil)
	e.expect(403, "GET", "/admin/search?q=kite", e.plain.token, nil, nil)
}
