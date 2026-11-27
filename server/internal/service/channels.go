package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/channel"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/ident"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/station"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

const (
	// maxIdent is the longest an ident runs. A station whose next program
	// is further off than this is off the air.
	maxIdent = time.Hour
	// minIdent is the shortest gap an ident fills.
	minIdent = time.Second
	// identsAhead is how far ahead the hourly task renders idents.
	identsAhead = 2 * time.Hour
	// identPart is how much of a gap one rendered ident fills, so that
	// tuning in to one not rendered yet waits seconds for it. The last
	// part of a gap runs up to shortPart longer rather than leave a part
	// that short.
	identPart = 10 * time.Minute
	shortPart = 30 * time.Second
	// unboundedFor is how long a file whose keyframe probe failed is
	// converted rather than copied.
	unboundedFor = 10 * time.Minute
)

var (
	// ErrUnavailable means none of a title's files is on disk.
	ErrUnavailable = errors.New("the title is unavailable: none of its files is on disk")
	// ErrNotTuned means a session that is on no channel this process
	// started.
	ErrNotTuned = errors.New("the session is not tuned in to a station")
	// errUnbounded means a file whose keyframes cannot be probed, so its
	// video cannot be copied into segments.
	errUnbounded = errors.New("the file's keyframes cannot be probed")
)

// ChannelService plays a movies or tv station as one continuous stream: a
// live playlist per session, written by channel.Timeline from the
// schedule, whose programs come from encoders that every session with the
// same playback decision shares, and whose gaps are filled by idents
// rendered ahead. A music station is not a channel; its songs are sessions
// of their own.
type ChannelService struct {
	stations  *StationService
	playback  *PlaybackService
	playlists *playlist.Repo
	items     *item.Repo
	files     *mediafile.Repo
	sessions  *session.Repo
	// IdentDir keeps rendered idents; a temporary directory when empty.
	IdentDir string

	mu    sync.Mutex
	tuned map[uid.ID]*tuned
	// sources are the engine's sessions under ids of their own, which no
	// playback session has.
	sources map[string]*source
	// unbounded is when each file's keyframe probe last failed.
	unbounded map[string]time.Time
	renders   singleflight.Group
	// ctx ends the renders, probes and encoder starts no request waits
	// for; bg counts the renders.
	ctx  context.Context
	stop context.CancelFunc
	bg   sync.WaitGroup
}

// source is one program as one playback decision delivers it: one encoder
// and at most one transcode slot, for as long as a session lists it.
type source struct {
	stream    transcode.Stream
	listeners map[uid.ID]bool
}

// piece is a channel.Piece and where its segments come from.
type piece struct {
	channel.Piece
	src *source // nil for an ident
	// An ident's directory and card, from which it is rendered.
	dir  string
	card ident.Card
	// A program's item and file, and the plan from the entry in use on.
	itemID, fileID uid.ID
	plan           []*session.PlanEntry
}

// tuned is one session on a channel.
type tuned struct {
	mu      sync.Mutex
	session uid.ID
	station uid.ID
	viewer  station.Viewer
	// key is the stream key the timeline was written for; a session that
	// restarted has another, and starts another timeline.
	key string
	// level is how many plan entries the client has refused.
	level  int
	tl     *channel.Timeline
	pieces map[string]*piece
}

// NewChannelService returns a ChannelService over its services and repositories.
func NewChannelService(stations *StationService, pb *PlaybackService, playlists *playlist.Repo, items *item.Repo, files *mediafile.Repo,
	sessions *session.Repo) *ChannelService {
	c := &ChannelService{stations: stations, playback: pb, playlists: playlists, items: items, files: files, sessions: sessions,
		tuned: map[uid.ID]*tuned{}, sources: map[string]*source{}, unbounded: map[string]time.Time{}}
	c.ctx, c.stop = context.WithCancel(context.Background())
	pb.channels = c
	return c
}

// Close stops the idents being rendered and waits for them to end.
func (c *ChannelService) Close() {
	c.stop()
	c.bg.Wait()
}

func (c *ChannelService) identDir() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.IdentDir == "" {
		dir, err := os.MkdirTemp("", "rabbit-ears-idents-")
		if err != nil {
			dir = filepath.Join(os.TempDir(), "rabbit-ears-idents")
		}
		c.IdentDir = dir
	}
	return c.IdentDir
}

