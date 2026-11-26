// Package tmdb is the TMDB v3 client: rate limited, with 429 backoff and an
// on-disk response cache, so a scan never hammers the provider.
package tmdb

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/atomicfile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/parse"
)

// Attribution is the notice TMDB's terms require wherever its data shows.
const Attribution = "This product uses the TMDB API but is not endorsed or certified by TMDB."

// RefreshEvery is the nominal time between metadata refreshes; RefreshAt
// jitters it so a library does not refresh in one burst.
const RefreshEvery = 7 * 24 * time.Hour

const (
	defaultBase  = "https://api.themoviedb.org/3"
	defaultImage = "https://image.tmdb.org/t/p"
	cacheTTL     = 24 * time.Hour
	maxRetries   = 3
	// maxWait bounds one rate-limit wait, whatever Retry-After says: it is
	// slept on the task loop's only goroutine.
	maxWait = time.Minute
	// TMDB allows roughly 40 requests a second.
	requestGap = 25 * time.Millisecond
)

// Errors a caller may branch on.
var (
	ErrNoKey        = errors.New("tmdb: no API key configured")
	ErrNotFound     = errors.New("tmdb: not found")
	ErrUnauthorized = errors.New("tmdb: API key rejected")
	ErrRateLimited  = errors.New("tmdb: rate limited")
	ErrUnavailable  = errors.New("tmdb: provider unavailable")
)

// Config configures a Client. Empty URLs mean the public TMDB endpoints.
type Config struct {
	Key          string
	BaseURL      string
	ImageBaseURL string
	CacheDir     string
	HTTP         *http.Client
}

// Client talks to TMDB. Its key may be replaced while requests run.
type Client struct {
	key                       atomic.Pointer[string]
	base, imageBase, cacheDir string
	http                      *http.Client
	lim                       *limiter
	now                       func() time.Time
	sleep                     func(context.Context, time.Duration) error
}

// New returns a Client over cfg.
func New(cfg Config) *Client {
	c := &Client{base: cfg.BaseURL, imageBase: cfg.ImageBaseURL, cacheDir: cfg.CacheDir, http: cfg.HTTP, now: time.Now, sleep: sleepCtx}
	c.SetKey(cfg.Key)
	if c.base == "" {
		c.base = defaultBase
	}
	if c.imageBase == "" {
		c.imageBase = defaultImage
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	c.lim = &limiter{gap: requestGap, now: func() time.Time { return c.now() }, sleep: func(ctx context.Context, d time.Duration) error { return c.sleep(ctx, d) }}
	return c
}

// Enabled reports whether a key is configured.
func (c *Client) Enabled() bool { return c.apiKey() != "" }

// SetKey replaces the key from the next request on; empty disables the client.
func (c *Client) SetKey(key string) { c.key.Store(&key) }

func (c *Client) apiKey() string { return *c.key.Load() }

// CheckKey asks TMDB whether key is valid, without the cache and without
// changing the client's own key: nil when accepted, ErrUnauthorized when
// rejected, any other error when TMDB could not say.
func (c *Client) CheckKey(ctx context.Context, key string) error {
	_, err := c.fetch(ctx, key, "/authentication", nil)
	return err
}

// RefreshAt returns when metadata fetched at now should next be refreshed:
// RefreshEvery plus or minus 20 percent.
func RefreshAt(now time.Time) time.Time {
	return now.Add(RefreshEvery*8/10 + time.Duration(rand.Int64N(int64(RefreshEvery*4/10)))) //nolint:gosec // jitter, not security
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// limiter spaces requests at least gap apart.
type limiter struct {
	mu    sync.Mutex
	gap   time.Duration
	last  time.Time
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

func (l *limiter) wait(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if d := l.gap - l.now().Sub(l.last); !l.last.IsZero() && d > 0 {
		if err := l.sleep(ctx, d); err != nil {
			return err
		}
	}
	l.last = l.now()
	return nil
}

// get fetches path with query, through the cache.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	key := c.apiKey()
	if key == "" {
		return ErrNoKey
	}
	file := c.cacheFile(path, q)
	if b, ok := c.cached(file); ok {
		return json.Unmarshal(b, out)
	}
	b, err := c.fetch(ctx, key, path, q)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%w: bad response: %w", ErrUnavailable, err)
	}
	c.store(file, b)
	return nil
}

func (c *Client) fetch(ctx context.Context, key, path string, q url.Values) ([]byte, error) {
	full := url.Values{"api_key": {key}}
	for k, v := range q {
		full[k] = v
	}
	for attempt := 0; ; attempt++ {
		if err := c.lim.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+full.Encode(), http.NoBody)
		if err != nil {
			return nil, err
		}
		resp, err := c.http.Do(req)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err // the URL it carries includes the API key
			}
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && rerr == nil:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusUnauthorized:
			return nil, ErrUnauthorized
		case resp.StatusCode == http.StatusTooManyRequests:
			if attempt >= maxRetries {
				return nil, ErrRateLimited
			}
			wait := time.Second << attempt
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				wait = time.Duration(s) * time.Second
			}
			wait = min(wait, maxWait)
			logstore.Log("tmdb").Warn(fmt.Sprintf("TMDB returned 429 Too Many Requests; waiting %s (retry %d of %d)", wait, attempt+1, maxRetries))
			if err := c.sleep(ctx, wait); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
		}
	}
}

