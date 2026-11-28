package collection_test

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

type fixture struct {
	d     db.DBTX
	r     *collection.Repo
	items *item.Repo
	lib   uid.ID
	home  uid.ID
	away  uid.ID
	adult uid.ID
	kidPG item.Audience
}

func setup(t *testing.T) fixture {
	t.Helper()
	d := dbtest.OpenPreferLocal(t)
	f := fixture{d: d, r: collection.New(d), items: item.New(d)}
	for i, h := range []*uid.ID{&f.home, &f.away} {
		a := &account.Account{Kind: account.Adult, Email: string(rune('a'+i)) + "@example.test", Name: "A"}
		must(t, account.New(d).Create(a, nil))
		hh := &household.Household{Name: "H", AdultID: a.ID}
		must(t, household.New(d).Create(hh, nil))
		*h, f.adult = hh.ID, a.ID
	}
	l := &library.Library{Name: "Movies", Kind: library.Movies}
	must(t, library.New(d).Create(l, nil))
	f.lib = l.ID
	kid := uid.N(999)
	f.kidPG = item.Audience{Kid: true, AccountID: &kid, Movie: []string{"G", "PG"}}
	return f
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (f fixture) movie(t *testing.T, title, date, rating string) uid.ID {
	t.Helper()
	it := &item.Item{LibraryID: f.lib, Kind: item.Movie, ParsedTitle: title}
	must(t, f.items.Create(it))
	must(t, f.items.SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, Title: title, ReleaseDate: date, ContentRating: rating}))
	return it.ID
}

