package service

import (
	"context"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
)

// ErrNotInPlaylist means a cover's title is not one of the playlist's entries.
var ErrNotInPlaylist = errors.New("not a title of this playlist")

// CoverService sets and finds the custom images of playlists and stations.
// The caller has checked that the one setting a cover owns what it is on.
type CoverService struct {
	repo      *cover.Repo
	playlists *playlist.Repo
	items     *item.Repo
	// Fetcher gets image links.
	Fetcher *cover.Fetcher
}

// NewCoverService returns a CoverService over its repositories.
func NewCoverService(repo *cover.Repo, playlists *playlist.Repo, items *item.Repo) *CoverService {
	return &CoverService{repo: repo, playlists: playlists, items: items, Fetcher: cover.NewFetcher()}
}

// Repo exposes the repository.
func (s *CoverService) Repo() *cover.Repo { return s.repo }

// Upload keeps an image file as o's cover; one that is not an image is
// cover.ErrNotImage or cover.ErrTooManyPixels.
func (s *CoverService) Upload(o cover.Owner, b []byte) (*cover.Cover, error) {
	jpeg, err := cover.Normalize(b)
	if err != nil {
		return nil, err
	}
	c := &cover.Cover{Owner: o, JPEG: jpeg}
	return c, s.repo.Set(c)
}

// Link fetches an image link once and keeps it as o's cover.
func (s *CoverService) Link(ctx context.Context, o cover.Owner, url string) (*cover.Cover, error) {
	b, err := s.Fetcher.Fetch(ctx, url)
	if err != nil {
		return nil, err
	}
	return s.Upload(o, b)
}

// Pick makes the artwork of one of the playlist's titles o's cover.
func (s *CoverService) Pick(o cover.Owner, playlistID, itemID int64, backdrop bool) (*cover.Cover, error) {
	if in, err := s.playlists.HasItem(playlistID, itemID); err != nil || !in {
		return nil, errors.Join(err, ErrNotInPlaylist)
	}
	c := &cover.Cover{Owner: o, ItemID: itemID, Backdrop: backdrop}
	return c, s.repo.Set(c)
}

// Shown is o's cover as the audience sees it, over playlistID's entries:
// nil when it has none, or when its title has left the playlist or is one
// the audience may not see.
func (s *CoverService) Shown(o cover.Owner, playlistID int64, a item.Audience) (*cover.Cover, error) {
	c, err := s.repo.Of(o)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil || c.ItemID == 0 {
		return c, err
	}
	in, err := s.playlists.HasItem(playlistID, c.ItemID)
	if err != nil || !in {
		return nil, err
	}
	seen, err := s.items.Visible(c.ItemID, a)
	if err != nil || !seen {
		return nil, err
	}
	return c, nil
}
