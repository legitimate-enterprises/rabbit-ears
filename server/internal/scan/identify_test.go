package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/musicbrainz"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
)

// fakeTMDB is a TMDB of a few titles. Every search answers all of them in
// the order given, as TMDB answers a title several share; found maps an
// external id ("tvdb_id 78107") to the title it is. It records its calls.
type fakeTMDB struct {
	Provider
	results  []tmdb.Result
	found    map[string]tmdb.Result
	episodes map[int64]string // a show's first episode
	calls    []string
}

func (f *fakeTMDB) Enabled() bool { return true }

func (f *fakeTMDB) Find(_ context.Context, source, id string) ([]tmdb.Result, []tmdb.Result, error) {
	f.calls = append(f.calls, "find "+source+" "+id)
	if r, ok := f.found[source+" "+id]; ok {
		return []tmdb.Result{r}, []tmdb.Result{r}, nil
	}
	return nil, nil, nil
}

func (f *fakeTMDB) search(title string) ([]tmdb.Result, error) {
	f.calls = append(f.calls, "search "+title)
	return f.results, nil
}

func (f *fakeTMDB) SearchMovie(_ context.Context, title string, _ *int) ([]tmdb.Result, error) {
	return f.search(title)
}

func (f *fakeTMDB) SearchTV(_ context.Context, title string, _ *int) ([]tmdb.Result, error) {
	return f.search(title)
}

func (f *fakeTMDB) details(id int64) (*tmdb.Details, error) {
	f.calls = append(f.calls, fmt.Sprint("details ", id))
	for _, r := range f.results {
		if r.ID == id {
			return &tmdb.Details{ID: id, Title: r.Title, ReleaseDate: r.ReleaseDate}, nil
		}
	}
	return nil, tmdb.ErrNotFound
}

func (f *fakeTMDB) Movie(_ context.Context, id int64) (*tmdb.Details, error) { return f.details(id) }
func (f *fakeTMDB) TV(_ context.Context, id int64) (*tmdb.Details, error)    { return f.details(id) }

func (f *fakeTMDB) Season(_ context.Context, id int64, _ int) ([]tmdb.Episode, error) {
	return []tmdb.Episode{{Number: 1, Title: f.episodes[id]}}, nil
}

func (f *fakeTMDB) CacheImage(context.Context, string, string, string) (string, error) {
	return "", nil
}

