package httpapi

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/api"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/edition"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/watchstate"
)

var errPlanExhausted = fail(409, api.ErrorCodeConflict, "no playable entry is left in the plan")

func strs[T ~string](in []T) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = string(v)
	}
	return out
}

func toDisplay(d api.DisplayCapabilities) playback.Display {
	return playback.Display{Width: d.Width, Height: d.Height, HDR: strs(d.HdrFormats)}
}

func toRoute(a api.AudioRouteCapabilities) playback.Route {
	return playback.Route{Channels: a.Channels, Passthrough: a.PassthroughCodecs}
}

// home reports a calling device whose local check is confirmed: one that
// reaches the API at a public address from the server's own network.
func (s *Server) home(ctx context.Context) bool { return s.svc.Local.Home(caller(ctx).device.ID) }

// toNetwork applies the same-LAN rule to the caller's address, or takes a
// confirmed local check in its place.
func toNetwork(ctx context.Context, n api.NetworkPathCapabilities, home bool) playback.Network {
	return playback.Network{Local: playback.SameLAN(clientIP(ctx), playback.InterfaceSubnets(), n.DiscoveredLocally) || n.DiscoveredLocally && home,
		Metered: n.Metered, LowData: n.LowData, MaxBitrate: or(n.MaxBitrate, 0), Auto: n.Auto}
}

// checkNetwork is the rule the schema cannot state: Auto has no cap.
func checkNetwork(n api.NetworkPathCapabilities) error {
	if or(n.Auto, false) && n.MaxBitrate != nil {
		return invalid("maxBitrate", "must be absent when auto is true")
	}
	return nil
}

func toSnapshot(ctx context.Context, s api.CapabilitySnapshot, home bool) playback.Snapshot {
	out := playback.Snapshot{Protocols: strs(s.Decoder.Protocols), Containers: s.Decoder.Containers, Audio: s.Decoder.AudioCodecs,
		Subtitles: s.Decoder.SubtitleFormats, Display: toDisplay(s.Display), Route: toRoute(s.AudioRoute), Network: toNetwork(ctx, s.Network, home)}
	if s.Decoder.SegmentFormats != nil {
		out.Segments = strs(*s.Decoder.SegmentFormats)
	}
	for _, v := range s.Decoder.VideoCodecs {
		var hdr []string
		if v.HdrFormats != nil {
			hdr = strs(*v.HdrFormats)
		}
		out.Video = append(out.Video, playback.VideoDecoder{Codec: v.Codec, Profiles: or(v.Profiles, nil), MaxLevel: or(v.MaxLevel, 0),
			Hardware: v.Hardware, MaxWidth: v.MaxWidth, MaxHeight: v.MaxHeight, MaxFrameRate: v.MaxFrameRate, MaxBitDepth: v.MaxBitDepth, HDR: hdr})
	}
	return out
}

