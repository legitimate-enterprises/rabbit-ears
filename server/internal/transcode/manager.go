package transcode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/logstore"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
)

var (
	// ErrBusy means every video transcode slot is taken.
	ErrBusy = errors.New("transcode limit reached")
	// ErrEncoder means ffmpeg failed before producing what was asked for.
	ErrEncoder = errors.New("encoder failed")
	// errSuperseded means the encoder was stopped for another stream of the
	// session, such as the rung a player switched to, before it got there.
	errSuperseded = errors.New("encoder replaced")
)

// Config tunes a Manager; zero fields take the defaults.
type Config struct {
	Root     string        // segment cache; a fresh temporary directory when empty
	CacheCap int64         // bytes kept under Root before idle sessions are pruned
	MaxVideo int           // simultaneous video transcodes
	Ahead    int           // segments encoded past the newest request before pausing, for a stream that names none
	Reach    int           // segments past the encoder's position a request waits for; one further restarts it there
	Wait     time.Duration // longest wait for one segment
	Idle     time.Duration // longest an encoder stays paused with no request before it stops
}

// Stream is one entry at one rung of a session. Key names it within the
// session; a request with another key replaces the session's encoder.
type Stream struct {
	SessionID int64
	Key       string
	// Ahead is the segments encoded past the reported playhead, or without
	// one the newest request, before pausing, which the stream's player is
	// told to hold past its playhead; zero takes Config.Ahead, for a player
	// that keeps its own buffer.
	Ahead int
	Job   Job // Segment and OutDir are set by the Manager
}

// Manager runs at most one encoder per session.
type Manager struct {
	cfg      Config
	mu       sync.Mutex
	encoders map[int64]*encoder
	slots    map[int64]bool
	bounds   map[string][]Bound
	probes   singleflight.Group
	// locks serialize, per session, choosing an encoder with starting or
	// stopping one, so racing requests never run two, and with deleting the
	// session's segments; m.mu is not held while a process starts or dies.
	locks map[int64]*sessionLock
}

type sessionLock struct {
	sync.Mutex
	users int
}

// lock takes the session's lock and returns its release.
func (m *Manager) lock(sessionID int64) func() {
	m.mu.Lock()
	l := m.locks[sessionID]
	if l == nil {
		l = &sessionLock{}
		m.locks[sessionID] = l
	}
	l.users++
	m.mu.Unlock()
	l.Lock()
	return func() { m.unlock(sessionID, l) }
}

// tryLock is lock without the wait: nil while the session's lock is held or
// awaited.
func (m *Manager) tryLock(sessionID int64) func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks[sessionID] != nil {
		return nil
	}
	l := &sessionLock{users: 1}
	l.Lock()
	m.locks[sessionID] = l
	return func() { m.unlock(sessionID, l) }
}

func (m *Manager) unlock(sessionID int64, l *sessionLock) {
	l.Unlock()
	m.mu.Lock()
	if l.users--; l.users == 0 {
		delete(m.locks, sessionID)
	}
	m.mu.Unlock()
}

type encoder struct {
	key, dir string
	ts       bool // MPEG-TS segments, which have no init segment
	start    int
	begun    time.Time // to the second, as some filesystems keep times
	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	killed   atomic.Bool // stopped on purpose, so its exit is not a failure
	stderr   *bytes.Buffer
	mu       sync.Mutex
	newest   int       // newest segment requested
	ahead    int       // segments written past newest before the run pauses
	reach    int       // Config.Reach
	head     int       // segment of the player's reported playhead
	heard    time.Time // when head was reported; zero for never
	ready    int       // segments from start up to here are known on disk
	trimmed  int       // segments below this are deleted
	paused   bool
	asked    time.Time // when a segment was last requested
	// Joining keyframe files into segments, when the run has bounds.
	bounds []Bound
	gops   int      // next keyframe file to join
	seg    int      // segment being joined
	part   *os.File // that segment so far
	scale  uint32   // video timescale, from the init segment
	shift  float64  // seconds the TS muxer added to the source's timeline
}