// called counts the calls that start with prefix.
func (f *fakeTMDB) called(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

var (
	officeUS = tmdb.Result{ID: 2316, Title: "The Office", ReleaseDate: "2005-03-24", OriginCountry: []string{"US"}}
	officeUK = tmdb.Result{ID: 2996, Title: "The Office", ReleaseDate: "2001-07-09", OriginCountry: []string{"GB"}}
)

// offices is a TMDB of the two shows called The Office, in this order.
func offices(order ...tmdb.Result) *fakeTMDB {
	return &fakeTMDB{results: order, episodes: map[int64]string{2316: "Pilot", 2996: "Downsize"},
		found: map[string]tmdb.Result{"tvdb_id 78107": officeUK, "tvdb_id 73244": officeUS}}
}

// bothOrders runs a test against TMDB listing the two shows either way
// round: what is decided may not depend on which comes first.
func bothOrders(t *testing.T, run func(t *testing.T, e *env, f *fakeTMDB)) {
	for name, order := range map[string][]tmdb.Result{"US first": {officeUS, officeUK}, "UK first": {officeUK, officeUS}} {
		t.Run(name, func(t *testing.T) {
			f := offices(order...)
			e := newEnv(t, library.TV, "")
			e.s.Provider = f
			run(t, e, f)
		})
	}
}

// episode puts an episode file at the path under the library.
func (e *env) episode(path ...string) string {
	e.t.Helper()
	p := filepath.Join(append([]string{e.root}, path...)...)
	fixtureCopy(e.t, "h264-high41.mp4", p)
	return p
}

// matched is the TMDB id the item is matched to, 0 when unmatched.
func (e *env) matched(it *item.Item) int64 {
	e.t.Helper()
	m, err := e.items.Remote(it.ID)
	if err != nil || m.Status != item.Matched || m.ProviderID == nil {
		return 0
	}
	return *m.ProviderID
}

func (e *env) showAt(folder string) *item.Item {
	e.t.Helper()
	sh, err := e.items.ByPath(e.lib.ID, item.Show, filepath.Join(e.root, folder))
	must(e.t, err)
	return sh
}

// firstEpisode is what the provider calls the show's only episode.
func (e *env) firstEpisode(show *item.Item) string {
	e.t.Helper()
	var title string
	must(e.t, e.d.QueryRow(`SELECT COALESCE((SELECT m.title FROM item s JOIN item ep ON ep.parent_id = s.id
		JOIN item_metadata m ON m.item_id = ep.id AND m.source = 'remote' WHERE s.parent_id = ?), '')`, show.ID).Scan(&title))
	return title
}

// said renders a review's evidence as "role provider id" entries.
func said(rv *review.Review) []string {
	var out []string
	for _, ev := range rv.Evidence {
		out = append(out, strings.TrimSpace(ev.Role+" "+ev.Provider+" "+ev.ExternalID))
	}
	return out
}

// Two shows of one title in folders that name their countries are each
// matched to the show of that country, whichever TMDB lists first.
func TestShowsOfOneTitleAreToldApartByCountry(t *testing.T) {
	bothOrders(t, func(t *testing.T, e *env, _ *fakeTMDB) {
		e.episode("The Office (UK)", "The Office - S01E01.mp4")
		e.episode("The Office (US)", "Season 01", "The Office - S01E01.mp4")
		e.mustScan()
		uk, us := e.showAt("The Office (UK)"), e.showAt("The Office (US)")
		if e.matched(uk) != 2996 || e.matched(us) != 2316 || uk.ParsedTitle != "The Office" || len(e.reviews("")) != 0 {
			t.Fatalf("UK %d, US %d, title %q, reviews %+v", e.matched(uk), e.matched(us), uk.ParsedTitle, e.reviews(""))
		}
		if e.firstEpisode(uk) != "Downsize" || e.firstEpisode(us) != "Pilot" {
			t.Fatalf("episodes %q %q", e.firstEpisode(uk), e.firstEpisode(us))
		}
	})
}

// A show whose folder names no year takes the premiere year its episode
// files name before their episode marker, never an air date after it.
// Files that name different years decide nothing: the match stays and the
// show is left for review.
func TestShowTakesItsYearFromItsEpisodeFiles(t *testing.T) {
	bothOrders(t, func(t *testing.T, e *env, _ *fakeTMDB) {
		e.episode("The Office", "Season 01", "The Office (2001) - S01E01 - 2005-03-24 - Downsize.mp4")
		e.mustScan()
		show := e.showAt("The Office")
		if e.matched(show) != 2996 || len(e.reviews("")) != 0 {
			t.Fatalf("matched %d, reviews %+v", e.matched(show), e.reviews(""))
		}
		e.episode("The Office", "Season 01", "The Office (2005) - S01E02.mp4")
		e.mustScan()
		rs := e.reviews(review.ConflictingYears)
		if e.matched(show) != 2996 || len(rs) != 1 || *rs[0].ItemID != show.ID || len(rs[0].Evidence) != 2 || *rs[0].Evidence[0].Year != 2001 || *rs[0].Evidence[1].Year != 2005 {
			t.Fatalf("matched %d, reviews %+v", e.matched(show), rs)
		}
	})
	t.Run("a new show", func(t *testing.T) {
		f := offices(officeUS, officeUK)
		e := newEnv(t, library.TV, "")
		e.s.Provider = f
		e.episode("The Office", "The Office (2001) - S01E01.mp4")
		e.episode("The Office", "The Office (2005) - S01E02.mp4")
		e.mustScan()
		if show := e.showAt("The Office"); e.matched(show) != 0 || len(e.reviews(review.ConflictingYears)) != 1 || f.called("search") != 0 {
			t.Fatalf("matched %d, reviews %+v, calls %v", e.matched(show), e.reviews(""), f.calls)
		}
	})
}

// Titles a name fits equally well are a tie whichever comes first: none
// is taken and the show is listed with both.
func TestTiedCandidatesAreLeftForReview(t *testing.T) {
	bothOrders(t, func(t *testing.T, e *env, _ *fakeTMDB) {
		e.episode("The Office", "The Office - S01E01.mp4")
		e.mustScan()
		show := e.showAt("The Office")
		rs := e.reviews(review.TiedCandidates)
		if e.matched(show) != 0 || len(rs) != 1 || len(e.reviews("")) != 1 {
			t.Fatalf("matched %d, reviews %+v", e.matched(show), e.reviews(""))
		}
		got := said(rs[0])
		slices.Sort(got)
		if !slices.Equal(got, []string{"candidate tmdb 2316", "candidate tmdb 2996"}) {
			t.Fatalf("evidence %v", got)
		}
		e.mustScan()
		if again := e.reviews(""); len(again) != 1 || again[0].ID != rs[0].ID {
			t.Fatalf("a second scan: %+v", again)
		}
	})
}

// An id a folder states decides the title without a search: a TVDB id
// through TMDB's lookup, a TMDB id outright. Neither needs season folders.
func TestStatedIdsDecideShows(t *testing.T) {
	bothOrders(t, func(t *testing.T, e *env, f *fakeTMDB) {
		e.episode("The Office (2001) {tvdb-78107}", "The Office (2001) - S01E01 - Downsize [WEBDL-1080p]-GRP.mp4")
		e.episode("The Office US (2005) {tmdb-2316}", "The Office US (2005) - S01E01 - Pilot.mp4")
		e.mustScan()
		uk, us := e.showAt("The Office (2001) {tvdb-78107}"), e.showAt("The Office US (2005) {tmdb-2316}")
		if e.matched(uk) != 2996 || e.matched(us) != 2316 || f.called("search") != 0 || len(e.reviews("")) != 0 {
			t.Fatalf("UK %d, US %d, calls %v, reviews %+v", e.matched(uk), e.matched(us), f.calls, e.reviews(""))
		}
		if uk.ParsedTitle != "The Office" || e.firstEpisode(uk) != "Downsize" || e.firstEpisode(us) != "Pilot" {
			t.Fatalf("title %q, episodes %q %q", uk.ParsedTitle, e.firstEpisode(uk), e.firstEpisode(us))
		}
	})
}

// movies is a TMDB of movies that share titles and years.
func movies() *fakeTMDB {
	lantern := func(id int64) tmdb.Result { return tmdb.Result{ID: id, Title: "Lantern", ReleaseDate: "2001-05-05"} }
	return &fakeTMDB{results: []tmdb.Result{lantern(11), lantern(12), {ID: 13, Title: "Glow", ReleaseDate: "2005-01-01"}, {ID: 14, Title: "Ember", ReleaseDate: "2010-01-01"}},
		found: map[string]tmdb.Result{"imdb_id tt0000012": lantern(12)}}
}

// Movies of one title and year whose names state different ids are
// different movies, each matched to its own; an IMDb id is looked up.
func TestSameTitledMoviesWithDifferentIdsStayApart(t *testing.T) {
	f := movies()
	e := newEnv(t, library.Movies, "")
	e.s.Provider = f
	a, b := filepath.Join(e.root, "Lantern (2001) {tmdb-11}", "Lantern (2001).mp4"), filepath.Join(e.root, "Lantern (2001) {imdb-tt0000012}", "Lantern (2001).mkv")
	fixtureCopy(t, "h264-high41.mp4", a)
	fixtureCopy(t, "chapters.mkv", b)
	e.mustScan()
	ms := e.list(item.Movie)
	if len(ms) != 2 {
		t.Fatalf("movies %+v", ms)
	}
	fa, err := e.files.FileByPath(a)
	must(t, err)
	fb, err := e.files.FileByPath(b)
	must(t, err)
	if *fa.ItemID == *fb.ItemID || e.matched(&item.Item{ID: *fa.ItemID}) != 11 || e.matched(&item.Item{ID: *fb.ItemID}) != 12 || f.called("search") != 0 || len(e.reviews("")) != 0 {
		t.Fatalf("a in %s matched %d, b in %s matched %d, calls %v", fa.ItemID, e.matched(&item.Item{ID: *fa.ItemID}), fb.ItemID, e.matched(&item.Item{ID: *fb.ItemID}), f.calls)
	}
	e.mustScan()
	if len(e.list(item.Movie)) != 2 {
		t.Fatalf("a second scan: %+v", e.list(item.Movie))
	}
}

// A malformed id, and one TMDB does not have, identify nothing: the
// movie is not matched by its name instead, and is listed with what its
// folder states.
func TestInvalidIdsAreLeftForReview(t *testing.T) {
	f := movies()
	e := newEnv(t, library.Movies, "")
	e.s.Provider = f
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Glow (2005) {tmdb-abc}", "Glow.mp4"))
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "Ember (2010) {tmdb-999}", "Ember.mkv"))
	e.mustScan()
	ms := e.list(item.Movie)
	rs := e.reviews(review.InvalidIdentity)
	if len(ms) != 2 || e.matched(ms[0]) != 0 || e.matched(ms[1]) != 0 || len(rs) != 2 || len(e.reviews("")) != 2 || f.called("search") != 0 {
		t.Fatalf("movies %+v, reviews %+v, calls %v", ms, e.reviews(""), f.calls)
	}
	var stated []string
	for _, rv := range rs {
		for _, ev := range rv.Evidence {
			stated = append(stated, ev.Role+" "+ev.Provider+ev.Title+" "+ev.ExternalID+" "+filepath.Base(ev.Path))
		}
	}
	slices.Sort(stated)
	if !slices.Equal(stated, []string{"stated tmdb 999 Ember (2010) {tmdb-999}", "stated {tmdb-abc}  Glow (2005) {tmdb-abc}"}) {
		t.Fatalf("evidence %q", stated)
	}
	if got := titles(ms); !slices.Equal(got, []string{"Ember", "Glow"}) {
		t.Fatalf("titles %q", got)
	}
}

