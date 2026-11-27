package httpapi

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

// member resolves a member of the device household, anything else being not
// found. While a kid is active, every other profile is forbidden.
func (s *Server) member(ctx context.Context, id int64) (*account.Account, error) {
	a, err := s.svc.Accounts.Profile(caller(ctx).device.HouseholdID, id)
	if err == nil && caller(ctx).asKid() && id != caller(ctx).profile.ID {
		return nil, errForbidden
	}
	return a, err
}

// profile resolves a profile whose own data the calling device reaches: its
// active profile or, while an adult is active, a kid of its household.
// Another adult's is not found; the device reaches it by switching to them,
// which asks their PIN.
func (s *Server) profile(ctx context.Context, id int64) (*account.Account, error) {
	a, err := s.member(ctx, id)
	if err == nil && a.Kind == account.Adult && a.ID != caller(ctx).profile.ID {
		return nil, errNotFound
	}
	return a, err
}

// managed resolves a profile an adult of the device household active on it
// may change.
func (s *Server) managed(ctx context.Context, id int64) (*account.Account, error) {
	a, err := s.member(ctx, id)
	if err == nil && !caller(ctx).manages() {
		return nil, errForbidden
	}
	return a, err
}

// pinTarget resolves a kid, or the active adult themselves: each adult owns
// their PIN, present or not.
func (s *Server) pinTarget(ctx context.Context, id int64) (*account.Account, error) {
	if p := caller(ctx); id == p.profile.ID && !p.asKid() {
		return p.profile, nil
	}
	a, err := s.managed(ctx, id)
	if err == nil && a.Kind == account.Adult {
		return nil, errForbidden
	}
	return a, err
}

func (s *Server) kid(ctx context.Context, id int64) (*account.Account, error) {
	a, err := s.managed(ctx, id)
	if err == nil && a.Kind != account.Kid {
		return nil, errForbidden
	}
	return a, err
}

// kidFrom applies a kid's form; a color left out is kept.
func kidFrom(in *api.ProfileInput, a *account.Account) error {
	if in.Color != nil {
		if !account.ValidColor(*in.Color) {
			return invalid("color", "not a profile color")
		}
		a.Color = *in.Color
	}
	a.Name, a.Birthday = in.Name, fromDate(&in.Birthday)
	a.FilterMode = filterModeFrom(in.FilterMode, a)
	a.MovieCeiling, a.TVCeiling = enumStr(in.MovieCeiling), enumStr(in.TvCeiling)
	suggestCeilings(a)
	return nil
}

// filterModeFrom is the mode a form asks for. Left out it keeps the stored
// one, so an incomplete request never widens what a kid sees; a new
// account has none stored and starts on ratings.
func filterModeFrom(in *api.FilterMode, a *account.Account) string {
	return string(or(in, api.FilterMode(cmp.Or(a.FilterMode, account.FilterRatings))))
}

// suggestCeilings gives a kid under ratings the ceilings suggested for their
// age where none is set. A kid with no birthday has no age to suggest from.
func suggestCeilings(a *account.Account) {
	if a.FilterMode != account.FilterRatings || a.Birthday == nil {
		return
	}
	movie, tv := account.SuggestCeilings(account.Age(*a.Birthday, time.Now().UTC()))
	a.MovieCeiling, a.TVCeiling = cmp.Or(a.MovieCeiling, movie), cmp.Or(a.TVCeiling, tv)
}

// GetMe describes the calling device and whom it may play as.
func (s *Server) GetMe(ctx context.Context, _ api.GetMeRequestObject) (api.GetMeResponseObject, error) {
	me, err := s.me(caller(ctx))
	if err != nil {
		return nil, err
	}
	as := []*api.Account{&me.Account}
	for i := range me.Profiles {
		as = append(as, &me.Profiles[i])
	}
	s.withAvatars(ctx, as...)
	return api.GetMe200JSONResponse(me), nil
}

// withAvatars fills avatarUrl on each adult whose email has a Gravatar,
// while the gravatar setting is on. A picture that cannot be had is left
// out, so Gravatar never fails a request.
func (s *Server) withAvatars(ctx context.Context, as ...*api.Account) {
	if !s.gravatarOn() {
		return
	}
	for _, a := range as {
		if a.Kind != api.Adult || a.Email == nil {
			continue
		}
		path, err := s.svc.Avatars.Picture(ctx, *a.Email)
		if err != nil {
			logstore.Log("gravatar").Warn("Gravatar picture unavailable", "err", err)
		}
		if path != "" {
			a.AvatarUrl = optStr(fmt.Sprintf("%s/profiles/%d/avatar", BasePath, a.Id))
		}
	}
}