// New returns a Manager. It never clears Root; the caller does that at
// startup when old segments are known to be stale.
func New(cfg Config) *Manager {
	if cfg.CacheCap == 0 {
		cfg.CacheCap = 20_000_000_000
	}
	if cfg.MaxVideo == 0 {
		cfg.MaxVideo = 3
	}
	if cfg.Ahead == 0 {
		cfg.Ahead = 5
	}
	if cfg.Reach == 0 {
		cfg.Reach = 5
	}
	if cfg.Wait == 0 {
		cfg.Wait = 60 * time.Second
	}
	if cfg.Idle == 0 {
		cfg.Idle = 5 * time.Minute
	}
	return &Manager{cfg: cfg, encoders: map[int64]*encoder{}, slots: map[int64]bool{}, bounds: map[string][]Bound{}, locks: map[int64]*sessionLock{}}
}

func (m *Manager) root() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Root == "" {
		dir, err := os.MkdirTemp("", "rabbit-ears-transcode-")
		if err != nil {
			dir = filepath.Join(os.TempDir(), "rabbit-ears-transcode")
		}
		m.cfg.Root = dir
	}
	return m.cfg.Root
}

// Acquire reserves a video transcode slot for a session; holding one already
// succeeds.
func (m *Manager) Acquire(sessionID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.slots[sessionID] && len(m.slots) >= m.cfg.MaxVideo {
		return ErrBusy
	}
	m.slots[sessionID] = true
	return nil
}

// Release frees a session's slot, if it holds one.
func (m *Manager) Release(sessionID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.slots, sessionID)
}

// Transcoding lists sessions holding a video transcode slot.
func (m *Manager) Transcoding() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []int64
	for id := range m.slots {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// Busy reports whether a session holding a video transcode slot is encoding
// or about to: its encoder runs unpaused, or it has none yet. One paused
// ahead of its player, idle ones included, or exited is not.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	var held []*encoder
	for id := range m.slots {
		e := m.encoders[id]
		if e == nil {
			m.mu.Unlock()
			return true
		}
		held = append(held, e)
	}
	m.mu.Unlock()
	for _, e := range held {
		e.mu.Lock()
		paused := e.paused
		e.mu.Unlock()
		if !paused && !e.exited() {
			return true
		}
	}
	return false
}

// Encoding lists sessions with an encoder, running or stopped.
func (m *Manager) Encoding() []int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Collect(maps.Keys(m.encoders))
}

// Limits are the engine's bounds.
type Limits struct {
	CacheCap int64 // bytes
	MaxVideo int   // simultaneous video transcodes
}

// Limits reports the cache cap and transcode limit in force.
func (m *Manager) Limits() Limits {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Limits{m.cfg.CacheCap, m.cfg.MaxVideo}
}

// SetLimits replaces the cache cap and transcode limit and prunes to the
// cap. Slots already held are kept under a lower limit; only new ones wait.
func (m *Manager) SetLimits(l Limits) {
	m.mu.Lock()
	m.cfg.CacheCap, m.cfg.MaxVideo = l.CacheCap, l.MaxVideo
	m.mu.Unlock()
	m.prune()
}

// Usage is the bytes held under the cache root.
func (m *Manager) Usage() int64 { return dirSize(m.root()) }

// Clear deletes everything in the cache except the segments of sessions in
// keep, of sessions whose encoder runs and of sessions whose lock is held;
// sidecars are re-extracted on demand.
func (m *Manager) Clear(keep map[int64]bool) error {
	root := m.root()
	entries, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var errs []error
	for _, de := range entries {
		if id, perr := strconv.ParseInt(de.Name(), 10, 64); perr != nil || !keep[id] {
			_, rerr := m.removeIdle(filepath.Join(root, de.Name()))
			errs = append(errs, rerr)
		}
	}
	return errors.Join(errs...)
}

// removeIdle deletes a directory under the root, unless it holds the
// segments of a session whose encoder runs or whose lock is held, and
// reports whether it went; a session whose encoder exited starts another at
// its next request. The lock is tried, not waited for, as prune holds its
// caller's.
func (m *Manager) removeIdle(path string) (bool, error) {
	if id, err := strconv.ParseInt(filepath.Base(path), 10, 64); err == nil {
		unlock := m.tryLock(id)
		if unlock == nil {
			return false, nil
		}
		defer unlock()
		m.mu.Lock()
		e := m.encoders[id]
		m.mu.Unlock()
		if e != nil && !e.exited() {
			return false, nil
		}
	}
	err := os.RemoveAll(path)
	return err == nil, err
}