// Ids that name different titles, in one name or between a folder and
// its file, decide nothing.
func TestConflictingIdsAreLeftForReview(t *testing.T) {
	f := offices(officeUS, officeUK)
	e := newEnv(t, library.TV, "")
	e.s.Provider = f
	e.episode("The Office (2001) {tvdb-78107} {tmdb-2316}", "The Office - S01E01.mp4")
	e.mustScan()
	show := e.list(item.Show)[0]
	rs := e.reviews(review.ConflictingIdentity)
	if e.matched(show) != 0 || len(rs) != 1 || !slices.Equal(said(rs[0]), []string{"stated tmdb 2316", "stated tvdb 78107", "candidate tmdb 2996"}) {
		t.Fatalf("matched %d, reviews %+v", e.matched(show), e.reviews(""))
	}

	m := movies()
	e = newEnv(t, library.Movies, "")
	e.s.Provider = m
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lantern (2001) {tmdb-11}", "Lantern (2001) {tmdb-11} {imdb-tt0000012}.mp4"))
	e.mustScan()
	if rs := e.reviews(review.ConflictingIdentity); e.matched(e.onlyMovie()) != 0 || len(rs) != 1 || m.called("search") != 0 {
		t.Fatalf("matched %d, reviews %+v, calls %v", e.matched(e.onlyMovie()), e.reviews(""), m.calls)
	}
}

