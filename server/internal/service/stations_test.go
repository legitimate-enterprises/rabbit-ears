package service_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
)

var local = time.FixedZone("local", -5*3600)

// fri is 2026-11-13, a Friday, plus days at h:m local.
func fri(days, h, m int) time.Time { return time.Date(2026, 11, 13+days, h, m, 0, 0, local) }

type airing struct {
	t        *testing.T
	s        *service.Services
	now      time.Time
	owner    *account.Account
	lib      int64
	playlist *playlist.Playlist
}

func newAiring(t *testing.T) *airing {
	t.Helper()
	s := service.New(dbtest.OpenPreferLocal(t))
	a := &airing{t: t, s: s, now: fri(0, 15, 5)}
	s.Stations.Now = func() time.Time { return a.now }
	a.owner, _ = adultWithHousehold(t, s, "owner@example.test")
	l := &library.Library{Name: "Films", Kind: library.Movies}
	a.must(s.Libraries.Repo().Create(l))
	a.lib = l.ID
	a.playlist = a.playlistOf("Nights", a.film("Lantern", "PG", 90), a.film("Harbor", "R", 61))
	return a
}

func (a *airing) must(err error) {
	a.t.Helper()
	if err != nil {
		a.t.Fatal(err)
	}
}

// film adds a rated movie with one file of minutes.
func (a *airing) film(title, rating string, minutes int64) int64 {
	a.t.Helper()
	it := &item.Item{LibraryID: a.lib, Kind: item.Movie, ParsedTitle: title}
	a.must(a.s.Items.Repo().Create(it))
	a.must(a.s.Items.Repo().SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, Title: title, ContentRating: rating}))
	ms := minutes * 60000
	a.must(a.s.MediaFiles.Repo().CreateFile(&mediafile.File{ItemID: it.ID, Path: "/films/" + title + ".mp4", SizeBytes: 1, ModifiedAt: a.now, DurationMs: &ms}))
	return it.ID
}

func (a *airing) playlistOf(name string, movies ...int64) *playlist.Playlist {
	a.t.Helper()
	p := &playlist.Playlist{AccountID: a.owner.ID, Name: name, Kind: playlist.Movies}
	a.must(a.s.Playlists.Repo().Create(p))
	for _, m := range movies {
		a.add(p, m)
	}
	return p
}

func (a *airing) add(p *playlist.Playlist, movie int64) {
	a.t.Helper()
	a.must(a.s.Playlists.Repo().AddEntry(&playlist.Entry{PlaylistID: p.ID, ItemID: movie}, nil))
}

func (a *airing) station(sched station.Schedule) *station.Station {
	a.t.Helper()
	st := &station.Station{AccountID: a.owner.ID, PlaylistID: a.playlist.ID, Name: "Late", Visibility: station.Household, Schedule: sched}
	a.must(a.s.Stations.Create(st))
	return st
}

type aired struct {
	item       int64
	start, end time.Time
}

func (a *airing) day(st *station.Station, days int) []aired {
	a.t.Helper()
	ps, err := a.s.Stations.Repo().Between(st.ID, fri(days, 0, 0), fri(days+1, 0, 0), item.Audience{}, 1000, 0)
	a.must(err)
	var out []aired
	for _, p := range ps {
		out = append(out, aired{p.ItemID, p.Start.In(local), p.End.In(local)})
	}
	return out
}

func TestNewStationIsScheduledADayAheadWithItsRating(t *testing.T) {
	a := newAiring(t)
	st := a.station(station.Schedule{})
	if st.Rating != "R" {
		t.Errorf("rating %q", st.Rating)
	}
	if got, _ := a.s.Stations.Repo().Get(st.ID); got.Rating != "R" {
		t.Errorf("stored rating %q", got.Rating)
	}
	today := a.day(st, 0)
	if len(today) < 2 || !today[0].start.Equal(fri(0, 16, 0)) || !today[0].end.Equal(fri(0, 17, 30)) || !today[1].start.Equal(fri(0, 18, 0)) {
		t.Fatalf("today %+v", today)
	}
	tomorrow := a.day(st, 1)
	last := tomorrow[len(tomorrow)-1]
	if !last.end.After(fri(1, 14, 5)) || !last.start.Before(fri(1, 15, 5)) {
		t.Fatalf("a day ahead only: %+v", tomorrow)
	}
}

func TestTimingDefaultsToTheKindsAndPlacesEachStart(t *testing.T) {
	a := newAiring(t)
	if st := a.station(station.Schedule{}); st.Timing != station.Hour {
		t.Fatalf("a movies station's default timing %q", st.Timing)
	}
	for _, c := range []struct {
		timing       string
		second, last time.Time
	}{
		// Lantern runs 15:05 to 16:35 back to back, else from 15:30 or 16:00.
		{station.Continuous, fri(0, 16, 35), fri(0, 17, 36)},
		{station.HalfHour, fri(0, 17, 0), fri(0, 18, 1)},
		{station.Hour, fri(0, 18, 0), fri(0, 19, 1)},
	} {
		st := &station.Station{AccountID: a.owner.ID, PlaylistID: a.playlist.ID, Name: c.timing, Visibility: station.Private, Timing: c.timing}
		a.must(a.s.Stations.Create(st))
		today := a.day(st, 0)
		if len(today) < 2 || !today[1].start.Equal(c.second) || !today[1].end.Equal(c.last) {
			t.Errorf("%s: %+v", c.timing, today[:min(2, len(today))])
		}
	}
}