func dirSize(p string) int64 {
	var size int64
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
}

// Rest frees the slot of a session whose encoder ran to the end of its
// input, and reports whether it did. As after an idle stop, the session's
// segments stay and it takes a slot again to start another encoder.
func (m *Manager) Rest(sessionID int64) bool {
	defer m.lock(sessionID)()
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.encoders[sessionID]
	if e == nil || !m.slots[sessionID] || !e.exited() || e.killed.Load() || e.err != nil {
		return false
	}
	delete(m.slots, sessionID)
	return true
}

func (m *Manager) dir(st Stream) string {
	return filepath.Join(m.root(), strconv.FormatInt(st.SessionID, 10), st.Key)
}

// Init returns the stream's initialization segment, starting the encoder at
// segment hint when none runs for this stream.
func (m *Manager) Init(ctx context.Context, st Stream, hint int) (string, error) {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		unlock := m.lock(st.SessionID)
		m.mu.Lock()
		e := m.encoders[st.SessionID]
		m.mu.Unlock()
		if e == nil || e.key != st.Key || e.exited() {
			// Every run's init is the same, so a rung switched back to needs
			// no encoder for it and the rung in use keeps its own. An exited
			// encoder's directory may be deleted, init and all.
			if p := filepath.Join(m.dir(st), InitName); exists(p) {
				unlock()
				return p, nil
			}
			if e, err = m.start(st, hint); err != nil {
				unlock()
				return "", err
			}
		}
		unlock()
		var p string
		if p, err = m.wait(ctx, e, InitName); !errors.Is(err, errFinished) {
			return p, err
		}
		// The run exited without its init on disk, as when the cache cap took
		// it first; one restart writes it again.
	}
	return "", err
}

// ahead is how many segments past its newest request the stream's encoder
// runs before pausing.
func (m *Manager) ahead(st Stream) int {
	if st.Ahead > 0 {
		return st.Ahead
	}
	return m.cfg.Ahead
}

// Segment returns segment n of the stream, encoding it if needed. A request
// outside what the running encoder will reach soon restarts it at n: Reach,
// not the stream's read-ahead, so a seek never waits out minutes of encoding.
func (m *Manager) Segment(ctx context.Context, st Stream, n int) (string, error) {
	name := segmentName(n, st.Job.Entry.TS)
	path := filepath.Join(m.dir(st), name)
	for attempt := 0; attempt < 2; attempt++ {
		unlock := m.lock(st.SessionID)
		m.mu.Lock()
		e := m.encoders[st.SessionID]
		m.mu.Unlock()
		if exists(path) {
			if e != nil && e.key == st.Key {
				e.request(n, m.ahead(st), st.Ahead)
			}
			unlock()
			return path, nil
		}
		// A segment before the encoder's position that is still not on disk,
		// as it may have just been published, was deleted behind the playhead.
		if e == nil || e.key != st.Key || (n < e.next() && !exists(path)) || n > e.next()+m.cfg.Reach || e.exited() {
			var err error
			if e, err = m.start(st, n); err != nil {
				unlock()
				return "", err
			}
		}
		e.request(n, m.ahead(st), st.Ahead)
		unlock()
		p, err := m.wait(ctx, e, name)
		if !errors.Is(err, errFinished) {
			return p, err
		}
		// Copied video splits only on source keyframes, so a clean exit can
		// skip a nominal segment; one restart at n produces it.
	}
	return "", fmt.Errorf("%w: segment %d never appeared", ErrEncoder, n)
}

var errFinished = errors.New("encoder finished")

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (m *Manager) wait(ctx context.Context, e *encoder, name string) (string, error) {
	path := filepath.Join(e.dir, name)
	deadline := time.After(m.cfg.Wait)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		select {
		case <-e.done:
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
			if e.killed.Load() {
				return "", errSuperseded
			}
			if e.err != nil {
				return "", fmt.Errorf("%w: %w: %s", ErrEncoder, e.err, strings.TrimSpace(e.stderr.String()))
			}
			return "", errFinished
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("%w: %s took longer than %s", ErrEncoder, name, m.cfg.Wait)
		case <-tick.C:
		}
	}
}

