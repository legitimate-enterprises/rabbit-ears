// Package musicbrainz is the MusicBrainz web service client, with album
// covers from the Cover Art Archive: one request a second, a User-Agent that
// names a contact as MusicBrainz requires, backoff on 503, and an on-disk
// response cache, so a scan never hammers either service.
package musicbrainz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/atomicfile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/parse"
)

// UserAgent identifies the server to MusicBrainz with a contact address.
const UserAgent = "RabbitEars/1 ( dev@legitimate.enterprises )"

const (
	defaultBase  = "https://musicbrainz.org/ws/2"
	defaultCover = "https://coverartarchive.org"
	cacheTTL     = 24 * time.Hour
	maxRetries   = 3
	// MusicBrainz allows one request a second per client address.
	requestGap = time.Second
)

// Errors a caller may branch on.
var (
	ErrNotFound    = errors.New("musicbrainz: not found")
	ErrUnavailable = errors.New("musicbrainz: provider unavailable")
)

// Config configures a Client. Empty URLs mean the public services.
type Config struct {
	BaseURL  string
	CoverURL string
	CacheDir string
	HTTP     *http.Client
}

// Client talks to MusicBrainz and the Cover Art Archive.
type Client struct {
	base, cover, cacheDir string
	http                  *http.Client
	mu                    sync.Mutex // spaces every request at least requestGap apart
	last                  time.Time
	now                   func() time.Time
	sleep                 func(context.Context, time.Duration) error
}

// New returns a Client over cfg.
func New(cfg Config) *Client {
	c := &Client{base: cfg.BaseURL, cover: cfg.CoverURL, cacheDir: cfg.CacheDir, http: cfg.HTTP, now: time.Now, sleep: sleepCtx}
	if c.base == "" {
		c.base = defaultBase
	}
	if c.cover == "" {
		c.cover = defaultCover
	}
	if c.http == nil {
		c.http = &http.Client{Timeout: 30 * time.Second}
	}
	return c
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

func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := requestGap - c.now().Sub(c.last); !c.last.IsZero() && d > 0 {
		if err := c.sleep(ctx, d); err != nil {
			return err
		}
	}
	c.last = c.now()
	return nil
}

// do sends one GET under the rate limit, retrying 503 and 429 with backoff.
func (c *Client) do(ctx context.Context, u, accept string) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		if err := c.wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", accept)
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK && rerr == nil:
			return body, nil
		case resp.StatusCode == http.StatusNotFound:
			return nil, ErrNotFound
		case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
			if attempt >= maxRetries {
				return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
			}
			wait := time.Second << attempt
			logstore.Log("musicbrainz").Warn(fmt.Sprintf("MusicBrainz returned %d; waiting %s (retry %d of %d)", resp.StatusCode, wait, attempt+1, maxRetries))
			if err := c.sleep(ctx, wait); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
		}
	}
}

// get fetches a web service path with query through the cache.
func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	q.Set("fmt", "json")
	u := c.base + path + "?" + q.Encode()
	h := sha256.Sum256([]byte(path + "?" + q.Encode()))
	file := filepath.Join(c.cacheDir, hex.EncodeToString(h[:])+".json")
	if c.cacheDir != "" {
		if st, err := os.Stat(file); err == nil && c.now().Sub(st.ModTime()) <= cacheTTL {
			if b, err := os.ReadFile(file); err == nil { //nolint:gosec // path is a hash under the cache dir
				return json.Unmarshal(b, out)
			}
		}
	}
	b, err := c.do(ctx, u, "application/json")
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%w: bad response: %w", ErrUnavailable, err)
	}
	// Best effort: a failed cache write must not fail the lookup.
	if c.cacheDir != "" && os.MkdirAll(c.cacheDir, 0o750) == nil {
		_ = os.WriteFile(file, b, 0o600)
	}
	return nil
}

// Release is one release search hit.
type Release struct {
	ID         string
	Title      string
	Date       string // as MusicBrainz gives it: YYYY, YYYY-MM or YYYY-MM-DD
	Score      int
	ArtistID   string   // the first credited artist
	ArtistName string   // every credited name joined as credited
	Artists    []string // each credited name
	GroupID    string   // the release group: the album whatever its edition
}

type credit struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
	Artist     struct {
		ID string `json:"id"`
	} `json:"artist"`
}