// StartPlayback opens a session for a profile of the calling device.
func (s *Server) StartPlayback(ctx context.Context, r api.StartPlaybackRequestObject) (api.StartPlaybackResponseObject, error) {
	in := r.Body
	if err := checkNetwork(in.Snapshot.Network); err != nil {
		return nil, err
	}
	who, err := s.profile(ctx, in.ProfileId)
	if err != nil {
		return nil, err
	}
	it, err := s.visibleTo(service.AudienceFor(who), in.ItemId)
	if err != nil {
		return nil, err
	}
	var from *playlist.Playlist
	if in.PlaylistId != nil {
		if from, err = s.svc.Playlists.Repo().Get(*in.PlaylistId); err != nil {
			return nil, err
		}
		if _, err := s.svc.Playlists.Access(from, in.ProfileId); err != nil {
			return nil, playlistError(err)
		}
	}
	snap, sel := toSnapshot(ctx, in.Snapshot, s.home(ctx)), playback.Selection{AudioTrackID: in.AudioTrackId, SubtitleTrackID: in.SubtitleTrackId}
	ed, file, err := s.playFile(it.ID, in.EditionId, snap, sel)
	if err != nil {
		return nil, err
	}
	pos := or(in.PositionMs, -1)
	if pos < 0 {
		pos = 0
		if w, err := s.svc.WatchStates.Repo().Get(in.ProfileId, it.ID); err == nil && w.EditionID != nil && *w.EditionID == ed.ID {
			pos = w.PositionMs
		}
	}
	sess := &session.Session{DeviceID: caller(ctx).device.ID, AccountID: in.ProfileId, ItemID: it.ID, MediaFileID: file.ID, PositionMs: pos}
	if err := s.svc.Playback.Start(sess, snap, sel); err != nil {
		return nil, err
	}
	// Music plays on while its listener waits in a Watch together.
	if it.Kind != item.Song {
		if err := s.svc.Together.LeaveDevice(sess.DeviceID, it.ID); err != nil {
			return nil, err
		}
	}
	// Only the owner's play stamps the playlist, for their Recently played.
	if from != nil && from.AccountID == in.ProfileId {
		if err := s.svc.Playlists.Repo().MarkPlayed(from.ID, sess.StartedAt); err != nil {
			return nil, err
		}
	}
	st, err := s.stream(sess)
	return api.StartPlayback201JSONResponse(st), err
}

// playFile is the edition of an item that playback uses, editionID or else
// the default, and the file of it on disk that the device plays best. An
// edition of another item is not found.
func (s *Server) playFile(itemID int64, editionID *int64, snap playback.Snapshot, sel playback.Selection) (*edition.Edition, *mediafile.File, error) {
	eds := s.svc.Editions.Repo()
	var ed *edition.Edition
	var err error
	if editionID == nil {
		ed, err = eds.Default(itemID)
		if errors.Is(err, db.ErrNotFound) {
			return nil, nil, fail(409, api.ErrorCodeConflict, "the item has no file to play")
		}
	} else if ed, err = eds.Get(*editionID); err == nil && ed.ItemID != itemID {
		err = errNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	files, err := s.svc.MediaFiles.Repo().FilesForEdition(ed.ID)
	if err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fail(409, api.ErrorCodeConflict, "the edition has no file to play")
	}
	if files, err = s.svc.Playback.OnDisk(files); err != nil {
		return nil, nil, err
	}
	if len(files) == 0 {
		return nil, nil, fail(409, api.ErrorCodeConflict, "the title is unavailable: none of its files is on disk")
	}
	f, err := s.svc.Playback.Best(files, snap, sel)
	return ed, f, err
}

// CheckPlayability answers the verdict StartPlayback would begin with for
// the active profile, without opening a session.
func (s *Server) CheckPlayability(ctx context.Context, r api.CheckPlayabilityRequestObject) (api.CheckPlayabilityResponseObject, error) {
	if err := checkNetwork(r.Body.Network); err != nil {
		return nil, err
	}
	it, err := s.seen(ctx, r.ItemId)
	if err != nil {
		return nil, err
	}
	if it.Kind != item.Movie && it.Kind != item.Episode && it.Kind != item.Video && it.Kind != item.Song {
		return nil, errNotFound
	}
	snap := toSnapshot(ctx, *r.Body, s.home(ctx))
	_, file, err := s.playFile(it.ID, nil, snap, playback.Selection{})
	if err != nil {
		return nil, err
	}
	plan, err := s.svc.Playback.Plan(file.ID, snap, playback.Selection{})
	if err != nil {
		return nil, err
	}
	_, media, err := s.svc.Playback.Media(file.ID)
	if err != nil {
		return nil, err
	}
	return api.CheckPlayability200JSONResponse{Verdict: api.Verdict(plan[0].Verdict), Negotiated: negotiate(plan[0], media)}, nil
}