// Tuned reports a session that is on a channel.
func (c *ChannelService) Tuned(sessionID uid.ID) bool { return c.of(sessionID) != nil }

func (c *ChannelService) of(sessionID uid.ID) *tuned {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tuned[sessionID]
}

// channelPlan is the plan for a stream that plays on from one file to the
// next: no direct play, which hands over one file; one rendition, which is
// what sessions can share; and no sidecar, which belongs to one file. A
// file of unknown length has no segments to list, so no plan.
func (c *ChannelService) channelPlan(m playback.Media, snap playback.Snapshot, sel playback.Selection) []*session.PlanEntry {
	plan := slices.DeleteFunc(c.playback.decide(m, snap, sel), func(e *session.PlanEntry) bool { return e.Verdict == session.DirectPlay })
	for _, e := range plan {
		if e.VideoAction == session.ActionConvert {
			e.SingleRung, e.Floor = true, false
		}
		if e.SubtitleAction == session.SubtitleSidecar {
			e.SubtitleAction, e.SubtitleTrackID = session.SubtitleNone, nil
		}
	}
	return plan
}

// decide is the file of a program that a device plays best and its plan
// from the entry level on: ErrUnavailable with no file on disk,
// ErrNothingPlayable with no way to stream one.
func (c *ChannelService) decide(p *station.Program, snap playback.Snapshot, sel playback.Selection, level int) (*mediafile.File, playback.Media, []*session.PlanEntry, error) {
	files, err := c.files.FilesForEdition(p.EditionID)
	if err == nil {
		files, err = c.playback.OnDisk(files)
	}
	if err != nil {
		return nil, playback.Media{}, nil, err
	}
	if len(files) == 0 {
		return nil, playback.Media{}, nil, ErrUnavailable
	}
	f, err := c.playback.Best(files, snap, sel)
	if err != nil {
		return nil, playback.Media{}, nil, err
	}
	_, m, err := c.playback.Media(f.ID)
	if err != nil {
		return nil, playback.Media{}, nil, err
	}
	plan := c.channelPlan(m, snap, sel)
	c.mu.Lock()
	unbounded := c.unbounded[f.Path]
	c.mu.Unlock()
	if time.Since(unbounded) < unboundedFor {
		plan = slices.DeleteFunc(plan, func(e *session.PlanEntry) bool { return copies(m, e) })
	}
	if len(plan) == 0 {
		return nil, playback.Media{}, nil, ErrNothingPlayable
	}
	return f, m, plan[min(level, len(plan)-1):], nil
}

// copies reports an entry that copies the file's video, whose segments
// follow the file's keyframes.
func copies(m playback.Media, e *session.PlanEntry) bool {
	return playback.VideoTrack(m) != nil && e.VideoAction == session.ActionCopy
}

