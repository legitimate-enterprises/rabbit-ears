package httpapi

import (
	"context"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// Bulk actions on a selection. Each resolves and checks every item before
// it writes any, so a rejected selection changes nothing.

// PutWatchStates marks a selection watched or unwatched: movies, episodes
// and videos themselves, shows and seasons their episodes, channels their
// videos.
func (s *Server) PutWatchStates(ctx context.Context, r api.PutWatchStatesRequestObject) (api.PutWatchStatesResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	a := service.AudienceFor(who)
	var marks []int64
	for _, id := range r.Body.ItemIds {
		it, err := s.visibleTo(a, id)
		if err != nil {
			return nil, err
		}
		switch it.Kind {
		case item.Movie, item.Episode, item.Video:
			marks = append(marks, it.ID)
		case item.Show:
			eps, err := s.svc.Items.Repo().Episodes(it.ID)
			if err != nil {
				return nil, err
			}
			for _, ep := range eps {
				if ok, err := s.svc.Items.Repo().Visible(ep.ID, a); err != nil {
					return nil, err
				} else if ok {
					marks = append(marks, ep.ID)
				}
			}
		case item.Season:
			eps, err := s.svc.Items.Repo().List(item.Filter{ParentID: it.ID, Kind: item.Episode, Audience: a, Limit: 10000})
			if err != nil {
				return nil, err
			}
			for _, ep := range eps {
				marks = append(marks, ep.ID)
			}
		case item.Channel:
			vs, err := s.svc.YouTube.Repo().Videos(youtube.VideoFilter{ChannelID: it.ID, Limit: -1})
			if err != nil {
				return nil, err
			}
			for _, v := range vs {
				marks = append(marks, v.ID)
			}
		default:
			return nil, invalid("itemIds", "only movies, shows, seasons, episodes, channels and videos are watched")
		}
	}
	at := watchedAt(r.Body.At)
	var tell []int64
	err = s.svc.WatchStates.Repo().InTx(func(repo *watchstate.Repo) error {
		for _, id := range marks {
			_, changed, err := saveWatchStateIn(repo, r.ProfileId, id, at, func(w *watchstate.State) { w.Mark(r.Body.Watched, 0) })
			if err != nil {
				return err
			}
			if changed {
				tell = append(tell, id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, id := range tell {
		s.watchStateChanged(r.ProfileId, id)
	}
	return api.PutWatchStates204Response{}, nil
}

// SetKidsContents says whether each movie or show of a selection is kids'
// content, and whether each channel is for kids.
func (s *Server) SetKidsContents(ctx context.Context, r api.SetKidsContentsRequestObject) (api.SetKidsContentsResponseObject, error) {
	if caller(ctx).asKid() {
		return nil, errForbidden
	}
	for _, id := range r.Body.ItemIds {
		it, err := s.svc.Items.Repo().Get(id)
		if err != nil || it.Kind != item.Movie && it.Kind != item.Show && it.Kind != item.Channel {
			return nil, orNotFound(err)
		}
	}
	for _, id := range r.Body.ItemIds {
		if err := s.svc.Items.Repo().SetKidsContent(id, &r.Body.KidsContent); err != nil {
			return nil, err
		}
	}
	return api.SetKidsContents204Response{}, nil
}

// AddPlaylistEntries appends a selection to a playlist in order; a title of
// several editions named without one takes its default.
func (s *Server) AddPlaylistEntries(ctx context.Context, r api.AddPlaylistEntriesRequestObject) (api.AddPlaylistEntriesResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Write)
	if err != nil {
		return nil, err
	}
	entries := make([]*playlist.Entry, 0, len(r.Body.Entries))
	for _, in := range r.Body.Entries {
		if (in.ItemId == nil) == (in.CollectionId == nil) {
			return nil, invalid("entries", "name exactly one of itemId and collectionId")
		}
		if in.CollectionId != nil {
			if p.Kind != playlist.Movies || in.EditionId != nil {
				return nil, invalid("entries", service.ErrWrongPlaylistKind.Error())
			}
			v, err := s.seenCollection(ctx, *in.CollectionId)
			if err != nil {
				return nil, err
			}
			entries = append(entries, &playlist.Entry{PlaylistID: p.ID, CollectionID: v.ID})
			continue
		}
		it, err := s.seen(ctx, *in.ItemId)
		if err != nil {
			return nil, err
		}
		if !service.Holds(p.Kind, it.Kind) {
			return nil, invalid("entries", service.ErrWrongPlaylistKind.Error())
		}
		ed := or(in.EditionId, 0)
		if ed == 0 && (it.Kind == item.Movie || it.Kind == item.Episode || it.Kind == item.Song) {
			if d, err := s.svc.Editions.Repo().Default(it.ID); err == nil {
				ed = d.ID
			}
		} else if ed != 0 {
			if e, err := s.svc.Editions.Repo().Get(ed); err != nil || e.ItemID != it.ID {
				return nil, invalid("entries", service.ErrNotItsEdition.Error())
			}
		}
		e, err := s.svc.Playlists.EntryFor(p, it, ed)
		if errors.Is(err, service.ErrEditionRequired) || errors.Is(err, service.ErrNotItsEdition) {
			return nil, invalid("entries", err.Error())
		} else if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err := s.svc.Playlists.Repo().AddEntries(entries); err != nil {
		return nil, err
	}
	return api.AddPlaylistEntries204Response{}, nil
}