// The key stays out of the cache key so rotating it keeps the cache.
func (c *Client) cacheFile(path string, q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	_, _ = h.Write([]byte(path))
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "\x00%s=%s", k, strings.Join(q[k], ","))
	}
	return filepath.Join(c.cacheDir, hex.EncodeToString(h.Sum(nil))+".json")
}

func (c *Client) cached(file string) ([]byte, bool) {
	if c.cacheDir == "" {
		return nil, false
	}
	st, err := os.Stat(file)
	if err != nil || c.now().Sub(st.ModTime()) > cacheTTL {
		return nil, false
	}
	b, err := os.ReadFile(file) //nolint:gosec // path is a hash under the cache dir
	return b, err == nil
}

// store is best effort: a failed cache write must not fail the lookup.
func (c *Client) store(file string, b []byte) {
	if c.cacheDir == "" {
		return
	}
	if os.MkdirAll(c.cacheDir, 0o750) == nil {
		_ = os.WriteFile(file, b, 0o600)
	}
}

// Result is one search hit, movie or show.
type Result struct {
	ID            int64
	Title         string
	OriginalTitle string
	ReleaseDate   string
	// OriginCountry is the ISO 3166-1 alpha-2 codes of a show's countries of
	// origin; TMDB's movie results name none.
	OriginCountry []string
}

// Details is what the scanner stores for a movie or a show.
type Details struct {
	ID            int64
	Title         string
	Overview      string
	ReleaseDate   string
	Runtime       int // minutes; for a show, the first episode_run_time
	Certification string
	PosterPath    string
	BackdropPath  string
	Genres        []Genre
	Keywords      []int64  // TMDB keyword ids
	Cast          []Credit // top billed, in billing order
	Crew          []Credit // directors and writers of a movie, creators of a show
	// Collection is the franchise collection a movie belongs to, without
	// its overview; nil when none.
	Collection *CollectionRef
}

// CollectionRef is a TMDB franchise collection.
type CollectionRef struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}