func ids(vs []*collection.View) (out []uid.ID) {
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func TestTMDBCollectionAppearsAtTwoMoviesAndGoesWithTheLast(t *testing.T) {
	f := setup(t)
	one, two := f.movie(t, "Gale Runner", "2004-06-01", "PG"), f.movie(t, "Gale Runner Returns", "2001-02-03", "R")
	c := &collection.Collection{TMDBID: ptr(77), Name: "Gale Runner Saga", Overview: "Wind.", PosterPath: "/p.jpg"}
	must(t, f.r.PutTMDB(c))
	must(t, f.r.SetTMDBMovie(one, &c.ID))
	if got, _ := f.r.List(f.home, item.Audience{}, 10, 0); len(got) != 0 {
		t.Fatalf("one movie is not a collection yet: %v", ids(got))
	}
	must(t, f.r.SetTMDBMovie(two, &c.ID))
	got, err := f.r.List(f.away, item.Audience{}, 10, 0)
	if err != nil || !slices.Equal(ids(got), []uid.ID{c.ID}) || got[0].MovieCount != 2 || got[0].Source != collection.TMDB {
		t.Fatalf("server-wide at two: %+v %v", got, err)
	}
	if order, _ := f.r.Movies(c.ID, item.Audience{}, 10, 0); !slices.Equal(order, []uid.ID{two, one}) {
		t.Fatalf("by release date: %v", order)
	}
	if v, err := f.r.Seen(f.home, c.ID, f.kidPG); err != nil || v.MovieCount != 1 {
		t.Fatalf("a kid counts only what they may see: %+v %v", v, err)
	}

	// Again with the same TMDB id updates in place and keeps an overview the
	// fetch could not give.
	again := &collection.Collection{TMDBID: ptr(77), Name: "Gale Runner Cycle"}
	must(t, f.r.PutTMDB(again))
	if g, _ := f.r.Get(c.ID); again.ID != c.ID || g.Name != "Gale Runner Cycle" || g.Overview != "Wind." || g.PosterPath != "" {
		t.Fatalf("%+v", g)
	}

	// A movie is in one TMDB collection: moving it leaves the old one.
	other := &collection.Collection{TMDBID: ptr(78), Name: "Other Saga"}
	must(t, f.r.PutTMDB(other))
	must(t, f.r.SetTMDBMovie(two, &other.ID))
	if order, _ := f.r.Movies(c.ID, item.Audience{}, 10, 0); !slices.Equal(order, []uid.ID{one}) {
		t.Fatalf("%v", order)
	}
	must(t, f.r.SetTMDBMovie(one, nil))
	if _, err := f.r.Get(c.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("an empty TMDB collection goes: %v", err)
	}
	must(t, f.items.Delete(two))
	if _, err := f.r.Get(other.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("deleting its last movie takes it too: %v", err)
	}
}

func TestCustomCollectionsStayInTheirHousehold(t *testing.T) {
	f := setup(t)
	g, r := f.movie(t, "Paper Kite", "1999-01-01", "G"), f.movie(t, "Iron Kite", "", "R")
	undated := f.movie(t, "Bare Kite", "", "G")
	c := &collection.Collection{HouseholdID: &f.home, Name: "kites"}
	empty := &collection.Collection{HouseholdID: &f.home, Name: "Afternoon"}
	theirs := &collection.Collection{HouseholdID: &f.away, Name: "Theirs"}
	for _, x := range []*collection.Collection{c, empty, theirs} {
		must(t, f.r.Create(x, nil))
	}
	for _, m := range []uid.ID{r, undated, g} {
		must(t, f.r.AddMovie(c.ID, m))
	}
	must(t, f.r.AddMovie(c.ID, g)) // idempotent
	got, err := f.r.List(f.home, item.Audience{}, 10, 0)
	if err != nil || !slices.Equal(ids(got), []uid.ID{empty.ID, c.ID}) || got[1].MovieCount != 3 || got[1].Source != collection.Custom {
		t.Fatalf("by name, empty ones too for adults: %+v %v", got, err)
	}
	if _, err := f.r.Seen(f.away, c.ID, item.Audience{}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("another household: %v", err)
	}
	if got, _ := f.r.List(f.home, f.kidPG, 10, 0); !slices.Equal(ids(got), []uid.ID{c.ID}) || got[0].MovieCount != 2 {
		t.Fatalf("a kid sees no empty collection: %+v", got)
	}
	if order, _ := f.r.Movies(c.ID, item.Audience{}, 10, 0); !slices.Equal(order, []uid.ID{g, undated, r}) {
		t.Fatalf("dated first, then by title: %v", order)
	}
	if order, _ := f.r.Movies(c.ID, f.kidPG, 10, 0); !slices.Equal(order, []uid.ID{g, undated}) {
		t.Fatalf("%v", order)
	}
	if order, _ := f.r.Movies(c.ID, item.Audience{}, 1, 1); !slices.Equal(order, []uid.ID{undated}) {
		t.Fatalf("paged: %v", order)
	}
	if of, _ := f.r.ForMovie(f.home, g, item.Audience{}); !slices.Equal(ids(of), []uid.ID{c.ID}) {
		t.Fatalf("%v", ids(of))
	}
	if of, _ := f.r.ForMovie(f.away, g, item.Audience{}); len(of) != 0 {
		t.Fatalf("%v", ids(of))
	}

	must(t, f.r.RemoveMovie(c.ID, r))
	if err := f.r.RemoveMovie(c.ID, r); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	c.Name = "Kites"
	must(t, f.r.Update(c, &f.adult))
	if got, err := f.r.Get(c.ID); err != nil || got.CreatedBy != nil || *got.ModifiedBy != f.adult {
		t.Fatalf("the renaming profile is the last author: %+v %v", got, err)
	}
	must(t, f.r.Delete(empty.ID))
	if got, _ := f.r.List(f.home, item.Audience{}, 10, 0); !slices.Equal(ids(got), []uid.ID{c.ID}) || got[0].Name != "Kites" || got[0].MovieCount != 2 {
		t.Fatalf("%+v", got)
	}
}

func TestAroundIsTheTenNearestOtherMoviesInOrder(t *testing.T) {
	f := setup(t)
	c := &collection.Collection{HouseholdID: &f.home, Name: "Long"}
	must(t, f.r.Create(c, nil))
	var m []uid.ID
	for i := range 13 {
		rating := "PG"
		if i == 1 || i == 2 {
			rating = "R"
		}
		m = append(m, f.movie(t, "Part "+string(rune('A'+i)), strconv.Itoa(2000+i)+"-01-01", rating))
		must(t, f.r.AddMovie(c.ID, m[i]))
	}
	join := slices.Concat[[]uid.ID]
	for _, tc := range []struct {
		name  string
		movie uid.ID
		a     item.Audience
		want  []uid.ID
	}{
		{"first: all ten after it", m[0], item.Audience{}, m[1:11]},
		{"middle: five each side", m[6], item.Audience{}, join(m[1:6], m[7:12])},
		{"last: all ten before it", m[12], item.Audience{}, m[2:12]},
		{"a kid's ten visible others are all of them", m[12], f.kidPG, join(m[:1], m[3:12])},
		{"a film the audience may not see still places the window", m[1], f.kidPG, join(m[:1], m[3:12])},
	} {
		if got, err := f.r.Around(c.ID, tc.movie, tc.a, 10); err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v: %v", tc.name, got, tc.want, err)
		}
	}
	if got, _ := f.r.Around(c.ID, m[6], item.Audience{}, 20); len(got) != 12 || slices.Contains(got, m[6]) {
		t.Fatalf("fewer than n gives every other movie: %v", got)
	}
}

