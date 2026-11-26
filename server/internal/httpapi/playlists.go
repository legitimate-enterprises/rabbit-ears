package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/collection"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// Playlists: under a profile a playlist is that profile's own or shared
// with it; any other is not found. Kids own none and only read what is
// shared with them.

// playlistProfile resolves the profile a playlist request names. Anything
// beyond reading, for a kid's profile or while a kid is active, is
// forbidden.
func (s *Server) playlistProfile(ctx context.Context, profileID int64, need string) (*account.Account, error) {
	who, err := s.profile(ctx, profileID)
	if err == nil && need != playlist.Read && (who.Kind == account.Kid || caller(ctx).asKid()) {
		return nil, errForbidden
	}
	return who, err
}

// playlistOf finds a playlist the profile owns or that is shared with it,
// else not found; access short of need is forbidden.
func (s *Server) playlistOf(ctx context.Context, profileID, id int64, need string) (*playlist.Playlist, error) {
	if _, err := s.playlistProfile(ctx, profileID, need); err != nil {
		return nil, err
	}
	p, err := s.svc.Playlists.Repo().Get(id)
	if err != nil {
		return nil, err
	}
	_, err = s.svc.Playlists.Check(p, profileID, need)
	return p, playlistError(err)
}

func (s *Server) entryOf(p *playlist.Playlist, id int64) (*playlist.Entry, error) {
	e, err := s.svc.Playlists.Repo().Entry(id)
	if err == nil && e.PlaylistID != p.ID {
		return nil, errNotFound
	}
	return e, err
}

func playlistName(name string) (string, error) {
	if name = strings.TrimSpace(name); name == "" {
		return "", invalid("name", "must not be blank")
	}
	return name, nil
}

func playlistError(err error) error {
	switch {
	case errors.Is(err, service.ErrPlaylistKindLocked):
		return fail(409, api.ErrorCodeConflict, err.Error())
	case errors.Is(err, service.ErrWrongPlaylistKind):
		return invalid("itemId", err.Error())
	case errors.Is(err, service.ErrEditionRequired), errors.Is(err, service.ErrNotItsEdition):
		return invalid("editionId", err.Error())
	case errors.Is(err, service.ErrNotShared):
		return errNotFound
	case errors.Is(err, service.ErrAccessDenied):
		return errForbidden
	case errors.Is(err, service.ErrShareTarget):
		return invalid("profileId", err.Error())
	case errors.Is(err, service.ErrHouseholdShareTarget):
		return invalid("householdId", err.Error())
	case errors.Is(err, service.ErrKidReadsOnly):
		return invalid("access", err.Error())
	case errors.Is(err, service.ErrStationPlaylist), errors.Is(err, service.ErrPlaylistOnAir):
		return fail(409, api.ErrorCodeConflict, err.Error())
	case errors.Is(err, db.ErrConflict):
		return fail(409, api.ErrorCodeConflict, "already shared with that profile")
	}
	return err
}

// withShow is a catalog item with its show beside a season or episode, with
// paths as the caller sees them.
func (s *Server) withShow(it *item.Item, admin bool) (api.Item, *api.Item, error) {
	v, err := s.item(it, admin)
	if err != nil || it.Kind != item.Season && it.Kind != item.Episode {
		return v, nil, err
	}
	up, err := s.svc.Items.Repo().Get(*it.ParentID)
	if err == nil && up.Kind == item.Season {
		up, err = s.svc.Items.Repo().Get(*up.ParentID)
	}
	if err != nil {
		return v, nil, err
	}
	show, err := s.item(up, admin)
	return v, &show, err
}

// toEntry fills the one of item, artist, album, song or collection that the
// entry's kind calls for, and what it stands for in the play order now.
func (s *Server) toEntry(ctx context.Context, e *playlist.Entry) (api.PlaylistEntry, error) {
	var t playlist.Total
	var err error
	if t.Count, t.DurationMs, err = s.svc.Playlists.Repo().EntryTotals(e.ID, browse(ctx)); err != nil {
		return api.PlaylistEntry{}, err
	}
	return s.entryWith(ctx, e, t)
}