// A match the scanner made from a folder's name is made again from the
// name the folder is renamed to: the show keeps its record, and takes the
// other title's details and episodes.
func TestRenamedFolderStatingAnIdRematches(t *testing.T) {
	f := offices(officeUS, officeUK)
	e := newEnv(t, library.TV, "")
	e.s.Provider = f
	old := e.episode("The Office (2005)", "The Office - S01E01.mp4")
	e.mustScan()
	show := e.showAt("The Office (2005)")
	if e.matched(show) != 2316 || e.firstEpisode(show) != "Pilot" {
		t.Fatalf("matched %d, episode %q", e.matched(show), e.firstEpisode(show))
	}
	rename(t, old, filepath.Join(e.root, "The Office (2001) {tvdb-78107}", "The Office (2001) - S01E01.mp4"))
	must(t, os.RemoveAll(filepath.Dir(old)))
	e.mustScan()
	got := e.showAt("The Office (2001) {tvdb-78107}")
	if got.ID != show.ID || e.matched(got) != 2996 || e.firstEpisode(got) != "Downsize" || len(e.reviews("")) != 0 || len(e.list(item.Show)) != 1 {
		t.Fatalf("show %s was %s, matched %d, episode %q, reviews %+v", got.ID, show.ID, e.matched(got), e.firstEpisode(got), e.reviews(""))
	}
}