// bounds are the keyframes a file's copied video is cut on. Without wait,
// a file not probed yet answers channel.ErrPending while the probe runs.
func (c *ChannelService) bounds(ctx context.Context, f *mediafile.File, wait bool) ([]transcode.Bound, error) {
	engine := c.playback.Engine
	if b, ok := engine.BoundsKnown(f.Path); ok {
		return b, nil
	}
	probe := func(ctx context.Context) ([]transcode.Bound, error) {
		b, err := engine.Bounds(ctx, f.Path, deref(f.DurationMs))
		if err != nil && ctx.Err() == nil {
			c.mu.Lock()
			c.unbounded[f.Path] = time.Now()
			c.mu.Unlock()
			logstore.Log("playback").Warn("A station cannot copy this file's video: its keyframes cannot be probed", "path", f.Path, "err", err)
			return nil, errUnbounded
		}
		return b, err
	}
	if wait {
		return probe(ctx)
	}
	go func() { _, _ = probe(c.ctx) }()
	return nil, channel.ErrPending
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// listen adds a session to the source named by key, making it from stream
// when it is new, with a transcode slot when it converts video. A full
// engine first takes back the slots of sources whose encoders ran out.
func (c *ChannelService) listen(sessionID uid.ID, key string, stream transcode.Stream, convert bool) (*source, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	src := c.sources[key]
	if src == nil {
		stream.SessionID = uid.New()
		engine := c.playback.Engine
		if convert && engine.Acquire(stream.SessionID) != nil {
			ids := make([]uid.ID, 0, len(c.sources))
			for _, o := range c.sources {
				ids = append(ids, o.stream.SessionID)
			}
			// Rest waits on each source's session lock, held across an
			// encoder's start or stop, so it runs without c.mu; another
			// listener may make this source meanwhile.
			c.mu.Unlock()
			for _, id := range ids {
				engine.Rest(id)
			}
			c.mu.Lock()
			if src = c.sources[key]; src == nil {
				if err := engine.Acquire(stream.SessionID); err != nil {
					return nil, err
				}
			}
		}
		if src == nil {
			src = &source{stream: stream, listeners: map[uid.ID]bool{}}
			c.sources[key] = src
		}
	}
	src.listeners[sessionID] = true
	return src, nil
}

// drop takes a session off a source; the last to go stops its encoder and
// frees its slot and segments.
// sourcing reports an engine session that is a source's.
func (c *ChannelService) sourcing(id uid.ID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, src := range c.sources {
		if src.stream.SessionID == id {
			return true
		}
	}
	return false
}

func (c *ChannelService) drop(sessionID uid.ID, src *source) {
	c.mu.Lock()
	delete(src.listeners, sessionID)
	last := len(src.listeners) == 0
	if last {
		for k, v := range c.sources {
			if v == src {
				delete(c.sources, k)
			}
		}
	}
	c.mu.Unlock()
	if last {
		c.playback.Engine.Stop(src.stream.SessionID)
		c.playback.Engine.Release(src.stream.SessionID)
	}
}

// leave takes a session off its channel.
func (c *ChannelService) leave(sessionID uid.ID) {
	c.mu.Lock()
	t := c.tuned[sessionID]
	delete(c.tuned, sessionID)
	c.mu.Unlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.clear(c)
}

// clear forgets the timeline and what it listed.
func (t *tuned) clear(c *ChannelService) {
	for _, p := range t.pieces {
		if p.src != nil {
			c.drop(t.session, p.src)
		}
	}
	t.tl, t.pieces = &channel.Timeline{}, map[string]*piece{}
}

// Tune opens sess for a viewer on st's channel, at the program on now or
// at the ident before the next: an OffAirError when neither is on,
// ErrProgramHidden when the program fails the viewer's kid filter,
// ErrUnavailable or ErrNothingPlayable when the device cannot be given it.
func (c *ChannelService) Tune(ctx context.Context, st *station.Station, v station.Viewer, sess *session.Session, snap playback.Snapshot, sel playback.Selection) error {
	now := c.stations.Now()
	p, _, err := c.stations.Tune(st, v.Audience)
	var off *OffAirError
	if errors.As(err, &off) {
		next, nerr := c.stations.repo.Upcoming(st.ID, now, item.Audience{}, 1)
		if nerr != nil {
			return nerr
		}
		if len(next) == 0 || next[0].Start.Sub(now) > maxIdent {
			return err
		}
		ok, verr := c.items.Visible(next[0].ItemID, v.Audience)
		if verr != nil {
			return verr
		}
		if !ok {
			return err
		}
		p, err = next[0], nil
	}
	if err != nil {
		return err
	}
	f, m, plan, err := c.decide(p, snap, sel, 0)
	// An ident ahead of a program the device cannot be given is not
	// played; tuning in when it starts says why.
	if off != nil && (errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNothingPlayable)) {
		return off
	}
	if err == nil && copies(m, plan[0]) {
		// The first playlist lists this file's segments, so the probe is
		// waited for; one that fails decides again without the copy.
		if _, err = c.bounds(ctx, f, true); errors.Is(err, errUnbounded) {
			f, _, plan, err = c.decide(p, snap, sel, 0)
		}
	}
	if err != nil {
		return err
	}
	sess.ItemID, sess.MediaFileID, sess.PositionMs, sess.Station = p.ItemID, f.ID, 0, true
	if err := c.playback.open(sess, plan, snap, sel); err != nil {
		return err
	}
	t := &tuned{session: sess.ID, station: st.ID, viewer: v, key: sess.StreamKey, tl: &channel.Timeline{}, pieces: map[string]*piece{}}
	c.mu.Lock()
	c.tuned[sess.ID] = t
	c.mu.Unlock()
	if _, err := c.Playlist(sess); err != nil {
		return errors.Join(err, c.playback.End(sess.ID))
	}
	return nil
}