// Collection returns a franchise collection's name, overview and artwork.
func (c *Client) Collection(ctx context.Context, id int64) (*CollectionRef, error) {
	var r CollectionRef
	if err := c.get(ctx, "/collection/"+strconv.FormatInt(id, 10), nil, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// Genre is a provider genre.
type Genre struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// keywordIDs are the ids of TMDB keywords, which have a genre's shape.
func keywordIDs(ks []Genre) []int64 {
	out := make([]int64, len(ks))
	for i, k := range ks {
		out[i] = k.ID
	}
	return out
}

// Credit is one person's part in a movie or show.
type Credit struct {
	PersonID    int64
	Name        string
	ProfilePath string
	Character   string
	Job         string
	Department  string
}

// maxCast bounds how much of a cast is stored; the rest is rarely shown.
const maxCast = 20

// keyJobs is the movie crew worth listing on a details page.
var keyJobs = map[string]bool{"Director": true, "Screenplay": true, "Writer": true}

type rawPerson struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ProfilePath string `json:"profile_path"`
}

func (p rawPerson) credit() Credit {
	return Credit{PersonID: p.ID, Name: p.Name, ProfilePath: p.ProfilePath}
}

// Episode is one episode of a season listing.
type Episode struct {
	Number   int
	Title    string
	Overview string
	Runtime  int
	StillURL string
}

type rawResult struct {
	ID            int64    `json:"id"`
	Title         string   `json:"title"`
	Name          string   `json:"name"`
	OriginalTitle string   `json:"original_title"`
	OriginalName  string   `json:"original_name"`
	ReleaseDate   string   `json:"release_date"`
	FirstAirDate  string   `json:"first_air_date"`
	OriginCountry []string `json:"origin_country"`
}

func (r rawResult) result() Result {
	return Result{ID: r.ID, Title: cmp.Or(r.Title, r.Name), OriginalTitle: cmp.Or(r.OriginalTitle, r.OriginalName),
		ReleaseDate: cmp.Or(r.ReleaseDate, r.FirstAirDate), OriginCountry: r.OriginCountry}
}

func results(raw []rawResult) []Result {
	out := make([]Result, len(raw))
	for i, x := range raw {
		out[i] = x.result()
	}
	return out
}

// External id namespaces Find resolves.
const (
	TVDB = "tvdb_id"
	IMDB = "imdb_id"
)

// Find lists the movies and the shows TMDB files under another provider's
// id; both are empty for an id it does not know.
func (c *Client) Find(ctx context.Context, source, id string) (movies, shows []Result, err error) {
	var r struct {
		Movies []rawResult `json:"movie_results"`
		Shows  []rawResult `json:"tv_results"`
	}
	if err := c.get(ctx, "/find/"+url.PathEscape(id), url.Values{"external_source": {source}}, &r); err != nil {
		return nil, nil, err
	}
	return results(r.Movies), results(r.Shows), nil
}

func (c *Client) search(ctx context.Context, kind, title string, year *int) ([]Result, error) {
	q := url.Values{"query": {title}}
	if year != nil {
		if kind == "movie" {
			q.Set("year", strconv.Itoa(*year))
		} else {
			q.Set("first_air_date_year", strconv.Itoa(*year))
		}
	}
	var r struct {
		Results []rawResult `json:"results"`
	}
	if err := c.get(ctx, "/search/"+kind, q, &r); err != nil {
		return nil, err
	}
	return results(r.Results), nil
}

// SearchMovie searches movies by title and optional release year.
func (c *Client) SearchMovie(ctx context.Context, title string, year *int) ([]Result, error) {
	return c.search(ctx, "movie", title, year)
}

// SearchTV searches shows by title and optional first-air year.
func (c *Client) SearchTV(ctx context.Context, title string, year *int) ([]Result, error) {
	return c.search(ctx, "tv", title, year)
}

// Query is what a title's names say it is, to pick a search result by.
type Query struct {
	Title string
	Year  *int
	// Country is an ISO 3166-1 alpha-2 code, "" for none stated.
	Country string
	// TV matches a show: its Year is the premiere year, which is exact.
	TV bool
}

// MovieYearSlack is how far a movie's release year may be from the year in
// its name, which festival and regional releases put a year apart. A
// show's premiere year has no slack.
const MovieYearSlack = 1

// Match picks the one result whose title equals the query's (ignoring case
// and punctuation, and accents and "&" only when no title equals it
// without), that is of its country when one is stated, and whose year fits
// when one is known, a movie's nearest year winning. Results that fit
// equally well are returned as tied instead, in no order that matters: a
// wrong match is worse than none, so there is no fuzzy fallback and no
// first-listed winner.
func Match(rs []Result, q Query) (hit *Result, tied []Result) {
	var fit []Result
	for _, key := range []func(string) string{parse.Norm, parse.Fold} {
		best := MovieYearSlack + 1
		for _, r := range rs {
			if key(r.Title) != key(q.Title) && key(r.OriginalTitle) != key(q.Title) {
				continue
			}
			if q.Country != "" && !slices.Contains(r.OriginCountry, q.Country) {
				continue
			}
			diff := 0
			if q.Year != nil {
				y, err := strconv.Atoi(strings.SplitN(r.ReleaseDate, "-", 2)[0])
				if err != nil {
					continue
				}
				if diff = max(y-*q.Year, *q.Year-y); diff > MovieYearSlack || q.TV && diff != 0 {
					continue
				}
			}
			if diff < best {
				fit, best = nil, diff
			}
			if diff == best {
				fit = append(fit, r)
			}
		}
		if fit != nil {
			break
		}
	}
	if len(fit) == 1 {
		return &fit[0], nil
	}
	return nil, fit
}

// Movie returns movie details with its US certification.
func (c *Client) Movie(ctx context.Context, id int64) (*Details, error) {
	var r struct {
		rawResult
		Overview     string         `json:"overview"`
		Runtime      int            `json:"runtime"`
		PosterPath   string         `json:"poster_path"`
		BackdropPath string         `json:"backdrop_path"`
		Genres       []Genre        `json:"genres"`
		Collection   *CollectionRef `json:"belongs_to_collection"`
		Keywords     struct {
			Keywords []Genre `json:"keywords"`
		} `json:"keywords"`
		Credits struct {
			Cast []struct {
				rawPerson
				Character string `json:"character"`
			} `json:"cast"`
			Crew []struct {
				rawPerson
				Job        string `json:"job"`
				Department string `json:"department"`
			} `json:"crew"`
		} `json:"credits"`
		ReleaseDates struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Dates   []struct {
					Certification string `json:"certification"`
					Type          int    `json:"type"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}
	if err := c.get(ctx, "/movie/"+strconv.FormatInt(id, 10), url.Values{"append_to_response": {"release_dates,credits,keywords"}}, &r); err != nil {
		return nil, err
	}
	d := &Details{ID: r.ID, Title: r.result().Title, Overview: r.Overview, ReleaseDate: r.ReleaseDate, Runtime: r.Runtime, PosterPath: r.PosterPath,
		BackdropPath: r.BackdropPath, Genres: r.Genres, Keywords: keywordIDs(r.Keywords.Keywords), Collection: r.Collection}
	for _, c := range r.Credits.Cast[:min(len(r.Credits.Cast), maxCast)] {
		cr := c.credit()
		cr.Character = c.Character
		d.Cast = append(d.Cast, cr)
	}
	for _, c := range r.Credits.Crew {
		if keyJobs[c.Job] {
			cr := c.credit()
			cr.Job, cr.Department = c.Job, c.Department
			d.Crew = append(d.Crew, cr)
		}
	}
	for _, c := range r.ReleaseDates.Results {
		if c.Country != "US" {
			continue
		}
		// Theatrical (3) wins; otherwise any non-empty certification.
		for _, rd := range c.Dates {
			if rd.Certification != "" && (d.Certification == "" || rd.Type == 3) {
				d.Certification = rd.Certification
			}
		}
	}
	return d, nil
}

// TV returns show details with its US content rating.
func (c *Client) TV(ctx context.Context, id int64) (*Details, error) {
	var r struct {
		rawResult
		Overview     string      `json:"overview"`
		RunTimes     []int       `json:"episode_run_time"`
		PosterPath   string      `json:"poster_path"`
		BackdropPath string      `json:"backdrop_path"`
		Genres       []Genre     `json:"genres"`
		CreatedBy    []rawPerson `json:"created_by"`
		Keywords     struct {
			Results []Genre `json:"results"`
		} `json:"keywords"`
		Credits struct {
			Cast []struct {
				rawPerson
				Roles []struct {
					Character string `json:"character"`
				} `json:"roles"`
			} `json:"cast"`
		} `json:"aggregate_credits"`
		Ratings struct {
			Results []struct {
				Country string `json:"iso_3166_1"`
				Rating  string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
	}
	if err := c.get(ctx, "/tv/"+strconv.FormatInt(id, 10), url.Values{"append_to_response": {"content_ratings,aggregate_credits,keywords"}}, &r); err != nil {
		return nil, err
	}
	d := &Details{ID: r.ID, Title: r.result().Title, Overview: r.Overview, ReleaseDate: r.FirstAirDate, PosterPath: r.PosterPath,
		BackdropPath: r.BackdropPath, Genres: r.Genres, Keywords: keywordIDs(r.Keywords.Results)}
	for _, c := range r.Credits.Cast[:min(len(r.Credits.Cast), maxCast)] {
		cr := c.credit()
		if len(c.Roles) > 0 {
			cr.Character = c.Roles[0].Character
		}
		d.Cast = append(d.Cast, cr)
	}
	for _, p := range r.CreatedBy {
		cr := p.credit()
		cr.Job = "Creator"
		d.Crew = append(d.Crew, cr)
	}
	if len(r.RunTimes) > 0 {
		d.Runtime = r.RunTimes[0]
	}
	for _, cr := range r.Ratings.Results {
		if cr.Country == "US" {
			d.Certification = cr.Rating
		}
	}
	return d, nil
}

// Season lists a season's episodes.
func (c *Client) Season(ctx context.Context, tvID int64, season int) ([]Episode, error) {
	var r struct {
		Episodes []struct {
			Number    int    `json:"episode_number"`
			Name      string `json:"name"`
			Overview  string `json:"overview"`
			Runtime   int    `json:"runtime"`
			StillPath string `json:"still_path"`
		} `json:"episodes"`
	}
	if err := c.get(ctx, fmt.Sprintf("/tv/%d/season/%d", tvID, season), nil, &r); err != nil {
		return nil, err
	}
	out := make([]Episode, len(r.Episodes))
	for i, e := range r.Episodes {
		out[i] = Episode{Number: e.Number, Title: e.Name, Overview: e.Overview, Runtime: e.Runtime, StillURL: e.StillPath}
	}
	return out, nil
}

var imagePath = regexp.MustCompile(`^/[A-Za-z0-9_-]+\.(jpg|png)$`)

// CacheImage downloads a poster or backdrop into dir/size/ unless already
// there, returning its local path. Only plain TMDB image names are accepted
// so a hostile response cannot write outside dir.
func (c *Client) CacheImage(ctx context.Context, dir, size, path string) (string, error) {
	if !imagePath.MatchString(path) || !regexp.MustCompile(`^(w\d+|original)$`).MatchString(size) {
		return "", fmt.Errorf("tmdb: bad image reference %q %q", size, path)
	}
	dest := filepath.Join(dir, size, path)
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.imageBase+"/"+size+path, http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tmdb: image %s: status %d", path, resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", err
	}
	return dest, atomicfile.Write(dest, io.LimitReader(resp.Body, 32<<20))
}
