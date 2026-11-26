package tmdb

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
)

// appended is what the recorded details responses were fetched with.
var appended = map[string]string{"/movie/603": "release_dates,credits,keywords", "/tv/1396": "content_ratings,aggregate_credits,keywords"}

// replay serves the recorded response for each path and counts requests.
func replay(t *testing.T, routes map[string]string) (*Client, *int32, *[]time.Duration) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if want, ok := appended[r.URL.Path]; ok && r.URL.Query().Get("append_to_response") != want {
			t.Errorf("%s appends %q, want %q", r.URL.Path, r.URL.Query().Get("append_to_response"), want)
		}
		file, ok := routes[r.URL.Path]
		status := http.StatusOK
		switch {
		case !ok:
			file, status = "error_404.json", http.StatusNotFound
		case r.URL.Query().Get("api_key") != "k":
			file, status = "error_401.json", http.StatusUnauthorized
		}
		b, err := os.ReadFile(filepath.Join("testdata", file)) //nolint:gosec // recorded fixture
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	var slept []time.Duration
	c := New(Config{Key: "k", BaseURL: srv.URL, ImageBaseURL: srv.URL + "/img", CacheDir: t.TempDir()})
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return c, &hits, &slept
}

func TestMovieFlow(t *testing.T) {
	c, _, _ := replay(t, map[string]string{"/search/movie": "search_movie.json", "/movie/603": "movie_603.json"})
	res, err := c.SearchMovie(context.Background(), "The Matrix", intp(1999))
	if err != nil || len(res) != 2 {
		t.Fatalf("%v %v", res, err)
	}
	pick, _ := Match(res, Query{Title: "the matrix", Year: intp(1999)})
	if pick == nil || pick.ID != 603 {
		t.Fatalf("pick %+v", pick)
	}
	m, err := c.Movie(context.Background(), 603)
	if err != nil || m.Runtime != 136 || m.Certification != "R" || m.ReleaseDate != "1999-03-30" || m.PosterPath == "" {
		t.Fatalf("%+v %v", m, err)
	}
	if len(m.Genres) != 2 || m.Genres[1] != (Genre{ID: 878, Name: "Science Fiction"}) {
		t.Errorf("genres %+v", m.Genres)
	}
	if len(m.Keywords) != 2 || m.Keywords[1] != 4565 {
		t.Errorf("a movie's keywords are keywords.keywords: %v", m.Keywords)
	}
	if len(m.Cast) != 3 || m.Cast[0] != (Credit{PersonID: 6384, Name: "Keanu Reeves", ProfilePath: "/4D0PpNI0kmP58hgrwGC3wCjxhnm.jpg", Character: "Thomas A. Anderson / Neo"}) {
		t.Errorf("cast %+v", m.Cast)
	}
	if len(m.Crew) != 3 || m.Crew[0].Job != "Director" || m.Crew[1].Name != "Lana Wachowski" || m.Crew[2].Job != "Writer" || m.Crew[2].Department != "Writing" {
		t.Errorf("only key crew is kept: %+v", m.Crew)
	}
}

func TestMovieCollection(t *testing.T) {
	c, hits, _ := replay(t, map[string]string{"/movie/9101": "movie_9101.json", "/movie/9103": "movie_9103.json", "/collection/9001": "collection_9001.json"})
	m, err := c.Movie(context.Background(), 9101)
	if err != nil || m.Collection == nil || *m.Collection != (CollectionRef{ID: 9001, Name: "The Lantern Keeper Collection",
		PosterPath: "/lanternSaga.jpg", BackdropPath: "/lanternSagaWide.jpg"}) {
		t.Fatalf("belongs_to_collection is read: %+v %v", m, err)
	}
	if m, err := c.Movie(context.Background(), 9103); err != nil || m.Collection != nil {
		t.Fatalf("a movie in no collection: %+v %v", m, err)
	}
	col, err := c.Collection(context.Background(), 9001)
	if err != nil || *col != (CollectionRef{ID: 9001, Name: "The Lantern Keeper Collection", Overview: "Every film about the last lantern.",
		PosterPath: "/lanternSaga.jpg", BackdropPath: "/lanternSagaWide.jpg"}) {
		t.Fatalf("%+v %v", col, err)
	}
	before := *hits
	if _, err := c.Collection(context.Background(), 9001); err != nil || *hits != before {
		t.Errorf("collections are cached like any response: %d %d %v", before, *hits, err)
	}
	if _, err := c.Collection(context.Background(), 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("%v", err)
	}
}