// Playlist slides sess's window to now and returns its live playlist.
// Sources no longer listed lose this listener, and the session's row
// follows the program on air.
func (c *ChannelService) Playlist(sess *session.Session) (string, error) {
	t := c.of(sess.ID)
	if t == nil {
		return "", ErrNotTuned
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// A session that left meanwhile is cleared; listing for it would hold
	// sources nothing drops.
	if c.of(sess.ID) != t {
		return "", ErrNotTuned
	}
	if t.key != sess.StreamKey {
		t.clear(c)
		t.key = sess.StreamKey
	}
	now := c.stations.Now()
	err := t.tl.Advance(now, func(at time.Time) (*channel.Piece, error) {
		p, err := c.pieceAt(t, at, now)
		if p == nil {
			return nil, err
		}
		return &p.Piece, err
	})
	listed := map[string]bool{}
	for _, p := range t.tl.Pieces() {
		listed[p.ID] = true
	}
	for id, p := range t.pieces {
		if !listed[id] {
			if p.src != nil {
				c.drop(t.session, p.src)
			}
			delete(t.pieces, id)
		}
	}
	if err != nil {
		return "", err
	}
	if on := t.tl.OnAir(now); on != nil {
		if p := t.pieces[on.ID]; p != nil && p.src != nil && (p.itemID != sess.ItemID || p.fileID != sess.MediaFileID) {
			if err := c.record(sess, p); err != nil {
				return "", err
			}
		}
	}
	return t.tl.Playlist(), nil
}

// record points the session's row at the program now on and its plan, so
// what the session reports and what an admin sees is what plays.
func (c *ChannelService) record(sess *session.Session, p *piece) error {
	if err := c.sessions.SetProgram(sess.ID, p.itemID, p.fileID); err != nil {
		return err
	}
	sess.ItemID, sess.MediaFileID, sess.CurrentEntry = p.itemID, p.fileID, 0
	return c.sessions.ReplacePlan(sess.ID, clonePlan(p.plan))
}

func clonePlan(plan []*session.PlanEntry) []*session.PlanEntry {
	out := make([]*session.PlanEntry, len(plan))
	for i, e := range plan {
		v := *e
		out[i] = &v
	}
	return out
}

// pieceAt is the lineup of t's channel: the program on at at, else the
// ident up to the next program, else nil, which ends the stream. A station
// or program the viewer may no longer see, or cannot be given, ends it
// too; tuning in again says why.
func (c *ChannelService) pieceAt(t *tuned, at, now time.Time) (*piece, error) {
	st, err := c.stations.Seen(t.station, t.viewer)
	if errors.Is(err, db.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	repo := c.stations.repo
	on, err := found(repo.On(st.ID, at, item.Audience{}))
	if err != nil {
		return nil, err
	}
	if on != nil {
		p, err := c.program(t, st, on, at, now)
		if p == nil || at.Before(p.End()) {
			return p, err
		}
		// Its file ran out before its slot did.
		at = on.End
	}
	next, err := repo.Upcoming(st.ID, at.Add(-time.Millisecond), item.Audience{}, 1)
	if err != nil || len(next) == 0 || next[0].Start.Sub(at) > maxIdent {
		return nil, err
	}
	if next[0].Start.Sub(at) < minIdent {
		return c.program(t, st, next[0], at, now)
	}
	if ok, err := c.items.Visible(next[0].ItemID, t.viewer.Audience); err != nil || !ok {
		return nil, err
	}
	from := next[0].Start.Add(-maxIdent)
	if prev, err := found(repo.Ended(st.ID, at)); err != nil {
		return nil, err
	} else if prev != nil && prev.End.After(from) {
		from = prev.End
	}
	card, err := c.card(st, from, next[0])
	if err != nil {
		return nil, err
	}
	snap, _, ok := c.playback.Live(t.session)
	if !ok {
		return nil, ErrNotTuned
	}
	parts := identParts(from, next[0].Start)
	n := min(int(at.Sub(from)/identPart), len(parts)-2)
	from, until := parts[n], parts[n+1]
	id := "i" + card.Key(from)
	if p := t.pieces[id]; p != nil {
		return p, nil
	}
	p := &piece{Piece: channel.Piece{ID: id, Start: from, Durations: ident.Durations(until.Sub(from)), TS: snap.TS()},
		dir: c.dir(st.ID, card, from, snap.TS()), card: card}
	t.pieces[id] = p
	go func() { _ = c.render(c.ctx, p) }()
	return p, nil
}

// render makes an ident's files unless they are there. Callers arriving
// while it renders share the one render, which a caller going away does
// not cut short.
func (c *ChannelService) render(ctx context.Context, p *piece) error {
	ch := c.renders.DoChan(p.dir, func() (any, error) {
		c.bg.Add(1)
		defer c.bg.Done()
		return nil, ident.Render(c.ctx, p.dir, p.card, p.Start, p.End(), p.TS)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-ch:
		return r.Err
	}
}

// identParts are the instants at which the idents filling a gap start,
// and the gap's end.
func identParts(from, until time.Time) []time.Time {
	parts := []time.Time{from}
	for at := from.Add(identPart); until.Sub(at) >= shortPart; at = at.Add(identPart) {
		parts = append(parts, at)
	}
	return append(parts, until)
}

func (c *ChannelService) dir(stationID uid.ID, card ident.Card, from time.Time, ts bool) string {
	name := card.Key(from)
	if ts {
		name += "-ts"
	}
	return filepath.Join(c.identDir(), stationID.String(), name)
}

// program is the piece of a program for t, made once: the file and plan
// entry decided for its device, on the source every session with the same
// decision shares. Asked for before the program starts, its encoder
// starts, so its first segments are there in time. Nil without an error
// means the viewer may not see the program or the device cannot be given
// it.
func (c *ChannelService) program(t *tuned, st *station.Station, p *station.Program, at, now time.Time) (*piece, error) {
	// A schedule written again may put another program at the same start.
	id := fmt.Sprintf("p%d-%s-%s", p.Start.UnixMilli(), p.ItemID, p.EditionID)
	if pc := t.pieces[id]; pc != nil {
		return pc, nil
	}
	if ok, err := c.items.Visible(p.ItemID, t.viewer.Audience); err != nil || !ok {
		return nil, err
	}
	snap, sel, ok := c.playback.Live(t.session)
	if !ok {
		return nil, ErrNotTuned
	}
	f, m, plan, err := c.decide(p, snap, sel, t.level)
	if errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNothingPlayable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	video := playback.VideoTrack(m)
	for i, e := range plan {
		var bounds []transcode.Bound
		if copies(m, e) {
			if bounds, err = c.bounds(c.ctx, f, false); errors.Is(err, errUnbounded) {
				continue
			} else if err != nil {
				return nil, err
			}
		}
		// A program is cut at its scheduled end: no segment starts after it.
		durs := transcode.Durations(bounds, deref(f.DurationMs))
		if video != nil && e.VideoAction == session.ActionConvert {
			durs = transcode.FoldTail(durs)
		}
		slot, offset := p.End.Sub(p.Start).Seconds(), 0.0
		for n, d := range durs {
			if offset >= slot {
				durs = durs[:n]
				break
			}
			offset += d
		}
		// Converted, a file of unknown length has no segments.
		if len(durs) == 0 {
			return nil, nil
		}
		var sub *mediafile.Track
		for _, tr := range m.Tracks {
			if e.SubtitleTrackID != nil && tr.ID == *e.SubtitleTrackID {
				sub = tr
			}
		}
		stream := transcode.Stream{Key: "0", Job: transcode.Job{Input: f.Path, Entry: e, Video: video, Audio: playback.AudioTrack(m, e.AudioTrackID),
			Subtitle: sub, Rung: playback.Rungs(e, video)[0], Bounds: bounds}}
		src, err := c.listen(t.session, sourceKey(st.ID, p, f.ID, e), stream, video != nil && e.VideoAction == session.ActionConvert)
		if errors.Is(err, transcode.ErrBusy) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pc := &piece{Piece: channel.Piece{ID: id, Start: p.Start, Durations: durs, TS: e.TS}, src: src, itemID: p.ItemID, fileID: f.ID, plan: plan[i:]}
		t.pieces[id] = pc
		if !at.After(p.Start) && now.Before(p.Start) {
			go func() { _, _ = c.playback.Engine.Segment(c.ctx, src.stream, 0) }()
		}
		return pc, nil
	}
	return nil, transcode.ErrBusy
}

// sourceKey names what a plan entry makes of one program's file: sessions
// whose keys match share one encoder. A sidecar forks no stream, so no
// subtitle is in the key but one burned in.
func sourceKey(stationID uid.ID, p *station.Program, fileID uid.ID, e *session.PlanEntry) string {
	id := func(p *uid.ID) string {
		if p == nil {
			return ""
		}
		return p.String()
	}
	return fmt.Sprintf("%s/%d/%s/%s/%s/%s/%s/%s/%d/%s/%s/%t/%d/%d/%t", stationID, p.Start.UnixMilli(), fileID, e.VideoAction, e.AudioAction,
		e.SubtitleAction, id(e.SubtitleTrackID), id(e.AudioTrackID), e.AudioChannels, e.VideoCodec, e.Encoder, e.ToneMap, e.TopHeight, e.TopBitrate, e.TS)
}

// card is what st's ident shows from from until next starts: the programs
// that follow, in the server's local time. Only programs of the station's
// playlist as it is now are named: a kid sees a station when they may see
// all of that playlist, and the schedule may still hold what it has lost.
func (c *ChannelService) card(st *station.Station, from time.Time, next *station.Program) (ident.Card, error) {
	loc := c.stations.Now().Location()
	card := ident.Card{Station: st.Name, Rating: st.Rating, Next: next.Start.In(loc)}
	ps, err := c.stations.repo.Upcoming(st.ID, from, item.Audience{}, 4)
	if err != nil {
		return card, err
	}
	order, err := c.playlists.PlayOrder(st.PlaylistID, item.Audience{}, -1, 0)
	if err != nil {
		return card, err
	}
	for _, p := range ps {
		if !slices.ContainsFunc(order, func(r playlist.Row) bool { return r.ItemID == p.ItemID }) {
			continue
		}
		row, err := c.row(p, loc)
		if err != nil {
			return card, err
		}
		card.Rows = append(card.Rows, row)
	}
	return card, nil
}

func (c *ChannelService) title(it *item.Item) (string, *item.Metadata, error) {
	m, err := c.items.Metadata(it.ID)
	if errors.Is(err, db.ErrNotFound) {
		return it.ParsedTitle, nil, nil
	}
	if err != nil || m.Title == "" {
		return it.ParsedTitle, m, err
	}
	return m.Title, m, nil
}

// row names a program as a card does: an episode by its show, code and
// title, a movie by its title and year.
func (c *ChannelService) row(p *station.Program, loc *time.Location) (ident.Row, error) {
	row := ident.Row{At: p.Start.In(loc)}
	it, err := c.items.Get(p.ItemID)
	if err != nil {
		return row, err
	}
	title, meta, err := c.title(it)
	if err != nil {
		return row, err
	}
	row.Title = title
	if it.Kind != item.Episode {
		switch {
		case meta != nil && len(meta.ReleaseDate) >= 4:
			row.Name = meta.ReleaseDate[:4]
		case it.ParsedYear != nil:
			row.Name = strconv.Itoa(*it.ParsedYear)
		}
		return row, nil
	}
	show := it
	for show.ParentID != nil && show.Kind != item.Show {
		if show, err = c.items.Get(*show.ParentID); err != nil {
			return row, err
		}
	}
	if row.Title, _, err = c.title(show); err != nil {
		return row, err
	}
	num := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	row.Episode, row.Name = fmt.Sprintf("S%d E%d", num(it.SeasonNumber), num(it.EpisodeNumber)), title
	return row, nil
}

// File is the path of one file of a piece sess lists: a program's init
// segment or segment, encoded if need be, or an ident's, rendered if it
// was not made ahead.
func (c *ChannelService) File(ctx context.Context, sess *session.Session, pieceID, name string) (string, error) {
	t := c.of(sess.ID)
	if t == nil {
		return "", ErrNotTuned
	}
	t.mu.Lock()
	p := t.pieces[pieceID]
	t.mu.Unlock()
	if p == nil {
		return "", db.ErrNotFound
	}
	ext := ".m4s"
	if p.TS {
		ext = ".ts"
	}
	n, err := strconv.Atoi(strings.TrimSuffix(name, ext))
	init := name == transcode.InitName && !p.TS
	if !init && (err != nil || !strings.HasSuffix(name, ext) || n < 0 || n >= len(p.Durations)) {
		return "", db.ErrNotFound
	}
	if p.src == nil {
		return filepath.Join(p.dir, name), c.render(ctx, p)
	}
	if init {
		// An encoder started for the init segment starts where the
		// channel is, not at the program's first segment.
		at, _ := p.At(c.stations.Now())
		return c.playback.Engine.Init(ctx, p.src.stream, at)
	}
	return c.playback.Engine.Segment(ctx, p.src.stream, n)
}

// failed moves a session whose client refused what it was sent down its
// plan, on a new stream; db.ErrNotFound when the plan has no entry left.
func (c *ChannelService) failed(sess *session.Session) error {
	t := c.of(sess.ID)
	if t == nil {
		return ErrNotTuned
	}
	plan, err := c.sessions.Plan(sess.ID)
	if err != nil {
		return err
	}
	if len(plan) < 2 {
		return db.ErrNotFound
	}
	t.mu.Lock()
	t.level++
	t.mu.Unlock()
	return c.restart(sess, plan[1:])
}

// changed re-decides the program on after a change to what the device can
// play, and reports whether its stream starts again.
func (c *ChannelService) changed(sess *session.Session, snap playback.Snapshot, sel playback.Selection) (bool, error) {
	t := c.of(sess.ID)
	if t == nil {
		return false, ErrNotTuned
	}
	_, m, err := c.playback.Media(sess.MediaFileID)
	if err != nil {
		return false, err
	}
	cur, err := c.sessions.Plan(sess.ID)
	if err != nil {
		return false, err
	}
	t.mu.Lock()
	level := t.level
	t.mu.Unlock()
	plan := c.channelPlan(m, snap, sel)
	if len(plan) == 0 {
		return false, nil // keep what plays rather than stop
	}
	plan = plan[min(level, len(plan)-1):]
	if len(cur) > 0 && plan[0].Same(cur[0]) {
		return false, nil
	}
	return true, c.restart(sess, plan)
}

// restart gives the session a plan and a new stream key, which its next
// playlist starts a new timeline for.
func (c *ChannelService) restart(sess *session.Session, plan []*session.PlanEntry) error {
	if err := c.sessions.ReplacePlan(sess.ID, clonePlan(plan)); err != nil {
		return err
	}
	key, err := newKey()
	if err != nil {
		return err
	}
	if err := c.sessions.SetStreamKey(sess.ID, key); err != nil {
		return err
	}
	sess.StreamKey, sess.CurrentEntry = key, 0
	return nil
}

// Schedule is the hourly task: StationService.ScheduleAll, then the idents
// of the gaps it leaves.
func (c *ChannelService) Schedule(ctx context.Context) (string, error) {
	summary, err := c.stations.ScheduleAll(ctx)
	if err != nil {
		return summary, err
	}
	n, err := c.Prerender(ctx)
	return fmt.Sprintf("%s, %d idents rendered", summary, n), err
}

// Prerender renders the ident of every gap that a movies or tv station's
// schedule leaves within identsAhead, as a device that plays fragmented MP4
// gets it, and deletes idents made more than a day ago. It returns how many
// it rendered.
func (c *ChannelService) Prerender(ctx context.Context) (int, error) {
	all, err := c.stations.repo.All(-1, 0)
	if err != nil {
		return 0, err
	}
	now := c.stations.Now()
	n := 0
	var errs []error
	for _, st := range all {
		if st.Kind == playlist.Music {
			continue
		}
		ps, err := c.stations.repo.Between(st.ID, now.Add(-maxIdent), now.Add(identsAhead), item.Audience{}, -1, 0)
		if err != nil {
			return n, err
		}
		for i := 1; i < len(ps); i++ {
			from, next := ps[i-1].End, ps[i]
			if gap := next.Start.Sub(from); gap < minIdent || gap > maxIdent || !next.Start.After(now) {
				continue
			}
			card, err := c.card(st, from, next)
			if err != nil {
				return n, err
			}
			parts := identParts(from, next.Start)
			for k, from := range parts[:len(parts)-1] {
				until := parts[k+1]
				dir := c.dir(st.ID, card, from, false)
				if _, err := os.Stat(dir); err == nil || !until.After(now) {
					continue
				}
				if err := ctx.Err(); err != nil {
					return n, err
				}
				if _, err, _ := c.renders.Do(dir, func() (any, error) { return nil, ident.Render(ctx, dir, card, from, until, false) }); err != nil {
					errs = append(errs, fmt.Errorf("station %s: %w", st.ID, err))
					continue
				}
				n++
			}
		}
	}
	stale, _ := filepath.Glob(filepath.Join(c.identDir(), "*", "*"))
	for _, dir := range stale {
		if info, err := os.Stat(dir); err == nil && now.Sub(info.ModTime()) > 24*time.Hour {
			_ = os.RemoveAll(dir)
		}
	}
	return n, errors.Join(errs...)
}