// Two shows both matched to the US Office before identity was kept have
// their folders renamed to state their TVDB ids. The match that agrees is
// confirmed. The one that does not is never replaced: it is listed with
// what it is matched to, what the folder states and the title that names,
// until an admin rematches it by hand, which refreshes it and its
// episodes.
func TestMatchMadeBeforeIdentityIsReviewedNotReplaced(t *testing.T) {
	f := offices(officeUS, officeUK)
	e := newEnv(t, library.TV, "")
	e.s.Provider = f
	oldUK := e.episode("The Office (UK)", "The Office - S01E01.mp4")
	e.mustScan()
	fixtureCopy(t, "chapters.mkv", filepath.Join(e.root, "The Office (US)", "The Office - S01E01.mkv"))
	e.mustScan()
	uk, us := e.showAt("The Office (UK)"), e.showAt("The Office (US)")
	id := officeUS.ID
	for _, sh := range []*item.Item{uk, us} {
		must(t, e.items.SetMetadata(&item.Metadata{ItemID: sh.ID, Status: item.Matched, ProviderID: &id, Title: "The Office", ReleaseDate: "2005-03-24"}))
		must(t, e.items.SetIdentityStamp(sh.ID, ""))
	}
	_, err := e.d.Exec(`UPDATE item_metadata SET title = 'Pilot' WHERE source = 'remote' AND item_id IN (SELECT id FROM item WHERE kind = 'episode')`)
	must(t, err)

	rename(t, oldUK, filepath.Join(e.root, "The Office (2001) {tvdb-78107}", "The Office (2001) - S01E01.mp4"))
	rename(t, filepath.Join(e.root, "The Office (US)", "The Office - S01E01.mkv"), filepath.Join(e.root, "The Office US (2005) {tvdb-73244}", "The Office US (2005) - S01E01.mkv"))
	must(t, os.RemoveAll(filepath.Join(e.root, "The Office (UK)")))
	must(t, os.RemoveAll(filepath.Join(e.root, "The Office (US)")))
	e.mustScan()

	gotUK, gotUS := e.showAt("The Office (2001) {tvdb-78107}"), e.showAt("The Office US (2005) {tvdb-73244}")
	rs := e.reviews("")
	if gotUK.ID != uk.ID || gotUS.ID != us.ID || e.matched(gotUK) != 2316 || e.matched(gotUS) != 2316 || e.firstEpisode(gotUK) != "Pilot" {
		t.Fatalf("UK %s was %s matched %d, US %s was %s matched %d", gotUK.ID, uk.ID, e.matched(gotUK), gotUS.ID, us.ID, e.matched(gotUS))
	}
	if len(rs) != 1 || *rs[0].ItemID != uk.ID || rs[0].Reason != review.ConflictingIdentity ||
		!slices.Equal(said(rs[0]), []string{"current tmdb 2316", "stated tvdb 78107", "candidate tmdb 2996"}) || rs[0].Evidence[2].Country != "GB" {
		t.Fatalf("reviews %+v", rs)
	}
	stampUS, err := e.items.IdentityStamp(us.ID)
	must(t, err)
	stampUK, err := e.items.IdentityStamp(uk.ID)
	must(t, err)
	if stampUS == "" || stampUK != "" {
		t.Fatalf("stamps US %q UK %q", stampUS, stampUK)
	}

	// Dismissed, it stays dismissed while the folder still says so, and
	// the confirmed match is not asked about again.
	must(t, e.s.Reviews.SetDismissed(rs[0].ID, e.clock))
	e.mustScan()
	if again := e.reviews(""); len(again) != 1 || again[0].ID != rs[0].ID || again[0].DismissedAt == nil || e.matched(gotUK) != 2316 || f.called("find tvdb_id 73244") != 1 {
		t.Fatalf("a second scan: %+v, matched %d, calls %v", again, e.matched(gotUK), f.calls)
	}

	// The admin's rematch, as PUT /items/{id}/metadata makes it.
	right := officeUK.ID
	rematched, err := e.items.MatchByHand(&item.Metadata{ItemID: uk.ID, Status: item.Matched, ProviderID: &right}, *e.clock)
	must(t, err)
	must(t, e.s.Reviews.Settle(uk.ID, "", review.OfIdentity...))
	if _, _, err := e.s.RetryDue(context.Background(), 10, nil); err != nil || !rematched {
		t.Fatalf("rematched %v: %v", rematched, err)
	}
	m, err := e.items.Remote(uk.ID)
	must(t, err)
	if e.matched(gotUK) != 2996 || m.ReleaseDate != "2001-07-09" || e.firstEpisode(gotUK) != "Downsize" || e.firstEpisode(gotUS) != "Pilot" {
		t.Fatalf("matched %d, released %q, episodes %q %q", e.matched(gotUK), m.ReleaseDate, e.firstEpisode(gotUK), e.firstEpisode(gotUS))
	}
	e.mustScan()
	if stampUK, _ = e.items.IdentityStamp(uk.ID); len(e.reviews("")) != 0 || stampUK == "" || e.matched(gotUK) != 2996 {
		t.Fatalf("after the rematch: reviews %+v, stamp %q", e.reviews(""), stampUK)
	}
}

