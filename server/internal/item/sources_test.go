package item_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
)

// shownAs is what Metadata shows of the fields both rows of every titled
// item below are given: the local row has a title, runtime and poster, the
// remote row a title, overview, rating, poster and backdrop.
type shownAs struct {
	title, overview, rating, poster, posterFrom, backdrop string
	runtime                                               *int
	genres                                                string
}

// Each of the four settings picks every field of a movie, an episode, an
// album and a channel from the source it names first, fills from the other
// only when preferred, and never reads the other when only. Both rows stay
// stored throughout, so switching back shows them again with no write.
func TestSettingsPickEachField(t *testing.T) {
	d := dbtest.Open(t)
	r, genres := item.New(d), genre.New(d)
	libs := library.New(d)
	lib := func(kind string) int64 {
		l := &library.Library{Name: kind, Kind: kind}
		if err := libs.Create(l); err != nil {
			t.Fatal(err)
		}
		return l.ID
	}
	mv, tv, mu, yt := lib(library.Movies), lib(library.TV), lib(library.Music), lib(library.YouTube)
	movie := mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "m"})
	show := mustCreate(t, r, &item.Item{LibraryID: tv, Kind: item.Show, ParsedTitle: "s"})
	season := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "s1", SeasonNumber: intp(1)})
	episode := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &season.ID, Kind: item.Episode, ParsedTitle: "e", EpisodeNumber: intp(1)})
	artist := mustCreate(t, r, &item.Item{LibraryID: mu, Kind: item.Artist, ParsedTitle: "a"})
	album := mustCreate(t, r, &item.Item{LibraryID: mu, ParentID: &artist.ID, Kind: item.Album, ParsedTitle: "al"})
	channel := mustCreate(t, r, &item.Item{LibraryID: yt, Kind: item.Channel, ParsedTitle: "c"})
	providers := map[int64]string{movie.ID: "tmdb", episode.ID: "tmdb", album.ID: item.MusicBrainz, channel.ID: "youtube"}
	for id, provider := range providers {
		if err := r.SetLocal(&item.Metadata{ItemID: id, Title: "Local", RuntimeMinutes: intp(90), PosterPath: "/files/local.jpg"}); err != nil {
			t.Fatal(err)
		}
		if err := r.SetMetadata(&item.Metadata{ItemID: id, Provider: provider, Status: item.Matched, Title: "Remote", Overview: "Told remotely",
			ContentRating: "PG-13", PosterPath: "/remote.jpg", BackdropPath: "/backdrop.jpg"}); err != nil {
			t.Fatal(err)
		}
		if err := genres.SetForItem(id, genre.Local, []genre.Genre{{Name: "Tagged"}}); err != nil {
			t.Fatal(err)
		}
		if err := genres.SetForItem(id, genre.Remote, []genre.Genre{{Name: "Fetched"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Remote-only genres on the show fill under Local preferred and stay
	// hidden under Local only.
	if err := genres.SetForItem(show.ID, genre.Remote, []genre.Genre{{Name: "Fetched"}}); err != nil {
		t.Fatal(err)
	}
	p := person.New(d)
	cast := &person.Person{Name: "Star", TMDBID: new(int64)}
	if err := p.Upsert(cast); err != nil {
		t.Fatal(err)
	}
	if err := p.SetCredits(movie.ID, []person.Credit{{PersonID: cast.ID, Kind: person.Cast}}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		source, mode string
		want         func(provider string) shownAs
		showGenres   string
		credits      int
	}{
		{"local", "only", func(string) shownAs {
			return shownAs{title: "Local", poster: "/files/local.jpg", posterFrom: item.Local, runtime: intp(90), genres: "Tagged"}
		}, "", 0},
		{"local", "preferred", func(pr string) shownAs {
			return shownAs{title: "Local", overview: "Told remotely", rating: "PG-13", poster: "/files/local.jpg", posterFrom: item.Local,
				backdrop: "/backdrop.jpg", runtime: intp(90), genres: "Tagged"}
		}, "Fetched", 1},
		{"remote", "only", func(pr string) shownAs {
			return shownAs{title: "Remote", overview: "Told remotely", rating: "PG-13", poster: "/remote.jpg", posterFrom: pr, backdrop: "/backdrop.jpg",
				genres: "Fetched"}
		}, "Fetched", 1},
		{"remote", "preferred", func(pr string) shownAs {
			return shownAs{title: "Remote", overview: "Told remotely", rating: "PG-13", poster: "/remote.jpg", posterFrom: pr, backdrop: "/backdrop.jpg",
				runtime: intp(90), genres: "Fetched"}
		}, "Fetched", 1},
		{"local", "only", func(string) shownAs {
			return shownAs{title: "Local", poster: "/files/local.jpg", posterFrom: item.Local, runtime: intp(90), genres: "Tagged"}
		}, "", 0},
	}
	for _, c := range cases {
		dbtest.Sources(t, d, c.source, c.mode)
		for _, it := range []*item.Item{movie, episode, album, channel} {
			m, err := r.Metadata(it.ID)
			if err != nil {
				t.Fatalf("%s %s %s: %v", c.source, c.mode, it.Kind, err)
			}
			gs, err := genres.ForItem(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			names := ""
			for _, g := range gs {
				names += g.Name
			}
			got := shownAs{m.Title, m.Overview, m.ContentRating, m.PosterPath, m.PosterFrom, m.BackdropPath, m.RuntimeMinutes, names}
			if want := c.want(providers[it.ID]); deref(got) != deref(want) {
				t.Errorf("%s %s %s: got %+v want %+v", c.source, c.mode, it.Kind, deref(got), deref(want))
			}
		}
		gs, err := genres.ForItem(show.ID)
		if err != nil {
			t.Fatal(err)
		}
		names := ""
		for _, g := range gs {
			names += g.Name
		}
		if names != c.showGenres {
			t.Errorf("%s %s: show genres %q want %q", c.source, c.mode, names, c.showGenres)
		}
		if cs, err := p.Credits(movie.ID); err != nil || len(cs) != c.credits {
			t.Errorf("%s %s: credits %v %v", c.source, c.mode, cs, err)
		}
	}
	// Under Local only an item with only a remote row shows none.
	if _, err := r.Metadata(show.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a remote-only show under Local only: %v", err)
	}
	if n, err := r.CountUnmatched(tv); err != nil || n != 0 {
		t.Fatalf("under Local only nothing is looked up, so nothing is unmatched: %d %v", n, err)
	}
}

func deref(s shownAs) string {
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%v|%s", s.title, s.overview, s.rating, s.poster, s.posterFrom, s.backdrop, s.runtime != nil && *s.runtime == 90, s.genres)
}

// A rating only the remote row gives counts for kids only while the
// settings use remote data; under Local only the movie is unrated, which
// kids under ceilings never see and which is no kids' content.
func TestKidsReadTheShownRating(t *testing.T) {
	d := dbtest.Open(t)
	r := item.New(d)
	l := &library.Library{Name: "mv", Kind: library.Movies}
	if err := library.New(d).Create(l); err != nil {
		t.Fatal(err)
	}
	m := mustCreate(t, r, &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "Cars"})
	if err := r.SetMetadata(&item.Metadata{ItemID: m.ID, Status: item.Matched, ContentRating: "G"}); err != nil {
		t.Fatal(err)
	}
	kid := item.Audience{Kid: true, Movie: []string{"G"}}
	hide := item.Audience{HideKids: true}
	for _, c := range []struct {
		source, mode   string
		kidSees, hides bool
	}{{"local", "only", false, false}, {"remote", "only", true, true}, {"local", "preferred", true, true}} {
		dbtest.Sources(t, d, c.source, c.mode)
		if ok, err := r.Visible(m.ID, kid); err != nil || ok != c.kidSees {
			t.Errorf("%s %s: kid sees %v %v", c.source, c.mode, ok, err)
		}
		if ok, err := r.Visible(m.ID, hide); err != nil || ok == c.hides {
			t.Errorf("%s %s: hidden as kids' content %v %v", c.source, c.mode, !ok, err)
		}
	}
}
