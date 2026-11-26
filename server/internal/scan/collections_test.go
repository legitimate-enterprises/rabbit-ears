package scan

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
)

type collectionEnv struct {
	s           *Scanner
	items       *item.Repo
	collections *collection.Repo
	lib         int64
	clock       time.Time
	imageDir    string
	// collectionsDown makes /collection/ answer 503 while movies still answer.
	collectionsDown *atomic.Bool
}

func newCollectionEnv(t *testing.T) *collectionEnv {
	t.Helper()
	d := dbtest.Open(t)
	down := &atomic.Bool{}
	routes := map[string]string{"/movie/9101": "movie_9101.json", "/movie/9102": "movie_9102.json", "/movie/9103": "movie_9103.json",
		"/collection/9001": "collection_9001.json"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/img/") {
			b, _ := os.ReadFile(filepath.Join("..", "tmdb", "testdata", "poster.jpg"))
			_, _ = w.Write(b)
			return
		}
		f, ok := routes[r.URL.Path]
		switch {
		case strings.HasPrefix(r.URL.Path, "/collection/") && down.Load():
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case !ok:
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
	libs := library.New(d)
	lib := &library.Library{Name: "Movies", Kind: library.Movies}
	if err := libs.Create(lib); err != nil {
		t.Fatal(err)
	}
	e := &collectionEnv{items: item.New(d), collections: collection.New(d), lib: lib.ID, clock: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		imageDir: t.TempDir(), collectionsDown: down}
	// No response cache, so each refresh asks the fake server again.
	client := tmdb.New(tmdb.Config{Key: "k", BaseURL: srv.URL, ImageBaseURL: srv.URL + "/img"})
	e.s = New(Config{Libraries: libs, Items: e.items, Files: mediafile.New(d), Editions: edition.New(d), Genres: genre.New(d), People: person.New(d), Provider: client,
		Collections: e.collections, ImageDir: e.imageDir, Settings: sources(t, d, localPreferred), Now: func() time.Time { return e.clock }})
	return e
}

// matched adds a movie already matched to tmdbID and due for a refresh.
func (e *collectionEnv) matched(t *testing.T, title string, tmdbID int64) int64 {
	t.Helper()
	it := &item.Item{LibraryID: e.lib, Kind: item.Movie, ParsedTitle: title}
	if err := e.items.Create(it); err != nil {
		t.Fatal(err)
	}
	e.due(t, it.ID, tmdbID)
	return it.ID
}

func (e *collectionEnv) due(t *testing.T, id, tmdbID int64) {
	t.Helper()
	if err := e.items.SetMetadata(&item.Metadata{ItemID: id, ProviderID: &tmdbID, Status: item.Matched, RetryAt: &e.clock}); err != nil {
		t.Fatal(err)
	}
}

func (e *collectionEnv) refresh(t *testing.T) {
	t.Helper()
	if _, err := e.s.RetryDue(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
}

func (e *collectionEnv) tmdb(t *testing.T) []*collection.View {
	t.Helper()
	vs, err := e.collections.List(0, item.Audience{}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return vs
}

func TestRefreshRecordsTMDBCollections(t *testing.T) {
	e := newCollectionEnv(t)
	one, two := e.matched(t, "The Lantern Keeper", 9101), e.matched(t, "The Lantern Keeper Returns", 9102)
	solo := e.matched(t, "Harbor Lights", 9103)
	e.refresh(t)

	vs := e.tmdb(t)
	if len(vs) != 1 || vs[0].TMDBID == nil || *vs[0].TMDBID != 9001 || vs[0].Name != "The Lantern Keeper Collection" ||
		vs[0].Overview != "Every film about the last lantern." || vs[0].PosterPath != "/lanternSaga.jpg" || vs[0].MovieCount != 2 {
		t.Fatalf("%+v", vs)
	}
	if got, _ := e.collections.Movies(vs[0].ID, item.Audience{}, 10, 0); !slices.Equal(got, []int64{one, two}) {
		t.Fatalf("only the library's movies, by release date: %v", got)
	}
	if of, _ := e.collections.ForMovie(0, solo, item.Audience{}); len(of) != 0 {
		t.Fatalf("%+v", of)
	}
	for _, p := range []string{"w500/lanternSaga.jpg", "w1280/lanternSagaWide.jpg"} {
		if _, err := os.Stat(filepath.Join(e.imageDir, p)); err != nil {
			t.Errorf("collection art cached as item art is: %v", err)
		}
	}

	// Rematched to a movie in no collection, the movie leaves it; the
	// collection goes with its last movie.
	e.due(t, one, 9103)
	e.refresh(t)
	if got, _ := e.collections.Movies(vs[0].ID, item.Audience{}, 10, 0); !slices.Equal(got, []int64{two}) {
		t.Fatalf("%v", got)
	}
	e.due(t, two, 9103)
	e.refresh(t)
	if _, err := e.collections.Get(vs[0].ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("an empty TMDB collection goes: %v", err)
	}
}

// A failed collection lookup never blocks the movie: the collection is
// recorded from what the movie named, keeping any overview it had.
func TestCollectionLookupFailureKeepsWhatTheMovieNamed(t *testing.T) {
	e := newCollectionEnv(t)
	e.collectionsDown.Store(true)
	one, two := e.matched(t, "The Lantern Keeper", 9101), e.matched(t, "The Lantern Keeper Returns", 9102)
	e.refresh(t)
	vs := e.tmdb(t)
	if len(vs) != 1 || vs[0].Name != "The Lantern Keeper Collection" || vs[0].Overview != "" || vs[0].PosterPath != "/lanternSaga.jpg" || vs[0].MovieCount != 2 {
		t.Fatalf("%+v", vs)
	}
	if m, err := e.items.Metadata(one); err != nil || m.Title != "The Lantern Keeper" || m.ContentRating != "PG" {
		t.Fatalf("the movie matched anyway: %+v %v", m, err)
	}

	e.collectionsDown.Store(false)
	e.due(t, one, 9101)
	e.refresh(t)
	e.collectionsDown.Store(true)
	e.due(t, two, 9102)
	e.refresh(t)
	if vs := e.tmdb(t); len(vs) != 1 || vs[0].Overview != "Every film about the last lantern." {
		t.Fatalf("an overview once fetched is kept: %+v", vs)
	}
}