// firstLight returns the fixture album First Light with its remote row.
func (e *env) firstLight() (*item.Item, *item.Metadata) {
	e.t.Helper()
	for _, al := range e.list(item.Album) {
		if al.ParsedTitle == "First Light" {
			m, err := e.items.Remote(al.ID)
			if err != nil {
				m = &item.Metadata{}
			}
			return al, m
		}
	}
	e.t.Fatal("no First Light")
	return nil, nil
}

// An album folder that states a MusicBrainz release is that release,
// with or without disc folders, and never the one a search would find.
func TestAlbumFolderStatesItsRelease(t *testing.T) {
	for name, disc := range map[string]string{"flat": "", "disc folders": "Disc 1"} {
		t.Run(name, func(t *testing.T) {
			e, mb := musicEnv(t)
			album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
			stated := filepath.Join(e.root, "The Example Band", "First Light (2024) [mb-release-"+otherRelease+"]", disc)
			for _, song := range []string{"01 Opening.flac", "02 Second Wind.mp3", "03 Last Call.m4a"} {
				rename(t, filepath.Join(album, song), filepath.Join(stated, "1-"+song))
			}
			e.mustScan()
			al, m := e.firstLight()
			if m.Status != item.Matched || m.ExternalID != otherRelease || !slices.Equal(mb.releases, []string{otherRelease}) || len(e.reviews(review.ConflictingIdentity)) != 0 {
				t.Fatalf("album %+v matched %+v, releases asked %v, reviews %+v", al, m, mb.releases, e.reviews(""))
			}
			if songs := e.list(item.Song); len(songs) != 4 {
				t.Fatalf("songs %+v", songs)
			}
		})
	}
}

// A release group is an album whatever its edition, not a release: an
// album stated only as a group takes the group's genres, names no
// release, and its id is never asked of the release endpoint.
func TestAlbumFolderStatingOnlyAReleaseGroup(t *testing.T) {
	e, mb := musicEnv(t)
	album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	rename(t, album, album+" [mb-release-group-"+fakeGroup+"]")
	e.mustScan()
	al, m := e.firstLight()
	gs, err := e.s.Genres.From(al.ID, genre.Remote)
	must(t, err)
	var names []string
	for _, g := range gs {
		names = append(names, g.Name)
	}
	if m.Status != item.Matched || m.ExternalID != "" || !slices.Equal(mb.groups, []string{fakeGroup}) || len(mb.releases) != 0 || !slices.Contains(names, "Shoegaze") {
		t.Fatalf("matched %+v, groups asked %v, releases asked %v, genres %v", m, mb.groups, mb.releases, names)
	}
	calls := mb.calls
	e.mustScan()
	if mb.calls != calls {
		t.Fatalf("a second scan asked again: %d calls", mb.calls-calls)
	}
}