// handOrdered makes a custom collection of Kite A to D, released in that
// order, and returns it with their ids.
func handOrdered(t *testing.T, f fixture) (*collection.Collection, []uid.ID) {
	t.Helper()
	c := &collection.Collection{HouseholdID: &f.home, Name: "Kites"}
	must(t, f.r.Create(c, nil))
	var m []uid.ID
	for i := range 4 {
		m = append(m, f.movie(t, "Kite "+string(rune('A'+i)), strconv.Itoa(2000+i)+"-01-01", "PG"))
		must(t, f.r.AddMovie(c.ID, m[i]))
	}
	return c, m
}

func movies(t *testing.T, f fixture, id uid.ID) []uid.ID {
	t.Helper()
	got, err := f.r.Movies(id, item.Audience{}, 20, 0)
	must(t, err)
	return got
}

func at(n int) *int { return &n }

func TestMoveMovieHandOrdersAndEveryConsumerFollows(t *testing.T) {
	f := setup(t)
	c, m := handOrdered(t, f)
	if v, _ := f.r.Seen(f.home, c.ID, item.Audience{}); v.CustomOrder {
		t.Fatal("release order until moved")
	}
	must(t, f.r.MoveMovie(c.ID, m[3], at(0)))
	want := []uid.ID{m[3], m[0], m[1], m[2]}
	if got := movies(t, f, c.ID); !slices.Equal(got, want) {
		t.Fatalf("first move numbers every movie from release order, then moves: %v want %v", got, want)
	}
	if v, _ := f.r.Seen(f.home, c.ID, item.Audience{}); !v.CustomOrder {
		t.Fatal("hand-ordered after a move")
	}
	if got, _ := f.r.Around(c.ID, m[0], item.Audience{}, 2); !slices.Equal(got, []uid.ID{m[3], m[1]}) {
		t.Fatalf("Around follows the hand order: %v", got)
	}

	must(t, f.r.MoveMovie(c.ID, m[3], at(99)))
	if got := movies(t, f, c.ID); !slices.Equal(got, m) {
		t.Fatalf("past the end moves it last: %v", got)
	}
	must(t, f.r.MoveMovie(c.ID, m[0], nil))
	if got := movies(t, f, c.ID); !slices.Equal(got, []uid.ID{m[1], m[2], m[3], m[0]}) {
		t.Fatalf("no position moves it last: %v", got)
	}
	if err := f.r.MoveMovie(c.ID, uid.N(999999), at(0)); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a movie not in it: %v", err)
	}

	early := f.movie(t, "Kite Z", "1990-01-01", "PG")
	must(t, f.r.AddMovie(c.ID, early))
	if got := movies(t, f, c.ID); !slices.Equal(got, []uid.ID{m[1], m[2], m[3], m[0], early}) {
		t.Fatalf("a movie added to a hand-ordered collection goes last: %v", got)
	}
	must(t, f.r.AddMovie(c.ID, early))
	must(t, f.r.RemoveMovie(c.ID, m[2]))
	if got := movies(t, f, c.ID); !slices.Equal(got, []uid.ID{m[1], m[3], m[0], early}) {
		t.Fatalf("removing keeps the rest in order: %v", got)
	}

	must(t, f.r.ResetOrder(c.ID))
	if got := movies(t, f, c.ID); !slices.Equal(got, []uid.ID{early, m[0], m[1], m[3]}) {
		t.Fatalf("reset returns release order: %v", got)
	}
	if v, _ := f.r.Seen(f.home, c.ID, item.Audience{}); v.CustomOrder {
		t.Fatal("reset clears the badge")
	}
}

func TestAddingToAReleaseOrderedCollectionStaysPositionless(t *testing.T) {
	f := setup(t)
	c, m := handOrdered(t, f)
	early := f.movie(t, "Kite Early", "1990-01-01", "PG")
	must(t, f.r.AddMovie(c.ID, early))
	if got := movies(t, f, c.ID); !slices.Equal(got, append([]uid.ID{early}, m...)) {
		t.Fatalf("%v", got)
	}
	if v, _ := f.r.Seen(f.home, c.ID, item.Audience{}); v.CustomOrder {
		t.Fatal("adding does not hand-order")
	}
}

