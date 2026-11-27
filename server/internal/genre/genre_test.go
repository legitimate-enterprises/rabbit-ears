package genre_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func id64(n int64) *int64 { return &n }

func names(gs []*genre.Genre) []string {
	out := []string{}
	for _, g := range gs {
		out = append(out, g.Name)
	}
	return out
}

func TestCRUDAndConstraints(t *testing.T) {
	r := genre.New(dbtest.OpenPreferLocal(t))
	g := &genre.Genre{Name: "Drama", TMDBID: id64(18)}
	if err := r.Create(g); err != nil || g.ID.IsZero() {
		t.Fatal(err)
	}
	if err := r.Create(&genre.Genre{Name: "drama"}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("names are unique ignoring case: %v", err)
	}
	if err := r.Create(&genre.Genre{Name: "Other", TMDBID: id64(18)}); !errors.Is(err, db.ErrConflict) {
		t.Fatalf("provider ids are unique: %v", err)
	}
	g.Name, g.TMDBID = "Dramas", nil
	if err := r.Update(g); err != nil {
		t.Fatal(err)
	}
	if got, err := r.Get(g.ID); err != nil || got.Name != "Dramas" || got.TMDBID != nil {
		t.Fatalf("%+v %v", got, err)
	}
	if err := r.Delete(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(g.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.Update(g); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestItemGenresAndAudience(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	libs, items, r := library.New(d), item.New(d), genre.New(d)
	mv := &library.Library{Name: "mv", Kind: library.Movies}
	tv := &library.Library{Name: "tv", Kind: library.TV}
	for _, l := range []*library.Library{mv, tv} {
		if err := libs.Create(l, nil); err != nil {
			t.Fatal(err)
		}
	}
	add := func(lib uid.ID, kind, rating string) uid.ID {
		it := &item.Item{LibraryID: lib, Kind: kind, ParsedTitle: rating}
		if err := items.Create(it); err != nil {
			t.Fatal(err)
		}
		if err := items.SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, ContentRating: rating}); err != nil {
			t.Fatal(err)
		}
		return it.ID
	}
	g, r18, show := add(mv.ID, item.Movie, "G"), add(mv.ID, item.Movie, "R"), add(tv.ID, item.Show, "TV-Y")
	if err := r.SetForItem(g, genre.Remote, []genre.Genre{{Name: "Family", TMDBID: id64(10751)}, {Name: "Animation", TMDBID: id64(16)}}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetForItem(r18, genre.Remote, []genre.Genre{{Name: "Crime", TMDBID: id64(80)}, {Name: "Animated", TMDBID: id64(16)}}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetForItem(show, genre.Remote, []genre.Genre{{Name: "Kids", TMDBID: id64(10762)}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&genre.Genre{Name: "Western"}); err != nil {
		t.Fatal(err)
	}
	r18Genres, _ := r.ForItem(r18)
	if len(r18Genres) != 2 || r18Genres[0].Name != "Animation" || r18Genres[1].Name != "Crime" {
		t.Fatalf("a stored name outlives the provider's: %v", names(r18Genres))
	}
	byItem, err := r.ForItems([]uid.ID{g, r18, show})
	if err != nil || len(byItem) != 3 || len(byItem[g]) != 2 || byItem[g][0].Name != "Animation" || byItem[show][0].Name != "Kids" {
		t.Fatalf("batched by item, each by name: %v %v", byItem, err)
	}
	if none, err := r.ForItems(nil); err != nil || len(none) != 0 {
		t.Fatalf("empty batch: %v %v", none, err)
	}
	if err := r.SetForItem(g, genre.Remote, []genre.Genre{{Name: "Family", TMDBID: id64(10751)}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ForItem(g); len(got) != 1 {
		t.Fatalf("links are replaced: %v", names(got))
	}

	if all, _ := r.List(genre.Filter{}); len(all) != 5 || all[0].Name != "Animation" || all[4].Name != "Western" {
		t.Fatalf("adults see every genre by name: %v", names(all))
	}
	if inTV, _ := r.List(genre.Filter{LibraryID: &tv.ID}); len(inTV) != 1 || inTV[0].Name != "Kids" {
		t.Fatalf("library filter: %v", names(inTV))
	}
	kid := item.Audience{Kid: true, Movie: []string{"G", "PG"}, TV: []string{"TV-Y"}}
	got, _ := r.List(genre.Filter{Audience: kid})
	if len(got) != 2 || got[0].Name != "Family" || got[1].Name != "Kids" {
		t.Fatalf("a kid sees genres of visible items only: %v", names(got))
	}
	if page, _ := r.List(genre.Filter{Audience: kid, Limit: 1, Offset: 1}); len(page) != 1 || page[0].Name != "Kids" {
		t.Fatalf("paging: %v", names(page))
	}
	crime := r18Genres[1].ID
	if ok, err := r.Seen(crime, kid); err != nil || ok {
		t.Fatalf("crime hidden from the kid: %v %v", ok, err)
	}
	if ok, _ := r.Seen(crime, item.Audience{}); !ok {
		t.Fatal("adult sees crime")
	}
	if ok, _ := r.Seen(uid.N(999), item.Audience{}); ok {
		t.Fatal("missing genre seen")
	}
	if err := items.Delete(r18); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ForItem(r18); len(got) != 0 {
		t.Fatal("links cascade with the item")
	}
}

func TestAdminRenameSurvivesRefresh(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	libs, items, r := library.New(d), item.New(d), genre.New(d)
	mv := &library.Library{Name: "mv", Kind: library.Movies}
	if err := libs.Create(mv, nil); err != nil {
		t.Fatal(err)
	}
	it := &item.Item{LibraryID: mv.ID, Kind: item.Movie, ParsedTitle: "Alien"}
	if err := items.Create(it); err != nil {
		t.Fatal(err)
	}
	sf := []genre.Genre{{Name: "Science Fiction", TMDBID: id64(878)}}
	if err := r.SetForItem(it.ID, genre.Remote, sf); err != nil {
		t.Fatal(err)
	}
	got, _ := r.ForItem(it.ID)
	got[0].Name = "Sci-Fi"
	if err := r.Update(got[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.Create(&genre.Genre{Name: "Science Fiction"}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetForItem(it.ID, genre.Remote, sf); err != nil {
		t.Fatalf("a refresh after a rename must not collide on name: %v", err)
	}
	if got, _ := r.ForItem(it.ID); len(got) != 1 || got[0].Name != "Sci-Fi" {
		t.Fatalf("the admin's name is kept: %v", names(got))
	}
}