// The release a track's tags name is the album's. A folder then renamed
// to state another, or a malformed one, decides nothing: the album keeps
// its match and is listed with every statement.
func TestAlbumIdsFromTagsAndTheirConflicts(t *testing.T) {
	e, mb := musicEnv(t)
	album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	e.retag(filepath.Join(album, "01 Opening.flac"), "MUSICBRAINZ_ALBUMID="+otherRelease)
	e.mustScan()
	if _, m := e.firstLight(); m.ExternalID != otherRelease || !slices.Equal(mb.releases, []string{otherRelease}) {
		t.Fatalf("matched %+v, releases asked %v", m, mb.releases)
	}

	clash := album + " [mb-release-" + fakeRelease + "]"
	rename(t, album, clash)
	e.mustScan()
	al, m := e.firstLight()
	rs := e.reviews(review.ConflictingIdentity)
	if m.ExternalID != otherRelease || len(rs) != 1 || *rs[0].ItemID != al.ID ||
		!slices.Equal(said(rs[0]), []string{"stated musicbrainz " + fakeRelease, "stated musicbrainz " + otherRelease}) {
		t.Fatalf("matched %+v, reviews %+v", m, e.reviews(""))
	}

	rename(t, clash, album+" [mb-release-not-a-uuid]")
	e.mustScan()
	if _, m := e.firstLight(); m.ExternalID != otherRelease || len(e.reviews(review.InvalidIdentity)) != 1 || len(e.reviews(review.ConflictingIdentity)) != 0 {
		t.Fatalf("matched %+v, reviews %+v", m, e.reviews(""))
	}

	rename(t, album+" [mb-release-not-a-uuid]", album)
	e.mustScan()
	if _, m := e.firstLight(); m.ExternalID != otherRelease || len(e.reviews(review.InvalidIdentity))+len(e.reviews(review.ConflictingIdentity)) != 0 {
		t.Fatalf("back as it was: matched %+v, reviews %+v", m, e.reviews(""))
	}
}

// An album no search found is listed until its retry finds it: here under
// a store's " - Single", a suffix MusicBrainz titles do not carry.
func TestRetryFindsAStoreTitledAlbumAndEndsItsReview(t *testing.T) {
	e, mb := musicEnv(t)
	rename(t, filepath.Join(e.root, "Folder Artist", "Folder Album (2019)"), filepath.Join(e.root, "Folder Artist", "First Light - Single"))
	mb.blind = true
	e.mustScan()
	var single *item.Item
	for _, al := range e.list(item.Album) {
		if al.ParsedTitle == "First Light - Single" {
			single = al
		}
	}
	if single == nil {
		t.Fatalf("albums %v", titles(e.list(item.Album)))
	}
	if !slices.ContainsFunc(e.reviews(review.NoMatch), func(r *review.Review) bool { return *r.ItemID == single.ID }) {
		t.Fatalf("reviews %+v", e.reviews(""))
	}

	mb.blind = false
	*e.clock = e.clock.Add(9 * 24 * time.Hour)
	if _, _, err := e.s.RetryDue(context.Background(), 100, nil); err != nil {
		t.Fatal(err)
	}
	m, err := e.items.Remote(single.ID)
	if err != nil || m.Status != item.Matched || m.ExternalID != fakeRelease || len(e.reviews(review.NoMatch)) != 0 {
		t.Fatalf("matched %+v %v, reviews %+v", m, err, e.reviews(""))
	}
}

// anthology is a MusicBrainz that also knows a release of another album.
type anthology struct{ *fakeMusic }

const (
	compilation = "33333333-4444-5555-6666-777777777777"
	otherGroup  = "88888888-7777-6666-5555-444444444444"
)

func (a anthology) Release(ctx context.Context, id string) (*musicbrainz.Details, error) {
	if id != compilation {
		return a.fakeMusic.Release(ctx, id)
	}
	a.calls++
	a.releases = append(a.releases, id)
	return &musicbrainz.Details{ID: id, GroupID: otherGroup, Genres: []string{"Pop"}}, nil
}

