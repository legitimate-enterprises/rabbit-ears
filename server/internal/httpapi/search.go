package httpapi

import (
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/person"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/youtube"
)

// AdminSearch is Search on an admin operation, which browses unfiltered.
func (s *Server) AdminSearch(ctx context.Context, r api.AdminSearchRequestObject) (api.AdminSearchResponseObject, error) {
	v, err := s.Search(ctx, api.SearchRequestObject{Params: api.SearchParams(r.Params)})
	if err != nil {
		return nil, err
	}
	return api.AdminSearch200JSONResponse(v.(api.Search200JSONResponse)), nil
}

// Search finds every kind at once as the active profile browses, each list
// best first and capped at limit.
func (s *Server) Search(ctx context.Context, r api.SearchRequestObject) (api.SearchResponseObject, error) {
	q, limit, a, admin := r.Params.Q, or(r.Params.Limit, 20), browse(ctx), caller(ctx).asAdmin()
	out := api.Search200JSONResponse{}
	for kind, dst := range map[string]*[]api.Item{item.Movie: &out.Movies, item.Show: &out.Shows, item.Episode: &out.Episodes} {
		rows, err := s.svc.Items.Repo().List(item.Filter{Kind: kind, Q: q, Catalog: true, Audience: a, BestFirst: true, Limit: limit})
		if err != nil {
			return nil, err
		}
		if *dst, err = s.items(ctx, rows, admin); err != nil {
			return nil, err
		}
	}
	people, err := s.svc.People.Repo().List(person.Filter{Q: q, Audience: a, BestFirst: true, Limit: limit})
	if err != nil {
		return nil, err
	}
	out.People = mapAll(people, toPerson)

	f := item.MusicFilter{Q: q, AccountID: &caller(ctx).profile.ID, Clean: clean(ctx), Limit: limit}
	artists, err := s.svc.Music.Repo().Artists(f)
	if err != nil {
		return nil, err
	}
	if out.Artists, err = s.musicArtists(artists); err != nil {
		return nil, err
	}
	albums, err := s.svc.Music.Repo().Albums(f)
	if err != nil {
		return nil, err
	}
	if out.Albums, err = s.musicAlbums(albums); err != nil {
		return nil, err
	}
	songs, err := s.svc.Music.Repo().Songs(f)
	if err != nil {
		return nil, err
	}
	out.Songs = mapAll(songs, toSong)

	channels, err := s.svc.YouTube.Repo().Channels(youtube.ChannelFilter{Q: q, Sort: "best", AccountID: &caller(ctx).profile.ID,
		Audience: a, Limit: limit})
	if err != nil {
		return nil, err
	}
	out.Channels = mapAll(channels, toYouTubeChannel)
	videos, err := s.svc.YouTube.Repo().Videos(youtube.VideoFilter{Q: q, Sort: "best", AccountID: &caller(ctx).profile.ID,
		Audience: a, Limit: limit})
	out.Videos = mapAll(videos, toYouTubeVideo)
	return out, err
}