// negotiate is the player pill for a plan entry of this media.
func negotiate(e *session.PlanEntry, media playback.Media) api.Negotiated {
	src := playback.VideoTrack(media)
	top := playback.Rungs(e, src)[0]
	n := api.Negotiated{Verdict: api.Verdict(e.Verdict), Width: top.Width, Height: top.Height, Bitrate: top.Bitrate}
	if playback.Song(media) {
		codec := "aac"
		if a := trackByID(media.Tracks, e.AudioTrackID); a != nil && e.AudioAction == session.ActionCopy {
			codec = a.Codec
		}
		n.AudioCodec = &codec
		if mediafile.Lossless(codec) {
			n.Lossless = &[]bool{true}[0]
		}
	}
	switch {
	case src == nil:
	case e.VideoAction == session.ActionCopy:
		n.HdrFormat = optEnum[api.HdrFormat](src.HDRFormat)
	case e.VideoCodec == "hevc" && !e.ToneMap && src.HDRFormat != "":
		n.HdrFormat = optEnum[api.HdrFormat]("hdr10")
	}
	if e.VideoAction == session.ActionConvert {
		n.Encoder = optEnum[api.NegotiatedEncoder](e.Encoder)
	}
	return n
}

// audioDetails is what a song's details panel shows: the probed source file
// and what plan entry e delivers. Facts the probe did not read are absent.
func audioDetails(e *session.PlanEntry, media playback.Media) *api.AudioDetails {
	a := trackByID(media.Tracks, e.AudioTrackID)
	if a == nil {
		return nil
	}
	pos := func(n int64) *int64 {
		if n > 0 {
			return &n
		}
		return nil
	}
	n := negotiate(e, media)
	d := api.AudioDelivered{Verdict: n.Verdict, Codec: *n.AudioCodec, Lossless: n.Lossless != nil, Bitrate: pos(n.Bitrate)}
	src := api.AudioSource{Container: optStr(media.Container), Codec: optStr(a.Codec), BitDepth: a.BitDepth, Channels: a.Channels,
		SampleRateHz: a.SampleRate, SizeBytes: pos(media.SizeBytes), Lossless: mediafile.Lossless(a.Codec), Bitrate: pos(media.Bitrate)}
	if a.Bitrate != nil {
		src.Bitrate = pos(*a.Bitrate)
	}
	if e.AudioAction == session.ActionCopy {
		d.Bitrate = src.Bitrate
	}
	return &api.AudioDetails{Source: src, Delivered: d}
}

// owned returns an open session of the calling device, and to a kid only
// their own, as a kid starts only their own; any other session is not
// found, whoever owns it.
func (s *Server) owned(ctx context.Context, id int64) (*session.Session, error) {
	v, err := s.svc.Sessions.Repo().Get(id)
	if err != nil {
		return nil, err
	}
	p := caller(ctx)
	if v.DeviceID != p.device.ID || v.EndedAt != nil || p.asKid() && v.AccountID != p.profile.ID {
		return nil, errNotFound
	}
	return v, nil
}

func trackByID(ts []*mediafile.Track, id *int64) *mediafile.Track {
	for _, t := range ts {
		if id != nil && t.ID == *id {
			return t
		}
	}
	return nil
}

// streamBase is the prefix of every URL of one plan entry of a session.
func streamBase(v *session.Session) string {
	return fmt.Sprintf("%s/streams/%s/%d", BasePath, v.StreamKey, v.CurrentEntry)
}

