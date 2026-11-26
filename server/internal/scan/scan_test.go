package scan

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/occasion"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/probe"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/seekpreview"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/serverinfo"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

type env struct {
	t      *testing.T
	s      *Scanner
	lib    *library.Library
	root   string
	items  *item.Repo
	files  *mediafile.Repo
	probes *int32
	clock  *time.Time
	tmdbUp *atomic.Bool
	d      *sql.DB
}

// generation is the artwork generation clients would be told after,
// past the time changes settle.
func (e *env) generation(after time.Duration) int64 {
	e.t.Helper()
	g, err := serverinfo.New(e.d).ArtworkGeneration(time.Now().UTC().Add(after), 10*time.Second)
	if err != nil {
		e.t.Fatal(err)
	}
	return g
}

func fixtureCopy(t *testing.T, name, dest string) {
	t.Helper()
	src, _ := filepath.Abs(filepath.Join("..", "..", "..", "fixtures", name))
	b, err := os.ReadFile(src) //nolint:gosec // fixture path
	if err != nil || testing.Short() {
		t.Skipf("fixture %s missing; run make deps", name)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, b, 0o600); err != nil { //nolint:gosec // test temp dir
		t.Fatal(err)
	}
}

// recorded serves the tmdb package's recorded responses; down switches it to 503s.
func recorded(t *testing.T, up *atomic.Bool) *httptest.Server {
	routes := map[string]string{"/search/movie": "search_movie.json", "/movie/603": "movie_603.json",
		"/search/tv": "search_tv.json", "/tv/1396": "tv_1396.json", "/tv/1396/season/1": "tv_1396_season_1.json"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/img/") {
			b, _ := os.ReadFile(filepath.Join("..", "tmdb", "testdata", "poster.jpg"))
			_, _ = w.Write(b)
			return
		}
		f, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, err := os.ReadFile(filepath.Join("..", "tmdb", "testdata", f))
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// sources makes the server row with the metadata settings src. Tests that
// are not about the settings run under Local preferred, which reads what
// the files say first and fetches the rest.
func sources(t *testing.T, d *sql.DB, src serverinfo.Sources) *serverinfo.Repo {
	t.Helper()
	r := serverinfo.New(d)
	if err := r.Save("Test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSources(src); err != nil {
		t.Fatal(err)
	}
	return r
}

var localPreferred = serverinfo.Sources{Source: serverinfo.Local, Mode: serverinfo.Preferred}

func newEnv(t *testing.T, kind, key string) *env {
	t.Helper()
	d := dbtest.Open(t)
	up := &atomic.Bool{}
	up.Store(true)
	srv := recorded(t, up)
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	e := &env{t: t, root: t.TempDir(), items: item.New(d), files: mediafile.New(d), clock: &clock, tmdbUp: up, d: d, probes: new(int32)}
	libs := library.New(d)
	e.lib = &library.Library{Name: "L", Kind: kind, ScanHour: 3, WatchFiles: true}
	if err := libs.Create(e.lib); err != nil {
		t.Fatal(err)
	}
	if _, err := libs.AddFolder(e.lib.ID, e.root); err != nil {
		t.Fatal(err)
	}
	client := tmdb.New(tmdb.Config{Key: key, BaseURL: srv.URL, ImageBaseURL: srv.URL + "/img", CacheDir: t.TempDir()})
	e.s = New(Config{Libraries: libs, Items: e.items, Files: e.files, Editions: edition.New(d), Genres: genre.New(d), People: person.New(d), Provider: client, ImageDir: t.TempDir(), ThumbDir: t.TempDir(),
		Previews: seekpreview.New(d), PreviewDir: t.TempDir(), Settings: sources(t, d, localPreferred), Reviews: review.New(d),
		Now: func() time.Time { return clock },
		Probe: func(ctx context.Context, p string) (*probe.Result, error) {
			atomic.AddInt32(e.probes, 1)
			return probe.Run(ctx, p)
		}})
	return e
}

func (e *env) scan() error {
	_, err := e.s.Scan(context.Background(), e.lib, nil)
	return err
}

func (e *env) mustScan() {
	e.t.Helper()
	if err := e.scan(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) list(kind string) []*item.Item {
	e.t.Helper()
	its, err := e.items.List(item.Filter{LibraryID: e.lib.ID, Kind: kind, Limit: 1000})
	if err != nil {
		e.t.Fatal(err)
	}
	return its
}

func TestScanMovie(t *testing.T) {
	e := newEnv(t, library.Movies, "k")
	fixtureCopy(t, "hdr10-main10.mkv", filepath.Join(e.root, "The Matrix (1999)", "The.Matrix.1999.1080p.BluRay.mkv"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Extras", "ignored.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Heat-trailer.mp4"))
	e.mustScan()

	ms := e.list(item.Movie)
	if len(ms) != 1 || ms[0].ParsedTitle != "The Matrix" || *ms[0].ParsedYear != 1999 {
		t.Fatalf("movies %+v", ms)
	}
	files, _ := e.files.FilesForItem(ms[0].ID)
	if len(files) != 1 || files[0].ProbedAt == nil || files[0].Container != "mkv" {
		t.Fatalf("files %+v", files)
	}
	if tr, _ := e.files.Tracks(files[0].ID, ""); len(tr) != 5 {
		t.Errorf("tracks %d", len(tr))
	}
	m, err := e.items.Metadata(ms[0].ID)
	if err != nil || m.Status != item.Matched || m.Title != "The Matrix" || *m.RuntimeMinutes != 136 || m.ContentRating != "R" ||
		m.RetryAt == nil || !m.RetryAt.After(*e.clock) {
		t.Fatalf("metadata %+v %v", m, err)
	}
	if _, err := os.Stat(filepath.Join(e.s.ImageDir, "w500", m.PosterPath)); err != nil {
		t.Errorf("poster not cached: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.s.ImageDir, "w1280", m.BackdropPath)); err != nil {
		t.Errorf("backdrop not cached: %v", err)
	}
	e.wantGenresAndCredits(ms[0].ID, 2, 3, 3)
}

func (e *env) wantGenresAndCredits(itemID int64, genres, cast, crew int) {
	e.t.Helper()
	gs, err := e.s.Genres.ForItem(itemID)
	if err != nil || len(gs) != genres {
		e.t.Errorf("genres %d, want %d: %v", len(gs), genres, err)
	}
	cs, err := e.s.People.Credits(itemID)
	n := map[string]int{}
	for _, c := range cs {
		n[c.Kind]++
	}
	if err != nil || n[person.Cast] != cast || n[person.Crew] != crew {
		e.t.Errorf("credits %v, want %d cast and %d crew: %v", n, cast, crew, err)
	}
}

// Matched items without genres or credits get them on their next refresh,
// without searching again.
func TestRefreshFillsGenresAndCreditsOfMatchedItems(t *testing.T) {
	e := newEnv(t, library.Movies, "k")
	it := &item.Item{LibraryID: e.lib.ID, Kind: item.Movie, ParsedTitle: "Renamed by hand"}
	if err := e.items.Create(it); err != nil {
		t.Fatal(err)
	}
	pid, due := int64(603), time.UnixMilli(0)
	if err := e.items.SetMetadata(&item.Metadata{ItemID: it.ID, ProviderID: &pid, Status: item.Matched, Title: "The Matrix", RetryAt: &due}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.RetryDue(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	e.wantGenresAndCredits(it.ID, 2, 3, 3)
}

// A matched movie fetched before keywords were stored joins an occasion by
// keyword at its next refresh.
func TestRefreshStoresKeywords(t *testing.T) {
	e := newEnv(t, library.Movies, "k")
	it := &item.Item{LibraryID: e.lib.ID, Kind: item.Movie, ParsedTitle: "The Matrix"}
	if err := e.items.Create(it); err != nil {
		t.Fatal(err)
	}
	pid, due := int64(603), time.UnixMilli(0)
	if err := e.items.SetMetadata(&item.Metadata{ItemID: it.ID, ProviderID: &pid, Status: item.Matched, Title: "The Matrix", RetryAt: &due}); err != nil {
		t.Fatal(err)
	}
	o := &occasion.Occasion{Name: "Dystopia Day", Month: 5, Day: 1, Keywords: []int64{4565}}
	if err := occasion.New(e.d).Create(o); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.RetryDue(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if ms, err := e.items.List(item.Filter{OccasionID: o.ID}); err != nil || len(ms) != 1 || ms[0].ID != it.ID {
		t.Fatalf("members %+v %v", ms, err)
	}
}

func TestScanTVWithChaptersAndEpisodeMetadata(t *testing.T) {
	e := newEnv(t, library.TV, "k")
	s1 := filepath.Join(e.root, "Breaking Bad (2008)", "Season 01")
	fixtureCopy(t, "chapters.mkv", filepath.Join(s1, "Breaking.Bad.S01E01.720p.mkv"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(s1, "Breaking.Bad.S01E02.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Breaking Bad (2008)", "Specials", "Breaking.Bad.S00E01.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "loose.S01E01.mp4"))
	e.mustScan()

	shows := e.list(item.Show)
	if len(shows) != 1 || shows[0].ParsedTitle != "Breaking Bad" {
		t.Fatalf("shows %+v", shows)
	}
	seasons := e.list(item.Season)
	if len(seasons) != 2 || *seasons[0].SeasonNumber+*seasons[1].SeasonNumber != 1 {
		t.Fatalf("seasons %+v", seasons)
	}
	eps, _ := e.items.Episodes(shows[0].ID)
	if len(eps) != 3 {
		t.Fatalf("episodes %d", len(eps))
	}
	sm, err := e.items.Metadata(shows[0].ID)
	if err != nil || sm.Status != item.Matched || sm.ContentRating != "TV-MA" || *sm.RuntimeMinutes != 44 {
		t.Fatalf("show metadata %+v %v", sm, err)
	}
	em, err := e.items.Metadata(eps[1].ID) // eps[0] is the special
	if err != nil || em.Title != "Pilot" || *em.RuntimeMinutes != 58 {
		t.Fatalf("episode metadata %+v %v", em, err)
	}
	e.wantGenresAndCredits(shows[0].ID, 2, 2, 1)
	if _, err := e.items.Metadata(eps[0].ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("special has no provider data in the recording: %v", err)
	}
	f, _ := e.files.FilesForItem(eps[1].ID)
	chs, _ := e.files.Chapters(f[0].ID)
	if len(chs) != 3 {
		t.Fatalf("chapters %d", len(chs))
	}
	for _, c := range chs {
		if _, err := os.Stat(c.ThumbnailPath); c.ThumbnailPath == "" || err != nil {
			t.Errorf("chapter %d thumbnail %q: %v", c.Position, c.ThumbnailPath, err)
		}
	}
}

// countTV counts show fetches.
type countTV struct {
	Provider
	calls *int
}

func (c countTV) TV(ctx context.Context, id int64) (*tmdb.Details, error) {
	*c.calls++
	return c.Provider.TV(ctx, id)
}

// A show is fetched again for an episode added since, not for one the
// provider does not list.
func TestEpisodeTheProviderLacksFetchesItsShowOnce(t *testing.T) {
	e := newEnv(t, library.TV, "k")
	e.s.Now = time.Now // the clock items are added by
	calls := 0
	e.s.Provider = countTV{e.s.Provider, &calls}
	s1 := filepath.Join(e.root, "Breaking Bad (2008)", "Season 01")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(s1, "Breaking.Bad.S01E01.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(s1, "Breaking.Bad.S01E09.mp4"))
	e.mustScan()
	e.mustScan()
	if calls != 1 {
		t.Fatalf("show fetched %d times", calls)
	}
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(s1, "Breaking.Bad.S01E02.mp4"))
	e.mustScan()
	ep, err := e.items.Episode(e.list(item.Season)[0].ID, 2)
	must(t, err)
	if m, err := e.items.Metadata(ep.ID); calls != 2 || err != nil || m.Title == "" {
		t.Fatalf("new episode: %d fetches, %+v %v", calls, m, err)
	}
}

// goneTV is a provider whose shows are no longer on TMDB.
type goneTV struct{ countTV }

func (g goneTV) TV(ctx context.Context, id int64) (*tmdb.Details, error) {
	*g.calls++
	return nil, tmdb.ErrNotFound
}

// A matched show TMDB no longer has is asked once for a new episode, not
// on every scan after.
func TestShowGoneFromTheProviderIsNotFetchedEveryScan(t *testing.T) {
	e := newEnv(t, library.TV, "k")
	e.s.Now = time.Now // the clock items are added by
	s1 := filepath.Join(e.root, "Breaking Bad (2008)", "Season 01")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(s1, "Breaking.Bad.S01E01.mp4"))
	e.mustScan()
	calls := 0
	e.s.Provider = goneTV{countTV{e.s.Provider, &calls}}
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(s1, "Breaking.Bad.S01E09.mp4"))
	e.mustScan()
	e.mustScan()
	e.mustScan()
	if calls != 1 {
		t.Fatalf("show fetched %d times", calls)
	}
}

func TestOutageLeavesCompleteUnmatchedItemThenRecovers(t *testing.T) {
	e := newEnv(t, library.Movies, "k")
	fixtureCopy(t, "hdr10-main10.mkv", filepath.Join(e.root, "The Matrix (1999)", "m.mkv"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Other (2000)", "o.mp4"))
	e.tmdbUp.Store(false)
	e.mustScan()

	ms := e.list(item.Movie)
	if len(ms) != 2 {
		t.Fatalf("movies %d", len(ms))
	}
	for _, m := range ms {
		files, _ := e.files.FilesForItem(m.ID)
		tr, _ := e.files.Tracks(files[0].ID, "")
		md, err := e.items.Metadata(m.ID)
		if len(files) != 1 || files[0].ProbedAt == nil || len(tr) == 0 || err != nil || md.Status != item.Unmatched ||
			md.RetryAt == nil || !md.RetryAt.Equal(e.clock.Add(RetryDelay)) {
			t.Fatalf("%s: files %v tracks %d meta %+v %v", m.ParsedTitle, files, len(tr), md, err)
		}
	}
	// Not yet due: nothing changes.
	if _, err := e.s.RetryDue(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if md, _ := e.items.Metadata(ms[0].ID); md.Status != item.Unmatched {
		t.Fatal("retried before due")
	}
	e.tmdbUp.Store(true)
	*e.clock = e.clock.Add(2 * time.Hour)
	if _, err := e.s.RetryDue(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	var matched, unmatched int
	for _, m := range ms {
		md, _ := e.items.Metadata(m.ID)
		if md.Status == item.Matched {
			matched++
		} else {
			unmatched++
			if md.RetryAt == nil || !md.RetryAt.After(*e.clock) {
				t.Errorf("unmatched title needs a later retry: %+v", md)
			}
		}
	}
	if matched != 1 || unmatched != 1 {
		t.Errorf("matched %d unmatched %d", matched, unmatched)
	}
}

func TestOutageCostsOneRequestPerBatch(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { atomic.AddInt32(&hits, 1); w.WriteHeader(503) }))
	defer srv.Close()
	e := newEnv(t, library.Movies, "k")
	e.s.Provider = tmdb.New(tmdb.Config{Key: "k", BaseURL: srv.URL, CacheDir: t.TempDir()})
	for _, n := range []string{"A (2001)", "B (2002)", "C (2003)"} {
		fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, n, "f.mp4"))
	}
	e.mustScan()
	if hits != 1 {
		t.Errorf("requests %d", hits)
	}
}

func TestNoKeyLeavesNoMetadataRows(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "f.mp4"))
	e.mustScan()
	ms := e.list(item.Movie)
	if _, err := e.items.Metadata(ms[0].ID); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("%v", err)
	}
}

// noShows is a provider that finds no show.
type noShows struct{ Provider }

func (noShows) Enabled() bool { return true }

func (noShows) SearchTV(context.Context, string, *int) ([]tmdb.Result, error) { return nil, nil }

func TestMatchLibraryReachesEveryShowPastSeasonsAndOneBatch(t *testing.T) {
	e := newEnv(t, library.TV, "")
	e.s.Provider = noShows{}
	var shows []*item.Item
	for n := 0; n <= batchLimit; n++ {
		show := &item.Item{LibraryID: e.lib.ID, Kind: item.Show, ParsedTitle: "Show " + strconv.Itoa(n)}
		must(t, e.items.Create(show))
		shows = append(shows, show)
	}
	for n := 1; n <= batchLimit; n++ {
		must(t, e.items.Create(&item.Item{LibraryID: e.lib.ID, ParentID: &shows[0].ID, Kind: item.Season, ParsedTitle: "Season", SeasonNumber: &n}))
	}
	if _, err := e.s.MatchLibrary(context.Background(), e.lib); err != nil {
		t.Fatal(err)
	}
	if _, err := e.items.Metadata(shows[batchLimit].ID); err != nil {
		t.Fatalf("last show never looked up: %v", err)
	}
}

func TestIncrementalRescan(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	a := filepath.Join(e.root, "A (2001)", "a.mp4")
	b := filepath.Join(e.root, "B (2002)", "b.mkv")
	fixtureCopy(t, "h264-high41.mp4", a)
	fixtureCopy(t, "chapters.mkv", b)
	e.mustScan()
	e.mustScan()
	if *e.probes != 2 {
		t.Fatalf("unchanged files were re-probed: %d", *e.probes)
	}
	// Changing a file re-probes only it.
	fixtureCopy(t, "hdr10-main10.mkv", b)
	e.mustScan()
	if *e.probes != 3 {
		t.Errorf("probes %d", *e.probes)
	}
	f, _ := e.files.FileByPath(b)
	if tr, _ := e.files.Tracks(f.ID, ""); len(tr) != 5 {
		t.Errorf("tracks not replaced: %d", len(tr))
	}
	// A file gone from disk is marked and keeps its movie; the other is
	// untouched.
	if err := os.RemoveAll(filepath.Dir(a)); err != nil {
		t.Fatal(err)
	}
	e.mustScan()
	if ms := e.list(item.Movie); len(ms) != 2 || !e.missing(a) || e.missing(b) || *e.probes != 3 {
		t.Errorf("movies %+v, a missing %v, b missing %v, probes %d", ms, e.missing(a), e.missing(b), *e.probes)
	}
}

func (e *env) missing(path string) bool {
	e.t.Helper()
	f, err := e.files.FileByPath(path)
	if err != nil {
		e.t.Fatal(err)
	}
	return f.MissingAt != nil
}

// A volume that is not mounted leaves its library folder unreadable, or
// readable and empty. Either way a scan deletes nothing: the movie keeps
// its watch state, its file is marked, and the file's chapter thumbnails
// stay. The volume coming back clears the mark without another probe.
func TestUnmountedVolumeMarksFilesAndDeletesNothing(t *testing.T) {
	for _, c := range []struct {
		name     string
		readable bool
	}{{"unreadable folder", false}, {"empty folder", true}} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, library.Movies, "")
			a := filepath.Join(e.root, "A (2001)", "a.mkv")
			fixtureCopy(t, "chapters.mkv", a)
			e.mustScan()
			m := e.onlyMovie()
			who := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A"}
			must(t, account.New(e.d).Create(who))
			states := watchstate.New(e.d)
			must(t, states.Save(&watchstate.State{AccountID: who.ID, ItemID: m.ID, PositionMs: 5000}))
			f, _ := e.files.FileByPath(a)
			thumbs := filepath.Join(e.s.ThumbDir, strconv.FormatInt(f.ID, 10))
			if _, err := os.Stat(thumbs); err != nil {
				t.Fatal(err)
			}

			volume := filepath.Join(t.TempDir(), "volume")
			must(t, os.Rename(e.root, volume))
			if c.readable {
				must(t, os.Mkdir(e.root, 0o750))
			}
			sum, err := e.s.Scan(context.Background(), e.lib, nil)
			if (err == nil) != c.readable {
				t.Errorf("scan error %v", err)
			}
			if got := sum.String(); got != "No changes; 0 movies checked; 1 file missing" {
				t.Errorf("summary %q", got)
			}
			if e.onlyMovie().ID != m.ID || !e.missing(a) {
				t.Fatalf("missing %v", e.missing(a))
			}
			if w, err := states.Get(who.ID, m.ID); err != nil || w.PositionMs != 5000 {
				t.Errorf("watch state %+v %v", w, err)
			}
			if _, err := os.Stat(thumbs); err != nil {
				t.Errorf("chapter thumbnails: %v", err)
			}

			if c.readable {
				must(t, os.Remove(e.root))
			}
			must(t, os.Rename(volume, e.root))
			probes := *e.probes
			if got := scanSummary(t, e); got != "No changes; 1 movie checked" {
				t.Errorf("summary once back %q", got)
			}
			if e.onlyMovie().ID != m.ID || e.missing(a) || *e.probes != probes {
				t.Fatalf("missing %v, probed %d", e.missing(a), *e.probes-probes)
			}
		})
	}
}

// A scan while a library folder cannot be read keeps the reviews raised
// of what is in it, dismissed ones included, so the volume coming back
// raises none of them again.
func TestUnmountedVolumeKeepsItsReviews(t *testing.T) {
	for _, c := range []struct {
		kind, reason string
		fill         func(e *env)
	}{
		{library.TV, review.Unrecognized, func(e *env) {
			fixtureCopy(e.t, "h264-high41.mp4", filepath.Join(e.root, "Lamp Street", "bonus clip.mp4"))
			e.mustScan()
		}},
		{library.Movies, review.AmbiguousMove, func(e *env) {
			a, b := filepath.Join(e.root, "Lantern (2001)", "Lantern.mp4"), filepath.Join(e.root, "Lamplight (2003)", "Lamplight.mp4")
			fixtureCopy(e.t, "h264-high41.mp4", a)
			clone(e.t, a, b)
			e.mustScan()
			rename(e.t, a, filepath.Join(e.root, "Glow (2005)", "Glow.mp4"))
			must(e.t, os.Remove(b))
			e.mustScan()
		}},
	} {
		t.Run(c.reason, func(t *testing.T) {
			e := newEnv(t, c.kind, "")
			c.fill(e)
			rs := e.reviews(c.reason)
			if len(rs) == 0 {
				t.Fatal("no review raised")
			}
			must(t, e.s.Reviews.SetDismissed(rs[0].ID, e.clock))

			volume := filepath.Join(t.TempDir(), "volume")
			must(t, os.Rename(e.root, volume))
			if err := e.scan(); err == nil {
				t.Fatal("a scan of an unreadable folder reported no error")
			}
			must(t, os.Rename(volume, e.root))
			e.mustScan()
			got, err := e.s.Reviews.Get(rs[0].ID)
			if err != nil || got.DismissedAt == nil || len(e.reviews(c.reason)) != len(rs) {
				t.Fatalf("dismissed review %+v %v, reviews %+v were %+v", got, err, e.reviews(c.reason), rs)
			}
		})
	}
}

// A show whose library folder is not mounted is not taken for renamed by
// a folder of its title found in another of the library's folders: once
// the volume is back it is the show it was, with its episode and what was
// played of it.
func TestUnmountedShowIsNotTakenForRenamed(t *testing.T) {
	e := newEnv(t, library.TV, "")
	other := t.TempDir()
	_, err := e.s.Libraries.AddFolder(e.lib.ID, other)
	must(t, err)
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "Lamp Street", "Lamp Street - S01E01.mp4"))
	e.mustScan()
	show := e.list(item.Show)[0]
	e.play(e.list(item.Episode)[0].ID, 5000, 100)
	plays := e.plays()

	volume := filepath.Join(t.TempDir(), "volume")
	must(t, os.Rename(e.root, volume))
	fixtureCopy(t, "chapters.mkv", filepath.Join(other, "Lamp Street", "Season 02", "Lamp Street - S02E01.mkv"))
	if err := e.scan(); err == nil {
		t.Fatal("a scan of an unreadable folder reported no error")
	}
	if got, err := e.items.Get(show.ID); err != nil || got.Path != show.Path {
		t.Fatalf("show %+v %v while its folder is unmounted, was at %q", got, err, show.Path)
	}
	must(t, os.Rename(volume, e.root))
	e.mustScan()
	if got, err := e.items.Get(show.ID); err != nil || got.Path != show.Path || e.plays() != plays {
		t.Fatalf("show %+v %v once back, was at %q; plays %q were %q", got, err, show.Path, e.plays(), plays)
	}
}

