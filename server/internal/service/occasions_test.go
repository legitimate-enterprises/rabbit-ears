package service_test

import (
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func TestActiveOccasions(t *testing.T) {
	s := service.New(dbtest.OpenPreferLocal(t))
	l := &library.Library{Name: "Films", Kind: library.Movies}
	must(t, s.Libraries.Repo().Create(l, nil))
	movie := func(rating string, keywords ...int64) uid.ID {
		it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: rating}
		must(t, s.Items.Repo().Create(it))
		must(t, s.Items.Repo().SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, ContentRating: rating}))
		must(t, s.Items.Repo().SetKeywords(it.ID, keywords))
		return it.ID
	}
	movie("R", 207317) // the seeded Christmas keyword
	g := movie("G")
	repo := s.Occasions.Repo()
	for _, o := range []*occasion.Occasion{
		{Name: "Turkey", Holiday: "thanksgiving", DaysBefore: 3, DaysAfter: 3},
		{Name: "Feast", Holiday: "thanksgiving", DaysAfter: 1},
		{Name: "Empty", Holiday: "thanksgiving", DaysAfter: 1},
	} {
		must(t, repo.Create(o, nil))
		if o.Name != "Empty" {
			must(t, repo.AddMember(o.ID, g))
		}
	}
	names := func(day time.Time, a item.Audience) []string {
		t.Helper()
		got, err := s.Occasions.Active(day, a, nil)
		must(t, err)
		out := []string{}
		for _, v := range got {
			out = append(out, v.Name)
		}
		return out
	}
	thanksgiving := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	if got := names(thanksgiving.AddDate(0, 0, 1), item.Audience{}); len(got) != 3 || got[0] != "Feast" || got[1] != "Turkey" || got[2] != "Christmas" {
		t.Fatalf("nearest first, ties by name, none without a member: %v", got)
	}
	got, err := s.Occasions.Active(thanksgiving, item.Audience{}, nil)
	must(t, err)
	if c := got[len(got)-1]; c.Name != "Christmas" || c.Distance != 29 || !c.Anchor.Equal(time.Date(2026, 12, 25, 0, 0, 0, 0, time.UTC)) ||
		!c.First.Equal(time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)) || !c.Last.Equal(time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", c)
	}
	kid := item.Audience{Kid: true, Movie: []string{"G"}}
	if got := names(thanksgiving, kid); len(got) != 2 || got[0] != "Feast" || got[1] != "Turkey" {
		t.Fatalf("a kid sees an occasion only with a member they see: %v", got)
	}
	if got := names(thanksgiving.AddDate(0, 0, -6), item.Audience{}); len(got) != 0 {
		t.Fatalf("out of season: %v", got)
	}
}

func TestActiveOccasionHeadlineAndItem(t *testing.T) {
	s := service.New(dbtest.OpenPreferLocal(t))
	l := &library.Library{Name: "Films", Kind: library.Movies}
	must(t, s.Libraries.Repo().Create(l, nil))
	movie := func(rating string) uid.ID {
		it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: rating}
		must(t, s.Items.Repo().Create(it))
		must(t, s.Items.Repo().SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, ContentRating: rating}))
		return it.ID
	}
	g, r, outside := movie("G"), movie("R"), movie("PG")
	repo := s.Occasions.Repo()
	feast := &occasion.Occasion{Name: "Feast", Holiday: "thanksgiving", DaysAfter: 1, Phrases: []string{"Gobble", "Stuffed", "Pie"}}
	plain := &occasion.Occasion{Name: "Plain", Holiday: "thanksgiving", DaysAfter: 1}
	for _, o := range []*occasion.Occasion{feast, plain} {
		must(t, repo.Create(o, nil))
		must(t, repo.AddMember(o.ID, g))
	}
	must(t, repo.AddMember(feast.ID, r))
	var asked []int
	s.Occasions.Pick = func(n int) int { asked = append(asked, n); return 1 }
	day := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	got, err := s.Occasions.Active(day, item.Audience{}, nil)
	must(t, err)
	if len(got) != 2 || got[0].Headline != "Stuffed" || got[1].Headline != "" || !slices.Equal(asked, []int{3}) {
		t.Fatalf("one phrase picked per occasion with phrases, none without: %+v %v", got, asked)
	}
	withItem := func(itemID uid.ID, a item.Audience) []string {
		t.Helper()
		got, err := s.Occasions.Active(day, a, &itemID)
		must(t, err)
		out := []string{}
		for _, v := range got {
			out = append(out, v.Name)
		}
		return out
	}
	if got := withItem(r, item.Audience{}); !slices.Equal(got, []string{"Feast"}) {
		t.Fatalf("only occasions the item is a member of: %v", got)
	}
	if got := withItem(outside, item.Audience{}); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	if got := withItem(r, item.Audience{Kid: true, Movie: []string{"G"}}); len(got) != 0 {
		t.Fatalf("only a member the profile can list: %v", got)
	}
}