func TestTVFlow(t *testing.T) {
	c, _, _ := replay(t, map[string]string{"/search/tv": "search_tv.json", "/tv/1396": "tv_1396.json", "/tv/1396/season/1": "tv_1396_season_1.json"})
	res, err := c.SearchTV(context.Background(), "Breaking Bad", nil)
	if hit, _ := Match(res, Query{Title: "Breaking Bad", TV: true}); err != nil || hit == nil || hit.ID != 1396 {
		t.Fatalf("%v %v", res, err)
	}
	show, err := c.TV(context.Background(), 1396)
	if err != nil || show.Title != "Breaking Bad" || show.Certification != "TV-MA" {
		t.Fatalf("%+v %v", show, err)
	}
	if len(show.Genres) != 2 || show.Genres[0].Name != "Drama" || len(show.Cast) != 2 || show.Cast[1].Character != "Jesse Pinkman" {
		t.Errorf("genres %+v cast %+v", show.Genres, show.Cast)
	}
	if len(show.Keywords) != 1 || show.Keywords[0] != 2231 {
		t.Errorf("a show's keywords are keywords.results: %v", show.Keywords)
	}
	if len(show.Crew) != 1 || show.Crew[0] != (Credit{PersonID: 66633, Name: "Vince Gilligan", ProfilePath: "/z3E0DhBg1V1PZVEtS9vfFPzOWYB.jpg", Job: "Creator"}) {
		t.Errorf("crew is the creators: %+v", show.Crew)
	}
	eps, err := c.Season(context.Background(), 1396, 1)
	if err != nil || len(eps) != 2 || eps[0].Runtime != 58 || eps[1].Title != "Cat's in the Bag..." {
		t.Fatalf("%+v %v", eps, err)
	}
}

func TestMatchIsStrict(t *testing.T) {
	rs := []Result{{ID: 1, Title: "Heat", ReleaseDate: "1995-12-15"}, {ID: 2, Title: "Heat", ReleaseDate: "1986-01-01"}, {ID: 3, Title: "Heated Rivalry"}}
	if m, _ := Match(rs, Query{Title: "Heat", Year: intp(1986)}); m == nil || m.ID != 2 {
		t.Errorf("year: %+v", m)
	}
	if m, tied := Match(rs, Query{Title: "Heat"}); m != nil || len(tied) != 2 {
		t.Errorf("no year picks neither: %+v %+v", m, tied)
	}
	if m, _ := Match(rs, Query{Title: "Heat", Year: intp(2020)}); m != nil {
		t.Errorf("wrong year matched: %+v", m)
	}
	if m, _ := Match(rs, Query{Title: "Unknown"}); m != nil {
		t.Errorf("unknown matched: %+v", m)
	}
}

func TestMatchIgnoresUnicodeNormalization(t *testing.T) {
	rs := []Result{{ID: 194, Title: "Am\u00e9lie", ReleaseDate: "2001-04-25"}}
	if m, _ := Match(rs, Query{Title: "Ame\u0301lie", Year: intp(2001)}); m == nil {
		t.Error("a decomposed accent did not match")
	}
}

func TestNoKeyMakesNoRequest(t *testing.T) {
	c, hits, _ := replay(t, nil)
	c.SetKey("")
	if _, err := c.SearchMovie(context.Background(), "x", nil); !errors.Is(err, ErrNoKey) {
		t.Errorf("got %v", err)
	}
	if *hits != 0 {
		t.Error("request made")
	}
}

func TestSetKeyAppliesFromTheNextRequest(t *testing.T) {
	c, _, _ := replay(t, map[string]string{"/movie/603": "movie_603.json"})
	c.SetKey("")
	if _, err := c.Movie(context.Background(), 603); c.Enabled() || !errors.Is(err, ErrNoKey) {
		t.Fatalf("removed: %v", err)
	}
	c.SetKey("k")
	if _, err := c.Movie(context.Background(), 603); !c.Enabled() || err != nil {
		t.Fatalf("set: %v", err)
	}
}