// start replaces the session's encoder with one for st beginning at segment
// n; the caller holds the session's lock. Every run cuts the same segments,
// so earlier runs' segments stay to be served; only their unjoined keyframe
// files go. A run converting video takes the session a slot unless it holds
// one, as after an idle stop or a restart, else ErrBusy; any other run frees
// the session's once the old encoder is gone.
func (m *Manager) start(st Stream, n int) (*encoder, error) {
	m.mu.Lock()
	old := m.encoders[st.SessionID]
	m.mu.Unlock()
	convert, _, _ := st.Job.modes()
	if convert {
		if err := m.Acquire(st.SessionID); err != nil {
			return nil, err
		}
	}
	if old != nil {
		m.kill(old)
	}
	if !convert {
		m.Release(st.SessionID)
	}
	m.prune()
	dir := m.dir(st)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	for _, pat := range []string{"g*.m4s", "g*.ts", ".seg-*", runInit} {
		stale, _ := filepath.Glob(filepath.Join(dir, pat))
		for _, p := range stale {
			_ = os.Remove(p)
		}
	}
	j := st.Job
	j.Segment, j.OutDir = n, dir
	args := Args(j)
	//nolint:gosec // arguments are built from probed facts and server paths
	cmd := exec.Command("ffmpeg", args...)
	e := &encoder{key: st.Key, dir: dir, ts: j.Entry.TS, start: n, begun: time.Now().Truncate(time.Second), cmd: cmd, done: make(chan struct{}), stderr: &bytes.Buffer{}, newest: n, ahead: m.ahead(st), reach: m.cfg.Reach, asked: time.Now(), seg: n}
	if j.gops() {
		e.bounds = j.Bounds
	}
	cmd.Stderr = e.stderr
	log := logstore.Log("transcode").With("session_id", st.SessionID)
	log.Info(fmt.Sprintf("Encoder started from segment %d", n), "ffmpeg_command", "ffmpeg "+strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		log.Error("Encoder could not start", "err", err)
		return nil, fmt.Errorf("%w: %w", ErrEncoder, err)
	}
	go func() {
		e.err = cmd.Wait()
		e.finish(e.err == nil)
		close(e.done)
		if e.err != nil && !e.killed.Load() {
			log.Error("Encoder exited", "err", e.err, "stderr", tail(e.stderr.String(), 400))
		}
		m.Rest(st.SessionID)
	}()
	go m.pace(st.SessionID, e)
	m.mu.Lock()
	m.encoders[st.SessionID] = e
	m.mu.Unlock()
	return e, nil
}

// pace pauses the encoder once it leads the player and resumes it when the
// player catches up. Paused for Idle, it stops.
func (m *Manager) pace(sessionID int64, e *encoder) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-e.done:
			return
		case <-tick.C:
			e.assemble()
			e.mu.Lock()
			ahead := e.leadsLocked()
			if ahead != e.paused {
				sig := syscall.SIGCONT
				if ahead {
					sig = syscall.SIGSTOP
				}
				if e.cmd.Process.Signal(sig) == nil {
					e.paused = ahead
				}
			}
			idle := e.idleLocked(m.cfg.Idle)
			e.mu.Unlock()
			if idle {
				m.sleep(sessionID, e)
			}
		}
	}
}

// leadsLocked reports a run that has written more than ahead segments past
// the player's fresh playhead and Reach past the newest request, so a player
// fetching beyond its reports never waits; without a fresh playhead, more
// than ahead past the newest request.
func (e *encoder) leadsLocked() bool {
	limit := e.newest + e.ahead
	if e.freshLocked() {
		limit = max(e.newest+e.reach, e.head+e.ahead)
	}
	return e.nextLocked()-1 > limit
}

func (e *encoder) freshLocked() bool {
	return !e.heard.IsZero() && time.Since(e.heard) < headStale
}

// Playhead records where the session's player reports it is, positionMs on
// the file's timeline, for its current encoder.
//
// shortcut: an encoder started for a seek or rung switch paces from its
// newest request until the next report, up to ten seconds; carry the
// playhead over in start if that burst of read-ahead shows on disk.
func (m *Manager) Playhead(sessionID, positionMs int64) {
	m.mu.Lock()
	e := m.encoders[sessionID]
	m.mu.Unlock()
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.head, e.heard = SegmentAt(e.bounds, positionMs), time.Now()
}