// entryWith is toEntry with the entry's totals already counted.
func (s *Server) entryWith(ctx context.Context, e *playlist.Entry, t playlist.Total) (api.PlaylistEntry, error) {
	out := api.PlaylistEntry{Id: e.ID, PlaylistId: e.PlaylistID, Position: e.Position, AddedAt: e.AddedAt, ItemCount: t.Count, DurationMs: t.DurationMs}
	if e.CollectionID != 0 {
		c, err := s.svc.Collections.Repo().Get(e.CollectionID)
		if err != nil {
			return out, err
		}
		v := toCollection(&collection.View{Collection: *c, MovieCount: out.ItemCount})
		out.Kind, out.CollectionId, out.Collection = api.PlaylistEntryKindCollection, &e.CollectionID, &v
		return out, nil
	}
	it, err := s.svc.Items.Repo().Get(e.ItemID)
	if err != nil {
		return out, err
	}
	out.ItemId, out.Kind = &e.ItemID, api.PlaylistEntryKind(it.Kind)
	if e.EditionID != 0 {
		name, err := s.editionName(e.ItemID, e.EditionID)
		if err != nil {
			return out, err
		}
		out.EditionId, out.EditionName = &e.EditionID, name
	}
	only := item.MusicFilter{IDs: []int64{it.ID}, AccountID: caller(ctx).profile.ID}
	switch it.Kind {
	case item.Song:
		v, err := one(s.svc.Music.Repo().Songs(only))
		if err != nil {
			return out, err
		}
		song := toSong(v)
		out.Song = &song
	case item.Album:
		views, err := s.svc.Music.Repo().Albums(only)
		if err != nil {
			return out, err
		}
		v, err := one(s.musicAlbums(views))
		out.Album = &v
		return out, err
	case item.Artist:
		views, err := s.svc.Music.Repo().Artists(only)
		if err != nil {
			return out, err
		}
		v, err := one(s.musicArtists(views))
		out.Artist = &v
		return out, err
	default:
		v, show, err := s.withShow(it, caller(ctx).asAdmin())
		out.Item, out.Show = &v, show
		return out, err
	}
	return out, nil
}

