package httpapi

import (
	"context"
	"errors"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
)

// Playlist shares: the owner alone reads and changes who a playlist is
// shared with; anyone it is shared with gets 403.

// toPlaylist is a playlist with its totals over what the active profile may
// see, and what profileID may do with it, else not found.
func (s *Server) toPlaylist(ctx context.Context, p *playlist.Playlist, profileID int64) (api.Playlist, error) {
	access, err := s.svc.Playlists.Access(p, profileID)
	if err != nil {
		return api.Playlist{}, playlistError(err)
	}
	n, ms, first, err := s.svc.Playlists.Repo().Totals(p.ID, browse(ctx))
	shared := access != service.Owner || p.Shared
	out := api.Playlist{Id: p.ID, ProfileId: p.AccountID, Name: p.Name, Kind: api.PlaylistKind(p.Kind), Access: api.PlaylistAccess(access),
		Shared: &shared, ItemCount: n, DurationMs: ms, ArtItemId: first, LastPlayedAt: p.LastPlayedAt,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
	if err != nil {
		return out, err
	}
	c, err := s.svc.Covers.Shown(playlistCover(p), p.ID, browse(ctx))
	out.ImageUrl = coverURL(playlistBase(profileID, p), c)
	if err != nil || access == service.Owner {
		return out, err
	}
	owner, err := s.svc.Accounts.Repo().Get(p.AccountID)
	if err == nil {
		out.OwnerName = &owner.Name
	}
	return out, err
}

// toShare is sh with to, labeled with its household in labels.
func toShare(sh *playlist.Share, to *account.Account, labels map[int64]account.Label) api.PlaylistShare {
	color := to.ShownColor()
	out := api.PlaylistShare{PlaylistId: sh.PlaylistID, ProfileId: sh.AccountID, Name: to.Name, Kind: api.AccountKind(to.Kind),
		Access: api.PlaylistShareAccess(sh.Access), CreatedAt: sh.CreatedAt, Color: &color}
	if l, ok := labels[to.ID]; ok {
		out.HouseholdId = &l.HouseholdID
	}
	return out
}

// labels is the household each of ids is labeled with to the owner.
func (s *Server) labels(ownerID int64, ids ...int64) (map[int64]account.Label, error) {
	owner, err := s.svc.Accounts.Repo().Get(ownerID)
	if err != nil {
		return nil, err
	}
	return s.svc.Accounts.Repo().Labels(owner, ids)
}

// oneShare is sh with to as the owner sees it.
func (s *Server) oneShare(ownerID int64, sh *playlist.Share, to *account.Account) (api.PlaylistShare, error) {
	labels, err := s.labels(ownerID, to.ID)
	return toShare(sh, to, labels), err
}

// shareOf finds the owned playlist's share with a profile, and that profile.
func (s *Server) shareOf(ctx context.Context, profileID, playlistID, with int64) (*playlist.Share, *account.Account, error) {
	p, err := s.playlistOf(ctx, profileID, playlistID, service.Owner)
	if err != nil {
		return nil, nil, err
	}
	sh, err := s.svc.Playlists.Repo().Share(p.ID, with)
	if err != nil {
		return nil, nil, err
	}
	to, err := s.svc.Accounts.Repo().Get(with)
	return sh, to, err
}

// ListPlaylistShares pages who the playlist is shared with.
func (s *Server) ListPlaylistShares(ctx context.Context, r api.ListPlaylistSharesRequestObject) (api.ListPlaylistSharesResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Playlists.Repo().Shares(p.ID, limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := api.ListPlaylistShares200JSONResponse{Items: []api.PlaylistShare{}}
	rows, out.NextOffset = window(rows, limit, offset)
	tos, ids := make([]*account.Account, len(rows)), make([]int64, len(rows))
	for i, sh := range rows {
		if tos[i], err = s.svc.Accounts.Repo().Get(sh.AccountID); err != nil {
			return nil, err
		}
		ids[i] = sh.AccountID
	}
	labels, err := s.labels(r.ProfileId, ids...)
	if err != nil {
		return nil, err
	}
	for i, sh := range rows {
		out.Items = append(out.Items, toShare(sh, tos[i], labels))
	}
	return out, nil
}

// CreatePlaylistShare shares the playlist with one profile.
func (s *Server) CreatePlaylistShare(ctx context.Context, r api.CreatePlaylistShareRequestObject) (api.CreatePlaylistShareResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	owner, err := s.svc.Accounts.Repo().Get(r.ProfileId)
	if err != nil {
		return nil, err
	}
	to, err := s.svc.Accounts.Repo().Get(r.Body.ProfileId)
	if errors.Is(err, db.ErrNotFound) {
		return nil, invalid("profileId", service.ErrShareTarget.Error())
	} else if err != nil {
		return nil, err
	}
	sh, err := s.svc.Playlists.Share(p, owner, to, string(r.Body.Access), device.TV(caller(ctx).device.Platform))
	if err != nil {
		return nil, playlistError(err)
	}
	v, err := s.oneShare(owner.ID, sh, to)
	return api.CreatePlaylistShare201JSONResponse(v), err
}

// GetPlaylistShare returns the share with one profile.
func (s *Server) GetPlaylistShare(ctx context.Context, r api.GetPlaylistShareRequestObject) (api.GetPlaylistShareResponseObject, error) {
	sh, to, err := s.shareOf(ctx, r.ProfileId, r.PlaylistId, r.ShareProfileId)
	if err != nil {
		return nil, err
	}
	v, err := s.oneShare(r.ProfileId, sh, to)
	return api.GetPlaylistShare200JSONResponse(v), err
}

// ReplacePlaylistShare changes the share's access.
func (s *Server) ReplacePlaylistShare(ctx context.Context, r api.ReplacePlaylistShareRequestObject) (api.ReplacePlaylistShareResponseObject, error) {
	sh, to, err := s.shareOf(ctx, r.ProfileId, r.PlaylistId, r.ShareProfileId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Playlists.SetAccess(sh, to, string(r.Body.Access)); err != nil {
		return nil, playlistError(err)
	}
	v, err := s.oneShare(r.ProfileId, sh, to)
	return api.ReplacePlaylistShare200JSONResponse(v), err
}

// DeletePlaylistShare stops sharing with one profile.
func (s *Server) DeletePlaylistShare(ctx context.Context, r api.DeletePlaylistShareRequestObject) (api.DeletePlaylistShareResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	return api.DeletePlaylistShare204Response{}, s.svc.Playlists.Unshare(p, r.ShareProfileId)
}

func (s *Server) toHouseholdShare(sh *playlist.HouseholdShare) (api.PlaylistHouseholdShare, error) {
	h, err := s.svc.Households.Repo().Get(sh.HouseholdID)
	if err != nil {
		return api.PlaylistHouseholdShare{}, err
	}
	return api.PlaylistHouseholdShare{PlaylistId: sh.PlaylistID, HouseholdId: sh.HouseholdID, Name: h.Name,
		Access: api.PlaylistShareAccess(sh.Access), CreatedAt: sh.CreatedAt}, nil
}

// householdShareOf finds the owned playlist's share with a household.
func (s *Server) householdShareOf(ctx context.Context, profileID, playlistID, householdID int64) (*playlist.HouseholdShare, error) {
	p, err := s.playlistOf(ctx, profileID, playlistID, service.Owner)
	if err != nil {
		return nil, err
	}
	return s.svc.Playlists.Repo().HouseholdShare(p.ID, householdID)
}

// ListPlaylistHouseholdShares pages which households the playlist is shared
// with.
func (s *Server) ListPlaylistHouseholdShares(ctx context.Context, r api.ListPlaylistHouseholdSharesRequestObject) (api.ListPlaylistHouseholdSharesResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Playlists.Repo().HouseholdShares(p.ID, limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := api.ListPlaylistHouseholdShares200JSONResponse{Items: []api.PlaylistHouseholdShare{}}
	rows, out.NextOffset = window(rows, limit, offset)
	for _, sh := range rows {
		v, err := s.toHouseholdShare(sh)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// CreatePlaylistHouseholdShare shares the playlist with one household.
func (s *Server) CreatePlaylistHouseholdShare(ctx context.Context, r api.CreatePlaylistHouseholdShareRequestObject) (api.CreatePlaylistHouseholdShareResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	owner, err := s.svc.Accounts.Repo().Get(r.ProfileId)
	if err != nil {
		return nil, err
	}
	sh, err := s.svc.Playlists.ShareHousehold(p, owner, r.Body.HouseholdId, string(r.Body.Access))
	if errors.Is(err, db.ErrConflict) {
		return nil, fail(409, api.ErrorCodeConflict, "already shared with that household")
	} else if err != nil {
		return nil, playlistError(err)
	}
	v, err := s.toHouseholdShare(sh)
	return api.CreatePlaylistHouseholdShare201JSONResponse(v), err
}

// GetPlaylistHouseholdShare returns the share with one household.
func (s *Server) GetPlaylistHouseholdShare(ctx context.Context, r api.GetPlaylistHouseholdShareRequestObject) (api.GetPlaylistHouseholdShareResponseObject, error) {
	sh, err := s.householdShareOf(ctx, r.ProfileId, r.PlaylistId, r.HouseholdId)
	if err != nil {
		return nil, err
	}
	v, err := s.toHouseholdShare(sh)
	return api.GetPlaylistHouseholdShare200JSONResponse(v), err
}

// ReplacePlaylistHouseholdShare changes the household share's access.
func (s *Server) ReplacePlaylistHouseholdShare(ctx context.Context, r api.ReplacePlaylistHouseholdShareRequestObject) (api.ReplacePlaylistHouseholdShareResponseObject, error) {
	sh, err := s.householdShareOf(ctx, r.ProfileId, r.PlaylistId, r.HouseholdId)
	if err != nil {
		return nil, err
	}
	sh.Access = string(r.Body.Access)
	if err := s.svc.Playlists.Repo().UpdateHouseholdShare(sh); err != nil {
		return nil, err
	}
	v, err := s.toHouseholdShare(sh)
	return api.ReplacePlaylistHouseholdShare200JSONResponse(v), err
}

// DeletePlaylistHouseholdShare stops sharing with one household.
func (s *Server) DeletePlaylistHouseholdShare(ctx context.Context, r api.DeletePlaylistHouseholdShareRequestObject) (api.DeletePlaylistHouseholdShareResponseObject, error) {
	p, err := s.playlistOf(ctx, r.ProfileId, r.PlaylistId, service.Owner)
	if err != nil {
		return nil, err
	}
	return api.DeletePlaylistHouseholdShare204Response{}, s.svc.Playlists.UnshareHousehold(p, r.HouseholdId)
}

// ListShareCandidates pages who the adult may share a playlist with: the
// People they see on the calling device.
func (s *Server) ListShareCandidates(ctx context.Context, r api.ListShareCandidatesRequestObject) (api.ListShareCandidatesResponseObject, error) {
	who, err := s.playlistProfile(ctx, r.ProfileId, service.Owner)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Accounts.Repo().People(who, device.TV(caller(ctx).device.Platform), limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := api.ListShareCandidates200JSONResponse{Items: []api.ShareCandidate{}}
	rows, out.NextOffset = window(rows, limit, offset)
	ids := make([]int64, len(rows))
	for i, a := range rows {
		ids[i] = a.ID
	}
	labels, err := s.svc.Accounts.Repo().Labels(who, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range rows {
		l, color := labels[a.ID], a.ShownColor()
		out.Items = append(out.Items, api.ShareCandidate{ProfileId: a.ID, Name: a.Name, Kind: api.AccountKind(a.Kind),
			HouseholdName: l.HouseholdName, HouseholdId: &l.HouseholdID, Color: &color})
	}
	return out, nil
}

// ListShareHouseholds lists the households the adult may share a playlist
// with: those they are in, their own first, each with its size.
func (s *Server) ListShareHouseholds(ctx context.Context, r api.ListShareHouseholdsRequestObject) (api.ListShareHouseholdsResponseObject, error) {
	if _, err := s.playlistProfile(ctx, r.ProfileId, service.Owner); err != nil {
		return nil, err
	}
	rows, err := s.svc.Households.Repo().Of(r.ProfileId)
	if err != nil {
		return nil, err
	}
	out := api.ListShareHouseholds200JSONResponse{Items: []api.ShareHousehold{}}
	for _, h := range rows {
		out.Items = append(out.Items, api.ShareHousehold{Id: h.ID, Name: h.Name, Profiles: h.Profiles})
	}
	return out, nil
}