// stream describes the plan entry in use and how to fetch it.
func (s *Server) stream(v *session.Session) (api.PlaybackStream, error) {
	plan, err := s.svc.Sessions.Repo().Plan(v.ID)
	if err != nil {
		return api.PlaybackStream{}, err
	}
	if v.CurrentEntry >= len(plan) {
		return api.PlaybackStream{}, errPlanExhausted
	}
	e := plan[v.CurrentEntry]
	_, media, err := s.svc.Playback.Media(v.MediaFileID)
	if err != nil {
		return api.PlaybackStream{}, err
	}
	n := negotiate(e, media)
	base := streamBase(v)
	st := api.PlaybackStream{SessionId: v.ID, MediaFileId: v.MediaFileID, Entry: toPlanEntry(e), PositionMs: v.PositionMs, Negotiated: n,
		StreamUrl: base + "/master.m3u8", StreamKind: api.PlaybackStreamStreamKindHls, Sidecars: []api.Sidecar{}}
	if playback.Song(media) {
		st.Audio = audioDetails(e, media)
	}
	if e.Verdict == session.DirectPlay {
		st.StreamUrl, st.StreamKind = base+"/file", api.PlaybackStreamStreamKindFile
	}
	var rungs []playback.Rung
	yes := true
	if s.svc.Channels.Tuned(v.ID) {
		st.StreamUrl, st.Live, st.PositionMs = fmt.Sprintf("%s/channels/%s/live.m3u8", BasePath, v.StreamKey), &yes, 0
		st.Rungs = []api.Rung{{Width: n.Width, Height: n.Height, Bitrate: n.Bitrate, Verdict: api.Verdict(e.Verdict)}}
		return st, nil
	}
	if playback.Song(media) {
		// A song has no quality menu, so no rungs and no Auto or away.
		st.Rungs = []api.Rung{}
		return st, nil
	}
	if e.Verdict != session.DirectPlay {
		ahead, err := s.svc.Server.ReadAhead()
		if err != nil {
			return api.PlaybackStream{}, err
		}
		st.BufferGoalSeconds = &ahead
	}
	if snap, sel, ok := s.svc.Playback.Live(v.ID); ok {
		rungs = s.svc.Playback.Menu(media, snap, sel)
		// Only a v2 client, which sends auto, hears about away and Auto.
		if snap.Network.Auto != nil && !snap.Network.Local {
			st.Away = &yes
		}
		if playback.Auto(snap, e) {
			st.Auto = &yes
		}
	}
	if len(rungs) == 0 {
		rungs = []playback.Rung{{Width: n.Width, Height: n.Height, Bitrate: n.Bitrate, Verdict: e.Verdict}}
	}
	for _, r := range rungs {
		out := api.Rung{Width: r.Width, Height: r.Height, Bitrate: r.Bitrate, Verdict: api.Verdict(r.Verdict)}
		if r.Blocked {
			out.Blocked = &yes
		}
		st.Rungs = append(st.Rungs, out)
	}
	if p, err := s.svc.Previews.Repo().ByMediaFile(v.MediaFileID); err == nil {
		st.Previews = &api.PreviewFrames{IntervalMs: p.IntervalMs, Count: p.Count, Width: p.Width, Height: p.Height,
			FrameUrl: base + "/previews/{n}.jpg", BifUrl: base + "/previews.bif"}
	} else if !errors.Is(err, db.ErrNotFound) {
		return api.PlaybackStream{}, err
	}
	if t := trackByID(media.Tracks, e.SubtitleTrackID); t != nil && e.SubtitleAction == session.SubtitleSidecar {
		st.Sidecars = append(st.Sidecars, api.Sidecar{TrackId: t.ID, Language: optStr(t.Language), Title: optStr(t.Title),
			Format: api.Webvtt, Forced: t.IsForced, Url: fmt.Sprintf("%s/subtitles/%d.vtt", base, t.ID)})
	}
	return st, nil
}

// update re-decides after a mid-playback change and answers continue or a
// new stream at the session's position.
func (s *Server) update(ctx context.Context, id int64, change func(*playback.Snapshot, *playback.Selection)) (api.PlaybackUpdate, error) {
	v, err := s.owned(ctx, id)
	if err != nil {
		return api.PlaybackUpdate{}, err
	}
	action := api.Continue
	if change != nil {
		restart, err := s.svc.Playback.Update(v, change)
		if err != nil {
			return api.PlaybackUpdate{}, err
		}
		if restart {
			action = api.Restart
		}
	}
	st, err := s.stream(v)
	return api.PlaybackUpdate{Action: action, Stream: st}, err
}