// ListPlaylists pages by name the profile's playlists and those shared
// with it.
func (s *Server) ListPlaylists(ctx context.Context, r api.ListPlaylistsRequestObject) (api.ListPlaylistsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	out := api.ListPlaylists200JSONResponse{Items: []api.Playlist{}}
	if _, err := s.playlistProfile(ctx, r.ProfileId, playlist.Read); err != nil {
		return nil, err
	}
	rows, err := s.svc.Playlists.Repo().List(r.ProfileId, enumStr(r.Params.Kind), limit+1, offset)
	if err != nil {
		return nil, err
	}
	if out.Total, err = s.svc.Playlists.Repo().Count(r.ProfileId, enumStr(r.Params.Kind)); err != nil {
		return nil, err
	}
	rows, out.NextOffset = window(rows, limit, offset)
	for _, p := range rows {
		v, err := s.toPlaylist(ctx, p, r.ProfileId)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// CreatePlaylist makes an empty playlist.
func (s *Server) CreatePlaylist(ctx context.Context, r api.CreatePlaylistRequestObject) (api.CreatePlaylistResponseObject, error) {
	if _, err := s.playlistProfile(ctx, r.ProfileId, service.Owner); err != nil {
		return nil, err
	}
	name, err := playlistName(r.Body.Name)
	if err != nil {
		return nil, err
	}
	p := &playlist.Playlist{AccountID: r.ProfileId, Name: name, Kind: string(r.Body.Kind)}
	if err := s.svc.Playlists.Repo().Create(p); err != nil {
		return nil, err
	}
	v, err := s.toPlaylist(ctx, p, r.ProfileId)
	return api.CreatePlaylist201JSONResponse(v), err
}

// GetPlaylist returns one playlist the profile owns or that is shared with it.
func (s *Server) GetPlaylist(ctx context.Context, r api.GetPlaylistRequestObject) (api.GetPlaylistResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Read)
	if err != nil {
		return nil, err
	}
	v, err := s.toPlaylist(ctx, p, r.ProfileId)
	return api.GetPlaylist200JSONResponse(v), err
}

// ReplacePlaylist renames a playlist, and changes its kind while it is empty.
func (s *Server) ReplacePlaylist(ctx context.Context, r api.ReplacePlaylistRequestObject) (api.ReplacePlaylistResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	name, err := playlistName(r.Body.Name)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Playlists.Rename(p, name, string(r.Body.Kind)); err != nil {
		return nil, playlistError(err)
	}
	v, err := s.toPlaylist(ctx, p, r.ProfileId)
	return api.ReplacePlaylist200JSONResponse(v), err
}

// DeletePlaylist deletes a playlist with its entries and shares, unless one
// of its owner's stations plays it.
func (s *Server) DeletePlaylist(ctx context.Context, r api.DeletePlaylistRequestObject) (api.DeletePlaylistResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	return api.DeletePlaylist204Response{}, playlistError(s.svc.Playlists.Delete(p))
}

// ListPlaylistEntries pages a playlist's entries in order, for a kid only
// those that stand for something the kid may play.
func (s *Server) ListPlaylistEntries(ctx context.Context, r api.ListPlaylistEntriesRequestObject) (api.ListPlaylistEntriesResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Read)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Playlists.Repo().Entries(p.ID, browse(ctx), limit+1, offset)
	if err != nil {
		return nil, err
	}
	totals, err := s.svc.Playlists.Repo().EntriesTotals(p.ID, browse(ctx))
	if err != nil {
		return nil, err
	}
	out := api.ListPlaylistEntries200JSONResponse{Items: []api.PlaylistEntry{}}
	rows, out.NextOffset = window(rows, limit, offset)
	for _, e := range rows {
		v, err := s.entryWith(ctx, e, totals[e.ID])
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// ListPlaylistPlayOrder pages what playing the playlist plays to the active
// profile: each row a movie, episode or song it may see, with the entry it
// comes from.
func (s *Server) ListPlaylistPlayOrder(ctx context.Context, r api.ListPlaylistPlayOrderRequestObject) (api.ListPlaylistPlayOrderResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Read)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Playlists.Repo().PlayOrder(p.ID, browse(ctx), limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := api.ListPlaylistPlayOrder200JSONResponse{Items: []api.PlaylistPlayOrderItem{}}
	rows, out.NextOffset = window(rows, limit, offset)
	var songIDs []int64
	for _, row := range rows {
		if row.Kind == item.Song {
			songIDs = append(songIDs, row.ItemID)
		}
	}
	songs := map[int64]*item.SongView{}
	if len(songIDs) > 0 {
		views, err := s.svc.Music.Repo().Songs(item.MusicFilter{IDs: songIDs, Limit: len(songIDs)})
		if err != nil {
			return nil, err
		}
		for _, v := range views {
			songs[v.ID] = v
		}
	}
	names := map[int64]*string{}
	for _, row := range rows {
		v := api.PlaylistPlayOrderItem{EntryId: row.EntryID, ItemId: row.ItemID, Kind: api.PlaylistEntryKind(row.Kind), DurationMs: row.RuntimeMs}
		if row.EditionID != 0 {
			name, ok := names[row.EditionID]
			if !ok {
				var err error
				if name, err = s.editionName(row.ItemID, row.EditionID); err != nil {
					return nil, err
				}
				names[row.EditionID] = name
			}
			v.EditionId, v.EditionName = &row.EditionID, name
		}
		if sv, ok := songs[row.ItemID]; ok {
			song := toSong(sv)
			v.Song, v.DurationMs = &song, sv.DurationMs
		} else {
			it, err := s.svc.Items.Repo().Get(row.ItemID)
			if err != nil {
				return nil, err
			}
			got, show, err := s.withShow(it, caller(ctx).asAdmin())
			if err != nil {
				return nil, err
			}
			v.Item, v.Show = &got, show
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// AddPlaylistEntry adds an item or collection the profile may see, if the
// playlist holds that kind of thing.
func (s *Server) AddPlaylistEntry(ctx context.Context, r api.AddPlaylistEntryRequestObject) (api.AddPlaylistEntryResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Write)
	if err != nil {
		return nil, err
	}
	in := r.Body
	if (in.ItemId == nil) == (in.CollectionId == nil) {
		return nil, invalid("itemId", "name exactly one of itemId and collectionId")
	}
	var e *playlist.Entry
	if in.CollectionId != nil {
		if in.EditionId != nil {
			return nil, invalid("editionId", service.ErrNotItsEdition.Error())
		}
		v, err := s.seenCollection(ctx, *in.CollectionId)
		if err != nil {
			return nil, err
		}
		if e, err = s.svc.Playlists.AddCollection(p, &v.Collection, in.Position); errors.Is(err, service.ErrWrongPlaylistKind) {
			return nil, invalid("collectionId", err.Error())
		} else if err != nil {
			return nil, err
		}
	} else {
		it, err := s.seen(ctx, *in.ItemId)
		if err != nil {
			return nil, err
		}
		if e, err = s.svc.Playlists.AddEntry(p, it, or(in.EditionId, 0), in.Position); err != nil {
			return nil, playlistError(err)
		}
	}
	v, err := s.toEntry(ctx, e)
	return api.AddPlaylistEntry201JSONResponse(v), err
}

// GetPlaylistEntry returns one entry; to a kid, one that stands for
// nothing the kid may play is not found.
func (s *Server) GetPlaylistEntry(ctx context.Context, r api.GetPlaylistEntryRequestObject) (api.GetPlaylistEntryResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Read)
	if err != nil {
		return nil, err
	}
	e, err := s.entryOf(p, r.EntryId)
	if err != nil {
		return nil, err
	}
	if a := browse(ctx); a.Kid || a.HideKids {
		if n, _, err := s.svc.Playlists.Repo().EntryTotals(e.ID, a); err != nil || n == 0 {
			return nil, orNotFound(err)
		}
	}
	v, err := s.toEntry(ctx, e)
	return api.GetPlaylistEntry200JSONResponse(v), err
}

// ReplacePlaylistEntry moves an entry; its item or collection cannot change.
func (s *Server) ReplacePlaylistEntry(ctx context.Context, r api.ReplacePlaylistEntryRequestObject) (api.ReplacePlaylistEntryResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Write)
	if err != nil {
		return nil, err
	}
	e, err := s.entryOf(p, r.EntryId)
	if err != nil {
		return nil, err
	}
	if or(r.Body.ItemId, 0) != e.ItemID || or(r.Body.CollectionId, 0) != e.CollectionID {
		return nil, invalid("itemId", "an entry only moves; add the other item instead")
	}
	if r.Body.EditionId != nil && *r.Body.EditionId != e.EditionID {
		return nil, invalid("editionId", "an entry keeps its edition; add the other edition instead")
	}
	if err := s.svc.Playlists.Repo().MoveEntry(e.ID, r.Body.Position); err != nil {
		return nil, err
	}
	if e, err = s.entryOf(p, e.ID); err != nil {
		return nil, err
	}
	v, err := s.toEntry(ctx, e)
	return api.ReplacePlaylistEntry200JSONResponse(v), err
}

// RemovePlaylistEntry removes one entry; later ones move up.
func (s *Server) RemovePlaylistEntry(ctx context.Context, r api.RemovePlaylistEntryRequestObject) (api.RemovePlaylistEntryResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, playlist.Write)
	if err != nil {
		return nil, err
	}
	e, err := s.entryOf(p, r.EntryId)
	if err != nil {
		return nil, err
	}
	return api.RemovePlaylistEntry204Response{}, s.svc.Playlists.Repo().RemoveEntry(e.ID)
}

// editionName is the name a playlist row shows for its edition: nil while
// the title has at most one edition with files, so a lone cut goes unnamed.
func (s *Server) editionName(itemID, editionID int64) (*string, error) {
	eds, err := s.svc.Editions.Repo().ForItem(itemID)
	if err != nil || len(eds) < 2 {
		return nil, err
	}
	ed, err := s.svc.Editions.Repo().Get(editionID)
	if err != nil {
		return nil, err
	}
	return &ed.Name, nil
}