func (e *encoder) idleLocked(after time.Duration) bool {
	return e.paused && time.Since(e.asked) >= after
}

// sleep stops a session's idle encoder, keeping its segments and its place
// among the encoders so the session's next request starts one, and frees
// its slot: a paused ffmpeg still holds its hardware encoder session.
func (m *Manager) sleep(sessionID int64, e *encoder) {
	defer m.lock(sessionID)()
	m.mu.Lock()
	current := m.encoders[sessionID] == e
	m.mu.Unlock()
	e.mu.Lock()
	idle := e.idleLocked(m.cfg.Idle)
	e.mu.Unlock()
	if !current || !idle || e.exited() {
		return
	}
	m.kill(e)
	m.Release(sessionID)
	logstore.Log("transcode").With("session_id", sessionID).Info(fmt.Sprintf("Encoder stopped after %s paused", m.cfg.Idle))
}

// assemble joins the keyframe files written so far into whole segments:
// segment k holds every fragment from bounds k up to bounds k+1.
func (e *encoder) assemble() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.publishInitLocked()
	if e.bounds != nil {
		e.joinLocked()
	}
}

// publishInitLocked makes the run's init segment InitName once its first
// media file shows it is complete, unless an earlier run's is there: players
// may be reading that one, and every run's init describes the same tracks.
func (e *encoder) publishInitLocked() {
	dst := filepath.Join(e.dir, InitName)
	if e.ts || exists(dst) {
		return
	}
	first := SegmentName(e.start)
	if e.bounds != nil {
		first = gopName(0, false)
	}
	if _, err := os.Stat(filepath.Join(e.dir, first)); err == nil {
		_ = os.Rename(filepath.Join(e.dir, runInit), dst)
	}
}

// finish joins what the run left; a clean exit publishes the last segment,
// any other drops it as partial.
func (e *encoder) finish(clean bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.publishInitLocked()
	if e.bounds == nil {
		// A dying ffmpeg closes the segment it was writing, short, under
		// the segment's own name; a killed one closes nothing.
		if n := e.nextLocked(); !clean && !e.killed.Load() && n > e.start {
			_ = os.Remove(filepath.Join(e.dir, segmentName(n-1, e.ts)))
			e.ready = n - 1
		}
		return
	}
	e.joinLocked()
	if clean {
		_ = e.flush()
	} else if e.part != nil {
		_ = e.part.Close()
		_ = os.Remove(e.part.Name())
		e.part = nil
	}
}

func (e *encoder) joinLocked() {
	for {
		p := filepath.Join(e.dir, gopName(e.gops, e.ts))
		b, err := os.ReadFile(p) //nolint:gosec // the encoder's own output
		if err != nil {
			return
		}
		at, ok := e.gopStart(b)
		if !ok {
			return
		}
		// A container storing only presentation times, such as Matroska,
		// has its decode times guessed, which puts a keyframe file a few
		// frames either side of its bound.
		for e.seg+1 < len(e.bounds) && at >= e.bounds[e.seg+1].At-boundSlack {
			if e.flush() != nil {
				return
			}
		}
		if e.part == nil {
			if e.part, err = os.CreateTemp(e.dir, ".seg-*"); err != nil {
				return
			}
		}
		if !e.ts {
			b = fragments(b)
		}
		if _, err := e.part.Write(b); err != nil {
			return
		}
		_ = os.Remove(p)
		e.gops++
	}
}

// boundSlack is how far before its bound a keyframe file may decode and
// still start the bound's segment.
const boundSlack = 0.25

// gopStart is where a keyframe file starts on the source's timeline, in
// seconds.
func (e *encoder) gopStart(b []byte) (float64, bool) {
	if e.ts {
		pts, ok := firstPTS(b)
		if !ok {
			return 0, false
		}
		at := float64(pts) / 90000
		// A run's first keyframe file starts on its bound, which measures
		// the muxer's shift for the rest of the run.
		if e.gops == 0 {
			e.shift = at - e.bounds[e.start].At
		}
		return at - e.shift, true
	}
	if e.scale == 0 {
		init, err := os.ReadFile(filepath.Join(e.dir, InitName)) //nolint:gosec // the encoder's own output
		if err != nil {
			return 0, false
		}
		if e.scale = timescale(init); e.scale == 0 {
			return 0, false
		}
	}
	dt, ok := decodeTime(b)
	return float64(dt)/float64(e.scale) - lead, ok
}