// UpdatePlaybackDisplay takes a new display group.
func (s *Server) UpdatePlaybackDisplay(ctx context.Context, r api.UpdatePlaybackDisplayRequestObject) (api.UpdatePlaybackDisplayResponseObject, error) {
	u, err := s.update(ctx, r.SessionId, func(sn *playback.Snapshot, _ *playback.Selection) { sn.Display = toDisplay(*r.Body) })
	return api.UpdatePlaybackDisplay200JSONResponse{PlaybackUpdatedJSONResponse: api.PlaybackUpdatedJSONResponse(u)}, err
}

// UpdatePlaybackAudioRoute takes a new audio route group.
func (s *Server) UpdatePlaybackAudioRoute(ctx context.Context, r api.UpdatePlaybackAudioRouteRequestObject) (api.UpdatePlaybackAudioRouteResponseObject, error) {
	u, err := s.update(ctx, r.SessionId, func(sn *playback.Snapshot, _ *playback.Selection) { sn.Route = toRoute(*r.Body) })
	return api.UpdatePlaybackAudioRoute200JSONResponse{PlaybackUpdatedJSONResponse: api.PlaybackUpdatedJSONResponse(u)}, err
}

// UpdatePlaybackNetwork takes a new network path group or quality cap.
func (s *Server) UpdatePlaybackNetwork(ctx context.Context, r api.UpdatePlaybackNetworkRequestObject) (api.UpdatePlaybackNetworkResponseObject, error) {
	if err := checkNetwork(*r.Body); err != nil {
		return nil, err
	}
	n := toNetwork(ctx, *r.Body, s.home(ctx))
	u, err := s.update(ctx, r.SessionId, func(sn *playback.Snapshot, _ *playback.Selection) { sn.Network = n })
	return api.UpdatePlaybackNetwork200JSONResponse{PlaybackUpdatedJSONResponse: api.PlaybackUpdatedJSONResponse(u)}, err
}

// ReportPlaybackThroughput records any position, stores telemetry and
// answers restart when the slow loop gave an Auto session its floor rung.
func (s *Server) ReportPlaybackThroughput(ctx context.Context, r api.ReportPlaybackThroughputRequestObject) (api.ReportPlaybackThroughputResponseObject, error) {
	v, err := s.owned(ctx, r.SessionId)
	if err != nil {
		return nil, err
	}
	b := r.Body
	if b.PositionMs != nil {
		if err := s.recordPosition(v, *b.PositionMs); err != nil {
			return nil, err
		}
	}
	restart, err := s.svc.Playback.Telemetry(v, &session.Sample{SessionID: v.ID, At: time.Now().UTC(), MeasuredBitrate: b.MeasuredBitrate,
		BufferMs: b.BufferMs, StallCount: b.StallCount, CurrentRung: b.CurrentRung, RungHeight: b.RungHeight, RungBitrate: b.RungBitrate})
	if err != nil {
		return nil, err
	}
	action := api.Continue
	if restart {
		action = api.Restart
	}
	st, err := s.stream(v)
	return api.ReportPlaybackThroughput200JSONResponse{PlaybackUpdatedJSONResponse: api.PlaybackUpdatedJSONResponse{Action: action, Stream: st}}, err
}

// SelectPlaybackTracks takes a new audio or subtitle choice.
func (s *Server) SelectPlaybackTracks(ctx context.Context, r api.SelectPlaybackTracksRequestObject) (api.SelectPlaybackTracksResponseObject, error) {
	u, err := s.update(ctx, r.SessionId, func(_ *playback.Snapshot, sel *playback.Selection) {
		*sel = playback.Selection{AudioTrackID: r.Body.AudioTrackId, SubtitleTrackID: r.Body.SubtitleTrackId}
	})
	return api.SelectPlaybackTracks200JSONResponse{PlaybackUpdatedJSONResponse: api.PlaybackUpdatedJSONResponse(u)}, err
}

