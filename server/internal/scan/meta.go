package scan

import (
	"context"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
)

// RetryDelay is how long an item waits after a provider failure.
const RetryDelay = time.Hour

const batchLimit = 1000

// run is one batch of lookups; after the first provider failure the rest of
// the batch is deferred without more calls, so an outage costs one timeout.
type run struct {
	outage bool
	failed int // lookups that hit a provider error or were deferred by an outage
}

// MatchLibrary fetches metadata for the library's movies and shows that have
// none, and fills in episodes that arrived after their show was matched. It
// returns how many lookups failed.
func (s *Scanner) MatchLibrary(ctx context.Context, lib *library.Library) (int, error) {
	if s.Provider == nil || !s.Provider.Enabled() || !s.remote() {
		return 0, nil
	}
	r := &run{}
	// Every fetch writes the item's metadata row, so each batch is new items.
	for {
		items, err := s.Items.WithoutMetadata(lib.ID, batchLimit)
		if err != nil {
			return r.failed, err
		}
		for _, it := range items {
			if err := s.fetch(ctx, r, it, nil); err != nil {
				return r.failed, err
			}
		}
		if len(items) < batchLimit {
			break
		}
	}
	if err := s.checkIdentities(ctx, r, lib.ID); err != nil {
		return r.failed, err
	}
	shows, err := s.Items.List(item.Filter{LibraryID: lib.ID, Kind: item.Show, Limit: 100000})
	if err != nil {
		return r.failed, err
	}
	for _, show := range shows {
		if err := s.fillNewEpisodes(ctx, r, show); err != nil {
			return r.failed, err
		}
	}
	return r.failed, nil
}

// RetryDue re-fetches up to limit items whose retry or refresh time has
// come, across all libraries: movies and shows from TMDB, albums from
// MusicBrainz, channel avatars from YouTube, and reports whether more may
// be due. Each provider's outage defers only its own items, and an item
// whose provider is off is deferred. Then every library's items no
// provider was asked about yet are looked up, as its scan would, and each
// Movies library's movies of one TMDB id are folded into one. While the
// metadata settings use no remote data it does nothing.
func (s *Scanner) RetryDue(ctx context.Context, limit int) (bool, error) {
	video := s.Provider != nil && s.Provider.Enabled()
	if !video && s.Music == nil && s.Avatars == nil || !s.remote() {
		return false, nil
	}
	due, err := s.Items.DueForRetry(s.Now(), limit)
	if err != nil {
		return false, err
	}
	tv, music, channels := &run{}, &run{}, &run{}
	for _, m := range due {
		it, err := s.Items.Get(m.ItemID)
		if err != nil {
			continue
		}
		switch {
		case it.Kind == item.Album && s.Music != nil:
			err = s.fetchAlbum(ctx, music, it, m, false)
		case (it.Kind == item.Movie || it.Kind == item.Show) && video:
			err = s.fetch(ctx, tv, it, m)
		case it.Kind == item.Channel && s.Avatars != nil:
			if err = s.fetchAvatar(ctx, channels, it, m); err == nil {
				err = s.Items.SetMetadata(m)
			}
		default:
			err = s.deferFetch(it, m, s.Now().UTC())
		}
		if err != nil {
			return false, err
		}
	}
	libs, err := s.Libraries.List()
	if err != nil {
		return false, err
	}
	for _, l := range libs {
		if _, err := s.matcher(l)(ctx, l); err != nil {
			return false, err
		}
		if l.Kind != library.Movies {
			continue
		}
		if err := s.foldMovies(l.ID, nil); err != nil {
			return false, err
		}
	}
	return len(due) == limit, nil
}

// fetch looks one movie or show up and stores the outcome. Provider errors
// are recorded as a retry on the item and never returned; only cancellation is.
func (s *Scanner) fetch(ctx context.Context, r *run, it *item.Item, prev *item.Metadata) error {
	return s.fetchAs(ctx, r, it, prev, false)
}

