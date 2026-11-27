package item_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func intp(n int) *int { return &n }

func idp(id uid.ID) *uid.ID { return &id }

func setup(t *testing.T) (*item.Repo, uid.ID, uid.ID) {
	d := dbtest.OpenPreferLocal(t)
	lr := library.New(d)
	tv := &library.Library{Name: "tv", Kind: library.TV}
	mv := &library.Library{Name: "mv", Kind: library.Movies}
	if err := lr.Create(tv, nil); err != nil {
		t.Fatal(err)
	}
	if err := lr.Create(mv, nil); err != nil {
		t.Fatal(err)
	}
	return item.New(d), tv.ID, mv.ID
}

func mustCreate(t *testing.T, r *item.Repo, it *item.Item) *item.Item {
	t.Helper()
	if err := r.Create(it); err != nil {
		t.Fatal(err)
	}
	return it
}

func TestMovieCRUD(t *testing.T) {
	r, _, mv := setup(t)
	m := mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, Path: "/m/Heat (1995)", ParsedTitle: "Heat", ParsedYear: intp(1995)})
	got, err := r.Get(m.ID)
	if err != nil || got.ParsedTitle != "Heat" || *got.ParsedYear != 1995 || got.ParentID != nil || got.SeasonNumber != nil {
		t.Fatalf("%+v %v", got, err)
	}
	got.ParsedTitle = "Heat!"
	got.ParsedYear = nil
	if err := r.Update(got); err != nil {
		t.Fatal(err)
	}
	if g, _ := r.Get(m.ID); g.ParsedTitle != "Heat!" || g.ParsedYear != nil {
		t.Fatalf("%+v", g)
	}
	if f, err := r.ByPath(mv, item.Movie, "/m/Heat (1995)"); err != nil || f.ID != m.ID {
		t.Fatal(err)
	}
	if err := r.Delete(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(m.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Delete(m.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestTreeConstraintsAndQueries(t *testing.T) {
	r, tv, _ := setup(t)
	show := mustCreate(t, r, &item.Item{LibraryID: tv, Kind: item.Show, Path: "/tv/Lost", ParsedTitle: "Lost"})
	s0 := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "Specials", SeasonNumber: intp(0)})
	s1 := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "Season 1", SeasonNumber: intp(1)})
	e2 := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &s1.ID, Kind: item.Episode, ParsedTitle: "Two", EpisodeNumber: intp(2)})
	mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &s1.ID, Kind: item.Episode, ParsedTitle: "One", EpisodeNumber: intp(1)})
	mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &s0.ID, Kind: item.Episode, ParsedTitle: "Special", EpisodeNumber: intp(1)})

	dup := &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "dup", SeasonNumber: intp(1)}
	if err := r.Create(dup); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("duplicate season: %v", err)
	}
	if err := r.Create(&item.Item{LibraryID: tv, ParentID: &s1.ID, Kind: item.Episode, ParsedTitle: "x", EpisodeNumber: intp(2)}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("duplicate episode: %v", err)
	}
	if err := r.Create(&item.Item{LibraryID: tv, Kind: item.Episode, ParsedTitle: "orphan"}); err == nil {
		t.Fatal("episode without parent accepted")
	}
	if err := r.Create(&item.Item{LibraryID: tv, Kind: item.Show, Path: "/tv/Lost", ParsedTitle: "again"}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("duplicate path: %v", err)
	}
	if err := r.Create(&item.Item{LibraryID: uid.N(99), Kind: item.Movie, ParsedTitle: "x"}); !errors.Is(err, db.ErrReference) {
		t.Fatalf("bad library: %v", err)
	}

	if s, err := r.Season(show.ID, 1); err != nil || s.ID != s1.ID {
		t.Fatal(err)
	}
	if e, err := r.Episode(s1.ID, 2); err != nil || e.ID != e2.ID {
		t.Fatal(err)
	}
	eps, _ := r.Episodes(show.ID)
	if len(eps) != 3 || eps[0].ParsedTitle != "Special" || eps[1].ParsedTitle != "One" || eps[2].ParsedTitle != "Two" {
		t.Fatalf("episode order: %+v", eps)
	}
	kids, _ := r.List(item.Filter{ParentID: &s1.ID})
	if len(kids) != 2 || kids[0].ParsedTitle != "One" {
		t.Fatalf("%+v", kids)
	}
	if n, _ := r.Count(item.Filter{LibraryID: &tv, Kind: item.Episode}); n != 3 {
		t.Fatal(n)
	}
	if page, _ := r.List(item.Filter{LibraryID: &tv, Kind: item.Episode, Limit: 1, Offset: 2}); len(page) != 1 {
		t.Fatal(len(page))
	}

	if err := r.Delete(show.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := r.Count(item.Filter{LibraryID: &tv}); n != 0 {
		t.Fatalf("children should cascade, %d left", n)
	}
}