// placed fills, on profiles shown on a device of a household, how many
// households each is in and whether an adult is only present in that one.
func (s *Server) placed(householdID int64, as ...*api.Account) error {
	for _, a := range as {
		hs, err := s.svc.Accounts.Repo().Households(&account.Account{ID: a.Id, HouseholdID: a.HouseholdId})
		if err != nil {
			return err
		}
		n, present := len(hs), a.Kind == api.Adult && (a.HouseholdId == nil || *a.HouseholdId != householdID)
		a.Households, a.Present = &n, &present
	}
	return nil
}

// shown is toAccount for a device of the calling household.
func (s *Server) shown(ctx context.Context, a *account.Account) (api.Account, error) {
	out := toAccount(a)
	return out, s.placed(caller(ctx).device.HouseholdID, &out)
}

func (s *Server) gravatarOn() bool {
	if s.svc.Avatars == nil {
		return false
	}
	info, err := s.svc.Server.Get()
	return err == nil && info.Gravatar
}

// GetProfileAvatar serves the server's copy of an adult's Gravatar picture
// to any device of the household, locked or not, and to the profiles that
// see them, as adults linked with them.
func (s *Server) GetProfileAvatar(ctx context.Context, r api.GetProfileAvatarRequestObject) (api.GetProfileAvatarResponseObject, error) {
	p := caller(ctx)
	a, err := s.svc.Accounts.Profile(p.device.HouseholdID, r.ProfileId)
	if errors.Is(err, db.ErrNotFound) {
		if ok, serr := s.svc.Accounts.Repo().Sees(p.profile, r.ProfileId, device.TV(p.device.Platform)); serr != nil || !ok {
			return nil, orNotFound(serr)
		}
		a, err = s.svc.Accounts.Repo().Get(r.ProfileId)
	}
	if err != nil || a.Kind != account.Adult || a.Email == "" || !s.gravatarOn() {
		return nil, orNotFound(err)
	}
	path, err := s.svc.Avatars.Picture(ctx, a.Email)
	if err != nil || path == "" {
		return nil, orNotFound(err)
	}
	b, err := os.ReadFile(path) //nolint:gosec // path is the gravatar cache's, named by a hash
	if err != nil {
		return nil, err
	}
	return api.GetProfileAvatar200ImageResponse{Body: bytes.NewReader(b), ContentType: http.DetectContentType(b), ContentLength: int64(len(b)),
		Headers: api.GetProfileAvatar200ResponseHeaders{CacheControl: artworkCache}}, nil
}