// Run every few hours, the task keeps every schedule between 23 and 24
// hours ahead, and a station off for days names its next window without
// running ahead.
func TestRunsKeepADayAhead(t *testing.T) {
	a := newAiring(t)
	st := a.station(station.Schedule{})
	tuesdays := a.station(station.Schedule{Days: []time.Weekday{time.Tuesday}, Times: []station.Range{{From: 20 * 60, To: 23 * 60}}})
	for range 8 * 8 {
		a.now = a.now.Add(3 * time.Hour)
		if _, err := a.s.Stations.ScheduleAll(context.Background()); err != nil {
			t.Fatal(err)
		}
		last, err := a.s.Stations.Repo().Upcoming(st.ID, a.now, item.Audience{}, 1000)
		a.must(err)
		if p := last[len(last)-1]; !p.End.After(a.now.Add(23*time.Hour)) || !p.Start.Before(a.now.Add(24*time.Hour)) {
			t.Fatalf("at %v the schedule ends %v", a.now, p.End)
		}
		on, err := a.s.Stations.OnAir(tuesdays, item.Audience{})
		a.must(err)
		if on.Next == nil || on.Next.Start.Sub(a.now) > 8*24*time.Hour || on.Next.Start.In(local).Weekday() != time.Tuesday {
			t.Fatalf("at %v the Tuesday station's next is %+v", a.now, on.Next)
		}
	}
}

func TestOnAir(t *testing.T) {
	a := newAiring(t)
	st := a.station(station.Schedule{})
	on, err := a.s.Stations.OnAir(st, item.Audience{})
	if err != nil || on.Status != service.BetweenPrograms || on.Now != nil || on.Next == nil || !on.Next.Start.Equal(fri(0, 16, 0)) {
		t.Fatalf("%+v %v", on, err)
	}
	a.now = fri(0, 16, 10)
	if on, _ = a.s.Stations.OnAir(st, item.Audience{}); on.Status != service.OnAir || on.Now == nil || on.PositionMs != 600000 ||
		on.Next == nil || !on.Next.Start.Equal(fri(0, 18, 0)) {
		t.Fatalf("%+v", on)
	}
	saturdays := a.station(station.Schedule{Days: []time.Weekday{time.Saturday}})
	if on, _ = a.s.Stations.OnAir(saturdays, item.Audience{}); on.Status != service.OffSchedule || on.Next == nil || !on.Next.Start.Equal(fri(1, 0, 0)) {
		t.Fatalf("%+v", on)
	}
}

func TestTaskExtendsAndRecalculates(t *testing.T) {
	a := newAiring(t)
	st := a.station(station.Schedule{})
	before := a.day(st, 1)
	strong := a.film("Undertow", "NC-17", 30)
	a.add(a.playlist, strong)
	a.now = fri(1, 23, 0)
	if got, _ := a.s.Stations.Repo().Get(st.ID); got.Rating != "R" {
		t.Fatalf("no real-time recalculation: %q", got.Rating)
	}
	sum, err := a.s.Stations.ScheduleAll(context.Background())
	if err != nil || sum == "" {
		t.Fatalf("%q %v", sum, err)
	}
	if got, _ := a.s.Stations.Repo().Get(st.ID); got.Rating != "NC-17" {
		t.Errorf("rating %q", got.Rating)
	}
	after := a.day(st, 1)
	if len(after) <= len(before) || !slices.Equal(after[:len(before)], before) {
		t.Errorf("what is written is not rewritten:\n%+v\n%+v", before, after)
	}
	added, has := append(after[len(before):], a.day(st, 2)...), false
	for _, p := range added {
		has = has || p.item == strong
	}
	if !has || !added[len(added)-1].start.Before(fri(2, 23, 0)) {
		t.Errorf("the change shows in what is added: %+v", added)
	}
}

func TestRescheduleNowKeepsWhatIsOn(t *testing.T) {
	a := newAiring(t)
	st := a.station(station.Schedule{})
	a.now = fri(0, 16, 10)
	on, _ := a.s.Stations.OnAir(st, item.Audience{})
	only := a.film("Lighthouse", "G", 45)
	st.PlaylistID = a.playlistOf("Short", only).ID
	a.must(a.s.Stations.Update(st))
	a.must(a.s.Stations.Reschedule(st))
	today := a.day(st, 0)
	if today[0].item != on.Now.ItemID || !today[0].start.Equal(fri(0, 16, 0)) || today[1].item != only || !today[1].start.Equal(fri(0, 18, 0)) {
		t.Fatalf("%+v", today)
	}
	if st.Rating != "G" {
		t.Errorf("rating %q", st.Rating)
	}
}

