package httpapi

import (
	"context"
	"fmt"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// GetUpNext answers what plays when the item ends: the playlist's next row
// when it was played from one, else the next episode, film or video.
func (s *Server) GetUpNext(ctx context.Context, r api.GetUpNextRequestObject) (api.GetUpNextResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	a := service.AudienceFor(who)
	it, err := s.visibleTo(a, r.ItemId)
	if err != nil {
		return nil, err
	}
	var out api.UpNext
	var next *item.Item
	if pl := r.Params.PlaylistId; pl != nil {
		p, err := s.playlistOf(ctx, r.ProfileId, *pl, playlist.Read)
		if err != nil {
			return nil, err
		}
		row, at, ok, err := s.svc.UpNext.NextRow(p.ID, it.ID, r.Params.Index, a)
		if err != nil || !ok {
			return api.GetUpNext204Response{}, err
		}
		if next, err = s.svc.Items.Repo().Get(row.ItemID); err != nil {
			return nil, err
		}
		out.Index = &at
		if row.EditionID != 0 {
			out.EditionId = &row.EditionID
		}
	} else if r.Params.Index != nil {
		return nil, invalid("index", "only with playlistId")
	} else if next, err = s.svc.UpNext.Next(it, a); err != nil || next == nil {
		return api.GetUpNext204Response{}, err
	}
	out.ItemId, out.Kind = next.ID, api.UpNextKind(next.Kind)
	if next.Kind == item.Video {
		v, err := one(s.svc.YouTube.Repo().Videos(youtube.VideoFilter{ID: next.ID, AccountID: who.ID, Audience: a}))
		if err != nil {
			return nil, err
		}
		video := toYouTubeVideo(v)
		out.Video, out.Title = &video, v.Title
		return api.GetUpNext200JSONResponse(out), nil
	}
	v, err := s.item(next, false)
	if err != nil {
		return nil, err
	}
	out.Item, out.Title = &v, titleOf(v)
	if next.Kind == item.Episode {
		if out.Title, err = s.episodeTitle(next, out.Title); err != nil {
			return nil, err
		}
	}
	if out.EditionId != nil {
		name, err := s.editionName(next.ID, *out.EditionId)
		if err != nil {
			return nil, err
		}
		if name != nil {
			out.Title += " · " + *name
		}
	}
	return api.GetUpNext200JSONResponse(out), nil
}

// titleOf is an item's provider title, else its parsed one.
func titleOf(v api.Item) string {
	if v.Metadata != nil && v.Metadata.Title != nil && *v.Metadata.Title != "" {
		return *v.Metadata.Title
	}
	return v.ParsedTitle
}

// episodeTitle names an episode as `Show · S1 E2 · Title`.
func (s *Server) episodeTitle(ep *item.Item, own string) (string, error) {
	season, err := s.svc.Items.Repo().Get(*ep.ParentID)
	if err != nil {
		return "", err
	}
	show, err := s.svc.Items.Repo().Get(*season.ParentID)
	if err != nil {
		return "", err
	}
	v, err := s.item(show, false)
	code := func(n *int) int {
		if n == nil {
			return 0
		}
		return *n
	}
	return fmt.Sprintf("%s · S%d E%d · %s", titleOf(v), code(season.SeasonNumber), code(ep.EpisodeNumber), own), err
}