// ReportPlaybackEvent continues after a stall; a client failure marks the
// entry in use bad and restarts on the next one at the reported position.
func (s *Server) ReportPlaybackEvent(ctx context.Context, r api.ReportPlaybackEventRequestObject) (api.ReportPlaybackEventResponseObject, error) {
	v, err := s.owned(ctx, r.SessionId)
	if err != nil {
		return nil, err
	}
	if err := s.svc.Sessions.Repo().SetPosition(v.ID, r.Body.PositionMs, time.Now().UTC()); err != nil {
		return nil, err
	}
	v.PositionMs = r.Body.PositionMs
	action := api.Continue
	if r.Body.Kind == api.ClientFailure {
		err := s.svc.Playback.ClientFailed(v, or(r.Body.Reason, "client failure"))
		if errors.Is(err, db.ErrNotFound) {
			return nil, errPlanExhausted
		}
		if err != nil {
			return nil, err
		}
		action = api.Restart
	}
	st, err := s.stream(v)
	return api.ReportPlaybackEvent200JSONResponse{PlaybackUpdatedJSONResponse: api.PlaybackUpdatedJSONResponse{Action: action, Stream: st}}, err
}

// ReportPlaybackPosition records progress on the session and on the
// profile's watch state.
func (s *Server) ReportPlaybackPosition(ctx context.Context, r api.ReportPlaybackPositionRequestObject) (api.ReportPlaybackPositionResponseObject, error) {
	v, err := s.owned(ctx, r.SessionId)
	if err != nil {
		return nil, err
	}
	return api.ReportPlaybackPosition204Response{}, s.recordPosition(v, r.Body.PositionMs)
}

// recordPosition sets the session's position, which keeps it live, and
// the profile's watch state, which may mark the item watched near the end;
// a station session leaves the watch state alone. A video's position paces
// its encoder; a song's encoder paces from its requests alone.
func (s *Server) recordPosition(v *session.Session, positionMs int64) error {
	if err := s.svc.Sessions.Repo().SetPosition(v.ID, positionMs, time.Now().UTC()); err != nil {
		return err
	}
	v.PositionMs = positionMs
	if v.Station {
		return nil
	}
	f, media, err := s.svc.Playback.Media(v.MediaFileID)
	if err != nil {
		return err
	}
	song := playback.Song(media)
	if !song {
		s.svc.Playback.Engine.Playhead(v.ID, positionMs)
	}
	ed, err := s.svc.Editions.Repo().Of(v.ItemID, v.MediaFileID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	change := func(w *watchstate.State) {
		w.Progress(positionMs, f.DurationMs)
		w.EditionID = &ed
		if ed == 0 {
			w.EditionID = nil
		}
	}
	if song {
		// A song's play reloads no screen anywhere, so its state is saved untold.
		_, _, err = saveWatchStateIn(s.svc.WatchStates.Repo(), v.AccountID, v.ItemID, nil, change)
		return err
	}
	_, err = s.saveWatchState(v.AccountID, v.ItemID, change)
	return err
}

// profileSession is a session of the active profile, open or ended, on any
// device of the household; another profile's is not found.
func (s *Server) profileSession(ctx context.Context, id int64) (*session.Session, error) {
	v, err := s.svc.Sessions.Repo().Get(id)
	if err == nil && v.AccountID != caller(ctx).profile.ID {
		err = errNotFound
	}
	return v, err
}

func (s *Server) toPlaybackSession(ctx context.Context, v *session.Session) (api.PlaybackSession, error) {
	devices := s.svc.Devices.Repo()
	d, err := devices.Get(v.DeviceID)
	if err != nil {
		return api.PlaybackSession{}, err
	}
	out := api.PlaybackSession{Id: v.ID, ItemId: v.ItemID, DeviceId: d.ID, DeviceName: d.Name, Platform: api.Platform(d.Platform),
		ThisDevice: d.ID == caller(ctx).device.ID, PositionMs: v.PositionMs, UpdatedAt: v.UpdatedAt, EndedAt: v.EndedAt}
	if v.MovedTo != nil {
		to, err := devices.Get(*v.MovedTo)
		if err != nil {
			return out, err
		}
		out.MovedToDeviceName = &to.Name
	}
	return out, nil
}

// handoffKinds are the titles offered as playing on another device: what
// resumes, which a song does not.
var handoffKinds = []string{item.Movie, item.Episode, item.Video}

// ListPlaybackSessions pages what the active profile plays now, anywhere.
func (s *Server) ListPlaybackSessions(ctx context.Context, r api.ListPlaybackSessionsRequestObject) (api.ListPlaybackSessionsResponseObject, error) {
	all, err := s.svc.Playback.Playing(caller(ctx).profile.ID)
	if err != nil {
		return nil, err
	}
	out := api.ListPlaybackSessions200JSONResponse{Items: []api.PlaybackSession{}}
	var rows []*session.Session
	for _, v := range all {
		if r.Params.ItemId != nil && v.ItemID != *r.Params.ItemId {
			continue
		}
		if it, err := s.svc.Items.Repo().Get(v.ItemID); err != nil {
			return nil, err
		} else if slices.Contains(handoffKinds, it.Kind) {
			rows = append(rows, v)
		}
	}
	limit, offset := paging(r.Params.Limit, r.Params.Offset)
	rows, out.NextOffset = slicePage(rows, limit, offset)
	for _, v := range rows {
		p, err := s.toPlaybackSession(ctx, v)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, p)
	}
	return out, nil
}

