package scan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/musicbrainz"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/parse"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// stated is what a title's folder and file names, and a song's tags, say
// it is, read fresh from the paths its records hold.
type stated struct {
	parse.Identity
	title string
	year  *int
	// years are the differing premiere years a show's episode files name
	// when its folder names none; more than one decides nothing.
	years []int
	// clash is whether two names or tags state different ids of one kind,
	// or different releases that no release group stated makes one album.
	clash bool
	// editions is whether they state different releases.
	editions bool
	// said is each statement with the path making it, as review evidence.
	said []review.Evidence
	// stamp identifies all of the above, to tell when it changes.
	stamp string
}

// say adds what the name at path states. Ids of one provider that differ
// between names clash; so do two countries. Releases that differ are
// editions, which seal settles.
func (st *stated) say(path string, id parse.Identity) {
	note := func(provider, value string) {
		st.said = append(st.said, review.Evidence{Role: review.Stated, Provider: provider, ExternalID: value, Path: path})
	}
	for _, tag := range id.Invalid {
		st.Invalid = append(st.Invalid, tag)
		st.said = append(st.said, review.Evidence{Role: review.Stated, Title: tag, Path: path})
	}
	text := func(have *string, got, provider string, differ *bool) {
		if got == "" {
			return
		}
		if *have != "" && *have != got {
			*differ = true
		}
		if *have != got {
			note(provider, got)
		}
		*have = got
	}
	number := func(have *int64, got int64, provider string) {
		if got == 0 {
			return
		}
		if *have != 0 && *have != got {
			st.clash = true
		}
		if *have != got {
			note(provider, strconv.FormatInt(got, 10))
		}
		*have = got
	}
	number(&st.TMDB, id.TMDB, "tmdb")
	number(&st.TVDB, id.TVDB, "tvdb")
	text(&st.IMDB, id.IMDB, "imdb", &st.clash)
	text(&st.Release, id.Release, "musicbrainz", &st.editions)
	text(&st.ReleaseGroup, id.ReleaseGroup, "musicbrainz", &st.clash)
	if id.Country != "" {
		if st.Country != "" && st.Country != id.Country {
			st.clash = true
		}
		st.Country = id.Country
		st.said = append(st.said, review.Evidence{Role: review.Stated, Country: id.Country, Path: path})
	}
}

// seal settles editions: under the one release group stated they are
// that album, stated as the group alone; with none stated they clash.
func (st *stated) seal() {
	if st.editions && st.ReleaseGroup != "" {
		st.Release = ""
	} else if st.editions {
		st.clash = true
	}
	year := ""
	if st.year != nil {
		year = strconv.Itoa(*st.year)
	}
	st.stamp = fmt.Sprintf("%s|%s|%s|tmdb=%d|tvdb=%d|imdb=%s|mb=%s|mbg=%s|bad=%s|clash=%t|years=%v", strings.ToLower(st.title), year, st.Country,
		st.TMDB, st.TVDB, st.IMDB, st.Release, st.ReleaseGroup, strings.Join(st.Invalid, ","), st.clash, st.years)
}

// statedVideo reads a show's folder name, or the names of a movie's files
// on disk and of the folders they have to themselves. A show whose folder
// names no year takes the one its episode files agree on.
func (s *Scanner) statedVideo(it *item.Item) (stated, error) {
	st := stated{title: it.ParsedTitle, year: it.ParsedYear}
	if it.Kind == item.Show {
		if it.Path != "" {
			st.say(it.Path, parse.ReadIdentity(filepath.Base(it.Path)))
		}
		if st.year == nil {
			paths, err := s.Items.EpisodePaths(it.ID)
			if err != nil {
				return st, err
			}
			for _, p := range paths {
				if y := parse.EpisodeYear(filepath.Base(p)); y != nil && !slices.Contains(st.years, *y) {
					st.years = append(st.years, *y)
					st.said = append(st.said, review.Evidence{Role: review.Stated, Year: y, Path: p})
				}
			}
			slices.Sort(st.years)
			if len(st.years) == 1 {
				st.year = &st.years[0]
			}
		}
		st.seal()
		return st, nil
	}
	files, err := s.Files.FilesForItem(it.ID)
	if err != nil {
		return st, err
	}
	folders, err := s.Libraries.Folders(it.LibraryID)
	if err != nil {
		return st, err
	}
	for _, f := range files {
		if f.MissingAt != nil {
			continue
		}
		st.say(f.Path, parse.ReadIdentity(filepath.Base(f.Path)))
		dir := filepath.Dir(f.Path)
		if !slices.ContainsFunc(folders, func(l *library.Folder) bool { return l.Path == dir }) && !ownName(f.Path) {
			st.say(dir, parse.ReadIdentity(filepath.Base(dir)))
		}
	}
	st.seal()
	return st, nil
}