// legacyAlbum matches First Light by search, then clears its stamp, as a
// match made by hand or before identity was kept has none.
func (e *env) legacyAlbum() *item.Item {
	e.t.Helper()
	e.mustScan()
	al, m := e.firstLight()
	if m.ExternalID != fakeRelease {
		e.t.Fatalf("matched %+v", m)
	}
	must(e.t, e.items.SetIdentityStamp(al.ID, ""))
	return al
}

// A match made before identity was kept whose folder states another
// edition of the album, the tags naming its release group, takes that
// edition and its cover without a review, and later scans agree with it.
func TestMatchMadeBeforeIdentityTakesAnotherEdition(t *testing.T) {
	e, mb := musicEnv(t)
	al := e.legacyAlbum()
	album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	e.retag(filepath.Join(album, "01 Opening.flac"), "MUSICBRAINZ_RELEASEGROUPID="+fakeGroup)
	rename(t, album, album+" [mb-release-"+otherRelease+"]")
	e.mustScan()
	_, m := e.firstLight()
	stamp, err := e.items.IdentityStamp(al.ID)
	must(t, err)
	if m.ExternalID != otherRelease || filepath.Base(m.PosterPath) != otherRelease+".jpg" || stamp == "" || len(e.reviews(review.ConflictingIdentity)) != 0 {
		t.Fatalf("matched %+v, stamp %q, reviews %+v", m, stamp, e.reviews(""))
	}
	calls := mb.calls
	e.mustScan()
	if mb.calls != calls {
		t.Fatalf("a second scan asked again: %d calls", mb.calls-calls)
	}
}

// A release of another album stated over a match made before identity was
// kept is a conflict: the match stays and is listed with the statement.
func TestMatchMadeBeforeIdentityKeepsItsAlbumOverAnother(t *testing.T) {
	e, mb := musicEnv(t)
	e.s.Music = anthology{mb}
	al := e.legacyAlbum()
	album := filepath.Join(e.root, "The Example Band", "First Light (2024)")
	rename(t, album, album+" [mb-release-"+compilation+"]")
	e.mustScan()
	_, m := e.firstLight()
	stamp, err := e.items.IdentityStamp(al.ID)
	must(t, err)
	rs := e.reviews(review.ConflictingIdentity)
	if m.ExternalID != fakeRelease || stamp != "" || len(rs) != 1 ||
		!slices.Equal(said(rs[0]), []string{"current musicbrainz " + fakeRelease, "stated musicbrainz " + compilation}) {
		t.Fatalf("matched %+v, stamp %q, reviews %+v", m, stamp, e.reviews(""))
	}
}

// An album held in several editions, a folder each stating its release,
// is the release group its tags name. Without a group stated to make them
// one album, the editions clash.
func TestAlbumInSeveralEditionsIsItsReleaseGroup(t *testing.T) {
	e, mb := musicEnv(t)
	band := filepath.Join(e.root, "The Example Band")
	album := filepath.Join(band, "First Light (2024)")
	e.retag(filepath.Join(album, "01 Opening.flac"), "MUSICBRAINZ_RELEASEGROUPID="+fakeGroup)
	rename(t, filepath.Join(album, "03 Last Call.m4a"), filepath.Join(band, "First Light (2024) [mb-release-"+otherRelease+"]", "03 Last Call.m4a"))
	stated := album + " [mb-release-" + fakeRelease + "]"
	rename(t, album, stated)
	e.mustScan()
	_, m := e.firstLight()
	if m.Status != item.Matched || m.ExternalID != "" || !slices.Equal(mb.groups, []string{fakeGroup}) || len(e.reviews(review.ConflictingIdentity)) != 0 {
		t.Fatalf("matched %+v, groups asked %v, reviews %+v", m, mb.groups, e.reviews(""))
	}
	calls := mb.calls
	e.mustScan()
	if mb.calls != calls {
		t.Fatalf("a second scan asked again: %d calls", mb.calls-calls)
	}

	e.retag(filepath.Join(stated, "01 Opening.flac"), "MUSICBRAINZ_RELEASEGROUPID=")
	e.mustScan()
	if rs := e.reviews(review.ConflictingIdentity); len(rs) != 1 {
		t.Fatalf("reviews %+v", e.reviews(""))
	}
}
