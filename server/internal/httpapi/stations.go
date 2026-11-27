package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/cover"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// Stations: each adult's continuous programming from one of their
// playlists. Every route first finds the station as the device's household
// and active profile see it, so one they may not see is not found before
// any write is refused; only the owner, active, writes.

func viewer(ctx context.Context) station.Viewer { return viewerOf(ctx, browse(ctx)) }

func viewerOf(ctx context.Context, a item.Audience) station.Viewer {
	p := caller(ctx)
	return station.Viewer{ProfileID: p.profile.ID, HouseholdID: p.device.HouseholdID, Audience: a}
}

func (s *Server) seenStation(ctx context.Context, id uid.ID) (*station.Station, error) {
	return s.svc.Stations.Seen(id, viewer(ctx))
}

// ownStation is seenStation for a write, which only its owner makes, and
// never as a kid: an adult made a kid keeps their stations.
func (s *Server) ownStation(ctx context.Context, id uid.ID) (*station.Station, error) {
	st, err := s.svc.Stations.Seen(id, viewerOf(ctx, audience(ctx)))
	if err == nil && (st.AccountID != caller(ctx).profile.ID || caller(ctx).asKid()) {
		return nil, errForbidden
	}
	return st, err
}

// weekdays maps time.Weekday onto the spec's names.
var weekdays = []api.Weekday{api.Sunday, api.Monday, api.Tuesday, api.Wednesday, api.Thursday, api.Friday, api.Saturday}

func minutes(hhmm string) int {
	var h, m int
	_, _ = fmt.Sscanf(hhmm, "%d:%d", &h, &m)
	return h*60 + m
}

func hhmm(minutes int) string { return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60) }

// stationFrom copies the input onto st after the rules the schema cannot
// state: the playlist is the active adult's own or shared with them, and
// not another station's, of the kind given; a server station's visibility
// is the admin's; and a station's own playlist changes kind only while
// empty, which the station's update writes with it.
func (s *Server) stationFrom(ctx context.Context, in *api.StationInput, st *station.Station) error {
	if in.PlaylistId != nil && in.OwnPlaylistId != nil {
		return invalid("ownPlaylistId", "names the playlist made when playlistId is absent")
	}
	name, err := playlistName(in.Name)
	if err != nil {
		return err
	}
	kind := string(or(in.Kind, api.PlaylistKind(st.Kind)))
	var p *playlist.Playlist
	switch {
	case in.PlaylistId != nil:
		p, err = s.svc.Playlists.Repo().Get(*in.PlaylistId)
		if err == nil && p.StationOwned && p.ID != st.PlaylistID {
			err = db.ErrNotFound
		} else if err == nil {
			_, err = s.svc.Playlists.Access(p, caller(ctx).profile.ID)
		}
		if errors.Is(err, db.ErrNotFound) || errors.Is(err, service.ErrNotShared) {
			return invalid("playlistId", "neither one of your playlists nor shared with you")
		} else if err != nil {
			return err
		}
		if in.Kind != nil && kind != p.Kind {
			return invalid("kind", "not the playlist's kind")
		}
	case st.ID.IsZero():
		if in.Kind == nil {
			return invalid("kind", "required without a playlistId")
		}
	default:
		if p, err = s.svc.Playlists.Repo().Get(st.PlaylistID); err != nil {
			return err
		}
		if kind != p.Kind && !p.StationOwned {
			return invalid("kind", "a station takes its playlist's kind; give another playlistId")
		}
	}
	if in.Visibility != nil {
		if st.Visibility == station.Server {
			return fail(http.StatusForbidden, api.ErrorCodeForbidden, "only an admin changes a server-wide station's visibility")
		}
		st.Visibility = string(*in.Visibility)
	}
	took := kind
	if in.PlaylistId != nil {
		took = p.Kind
	}
	switch {
	case in.Timing != nil:
		st.Timing = string(*in.Timing)
	case st.ID.IsZero() || took != st.Kind:
		st.Timing = station.DefaultTiming(took)
	}
	st.Name, st.Kind, st.Random = name, kind, or(in.Order, api.StationOrderPlaylist) == api.StationOrderRandom
	if p != nil {
		st.PlaylistID = p.ID
		if in.PlaylistId == nil && kind != p.Kind {
			n, err := s.svc.Playlists.Repo().CountEntries(p.ID)
			if err != nil {
				return err
			}
			if n > 0 {
				return playlistError(service.ErrPlaylistKindLocked)
			}
		}
	}
	st.Schedule = station.Schedule{}
	for _, d := range or(in.Days, nil) {
		for i, w := range weekdays {
			if w == d {
				st.Schedule.Days = append(st.Schedule.Days, time.Weekday(i))
			}
		}
	}
	for _, t := range or(in.Times, nil) {
		r := station.Range{ID: or(t.Id, uid.ID{}), From: minutes(t.From), To: minutes(t.To)}
		if !r.Holds(st.Timing) {
			return invalid("times", fmt.Sprintf("%s to %s holds no %s start, so nothing would air", t.From, t.To, st.Timing))
		}
		st.Schedule.Times = append(st.Schedule.Times, r)
	}
	return nil
}

