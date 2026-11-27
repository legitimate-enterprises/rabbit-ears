package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/live"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// ErrNothingPlayable means no plan entry fits what the client reported.
var ErrNothingPlayable = errors.New("nothing the client reported can play this file")

// PlaybackService runs sessions: it decides plans, walks them on failure,
// re-decides on a mid-playback group change and owns the encoders.
//
// The snapshot and track choice of each open session live in memory only;
// after a restart a session keeps its stored plan and group updates answer
// continue until the client starts a new session.
type PlaybackService struct {
	sessions *session.Repo
	files    *mediafile.Repo
	devices  *device.Repo
	Engine   *transcode.Manager
	// Hardware is whether the host encodes video in hardware; without it,
	// plans convert only in software.
	Hardware bool
	// Events hears of every session that starts, ends or moves, but those
	// tuned in to a station.
	Events *live.Hub
	// channels is set by NewChannelService; a station's session is its.
	channels *ChannelService

	mu   sync.Mutex
	live map[uid.ID]*liveSession
}

type liveSession struct {
	snap playback.Snapshot
	sel  playback.Selection
	// serverMoved is set when the server failed the entry the client was
	// playing, so the client's own failure report for it is not a second one.
	serverMoved bool
}

// NewPlaybackService returns a PlaybackService over the repositories and engine.
func NewPlaybackService(sessions *session.Repo, files *mediafile.Repo, devices *device.Repo, engine *transcode.Manager) *PlaybackService {
	return &PlaybackService{sessions: sessions, files: files, devices: devices, Engine: engine, Hardware: transcode.Hardware(), live: map[uid.ID]*liveSession{}}
}

// Media loads a file and its probed tracks in the decision's terms.
func (p *PlaybackService) Media(fileID uid.ID) (*mediafile.File, playback.Media, error) {
	f, err := p.files.File(fileID)
	if err != nil {
		return nil, playback.Media{}, err
	}
	tracks, err := p.files.Tracks(fileID, "")
	if err != nil {
		return nil, playback.Media{}, err
	}
	m := playback.Media{Container: f.Container, SizeBytes: f.SizeBytes, Tracks: tracks}
	if f.Bitrate != nil {
		m.Bitrate = *f.Bitrate
	}
	if f.DurationMs != nil {
		m.DurationMs = *f.DurationMs
	}
	return f, m, nil
}

// Live returns an open session's snapshot and track choice, if this process
// started it.
func (p *PlaybackService) Live(id uid.ID) (playback.Snapshot, playback.Selection, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.live[id]
	if !ok {
		return playback.Snapshot{}, playback.Selection{}, false
	}
	return l.snap, l.sel, true
}

// Plan decides how a client would play a file now; it opens nothing and
// holds no transcode slot.
func (p *PlaybackService) Plan(fileID uid.ID, snap playback.Snapshot, sel playback.Selection) ([]*session.PlanEntry, error) {
	_, m, err := p.Media(fileID)
	if err != nil {
		return nil, err
	}
	plan := p.decide(m, snap, sel)
	if len(plan) == 0 {
		return nil, ErrNothingPlayable
	}
	return plan, nil
}

// decide is playback.Decide less the streams the server cannot serve of m:
// a stream's segments are counted from the file's length, and a host
// without a hardware encoder converts only in software.
func (p *PlaybackService) decide(m playback.Media, snap playback.Snapshot, sel playback.Selection) []*session.PlanEntry {
	plan := playback.Decide(m, snap, sel)
	if m.DurationMs <= 0 {
		plan = slices.DeleteFunc(plan, func(e *session.PlanEntry) bool { return e.Verdict != session.DirectPlay })
	}
	if !p.Hardware {
		plan = slices.DeleteFunc(plan, func(e *session.PlanEntry) bool { return e.Encoder == session.EncoderHardware })
	}
	return plan
}

// Menu is playback.Menu less the sizes the server cannot serve of m, as
// decide drops their entries.
func (p *PlaybackService) Menu(m playback.Media, snap playback.Snapshot, sel playback.Selection) []playback.Rung {
	rungs := playback.Menu(m, snap, sel)
	if m.DurationMs <= 0 {
		rungs = slices.DeleteFunc(rungs, func(r playback.Rung) bool { return r.Verdict != session.DirectPlay })
	}
	return rungs
}