func TestReschedulesAtOnceWriteOneSchedule(t *testing.T) {
	a := newAiring(t)
	st := a.station(station.Schedule{})
	want := len(a.day(st, 0)) + len(a.day(st, 1))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		c := *st
		wg.Go(func() { errs[i] = a.s.Stations.Reschedule(&c) })
	}
	wg.Wait()
	a.must(errors.Join(errs...))
	if got := len(a.day(st, 0)) + len(a.day(st, 1)); got != want {
		t.Fatalf("%d programs, want %d", got, want)
	}
}

func TestTune(t *testing.T) {
	a := newAiring(t)
	st := a.station(station.Schedule{})
	_, _, err := a.s.Stations.Tune(st, item.Audience{})
	var off *service.OffAirError
	if !errors.As(err, &off) || off.Resumes == nil || !off.Resumes.Equal(fri(0, 16, 0)) {
		t.Fatalf("%v", err)
	}
	a.now = fri(0, 16, 10)
	p, pos, err := a.s.Stations.Tune(st, item.Audience{})
	if err != nil || pos != 600000 || p.EditionID == 0 {
		t.Fatalf("%+v %d %v", p, pos, err)
	}
	kid := item.Audience{Kid: true, AccountID: 1, Movie: account.AtOrBelow(account.MovieRatings, "G")}
	if _, _, err := a.s.Stations.Tune(st, kid); !errors.Is(err, service.ErrProgramHidden) {
		t.Fatalf("the kid's filter is checked at tune-in: %v", err)
	}
}

// A station off for days still names its next program, so a client can say
// when it is back.
func TestAStationOffForDaysIsScheduledToItsNextWindow(t *testing.T) {
	a := newAiring(t)
	tuesdays := a.station(station.Schedule{Days: []time.Weekday{time.Tuesday}, Times: []station.Range{{From: 20 * 60, To: 23 * 60}}})
	on, err := a.s.Stations.OnAir(tuesdays, item.Audience{})
	if err != nil || on.Status != service.OffSchedule || on.Next == nil || !on.Next.Start.Equal(fri(4, 20, 0)) {
		t.Fatalf("%+v %v", on, err)
	}
	a.now = fri(4, 23, 30)
	if _, err := a.s.Stations.ScheduleAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.now = fri(5, 9, 0)
	if on, _ = a.s.Stations.OnAir(tuesdays, item.Audience{}); on.Next == nil || !on.Next.Start.Equal(fri(11, 20, 0)) {
		t.Fatalf("the week after: %+v", on)
	}
}

// Programs older than the prune cutoff are dropped only after each station is
// extended from its last, so a long gap does not restart an ordered loop.
func TestAGapOfDaysContinuesTheLoop(t *testing.T) {
	a := newAiring(t)
	ids := []int64{a.film("A", "PG", 50), a.film("B", "PG", 50), a.film("C", "PG", 50), a.film("D", "PG", 50), a.film("E", "PG", 50)}
	a.playlist = a.playlistOf("Five", ids...)
	st := a.station(station.Schedule{})
	last, _, err := a.s.Stations.Repo().Last(st.ID)
	a.must(err)
	a.now = fri(3, 9, 0)
	if _, err := a.s.Stations.ScheduleAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	on, err := a.s.Stations.Repo().On(st.ID, a.now, item.Audience{})
	a.must(err)
	if want := ids[(last.Position+1)%len(ids)]; on.ItemID != want {
		t.Fatalf("got item %d, want %d after position %d", on.ItemID, want, last.Position)
	}
}

// An Update landing mid-run, between the run's read of the loop and its
// write, still leaves the new playlist's loop to be shuffled afresh.
func TestUpdateDuringAScheduleRunClearsTheLoop(t *testing.T) {
	a := newAiring(t)
	st := &station.Station{AccountID: a.owner.ID, PlaylistID: a.playlist.ID, Name: "Shuffled", Visibility: station.Household, Random: true}
	a.must(a.s.Stations.Create(st))
	other := a.playlistOf("Other", a.film("Lighthouse", "G", 45))
	updated := make(chan error, 1)
	var once sync.Once
	a.s.Stations.Shuffle = func([]station.Key) {
		once.Do(func() {
			c := *st
			c.PlaylistID = other.ID
			go func() { updated <- a.s.Stations.Update(&c) }()
			select {
			case err := <-updated:
				updated <- err
			case <-time.After(200 * time.Millisecond):
			}
		})
	}
	a.now = a.now.Add(24 * time.Hour)
	if _, err := a.s.Stations.ScheduleAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.must(<-updated)
	if l, err := a.s.Stations.Repo().Loop(st.ID); err != nil || len(l.Keys) != 0 {
		t.Fatalf("the old playlist's loop was written back: %+v %v", l, err)
	}
}
