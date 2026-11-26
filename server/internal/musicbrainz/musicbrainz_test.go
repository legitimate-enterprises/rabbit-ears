package musicbrainz

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	releaseID = "11111111-2222-3333-4444-555555555555"
	artistID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

var routes = map[string]string{
	"/ws/2/release": `{"releases":[
		{"id":"99999999-2222-3333-4444-555555555555","score":100,"title":"First Light (Live)","date":"2024",
		 "artist-credit":[{"name":"The Example Band","artist":{"id":"` + artistID + `","name":"The Example Band"}}]},
		{"id":"` + releaseID + `","score":98,"title":"First Light","date":"2024-03-01",
		 "artist-credit":[{"name":"The Example Band","artist":{"id":"` + artistID + `","name":"The Example Band"}}]}]}`,
	"/ws/2/release/" + releaseID: `{"id":"` + releaseID + `","title":"First Light","genres":[{"name":"indie rock","count":2}],
		"release-group":{"genres":[{"name":"rock","count":5},{"name":"indie rock","count":1}]}}`,
	"/ws/2/artist/" + artistID: `{"id":"` + artistID + `","name":"The Example Band","genres":[{"name":"rock","count":3}]}`,
}

// fake serves routes, answers 503 for the first `busy` requests, and checks
// every request names the client with a contact address.
func fake(t *testing.T, busy int32) (*Client, *int32, *[]time.Duration) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "dev@legitimate.enterprises") {
			t.Errorf("user agent %q", ua)
		}
		if n <= busy {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/release/") {
			if r.URL.Path != "/release/"+releaseID+"/front-500" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte("\xff\xd8jpeg"))
			return
		}
		if r.URL.Query().Get("fmt") != "json" {
			t.Errorf("fmt %q", r.URL.Query().Get("fmt"))
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	var slept []time.Duration
	c := New(Config{BaseURL: srv.URL + "/ws/2", CoverURL: srv.URL, CacheDir: t.TempDir()})
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	return c, &hits, &slept
}

func intp(n int) *int { return &n }

func TestMatchIgnoresUnicodeNormalization(t *testing.T) {
	rs := []Release{{ID: releaseID, Title: "Caf\u00e9", ArtistName: "Bj\u00f6rk"}}
	if Match(rs, "Bjo\u0308rk", "Cafe\u0301", nil) == nil {
		t.Error("decomposed accents did not match")
	}
}