// OnDisk keeps the files that are on disk now and sets each file's missing
// mark to match, the mark being only as fresh as the last scan.
func (p *PlaybackService) OnDisk(files []*mediafile.File) ([]*mediafile.File, error) {
	now := time.Now().UTC()
	var out []*mediafile.File
	for _, f := range files {
		at := &now
		if _, err := os.Stat(f.Path); err == nil {
			at, out = nil, append(out, f)
		}
		if err := p.files.SetMissing(f, at); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Best is the file of files the client plays best now, by playback.Best
// over the plans Plan decides; ErrNothingPlayable when it can play none.
func (p *PlaybackService) Best(files []*mediafile.File, snap playback.Snapshot, sel playback.Selection) (*mediafile.File, error) {
	return p.best(files, snap, sel, p.decide)
}

// best is Best over the plans plan decides.
func (p *PlaybackService) best(files []*mediafile.File, snap playback.Snapshot, sel playback.Selection,
	plan func(playback.Media, playback.Snapshot, playback.Selection) []*session.PlanEntry) (*mediafile.File, error) {
	opts := make([]playback.Option, len(files))
	for i, f := range files {
		_, m, err := p.Media(f.ID)
		if err != nil {
			return nil, err
		}
		opts[i] = playback.Option{Media: m, Plan: plan(m, snap, sel)}
	}
	i := playback.Best(opts, snap, sel)
	if i < 0 {
		return nil, ErrNothingPlayable
	}
	return files[i], nil
}

func newKey() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

// logDecision records the verdict chosen for a session and, when it is not
// direct play, why.
func (p *PlaybackService) logDecision(s *session.Session, what string, snap playback.Snapshot, sel playback.Selection, e *session.PlanEntry) {
	_, m, err := p.Media(s.MediaFileID)
	if err != nil {
		return
	}
	logstore.Log("playback").Info(fmt.Sprintf("Session %s %s: %s", s.ID, what, e.Verdict),
		"session_id", s.ID, "item_id", s.ItemID, "transcode_reason", playback.Reason(m, snap, sel, e))
}

// Start decides s's plan, stores the session and points it at the first
// entry the server can run now.
func (p *PlaybackService) Start(s *session.Session, snap playback.Snapshot, sel playback.Selection) error {
	plan, err := p.Plan(s.MediaFileID, snap, sel)
	if err != nil {
		return err
	}
	if err := p.open(s, plan, snap, sel); err != nil {
		return err
	}
	if err := p.activate(s); err != nil {
		return err
	}
	p.announce(s, live.Started)
	return nil
}

// open stores s with plan as the one open session of its device.
func (p *PlaybackService) open(s *session.Session, plan []*session.PlanEntry, snap playback.Snapshot, sel playback.Selection) error {
	// One device plays one stream; what it played before is abandoned and
	// must not keep an encoder or a transcode slot.
	if err := p.EndDevice(s.DeviceID); err != nil {
		return err
	}
	var err error
	if s.StreamKey, err = newKey(); err != nil {
		return err
	}
	if err := p.sessions.Create(s, plan); err != nil {
		return err
	}
	p.mu.Lock()
	p.live[s.ID] = &liveSession{snap: snap, sel: sel}
	p.mu.Unlock()
	p.logDecision(s, "started", snap, sel, plan[0])
	return nil
}

// PlayingWithin is how recently a session must have reported to count as
// still playing; reports come every 10 seconds.
const PlayingWithin = 30 * time.Second

// Playing lists what an account plays now on any device, newest first:
// its open sessions that reported within PlayingWithin, stations aside.
func (p *PlaybackService) Playing(accountID uid.ID) ([]*session.Session, error) {
	return p.sessions.Playing(accountID, time.Now().Add(-PlayingWithin))
}

// announce tells the profile's devices of a video's session; a station's or
// a song's changes nothing they show.
func (p *PlaybackService) announce(s *session.Session, change string) {
	if s.Station {
		return
	}
	if _, m, err := p.Media(s.MediaFileID); err == nil && playback.Song(m) {
		return
	}
	p.Events.Publish(live.Event{Kind: live.Playback, Change: change, AccountIDs: []uid.ID{s.AccountID}, ItemID: ref(s.ItemID), SessionID: ref(s.ID),
		DeviceID: ref(s.DeviceID)})
}

// ended frees an ended session's encoder and slot and announces it.
func (p *PlaybackService) ended(id uid.ID, change string, err error) error {
	p.Forget(id)
	if err != nil {
		return err
	}
	if s, err := p.sessions.Get(id); err == nil {
		p.announce(s, change)
	}
	return nil
}

// Move ends a session because the device toDeviceID takes its title over.
func (p *PlaybackService) Move(id, toDeviceID uid.ID) error {
	return p.ended(id, live.Moved, p.sessions.Move(id, toDeviceID, time.Now().UTC()))
}

// activate holds a transcode slot while the current entry converts video.
// A full engine is a server failure: the entry is marked bad and the next
// one tried; with none left the session ends with transcode.ErrBusy.
func (p *PlaybackService) activate(s *session.Session) error {
	for {
		plan, err := p.sessions.Plan(s.ID)
		if err != nil {
			return err
		}
		if s.CurrentEntry >= len(plan) || plan[s.CurrentEntry].VideoAction != session.ActionConvert {
			p.Engine.Release(s.ID)
			return nil
		}
		if p.Engine.Acquire(s.ID) == nil {
			return nil
		}
		next, err := p.sessions.FailEntry(s.ID, s.CurrentEntry, session.ServerFailure+transcode.ErrBusy.Error())
		if errors.Is(err, session.ErrMoved) {
			s.CurrentEntry = next
			continue
		}
		if errors.Is(err, db.ErrNotFound) {
			_ = p.End(s.ID)
			return transcode.ErrBusy
		}
		if err != nil {
			return err
		}
		s.CurrentEntry = next
	}
}

// Update applies a mid-playback change and re-decides. It reports restart
// when the best entry changed; the session then points at the new plan's
// first entry and keeps its position.
func (p *PlaybackService) Update(s *session.Session, change func(*playback.Snapshot, *playback.Selection)) (bool, error) {
	p.mu.Lock()
	l, ok := p.live[s.ID]
	if !ok {
		p.mu.Unlock()
		return false, nil
	}
	change(&l.snap, &l.sel)
	snap, sel := l.snap, l.sel
	p.mu.Unlock()
	if p.channels != nil && p.channels.Tuned(s.ID) {
		return p.channels.changed(s, snap, sel)
	}
	plan, err := p.Plan(s.MediaFileID, snap, sel)
	if errors.Is(err, ErrNothingPlayable) {
		return false, nil // keep what plays rather than stop
	}
	if err != nil {
		return false, err
	}
	cur, err := p.sessions.Plan(s.ID)
	if err != nil {
		return false, err
	}
	// What the client refused in this session stays out of its new plan.
	plan = playback.Without(plan, slices.DeleteFunc(slices.Clone(cur), func(e *session.PlanEntry) bool {
		return !e.Bad || strings.HasPrefix(e.BadReason, session.ServerFailure)
	}))
	if s.CurrentEntry < len(cur) && plan[0].Same(cur[s.CurrentEntry]) {
		return false, nil
	}
	if err := p.sessions.ReplacePlan(s.ID, plan); err != nil {
		return false, err
	}
	if s.StreamKey, err = newKey(); err != nil {
		return false, err
	}
	if err := p.sessions.SetStreamKey(s.ID, s.StreamKey); err != nil {
		return false, err
	}
	s.CurrentEntry = 0
	p.Engine.Stop(s.ID)
	p.logDecision(s, "re-decided after a change", snap, sel, plan[0])
	return true, p.activate(s)
}

// ClientFailed marks the entry the client was playing bad and moves down
// the plan; db.ErrNotFound means nothing is left. A report for an entry the
// server already failed only restarts the client on the current one.
func (p *PlaybackService) ClientFailed(s *session.Session, reason string) error {
	p.mu.Lock()
	l := p.live[s.ID]
	moved := l != nil && l.serverMoved
	if moved {
		l.serverMoved = false
	}
	p.mu.Unlock()
	if moved {
		return nil
	}
	plan, err := p.sessions.Plan(s.ID)
	if err != nil {
		return err
	}
	verdict := ""
	if s.CurrentEntry < len(plan) {
		verdict = plan[s.CurrentEntry].Verdict
	}
	model := "unknown"
	if d, err := p.devices.Get(s.DeviceID); err == nil {
		model = d.Platform + " " + d.Name
	}
	log := logstore.Log("playback").With("session_id", s.ID, "item_id", s.ItemID)
	log.Warn(fmt.Sprintf("Session %s: client failed on %s (%s)", s.ID, verdict, model), "device_id", s.DeviceID, "reason", reason)
	if p.channels != nil && p.channels.Tuned(s.ID) {
		return p.channels.failed(s)
	}
	next, err := p.sessions.FailEntry(s.ID, s.CurrentEntry, reason)
	if errors.Is(err, session.ErrMoved) {
		// The report is about an entry already left; the client restarts on
		// the one in use.
		s.CurrentEntry = next
		return nil
	}
	if err != nil {
		log.Warn(fmt.Sprintf("Session %s: no entries left to fall back to", s.ID))
		return err
	}
	log.Info(fmt.Sprintf("Session %s: falling back to plan entry %d", s.ID, next))
	s.CurrentEntry = next
	p.Engine.Stop(s.ID)
	return p.activate(s)
}

// ServerFailed marks entry bad after an encoder failure, if the session is
// still on it, and moves down the plan.
func (p *PlaybackService) ServerFailed(s *session.Session, entry int, cause error) {
	if s.CurrentEntry != entry {
		return
	}
	log := logstore.Log("playback").With("session_id", s.ID, "item_id", s.ItemID)
	log.Error(fmt.Sprintf("Session %s: encoder failure on plan entry %d", s.ID, entry), "err", cause)
	next, err := p.sessions.FailEntry(s.ID, entry, session.ServerFailure+cause.Error())
	if errors.Is(err, session.ErrMoved) {
		return // another report moved it, and the encoder is the new entry's
	}
	p.Engine.Stop(s.ID)
	if errors.Is(err, db.ErrNotFound) {
		log.Error(fmt.Sprintf("Session %s: no entries left to fall back to", s.ID))
		_ = p.End(s.ID)
		return
	}
	if err != nil {
		return
	}
	p.mu.Lock()
	if l := p.live[s.ID]; l != nil {
		l.serverMoved = true
	}
	p.mu.Unlock()
	s.CurrentEntry = next
	if err := p.activate(s); err != nil {
		log.Error(fmt.Sprintf("Session %s: no fallback available", s.ID), "err", err)
	}
}

// Telemetry stores a throughput report and runs the slow loop: in Auto,
// three samples in a row on the ladder's bottom rung with the stall count
// rising across them restart s once, with the floor rung under the ladder.
// It reports whether s restarted.
func (p *PlaybackService) Telemetry(s *session.Session, v *session.Sample) (bool, error) {
	if err := p.sessions.AddSample(v); err != nil {
		return false, err
	}
	if due, err := p.floorDue(s); !due || err != nil {
		return false, err
	}
	return p.Update(s, func(sn *playback.Snapshot, _ *playback.Selection) { sn.Floor = true })
}

func (p *PlaybackService) floorDue(s *session.Session) (bool, error) {
	snap, _, ok := p.Live(s.ID)
	if !ok || snap.Floor {
		return false, nil
	}
	plan, err := p.sessions.Plan(s.ID)
	if err != nil || s.CurrentEntry >= len(plan) || !playback.Auto(snap, plan[s.CurrentEntry]) {
		return false, err
	}
	_, m, err := p.Media(s.MediaFileID)
	if err != nil {
		return false, err
	}
	rungs := playback.Rungs(plan[s.CurrentEntry], playback.VideoTrack(m))
	bottom := rungs[len(rungs)-1].Height
	samples, err := p.sessions.Samples(s.ID, 3)
	if err != nil || len(samples) < 3 {
		return false, err
	}
	last := samples[len(samples)-3:]
	for _, v := range last {
		if v.RungHeight == nil || *v.RungHeight != bottom {
			return false, nil
		}
	}
	return last[2].StallCount > last[0].StallCount, nil
}

// End closes a session and frees its encoder and slot. The row ends first so
// a segment request in between cannot start an encoder for it.
func (p *PlaybackService) End(id uid.ID) error {
	return p.ended(id, live.Ended, p.sessions.End(id, time.Now().UTC()))
}

// EndDevice ends every open session of a device.
func (p *PlaybackService) EndDevice(deviceID uid.ID) error {
	open, err := p.sessions.ActiveByDevice(deviceID)
	if err != nil {
		return err
	}
	for _, o := range open {
		if err := p.End(o.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
			return err
		}
	}
	return nil
}

// Forget frees a session's encoder, slot and live state without touching
// its row.
func (p *PlaybackService) Forget(id uid.ID) {
	if p.channels != nil {
		p.channels.leave(id)
	}
	p.Engine.Stop(id)
	p.Engine.Release(id)
	p.mu.Lock()
	delete(p.live, id)
	p.mu.Unlock()
}

// Reap ends sessions not updated since cutoff, so an abandoned player does
// not hold an encoder or a transcode slot, and forgets those whose row is
// gone, those started before a restart included.
func (p *PlaybackService) Reap(cutoff time.Time) error {
	// Taken before the rows are read, so a session started in between is not
	// mistaken for one whose row is gone.
	p.mu.Lock()
	gone := maps.Clone(p.live)
	p.mu.Unlock()
	encoding := p.Engine.Encoding()
	active, err := p.sessions.Active()
	if err != nil {
		return err
	}
	for _, s := range active {
		delete(gone, s.ID)
		encoding = slices.DeleteFunc(encoding, func(id uid.ID) bool { return id == s.ID })
	}
	for id, l := range gone {
		p.mu.Lock()
		same := p.live[id] == l
		p.mu.Unlock()
		if same {
			p.Forget(id)
		}
	}
	for _, id := range encoding {
		p.mu.Lock()
		_, live := p.live[id]
		p.mu.Unlock()
		// A channel's source is its listeners' to keep.
		if !live && (p.channels == nil || !p.channels.sourcing(id)) {
			p.Forget(id)
		}
	}
	for _, s := range active {
		if s.ModifiedAt.Before(cutoff) {
			if err := p.End(s.ID); err != nil && !errors.Is(err, db.ErrNotFound) {
				return err
			}
		}
	}
	return nil
}
