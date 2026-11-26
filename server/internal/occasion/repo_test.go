package occasion_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCRUDAndSeeds(t *testing.T) {
	r := occasion.New(dbtest.Open(t))
	seeded, err := r.List(100, 0)
	must(t, err)
	want := []occasion.Occasion{{Name: "Christmas", Holiday: "christmas", DaysBefore: 30, DaysAfter: 5, Keywords: []int64{207317}},
		{Name: "Halloween", Holiday: "halloween", DaysBefore: 30, DaysAfter: 1, Keywords: []int64{3335}}}
	if len(seeded) != 2 {
		t.Fatalf("%+v", seeded)
	}
	for i, o := range seeded {
		o.ID = 0
		if !reflect.DeepEqual(*o, want[i]) {
			t.Errorf("seeded %+v, want %+v", *o, want[i])
		}
	}

	o := &occasion.Occasion{Name: "Groundhog Day", Month: 2, Day: 2, DaysBefore: 1, Keywords: []int64{9, 3}}
	must(t, r.Create(o))
	if got, err := r.Get(o.ID); err != nil || !reflect.DeepEqual(got, &occasion.Occasion{ID: o.ID, Name: "Groundhog Day", Month: 2, Day: 2,
		DaysBefore: 1, Keywords: []int64{3, 9}}) {
		t.Fatalf("%+v %v", got, err)
	}
	if err := r.Create(&occasion.Occasion{Name: "christmas", Month: 1, Day: 1}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("names are unique ignoring case: %v", err)
	}
	o.Name, o.Holiday, o.Month, o.Day, o.Keywords = "Easter", "easter", 0, 0, nil
	must(t, r.Update(o))
	if got, err := r.Get(o.ID); err != nil || got.Holiday != "easter" || got.Month != 0 || len(got.Keywords) != 0 {
		t.Fatalf("keywords are replaced: %+v %v", got, err)
	}
	if page, _ := r.List(1, 1); len(page) != 1 || page[0].Name != "Easter" {
		t.Fatalf("paged by name: %+v", page)
	}
	must(t, r.Delete(o.ID))
	if _, err := r.Get(o.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Update(o); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

type world struct {
	items                 *item.Repo
	r                     *occasion.Repo
	g, rated, show, plain int64
	episode               int64
}

// newWorld makes a G movie and an R movie carrying the keyword 7, a TV-Y
// show carrying it, an episode of the show carrying it, and an unrated
// movie without it.
func newWorld(t *testing.T, source, mode string) world {
	d := dbtest.Open(t)
	dbtest.Sources(t, d, source, mode)
	libs, w := library.New(d), world{items: item.New(d), r: occasion.New(d)}
	mv := &library.Library{Name: "mv", Kind: library.Movies}
	tv := &library.Library{Name: "tv", Kind: library.TV}
	must(t, libs.Create(mv))
	must(t, libs.Create(tv))
	add := func(lib int64, parent *int64, kind, rating string, keywords ...int64) int64 {
		it := &item.Item{LibraryID: lib, ParentID: parent, Kind: kind, ParsedTitle: kind + rating}
		must(t, w.items.Create(it))
		must(t, w.items.SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, ContentRating: rating}))
		must(t, w.items.SetKeywords(it.ID, keywords))
		return it.ID
	}
	w.g, w.rated, w.plain = add(mv.ID, nil, item.Movie, "G", 7, 8), add(mv.ID, nil, item.Movie, "R", 7), add(mv.ID, nil, item.Movie, "")
	w.show = add(tv.ID, nil, item.Show, "TV-Y", 7)
	season := add(tv.ID, &w.show, item.Season, "")
	w.episode = add(tv.ID, &season, item.Episode, "", 7)
	return w
}

func (w world) members(t *testing.T, occasionID int64, a item.Audience) []int64 {
	t.Helper()
	rows, err := w.items.List(item.Filter{OccasionID: occasionID, Audience: a})
	must(t, err)
	var out []int64
	for _, it := range rows {
		out = append(out, it.ID)
	}
	slices.Sort(out)
	return out
}

func TestMembership(t *testing.T) {
	w := newWorld(t, "remote", "only")
	o := &occasion.Occasion{Name: "Sevens", Month: 7, Day: 7, Keywords: []int64{7}}
	must(t, w.r.Create(o))
	if got := w.members(t, o.ID, item.Audience{}); !reflect.DeepEqual(got, []int64{w.g, w.rated, w.show}) {
		t.Fatalf("movies and shows carrying a keyword, never episodes: %v", got)
	}
	must(t, w.r.AddMember(o.ID, w.plain))
	must(t, w.r.RemoveMember(o.ID, w.rated))
	if got := w.members(t, o.ID, item.Audience{}); !reflect.DeepEqual(got, []int64{w.g, w.plain, w.show}) {
		t.Fatalf("added by hand, removed by hand: %v", got)
	}
	via, err := w.r.Keyworded(o.ID, []int64{w.g, w.show, w.plain})
	must(t, err)
	if !reflect.DeepEqual(via, map[int64]bool{w.g: true, w.show: true}) {
		t.Fatalf("which members a keyword brings: %v", via)
	}
	must(t, w.items.SetKeywords(w.rated, []int64{7, 9}))
	if got := w.members(t, o.ID, item.Audience{}); slices.Contains(got, w.rated) {
		t.Fatalf("a removal outlives a metadata refresh: %v", got)
	}
	must(t, w.r.AddMember(o.ID, w.rated))
	if got := w.members(t, o.ID, item.Audience{}); !slices.Contains(got, w.rated) {
		t.Fatalf("adding a removed item back clears its removal: %v", got)
	}
	must(t, w.r.RemoveMember(o.ID, w.plain))
	if got := w.members(t, o.ID, item.Audience{}); slices.Contains(got, w.plain) {
		t.Fatalf("a hand addition is removed: %v", got)
	}

	kid := item.Audience{Kid: true, Movie: []string{"G", "PG"}, TV: []string{"TV-Y"}}
	if got := w.members(t, o.ID, kid); !reflect.DeepEqual(got, []int64{w.g, w.show}) {
		t.Fatalf("a kid's ceilings apply: %v", got)
	}
	if got := w.members(t, o.ID, item.Audience{HideKids: true}); !reflect.DeepEqual(got, []int64{w.rated}) {
		t.Fatalf("an adult hiding kids' content: %v", got)
	}
	if got := w.members(t, 0, item.Audience{}); len(got) != 6 {
		t.Fatalf("no occasion filters nothing: %v", got)
	}
	must(t, w.items.Delete(w.g))
	if got := w.members(t, o.ID, item.Audience{}); slices.Contains(got, w.g) {
		t.Fatalf("%v", got)
	}
	must(t, w.r.Delete(o.ID))
}

func TestLocalOnlyMetadataHasNoKeywordMembers(t *testing.T) {
	w := newWorld(t, "local", "only")
	o := &occasion.Occasion{Name: "Sevens", Month: 7, Day: 7, Keywords: []int64{7}}
	must(t, w.r.Create(o))
	must(t, w.r.AddMember(o.ID, w.plain))
	if got := w.members(t, o.ID, item.Audience{}); !reflect.DeepEqual(got, []int64{w.plain}) {
		t.Fatalf("keywords are TMDB's, unused while metadata is local only: %v", got)
	}
}