// fetchAs is fetch; with check, a title whose names leave its match as it
// is has only that recorded, and is not fetched again.
func (s *Scanner) fetchAs(ctx context.Context, r *run, it *item.Item, prev *item.Metadata, check bool) error {
	if prev == nil {
		if p, err := s.Items.Remote(it.ID); err == nil {
			prev = p
		}
	}
	now := s.Now().UTC()
	if r.outage {
		r.failed++
		return s.deferFetch(it, prev, now)
	}
	m, d, stamp, err := s.lookup(ctx, it, prev, now, check)
	switch {
	case err == nil:
	case errors.Is(err, errKept):
		return s.Items.SetIdentityStamp(it.ID, stamp)
	case errors.Is(err, tmdb.ErrNotFound) || errors.Is(err, errNoMatch):
		next := tmdb.RefreshAt(now)
		if prev != nil && prev.Status == item.Matched {
			// The title vanished upstream; keep what we have. The answer is
			// a fetch, so episodes already here wait for the retry.
			prev.RetryAt, prev.FetchedAt = &next, &now
			return s.Items.SetMetadata(prev)
		}
		if err := s.Items.SetMetadata(&item.Metadata{ItemID: it.ID, Status: item.Unmatched, RetryAt: &next}); err != nil {
			return err
		}
		return s.Items.SetIdentityStamp(it.ID, stamp)
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		logstore.Log("tmdb").Warn("Metadata lookup failed; will retry", "item_id", it.ID, "title", it.ParsedTitle, "err", err)
		r.outage = true
		r.failed++
		return s.deferFetch(it, prev, now)
	}
	if err := s.Items.SetMetadata(m); err != nil {
		return err
	}
	if err := s.Items.SetIdentityStamp(it.ID, stamp); err != nil {
		return err
	}
	if err := s.storeCollection(ctx, it, d); err != nil {
		return err
	}
	if err := s.Items.SetKeywords(it.ID, d.Keywords); err != nil {
		return err
	}
	return s.storeGenresAndCredits(it.ID, d)
}

// storeCollection records the TMDB collection a movie is in, or that it is
// in none. The collection's own lookup is best effort: when it fails, the
// collection keeps the name and artwork the movie named and any overview
// stored before, and the movie's next refresh asks again.
func (s *Scanner) storeCollection(ctx context.Context, it *item.Item, d *tmdb.Details) error {
	if s.Collections == nil || it.Kind != item.Movie {
		return nil
	}
	if d.Collection == nil {
		return s.Collections.SetTMDBMovie(it.ID, 0)
	}
	ref := *d.Collection
	full, err := s.Provider.Collection(ctx, ref.ID)
	switch {
	case err == nil:
		ref = *full
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		logstore.Log("tmdb").Warn("Collection lookup failed; keeping what the movie named", "item_id", it.ID, "collection", ref.ID, "err", err)
	}
	c := &collection.Collection{TMDBID: &ref.ID, Name: ref.Name, Overview: ref.Overview, PosterPath: ref.PosterPath, BackdropPath: ref.BackdropPath}
	if err := s.Collections.PutTMDB(c); err != nil {
		return err
	}
	s.cacheImages(ctx, ref.PosterPath, ref.BackdropPath)
	return s.Collections.SetTMDBMovie(it.ID, c.ID)
}

func (s *Scanner) storeGenresAndCredits(itemID int64, d *tmdb.Details) error {
	gs := make([]genre.Genre, len(d.Genres))
	for i, g := range d.Genres {
		gs[i] = genre.Genre{Name: g.Name, TMDBID: &g.ID}
	}
	if err := s.Genres.SetForItem(itemID, genre.Remote, gs); err != nil {
		return err
	}
	var cs []person.Credit
	for _, k := range []struct {
		kind    string
		credits []tmdb.Credit
	}{{person.Cast, d.Cast}, {person.Crew, d.Crew}} {
		for _, c := range k.credits {
			p := &person.Person{TMDBID: &c.PersonID, Name: c.Name, ProfilePath: c.ProfilePath}
			if err := s.People.Upsert(p); err != nil {
				return err
			}
			cs = append(cs, person.Credit{PersonID: p.ID, Kind: k.kind, Character: c.Character, Job: c.Job, Department: c.Department})
		}
	}
	return s.People.SetCredits(itemID, cs)
}

func (s *Scanner) deferFetch(it *item.Item, prev *item.Metadata, now time.Time) error {
	next := now.Add(RetryDelay)
	if prev == nil {
		prev = &item.Metadata{ItemID: it.ID, Status: item.Unmatched}
	}
	prev.RetryAt = &next
	return s.Items.SetMetadata(prev)
}

var (
	errNoMatch = errors.New("no provider match")
	// errKept says a check of a title's names left its match as it was.
	errKept = errors.New("match kept")
)