func TestMetadata(t *testing.T) {
	r, _, mv := setup(t)
	a := mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "A"})
	b := mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "B"})
	if _, err := r.Metadata(a.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if w, _ := r.WithoutMetadata(mv, 10); len(w) != 2 {
		t.Fatal(len(w))
	}
	pid := int64(603)
	now := time.Now().UTC().Truncate(time.Millisecond)
	retry := now.Add(-time.Minute)
	if err := r.SetMetadata(&item.Metadata{ItemID: a.ID, ProviderID: &pid, Status: item.Matched, Title: "The A", RuntimeMinutes: intp(136), FetchedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMetadata(&item.Metadata{ItemID: b.ID, RetryAt: &retry}); err != nil {
		t.Fatal(err)
	}
	m, _ := r.Metadata(a.ID)
	if m.Status != item.Matched || *m.ProviderID != 603 || m.Title != "The A" || *m.RuntimeMinutes != 136 || m.Overview != "" || m.RetryAt != nil {
		t.Fatalf("%+v", m)
	}
	if !m.FetchedAt.Equal(now) {
		t.Fatal("instant changed")
	}
	if err := r.SetMetadata(&item.Metadata{ItemID: a.ID, Status: item.Matched, Title: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.Metadata(a.ID); m.Title != "Renamed" || m.ProviderID != nil {
		t.Fatalf("upsert: %+v", m)
	}
	if all, err := r.MetadataFor([]uid.ID{a.ID, b.ID, uid.N(999)}); err != nil || len(all) != 2 || all[a.ID].Title != "Renamed" || all[b.ID].RetryAt == nil {
		t.Fatalf("batched: %v %v", all, err)
	}
	if none, err := r.MetadataFor(nil); err != nil || len(none) != 0 {
		t.Fatalf("empty batch: %v %v", none, err)
	}
	due, _ := r.DueForRetry(now, 10)
	if len(due) != 1 || due[0].ItemID != b.ID || due[0].Status != item.Unmatched {
		t.Fatalf("%+v", due)
	}
	if due, _ := r.DueForRetry(now.Add(-time.Hour), 10); len(due) != 0 {
		t.Fatal("not yet due")
	}
	if w, _ := r.WithoutMetadata(mv, 10); len(w) != 0 {
		t.Fatal(len(w))
	}
	if err := r.SetMetadata(&item.Metadata{ItemID: uid.N(999)}); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
	if err := r.DeleteMetadata(a.ID); err != nil {
		t.Fatal(err)
	}
	_ = r.Delete(b.ID)
	if _, err := r.Metadata(b.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("metadata should cascade")
	}
}

func TestLibraryStatsQueries(t *testing.T) {
	r, tv, mv := setup(t)
	show := mustCreate(t, r, &item.Item{LibraryID: tv, Kind: item.Show, Path: "/tv/Lost", ParsedTitle: "Lost"})
	s1 := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "Season 1", SeasonNumber: intp(1)})
	mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &s1.ID, Kind: item.Episode, ParsedTitle: "One", EpisodeNumber: intp(1)})
	mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &s1.ID, Kind: item.Episode, ParsedTitle: "Two", EpisodeNumber: intp(2)})
	a := mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "A"})
	b := mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "B"})
	mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "C"}) // no metadata row
	if err := r.SetMetadata(&item.Metadata{ItemID: a.ID, Status: item.Matched}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMetadata(&item.Metadata{ItemID: b.ID, Status: item.Unmatched}); err != nil {
		t.Fatal(err)
	}

	got, err := r.KindCounts(tv)
	if err != nil || got[item.Show] != 1 || got[item.Season] != 1 || got[item.Episode] != 2 || got[item.Movie] != 0 {
		t.Fatalf("tv counts %v %v", got, err)
	}
	if n, err := r.CountUnmatched(mv); err != nil || n != 2 {
		t.Fatalf("movies unmatched %d %v", n, err)
	}
	if n, err := r.CountUnmatched(tv); err != nil || n != 1 {
		t.Fatalf("seasons and episodes are not looked up, the show is: %d %v", n, err)
	}
}