// flush publishes the segment being joined and moves to the next.
func (e *encoder) flush() error {
	var err error
	if e.part != nil {
		name := e.part.Name()
		if err = e.part.Close(); err == nil {
			err = os.Rename(name, filepath.Join(e.dir, segmentName(e.seg, e.ts)))
		}
		e.part = nil
	}
	e.seg++
	return err
}

// keepBehind is how many segments, a few minutes of video, stay on disk
// behind the playhead; the cache cap prunes only idle sessions.
const keepBehind = 30

// headStale is how old a reported playhead may be and still pace the run:
// three intervals of the players' ten-second telemetry.
const headStale = 30 * time.Second

// request records n as the newest request, under the read-ahead in force
// now, and deletes the stream's segments more than keepBehind before the
// playhead, whichever run wrote them. Without a fresh report the playhead
// may be held segments before n; a report ahead of n, as after a seek back,
// yields to n.
func (e *encoder) request(n, ahead, held int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.newest, e.ahead, e.asked = n, ahead, time.Now()
	head := n - held
	if e.freshLocked() {
		head = min(n, e.head)
	}
	for ; e.trimmed < head-keepBehind; e.trimmed++ {
		_ = os.Remove(filepath.Join(e.dir, segmentName(e.trimmed, e.ts)))
	}
	e.ready = max(e.ready, e.trimmed)
}

func (e *encoder) next() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nextLocked()
}

// nextLocked is the first segment from start this run has not written; an
// earlier run's segments ahead do not count, or the encoder would be paused
// behind them and asked for what lies past them. Published segments stay
// until trimmed, so the search resumes where the last one stopped.
func (e *encoder) nextLocked() int {
	n := max(e.start, e.ready)
	for {
		info, err := os.Stat(filepath.Join(e.dir, segmentName(n, e.ts)))
		if err != nil || info.ModTime().Before(e.begun) {
			break
		}
		n++
	}
	e.ready = n
	return n
}

func (e *encoder) exited() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

func (m *Manager) kill(e *encoder) {
	if e.exited() {
		return
	}
	e.killed.Store(true)
	_ = e.cmd.Process.Kill()
	<-e.done
}

// Stop kills the session's encoder and deletes its segments; the slot stays
// with the session until Release.
func (m *Manager) Stop(sessionID int64) {
	defer m.lock(sessionID)()
	m.mu.Lock()
	e := m.encoders[sessionID]
	delete(m.encoders, sessionID)
	m.mu.Unlock()
	if e != nil {
		m.kill(e)
		_ = os.RemoveAll(filepath.Dir(e.dir))
	}
}

// Close stops every encoder.
func (m *Manager) Close() {
	m.mu.Lock()
	ids := make([]int64, 0, len(m.encoders))
	for id := range m.encoders {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Stop(id)
	}
}

// prune deletes the oldest directories removeIdle lets go until the cache is
// under its cap; running encoders' segments stay even over it.
func (m *Manager) prune() {
	root := m.root()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	type dir struct {
		path string
		size int64
		mod  time.Time
	}
	var dirs []dir
	var total int64
	for _, de := range entries {
		p := filepath.Join(root, de.Name())
		// Walked without m.mu: the walk is slow and every request needs it.
		size := dirSize(p)
		total += size
		if info, ierr := de.Info(); ierr == nil {
			dirs = append(dirs, dir{p, size, info.ModTime()})
		}
	}
	slices.SortFunc(dirs, func(a, b dir) int { return a.mod.Compare(b.mod) })
	for _, d := range dirs {
		if total <= m.Limits().CacheCap {
			return
		}
		if gone, _ := m.removeIdle(d.path); gone {
			total -= d.size
		}
	}
}

// sidecarTimeout bounds one extraction, which reads the whole file: long
// enough for an 80 GB remux on a volume reading 45 MB/s.
var sidecarTimeout = 30 * time.Minute