// GetPlaybackSession reads one session of the active profile.
func (s *Server) GetPlaybackSession(ctx context.Context, r api.GetPlaybackSessionRequestObject) (api.GetPlaybackSessionResponseObject, error) {
	v, err := s.profileSession(ctx, r.SessionId)
	if err != nil {
		return nil, err
	}
	out, err := s.toPlaybackSession(ctx, v)
	return api.GetPlaybackSession200JSONResponse(out), err
}

// MovePlaybackSession is Move here: it ends the profile's session on
// another device, which the calling device then starts its own in place of.
func (s *Server) MovePlaybackSession(ctx context.Context, r api.MovePlaybackSessionRequestObject) (api.MovePlaybackSessionResponseObject, error) {
	v, err := s.profileSession(ctx, r.SessionId)
	if err != nil {
		return nil, err
	}
	here := caller(ctx).device.ID
	if v.DeviceID == here || v.Station || v.EndedAt != nil {
		return nil, fail(409, api.ErrorCodeConflict, "only a title playing on another device moves here")
	}
	if err := s.svc.Playback.Move(v.ID, here); errors.Is(err, db.ErrNotFound) {
		return nil, fail(409, api.ErrorCodeConflict, "the session has ended")
	} else if err != nil {
		return nil, err
	}
	if v, err = s.svc.Sessions.Repo().Get(v.ID); err != nil {
		return nil, err
	}
	out, err := s.toPlaybackSession(ctx, v)
	return api.MovePlaybackSession200JSONResponse(out), err
}

// EndPlayback records any final position, then closes the calling device's
// session and stops its encoder.
func (s *Server) EndPlayback(ctx context.Context, r api.EndPlaybackRequestObject) (api.EndPlaybackResponseObject, error) {
	v, err := s.owned(ctx, r.SessionId)
	if err != nil {
		return nil, err
	}
	if r.Params.PositionMs != nil {
		if err := s.recordPosition(v, *r.Params.PositionMs); err != nil {
			return nil, err
		}
	}
	return api.EndPlayback204Response{}, s.svc.Playback.End(v.ID)
}