func TestSearchGenreAndPersonFilters(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	libs, r, genres, people := library.New(d), item.New(d), genre.New(d), person.New(d)
	tv, mv := &library.Library{Name: "tv", Kind: library.TV}, &library.Library{Name: "mv", Kind: library.Movies}
	for _, l := range []*library.Library{tv, mv} {
		if err := libs.Create(l, nil); err != nil {
			t.Fatal(err)
		}
	}
	meta := func(it *item.Item, title, rating string) *item.Item {
		if err := r.SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, Title: title, ContentRating: rating}); err != nil {
			t.Fatal(err)
		}
		return it
	}
	matrix := meta(mustCreate(t, r, &item.Item{LibraryID: mv.ID, Kind: item.Movie, ParsedTitle: "Matrix"}), "The Matrix", "R")
	heat := meta(mustCreate(t, r, &item.Item{LibraryID: mv.ID, Kind: item.Movie, ParsedTitle: "Heat"}), "Heat", "R")
	bluey := meta(mustCreate(t, r, &item.Item{LibraryID: tv.ID, Kind: item.Show, ParsedTitle: "Bluey"}), "Bluey", "TV-Y")
	s1 := mustCreate(t, r, &item.Item{LibraryID: tv.ID, ParentID: &bluey.ID, Kind: item.Season, ParsedTitle: "Bluey", SeasonNumber: intp(1)})
	ep := meta(mustCreate(t, r, &item.Item{LibraryID: tv.ID, ParentID: &s1.ID, Kind: item.Episode, ParsedTitle: "Bluey", EpisodeNumber: intp(1)}), "The Magic Xylophone", "")

	search := func(f item.Filter) []uid.ID {
		t.Helper()
		its, err := r.List(f)
		if err != nil {
			t.Fatal(err)
		}
		n, err := r.Count(f)
		if err != nil || n != len(its) {
			t.Fatalf("count %d for %d listed: %v", n, len(its), err)
		}
		out := []uid.ID{}
		for _, it := range its {
			out = append(out, it.ID)
		}
		return out
	}
	same := func(got []uid.ID, want ...uid.ID) bool { return slices.Equal(got, want) }
	if got := search(item.Filter{Q: "THE MA"}); !same(got, ep.ID, matrix.ID) {
		t.Errorf("provider titles of movies and episodes match, ignoring case: %v", got)
	}
	if got := search(item.Filter{Q: "matr"}); !same(got, matrix.ID) {
		t.Errorf("parsed title: %v", got)
	}
	if got := search(item.Filter{Q: "bluey"}); !same(got, bluey.ID) {
		t.Errorf("a show matches; its season and episodes do not by the show's name: %v", got)
	}
	kid := item.Audience{Kid: true, Movie: []string{"G"}, TV: []string{"TV-Y"}}
	if got := search(item.Filter{Q: "the", Audience: kid}); !same(got, ep.ID) {
		t.Errorf("kid search: %v", got)
	}

	if err := genres.SetForItem(heat.ID, genre.Remote, []genre.Genre{{Name: "Crime"}}); err != nil {
		t.Fatal(err)
	}
	if err := genres.SetForItem(matrix.ID, genre.Remote, []genre.Genre{{Name: "Crime"}, {Name: "Action"}}); err != nil {
		t.Fatal(err)
	}
	crime, _ := genres.ForItem(heat.ID)
	if got := search(item.Filter{GenreID: &crime[0].ID}); !same(got, heat.ID, matrix.ID) {
		t.Errorf("genre: %v", got)
	}
	if got := search(item.Filter{GenreID: &crime[0].ID, Q: "heat"}); !same(got, heat.ID) {
		t.Errorf("genre and q: %v", got)
	}
	keanu := &person.Person{Name: "Keanu Reeves"}
	if err := people.Create(keanu); err != nil {
		t.Fatal(err)
	}
	if err := people.SetCredits(matrix.ID, []person.Credit{{PersonID: keanu.ID, Kind: person.Cast}}); err != nil {
		t.Fatal(err)
	}
	if got := search(item.Filter{PersonID: &keanu.ID}); !same(got, matrix.ID) {
		t.Errorf("person: %v", got)
	}
	if got := search(item.Filter{PersonID: &keanu.ID, Audience: kid}); len(got) != 0 {
		t.Errorf("person filter respects the kid: %v", got)
	}
}