// rematch settles the review rv that reading a title's names raised, a
// match recorded with none already judged against them by the caller, and
// returns whether to take what they identify and the stamp to record;
// errNoMatch for a title left without a match. Where the line runs
// between applying what the names state and asking an admin:
//   - A title without a match takes the one its names identify.
//   - A match recorded with the names it was made from is the scanner's
//     own reading of them: while they say the same it is only refreshed,
//     and when they change (a folder renamed to carry an id, a year or a
//     country) it is made again from what they say now.
//   - A match recorded with no names, made by hand or before they were
//     kept, is someone's decision, not a reading: it is checked against
//     the names and never replaced by them. Names that identify another
//     title, or cannot identify one, leave it in place and raise a review.
//   - Names that identify nothing (malformed, unknown, disagreeing or tied)
//     never fall back to a looser reading; the title keeps the match it
//     has, or none, and is left for review.
//
// A matched title's names are read only with check, which each scan and
// each metadata run asks of every title whose stamp differs; a refresh
// that comes due between them fetches the match it has.
func (s *Scanner) rematch(itemID int64, matched bool, was string, st stated, rv *review.Review) (take bool, stamp string, err error) {
	byHand, stamp := matched && was == "", st.stamp
	switch {
	case rv == nil:
	case byHand:
		stamp = ""
	case matched:
		stamp = was
	}
	if err := s.settle(itemID, rv); err != nil {
		return false, "", err
	}
	if rv != nil && !matched {
		return false, stamp, errNoMatch
	}
	return rv == nil && !byHand, stamp, nil
}

// lookup fetches the title a movie or show is, and returns with it the
// identity stamp to record, as rematch draws the line.
func (s *Scanner) lookup(ctx context.Context, it *item.Item, prev *item.Metadata, now time.Time, check bool) (*item.Metadata, *tmdb.Details, string, error) {
	st, err := s.statedVideo(it)
	if err != nil {
		return nil, nil, "", err
	}
	was, err := s.Items.IdentityStamp(it.ID)
	if err != nil {
		return nil, nil, "", err
	}
	matched := prev != nil && prev.Status == item.Matched && prev.ProviderID != nil
	id, stamp := int64(0), st.stamp
	if matched {
		id = *prev.ProviderID
	}
	if matched && !check {
		stamp = was
	}
	if !matched || check && was != st.stamp {
		want, rv, err := s.identify(ctx, it, st)
		if err != nil {
			return nil, nil, "", err
		}
		if matched && was == "" {
			switch {
			case rv == nil && want.ID != id:
				rv = s.review(it, review.ConflictingIdentity, append(append([]review.Evidence{matchedTo(prev)}, st.said...), candidates([]tmdb.Result{*want})...))
			case rv != nil && rv.Reason == review.NoMatch:
				rv = nil // nothing found by name is nothing against the match
			case rv != nil:
				rv.Evidence = append([]review.Evidence{matchedTo(prev)}, rv.Evidence...)
			}
		}
		var take bool
		if take, stamp, err = s.rematch(it.ID, matched, was, st, rv); err != nil {
			return nil, nil, stamp, err
		}
		if take {
			id = want.ID
		}
		if matched && id == *prev.ProviderID {
			return nil, nil, stamp, errKept
		}
	}
	var d *tmdb.Details
	runtime := 0
	if it.Kind == item.Movie {
		d, err = s.Provider.Movie(ctx, id)
	} else if d, err = s.Provider.TV(ctx, id); err == nil {
		if matched && id != *prev.ProviderID {
			// The episodes' rows describe the show this was matched to before.
			err = s.Items.DeleteEpisodeMetadata(it.ID)
		}
		if err == nil {
			runtime, err = s.fillEpisodes(ctx, it, d, now)
		}
	}
	if errors.Is(err, tmdb.ErrNotFound) && id == st.TMDB && (!matched || id != *prev.ProviderID) {
		// The id the names state is none TMDB has.
		if err := s.settle(it.ID, s.review(it, review.InvalidIdentity, st.said)); err != nil {
			return nil, nil, "", err
		}
		return nil, nil, stamp, errNoMatch
	}
	if err != nil {
		return nil, nil, "", err
	}
	if it.Kind == item.Movie {
		runtime = d.Runtime
	} else if runtime == 0 {
		runtime = TypicalLength([]int{d.Runtime})
	}
	m := &item.Metadata{ItemID: it.ID, ProviderID: &id, Status: item.Matched, Title: d.Title, Overview: d.Overview,
		ReleaseDate: d.ReleaseDate, ContentRating: d.Certification, FetchedAt: &now}
	if runtime > 0 {
		m.RuntimeMinutes = &runtime
	}
	refresh := tmdb.RefreshAt(now)
	m.RetryAt = &refresh
	m.PosterPath, m.BackdropPath = d.PosterPath, d.BackdropPath
	s.cacheImages(ctx, d.PosterPath, d.BackdropPath)
	return m, d, stamp, nil
}

