package item_test

import (
	"slices"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// The one rule: a single leading English article, followed by whitespace
// and more text, is left out; nothing else changes.
func TestSortTitleLeavesOutOneLeadingArticle(t *testing.T) {
	r, tv, mv := setup(t)
	for title, want := range map[string]string{
		"The Matrix": "Matrix", "A Quiet Place": "Quiet Place", "An Education": "Education", "the  lighthouse": "lighthouse",
		"THE\tWire": "Wire", "The The": "The", "A": "A", "A.I.": "A.I.", "The": "The", "The ": "The ", "An": "An", "Therapy": "Therapy",
		"Anna": "Anna", "Ant-Man": "Ant-Man", "Die Hard": "Die Hard", "Das Boot": "Das Boot", "La La Land": "La La Land",
		"Le Mans": "Le Mans", "1917": "1917",
	} {
		for kind, lib := range map[string]uid.ID{item.Movie: mv, item.Show: tv} {
			it := mustCreate(t, r, &item.Item{LibraryID: lib, Kind: kind, ParsedTitle: title})
			got, err := r.Get(it.ID)
			must(t, err)
			if got.SortTitle != want || it.SortTitle != want {
				t.Errorf("%s %q sorts as %q (Create says %q), want %q", kind, title, got.SortTitle, it.SortTitle, want)
			}
		}
	}
}

// The sort title is that of the title shown: the metadata settings' pick
// of the local and remote rows, else the parsed title, through every write
// that changes which.
func TestSortTitleFollowsTheShownTitle(t *testing.T) {
	d := dbtest.OpenPreferLocal(t)
	lib := &library.Library{Name: "mv", Kind: library.Movies}
	if err := library.New(d).Create(lib, nil); err != nil {
		t.Fatal(err)
	}
	r := item.New(d)
	m := mustCreate(t, r, &item.Item{LibraryID: lib.ID, Kind: item.Movie, ParsedTitle: "The Matrix"})
	sorts := func(step, want string) {
		t.Helper()
		got, err := r.Get(m.ID)
		must(t, err)
		if got.SortTitle != want {
			t.Fatalf("%s: sorts as %q, want %q", step, got.SortTitle, want)
		}
	}
	sorts("parsed", "Matrix")
	must(t, r.SetMetadata(&item.Metadata{ItemID: m.ID, Status: item.Matched, Title: "A Quiet Place"}))
	sorts("matched", "Quiet Place")
	must(t, r.SetLocal(&item.Metadata{ItemID: m.ID, Title: "An Education"}))
	sorts("local first", "Education")
	dbtest.Sources(t, d, "remote", "preferred")
	sorts("remote first", "Quiet Place")
	must(t, r.SetMetadata(&item.Metadata{ItemID: m.ID, Status: item.Matched, Title: "The Thing"}))
	sorts("re-matched", "Thing")
	must(t, r.DeleteMetadata(m.ID))
	sorts("unmatched", "Education")
	must(t, r.DeleteLocal([]uid.ID{m.ID}))
	sorts("no metadata", "Matrix")
	m.ParsedTitle = "Die Hard"
	must(t, r.Update(m))
	if m.SortTitle != "Die Hard" {
		t.Errorf("Update leaves SortTitle %q", m.SortTitle)
	}
	sorts("renamed", "Die Hard")
	must(t, r.SetMetadata(&item.Metadata{ItemID: m.ID, Status: item.Matched, Title: "The Abyss"}))
	dbtest.Sources(t, d, "local", "only")
	sorts("remote off", "Die Hard")
}

// Music keeps the sort name the scanner gives it, and other kinds none.
func TestSortTitleLeavesMusicAndEpisodes(t *testing.T) {
	r, tv, _ := setup(t)
	show := mustCreate(t, r, &item.Item{LibraryID: tv, Kind: item.Show, ParsedTitle: "The Wire"})
	season := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: "The Season", SeasonNumber: intp(1)})
	ep := mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &season.ID, Kind: item.Episode, ParsedTitle: "A Pilot", EpisodeNumber: intp(1)})
	artist := mustCreate(t, r, &item.Item{LibraryID: tv, Kind: item.Artist, ParsedTitle: "A Tribe Called Quest", SortTitle: "A Tribe Called Quest"})
	for _, it := range []*item.Item{season, ep, artist} {
		must(t, r.SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Matched, Title: "The Other"}))
	}
	for id, want := range map[uid.ID]string{season.ID: "", ep.ID: "", artist.ID: "A Tribe Called Quest"} {
		got, err := r.Get(id)
		must(t, err)
		if got.SortTitle != want {
			t.Errorf("%s %q sorts as %q, want %q", got.Kind, got.ParsedTitle, got.SortTitle, want)
		}
	}
}

// Movies and shows list by sort title, ignoring case; a show's seasons and
// episodes keep their order.
func TestListOrdersBySortTitle(t *testing.T) {
	r, tv, mv := setup(t)
	for _, title := range []string{"A Quiet Place", "the matrix", "Die Hard", "Zodiac", "An Education", "Mad Max"} {
		mustCreate(t, r, &item.Item{LibraryID: mv, Kind: item.Movie, ParsedTitle: title})
	}
	ms, err := r.List(item.Filter{Kind: item.Movie})
	must(t, err)
	want := []string{"Die Hard", "An Education", "Mad Max", "the matrix", "A Quiet Place", "Zodiac"}
	if got := parsed(ms); !slices.Equal(got, want) {
		t.Errorf("movies %q, want %q", got, want)
	}
	show := mustCreate(t, r, &item.Item{LibraryID: tv, Kind: item.Show, ParsedTitle: "Show"})
	for n, title := range []string{"The Alpha", "A Zulu"} {
		mustCreate(t, r, &item.Item{LibraryID: tv, ParentID: &show.ID, Kind: item.Season, ParsedTitle: title, SeasonNumber: intp(n)})
	}
	ss, err := r.List(item.Filter{ParentID: &show.ID})
	must(t, err)
	if got := parsed(ss); !slices.Equal(got, []string{"A Zulu", "The Alpha"}) {
		t.Errorf("seasons %q", got)
	}
}

func parsed(its []*item.Item) []string {
	out := make([]string, len(its))
	for i, it := range its {
		out[i] = it.ParsedTitle
	}
	return out
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