// SetProfilePreferences sets a profile's color and autoplay, an adult's own
// or a kid's of the household, and whether an adult hides kids' content,
// their own. An active kid sets their own autoplay alone.
func (s *Server) SetProfilePreferences(ctx context.Context, r api.SetProfilePreferencesRequestObject) (api.SetProfilePreferencesResponseObject, error) {
	a, err := s.member(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	self := a.ID == caller(ctx).profile.ID
	if caller(ctx).asKid() && (r.Body.Color != nil || r.Body.HideKidsContent != nil) || !self && !caller(ctx).manages() ||
		!self && a.Kind == account.Adult || r.Body.HideKidsContent != nil && a.Kind != account.Adult {
		return nil, errForbidden
	}
	if c := r.Body.Color; c != nil {
		if !account.ValidColor(*c) {
			return nil, invalid("color", "not a profile color")
		}
		a.Color = *c
	}
	a.HideKidsContent = or(r.Body.HideKidsContent, a.HideKidsContent)
	a.NoAutoplay = !or(r.Body.Autoplay, !a.NoAutoplay)
	if err := s.svc.Accounts.Repo().Update(a); err != nil {
		return nil, err
	}
	if err := s.profileChanged(a); err != nil {
		return nil, err
	}
	out, err := s.shown(ctx, a)
	s.withAvatars(ctx, &out)
	return api.SetProfilePreferences200JSONResponse(out), err
}

// profileChanged tells the devices of each of a's households that it changed.
func (s *Server) profileChanged(a *account.Account) error {
	hs, err := s.svc.Accounts.Repo().Households(a)
	if err == nil {
		s.householdChanged(hs...)
	}
	return err
}

func (s *Server) me(p principal) (api.Me, error) {
	locked := p.device.LockedAt != nil
	out := api.Me{Device: toDevice(p.device), Account: toAccount(p.account), ActiveProfileId: p.profile.ID, Locked: &locked}
	h, err := s.svc.Households.Repo().Get(p.device.HouseholdID)
	if err != nil {
		return out, err
	}
	hh := toHousehold(h)
	out.Household = &hh
	members, err := s.svc.Accounts.Repo().ByHousehold(h.ID)
	out.Profiles = []api.Account{}
	if err != nil {
		return out, err
	}
	for _, m := range members {
		if !m.Pending() || h.AdultID == p.profile.ID {
			out.Profiles = append(out.Profiles, toAccount(m))
		}
	}
	as := []*api.Account{&out.Account}
	for i := range out.Profiles {
		as = append(as, &out.Profiles[i])
	}
	return out, s.placed(h.ID, as...)
}

// signedIn answers a sign-in, listing the adult's households when they are
// in more than one.
func (s *Server) signedIn(sg *service.Signed) (api.SignInResult, error) {
	me, err := s.me(principal{device: sg.Device, account: sg.Adult, profile: sg.Adult})
	out := api.SignInResult{Token: sg.Token, Me: me}
	if err != nil {
		return out, err
	}
	hs, err := s.svc.Accounts.Repo().Households(sg.Adult)
	if err != nil || len(hs) < 2 {
		return out, err
	}
	choices := []api.HouseholdChoice{}
	for _, id := range hs {
		h, err := s.svc.Households.Repo().Get(id)
		if err != nil {
			return out, err
		}
		choices = append(choices, api.HouseholdChoice{Id: h.ID, Name: h.Name})
	}
	out.Households = &choices
	return out, nil
}

// MoveDevice moves the calling device to another household its adult is in.
// It acts as that adult again, and its playback and Watch together end, as
// what they showed belongs to the household it left.
func (s *Server) MoveDevice(ctx context.Context, r api.MoveDeviceRequestObject) (api.MoveDeviceResponseObject, error) {
	p := caller(ctx)
	if !p.asSelf() {
		return nil, errForbidden
	}
	in, err := s.svc.Accounts.Repo().In(p.account.ID, r.Body.HouseholdId)
	if err != nil || !in {
		return nil, orNotFound(err)
	}
	if err := s.svc.Devices.Repo().Move(p.device.ID, r.Body.HouseholdId); err != nil {
		return nil, err
	}
	if err := s.svc.Together.LeaveDevice(p.device.ID, 0); err != nil {
		return nil, err
	}
	return api.MoveDevice204Response{}, s.svc.Playback.EndDevice(p.device.ID)
}

// SignOut revokes the calling device.
func (s *Server) SignOut(ctx context.Context, _ api.SignOutRequestObject) (api.SignOutResponseObject, error) {
	if err := s.svc.Devices.Repo().Revoke(caller(ctx).device.ID, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.svc.Together.LeaveDevice(caller(ctx).device.ID, 0); err != nil {
		return nil, err
	}
	return api.SignOut204Response{}, s.svc.Playback.EndDevice(caller(ctx).device.ID)
}

// signedOut finishes signing out the devices a change revoked, as sign-out
// does: the sessions it ended free their encoders, and the devices leave
// Watch together and lose their local check. Those it sent back to Who's
// watching leave Watch together too.
func (s *Server) signedOut(out *service.SignedOut, err error) error {
	if err != nil {
		return err
	}
	for _, id := range out.Sessions {
		s.svc.Playback.Forget(id)
	}
	for _, id := range out.Devices {
		s.svc.Local.Forget(id)
		if err := s.svc.Together.LeaveDevice(id, 0); err != nil {
			return err
		}
	}
	for _, id := range out.Reset {
		if err := s.svc.Together.LeaveDevice(id, 0); err != nil {
			return err
		}
	}
	return nil
}

// ListProfiles lists the device household, owner first. Pending adults are
// shown to the owner only, with when their code expires.
func (s *Server) ListProfiles(ctx context.Context, _ api.ListProfilesRequestObject) (api.ListProfilesResponseObject, error) {
	p := caller(ctx)
	h, err := s.svc.Households.Repo().Get(p.device.HouseholdID)
	if err != nil {
		return nil, err
	}
	members, err := s.svc.Accounts.Repo().ByHousehold(h.ID)
	if err != nil {
		return nil, err
	}
	out := []api.Account{}
	now := time.Now().UTC()
	for _, m := range members {
		v := toAccount(m)
		s.withAvatars(ctx, &v)
		if err := s.placed(h.ID, &v); err != nil {
			return nil, err
		}
		if m.Pending() {
			if h.AdultID != p.profile.ID {
				continue
			}
			codes, err := s.svc.JoinCodes.Repo().ByAccount(m.ID)
			if err != nil {
				return nil, err
			}
			for _, j := range codes {
				if j.Usable(now) {
					v.InviteExpiresAt = &j.ExpiresAt
					break
				}
			}
		}
		out = append(out, v)
	}
	return api.ListProfiles200JSONResponse{Items: out}, nil
}

// CreateProfile adds a kid to the device household.
func (s *Server) CreateProfile(ctx context.Context, r api.CreateProfileRequestObject) (api.CreateProfileResponseObject, error) {
	if !caller(ctx).manages() {
		return nil, errForbidden
	}
	hh := caller(ctx).device.HouseholdID
	a := account.Account{Kind: account.Kid, HouseholdID: &hh}
	if err := kidFrom(r.Body, &a); err != nil {
		return nil, err
	}
	if err := s.svc.Accounts.Repo().Create(&a); err != nil {
		return nil, err
	}
	s.householdChanged(hh)
	out, err := s.shown(ctx, &a)
	return api.CreateProfile201JSONResponse(out), err
}

// GetProfile returns one member of the device household.
func (s *Server) GetProfile(ctx context.Context, r api.GetProfileRequestObject) (api.GetProfileResponseObject, error) {
	a, err := s.member(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	if a.Pending() {
		h, err := s.svc.Households.Repo().Get(caller(ctx).device.HouseholdID)
		if err != nil {
			return nil, err
		}
		if h.AdultID != caller(ctx).profile.ID {
			return nil, db.ErrNotFound
		}
	}
	out, err := s.shown(ctx, a)
	s.withAvatars(ctx, &out)
	return api.GetProfile200JSONResponse(out), err
}

// ReplaceProfile rewrites a kid of the device household.
func (s *Server) ReplaceProfile(ctx context.Context, r api.ReplaceProfileRequestObject) (api.ReplaceProfileResponseObject, error) {
	a, err := s.kid(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	if err := kidFrom(r.Body, a); err != nil {
		return nil, err
	}
	if err := s.svc.Accounts.Repo().Update(a); err != nil {
		return nil, err
	}
	if err := s.profileChanged(a); err != nil {
		return nil, err
	}
	out, err := s.shown(ctx, a)
	return api.ReplaceProfile200JSONResponse(out), err
}

// DeleteProfile takes a kid, an adult present in it or, for the owner,
// another adult out of the device household; a present adult may take
// themselves out. A profile another household holds only leaves this one;
// any other is deleted, its devices with it.
func (s *Server) DeleteProfile(ctx context.Context, r api.DeleteProfileRequestObject) (api.DeleteProfileResponseObject, error) {
	p := caller(ctx)
	a, err := s.managed(ctx, r.ProfileId)
	if p.present() && r.ProfileId == p.profile.ID {
		a, err = p.profile, nil
	}
	if err != nil {
		return nil, err
	}
	hh := p.device.HouseholdID
	present := a.Kind == account.Adult && (a.HouseholdID == nil || *a.HouseholdID != hh)
	if a.Kind == account.Adult && !present {
		h, err := s.owner(ctx)
		if err != nil {
			return nil, err
		}
		if a.ID == h.AdultID {
			return nil, errForbidden
		}
	}
	hs, err := s.svc.Accounts.Repo().Households(a)
	if err != nil {
		return nil, err
	}
	if len(hs) > 1 || present {
		if err := s.signedOut(s.svc.Households.Leave(a, hh)); err != nil {
			return nil, err
		}
		s.householdChanged(hh)
		return api.DeleteProfile204Response{}, nil
	}
	return api.DeleteProfile204Response{}, s.deleteAccount(a)
}

// owner returns the device household when its owner is the active profile.
func (s *Server) owner(ctx context.Context) (*household.Household, error) {
	h, err := s.svc.Households.Repo().Get(caller(ctx).device.HouseholdID)
	if err == nil && h.AdultID != caller(ctx).profile.ID {
		return nil, errForbidden
	}
	return h, err
}

// ownerManaged resolves a member of the device household other than its
// owner, for the owner active on the device.
func (s *Server) ownerManaged(ctx context.Context, id int64) (*account.Account, error) {
	a, err := s.managed(ctx, id)
	if err != nil {
		return nil, err
	}
	h, err := s.owner(ctx)
	if err == nil && a.ID == h.AdultID {
		return nil, errForbidden
	}
	return a, err
}

// ResendProfileJoinCode replaces a pending adult's code with a new one.
func (s *Server) ResendProfileJoinCode(ctx context.Context, r api.ResendProfileJoinCodeRequestObject) (api.ResendProfileJoinCodeResponseObject, error) {
	a, err := s.ownerManaged(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	j, err := s.svc.Auth.ResendJoinCode(a, int(api.HouseholdAdultInputExpiresInDaysN7))
	if err != nil {
		return nil, err
	}
	audit(ctx, "Resent the join code of "+a.Name)
	return api.ResendProfileJoinCode201JSONResponse{Account: toAccount(a), JoinCode: toJoinCode(j)}, nil
}

// MakeProfileAdult turns a kid into a pending adult with a code to hand them.
func (s *Server) MakeProfileAdult(ctx context.Context, r api.MakeProfileAdultRequestObject) (api.MakeProfileAdultResponseObject, error) {
	a, err := s.ownerManaged(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	j, out, err := s.svc.Auth.MakeAdult(a, caller(ctx).device.HouseholdID, string(r.Body.Email),
		int(or(r.Body.ExpiresInDays, api.MakeAdultInputExpiresInDaysN7)))
	if err := s.signedOut(out, err); err != nil {
		return nil, err
	}
	audit(ctx, "Made "+a.Name+" an adult")
	return api.MakeProfileAdult201JSONResponse{Account: toAccount(a), JoinCode: toJoinCode(j)}, nil
}

// AddHouseholdAdult is the owner adding a pending adult to their household,
// with the join code to hand them.
func (s *Server) AddHouseholdAdult(ctx context.Context, r api.AddHouseholdAdultRequestObject) (api.AddHouseholdAdultResponseObject, error) {
	h, err := s.owner(ctx)
	if err != nil {
		return nil, err
	}
	a, j, err := s.svc.Auth.InviteAdult(h.ID, r.Body.Name, string(r.Body.Email), int(or(r.Body.ExpiresInDays, api.HouseholdAdultInputExpiresInDaysN7)))
	if err != nil {
		return nil, err
	}
	audit(ctx, "Added adult "+a.Name+" to household "+h.Name)
	return api.AddHouseholdAdult201JSONResponse{Account: toAccount(a), JoinCode: toJoinCode(j)}, nil
}

// ListAllowedItems returns a profile's manual allow list.
func (s *Server) ListAllowedItems(ctx context.Context, r api.ListAllowedItemsRequestObject) (api.ListAllowedItemsResponseObject, error) {
	if _, err := s.profile(ctx, r.ProfileId); err != nil {
		return nil, err
	}
	ids, err := s.svc.Accounts.Repo().Allowed(r.ProfileId)
	if ids == nil {
		ids = []int64{}
	}
	return api.ListAllowedItems200JSONResponse{ItemIds: ids}, err
}

// AllowItem adds an existing item to a kid's allow list.
func (s *Server) AllowItem(ctx context.Context, r api.AllowItemRequestObject) (api.AllowItemResponseObject, error) {
	if _, err := s.kid(ctx, r.ProfileId); err != nil {
		return nil, err
	}
	if _, err := s.svc.Items.Repo().Get(r.ItemId); err != nil {
		return nil, err
	}
	return api.AllowItem204Response{}, s.svc.Accounts.Repo().Allow(r.ProfileId, r.ItemId)
}

// DisallowItem removes an item from the allow list.
func (s *Server) DisallowItem(ctx context.Context, r api.DisallowItemRequestObject) (api.DisallowItemResponseObject, error) {
	if _, err := s.profile(ctx, r.ProfileId); err != nil {
		return nil, err
	}
	if !caller(ctx).manages() {
		return nil, errForbidden
	}
	return api.DisallowItem204Response{}, s.svc.Accounts.Repo().Disallow(r.ProfileId, r.ItemId)
}

// ListWatchStates pages a profile's watch state, most recent first.
func (s *Server) ListWatchStates(ctx context.Context, r api.ListWatchStatesRequestObject) (api.ListWatchStatesResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.WatchStates.Repo().List(r.ProfileId, or(r.Params.ParentId, 0), service.BrowseAudienceFor(who), limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListWatchStates200JSONResponse{Items: mapAll(rows, toWatchState), NextOffset: next}, nil
}

// GetWatchState returns one item's state for a profile.
func (s *Server) GetWatchState(ctx context.Context, r api.GetWatchStateRequestObject) (api.GetWatchStateResponseObject, error) {
	if err := s.watchable(ctx, r.ProfileId, r.ItemId); err != nil {
		return nil, err
	}
	w, err := s.svc.WatchStates.Repo().Get(r.ProfileId, r.ItemId)
	if err != nil {
		return nil, err
	}
	return api.GetWatchState200JSONResponse(toWatchState(w)), nil
}

// PutWatchState marks an item watched or unwatched, or sets where to resume.
func (s *Server) PutWatchState(ctx context.Context, r api.PutWatchStateRequestObject) (api.PutWatchStateResponseObject, error) {
	if err := s.watchable(ctx, r.ProfileId, r.ItemId); err != nil {
		return nil, err
	}
	def, err := s.watchedEdition(r.ItemId, r.Body.EditionId)
	if err != nil {
		return nil, err
	}
	w, err := s.saveWatchStateAt(r.ProfileId, r.ItemId, watchedAt(r.Body.At), func(w *watchstate.State) {
		w.Mark(r.Body.Watched, or(r.Body.PositionMs, 0))
		w.EditionID = def
	})
	if err != nil {
		return nil, err
	}
	return api.PutWatchState200JSONResponse(toWatchState(w)), nil
}

// RecordPlay counts one finished play the server did not stream.
func (s *Server) RecordPlay(ctx context.Context, r api.RecordPlayRequestObject) (api.RecordPlayResponseObject, error) {
	if err := s.watchable(ctx, r.ProfileId, r.ItemId); err != nil {
		return nil, err
	}
	ed, err := s.watchedEdition(r.ItemId, r.Body.EditionId)
	if err != nil {
		return nil, err
	}
	w, err := s.svc.WatchStates.Repo().RecordPlay(r.ProfileId, r.ItemId, ed, *watchedAt(&r.Body.At))
	if err != nil {
		return nil, err
	}
	s.watchStateChanged(r.ProfileId, r.ItemId)
	return api.RecordPlay200JSONResponse(toWatchState(w)), nil
}

// watchedEdition is the edition id, of the item, a watch state names: the
// default for nil, and nil when the item has none.
func (s *Server) watchedEdition(itemID int64, id *int64) (*int64, error) {
	if id != nil {
		ed, err := s.edition(itemID, *id)
		return &ed, err
	}
	switch ed, err := s.svc.Editions.Repo().Default(itemID); {
	case err == nil:
		return &ed.ID, nil
	case !errors.Is(err, db.ErrNotFound):
		return nil, err
	}
	return nil, nil
}

// watchable resolves a profile of the device and an item that profile may
// see; an item outside a kid's filter is not found.
func (s *Server) watchable(ctx context.Context, profileID, itemID int64) error {
	who, err := s.profile(ctx, profileID)
	if err != nil {
		return err
	}
	_, err = s.visibleTo(service.AudienceFor(who), itemID)
	return err
}

// saveWatchState loads or starts a state, applies change, stamps it played
// now and saves it.
func (s *Server) saveWatchState(accountID, itemID int64, change func(*watchstate.State)) (*watchstate.State, error) {
	return s.saveWatchStateAt(accountID, itemID, nil, change)
}

// watchedAt is the instant a client says a change happened, to the
// millisecond and never ahead of now; nil is now.
func watchedAt(at *time.Time) *time.Time {
	if at == nil {
		return nil
	}
	v, now := at.UTC().Truncate(time.Millisecond), time.Now().UTC().Truncate(time.Millisecond)
	if v.After(now) {
		v = now
	}
	return &v
}

// saveWatchStateAt is saveWatchState as of at, nil for now. A change as of
// an earlier instant than the stored state's is dropped, and the stored
// state answered.
func (s *Server) saveWatchStateAt(accountID, itemID int64, at *time.Time, change func(*watchstate.State)) (*watchstate.State, error) {
	var w *watchstate.State
	var tell bool
	err := s.svc.WatchStates.Repo().InTx(func(repo *watchstate.Repo) error {
		var err error
		w, tell, err = saveWatchStateIn(repo, accountID, itemID, at, change)
		return err
	})
	if tell && err == nil {
		s.watchStateChanged(accountID, itemID)
	}
	return w, err
}

// saveWatchStateIn is saveWatchStateAt through repo, reporting whether the
// change is to be told instead of telling it, so a caller in a transaction
// tells only what commits.
func saveWatchStateIn(repo *watchstate.Repo, accountID, itemID int64, at *time.Time, change func(*watchstate.State)) (*watchstate.State, bool, error) {
	w, err := repo.Get(accountID, itemID)
	if errors.Is(err, db.ErrNotFound) {
		w, err = &watchstate.State{AccountID: accountID, ItemID: itemID}, nil
	}
	if err != nil {
		return nil, false, err
	}
	was := *w
	change(w)
	now := time.Now().UTC()
	w.LastPlayedAt = &now
	if at == nil {
		err = repo.Save(w)
	} else {
		w.LastPlayedAt = at
		var saved bool
		if saved, err = repo.SaveAt(w, *at); err == nil && !saved {
			w, err = repo.Get(accountID, itemID)
			return w, false, err
		}
	}
	if err != nil {
		return nil, false, err
	}
	// Told when it is new, its mark changes, or its position jumps or
	// leaves or reaches the start, which is what puts it in or out of
	// progress; a position that only plays on is told when its session ends.
	jump := w.PositionMs - was.PositionMs
	return w, was.UpdatedAt.IsZero() || w.Watched != was.Watched || max(jump, -jump) >= watchStateJump ||
		(w.PositionMs == 0) != (was.PositionMs == 0), nil
}

// watchStateJump is how far a position must move at once to be told: more
// than the reports of a session playing on are apart.
const watchStateJump = 30_000

func (s *Server) watchStateChanged(accountID, itemID int64) {
	s.svc.Events.Publish(live.Event{Kind: live.WatchState, AccountIDs: []int64{accountID}, ItemID: itemID})
}

// DeleteWatchState forgets one item's state for a profile.
func (s *Server) DeleteWatchState(ctx context.Context, r api.DeleteWatchStateRequestObject) (api.DeleteWatchStateResponseObject, error) {
	if err := s.watchable(ctx, r.ProfileId, r.ItemId); err != nil {
		return nil, err
	}
	repo := s.svc.WatchStates.Repo()
	var err error
	if at := watchedAt(r.Params.At); at != nil {
		err = repo.DeleteAt(r.ProfileId, r.ItemId, *at)
	} else {
		err = repo.Delete(r.ProfileId, r.ItemId)
	}
	if err != nil {
		return nil, err
	}
	s.watchStateChanged(r.ProfileId, r.ItemId)
	return api.DeleteWatchState204Response{}, nil
}

// Setup is first run.
func (s *Server) Setup(_ context.Context, r api.SetupRequestObject) (api.SetupResponseObject, error) {
	b := r.Body
	sg, err := s.svc.Auth.Setup(b.ServerName, b.Name, string(b.Email), b.Password, b.DeviceName, string(b.Platform))
	if err != nil {
		return nil, err
	}
	res, err := s.signedIn(sg)
	return api.Setup201JSONResponse{SignedInJSONResponse: api.SignedInJSONResponse(res)}, err
}

// SignIn exchanges an adult's email and password for a device token.
func (s *Server) SignIn(ctx context.Context, r api.SignInRequestObject) (api.SignInResponseObject, error) {
	b := r.Body
	sg, err := s.svc.Auth.SignIn(service.SourceKey(clientIP(ctx)), string(b.Email), b.Password, b.DeviceName, string(b.Platform))
	if errors.Is(err, service.ErrUnauthenticated) {
		logstore.Log("auth").Warn("Failed sign-in for " + string(b.Email) + " on " + b.DeviceName)
	}
	if err != nil {
		return nil, err
	}
	res, err := s.signedIn(sg)
	return api.SignIn201JSONResponse{SignedInJSONResponse: api.SignedInJSONResponse(res)}, err
}

// GetJoin previews a join link for the Accept screen.
func (s *Server) GetJoin(ctx context.Context, r api.GetJoinRequestObject) (api.GetJoinResponseObject, error) {
	a, j, by, err := s.svc.Auth.PreviewJoin(service.SourceKey(clientIP(ctx)), r.Code)
	if err != nil {
		return nil, err
	}
	return api.GetJoin200JSONResponse{Name: a.Name, InvitedBy: optStr(by), ExpiresAt: j.ExpiresAt}, nil
}

// ChangePassword replaces the password of the active adult and signs out
// their other devices.
func (s *Server) ChangePassword(ctx context.Context, r api.ChangePasswordRequestObject) (api.ChangePasswordResponseObject, error) {
	p := caller(ctx)
	if p.asKid() {
		return nil, errForbidden
	}
	return api.ChangePassword204Response{}, s.signedOut(s.svc.Auth.ChangePassword(service.SourceKey(clientIP(ctx)), p.device.ID, p.profile, r.Body.CurrentPassword, r.Body.NewPassword))
}

// AcceptJoin consumes a join link and signs the device in.
func (s *Server) AcceptJoin(ctx context.Context, r api.AcceptJoinRequestObject) (api.AcceptJoinResponseObject, error) {
	b := r.Body
	sg, err := s.svc.Auth.AcceptJoin(service.SourceKey(clientIP(ctx)), r.Code, b.Name, b.Password, b.DeviceName, string(b.Platform))
	if err != nil {
		return nil, err
	}
	res, err := s.signedIn(sg)
	return api.AcceptJoin201JSONResponse{SignedInJSONResponse: api.SignedInJSONResponse(res)}, err
}

// SetProfilePin sets or resets the PIN of the adult or a kid.
func (s *Server) SetProfilePin(ctx context.Context, r api.SetProfilePinRequestObject) (api.SetProfilePinResponseObject, error) {
	a, err := s.pinTarget(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	return api.SetProfilePin204Response{}, s.svc.Auth.SetPIN(a, r.Body.Pin)
}

// ClearProfilePin removes a PIN.
func (s *Server) ClearProfilePin(ctx context.Context, r api.ClearProfilePinRequestObject) (api.ClearProfilePinResponseObject, error) {
	a, err := s.pinTarget(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	return api.ClearProfilePin204Response{}, s.svc.Auth.SetPIN(a, "")
}

// UnlockProfile switches the device to a profile of its household. A kid
// may switch back to the adult, so only the household bounds it.
func (s *Server) UnlockProfile(ctx context.Context, r api.UnlockProfileRequestObject) (api.UnlockProfileResponseObject, error) {
	p := caller(ctx)
	a, err := s.svc.Accounts.Profile(p.device.HouseholdID, r.ProfileId)
	if err != nil {
		return nil, err
	}
	return api.UnlockProfile204Response{}, s.svc.Auth.Switch(p.device, a, r.Body.Pin)
}

// LockDevice holds the device at Who's watching until a profile is unlocked.
// Its playback and Watch together end, as on sign-out.
func (s *Server) LockDevice(ctx context.Context, _ api.LockDeviceRequestObject) (api.LockDeviceResponseObject, error) {
	id := caller(ctx).device.ID
	if err := s.svc.Devices.Repo().Lock(id, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.svc.Together.LeaveDevice(id, 0); err != nil {
		return nil, err
	}
	return api.LockDevice204Response{}, s.svc.Playback.EndDevice(id)
}

// SuggestCeilings answers the Add a kid form's birthday hint.
func (s *Server) SuggestCeilings(_ context.Context, r api.SuggestCeilingsRequestObject) (api.SuggestCeilingsResponseObject, error) {
	age := account.Age(r.Params.Birthday.Time, time.Now().UTC())
	movie, tv := account.SuggestCeilings(age)
	return api.SuggestCeilings200JSONResponse{Age: age, MovieCeiling: api.MovieRating(movie), TvCeiling: api.TvRating(tv)}, nil
}

// ListShowProgress pages the profile's progress through each show it sees.
func (s *Server) ListShowProgress(ctx context.Context, r api.ListShowProgressRequestObject) (api.ListShowProgressResponseObject, error) {
	who, err := s.profile(ctx, r.ProfileId)
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.WatchStates.Repo().ShowProgress(r.ProfileId, service.BrowseAudienceFor(who), limit+1, offset)
	if err != nil {
		return nil, err
	}
	rows, next := window(rows, limit, offset)
	return api.ListShowProgress200JSONResponse{Items: mapAll(rows, toShowProgress), NextOffset: next}, nil
}

func toShowProgress(v *watchstate.ShowProgress) api.ShowProgress {
	return api.ShowProgress{ShowId: v.ShowID, Seasons: v.Seasons, Episodes: v.Episodes, Unwatched: v.Unwatched,
		InProgress: v.InProgress, LastPlayedAt: v.LastPlayedAt, NextEpisodeId: v.NextEpisodeID}
}