func (s *Scanner) cacheImages(ctx context.Context, poster, backdrop string) {
	if s.ImageDir == "" {
		return
	}
	for _, im := range []struct{ size, path string }{{"w500", poster}, {"w1280", backdrop}} {
		if im.path == "" {
			continue
		}
		if _, err := s.Provider.CacheImage(ctx, s.ImageDir, im.size, im.path); err != nil {
			logstore.Log("tmdb").Warn("Image not cached", "path", im.path, "err", err)
		}
	}
}

// fillEpisodes stores per-episode metadata for the show's seasons in the
// library and returns the show's typical episode length (0 if unknown).
func (s *Scanner) fillEpisodes(ctx context.Context, show *item.Item, d *tmdb.Details, now time.Time) (int, error) {
	seasons, err := s.Items.List(item.Filter{ParentID: show.ID, Kind: item.Season, Limit: 10000})
	if err != nil {
		return 0, err
	}
	var runtimes []int
	for _, season := range seasons {
		if season.SeasonNumber == nil {
			continue
		}
		n := *season.SeasonNumber
		eps, err := s.Provider.Season(ctx, d.ID, n)
		if errors.Is(err, tmdb.ErrNotFound) {
			continue
		} else if err != nil {
			return 0, err
		}
		byNum := map[int]tmdb.Episode{}
		for _, e := range eps {
			byNum[e.Number] = e
			if n != 0 { // specials say nothing about a show's usual length
				runtimes = append(runtimes, e.Runtime)
			}
		}
		kids, err := s.Items.List(item.Filter{ParentID: season.ID, Kind: item.Episode, Limit: 10000})
		if err != nil {
			return 0, err
		}
		for _, k := range kids {
			if k.EpisodeNumber == nil {
				continue
			}
			e, ok := byNum[*k.EpisodeNumber]
			if !ok {
				continue
			}
			m := &item.Metadata{ItemID: k.ID, Status: item.Matched, Title: e.Title, Overview: e.Overview, PosterPath: e.StillURL, FetchedAt: &now}
			if e.Runtime > 0 {
				rt := e.Runtime
				m.RuntimeMinutes = &rt
			}
			if err := s.Items.SetMetadata(m); err != nil {
				return 0, err
			}
		}
	}
	if t := TypicalLength(runtimes); t > 0 {
		return t, nil
	}
	return s.typicalFromFiles(show)
}

// typicalFromFiles is the fallback when the provider has no runtimes.
func (s *Scanner) typicalFromFiles(show *item.Item) (int, error) {
	eps, err := s.Items.Episodes(show.ID)
	if err != nil {
		return 0, err
	}
	var runtimes []int
	for _, e := range eps {
		if e.SeasonNumber != nil && *e.SeasonNumber == 0 {
			continue
		}
		files, err := s.Files.FilesForItem(e.ID)
		if err != nil {
			return 0, err
		}
		if len(files) > 0 && files[0].DurationMs != nil {
			runtimes = append(runtimes, int(*files[0].DurationMs/60000))
		}
	}
	return TypicalLength(runtimes), nil
}

// fillNewEpisodes covers episodes that appeared after their show was last
// fetched. An episode the provider does not list keeps no metadata row.
func (s *Scanner) fillNewEpisodes(ctx context.Context, r *run, show *item.Item) error {
	m, err := s.Items.Remote(show.ID)
	if err != nil || m.Status != item.Matched || m.ProviderID == nil || r.outage {
		return nil
	}
	eps, err := s.Items.Episodes(show.ID)
	if err != nil {
		return err
	}
	for _, e := range eps {
		if _, err := s.Items.Remote(e.ID); errors.Is(err, db.ErrNotFound) && (m.FetchedAt == nil || e.AddedAt.After(*m.FetchedAt)) {
			return s.fetch(ctx, r, show, m)
		}
	}
	return nil
}