// quote is a Lucene phrase of s.
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// SearchRelease searches releases by title and credited artist. It reads
// 25 hits: the first 10 of a much-reissued album can all be pressings of
// other years.
func (c *Client) SearchRelease(ctx context.Context, artist, album string) ([]Release, error) {
	var r struct {
		Releases []struct {
			ID    string `json:"id"`
			Score int    `json:"score"`
			Title string `json:"title"`
			Date  string `json:"date"`
			Group struct {
				ID string `json:"id"`
			} `json:"release-group"`
			Credits []credit `json:"artist-credit"`
		} `json:"releases"`
	}
	q := url.Values{"query": {"release:" + quote(album) + " AND artist:" + quote(artist)}, "limit": {"25"}}
	if err := c.get(ctx, "/release", q, &r); err != nil {
		return nil, err
	}
	out := make([]Release, len(r.Releases))
	for i, x := range r.Releases {
		out[i] = Release{ID: x.ID, Title: x.Title, Date: x.Date, Score: x.Score, GroupID: x.Group.ID}
		for j, cr := range x.Credits {
			if j == 0 {
				out[i].ArtistID = cr.Artist.ID
			}
			out[i].ArtistName += cr.Name + cr.JoinPhrase
			out[i].Artists = append(out[i].Artists, cr.Name)
		}
	}
	return out, nil
}

// Find searches for the album as stored and returns its match, or nil. On
// a miss it widens the search, to the title without each decoration and
// to the first artist named, until the hits so far hold a match.
func Find(ctx context.Context, search func(ctx context.Context, artist, album string) ([]Release, error), artist, album string, year *int) (*Release, error) {
	var hits []Release
	for _, t := range titles(album, artist) {
		for _, a := range slices.Compact([]string{artist, nameBreak.Split(artist, 2)[0]}) {
			if a == "" {
				continue
			}
			rs, err := search(ctx, a, t)
			if err != nil {
				return nil, err
			}
			hits = append(hits, rs...)
			if r := Match(hits, artist, album, year); r != nil {
				return r, nil
			}
		}
	}
	return nil, nil
}

// What a music store adds to an album's title and MusicBrainz leaves out:
// the " - Single" or " - EP" it files short releases under, and edition
// or soundtrack wording in brackets or after the last colon or dash. A
// remix or live set is another album, so those words are not among them.
const editionWords = `(?i:deluxe|expanded|remaster|collector|soundtrack|score|motion picture|feat\.|bonus)`

var (
	storeSuffix    = regexp.MustCompile(`\s+-\s+(Single|EP)$`)
	editionBracket = regexp.MustCompile(`\s*[(\[][^)\]]*` + editionWords + `[^)\]]*[)\]]`)
	editionTail    = regexp.MustCompile(`\s*[:\-–]\s*[^:\-–]*` + editionWords + `[^:\-–]*$`)
	// nameBreak parts the names of an artist credit written as one string.
	nameBreak = regexp.MustCompile(`\s*(?:,|&| and | feat\.? )\s*`)
)

func stripEdition(t string) string {
	return strings.TrimSpace(editionTail.ReplaceAllString(editionBracket.ReplaceAllString(t, ""), ""))
}

// titles is the album title as stored, then with each decoration removed
// in turn: the store's suffix, edition wording, the artist before it.
func titles(album, artist string) []string {
	out := []string{album}
	afterArtist := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(artist) + `\s*[:\-–]\s*`)
	for _, strip := range []func(string) string{
		func(t string) string { return storeSuffix.ReplaceAllString(t, "") },
		stripEdition,
		func(t string) string { return afterArtist.ReplaceAllString(t, "") },
	} {
		if t := strings.TrimSpace(strip(out[len(out)-1])); t != "" && t != out[len(out)-1] {
			out = append(out, t)
		}
	}
	return out
}

// sameArtist reports whether the release is credited to artist, or to
// some of the names artist holds, or to those and more.
func sameArtist(r *Release, artist string) bool {
	if parse.Fold(r.ArtistName) == parse.Fold(artist) {
		return true
	}
	lib, mb := map[string]bool{}, map[string]bool{}
	for _, n := range nameBreak.Split(artist, -1) {
		lib[parse.Fold(n)] = true
	}
	for _, n := range r.Artists {
		mb[parse.Fold(n)] = true
	}
	delete(lib, "")
	delete(mb, "")
	within := func(a, b map[string]bool) bool {
		for n := range a {
			if !b[n] {
				return false
			}
		}
		return len(a) > 0
	}
	return within(lib, mb) || within(mb, lib)
}

func near(r *Release, year int) bool {
	y, err := strconv.Atoi(strings.SplitN(r.Date, "-", 2)[0])
	return err == nil && y-year <= 1 && year-y <= 1
}

// Match picks the best-scored release whose title and credited artist equal
// these (ignoring case and punctuation) and, when year is known, whose year
// is within one of it. When none is, it sees through a store's naming as
// loose does. A wrong match is worse than none.
func Match(rs []Release, artist, album string, year *int) *Release {
	var best *Release
	for i := range rs {
		r := &rs[i]
		if parse.Norm(r.Title) != parse.Norm(album) || parse.Norm(r.ArtistName) != parse.Norm(artist) || year != nil && !near(r, *year) {
			continue
		}
		if best == nil || r.Score > best.Score {
			best = r
		}
	}
	if best != nil {
		return best
	}
	return loose(rs, artist, album, year)
}

