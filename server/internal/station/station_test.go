package station_test

import (
	"database/sql"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type world struct {
	d                *sql.DB
	r                *station.Repo
	owner, neighbor  *account.Account
	kid, stranger    *account.Account
	home, away       int64
	movie            int64
	edition          int64
	playlist, second int64
	entry            int64
}

func adultIn(t *testing.T, d *sql.DB, email string) (*account.Account, int64) {
	a := &account.Account{Kind: account.Adult, Email: email, Name: email, PasswordHash: "x"}
	check(t, account.New(d).Create(a))
	h := &household.Household{Name: email, AdultID: a.ID}
	check(t, household.New(d).Create(h))
	a.HouseholdID = &h.ID
	check(t, account.New(d).Update(a))
	return a, h.ID
}

func newWorld(t *testing.T) world {
	t.Helper()
	d := dbtest.Open(t)
	w := world{d: d, r: station.New(d)}
	w.owner, w.home = adultIn(t, d, "owner@example.test")
	w.stranger, w.away = adultIn(t, d, "stranger@example.test")
	w.neighbor = &account.Account{Kind: account.Adult, Email: "neighbor@example.test", Name: "Neighbor", PasswordHash: "x", HouseholdID: &w.home}
	check(t, account.New(d).Create(w.neighbor))
	w.kid = &account.Account{Kind: account.Kid, Name: "Kid", HouseholdID: &w.home, MovieCeiling: "PG", TVCeiling: "TV-Y"}
	check(t, account.New(d).Create(w.kid))
	lib := &library.Library{Name: "Films", Kind: library.Movies}
	check(t, library.New(d).Create(lib))
	m := &item.Item{LibraryID: lib.ID, Kind: item.Movie, ParsedTitle: "Lantern"}
	check(t, item.New(d).Create(m))
	check(t, item.New(d).SetMetadata(&item.Metadata{ItemID: m.ID, Status: item.Matched, Title: "Lantern", ContentRating: "PG"}))
	dur := int64(5400000)
	f := &mediafile.File{ItemID: m.ID, Path: "/films/lantern.mp4", SizeBytes: 1, ModifiedAt: time.Now(), DurationMs: &dur}
	check(t, mediafile.New(d).CreateFile(f))
	ed, err := edition.New(d).Default(m.ID)
	check(t, err)
	w.movie, w.edition = m.ID, ed.ID
	for _, p := range []*int64{&w.playlist, &w.second} {
		pl := &playlist.Playlist{AccountID: w.owner.ID, Name: "Nights", Kind: playlist.Movies}
		check(t, playlist.New(d).Create(pl))
		*p = pl.ID
	}
	e := &playlist.Entry{PlaylistID: w.playlist, ItemID: m.ID, EditionID: ed.ID}
	check(t, playlist.New(d).AddEntry(e, nil))
	w.entry = e.ID
	return w
}

func viewer(a *account.Account, household int64, audience item.Audience) station.Viewer {
	return station.Viewer{ProfileID: a.ID, HouseholdID: household, Audience: audience}
}

func TestStationCRUD(t *testing.T) {
	w := newWorld(t)
	s := &station.Station{AccountID: w.owner.ID, PlaylistID: w.playlist, Name: "Late Show", Random: true, Visibility: station.Household,
		Timing: station.HalfHour, Schedule: station.Schedule{Days: []time.Weekday{time.Friday, time.Saturday}, Times: []station.Range{{18 * 60, 2 * 60}}}}
	check(t, w.r.Create(s))
	got, err := w.r.Get(s.ID)
	check(t, err)
	if got.Name != "Late Show" || !got.Random || got.Kind != playlist.Movies || got.HouseholdID != w.home || got.Visibility != station.Household || got.Timing != station.HalfHour ||
		!slices.Equal(got.Schedule.Days, []time.Weekday{time.Friday, time.Saturday}) || !slices.Equal(got.Schedule.Times, []station.Range{{1080, 120}}) ||
		got.CreatedAt.IsZero() || got.Rating != "" {
		t.Fatalf("%+v", got)
	}
	got.Name, got.Random, got.PlaylistID, got.Schedule, got.Timing = "Matinee", false, w.second, station.Schedule{}, station.Continuous
	check(t, w.r.Update(got))
	check(t, w.r.SetRating(s.ID, "PG"))
	if got, err = w.r.Get(s.ID); err != nil || got.Name != "Matinee" || got.Random || got.PlaylistID != w.second || !got.Schedule.Continuous() || got.Rating != "PG" ||
		got.Timing != station.Continuous {
		t.Fatalf("%+v %v", got, err)
	}
	check(t, w.r.SetVisibility(s.ID, station.Server))
	if got, _ = w.r.Get(s.ID); got.Visibility != station.Server {
		t.Fatalf("%+v", got)
	}
	if used, err := w.r.UsesPlaylist(w.second, w.owner.ID); err != nil || !used {
		t.Fatalf("used %v %v", used, err)
	}
	if used, err := w.r.UsesPlaylist(w.second, w.stranger.ID); err != nil || used {
		t.Fatalf("only the owner's stations count %v %v", used, err)
	}
	all, err := w.r.All(10, 0)
	if err != nil || len(all) != 1 {
		t.Fatalf("%v %v", all, err)
	}
	check(t, w.r.Delete(s.ID))
	if _, err := w.r.Get(s.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestDeletePlaying(t *testing.T) {
	w := newWorld(t)
	mine := &station.Station{AccountID: w.owner.ID, PlaylistID: w.playlist, Name: "Mine", Visibility: station.Private, Timing: station.Hour}
	theirs := &station.Station{AccountID: w.stranger.ID, PlaylistID: w.playlist, Name: "Theirs", Visibility: station.Private, Timing: station.Hour}
	other := &station.Station{AccountID: w.stranger.ID, PlaylistID: w.second, Name: "Other", Visibility: station.Private, Timing: station.Hour}
	for _, s := range []*station.Station{mine, theirs, other} {
		check(t, w.r.Create(s))
	}
	check(t, w.r.DeletePlaying(w.playlist, w.stranger.ID))
	for id, want := range map[int64]bool{mine.ID: true, theirs.ID: false, other.ID: true} {
		if _, err := w.r.Get(id); (err == nil) != want {
			t.Fatalf("station %d kept %v: %v", id, want, err)
		}
	}
	check(t, w.r.DeletePlaying(w.playlist, 0))
	if _, err := w.r.Get(mine.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("0 deletes everyone's: %v", err)
	}
}

func TestStationVisibility(t *testing.T) {
	w := newWorld(t)
	mk := func(name, vis, rating string) int64 {
		s := &station.Station{AccountID: w.owner.ID, PlaylistID: w.playlist, Name: name, Visibility: vis, Timing: station.Hour}
		check(t, w.r.Create(s))
		check(t, w.r.SetRating(s.ID, rating))
		return s.ID
	}
	private, house, server, strong, unrated := mk("A private", station.Private, "PG"), mk("B house", station.Household, "G"),
		mk("C server", station.Server, "PG"), mk("D strong", station.Household, "R"), mk("E unrated", station.Household, "")
	kidAudience := item.Audience{Kid: true, AccountID: w.kid.ID, Movie: account.AtOrBelow(account.MovieRatings, "PG"), TV: account.AtOrBelow(account.TVRatings, "TV-Y")}
	for _, c := range []struct {
		name string
		v    station.Viewer
		want []int64
	}{
		{"owner", viewer(w.owner, w.home, item.Audience{}), []int64{private, house, server, strong, unrated}},
		{"household adult", viewer(w.neighbor, w.home, item.Audience{}), []int64{house, server, strong, unrated}},
		// What a kid may see of a shared station its play order decides,
		// above the repository.
		{"kid", viewer(w.kid, w.home, kidAudience), []int64{house, server, strong, unrated}},
		{"another household", viewer(w.stranger, w.away, item.Audience{}), []int64{server}},
	} {
		got, err := w.r.List(c.v, "", 50, 0)
		check(t, err)
		var ids []int64
		for _, s := range got {
			ids = append(ids, s.ID)
		}
		if !slices.Equal(ids, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, ids, c.want)
		}
		for _, id := range []int64{private, house, server, strong, unrated} {
			_, err := w.r.Seen(id, c.v)
			if seen := err == nil; seen != slices.Contains(c.want, id) {
				t.Errorf("%s: Seen(%d) = %v", c.name, id, err)
			}
		}
	}
	if got, _ := w.r.List(viewer(w.owner, w.home, item.Audience{}), playlist.Music, 50, 0); len(got) != 0 {
		t.Errorf("the kind filter: %v", got)
	}
	if got, _ := w.r.List(viewer(w.owner, w.home, item.Audience{}), "", 2, 1); len(got) != 2 || got[0].ID != house {
		t.Errorf("paging by name: %v", got)
	}
}

func TestStationPrograms(t *testing.T) {
	w := newWorld(t)
	s := &station.Station{AccountID: w.owner.ID, PlaylistID: w.playlist, Name: "Nights", Visibility: station.Private, Timing: station.Hour}
	check(t, w.r.Create(s))
	base := time.Date(2026, 11, 13, 20, 0, 0, 0, time.UTC)
	prog := func(h, pos int) station.Program {
		return station.Program{Row: station.Row{Key: station.Key{EntryID: w.entry, ItemID: w.movie}, EditionID: w.edition}, Loop: 3, Position: pos,
			Start: base.Add(time.Duration(h) * time.Hour), End: base.Add(time.Duration(h)*time.Hour + 90*time.Minute)}
	}
	loop := &station.Loop{No: 3, Keys: []station.Key{{EntryID: w.entry, ItemID: w.movie}, {EntryID: 99, ItemID: 98}}}
	check(t, w.r.Write(s.ID, []station.Program{prog(0, 0), prog(2, 1), prog(4, 0)}, loop))
	cur, end, err := w.r.Last(s.ID)
	if err != nil || cur == nil || cur.Position != 0 || cur.Loop != 3 || cur.EntryID != w.entry || !end.Equal(base.Add(5*time.Hour+30*time.Minute)) {
		t.Fatalf("%+v %v %v", cur, end, err)
	}
	got, err := w.r.Loop(s.ID)
	if err != nil || got.No != 3 || !slices.Equal(got.Keys, loop.Keys) {
		t.Fatalf("%+v %v", got, err)
	}
	adult := item.Audience{}
	on, err := w.r.On(s.ID, base.Add(time.Hour), adult)
	if err != nil || !on.Start.Equal(base) || on.ItemID != w.movie || on.EditionID != w.edition {
		t.Fatalf("%+v %v", on, err)
	}
	if _, err := w.r.On(s.ID, base.Add(100*time.Minute), adult); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("between programs: %v", err)
	}
	ended, err := w.r.Ended(s.ID, base.Add(4*time.Hour))
	if err != nil || !ended.Start.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("the program that ended last: %+v %v", ended, err)
	}
	if _, err := w.r.Ended(s.ID, base.Add(time.Hour)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("none has ended while the first is on: %v", err)
	}
	next, err := w.r.Upcoming(s.ID, base, adult, 1)
	if err != nil || len(next) != 1 || !next[0].Start.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("%+v %v", next, err)
	}
	day, err := w.r.Between(s.ID, base.Add(time.Hour), base.Add(3*time.Hour), adult, 50, 0)
	if err != nil || len(day) != 2 {
		t.Fatalf("overlapping the range: %+v %v", day, err)
	}
	hidden := item.Audience{Kid: true, AccountID: w.kid.ID, Movie: []string{"G"}}
	if _, err := w.r.On(s.ID, base.Add(time.Hour), hidden); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a program the audience may not see is left out: %v", err)
	}

	check(t, w.r.Unstart(s.ID, base.Add(time.Hour)))
	if day, _ = w.r.Between(s.ID, base, base.Add(24*time.Hour), adult, 50, 0); len(day) != 1 {
		t.Fatalf("what is on stays, what has not started goes: %+v", day)
	}
	check(t, w.r.Prune(base.Add(2*time.Hour)))
	if cur, _, _ = w.r.Last(s.ID); cur != nil {
		t.Fatalf("pruned: %+v", cur)
	}

	// A program from another playlist starts the next loop afresh.
	check(t, w.r.Write(s.ID, []station.Program{prog(6, 0)}, &station.Loop{}))
	got2, _ := w.r.Get(s.ID)
	got2.PlaylistID = w.second
	check(t, w.r.Update(got2))
	if cur, _, err = w.r.Last(s.ID); err != nil || cur != nil {
		t.Fatalf("foreign cursor: %+v %v", cur, err)
	}
	check(t, w.r.ClearLoop(s.ID))
	if got, _ = w.r.Loop(s.ID); got.No != 0 || len(got.Keys) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestStationGoesWithItsOwnerAndHoldsItsPlaylist(t *testing.T) {
	w := newWorld(t)
	s := &station.Station{AccountID: w.owner.ID, PlaylistID: w.playlist, Name: "Nights", Visibility: station.Private, Timing: station.Hour}
	check(t, w.r.Create(s))
	if err := playlist.New(w.d).Delete(w.playlist); err == nil {
		t.Fatal("a station's playlist cannot be deleted under it")
	}
	check(t, account.New(w.d).Delete(w.owner.ID))
	if _, err := w.r.Get(s.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}
