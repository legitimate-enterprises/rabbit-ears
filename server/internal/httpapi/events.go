package httpapi

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Live updates: every signed-in client follows one stream of events by long
// polling and reads what each names through the ordinary operations.

// hears decides whether the calling profile is told of e: it must be among
// those e is for, or see its opener, and e must name nothing its kid filter
// hides.
func (s *Server) hears(ctx context.Context, e live.Event) (bool, error) {
	p := caller(ctx)
	if !e.Hears(p.device.HouseholdID, p.profile.ID) {
		if e.OpenerID == nil || *e.OpenerID == p.profile.ID {
			return false, nil
		}
		if ok, err := s.svc.Accounts.Repo().Sees(p.profile, *e.OpenerID, device.TV(p.device.Platform)); err != nil || !ok {
			return false, err
		}
	}
	a := service.AudienceFor(p.profile)
	if e.ItemID != nil {
		if ok, err := s.svc.Items.Repo().Visible(*e.ItemID, a); err != nil || !ok {
			return false, err
		}
	}
	// An adult hears of a station gone, to drop it; a kid only of one seen.
	if e.StationID != nil && a.Kid {
		if _, err := s.seenStation(ctx, *e.StationID); errors.Is(err, db.ErrNotFound) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	return true, nil
}

func toEvent(e live.Event) api.Event {
	return api.Event{Kind: api.EventKind(e.Kind), Change: optStr(e.Change), ItemId: e.ItemID, SessionId: e.SessionID,
		DeviceId: e.DeviceID, StationId: e.StationID, PlaylistId: e.PlaylistID, WatchTogetherId: e.WatchTogetherID,
		LibraryId: e.LibraryID}
}

// ListEvents answers what the active profile is to hear since a cursor,
// held until there is something or the hold runs out.
func (s *Server) ListEvents(ctx context.Context, r api.ListEventsRequestObject) (api.ListEventsResponseObject, error) {
	hub := s.svc.Events
	out := api.ListEvents200JSONResponse{Cursor: hub.Cursor(), Events: []api.Event{}}
	if r.Params.After == nil {
		return out, nil
	}
	after := *r.Params.After
	// Kinds a released client may not decode reach only a client that names them.
	follows := map[string]bool{live.WatchState: true, live.Playback: true, live.Station: true, live.WatchTogether: true, live.Playlist: true}
	for _, k := range or(r.Params.Kinds, nil) {
		follows[string(k)] = true
	}
	timeout := time.NewTimer(s.svc.EventHold)
	defer timeout.Stop()
	for {
		events, next, wait, ok := hub.Since(after)
		out.Cursor, out.Resync = next, !ok
		for _, e := range events {
			if !follows[e.Kind] {
				continue
			}
			if told, err := s.hears(ctx, e); err != nil {
				return nil, err
			} else if told {
				out.Events = append(out.Events, toEvent(e))
			}
		}
		if !ok || len(out.Events) > 0 {
			return out, nil
		}
		// What was not for this profile is not looked at again.
		after = next
		select {
		case <-wait:
			if err := s.stillSignedIn(ctx); err != nil {
				return nil, err
			}
		case <-timeout.C:
			return out, nil
		case <-ctx.Done():
			// The client has gone; nobody reads this.
			return out, nil
		}
	}
}

// stillSignedIn refuses a held poll whose device was signed out while it
// waited, as by its account's deletion, so the device learns at once.
func (s *Server) stillSignedIn(ctx context.Context) error {
	d, err := s.svc.Devices.Repo().Get(caller(ctx).device.ID)
	if errors.Is(err, db.ErrNotFound) || err == nil && d.RevokedAt != nil {
		return service.ErrUnauthenticated
	}
	return err
}

// idField reads an id field of a generated request or response.
func idField(v any, name string) (uid.ID, bool) {
	f := reflect.Indirect(reflect.ValueOf(v))
	if f.Kind() != reflect.Struct {
		return uid.ID{}, false
	}
	if f = f.FieldByName(name); !f.IsValid() || !f.CanInterface() {
		return uid.ID{}, false
	}
	id, ok := f.Interface().(uid.ID)
	return id, ok
}

// playlistAudience is who sees a playlist: its owner and everyone its
// shares reach; nobody when it does not exist.
func (s *Server) playlistAudience(id uid.ID) []uid.ID {
	out, _ := s.svc.Playlists.Repo().Audience(id)
	return out
}

// announcePlaylists tells of every change made through a playlist's
// operations, to those who saw the playlist before it and those who see it
// after, so that an unshare and a delete are heard by whom they concern.
func (s *Server) announcePlaylists(f api.StrictHandlerFunc, operationID string) api.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		id, named := idField(request, "PlaylistId")
		if r.Method == http.MethodGet || !named && operationID != "CreatePlaylist" {
			return f(ctx, w, r, request)
		}
		before := s.playlistAudience(id)
		res, err := f(ctx, w, r, request)
		if err != nil {
			return res, err
		}
		if !named {
			id, _ = idField(res, "Id")
		}
		if to := append(before, s.playlistAudience(id)...); len(to) > 0 {
			s.svc.Events.Publish(live.Event{Kind: live.Playlist, PlaylistID: &id, AccountIDs: to})
		}
		// Unsharing and deleting a playlist delete the stations that played it.
		if r.Method == http.MethodDelete {
			s.svc.Events.Publish(live.Event{Kind: live.Station})
		}
		return res, nil
	}
}