// loose picks the best-scored release that fits the least-decorated title
// any release fits, so a release titled as stored beats the plain edition
// of the same album. Accents and "&" for "and" are ignored, and the
// credited names may be some of the artist's or more. The year only picks
// between albums: it narrows the fits to those within one of it when any
// are. The fits left must all be one release group, or nothing is picked.
func loose(rs []Release, artist, album string, year *int) *Release {
	for _, t := range titles(album, artist) {
		var fits, close []*Release
		for i := range rs {
			r := &rs[i]
			if parse.Fold(r.Title) != parse.Fold(t) && parse.Fold(stripEdition(r.Title)) != parse.Fold(t) || !sameArtist(r, artist) {
				continue
			}
			fits = append(fits, r)
			if year != nil && near(r, *year) {
				close = append(close, r)
			}
		}
		if len(fits) == 0 {
			continue
		}
		if len(close) > 0 {
			fits = close
		}
		best := fits[0]
		for _, r := range fits[1:] {
			if r.GroupID != best.GroupID {
				return nil
			}
			if r.Score > best.Score {
				best = r
			}
		}
		return best
	}
	return nil
}

type genres []struct {
	Name string `json:"name"`
}

// names title-cases MusicBrainz's lower-case genre names, dropping repeats.
func names(lists ...genres) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, g := range l {
			words := strings.Fields(g.Name)
			for i, w := range words {
				r := []rune(w)
				r[0] = unicode.ToUpper(r[0])
				words[i] = string(r)
			}
			n := strings.Join(words, " ")
			if n != "" && !seen[strings.ToLower(n)] {
				seen[strings.ToLower(n)] = true
				out = append(out, n)
			}
		}
	}
	return out
}

// Details is what the scanner stores about a release, a release group or
// an artist.
type Details struct {
	ID     string
	Genres []string
	// GroupID is a release's release group, and ArtistID a release's or a
	// release group's first credited artist.
	GroupID, ArtistID string
}

var mbid = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidID reports whether id has the form of a MusicBrainz id.
func ValidID(id string) bool { return mbid.MatchString(id) }

func checkID(id string) error {
	if !mbid.MatchString(id) {
		return fmt.Errorf("musicbrainz: bad id %q", id)
	}
	return nil
}

// Release returns a release's genres with its release group's.
func (c *Client) Release(ctx context.Context, id string) (*Details, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var r struct {
		ID     string `json:"id"`
		Genres genres `json:"genres"`
		Group  struct {
			ID     string `json:"id"`
			Genres genres `json:"genres"`
		} `json:"release-group"`
		Credits []credit `json:"artist-credit"`
	}
	if err := c.get(ctx, "/release/"+id, url.Values{"inc": {"genres release-groups artist-credits"}}, &r); err != nil {
		return nil, err
	}
	return &Details{ID: r.ID, Genres: names(r.Genres, r.Group.Genres), GroupID: r.Group.ID, ArtistID: first(r.Credits)}, nil
}

func first(cs []credit) string {
	if len(cs) == 0 {
		return ""
	}
	return cs[0].Artist.ID
}

// ReleaseGroup returns a release group's genres: an album whatever its
// edition. A group is never a release; its id is only ever sent here.
func (c *Client) ReleaseGroup(ctx context.Context, id string) (*Details, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var r struct {
		ID      string   `json:"id"`
		Genres  genres   `json:"genres"`
		Credits []credit `json:"artist-credit"`
	}
	if err := c.get(ctx, "/release-group/"+id, url.Values{"inc": {"genres artist-credits"}}, &r); err != nil {
		return nil, err
	}
	return &Details{ID: r.ID, Genres: names(r.Genres), GroupID: r.ID, ArtistID: first(r.Credits)}, nil
}

// Artist returns an artist's genres.
func (c *Client) Artist(ctx context.Context, id string) (*Details, error) {
	if err := checkID(id); err != nil {
		return nil, err
	}
	var r struct {
		ID     string `json:"id"`
		Genres genres `json:"genres"`
	}
	if err := c.get(ctx, "/artist/"+id, url.Values{"inc": {"genres"}}, &r); err != nil {
		return nil, err
	}
	return &Details{ID: r.ID, Genres: names(r.Genres)}, nil
}

// CacheCover downloads a release's front cover, 500 pixels wide, into
// dir/caa/ unless already there, returning its local path; ErrNotFound when
// the archive has none.
func (c *Client) CacheCover(ctx context.Context, dir, releaseID string) (string, error) {
	if err := checkID(releaseID); err != nil {
		return "", err
	}
	dest := filepath.Join(dir, "caa", releaseID+".jpg")
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	b, err := c.do(ctx, c.cover+"/release/"+releaseID+"/front-500", "image/*")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", err
	}
	return dest, atomicfile.Write(dest, bytes.NewReader(b))
}