func TestReleaseFlow(t *testing.T) {
	c, hits, slept := fake(t, 0)
	ctx := context.Background()
	rs, err := c.SearchRelease(ctx, "The Example Band", "First Light")
	if err != nil || len(rs) != 2 || rs[1].ArtistID != artistID || rs[1].Date != "2024-03-01" {
		t.Fatalf("%+v %v", rs, err)
	}
	pick := Match(rs, "the example band", "first light", intp(2024))
	if pick == nil || pick.ID != releaseID {
		t.Fatalf("an exact title beats a higher score: %+v", pick)
	}
	if Match(rs, "Someone Else", "First Light", nil) != nil {
		t.Fatal("the artist must agree")
	}
	d, err := c.Release(ctx, releaseID)
	if err != nil || strings.Join(d.Genres, ",") != "Indie Rock,Rock" {
		t.Fatalf("%+v %v", d, err)
	}
	a, err := c.Artist(ctx, artistID)
	if err != nil || strings.Join(a.Genres, ",") != "Rock" {
		t.Fatalf("%+v %v", a, err)
	}
	if _, err := c.Release(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// One request a second: every request after the first waited.
	if len(*slept) != int(*hits)-1 {
		t.Fatalf("%d requests, %d waits", *hits, len(*slept))
	}
	for _, d := range *slept {
		if d <= 0 || d > time.Second {
			t.Fatalf("wait %v", d)
		}
	}
	before := *hits
	if _, err := c.SearchRelease(ctx, "The Example Band", "First Light"); err != nil || *hits != before {
		t.Fatal("a repeated lookup is served from the cache")
	}
}

func TestCoverArt(t *testing.T) {
	c, hits, _ := fake(t, 0)
	dir := t.TempDir()
	p, err := c.CacheCover(context.Background(), dir, releaseID)
	if err != nil || p != filepath.Join(dir, "caa", releaseID+".jpg") {
		t.Fatalf("%q %v", p, err)
	}
	if b, _ := os.ReadFile(p); string(b) != "\xff\xd8jpeg" { //nolint:gosec // test temp dir
		t.Fatalf("%q", b)
	}
	n := *hits
	if _, err := c.CacheCover(context.Background(), dir, releaseID); err != nil || *hits != n {
		t.Fatal("a cached cover is not fetched again")
	}
	if _, err := c.CacheCover(context.Background(), dir, "99999999-2222-3333-4444-555555555555"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := c.CacheCover(context.Background(), dir, "../../etc"); err == nil {
		t.Fatal("a release id that is not an MBID is refused")
	}
}

// A cover's download writes a temporary of its own, so another's under the
// old fixed name is neither in its way nor touched.
func TestCoverArtWritesThroughItsOwnTemporary(t *testing.T) {
	c, _, _ := fake(t, 0)
	dir := t.TempDir()
	other := filepath.Join(dir, "caa", releaseID+".jpg.tmp")
	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CacheCover(context.Background(), dir, releaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal(err)
	}
}

func TestBusyRetriesThenFails(t *testing.T) {
	c, _, slept := fake(t, 2)
	if _, err := c.Artist(context.Background(), artistID); err != nil {
		t.Fatalf("two 503s then success: %v", err)
	}
	if len(*slept) < 2 {
		t.Fatalf("backoff waits %v", *slept)
	}
	c, _, _ = fake(t, 100)
	if _, err := c.Artist(context.Background(), artistID); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

// hit is a search result of release group group, credited to artists.
func hit(id, title, date, group string, score int, artists ...string) Release {
	return Release{ID: id, Title: title, Date: date, GroupID: group, Score: score, ArtistName: strings.Join(artists, " & "), Artists: artists}
}

// A music store's naming is not MusicBrainz's: the album is still found
// when what differs is decoration, and only while one album fits.
func TestMatchSeesThroughStoreNaming(t *testing.T) {
	cases := []struct {
		name, artist, album string
		year                *int
		hits                []Release
		want                string
	}{
		{"a Single suffix", "Alex Warren", "Ordinary - Single", intp(2025),
			[]Release{hit("a", "Ordinary", "2025-02-07", "g", 100, "Alex Warren")}, "a"},
		{"an EP suffix", "Tag Team", "Whoomp! - EP", nil,
			[]Release{hit("a", "Whoomp!", "1993", "g", 100, "Tag Team")}, "a"},
		{"edition wording on either side", "Hans Zimmer & James Newton Howard",
			"The Dark Knight (Collectors Edition) [Original Motion Picture Soundtrack]", intp(2008),
			[]Release{hit("a", "The Dark Knight: Original Motion Picture Soundtrack", "2008-07-15", "g", 100, "Hans Zimmer", "James Newton Howard")}, "a"},
		{"a remix is another album", "Kesha", "TiK-Tok (Remixes) - EP", nil,
			[]Release{hit("a", "TiK ToK", "2009", "g", 100, "Kesha")}, ""},
		{"the artist before the title", "Tavares", "Tavares: Anthology", nil,
			[]Release{hit("a", "Anthology", "2004", "g", 100, "Tavares")}, "a"},
		{"accents and an ampersand", "Joan Jett & The Blackhearts", "Electrodomesticos", intp(1988),
			[]Release{hit("a", "electrodomésticos", "1988", "g", 100, "Joan Jett and the Blackhearts")}, "a"},
		{"more names credited", "Big Data", "Dangerous (feat. Joywave) - Single", intp(2013),
			[]Release{{ID: "a", Title: "Dangerous", Date: "2014-07-23", GroupID: "g", ArtistName: "Big Data feat. Joywave", Artists: []string{"Big Data", "Joywave"}}}, "a"},
		{"fewer names credited", "Louis Jordan & His Tympany Five", "The Best of Louis Jordan", nil,
			[]Release{hit("a", "The Best of Louis Jordan", "1975", "g", 100, "Louis Jordan")}, "a"},
		{"another artist", "Camille & Michael Giacchino", "Ratatouille", nil,
			[]Release{hit("a", "Ratatouille", "2007", "g", 100, "Hans Zimmer")}, ""},
		{"a far year while one album fits", "Chic", "Dance, Dance, Dance: The Best of Chic", intp(1978),
			[]Release{hit("a", "Dance, Dance, Dance: The Best of Chic", "1991", "g", 90, "Chic"),
				hit("b", "Dance, Dance, Dance: The Best of Chic", "2014", "g", 100, "Chic")}, "b"},
		{"a far year while two albums fit", "Various Artists", "'70s Greatest Hits", intp(1970),
			[]Release{hit("a", "70s Greatest Hits", "2002", "g1", 100, "Various Artists"),
				hit("b", "’70s Greatest Hits", "2007", "g2", 100, "Various Artists")}, ""},
		{"the year picks between two albums", "Elvis Presley", "Greatest Hits (Remastered)", intp(2001),
			[]Release{hit("a", "Greatest Hits", "1981", "g1", 100, "Elvis Presley"),
				hit("b", "Greatest Hits", "2001-05", "g2", 90, "Elvis Presley")}, "b"},
		{"the title as stored beats the plain edition", "AC/DC", "Live (Collector's Edition)", intp(1992),
			[]Release{hit("a", "Live", "1992", "g1", 100, "AC/DC"),
				hit("b", "Live (collector’s edition)", "2003", "g2", 90, "AC/DC")}, "b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ""
			if r := Match(c.hits, c.artist, c.album, c.year); r != nil {
				got = r.ID
			}
			if got != c.want {
				t.Errorf("matched %q, want %q", got, c.want)
			}
		})
	}
}

// Find asks for the title as stored first and widens only on a miss: to
// the title without its decoration, then to the first artist named. Every
// search reads enough hits to hold the right pressing of a much-reissued
// album, with each hit's release group and credited names.
func TestFindWidensTheSearchOnlyOnAMiss(t *testing.T) {
	const group = "99999999-8888-7777-6666-555555555555"
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		asked = append(asked, q.Get("query"))
		if q.Get("limit") != "25" {
			t.Errorf("limit %q", q.Get("limit"))
		}
		if !strings.HasPrefix(q.Get("query"), `release:"Ordinary" AND`) {
			_, _ = w.Write([]byte(`{"releases":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"releases":[{"id":"` + releaseID + `","score":100,"title":"Ordinary","date":"2025-02-07",
			"release-group":{"id":"` + group + `"},
			"artist-credit":[{"name":"Alex Warren","joinphrase":" feat. ","artist":{"id":"` + artistID + `"}},{"name":"Someone","artist":{"id":"x"}}]}]}`))
	}))
	t.Cleanup(srv.Close)
	c := New(Config{BaseURL: srv.URL + "/ws/2", CacheDir: t.TempDir()})
	c.sleep = func(context.Context, time.Duration) error { return nil }
	ctx := context.Background()

	r, err := Find(ctx, c.SearchRelease, "Alex Warren", "Ordinary - Single", intp(2025))
	if err != nil || r == nil || r.ID != releaseID || r.GroupID != group || r.ArtistID != artistID ||
		strings.Join(r.Artists, "|") != "Alex Warren|Someone" || r.ArtistName != "Alex Warren feat. Someone" {
		t.Fatalf("%+v %v", r, err)
	}
	want := []string{`release:"Ordinary - Single" AND artist:"Alex Warren"`, `release:"Ordinary" AND artist:"Alex Warren"`}
	if strings.Join(asked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("asked %q", asked)
	}

	asked = nil
	if r, err := Find(ctx, c.SearchRelease, "Hans Zimmer & James Newton Howard", "Nothing Here", nil); r != nil || err != nil {
		t.Fatalf("%+v %v", r, err)
	}
	want = []string{`release:"Nothing Here" AND artist:"Hans Zimmer & James Newton Howard"`, `release:"Nothing Here" AND artist:"Hans Zimmer"`}
	if strings.Join(asked, "\n") != strings.Join(want, "\n") {
		t.Fatalf("asked %q", asked)
	}
}

// A provider error ends the search at once.
func TestFindReturnsASearchError(t *testing.T) {
	calls := 0
	down := func(context.Context, string, string) ([]Release, error) { calls++; return nil, ErrUnavailable }
	if r, err := Find(context.Background(), down, "A & B", "X - Single", nil); r != nil || !errors.Is(err, ErrUnavailable) || calls != 1 {
		t.Fatalf("%+v %v after %d calls", r, err, calls)
	}
}