// Only a TMDB collection feeds up next and neighbors, and no route gives it
// positions; this puts them there to show both read the shared order.
func TestUpNextAndNeighborsFollowAHandOrder(t *testing.T) {
	f := setup(t)
	a, b, c := f.movie(t, "Gale A", "2001-01-01", "PG"), f.movie(t, "Gale B", "2002-01-01", "PG"), f.movie(t, "Gale C", "2003-01-01", "PG")
	saga := &collection.Collection{TMDBID: ptr(5), Name: "Gale Saga"}
	must(t, f.r.PutTMDB(saga))
	for i, id := range []uid.ID{c, a, b} {
		must(t, f.r.SetTMDBMovie(id, &saga.ID))
		_, err := f.d.Exec(`UPDATE collection_movie SET position = ? WHERE collection_id = ? AND item_id = ?`, i, saga.ID, id)
		must(t, err)
	}
	if next, err := f.r.NextMovie(c, item.Audience{}); err != nil || next == nil || *next != a {
		t.Fatalf("up next after the first of the hand order: %v %v", next, err)
	}
	if prev, next, err := f.r.MovieNeighbors(a, item.Audience{}); err != nil || prev == nil || *prev != c || next == nil || *next != b {
		t.Fatalf("neighbors in the hand order: %v %v %v", prev, next, err)
	}
}

func ptr(n int64) *int64 { return &n }

// A collection's order reads each movie's shown date and title by its key;
// building every title's shown metadata first costs a whole-library pass.
func TestMovieOrderLooksUpEachMovie(t *testing.T) {
	f := setup(t)
	c := &collection.Collection{HouseholdID: &f.home, Name: "C"}
	must(t, f.r.Create(c, nil))
	must(t, f.r.AddMovie(c.ID, f.movie(t, "A", "2001-01-01", "G")))
	p := &dbtest.Planner{DBTX: f.d}
	if _, err := collection.New(p).Movies(c.ID, item.Audience{}, -1, 0); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Plans[0], "MATERIALIZE item_shown") {
		t.Error(p.Plans[0])
	}
}

// A collection shows its own TMDB image, else that of its first movie the
// reader may see, in Order.
func TestCollectionArtIsItsOwnElseItsFirstSeenMovies(t *testing.T) {
	f := setup(t)
	kids, grown := f.movie(t, "Paper Kite", "2004-01-01", "G"), f.movie(t, "Iron Kite", "1999-01-01", "R")
	must(t, f.items.SetMetadata(&item.Metadata{ItemID: kids, Status: item.Matched, ContentRating: "G", PosterPath: "/kite.jpg"}))
	must(t, f.items.SetMetadata(&item.Metadata{ItemID: grown, Status: item.Matched, ContentRating: "R", PosterPath: "/iron.jpg",
		BackdropPath: "/iron-wide.jpg"}))
	c := &collection.Collection{TMDBID: ptr(90), Name: "Kites"}
	must(t, f.r.PutTMDB(c))
	must(t, f.r.SetTMDBMovie(kids, &c.ID))
	must(t, f.r.SetTMDBMovie(grown, &c.ID))
	art := func(a item.Audience) item.Art {
		t.Helper()
		v, err := f.r.Seen(f.home, c.ID, a)
		must(t, err)
		if got, err := f.r.Art(c.ID, a); err != nil || got != v.Art {
			t.Fatalf("Art %+v %v, the view %+v", got, err, v.Art)
		}
		return v.Art
	}
	if got := art(item.Audience{}); got.Poster != item.ArtURL+"w500/iron.jpg" || got.Backdrop != item.ArtURL+"w1280/iron-wide.jpg" {
		t.Errorf("an adult sees the first movie's: %+v", got)
	}
	if got := art(f.kidPG); got.Poster != item.ArtURL+"w500/kite.jpg" || got.Backdrop != "" {
		t.Errorf("a kid sees the first movie they may: %+v", got)
	}
	c.PosterPath = "/own.jpg"
	must(t, f.r.PutTMDB(c))
	if got := art(f.kidPG); got.Poster != item.ArtURL+"w500/own.jpg" {
		t.Errorf("its own: %+v", got)
	}
}
