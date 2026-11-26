package tmdb

import (
	"context"
	"slices"
	"testing"
)

var offices = []Result{
	{ID: 2316, Title: "The Office", ReleaseDate: "2005-03-24", OriginCountry: []string{"US"}},
	{ID: 2996, Title: "The Office", ReleaseDate: "2001-07-09", OriginCountry: []string{"GB"}},
}

func ids(rs []Result) []int64 {
	var out []int64
	for _, r := range rs {
		out = append(out, r.ID)
	}
	slices.Sort(out)
	return out
}

// Two shows of one title are told apart by premiere year or country, in
// whichever order the provider lists them; with neither, or with both
// pointing at different shows, there is no match and both are candidates.
func TestMatchTellsSameTitlesApart(t *testing.T) {
	reversed := []Result{offices[1], offices[0]}
	cases := []struct {
		name string
		q    Query
		want int64
		tied []int64
	}{
		{"premiere year", Query{Title: "The Office", Year: intp(2001), TV: true}, 2996, nil},
		{"country", Query{Title: "The Office", Country: "US", TV: true}, 2316, nil},
		{"year and country agree", Query{Title: "The Office", Year: intp(2001), Country: "GB", TV: true}, 2996, nil},
		{"year and country disagree", Query{Title: "The Office", Year: intp(2001), Country: "US", TV: true}, 0, nil},
		{"neither", Query{Title: "The Office", TV: true}, 0, []int64{2316, 2996}},
		{"a show's premiere year is exact", Query{Title: "The Office", Year: intp(2002), TV: true}, 0, nil},
	}
	for _, c := range cases {
		for _, rs := range [][]Result{offices, reversed} {
			hit, tied := Match(rs, c.q)
			if got := ids(tied); c.want == 0 && hit != nil || c.want != 0 && (hit == nil || hit.ID != c.want) || !slices.Equal(got, c.tied) {
				t.Errorf("%s, %d listed first: hit %+v tied %v", c.name, rs[0].ID, hit, got)
			}
		}
	}
}

// Accents and "&" in a provider's title do not keep a file's title from
// matching it, but a title that fits exactly beats one that fits only
// folded, and equal folded fits are tied.
func TestMatchFoldsAccentsAndAmpersands(t *testing.T) {
	for _, c := range [][2]string{
		{"Amelie", "Amélie"},
		{"Leon The Professional", "Léon: The Professional"},
		{"Fast and Furious", "Fast & Furious"},
		{"Pokemon Detective Pikachu", "Pokémon Detective Pikachu"},
	} {
		hit, _ := Match([]Result{{ID: 1, Title: c[1], ReleaseDate: "2001-01-01"}}, Query{Title: c[0]})
		if hit == nil {
			t.Errorf("%q does not match %q", c[0], c[1])
		}
	}
	rs := []Result{{ID: 1, Title: "Léon", ReleaseDate: "1994-09-14"}, {ID: 2, Title: "Leon", ReleaseDate: "1994-01-01"}}
	if hit, _ := Match(rs, Query{Title: "Leon", Year: intp(1994)}); hit == nil || hit.ID != 2 {
		t.Errorf("exact title beside a folded one: %+v", hit)
	}
	rs = []Result{{ID: 1, Title: "Léon", ReleaseDate: "1994-09-14"}, {ID: 2, Title: "Leön", ReleaseDate: "1994-01-01"}}
	if hit, tied := Match(rs, Query{Title: "Leon", Year: intp(1994)}); hit != nil || !slices.Equal(ids(tied), []int64{1, 2}) {
		t.Errorf("equal folded fits: %+v %v", hit, ids(tied))
	}
}

// A movie's release year may be one off its name's, the nearer year
// winning; two movies of one title and year are a tie.
func TestMatchMovieYear(t *testing.T) {
	rs := []Result{{ID: 1, Title: "Crash", ReleaseDate: "2005-05-06"}, {ID: 2, Title: "Crash", ReleaseDate: "2004-09-10"},
		{ID: 3, Title: "Crash", ReleaseDate: "1996-07-17"}, {ID: 4, Title: "Crash", ReleaseDate: "2005-11-01"}}
	if hit, _ := Match(rs, Query{Title: "Crash", Year: intp(1997)}); hit == nil || hit.ID != 3 {
		t.Errorf("one year off: %+v", hit)
	}
	if hit, _ := Match(rs, Query{Title: "Crash", Year: intp(2004)}); hit == nil || hit.ID != 2 {
		t.Errorf("the nearer year: %+v", hit)
	}
	if hit, tied := Match(rs, Query{Title: "Crash", Year: intp(2005)}); hit != nil || !slices.Equal(ids(tied), []int64{1, 4}) {
		t.Errorf("same title and year: %+v %v", hit, ids(tied))
	}
	if hit, tied := Match(rs, Query{Title: "Crash", Year: intp(1999)}); hit != nil || tied != nil {
		t.Errorf("no such year: %+v %v", hit, tied)
	}
}

// A TVDB or IMDb id is resolved through TMDB's external id lookup, movies
// and shows apart; an id TMDB does not know finds nothing.
func TestFind(t *testing.T) {
	c, _, _ := replay(t, map[string]string{"/find/78107": "find_78107.json", "/find/1": "find_none.json"})
	movies, shows, err := c.Find(context.Background(), TVDB, "78107")
	if err != nil || len(movies) != 0 || len(shows) != 1 || shows[0].ID != 2996 || !slices.Equal(shows[0].OriginCountry, []string{"GB"}) {
		t.Fatalf("%+v %+v %v", movies, shows, err)
	}
	if movies, shows, err := c.Find(context.Background(), TVDB, "1"); err != nil || len(movies)+len(shows) != 0 {
		t.Fatalf("unknown id: %+v %+v %v", movies, shows, err)
	}
	res, err := replaySearch(t).SearchTV(context.Background(), "Breaking Bad", nil)
	if err != nil || len(res) != 1 || !slices.Equal(res[0].OriginCountry, []string{"US"}) {
		t.Fatalf("a search result keeps its countries: %+v %v", res, err)
	}
}

func replaySearch(t *testing.T) *Client {
	c, _, _ := replay(t, map[string]string{"/search/tv": "search_tv.json"})
	return c
}
