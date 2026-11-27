package httpapi

import (
	"cmp"
	"context"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/genre"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Music: artists, albums, songs, letters, search and recently played. While
// a kid is active every view is Clean, so what is explicit is not found.

func clean(ctx context.Context) bool { return caller(ctx).asKid() }

// musicExtras loads the provider fields and genres of these items.
func (s *Server) musicExtras(items []*item.Item) (map[uid.ID]*item.Metadata, map[uid.ID][]*genre.Genre, error) {
	ids := make([]uid.ID, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	metas, err := s.svc.Items.Repo().MetadataFor(ids)
	if err != nil {
		return nil, nil, err
	}
	genres, err := s.svc.Genres.Repo().ForItems(ids)
	return metas, genres, err
}

// mbid is a matched item's MusicBrainz id.
func mbid(m *item.Metadata) *string {
	if m == nil || m.Status != item.Matched {
		return nil
	}
	return optStr(m.ExternalID)
}

func overview(m *item.Metadata) *string {
	if m == nil {
		return nil
	}
	return optStr(m.Overview)
}

func (s *Server) musicArtists(rows []*item.ArtistView) ([]api.MusicArtist, error) {
	metas, genres, err := s.musicExtras(mapAll(rows, func(v *item.ArtistView) *item.Item { return v.Item }))
	if err != nil {
		return nil, err
	}
	return mapAll(rows, func(v *item.ArtistView) api.MusicArtist {
		m := metas[v.ID]
		return api.MusicArtist{Id: v.ID, LibraryId: v.LibraryID, Name: v.ParsedTitle, SortName: cmp.Or(v.SortTitle, v.ParsedTitle),
			AlbumCount: v.AlbumCount, SongCount: v.SongCount, Overview: overview(m), Genres: mapAll(genres[v.ID], toGenre),
			MusicBrainzId: mbid(m), AddedAt: v.AddedAt}
	}), nil
}

func (s *Server) musicAlbums(rows []*item.AlbumView) ([]api.MusicAlbum, error) {
	metas, genres, err := s.musicExtras(mapAll(rows, func(v *item.AlbumView) *item.Item { return v.Item }))
	if err != nil {
		return nil, err
	}
	return mapAll(rows, func(v *item.AlbumView) api.MusicAlbum {
		m := metas[v.ID]
		return api.MusicAlbum{Id: v.ID, LibraryId: v.LibraryID, ArtistId: *v.ParentID, ArtistName: v.ArtistName, Title: v.ParsedTitle,
			Year: v.ParsedYear, SongCount: v.SongCount, DurationMs: v.DurationMs, Lossless: v.Lossless, Overview: overview(m),
			Genres: mapAll(genres[v.ID], toGenre), MusicBrainzId: mbid(m), LastPlayedAt: v.LastPlayedAt, AddedAt: v.AddedAt}
	}), nil
}

func toSong(v *item.SongView) api.MusicSong {
	return api.MusicSong{Id: v.ID, LibraryId: v.LibraryID, AlbumId: *v.ParentID, AlbumTitle: v.AlbumTitle, AlbumArtistId: v.AlbumArtistID,
		ArtistId: v.PerformerID, ArtistName: v.ArtistName, Title: v.ParsedTitle, DiscNumber: v.DiscNumber, TrackNumber: v.TrackNumber,
		DurationMs: v.DurationMs, Year: v.Year, Codec: optStr(v.Codec), Lossless: v.Lossless, Unavailable: optTrue(v.Unavailable)}
}

// one returns the single row a by-id view found, or not found.
func one[T any](rows []T, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, errNotFound
	}
	return rows[0], nil
}

// ListMusicArtists pages artists A to Z by sort name.
func (s *Server) ListMusicArtists(ctx context.Context, r api.ListMusicArtistsRequestObject) (api.ListMusicArtistsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	out := api.ListMusicArtists200JSONResponse{Items: []api.MusicArtist{}}
	f := item.MusicFilter{LibraryID: r.Params.LibraryId, GenreID: r.Params.GenreId,
		Letter: or(r.Params.Letter, ""), Q: or(r.Params.Q, ""), Clean: clean(ctx), Limit: limit + 1, Offset: offset}
	rows, err := s.svc.Music.Repo().Artists(f)
	if err != nil {
		return nil, err
	}
	if out.Total, err = s.svc.Music.Repo().CountArtists(f); err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	out.Items, err = s.musicArtists(rows)
	return out, err
}

// GetMusicArtist returns one artist.
func (s *Server) GetMusicArtist(ctx context.Context, r api.GetMusicArtistRequestObject) (api.GetMusicArtistResponseObject, error) {
	row, err := one(s.svc.Music.Repo().Artists(item.MusicFilter{IDs: []uid.ID{r.ArtistId}, Clean: clean(ctx)}))
	if err != nil {
		return nil, err
	}
	v, err := one(s.musicArtists([]*item.ArtistView{row}))
	return api.GetMusicArtist200JSONResponse(v), err
}

// ListMusicAlbums pages albums.
func (s *Server) ListMusicAlbums(ctx context.Context, r api.ListMusicAlbumsRequestObject) (api.ListMusicAlbumsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	out := api.ListMusicAlbums200JSONResponse{Items: []api.MusicAlbum{}}
	f := item.MusicFilter{LibraryID: r.Params.LibraryId, ArtistID: r.Params.ArtistId,
		GenreID: r.Params.GenreId, Letter: or(r.Params.Letter, ""), Q: or(r.Params.Q, ""), Sort: enumStr(r.Params.Sort),
		AccountID: &caller(ctx).profile.ID, Clean: clean(ctx), Limit: limit + 1, Offset: offset}
	rows, err := s.svc.Music.Repo().Albums(f)
	if err != nil {
		return nil, err
	}
	if out.Total, err = s.svc.Music.Repo().CountAlbums(f); err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	out.Items, err = s.musicAlbums(rows)
	return out, err
}

// GetMusicAlbum returns one album.
func (s *Server) GetMusicAlbum(ctx context.Context, r api.GetMusicAlbumRequestObject) (api.GetMusicAlbumResponseObject, error) {
	row, err := one(s.svc.Music.Repo().Albums(item.MusicFilter{IDs: []uid.ID{r.AlbumId}, AccountID: &caller(ctx).profile.ID, Clean: clean(ctx)}))
	if err != nil {
		return nil, err
	}
	v, err := one(s.musicAlbums([]*item.AlbumView{row}))
	return api.GetMusicAlbum200JSONResponse(v), err
}

// ListMusicSongs pages songs.
func (s *Server) ListMusicSongs(ctx context.Context, r api.ListMusicSongsRequestObject) (api.ListMusicSongsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	out := api.ListMusicSongs200JSONResponse{Items: []api.MusicSong{}}
	f := item.MusicFilter{LibraryID: r.Params.LibraryId, AlbumID: r.Params.AlbumId,
		ArtistID: r.Params.ArtistId, GenreID: r.Params.GenreId, Q: or(r.Params.Q, ""), Sort: enumStr(r.Params.Sort),
		Clean: clean(ctx), Limit: limit + 1, Offset: offset}
	rows, err := s.svc.Music.Repo().Songs(f)
	if err != nil {
		return nil, err
	}
	if out.Total, err = s.svc.Music.Repo().CountSongs(f); err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	out.Items = mapAll(rows, toSong)
	return out, nil
}

// GetMusicSong returns one song.
func (s *Server) GetMusicSong(ctx context.Context, r api.GetMusicSongRequestObject) (api.GetMusicSongResponseObject, error) {
	row, err := one(s.svc.Music.Repo().Songs(item.MusicFilter{IDs: []uid.ID{r.SongId}, Clean: clean(ctx)}))
	if err != nil {
		return nil, err
	}
	return api.GetMusicSong200JSONResponse(toSong(row)), nil
}

// ListMusicLetters counts artists or albums per letter.
func (s *Server) ListMusicLetters(ctx context.Context, r api.ListMusicLettersRequestObject) (api.ListMusicLettersResponseObject, error) {
	out := api.ListMusicLetters200JSONResponse{Items: []api.MusicLetterCount{}}
	kind := item.Album
	if r.Params.Of == api.Artists {
		kind = item.Artist
	}
	rows, err := s.svc.Music.Repo().Letters(r.Params.LibraryId, kind, clean(ctx))
	if err != nil {
		return nil, err
	}
	out.Items = mapAll(rows, func(c item.LetterCount) api.MusicLetterCount {
		return api.MusicLetterCount{Letter: c.Letter, Count: c.Count}
	})
	return out, nil
}

// SearchMusic finds artists, albums and songs, prefix matches first.
func (s *Server) SearchMusic(ctx context.Context, r api.SearchMusicRequestObject) (api.SearchMusicResponseObject, error) {
	out := api.SearchMusic200JSONResponse{Artists: []api.MusicArtist{}, Albums: []api.MusicAlbum{}, Songs: []api.MusicSong{}}
	f := item.MusicFilter{LibraryID: r.Params.LibraryId, Q: r.Params.Q, AccountID: &caller(ctx).profile.ID, Clean: clean(ctx),
		Limit: or(r.Params.Limit, 20)}
	repo := s.svc.Music.Repo()
	artists, err := repo.Artists(f)
	if err != nil {
		return nil, err
	}
	albums, err := repo.Albums(f)
	if err != nil {
		return nil, err
	}
	songs, err := repo.Songs(f)
	if err != nil {
		return nil, err
	}
	if out.Artists, err = s.musicArtists(artists); err != nil {
		return nil, err
	}
	out.Albums, err = s.musicAlbums(albums)
	out.Songs = mapAll(songs, toSong)
	return out, err
}

// ListRecentlyPlayedMusic pages what the profile played last. A kid has no
// playlists, so mixes only albums, and is shown nothing explicit: a row
// whose album or artist the kid may not hear is left out.
func (s *Server) ListRecentlyPlayedMusic(ctx context.Context, r api.ListRecentlyPlayedMusicRequestObject) (api.ListRecentlyPlayedMusicResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	out := api.ListRecentlyPlayedMusic200JSONResponse{Items: []api.MusicRecent{}}
	kid, kind := who.Kind == account.Kid, enumStr(r.Params.Kind)
	if kid && kind == "" {
		kind = item.Album
	}
	if kid && kind == "playlist" {
		return out, nil
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Music.Repo().RecentMusic(r.ProfileId, kind, limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	byKind := map[string][]uid.ID{}
	for _, v := range rows {
		byKind[v.Kind] = append(byKind[v.Kind], v.ID)
	}
	albums, artists := map[uid.ID]api.MusicAlbum{}, map[uid.ID]api.MusicArtist{}
	if ids := byKind[item.Album]; len(ids) > 0 {
		views, err := s.svc.Music.Repo().Albums(item.MusicFilter{IDs: ids, AccountID: &r.ProfileId, Clean: kid, Limit: len(ids)})
		if err != nil {
			return nil, err
		}
		list, err := s.musicAlbums(views)
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			albums[a.Id] = a
		}
	}
	if ids := byKind[item.Artist]; len(ids) > 0 {
		views, err := s.svc.Music.Repo().Artists(item.MusicFilter{IDs: ids, Clean: kid, Limit: len(ids)})
		if err != nil {
			return nil, err
		}
		list, err := s.musicArtists(views)
		if err != nil {
			return nil, err
		}
		for _, a := range list {
			artists[a.Id] = a
		}
	}
	for _, v := range rows {
		rec := api.MusicRecent{Kind: api.MusicRecentKind(v.Kind), PlayedAt: v.PlayedAt}
		switch v.Kind {
		case item.Album:
			a, ok := albums[v.ID]
			if !ok {
				continue
			}
			rec.Album = &a
		case item.Artist:
			a, ok := artists[v.ID]
			if !ok {
				continue
			}
			rec.Artist = &a
		default:
			p, err := s.svc.Playlists.Repo().Get(v.ID)
			if err != nil {
				return nil, err
			}
			pl, err := s.toPlaylist(ctx, p, r.ProfileId)
			if err != nil {
				return nil, err
			}
			rec.Playlist = &pl
		}
		out.Items = append(out.Items, rec)
	}
	return out, nil
}