// programs fills each program's item, show or song as a play-order row is,
// reading metadata, genres and songs for all of them at once.
func (s *Server) programs(ps []*station.Program, admin bool) ([]api.StationProgram, error) {
	out := make([]api.StationProgram, len(ps))
	var its, shows []*item.Item
	var songs []uid.ID
	for i, p := range ps {
		out[i] = api.StationProgram{ItemId: p.ItemID, EditionId: p.EditionID, StartsAt: p.Start, EndsAt: p.End, DurationMs: p.RuntimeMs}
		it, err := s.svc.Items.Repo().Get(p.ItemID)
		if err != nil {
			return nil, err
		}
		out[i].Kind = api.PlaylistEntryKind(it.Kind)
		if out[i].EditionName, err = s.editionName(p.ItemID, p.EditionID); err != nil {
			return nil, err
		}
		its = append(its, it)
		if it.Kind == item.Song {
			songs = append(songs, it.ID)
			continue
		}
		var up *item.Item
		if it.Kind == item.Season || it.Kind == item.Episode {
			if up, err = s.svc.Items.Repo().Get(*it.ParentID); err == nil && up.Kind == item.Season {
				up, err = s.svc.Items.Repo().Get(*up.ParentID)
			}
			if err != nil {
				return nil, err
			}
			shows = append(shows, up)
		}
	}
	byID := map[uid.ID]api.MusicSong{}
	if len(songs) > 0 {
		vs, err := s.svc.Music.Repo().Songs(item.MusicFilter{IDs: songs, Limit: len(songs)})
		if err != nil {
			return nil, err
		}
		for _, v := range vs {
			byID[v.ID] = toSong(v)
		}
	}
	var films []*item.Item
	for _, it := range its {
		if it.Kind != item.Song {
			films = append(films, it)
		}
	}
	vs, err := s.items(films, admin)
	if err != nil {
		return nil, err
	}
	ups, err := s.items(shows, admin)
	if err != nil {
		return nil, err
	}
	for i, it := range its {
		if it.Kind == item.Song {
			song, ok := byID[it.ID]
			if !ok {
				return nil, errNotFound
			}
			out[i].Song = &song
			continue
		}
		out[i].Item, vs = &vs[0], vs[1:]
		if it.Kind == item.Season || it.Kind == item.Episode {
			out[i].Show, ups = &ups[0], ups[1:]
		}
	}
	return out, nil
}

func (s *Server) onAir(a service.Airing, admin bool) (api.StationOnAir, error) {
	out := api.StationOnAir{Status: api.StationStatus(a.Status), UpNext: []api.StationProgram{}}
	ps := a.UpNext
	if a.Now != nil {
		ps = append([]*station.Program{a.Now}, ps...)
	}
	vs, err := s.programs(ps, admin)
	if err != nil {
		return out, err
	}
	if a.Now != nil {
		out.Now, out.PositionMs, vs = &vs[0], &a.PositionMs, vs[1:]
	}
	out.UpNext = append(out.UpNext, vs...)
	if len(out.UpNext) > 0 {
		out.Next = &out.UpNext[0]
	}
	return out, nil
}

func (s *Server) toStation(ctx context.Context, st *station.Station) (api.Station, error) {
	out := api.Station{Id: st.ID, Name: st.Name, Kind: api.PlaylistKind(st.Kind), OwnerId: st.AccountID,
		PlaylistId: st.PlaylistID, Order: api.StationOrderPlaylist, Visibility: api.StationVisibility(st.Visibility), Timing: api.StationTiming(st.Timing),
		Rating: optStr(st.Rating),
		Days:   []api.Weekday{}, Times: []api.StationTimeRange{}, CreatedAt: st.CreatedAt, ModifiedAt: st.ModifiedAt, CreatedBy: st.CreatedBy,
		ModifiedBy: st.ModifiedBy}
	// An owner is always in a household; a zero id fails to encode rather than misstate one.
	if st.HouseholdID != nil {
		out.HouseholdId = *st.HouseholdID
	}
	if st.Random {
		out.Order = api.StationOrderRandom
	}
	for _, d := range st.Schedule.Days {
		out.Days = append(out.Days, weekdays[d])
	}
	for _, t := range st.Schedule.Times {
		out.Times = append(out.Times, api.StationTimeRange{Id: &t.ID, From: hhmm(t.From), To: hhmm(t.To)})
	}
	c, err := s.svc.Covers.Shown(cover.Owner{StationID: &st.ID}, st.PlaylistID, browse(ctx))
	if err != nil {
		return out, err
	}
	out.ImageUrl = coverURL("/stations/"+st.ID.String(), c)
	a, err := s.svc.Stations.OnAir(st, browse(ctx))
	if err != nil {
		return out, err
	}
	out.OnAir, err = s.onAir(a, caller(ctx).asAdmin())
	return out, err
}

