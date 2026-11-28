package occasion_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCRUDAndSeeds(t *testing.T) {
	d := dbtest.Open(t)
	r := occasion.New(d)
	seeded, err := r.List(100, 0)
	must(t, err)
	bolder := &occasion.Wash{Strength: 200, Brightness: 15, Sharpness: 0, Reach: 100}
	want := []occasion.Occasion{{Name: "Christmas", Holiday: "christmas", DaysBefore: 30, DaysAfter: 5, Keywords: []int64{207317}, Wash: bolder},
		{Name: "Halloween", Holiday: "halloween", DaysBefore: 30, DaysAfter: 1, Keywords: []int64{3335}, Wash: bolder}}
	christmas, halloween := uid.ID{6: 0x70, 8: 0x90, 15: 1}, uid.ID{6: 0x70, 8: 0x90, 15: 2}
	if len(seeded) != 2 || seeded[0].ID != christmas || seeded[1].ID != halloween {
		t.Fatalf("%+v", seeded)
	}
	for i, o := range seeded {
		if !o.CreatedAt.Equal(db.FromMs(0)) || !o.ModifiedAt.Equal(db.FromMs(0)) {
			t.Errorf("seeded %+v at %v %v", *o, o.CreatedAt, o.ModifiedAt)
		}
		o.ID, o.CreatedAt, o.ModifiedAt = uid.ID{}, want[i].CreatedAt, want[i].ModifiedAt
		if !reflect.DeepEqual(*o, want[i]) {
			t.Errorf("seeded %+v, want %+v", *o, want[i])
		}
	}

	o := &occasion.Occasion{Name: "Groundhog Day", Month: 2, Day: 2, DaysBefore: 1, Keywords: []int64{9, 3}}
	by, editor := uid.N(7), uid.N(8)
	_, err = d.Exec(`INSERT INTO account (id, kind, name, created_at, modified_at) VALUES (?, 'adult', 'A', 0, 0), (?, 'adult', 'B', 0, 0)`, by, editor)
	must(t, err)
	must(t, r.Create(o, &by))
	if got, err := r.Get(o.ID); err != nil || o.ID.IsZero() || !reflect.DeepEqual(got, &occasion.Occasion{ID: o.ID, Name: "Groundhog Day", Month: 2, Day: 2,
		DaysBefore: 1, Keywords: []int64{3, 9}, Wash: bolder, CreatedAt: o.CreatedAt, ModifiedAt: o.CreatedAt, CreatedBy: &by, ModifiedBy: &by}) {
		t.Fatalf("%+v %v", got, err)
	}
	if err := r.Create(&occasion.Occasion{Name: "christmas", Month: 1, Day: 1}, nil); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("names are unique ignoring case: %v", err)
	}
	if err := r.Create(&occasion.Occasion{ID: o.ID, Name: "Other", Month: 1, Day: 1}, nil); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("a taken id: %v", err)
	}
	o.Name, o.Holiday, o.Month, o.Day, o.Keywords = "Easter", "easter", 0, 0, nil
	must(t, r.Update(o, &editor))
	if got, err := r.Get(o.ID); err != nil || got.Holiday != "easter" || got.Month != 0 || len(got.Keywords) != 0 ||
		*got.CreatedBy != by || *got.ModifiedBy != editor || got.ModifiedAt.Before(got.CreatedAt) {
		t.Fatalf("keywords are replaced: %+v %v", got, err)
	}
	if page, _ := r.List(1, 1); len(page) != 1 || page[0].Name != "Easter" {
		t.Fatalf("paged by name: %+v", page)
	}
	must(t, r.Delete(o.ID))
	if _, err := r.Get(o.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Update(o, nil); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestWash(t *testing.T) {
	r := occasion.New(dbtest.Open(t))
	o := &occasion.Occasion{Name: "Snow", Month: 1, Day: 2}
	must(t, r.Create(o, nil))
	if bolder := (occasion.Wash{Strength: 200, Brightness: 15, Sharpness: 0, Reach: 100}); o.Wash == nil || *o.Wash != bolder {
		t.Fatalf("a create without a wash takes the defaults: %+v", o.Wash)
	}
	mine := occasion.Wash{Strength: 400, Brightness: 60, Sharpness: 70, Reach: 60}
	o.Wash = &mine
	must(t, r.Update(o, nil))
	o.Wash = nil
	must(t, r.Update(o, nil))
	if got, err := r.Get(o.ID); err != nil || *got.Wash != mine || *o.Wash != mine {
		t.Fatalf("an update without a wash keeps the stored one: %+v %+v %v", o.Wash, got, err)
	}
	other := &occasion.Occasion{Name: "Sun", Month: 7, Day: 1, Wash: &occasion.Wash{Strength: 100, Brightness: 0, Sharpness: 0, Reach: 160}}
	must(t, r.Create(other, nil))
	if got, err := r.Get(other.ID); err != nil || got.Wash.Strength != 100 || got.Wash.Reach != 160 {
		t.Fatalf("a create with a wash stores it: %+v %v", got, err)
	}
}

type world struct {
	items                 *item.Repo
	r                     *occasion.Repo
	g, rated, show, plain uid.ID
	episode               uid.ID
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
	must(t, libs.Create(mv, nil))
	must(t, libs.Create(tv, nil))
	add := func(lib uid.ID, parent *uid.ID, kind, rating string, keywords ...int64) uid.ID {
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

func (w world) members(t *testing.T, occasionID *uid.ID, a item.Audience) []uid.ID {
	t.Helper()
	rows, err := w.items.List(item.Filter{OccasionID: occasionID, Audience: a})
	must(t, err)
	var out []uid.ID
	for _, it := range rows {
		out = append(out, it.ID)
	}
	slices.SortFunc(out, uid.ID.Compare)
	return out
}

func TestMembership(t *testing.T) {
	w := newWorld(t, "remote", "only")
	o := &occasion.Occasion{Name: "Sevens", Month: 7, Day: 7, Keywords: []int64{7}}
	must(t, w.r.Create(o, nil))
	if got := w.members(t, &o.ID, item.Audience{}); !reflect.DeepEqual(got, []uid.ID{w.g, w.rated, w.show}) {
		t.Fatalf("movies and shows carrying a keyword, never episodes: %v", got)
	}
	must(t, w.r.AddMember(o.ID, w.plain))
	must(t, w.r.RemoveMember(o.ID, w.rated))
	if got := w.members(t, &o.ID, item.Audience{}); !reflect.DeepEqual(got, []uid.ID{w.g, w.plain, w.show}) {
		t.Fatalf("added by hand, removed by hand: %v", got)
	}
	via, err := w.r.Keyworded(o.ID, []uid.ID{w.g, w.show, w.plain})
	must(t, err)
	if !reflect.DeepEqual(via, map[uid.ID]bool{w.g: true, w.show: true}) {
		t.Fatalf("which members a keyword brings: %v", via)
	}
	must(t, w.items.SetKeywords(w.rated, []int64{7, 9}))
	if got := w.members(t, &o.ID, item.Audience{}); slices.Contains(got, w.rated) {
		t.Fatalf("a removal outlives a metadata refresh: %v", got)
	}
	must(t, w.r.AddMember(o.ID, w.rated))
	if got := w.members(t, &o.ID, item.Audience{}); !slices.Contains(got, w.rated) {
		t.Fatalf("adding a removed item back clears its removal: %v", got)
	}
	must(t, w.r.RemoveMember(o.ID, w.plain))
	if got := w.members(t, &o.ID, item.Audience{}); slices.Contains(got, w.plain) {
		t.Fatalf("a hand addition is removed: %v", got)
	}

	kid := item.Audience{Kid: true, Movie: []string{"G", "PG"}, TV: []string{"TV-Y"}}
	if got := w.members(t, &o.ID, kid); !reflect.DeepEqual(got, []uid.ID{w.g, w.show}) {
		t.Fatalf("a kid's ceilings apply: %v", got)
	}
	if got := w.members(t, &o.ID, item.Audience{HideKids: true}); !reflect.DeepEqual(got, []uid.ID{w.rated}) {
		t.Fatalf("an adult hiding kids' content: %v", got)
	}
	if got := w.members(t, nil, item.Audience{}); len(got) != 6 {
		t.Fatalf("no occasion filters nothing: %v", got)
	}
	must(t, w.items.Delete(w.g))
	if got := w.members(t, &o.ID, item.Audience{}); slices.Contains(got, w.g) {
		t.Fatalf("%v", got)
	}
	must(t, w.r.Delete(o.ID))
}

func TestLocalOnlyMetadataHasNoKeywordMembers(t *testing.T) {
	w := newWorld(t, "local", "only")
	o := &occasion.Occasion{Name: "Sevens", Month: 7, Day: 7, Keywords: []int64{7}}
	must(t, w.r.Create(o, nil))
	must(t, w.r.AddMember(o.ID, w.plain))
	if got := w.members(t, &o.ID, item.Audience{}); !reflect.DeepEqual(got, []uid.ID{w.plain}) {
		t.Fatalf("keywords are TMDB's, unused while metadata is local only: %v", got)
	}
}

func TestDailyOrder(t *testing.T) {
	w := newWorld(t, "remote", "only")
	mv, err := w.items.Get(w.g)
	must(t, err)
	a, b := &occasion.Occasion{Name: "Sevens", Month: 7, Day: 7}, &occasion.Occasion{Name: "Eights", Month: 8, Day: 8}
	must(t, w.r.Create(a, nil))
	must(t, w.r.Create(b, nil))
	for i := range 20 {
		it := &item.Item{LibraryID: mv.LibraryID, Kind: item.Movie, ParsedTitle: string(rune('a' + i))}
		must(t, w.items.Create(it))
		must(t, w.r.AddMember(a.ID, it.ID))
		must(t, w.r.AddMember(b.ID, it.ID))
	}
	order := func(o uid.ID, day time.Time, limit, offset int) []uid.ID {
		t.Helper()
		rows, err := w.items.List(item.Filter{OccasionID: &o, Daily: day, Limit: limit, Offset: offset})
		must(t, err)
		var out []uid.ID
		for _, it := range rows {
			out = append(out, it.ID)
		}
		return out
	}
	day := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	today := order(a.ID, day, 0, 0)
	if len(today) != 20 || !reflect.DeepEqual(order(a.ID, day.Add(23*time.Hour), 0, 0), today) {
		t.Fatalf("the same UTC day gives the same order: %v", today)
	}
	if reflect.DeepEqual(order(a.ID, day.AddDate(0, 0, 1), 0, 0), today) {
		t.Fatal("another day gives another order")
	}
	if reflect.DeepEqual(order(b.ID, day, 0, 0), today) {
		t.Fatal("two occasions differ on the same day")
	}
	if got := append(order(a.ID, day, 5, 0), order(a.ID, day, 5, 5)...); !reflect.DeepEqual(got, today[:10]) {
		t.Fatalf("the order comes before the page, so a row's membership rotates: %v", got)
	}
	byTitle := order(a.ID, time.Time{}, 0, 0)
	for i := 1; i < len(byTitle); i++ {
		x, err := w.items.Get(byTitle[i-1])
		must(t, err)
		y, err := w.items.Get(byTitle[i])
		must(t, err)
		if x.ParsedTitle > y.ParsedTitle {
			t.Fatalf("the default is still by title: %v", byTitle)
		}
	}
	if _, err := w.items.List(item.Filter{Daily: day}); err == nil {
		t.Fatal("a daily order is an occasion's")
	}
}