func TestMarkAllMetadataDue(t *testing.T) {
	r, tv, mv := setup(t)
	a := mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "A"})
	mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: "B"})
	now := time.Now().UTC().Truncate(time.Millisecond)
	later := now.Add(7 * 24 * time.Hour)
	if err := r.SetMetadata(&item.Metadata{ItemID: a.ID, Status: item.Matched, RetryAt: &later}); err != nil {
		t.Fatal(err)
	}
	show := mustCreate(t, r, &item.Item{LibraryID: tv, Kind: item.Show, ParsedTitle: "Show"})
	s1 := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "Season 1", SeasonNumber: intp(1)})
	ep := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &s1.ID, Kind: item.Episode, ParsedTitle: "S01E02", EpisodeNumber: intp(2)})
	if err := r.SetMetadata(&item.Metadata{ItemID: ep.ID, Status: item.Matched}); err != nil {
		t.Fatal(err)
	}
	if n, err := r.MarkAllDue(now); err != nil || n != 1 {
		t.Fatalf("only movies and shows with metadata rows are refreshed: %d %v", n, err)
	}
	if due, _ := r.DueForRetry(now, 10); len(due) != 1 || due[0].ItemID != a.ID {
		t.Fatalf("%+v", due)
	}
}

// Recently added asks for the newest few of a kind: for every kind of
// audience the kind's index is walked newest first and stops at the page,
// never the whole table sorted.
func TestNewestOfAKindWalksTheIndex(t *testing.T) {
	d := dbtest.Open(t)
	p := &dbtest.Planner{DBTX: d}
	r := item.New(p)
	for _, a := range []item.Audience{{}, {Kid: true, AccountID: idp(uid.N(1)), Movie: []string{"G"}}, {Kid: true, AccountID: idp(uid.N(1)), AllowList: true}, {HideKids: true}} {
		p.Plans = nil
		if _, err := r.List(item.Filter{Kind: item.Movie, Catalog: true, Newest: true, Audience: a, Limit: 14}); err != nil {
			t.Fatal(err)
		}
		if plan := p.Plans[0]; !strings.HasPrefix(plan, "SEARCH item USING INDEX item_kind_added (kind=?)") || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%+v:\n%s", a, plan)
		}
	}
}

// Hiding kids' content reads each title's shown rating by its key; building
// every title's shown metadata first costs a whole-library pass per query.
func TestHidingKidsContentLooksUpEachRating(t *testing.T) {
	d := dbtest.Open(t)
	p := &dbtest.Planner{DBTX: d}
	r := item.New(p)
	for _, f := range []item.Filter{{Kind: item.Movie, Audience: item.Audience{HideKids: true}}, {Kind: item.Movie, KidsOnly: true}} {
		p.Plans = nil
		if _, err := r.List(f); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.Plans[0], "MATERIALIZE item_shown") {
			t.Errorf("%+v:\n%s", f, p.Plans[0])
		}
	}
}