// statedAlbum reads the folders of an album's songs, the album's own
// above a disc's, and the MusicBrainz ids their tags name. Folders of
// several editions that the tags put in one release group state the
// group; nothing here asks MusicBrainz which album a release is.
func (s *Scanner) statedAlbum(al *item.Item) (stated, error) {
	st := stated{title: al.ParsedTitle, year: al.ParsedYear}
	songs, err := s.Items.AlbumReleases(al.ID)
	if err != nil {
		return st, err
	}
	seen := map[string]bool{}
	for _, song := range songs {
		dir := filepath.Dir(song.Path)
		if discDir.MatchString(filepath.Base(dir)) {
			dir = filepath.Dir(dir)
		}
		if song.Path != "" && !seen[dir] {
			seen[dir] = true
			st.say(dir, parse.ReadIdentity(filepath.Base(dir)))
		}
		tags := parse.Identity{}
		for _, t := range []struct {
			id   string
			into *string
		}{{song.Release, &tags.Release}, {song.Group, &tags.ReleaseGroup}} {
			switch {
			case t.id == "":
			case musicbrainz.ValidID(t.id):
				*t.into = t.id
			default:
				tags.Invalid = append(tags.Invalid, t.id)
			}
		}
		st.say(song.Path, tags)
	}
	st.seal()
	return st, nil
}