func TestCheckKey(t *testing.T) {
	c, hits, _ := replay(t, map[string]string{"/authentication": "authentication.json", "/movie/603": "movie_603.json"})
	for range 2 {
		if err := c.CheckKey(context.Background(), "k"); err != nil {
			t.Fatalf("accepted key: %v", err)
		}
	}
	if *hits != 2 {
		t.Errorf("a check is never answered from the cache: %d requests", *hits)
	}
	if err := c.CheckKey(context.Background(), "bad"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("rejected key: %v", err)
	}
	if _, err := c.Movie(context.Background(), 603); err != nil {
		t.Fatalf("a check leaves the client's own key: %v", err)
	}
}

func TestErrors(t *testing.T) {
	c, _, _ := replay(t, map[string]string{"/movie/603": "movie_603.json"})
	if _, err := c.Movie(context.Background(), 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("404: %v", err)
	}
	c.SetKey("bad")
	if _, err := c.Movie(context.Background(), 603); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("401: %v", err)
	}
}

// A provider or proxy cannot stall the task loop for longer than maxWait.
func TestRateLimitWaitIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	var slept []time.Duration
	c := New(Config{Key: "k", BaseURL: srv.URL, CacheDir: t.TempDir()})
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if _, err := c.SearchMovie(context.Background(), "x", nil); !errors.Is(err, ErrRateLimited) {
		t.Fatal(err)
	}
	for _, d := range slept {
		if d > maxWait {
			t.Errorf("slept %v, more than %v", d, maxWait)
		}
	}
}