// Sidecar extracts the text subtitle track at streamIndex as WebVTT once per
// file, track and modification time. The first read takes the file's other
// text tracks among tracks with it, so asking for each in turn reads the
// file once; a track that read fails on is read alone, so a track ffmpeg
// cannot convert fails only itself. Writing sidecars removes the file's
// from before its modification time.
func (m *Manager) Sidecar(ctx context.Context, fileID int64, input string, streamIndex int, tracks []*mediafile.Track) (string, error) {
	info, err := os.Stat(input)
	if err != nil {
		return "", err
	}
	dir, stamp := filepath.Join(m.root(), "sidecars"), info.ModTime().UnixNano()
	name := func(i int) string {
		return filepath.Join(dir, fmt.Sprintf("%d-%d-%d.vtt", fileID, i, stamp))
	}
	out := name(streamIndex)
	if exists(out) {
		return out, nil
	}
	// As with Bounds, a read outlives the request that started it and is
	// shared by the requests arriving while it runs under its key. It
	// reports the stream indexes it read, which its starter chose.
	extract := func(key string, ids func() []int) ([]int, error) {
		ch := m.probes.DoChan(key, func() (any, error) {
			read := ids()
			return read, extractSidecars(ctx, input, dir, read, name, fileID, stamp)
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-ch:
			read, _ := r.Val.([]int)
			return read, r.Err
		}
	}
	read, err := extract(fmt.Sprintf("sidecars %d %d", fileID, stamp), func() []int {
		ids := []int{streamIndex}
		for _, t := range tracks {
			if t.StreamIndex != streamIndex && t.Kind == mediafile.Subtitle && t.SubtitleIsText != nil && *t.SubtitleIsText && !exists(name(t.StreamIndex)) {
				ids = append(ids, t.StreamIndex)
			}
		}
		return ids
	})
	switch {
	case exists(out):
		return out, nil
	case ctx.Err() != nil:
		return "", ctx.Err()
	case err != nil && slices.Equal(read, []int{streamIndex}):
		return "", err
	}
	// The shared read failed on some track or did not take this one.
	if _, err := extract(fmt.Sprintf("sidecar %d %d %d", fileID, streamIndex, stamp), func() []int { return []int{streamIndex} }); err != nil {
		return "", err
	}
	return out, nil
}

// extractSidecars writes the text tracks at the stream indexes ids, each to
// its name, in one read of input, all or none. Each read writes in a
// directory of its own, so reads of one track that overlap never write one
// file.
func extractSidecars(ctx context.Context, input, dir string, ids []int, name func(int) string, fileID, stamp int64) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, "tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	outs := map[int]string{}
	for _, i := range ids {
		outs[i] = filepath.Join(tmp, filepath.Base(name(i)))
	}
	xctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sidecarTimeout)
	defer cancel()
	//nolint:gosec // arguments are built from probed facts and server paths
	if b, err := exec.CommandContext(xctx, "nice", append([]string{"-n", "19", "ffmpeg"}, SidecarArgs(input, outs)...)...).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %w: %s", ErrEncoder, err, strings.TrimSpace(string(b)))
	}
	for _, i := range ids {
		if err := os.Rename(outs[i], name(i)); err != nil {
			return err
		}
	}
	// The file's sidecars named with another modification time, or with
	// none, are stale.
	old, _ := filepath.Glob(filepath.Join(dir, fmt.Sprintf("%d-*.vtt", fileID)))
	for _, p := range old {
		if !strings.HasSuffix(p, fmt.Sprintf("-%d.vtt", stamp)) {
			_ = os.Remove(p)
		}
	}
	return nil
}

// KillOrphans kills every process whose arguments name a path under root:
// the encoders of a server that died without Close, stopped ones included.
func KillOrphans(root string) error {
	pattern := "(^| )" + regexp.QuoteMeta(filepath.Clean(root)+string(filepath.Separator))
	err := exec.Command("pkill", "-KILL", "-f", pattern).Run() //nolint:gosec // the pattern is a quoted server path
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil // nothing matched
	}
	return err
}

// tail is the last n bytes of s, trimmed: where ffmpeg says what went wrong.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}
