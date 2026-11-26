package httpapi

import (
	"context"
	"net/url"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// YouTube: channels, videos and watch state, as the active profile may see
// them: a kid the channels for kids or on their allow list, with their
// videos.

// channelURL is the YouTube page of a channel id (UC...) or playlist id
// (PL...). Ids come from sidecar files, so they are escaped.
func channelURL(id string) string {
	if strings.HasPrefix(id, "PL") {
		return "https://www.youtube.com/playlist?list=" + url.QueryEscape(id)
	}
	return "https://www.youtube.com/channel/" + url.PathEscape(id)
}

func toYouTubeChannel(v *youtube.ChannelView) api.YouTubeChannel {
	out := api.YouTubeChannel{Id: v.ID, LibraryId: v.LibraryID, Title: v.Title, Overview: optStr(v.Overview), ForKids: &v.ForKids, VideoCount: v.VideoCount,
		UnwatchedCount: v.UnwatchedCount, LastVideoOn: optDate(v.LastVideoAt), ExternalId: optStr(v.ExternalID),
		AddedAt: v.AddedAt}
	if v.ExternalID != "" {
		out.SourceUrl = optStr(channelURL(v.ExternalID))
	}
	return out
}

func toYouTubeVideo(v *youtube.VideoView) api.YouTubeVideo {
	out := api.YouTubeVideo{Id: v.ID, LibraryId: v.LibraryID, ChannelId: v.ChannelID, ChannelTitle: v.ChannelTitle, Title: v.Title,
		Overview: optStr(v.Overview), DurationMs: v.DurationMs, PublishedOn: optDate(v.PublishedAt), Height: v.Height, Watched: v.Watched,
		PositionMs: v.PositionMs, LastPlayedAt: v.LastPlayedAt, ExternalId: optStr(v.ExternalID), AddedAt: v.AddedAt,
		Unavailable: optTrue(v.Unavailable)}
	if v.ExternalID != "" {
		out.SourceUrl = optStr("https://www.youtube.com/watch?v=" + url.QueryEscape(v.ExternalID))
	}
	return out
}

// ListYouTubeChannels pages channels with videos.
func (s *Server) ListYouTubeChannels(ctx context.Context, r api.ListYouTubeChannelsRequestObject) (api.ListYouTubeChannelsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	out := api.ListYouTubeChannels200JSONResponse{Items: []api.YouTubeChannel{}}
	rows, err := s.svc.YouTube.Repo().Channels(youtube.ChannelFilter{LibraryID: or(r.Params.LibraryId, 0), Q: or(r.Params.Q, ""),
		Sort: enumStr(r.Params.Sort), AccountID: caller(ctx).profile.ID, Audience: browse(ctx), Limit: limit + 1, Offset: offset})
	if err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	out.Items = mapAll(rows, toYouTubeChannel)
	return out, nil
}

// GetYouTubeChannel returns one channel.
func (s *Server) GetYouTubeChannel(ctx context.Context, r api.GetYouTubeChannelRequestObject) (api.GetYouTubeChannelResponseObject, error) {
	v, err := one(s.svc.YouTube.Repo().Channels(youtube.ChannelFilter{ID: r.ChannelId, AccountID: caller(ctx).profile.ID, Audience: audience(ctx)}))
	if err != nil {
		return nil, err
	}
	return api.GetYouTubeChannel200JSONResponse(toYouTubeChannel(v)), nil
}

// ListYouTubeVideos pages videos with the active profile's watch state.
func (s *Server) ListYouTubeVideos(ctx context.Context, r api.ListYouTubeVideosRequestObject) (api.ListYouTubeVideosResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	out := api.ListYouTubeVideos200JSONResponse{Items: []api.YouTubeVideo{}}
	rows, err := s.svc.YouTube.Repo().Videos(youtube.VideoFilter{LibraryID: or(r.Params.LibraryId, 0), ChannelID: or(r.Params.ChannelId, 0),
		Unwatched: or(r.Params.Unwatched, false), InProgress: or(r.Params.InProgress, false), Q: or(r.Params.Q, ""),
		Sort: enumStr(r.Params.Sort), AccountID: caller(ctx).profile.ID, Audience: browse(ctx), Limit: limit + 1, Offset: offset})
	if err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	out.Items = mapAll(rows, toYouTubeVideo)
	return out, nil
}

// GetYouTubeVideo returns one video.
func (s *Server) GetYouTubeVideo(ctx context.Context, r api.GetYouTubeVideoRequestObject) (api.GetYouTubeVideoResponseObject, error) {
	v, err := one(s.svc.YouTube.Repo().Videos(youtube.VideoFilter{ID: r.VideoId, AccountID: caller(ctx).profile.ID, Audience: audience(ctx)}))
	if err != nil {
		return nil, err
	}
	return api.GetYouTubeVideo200JSONResponse(toYouTubeVideo(v)), nil
}

// PutYouTubeChannelWatchState marks every video of a channel the profile
// may see watched or unwatched.
func (s *Server) PutYouTubeChannelWatchState(ctx context.Context, r api.PutYouTubeChannelWatchStateRequestObject) (api.PutYouTubeChannelWatchStateResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	ch, err := s.visibleTo(service.AudienceFor(who), r.ChannelId)
	if err != nil || ch.Kind != item.Channel {
		return nil, orNotFound(err)
	}
	videos, err := s.svc.YouTube.Repo().Videos(youtube.VideoFilter{ChannelID: ch.ID, Limit: -1})
	if err != nil {
		return nil, err
	}
	var tell []int64
	err = s.svc.WatchStates.Repo().InTx(func(repo *watchstate.Repo) error {
		for _, v := range videos {
			_, changed, err := saveWatchStateIn(repo, r.ProfileId, v.ID, nil, func(w *watchstate.State) { w.Mark(r.Body.Watched, 0) })
			if err != nil {
				return err
			}
			if changed {
				tell = append(tell, v.ID)
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
	return api.PutYouTubeChannelWatchState204Response{}, nil
}