func sameYear(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// settle leaves the title the one identity review rv, or with nil none.
func (s *Scanner) settle(itemID uid.ID, rv *review.Review) error {
	if s.Reviews == nil {
		return nil
	}
	keep := ""
	if rv != nil {
		if err := s.Reviews.Raise(rv); err != nil {
			return err
		}
		keep = rv.Reason
	}
	return s.Reviews.Settle(itemID, keep, review.OfIdentity...)
}

func (s *Scanner) review(it *item.Item, reason string, ev []review.Evidence) *review.Review {
	return &review.Review{LibraryID: it.LibraryID, ItemID: &it.ID, Reason: reason, RaisedAt: s.Now().UTC(), Evidence: ev}
}

func candidates(rs []tmdb.Result) []review.Evidence {
	out := make([]review.Evidence, len(rs))
	for i, r := range rs {
		out[i] = review.Evidence{Role: review.Candidate, Provider: "tmdb", ExternalID: strconv.FormatInt(r.ID, 10), Title: r.Title,
			Country: strings.Join(r.OriginCountry, ",")}
		if y, err := strconv.Atoi(strings.SplitN(r.ReleaseDate, "-", 2)[0]); err == nil {
			out[i].Year = &y
		}
	}
	return out
}

// identify finds the TMDB title a movie or show is by what its names
// state: an id outright, a TVDB or IMDb id through TMDB's lookup, else the
// one search result of its title, year and country. When that decides
// nothing it returns the review to raise instead: ids that are malformed,
// unknown or disagree are never passed over for a title that merely has
// the same name.
func (s *Scanner) identify(ctx context.Context, it *item.Item, st stated) (*tmdb.Result, *review.Review, error) {
	tv := it.Kind == item.Show
	switch {
	case len(st.Invalid) > 0:
		return nil, s.review(it, review.InvalidIdentity, st.said), nil
	case st.clash:
		return nil, s.review(it, review.ConflictingIdentity, st.said), nil
	case len(st.years) > 1:
		return nil, s.review(it, review.ConflictingYears, st.said), nil
	}
	var ids []tmdb.Result
	ev := st.said
	if st.TMDB != 0 {
		ids = append(ids, tmdb.Result{ID: st.TMDB})
	}
	for _, x := range []struct{ source, id string }{{tmdb.TVDB, strconv.FormatInt(st.TVDB, 10)}, {tmdb.IMDB, st.IMDB}} {
		if x.id == "" || x.id == "0" {
			continue
		}
		movies, shows, err := s.Provider.Find(ctx, x.source, x.id)
		if err != nil && !errors.Is(err, tmdb.ErrNotFound) {
			return nil, nil, err
		}
		if tv {
			movies = shows
		}
		if len(movies) != 1 {
			return nil, s.review(it, review.InvalidIdentity, append(ev, candidates(movies)...)), nil
		}
		ids = append(ids, movies[0])
		ev = append(ev, candidates(movies)...)
	}
	if n := len(ids); n > 0 {
		if slices.ContainsFunc(ids, func(r tmdb.Result) bool { return r.ID != ids[0].ID }) {
			return nil, s.review(it, review.ConflictingIdentity, ev), nil
		}
		return &ids[n-1], nil, nil
	}
	q := tmdb.Query{Title: st.title, Year: st.year, TV: tv}
	var rs []tmdb.Result
	var err error
	if tv {
		// TMDB's movie results name no country to hold a hint against.
		q.Country = st.Country
		rs, err = s.Provider.SearchTV(ctx, q.Title, q.Year)
	} else {
		rs, err = s.Provider.SearchMovie(ctx, q.Title, q.Year)
	}
	if err != nil {
		return nil, nil, err
	}
	hit, tied := tmdb.Match(rs, q)
	switch {
	case hit != nil:
		return hit, nil, nil
	case len(tied) > 0:
		return nil, s.review(it, review.TiedCandidates, candidates(tied)), nil
	}
	return nil, s.review(it, review.NoMatch, nil), nil
}

// matchedTo is a title's present match as review evidence.
func matchedTo(m *item.Metadata) review.Evidence {
	ev := review.Evidence{Role: review.Current, Provider: m.Provider, ExternalID: m.ExternalID, Title: m.Title}
	if m.ProviderID != nil {
		ev.ExternalID = strconv.FormatInt(*m.ProviderID, 10)
	}
	if y, err := strconv.Atoi(strings.SplitN(m.ReleaseDate, "-", 2)[0]); err == nil {
		ev.Year = &y
	}
	return ev
}

// reviewed is the library's items that have a review of their identity.
func (s *Scanner) reviewed(libraryID uid.ID) (map[uid.ID]bool, error) {
	out := map[uid.ID]bool{}
	if s.Reviews == nil {
		return out, nil
	}
	rs, err := s.Reviews.List(review.Filter{LibraryID: &libraryID})
	for _, rv := range rs {
		if rv.ItemID != nil && slices.Contains(review.OfIdentity, rv.Reason) {
			out[*rv.ItemID] = true
		}
	}
	return out, err
}

// agreed ends the identity reviews of a matched title whose names say
// again what its match was made from.
func (s *Scanner) agreed(itemID uid.ID) error {
	m, err := s.Items.Remote(itemID)
	if err != nil || m.Status != item.Matched {
		return nil //nolint:nilerr // without a match its review stands
	}
	return s.settle(itemID, nil)
}

// checkIdentities looks again at each of the library's movies and shows
// whose names no longer say what its remote row was made from or last
// checked against, a row never checked included.
func (s *Scanner) checkIdentities(ctx context.Context, r *run, libraryID uid.ID) error {
	stamps, err := s.Items.IdentityStamps(libraryID)
	if err != nil {
		return err
	}
	reviewed, err := s.reviewed(libraryID)
	if err != nil {
		return err
	}
	for _, kind := range []string{item.Movie, item.Show} {
		items, err := s.Items.List(item.Filter{LibraryID: &libraryID, Kind: kind, Limit: 1000000})
		if err != nil {
			return err
		}
		for _, it := range items {
			was, ok := stamps[it.ID]
			if !ok || r.outage {
				continue
			}
			st, err := s.statedVideo(it)
			if err != nil {
				return err
			}
			switch {
			case st.stamp != was:
				if r.gaveWay() {
					return nil
				}
				err = s.fetchAs(ctx, r, it, nil, true)
			case reviewed[it.ID]:
				err = s.agreed(it.ID)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
