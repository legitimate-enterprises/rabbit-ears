package service

import (
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// UpNextService answers what plays when a movie, episode or video ends.
type UpNextService struct {
	items       *item.Repo
	collections *collection.Repo
	youtube     *youtube.Repo
	playlists   *playlist.Repo
	files       *mediafile.Repo
}

// NewUpNextService returns an UpNextService over its repositories.
func NewUpNextService(items *item.Repo, collections *collection.Repo, yt *youtube.Repo, playlists *playlist.Repo,
	files *mediafile.Repo) *UpNextService {
	return &UpNextService{items, collections, yt, playlists, files}
}

// Next is what follows it for the audience, nil for nothing.
func (s *UpNextService) Next(it *item.Item, a item.Audience) (*item.Item, error) {
	var id *uid.ID
	var err error
	switch it.Kind {
	case item.Episode:
		id, err = s.items.NextEpisode(it.ID, a)
	case item.Movie:
		id, err = s.collections.NextMovie(it.ID, a)
	case item.Video:
		id, err = s.youtube.NextVideo(it.ID, a)
	}
	if err != nil || id == nil {
		return nil, err
	}
	return s.items.Get(*id)
}

// NextRow is the row after index of the playlist's play order for the
// audience, else after itemID's first row, skipping rows with no file on
// disk; it answers the row's index too, and ok false for none.
func (s *UpNextService) NextRow(playlistID, itemID uid.ID, index *int, a item.Audience) (row playlist.Row, at int, ok bool, err error) {
	rows, err := s.playlists.PlayOrder(playlistID, a, -1, 0)
	if err != nil {
		return row, 0, false, err
	}
	at = -1
	if index != nil && *index < len(rows) && rows[*index].ItemID == itemID {
		at = *index
	}
	for i := 0; at < 0 && i < len(rows); i++ {
		if rows[i].ItemID == itemID {
			at = i
		}
	}
	if at < 0 {
		return row, 0, false, nil
	}
	// Chunks keep each lookup under SQLite's bound-variable limit.
	const chunk = 500
	for from := at + 1; from < len(rows); from += chunk {
		tail := rows[from:min(from+chunk, len(rows))]
		ids := make([]uid.ID, len(tail))
		for i, r := range tail {
			ids[i] = r.ItemID
		}
		gone, err := s.files.Unavailable(ids)
		if err != nil {
			return row, 0, false, err
		}
		for i, r := range tail {
			if !gone[r.ItemID] {
				return r, from + i, true, nil
			}
		}
	}
	return row, 0, false, nil
}