func (s *Server) stationPage(ctx context.Context, rows []*station.Station, total, limit, offset int) (api.StationPage, error) {
	out := api.StationPage{Items: []api.Station{}, Total: total}
	rows, out.NextOffset = window(rows, limit, offset)
	for _, st := range rows {
		v, err := s.toStation(ctx, st)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}

// ListStations pages by name the stations the active profile may see.
func (s *Server) ListStations(ctx context.Context, r api.ListStationsRequestObject) (api.ListStationsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	v := viewer(ctx)
	rows, total, err := s.svc.Stations.List(v, enumStr(r.Params.Kind), limit+1, offset)
	if err != nil {
		return nil, err
	}
	out, err := s.stationPage(ctx, rows, total, limit, offset)
	return api.ListStations200JSONResponse(out), err
}

// CreateStation makes a station of the active adult's and schedules it.
func (s *Server) CreateStation(ctx context.Context, r api.CreateStationRequestObject) (api.CreateStationResponseObject, error) {
	if caller(ctx).asKid() {
		return nil, errForbidden
	}
	st := &station.Station{AccountID: caller(ctx).profile.ID, Visibility: station.Private}
	if err := s.stationFrom(ctx, r.Body, st); err != nil {
		return nil, err
	}
	st.ID = or(r.Body.Id, uid.ID{})
	if err := s.svc.Stations.Create(st, r.Body.OwnPlaylistId, actor(ctx)); err != nil {
		return nil, err
	}
	v, err := s.toStation(ctx, st)
	return api.CreateStation201JSONResponse(v), err
}

// GetStation returns one station the active profile may see.
func (s *Server) GetStation(ctx context.Context, r api.GetStationRequestObject) (api.GetStationResponseObject, error) {
	st, err := s.seenStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	v, err := s.toStation(ctx, st)
	return api.GetStation200JSONResponse(v), err
}

// ReplaceStation edits the owner's station; what is scheduled stays.
func (s *Server) ReplaceStation(ctx context.Context, r api.ReplaceStationRequestObject) (api.ReplaceStationResponseObject, error) {
	if err := sameID(r.Body.Id, r.StationId); err != nil {
		return nil, err
	}
	if r.Body.OwnPlaylistId != nil {
		return nil, invalid("ownPlaylistId", "names a new station's own playlist only")
	}
	st, err := s.ownStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	if err := s.stationFrom(ctx, r.Body, st); err != nil {
		return nil, err
	}
	if err := s.svc.Stations.Update(st, actor(ctx)); err != nil {
		return nil, err
	}
	v, err := s.toStation(ctx, st)
	return api.ReplaceStation200JSONResponse(v), err
}

// DeleteStation deletes the owner's station with its schedule and the
// playlist it owns.
func (s *Server) DeleteStation(ctx context.Context, r api.DeleteStationRequestObject) (api.DeleteStationResponseObject, error) {
	st, err := s.ownStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	return api.DeleteStation204Response{}, s.svc.Stations.Delete(st)
}

// ListStationSchedule pages the programs overlapping one local day.
func (s *Server) ListStationSchedule(ctx context.Context, r api.ListStationScheduleRequestObject) (api.ListStationScheduleResponseObject, error) {
	st, err := s.seenStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	now := s.svc.Stations.Now()
	y, m, d := now.Date()
	if r.Params.Date != nil {
		y, m, d = r.Params.Date.Date()
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Stations.Repo().Between(st.ID, time.Date(y, m, d, 0, 0, 0, 0, now.Location()), time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()),
		browse(ctx), limit+1, offset)
	if err != nil {
		return nil, err
	}
	out := api.ListStationSchedule200JSONResponse{Items: []api.StationProgram{}}
	rows, out.NextOffset = window(rows, limit, offset)
	vs, err := s.programs(rows, caller(ctx).asAdmin())
	out.Items = append(out.Items, vs...)
	return out, err
}

// RescheduleStation is Reschedule now on the owner's station.
func (s *Server) RescheduleStation(ctx context.Context, r api.RescheduleStationRequestObject) (api.RescheduleStationResponseObject, error) {
	st, err := s.ownStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Stations.Reschedule(st); err != nil {
		return nil, err
	}
	v, err := s.toStation(ctx, st)
	return api.RescheduleStation200JSONResponse(v), err
}

// StartStationPlayback tunes the active profile in: an ordinary session on
// the program on now, at its live position, decided for this device.
func (s *Server) StartStationPlayback(ctx context.Context, r api.StartStationPlaybackRequestObject) (api.StartStationPlaybackResponseObject, error) {
	in := r.Body
	if err := checkNetwork(in.Snapshot.Network); err != nil {
		return nil, err
	}
	st, err := s.seenStation(ctx, r.StationId)
	if err != nil {
		return nil, err
	}
	// A listed station plays whatever is on air: Hide decides what is listed, not what plays.
	snap, sel := toSnapshot(ctx, in.Snapshot, s.home(ctx)), playback.Selection{AudioTrackID: in.AudioTrackId, SubtitleTrackID: in.SubtitleTrackId}
	device := caller(ctx).device.ID
	sess := &session.Session{DeviceID: device, AccountID: caller(ctx).profile.ID, Station: true}
	if st.Kind != playlist.Music && or(in.Live, false) {
		err = s.svc.Channels.Tune(ctx, st, viewerOf(ctx, audience(ctx)), sess, snap, sel)
	} else {
		err = s.tuneProgram(st, audience(ctx), sess, snap, sel)
	}
	var off *service.OffAirError
	switch {
	case errors.As(err, &off):
		return nil, &apiError{http.StatusConflict, api.Error{Code: api.ErrorCodeOffAir, Message: off.Error(), ResumesAt: off.Resumes}}
	case errors.Is(err, service.ErrProgramHidden):
		return nil, fail(http.StatusForbidden, api.ErrorCodeForbidden, err.Error())
	case errors.Is(err, service.ErrUnavailable):
		return nil, fail(http.StatusConflict, api.ErrorCodeConflict, err.Error())
	case err != nil:
		return nil, err
	}
	// A station is no Watch together, even on the same title; music plays
	// on while its listener waits in one.
	if st.Kind != playlist.Music {
		if err := s.svc.Together.LeaveDevice(device, nil); err != nil {
			return nil, err
		}
	}
	out := api.StartStationPlayback201JSONResponse{}
	if out.Stream, err = s.stream(sess); err != nil {
		return nil, err
	}
	a, err := s.svc.Stations.OnAir(st, browse(ctx))
	if err != nil {
		return nil, err
	}
	out.OnAir, err = s.onAir(a, caller(ctx).asAdmin())
	return out, err
}

// tuneProgram starts sess on the one program st plays now, at its live
// position.
func (s *Server) tuneProgram(st *station.Station, a item.Audience, sess *session.Session, snap playback.Snapshot, sel playback.Selection) error {
	p, pos, err := s.svc.Stations.Tune(st, a)
	if err != nil {
		return err
	}
	_, file, err := s.playFile(p.ItemID, &p.EditionID, snap, sel)
	if err != nil {
		return err
	}
	sess.ItemID, sess.MediaFileID, sess.PositionMs = p.ItemID, file.ID, pos
	return s.svc.Playback.Start(sess, snap, sel)
}

// ListAdminStations pages every station by name.
func (s *Server) ListAdminStations(ctx context.Context, r api.ListAdminStationsRequestObject) (api.ListAdminStationsResponseObject, error) {
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, err := s.svc.Stations.Repo().All(limit+1, offset)
	if err != nil {
		return nil, err
	}
	total, err := s.svc.Stations.Repo().CountAll()
	if err != nil {
		return nil, err
	}
	out, err := s.stationPage(ctx, rows, total, limit, offset)
	return api.ListAdminStations200JSONResponse(out), err
}

// SetStationServerWide is the admin's switch: on makes a station server
// visible, off returns a server one to household and leaves any other as
// it is.
func (s *Server) SetStationServerWide(ctx context.Context, r api.SetStationServerWideRequestObject) (api.SetStationServerWideResponseObject, error) {
	st, err := s.svc.Stations.Repo().Get(r.StationId)
	if err != nil {
		return nil, err
	}
	to := st.Visibility
	switch {
	case r.Body.ServerWide:
		to = station.Server
	case st.Visibility == station.Server:
		to = station.Household
	}
	if err := s.svc.Stations.SetVisibility(st, to, actor(ctx)); err != nil {
		return nil, err
	}
	if st, err = s.svc.Stations.Repo().Get(st.ID); err != nil {
		return nil, err
	}
	v, err := s.toStation(ctx, st)
	return api.SetStationServerWide200JSONResponse(v), err
}