func TestRateLimitBackoff(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&n, 1) <= 2 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"page":1,"results":[]}`))
	}))
	defer srv.Close()
	var slept []time.Duration
	c := New(Config{Key: "k", BaseURL: srv.URL, CacheDir: t.TempDir()})
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	if _, err := c.SearchMovie(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	backoffs := 0
	for _, d := range slept {
		if d == 2*time.Second {
			backoffs++
		}
	}
	if n != 3 || backoffs != 2 {
		t.Errorf("requests %d slept %v", n, slept)
	}
}

func TestRateLimitGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }))
	defer srv.Close()
	c := New(Config{Key: "k", BaseURL: srv.URL, CacheDir: t.TempDir()})
	c.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.SearchMovie(context.Background(), "x", nil); !errors.Is(err, ErrRateLimited) {
		t.Errorf("got %v", err)
	}
}

func TestServerErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	c := New(Config{Key: "k", BaseURL: srv.URL, CacheDir: t.TempDir()})
	if _, err := c.Movie(context.Background(), 1); !errors.Is(err, ErrUnavailable) {
		t.Errorf("got %v", err)
	}
	srv.Close()
	if _, err := c.Movie(context.Background(), 1); !errors.Is(err, ErrUnavailable) {
		t.Errorf("connection refused: %v", err)
	}
}

func TestResponsesAreCached(t *testing.T) {
	c, hits, _ := replay(t, map[string]string{"/movie/603": "movie_603.json"})
	for range 3 {
		if _, err := c.Movie(context.Background(), 603); err != nil {
			t.Fatal(err)
		}
	}
	if *hits != 1 {
		t.Errorf("hits %d", *hits)
	}
	c.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if _, err := c.Movie(context.Background(), 603); err != nil || *hits != 2 {
		t.Errorf("expired entry not refetched: %d %v", *hits, err)
	}
}

func TestLimiterSpacesRequests(t *testing.T) {
	base := time.Unix(1000, 0)
	now := base
	var slept []time.Duration
	l := &limiter{gap: 25 * time.Millisecond, now: func() time.Time { return now }, sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); now = now.Add(d); return nil }}
	for range 3 {
		if err := l.wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(slept) != 2 || slept[0] != 25*time.Millisecond {
		t.Errorf("slept %v", slept)
	}
}

func TestRefreshAtIsJittered(t *testing.T) {
	now := time.Unix(0, 0)
	lo, hi := now.Add(RefreshEvery*8/10), now.Add(RefreshEvery*12/10)
	seen := map[time.Time]bool{}
	for range 50 {
		at := RefreshAt(now)
		if at.Before(lo) || at.After(hi) {
			t.Fatalf("out of range: %v", at)
		}
		seen[at] = true
	}
	if len(seen) < 10 {
		t.Error("not jittered")
	}
}

func TestImageCaching(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != "/img/w500/f89U.jpg" {
			http.NotFound(w, r)
			return
		}
		b, _ := os.ReadFile("testdata/poster.jpg")
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	dir := t.TempDir()
	c := New(Config{Key: "k", BaseURL: srv.URL, ImageBaseURL: srv.URL + "/img", CacheDir: t.TempDir()})
	p, err := c.CacheImage(context.Background(), dir, "w500", "/f89U.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil || filepath.Dir(p) != filepath.Join(dir, "w500") {
		t.Fatalf("%s %v", p, err)
	}
	if _, err := c.CacheImage(context.Background(), dir, "w500", "/f89U.jpg"); err != nil || hits != 1 {
		t.Errorf("not cached: %d %v", hits, err)
	}
	if _, err := c.CacheImage(context.Background(), dir, "w500", "/missing.jpg"); err == nil {
		t.Error("want error for 404")
	}
	if _, err := c.CacheImage(context.Background(), dir, "w500", "/../../etc/passwd"); err == nil {
		t.Error("want error for path traversal")
	}
	if _, err := c.CacheImage(context.Background(), dir, "w500", ""); err == nil {
		t.Error("want error for empty path")
	}
}

func TestAttributionMentionsTMDB(t *testing.T) {
	if Attribution == "" || !contains(Attribution, "TMDB") {
		t.Error(Attribution)
	}
}

func TestLive(t *testing.T) {
	key := os.Getenv("RABBIT_EARS_TMDB_KEY")
	if os.Getenv("RABBIT_EARS_TMDB_LIVE") == "" || key == "" {
		t.Skip("set RABBIT_EARS_TMDB_LIVE=1 and RABBIT_EARS_TMDB_KEY to run against api.themoviedb.org")
	}
	c := New(Config{Key: key, CacheDir: t.TempDir()})
	res, err := c.SearchMovie(context.Background(), "The Matrix", intp(1999))
	if hit, _ := Match(res, Query{Title: "The Matrix", Year: intp(1999)}); err != nil || hit == nil {
		t.Fatalf("%v %v", res, err)
	}
}

func intp(n int) *int { return &n }

func contains(s, sub string) bool { return strings.Contains(s, sub) }

type textSink struct{ b *strings.Builder }

func (s textSink) Write(e *logstore.Entry) { s.b.WriteString(e.Source + " " + e.Message + "\n") }

// The key travels as a query parameter, so Go's url.Error would carry it
// into every error and log line.
func TestKeyNeverReachesErrorsOrLogs(t *testing.T) {
	const key = "SECRETKEY123"
	var stored, stderr strings.Builder
	old := slog.Default()
	slog.SetDefault(slog.New(logstore.NewHandler(slog.NewTextHandler(&stderr, nil), textSink{&stored}, slog.LevelDebug)))
	t.Cleanup(func() { slog.SetDefault(old) })

	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(429) }))
	c := New(Config{Key: key, BaseURL: limited.URL, CacheDir: t.TempDir()})
	c.sleep = func(context.Context, time.Duration) error { return nil }
	_, errLimited := c.SearchMovie(context.Background(), "x", nil)
	limited.Close()
	_, errDown := c.SearchMovie(context.Background(), "y", nil) // connection refused
	errCheck := c.CheckKey(context.Background(), key)

	if !errors.Is(errLimited, ErrRateLimited) || !errors.Is(errDown, ErrUnavailable) || !errors.Is(errCheck, ErrUnavailable) {
		t.Fatalf("errors: %v / %v / %v", errLimited, errDown, errCheck)
	}
	for name, text := range map[string]string{"error": errDown.Error() + errLimited.Error() + errCheck.Error(), "stored log": stored.String(), "stderr": stderr.String()} {
		if strings.Contains(text, key) {
			t.Errorf("%s contains the API key: %s", name, text)
		}
	}
	if !strings.Contains(stored.String(), "tmdb") || !strings.Contains(stored.String(), "429") {
		t.Errorf("backoff not logged: %q", stored.String())
	}
}
