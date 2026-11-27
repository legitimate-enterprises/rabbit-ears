package scan

import (
	"context"
	"errors"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/musicbrainz"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/review"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/tmdb"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// MatchMusic looks up the library's albums that no provider was asked about
// yet, and looks again at those whose folders and tags no longer say what
// their remote row was made from or last checked against, returning how
// many lookups failed. Like MatchLibrary it never fails on a provider error.
func (s *Scanner) MatchMusic(ctx context.Context, lib *library.Library) (int, error) {
	if s.Music == nil || !s.remote() {
		return 0, nil
	}
	albums, err := s.Items.NeverLookedUp(lib.ID, batchLimit)
	if err != nil {
		return 0, err
	}
	r := &run{}
	for _, al := range albums {
		if err := s.fetchAlbum(ctx, r, al, nil, false); err != nil {
			return r.failed, err
		}
	}
	stamps, err := s.Items.IdentityStamps(lib.ID)
	if err != nil {
		return r.failed, err
	}
	reviewed, err := s.reviewed(lib.ID)
	if err != nil {
		return r.failed, err
	}
	if albums, err = s.Items.List(item.Filter{LibraryID: &lib.ID, Kind: item.Album, Limit: 1000000}); err != nil {
		return r.failed, err
	}
	for _, al := range albums {
		was, ok := stamps[al.ID]
		if !ok || r.outage {
			continue
		}
		st, err := s.statedAlbum(al)
		if err != nil {
			return r.failed, err
		}
		switch {
		case st.stamp != was:
			err = s.fetchAlbum(ctx, r, al, nil, true)
		case reviewed[al.ID]:
			err = s.agreed(al.ID)
		}
		if err != nil {
			return r.failed, err
		}
	}
	return r.failed, nil
}

// fetchAlbum looks one album up and stores the outcome over prev, its
// remote row. Provider errors become a retry on the album; only
// cancellation is returned. check is as fetchAs has it.
func (s *Scanner) fetchAlbum(ctx context.Context, r *run, al *item.Item, prev *item.Metadata, check bool) error {
	if prev == nil {
		p, err := s.Items.Remote(al.ID)
		switch {
		case errors.Is(err, db.ErrNotFound):
			p = &item.Metadata{ItemID: al.ID, Provider: item.MusicBrainz, Status: item.Unmatched}
		case err != nil:
			return err
		}
		prev = p
	}
	now := s.Now().UTC()
	if r.outage {
		r.failed++
		return s.deferFetch(al, prev, now)
	}
	stamp, err := s.lookupAlbum(ctx, al, prev, now, check)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, musicbrainz.ErrNotFound) || errors.Is(err, errNoMatch):
		next := tmdb.RefreshAt(now)
		prev.RetryAt = &next
		if err := s.Items.SetMetadata(prev); err != nil || prev.Status == item.Matched {
			return err
		}
		return s.Items.SetIdentityStamp(al.ID, stamp)
	case ctx.Err() != nil:
		return ctx.Err()
	}
	logstore.Log("musicbrainz").Warn("Metadata lookup failed; will retry", "item_id", al.ID, "title", al.ParsedTitle, "err", err)
	r.outage = true
	r.failed++
	return s.deferFetch(al, prev, now)
}

// identifyAlbum finds the MusicBrainz release an album is by the id its
// folders and tags state, or its release group when they state only that,
// else by title, album artist and year. artistID is the artist a search
// credits. When that decides nothing it returns the review to raise.
func (s *Scanner) identifyAlbum(ctx context.Context, al, artist *item.Item, st stated) (release, group, artistID string, rv *review.Review, err error) {
	switch {
	case len(st.Invalid) > 0:
		return "", "", "", s.review(al, review.InvalidIdentity, st.said), nil
	case st.clash:
		return "", "", "", s.review(al, review.ConflictingIdentity, st.said), nil
	case st.Release != "":
		return st.Release, "", "", nil, nil
	case st.ReleaseGroup != "":
		return "", st.ReleaseGroup, "", nil, nil
	}
	hit, err := musicbrainz.Find(ctx, s.Music.SearchRelease, artist.ParsedTitle, al.ParsedTitle, al.ParsedYear)
	if err != nil {
		return "", "", "", nil, err
	}
	if hit == nil {
		return "", "", "", s.review(al, review.NoMatch, nil), nil
	}
	return hit.ID, "", hit.ArtistID, nil, nil
}

