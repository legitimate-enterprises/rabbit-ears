package httpapi

import (
	"context"
	"errors"
	"slices"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/together"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Watch together: two profiles, each on their own device, following one
// shared play state by long polling. The member is the active profile, adult
// or kid, and each member may see the title: the service finds none on a
// title the active profile may not see, nor one whose household is not the
// device's unless the profile sees its host.

var togetherKinds = []string{item.Movie, item.Episode, item.Video}

func togetherCaller(ctx context.Context) (service.Where, together.Member) {
	p := caller(ctx)
	return service.Where{HouseholdID: p.device.HouseholdID, TV: device.TV(p.device.Platform)},
		together.Member{AccountID: p.profile.ID, DeviceID: p.device.ID}
}

func togetherError(err error) error {
	switch {
	case errors.Is(err, service.ErrTogetherConflict):
		return fail(409, api.ErrorCodeConflict, err.Error())
	case errors.Is(err, service.ErrNotMember):
		return errForbidden
	}
	return err
}

func (s *Server) toWatchTogether(ctx context.Context, v service.Shared) (api.WatchTogether, error) {
	w := v.W
	out := api.WatchTogether{Id: w.ID, ItemId: w.ItemID, Status: api.WatchTogetherStatus(v.Status()), Members: []api.WatchTogetherMember{},
		JoinBy: w.JoinBy, JoinMsLeft: v.JoinLeft().Milliseconds(), Playing: w.Playing, PositionMs: v.PositionMs(), Version: w.Version,
		ServerTime: v.At, OpenedAt: w.OpenedAt, EndedAt: w.EndedAt, ServerTimeMs: v.At.UnixMilli(), EffectiveAtMs: w.PositionAt.UnixMilli(),
		EffectivePositionMs: w.PositionMs}
	for _, m := range v.Members {
		a, err := s.svc.Accounts.Repo().Get(m.AccountID)
		if err != nil {
			return out, err
		}
		d, err := s.svc.Devices.Repo().Get(m.DeviceID)
		if err != nil {
			return out, err
		}
		here := d.ID == caller(ctx).device.ID
		out.Members = append(out.Members, api.WatchTogetherMember{AccountId: a.ID, Name: a.Name, Role: api.WatchTogetherRole(m.Role),
			DeviceName: d.Name, Platform: api.Platform(d.Platform), Present: v.Present(m), JoinedAt: m.JoinedAt, ThisDevice: &here})
	}
	if w.PausedBy != nil && !w.Playing {
		a, err := s.svc.Accounts.Repo().Get(*w.PausedBy)
		if err != nil {
			return out, err
		}
		out.PausedById, out.PausedByName = &a.ID, &a.Name
	}
	return out, nil
}

// OpenWatchTogether opens a window on a title, paused where the host would
// resume it.
func (s *Server) OpenWatchTogether(ctx context.Context, r api.OpenWatchTogetherRequestObject) (api.OpenWatchTogetherResponseObject, error) {
	household, host := togetherCaller(ctx)
	it, err := s.seen(ctx, r.Body.ItemId)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(togetherKinds, it.Kind) {
		return nil, invalid("itemId", "watch together plays a movie, an episode or a video")
	}
	var pos int64
	if ws, err := s.svc.WatchStates.Repo().Get(host.AccountID, it.ID); err == nil {
		pos = ws.PositionMs
	}
	// From "Playing on", it opens where the profile's other device is.
	from := r.Body.FromSessionId
	if from != nil {
		sess, err := s.svc.Sessions.Repo().Get(*from)
		if err != nil || sess.AccountID != host.AccountID || sess.DeviceID == host.DeviceID || sess.ItemID != it.ID || sess.EndedAt != nil || sess.Station {
			return nil, invalid("fromSessionId", "must be the profile's open session on this item on another device")
		}
		pos = sess.PositionMs
	}
	v, err := s.svc.Together.Open(household.HouseholdID, it.ID, host, pos, from)
	if err != nil {
		return nil, err
	}
	out, err := s.toWatchTogether(ctx, v)
	return api.OpenWatchTogether201JSONResponse(out), err
}

// ListWatchTogethers pages the waiting ones the caller finds and the
// caller's own, on the titles the caller may see.
func (s *Server) ListWatchTogethers(ctx context.Context, r api.ListWatchTogethersRequestObject) (api.ListWatchTogethersResponseObject, error) {
	out := api.ListWatchTogethers200JSONResponse{Items: []api.WatchTogether{}}
	household, me := togetherCaller(ctx)
	all, err := s.svc.Together.List(household, me.AccountID, r.Params.ItemId, audience(ctx))
	if err != nil {
		return nil, err
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, next := slicePage(all, limit, offset)
	for _, v := range rows {
		w, err := s.toWatchTogether(ctx, v)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, w)
	}
	out.NextOffset = next
	return out, nil
}

// reportNeeds hands the service what the calling device says it needs.
func (s *Server) reportNeeds(id uid.ID, me together.Member, rttMs, floorMs *int) {
	conv := func(n *int) *int64 {
		if n == nil {
			return nil
		}
		v := int64(*n)
		return &v
	}
	s.svc.Together.Report(id, me.DeviceID, conv(rttMs), conv(floorMs))
}

// GetWatchTogether is the members' poll, held for a change with after.
func (s *Server) GetWatchTogether(ctx context.Context, r api.GetWatchTogetherRequestObject) (api.GetWatchTogetherResponseObject, error) {
	household, me := togetherCaller(ctx)
	s.reportNeeds(r.WatchTogetherId, me, r.Params.RttMs, r.Params.FloorMs)
	var v service.Shared
	var err error
	if r.Params.After != nil {
		v, err = s.svc.Together.Wait(ctx, household, me.AccountID, r.WatchTogetherId, *r.Params.After, audience(ctx))
	} else {
		v, err = s.svc.Together.Poll(household, me.AccountID, r.WatchTogetherId, audience(ctx))
	}
	if err != nil {
		return nil, err
	}
	out, err := s.toWatchTogether(ctx, v)
	return api.GetWatchTogether200JSONResponse(out), err
}

// LeaveWatchTogether ends it for both members.
func (s *Server) LeaveWatchTogether(ctx context.Context, r api.LeaveWatchTogetherRequestObject) (api.LeaveWatchTogetherResponseObject, error) {
	household, me := togetherCaller(ctx)
	return api.LeaveWatchTogether204Response{}, togetherError(s.svc.Together.Leave(household, me.AccountID, r.WatchTogetherId, audience(ctx)))
}

// JoinWatchTogether makes the caller the guest on this device.
func (s *Server) JoinWatchTogether(ctx context.Context, r api.JoinWatchTogetherRequestObject) (api.JoinWatchTogetherResponseObject, error) {
	household, me := togetherCaller(ctx)
	v, err := s.svc.Together.Join(household, r.WatchTogetherId, me, audience(ctx))
	if err != nil {
		return nil, togetherError(err)
	}
	out, err := s.toWatchTogether(ctx, v)
	return api.JoinWatchTogether200JSONResponse(out), err
}

// SetWatchTogetherState applies a member's play, pause or seek to both.
func (s *Server) SetWatchTogetherState(ctx context.Context, r api.SetWatchTogetherStateRequestObject) (api.SetWatchTogetherStateResponseObject, error) {
	household, me := togetherCaller(ctx)
	s.reportNeeds(r.WatchTogetherId, me, r.Params.RttMs, r.Params.FloorMs)
	v, err := s.svc.Together.SetState(household, me.AccountID, r.WatchTogetherId, r.Body.Playing, r.Body.PositionMs, audience(ctx))
	if err != nil {
		return nil, togetherError(err)
	}
	out, err := s.toWatchTogether(ctx, v)
	return api.SetWatchTogetherState200JSONResponse(out), err
}