func TestProbeFailureDoesNotBlockScan(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "A (2001)", "a.mp4"))
	fixtureCopy(t, "h264-high41.mp4", filepath.Join(e.root, "B (2002)", "b.mp4"))
	var mu sync.Mutex
	broken := true
	real := e.s.Probe
	e.s.Probe = func(ctx context.Context, p string) (*probe.Result, error) {
		mu.Lock()
		defer mu.Unlock()
		if broken && filepath.Base(p) == "a.mp4" {
			return nil, errors.New("boom")
		}
		return real(ctx, p)
	}
	e.mustScan()
	fa, _ := e.files.FileByPath(filepath.Join(e.root, "A (2001)", "a.mp4"))
	fb, _ := e.files.FileByPath(filepath.Join(e.root, "B (2002)", "b.mp4"))
	if fa.ProbedAt != nil || fb.ProbedAt == nil {
		t.Fatalf("a %v b %v", fa.ProbedAt, fb.ProbedAt)
	}
	mu.Lock()
	broken = false
	mu.Unlock()
	e.mustScan()
	if fa, _ = e.files.FileByPath(fa.Path); fa.ProbedAt == nil {
		t.Error("not retried")
	}
}

func dbOf(e *env) db.DBTX { return e.d }

func TestWalkFollowsASymlinkedRootAndFile(t *testing.T) {
	real, link := t.TempDir(), filepath.Join(t.TempDir(), "Movies")
	if err := os.WriteFile(filepath.Join(real, "a.mkv"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "target.mkv")
	if err := os.WriteFile(outside, []byte("1234567890"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(real, "b.mkv")); err != nil {
		t.Fatal(err)
	}
	got, err := walk(link, library.Movies, videoExt)
	if err != nil {
		t.Fatal(err)
	}
	sizes := map[string]int64{}
	for _, c := range got {
		sizes[c.path] = c.size
	}
	if len(got) != 2 || sizes[filepath.Join(link, "a.mkv")] != 5 || sizes[filepath.Join(link, "b.mkv")] != 10 {
		t.Fatalf("candidates %+v", got)
	}
}

// A library folder stored with a trailing slash is the same folder: loose
// files in it are their own movies, not the movie of a folder named for it.
func TestScanOfARootStoredWithATrailingSlash(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	if _, err := e.d.Exec(`UPDATE library_folder SET path = ? WHERE library_id = ?`, e.root+"/", e.lib.ID); err != nil {
		t.Fatal(err)
	}
	touch(t, filepath.Join(e.root, "Alpha.mkv"))
	touch(t, filepath.Join(e.root, "Beta.mkv"))
	touch(t, filepath.Join(e.root, "Gamma (2003)", "Gamma (2003).mkv"))
	e.mustScan()
	var titles []string
	for _, m := range e.list(item.Movie) {
		titles = append(titles, m.ParsedTitle)
	}
	slices.Sort(titles)
	if want := []string{"Alpha", "Beta", "Gamma"}; !slices.Equal(titles, want) {
		t.Errorf("movies %v, want %v", titles, want)
	}
}

// Names the parser now reads another way, scanned before as it read them
// then: the movies, shows and episodes keep their items and their watch
// state, and a name that now holds more episodes gains the others.
func TestRescanKeepsItemsScannedUnderTheOldParse(t *testing.T) {
	who := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A"}
	watch := func(e *env, ids ...int64) *watchstate.Repo {
		must(t, account.New(e.d).Create(who))
		states := watchstate.New(e.d)
		for _, id := range ids {
			must(t, states.Save(&watchstate.State{AccountID: who.ID, ItemID: id, PositionMs: 5000}))
		}
		return states
	}
	// asScanned gives an item the title and year an earlier scan read.
	asScanned := func(e *env, it *item.Item, title string, year *int) {
		it.ParsedTitle, it.ParsedYear = title, year
		must(t, e.items.Update(it))
	}
	stub := func(e *env) {
		e.s.Probe = func(context.Context, string) (*probe.Result, error) { return &probe.Result{Container: "mkv"}, nil }
	}
	y := func(n int) *int { return &n }

	t.Run("movies", func(t *testing.T) {
		e := newEnv(t, library.Movies, "")
		stub(e)
		touch(t, filepath.Join(e.root, "(500) Days of Summer (2009).mkv"))
		touch(t, filepath.Join(e.root, "Blade Runner 2049.mkv"))
		touch(t, filepath.Join(e.root, "[REC] (2007)", "[REC] (2007).mkv"))
		e.mustScan()
		type read struct {
			title string
			year  *int
		}
		before := map[string]read{"(500) Days of Summer": {"Days of Summer", y(2009)}, "Blade Runner 2049": {"Blade Runner", y(2049)}, "REC": {"2007", nil}}
		var ids []int64
		for _, m := range e.list(item.Movie) {
			ids = append(ids, m.ID)
			asScanned(e, m, before[m.ParsedTitle].title, before[m.ParsedTitle].year)
		}
		if len(ids) != 3 {
			t.Fatalf("movies %d", len(ids))
		}
		states := watch(e, ids...)
		e.mustScan()
		for _, id := range ids {
			if _, err := states.Get(who.ID, id); err != nil {
				t.Errorf("movie %d lost its watch state: %v", id, err)
			}
		}
		var after []int64
		for _, m := range e.list(item.Movie) {
			after = append(after, m.ID)
		}
		slices.Sort(ids)
		slices.Sort(after)
		if !slices.Equal(after, ids) {
			t.Errorf("movies after rescan %v, want %v", after, ids)
		}
	})

	t.Run("music", func(t *testing.T) {
		e := newEnv(t, library.Music, "")
		e.s.Probe = func(context.Context, string) (*probe.Result, error) {
			return &probe.Result{Container: "mp3", Tags: map[string]string{}}, nil
		}
		touch(t, filepath.Join(e.root, "Band", "(500) Days (2009)", "01 Song.mp3"))
		touch(t, filepath.Join(e.root, "Band", "[REC] (2007)", "01 Song.mp3"))
		e.mustScan()
		albums := map[int64]string{}
		for _, a := range e.list(item.Album) {
			if a.ParsedTitle == "(500) Days" {
				asScanned(e, a, "Days", y(2009))
			} else {
				asScanned(e, a, "2007", nil)
			}
			albums[a.ID] = a.ParsedTitle
		}
		e.mustScan()
		now := e.list(item.Album)
		if len(now) != 2 || len(e.list(item.Song)) != 2 {
			t.Fatalf("albums %d, songs %d after the rescan", len(now), len(e.list(item.Song)))
		}
		for _, a := range now {
			if albums[a.ID] == "" || a.ParsedTitle != albums[a.ID] {
				t.Errorf("album %d is %q, want the one it had", a.ID, a.ParsedTitle)
			}
		}
	})

	t.Run("shows and episodes", func(t *testing.T) {
		e := newEnv(t, library.TV, "")
		stub(e)
		touch(t, filepath.Join(e.root, "Blade Runner 2049", "Blade Runner 2049 - S01E01.mkv"))
		touch(t, filepath.Join(e.root, "Lamp Street", "Lamp Street - S01E01-S01E02.mkv"))
		e.mustScan()
		for _, s := range e.list(item.Show) {
			if s.ParsedTitle == "Blade Runner 2049" {
				asScanned(e, s, "Blade Runner", y(2049))
			}
		}
		lamp := e.list(item.Episode)
		var first, second *item.Item
		for _, ep := range lamp {
			if p, _ := e.items.Get(*ep.ParentID); p != nil {
				if sh, _ := e.items.Get(*p.ParentID); sh != nil && sh.ParsedTitle == "Lamp Street" {
					if *ep.EpisodeNumber == 1 {
						first = ep
					} else {
						second = ep
					}
				}
			}
		}
		if first == nil || second == nil {
			t.Fatalf("episodes %+v", lamp)
		}
		// Scanned before, the file held episode 1 alone.
		must(t, e.items.Delete(second.ID))
		var ids []int64
		for _, ep := range e.list(item.Episode) {
			ids = append(ids, ep.ID)
		}
		states := watch(e, ids...)
		e.mustScan()
		for _, id := range ids {
			if _, err := states.Get(who.ID, id); err != nil {
				t.Errorf("episode %d lost its watch state: %v", id, err)
			}
		}
		if got := len(e.list(item.Episode)); got != 3 {
			t.Errorf("episodes after rescan %d, want 3 (the second of the chained file is new)", got)
		}
		shows := e.list(item.Show)
		if len(shows) != 2 {
			t.Errorf("shows after rescan %d", len(shows))
		}
	})
}

// A folder a scan cannot read is reported with its path, not skipped in
// silence, and the files seen elsewhere are still scanned.
func TestScanReportsAFolderItCannotRead(t *testing.T) {
	e := newEnv(t, library.Movies, "")
	touch(t, filepath.Join(e.root, "A (2001)", "A (2001).mkv"))
	locked := filepath.Join(e.root, "B (2002)")
	touch(t, filepath.Join(locked, "B (2002).mkv"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // a folder needs its execute bit to be removed
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("permissions do not stop this user reading")
	}
	sum, err := e.s.Scan(context.Background(), e.lib, nil)
	if err == nil || !strings.Contains(err.Error(), locked) {
		t.Fatalf("error %v, want one naming %s", err, locked)
	}
	if len(e.list(item.Movie)) != 1 || sum.Checked != 1 {
		t.Errorf("movies %d, checked %d; want the readable one", len(e.list(item.Movie)), sum.Checked)
	}
}

// A show named like an extras folder is a show, and an album named so is an
// album; the extras of a title are left out wherever they sit under it.
func TestExtrasFoldersAreSkippedOnlyWhereTheyAreExtras(t *testing.T) {
	tv := newEnv(t, library.TV, "")
	tv.s.Probe = func(context.Context, string) (*probe.Result, error) { return &probe.Result{Container: "mp4"}, nil }
	touch(t, filepath.Join(tv.root, "Extras", "Season 01", "Extras - S01E01.mp4"))
	touch(t, filepath.Join(tv.root, "Lamp Street", "Lamp Street - S01E01.mp4"))
	touch(t, filepath.Join(tv.root, "Lamp Street", "Extras", "Lamp Street - S01E02.mp4"))
	touch(t, filepath.Join(tv.root, "Lamp Street", "Season 01", "Featurettes", "Lamp Street - S01E03.mp4"))
	tv.mustScan()
	var shows []string
	for _, s := range tv.list(item.Show) {
		shows = append(shows, s.ParsedTitle)
	}
	slices.Sort(shows)
	if !slices.Equal(shows, []string{"Extras", "Lamp Street"}) || len(tv.list(item.Episode)) != 2 {
		t.Errorf("shows %v, episodes %d; want both shows and one episode each", shows, len(tv.list(item.Episode)))
	}

	music := newEnv(t, library.Music, "")
	music.s.Probe = func(context.Context, string) (*probe.Result, error) {
		return &probe.Result{Container: "flac", Tags: map[string]string{}}, nil
	}
	touch(t, filepath.Join(music.root, "The Jam", "Extras", "01 Song.flac"))
	music.mustScan()
	if n := len(music.list(item.Song)); n != 1 {
		t.Errorf("songs %d, want the one in the album named Extras", n)
	}

	movies := newEnv(t, library.Movies, "")
	touch(t, filepath.Join(movies.root, "Extras", "Loose (2001).mkv"))
	touch(t, filepath.Join(movies.root, "Heat (1995)", "Behind The Scenes", "Heat (1995).mkv"))
	movies.mustScan()
	if n := len(movies.list(item.Movie)); n != 0 {
		t.Errorf("movies %d, want the extras of a movie left out at any depth", n)
	}
}