// lookupAlbum identifies the album (or reuses its match), stores the
// release id, its genres and the archive's cover, and matches the album
// artist, returning the identity stamp to record for an album left
// unmatched. The settings choose them over the tags' and the files' when
// read. An album stated only as a release group is matched to the group:
// it takes the group's genres and names no release, so no edition's cover.
// The line between applying what the folders and tags state and asking an
// admin runs as rematch draws it, but for editions: a match never checked
// takes another release of its release group that the names state, as the
// same album, and only a release of another group is a conflict. A match
// never checked that states no id is taken as it is, since a search proves
// nothing against it.
func (s *Scanner) lookupAlbum(ctx context.Context, al *item.Item, m *item.Metadata, now time.Time, check bool) (string, error) {
	artist, err := s.Items.Get(*al.ParentID)
	if err != nil {
		return "", err
	}
	st, err := s.statedAlbum(al)
	if err != nil {
		return "", err
	}
	was, err := s.Items.IdentityStamp(al.ID)
	if err != nil {
		return "", err
	}
	matched := m.Status == item.Matched
	release, group, artistID, stamp := m.ExternalID, "", "", st.stamp
	if matched && release == "" {
		group = st.ReleaseGroup
	}
	fail := func(rv *review.Review) (string, error) {
		if err := s.settle(al.ID, rv); err != nil {
			return "", err
		}
		return st.stamp, errNoMatch
	}
	if matched && !check {
		stamp = was
	}
	if !matched || check && was != st.stamp {
		legacy := matched && was == ""
		var rv *review.Review
		var wantRelease, wantGroup, wantArtist string
		if !legacy || st.Release != "" || len(st.Invalid) > 0 || st.clash {
			if wantRelease, wantGroup, wantArtist, rv, err = s.identifyAlbum(ctx, al, artist, st); err != nil {
				return "", err
			}
		}
		if legacy && rv == nil && wantRelease != "" && wantRelease != release {
			same, err := s.sameAlbum(ctx, release, wantRelease, st.ReleaseGroup)
			if err != nil {
				return "", err
			}
			if same {
				release = wantRelease
			} else {
				rv = s.review(al, review.ConflictingIdentity, append([]review.Evidence{matchedTo(m)}, st.said...))
			}
		}
		var take bool
		if take, stamp, err = s.rematch(al.ID, matched, was, st, rv); err != nil {
			return stamp, err
		}
		if take {
			release, group, artistID = wantRelease, wantGroup, wantArtist
		}
		if matched && release != "" && release == m.ExternalID {
			return "", s.Items.SetIdentityStamp(al.ID, stamp)
		}
	}
	var d *musicbrainz.Details
	if release != "" {
		d, err = s.Music.Release(ctx, release)
	} else {
		d, err = s.Music.ReleaseGroup(ctx, group)
	}
	fresh := release != m.ExternalID || release == ""
	switch {
	case errors.Is(err, musicbrainz.ErrNotFound) && fresh && (release != "" && release == st.Release || group != ""):
		return fail(s.review(al, review.InvalidIdentity, st.said))
	case err != nil:
		return "", err
	case fresh && release != "" && st.ReleaseGroup != "" && d.GroupID != "" && d.GroupID != st.ReleaseGroup:
		// The release stated is an edition of another album than the group stated.
		return fail(s.review(al, review.ConflictingIdentity, st.said))
	}
	if release != m.ExternalID {
		m.PosterPath = "" // the cover of the release it was matched to before
	}
	if m.PosterPath == "" && s.ImageDir != "" && release != "" {
		p, err := s.Music.CacheCover(ctx, s.ImageDir, release)
		switch {
		case err == nil:
			m.PosterPath = p
		case !errors.Is(err, musicbrainz.ErrNotFound):
			logstore.Log("musicbrainz").Warn("Cover not cached", "release", release, "err", err)
		}
	}
	refresh := tmdb.RefreshAt(now)
	m.Provider, m.Status, m.ExternalID, m.FetchedAt, m.RetryAt = item.MusicBrainz, item.Matched, release, &now, &refresh
	if err := s.Items.SetMetadata(m); err != nil {
		return "", err
	}
	if err := s.Items.SetIdentityStamp(al.ID, stamp); err != nil {
		return "", err
	}
	if err := s.remoteGenres(al.ID, d.Genres); err != nil {
		return "", err
	}
	if artistID == "" {
		artistID = d.ArtistID
	}
	if artistID == "" {
		return "", nil
	}
	if am, err := s.Items.Remote(artist.ID); err == nil && am.Status == item.Matched {
		return "", nil
	}
	ad, err := s.Music.Artist(ctx, artistID)
	if err != nil {
		return "", err
	}
	if err := s.Items.SetMetadata(&item.Metadata{ItemID: artist.ID, Provider: item.MusicBrainz, Status: item.Matched,
		ExternalID: artistID, FetchedAt: &now}); err != nil {
		return "", err
	}
	return "", s.remoteGenres(artist.ID, ad.Genres)
}

// sameAlbum reports whether release b is an edition of the album release
// a is, b's release group being group when the names state one. A release
// MusicBrainz does not know proves nothing.
func (s *Scanner) sameAlbum(ctx context.Context, a, b, group string) (bool, error) {
	d, err := s.Music.Release(ctx, a)
	if err == nil && group == "" {
		var of *musicbrainz.Details
		if of, err = s.Music.Release(ctx, b); err == nil {
			group = of.GroupID
		}
	}
	switch {
	case errors.Is(err, musicbrainz.ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return d.GroupID != "" && d.GroupID == group, nil
}

// remoteGenres stores a provider's genres for an item beside its tags'.
func (s *Scanner) remoteGenres(itemID uid.ID, names []string) error {
	gs := make([]genre.Genre, len(names))
	for i, n := range names {
		gs[i] = genre.Genre{Name: n}
	}
	return s.Genres.SetForItem(itemID, genre.Remote, gs)
}
