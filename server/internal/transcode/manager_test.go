package transcode

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// longClip synthesizes two minutes of small H.264 so there are enough segments
// to see the encoder pause ahead of the playhead.
func longClip(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	p := filepath.Join(t.TempDir(), "long.mp4")
	out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24", "-f", "lavfi", "-i", "sine", //nolint:gosec // test inputs are fixtures and temp paths
		"-t", "120", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", p).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	return p
}

func convertJob(input string) Job {
	return Job{Input: input, Entry: &session.PlanEntry{VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", Encoder: session.EncoderSoftware},
		Video: &mediafile.Track{StreamIndex: 0, Kind: mediafile.Video, Codec: "h264", Width: ptr(320), Height: ptr(180)},
		Audio: &mediafile.Track{StreamIndex: 1, Kind: mediafile.Audio, Codec: "aac"},
		Rung:  playback.Rung{Width: 320, Height: 180, Bitrate: 300e3}}
}

func produced(dir string) int {
	n := -1
	for i := 0; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, SegmentName(i))); err != nil {
			return n
		}
		n = i
	}
}

func TestLazySegments(t *testing.T) {
	clip := longClip(t)
	m := New(Config{Root: t.TempDir(), Ahead: 2, Reach: 2})
	defer m.Close()
	ctx := context.Background()
	st := Stream{SessionID: uid.N(1), Key: "0-0", Job: convertJob(clip)}
	init, err := m.Init(ctx, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(init); err != nil {
		t.Fatal(err)
	}
	p, err := m.Segment(ctx, st, 0)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(p)
	time.Sleep(3 * time.Second)
	// Pacing polls, so a fast encoder overshoots a little; it must still
	// stop far short of the 20 segments in the clip.
	if n := produced(dir); n > 8 {
		t.Fatalf("encoder ran %d segments past a playhead at 0 with ahead 2", n)
	}
	if _, err := m.Segment(ctx, st, 3); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	if n := produced(dir); n < 4 || n > 12 {
		t.Fatalf("after a request for 3, produced up to %d", n)
	}
	// A seek far ahead restarts the encoder there instead of encoding the gap.
	if _, err := m.Segment(ctx, st, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, SegmentName(16))); err == nil {
		names, _ := os.ReadDir(dir)
		t.Fatalf("the gap before a seek must not be encoded; produced %d: %v", produced(dir), names)
	}
	m.Stop(uid.N(1))
	if _, err := os.Stat(filepath.Join(m.cfg.Root, uid.N(1).String())); !os.IsNotExist(err) {
		t.Fatal("stop removes the session's segments")
	}
}

// tfdt reads a segment's first decode time in seconds, on the timescale of
// the first track in its init segment.
func tfdt(t *testing.T, initPath, segPath string) float64 {
	t.Helper()
	init, err := os.ReadFile(initPath) //nolint:gosec // test temp path
	must(t, err)
	seg, err := os.ReadFile(segPath) //nolint:gosec // test temp path
	must(t, err)
	m := bytes.Index(init, []byte("mdhd"))
	f := bytes.Index(seg, []byte("tfdt"))
	if m < 0 || f < 0 {
		t.Fatal("no mdhd or tfdt")
	}
	off := 4 + 4 + 8 // version and flags, then creation and modification times
	if init[m+4] == 1 {
		off = 4 + 4 + 16
	}
	scale := binary.BigEndian.Uint32(init[m+off:])
	var dt uint64
	if seg[f+4] == 1 {
		dt = binary.BigEndian.Uint64(seg[f+8:])
	} else {
		dt = uint64(binary.BigEndian.Uint32(seg[f+8:]))
	}
	return float64(dt) / float64(scale)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestOneEncoderPerSession(t *testing.T) {
	clip := longClip(t)
	m := New(Config{Root: t.TempDir()})
	defer m.Close()
	ctx := context.Background()
	a := Stream{SessionID: uid.N(1), Key: "0-0", Job: convertJob(clip)}
	b := a
	b.Key = "0-1"
	b.Job.Rung = playback.Rung{Width: 160, Height: 90, Bitrate: 100e3}
	if _, err := m.Segment(ctx, a, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Segment(ctx, b, 0); err != nil {
		t.Fatal(err)
	}
	if n := m.Running(); n != 1 {
		t.Fatalf("running encoders = %d", n)
	}
}

// Running counts live encoder processes.
func (m *Manager) Running() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.encoders {
		if !e.exited() {
			n++
		}
	}
	return n
}

// ffmpegs counts the encoder processes writing under root.
func ffmpegs(t *testing.T, root string) int {
	t.Helper()
	out, _ := exec.Command("pgrep", "-f", root).Output() //nolint:gosec // a test temp dir
	return len(strings.Fields(string(out)))
}

// Requests racing for one session, as a player's init and first segment do
// or a rung switch with the old rung still in flight, start one encoder at a
// time and never one the Manager has lost track of.
func TestRacingRequestsShareOneEncoder(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil || testing.Short() {
		t.Skip("pgrep not installed")
	}
	clip := longClip(t)
	root := t.TempDir()
	m := New(Config{Root: root})
	defer m.Close()
	a := Stream{SessionID: uid.N(1), Key: "0-0", Job: convertJob(clip)}
	b := a
	b.Key = "0-1"
	b.Job.Rung = playback.Rung{Width: 160, Height: 90, Bitrate: 100e3}
	var wg sync.WaitGroup
	for range 4 {
		for _, st := range []Stream{a, b} {
			wg.Go(func() { _, _ = m.Segment(context.Background(), st, 0) })
		}
	}
	wg.Wait()
	if n := ffmpegs(t, root); n > 1 || n != m.Running() {
		t.Fatalf("%d encoders running, %d tracked", n, m.Running())
	}
	m.Stop(uid.N(1))
	if n := ffmpegs(t, root); n != 0 {
		t.Fatalf("%d encoders outlived Stop", n)
	}
}

func TestEncoderFailureIsReported(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	m := New(Config{Root: t.TempDir()})
	defer m.Close()
	_, err := m.Segment(context.Background(), Stream{SessionID: uid.N(1), Key: "0-0", Job: convertJob("/nonexistent.mkv")}, 0)
	if !errors.Is(err, ErrEncoder) {
		t.Fatalf("got %v", err)
	}
}

func TestTranscodeLimit(t *testing.T) {
	m := New(Config{Root: t.TempDir(), MaxVideo: 1})
	if err := m.Acquire(uid.N(1)); err != nil {
		t.Fatal(err)
	}
	if err := m.Acquire(uid.N(1)); err != nil {
		t.Fatal("acquire is idempotent per session")
	}
	if err := m.Acquire(uid.N(2)); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
	m.Release(uid.N(1))
	if err := m.Acquire(uid.N(2)); err != nil {
		t.Fatal(err)
	}
	if New(Config{}).cfg.MaxVideo != 3 {
		t.Fatal("default limit is 3 simultaneous transcodes")
	}
}

func TestCacheCapPrunesIdleSessions(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{uid.N(7).String(), uid.N(8).String()} {
		_ = os.MkdirAll(filepath.Join(root, d, "0-0"), 0o750)
		_ = os.WriteFile(filepath.Join(root, d, "0-0", "0.m4s"), make([]byte, 1000), 0o600)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(root, uid.N(7).String()), old, old)
	m := New(Config{Root: root, CacheCap: 1500})
	m.prune()
	if _, err := os.Stat(filepath.Join(root, uid.N(7).String())); !os.IsNotExist(err) {
		t.Fatal("the oldest idle session goes first")
	}
	if _, err := os.Stat(filepath.Join(root, uid.N(8).String())); err != nil {
		t.Fatal("pruning stops once under the cap")
	}
}

// ranOutBesideRunning is a Manager whose session 2 encoder runs, paused
// ahead of its one request, and whose session 1 encoder, started after, ran
// to the end of its file and exited.
func ranOutBesideRunning(t *testing.T) *Manager {
	t.Helper()
	st := Stream{Key: "0-0", Job: convertJob(longClip(t))}
	m := New(Config{Root: t.TempDir(), Ahead: 1})
	t.Cleanup(m.Close)
	running, done := st, st
	running.SessionID, done.SessionID, done.Ahead = uid.N(2), uid.N(1), 1000
	for _, s := range []Stream{running, done} {
		_, err := m.Segment(context.Background(), s, 0)
		must(t, err)
	}
	e := m.encoders[uid.N(2)]
	paused := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.paused
	}
	for end := time.Now().Add(30 * time.Second); !paused() || m.Running() > 1; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("running %d, session 2 paused %v", m.Running(), paused())
		}
	}
	return m
}

// The cap takes the segments of a session whose encoder ran to the end of
// its file, oldest first, so before a newer sidecar; a running encoder's
// stay. Pruning runs under the lock of the session starting an encoder, so
// a session busy with a request is passed over, not waited for.
func TestCacheCapPrunesSessionsWhoseEncoderExited(t *testing.T) {
	srt := fixture(t, "sample.srt")
	m := ranOutBesideRunning(t)
	done, running := filepath.Join(m.root(), uid.N(1).String()), filepath.Join(m.root(), uid.N(2).String())
	unlock := m.lock(uid.N(1))
	pruned := make(chan struct{})
	go func() { m.SetLimits(Limits{CacheCap: 1, MaxVideo: 3}); close(pruned) }()
	select {
	case <-pruned:
		unlock()
	case <-time.After(10 * time.Second):
		unlock()
		t.Fatal("pruning waited for a session busy with a request")
	}
	if !exists(done) {
		t.Fatal("pruning took the segments of a session busy with a request")
	}
	sub, err := m.Sidecar(context.Background(), uid.N(5), srt, 0, nil)
	must(t, err)
	m.SetLimits(Limits{CacheCap: m.Usage() - dirSize(done), MaxVideo: 3})
	if exists(done) {
		t.Fatal("the segments of a session whose encoder ran out outlived the cap")
	}
	if !exists(running) {
		t.Fatal("pruning took a running encoder's segments")
	}
	if !exists(sub) {
		t.Fatal("pruning took a sidecar newer than the session that sufficed")
	}
}

// A session whose segments the cap took encodes them again: its next
// request, for its init segment or a segment, restarts its encoder, which
// takes a slot again as after an idle stop.
func TestPrunedSessionEncodesAgain(t *testing.T) {
	st := Stream{Key: "0-0", Job: convertJob(longClip(t))}
	m := New(Config{Root: t.TempDir(), MaxVideo: 2, Ahead: 1, Idle: 300 * time.Millisecond})
	defer m.Close()
	ctx := context.Background()
	idle, done := st, st
	idle.SessionID, done.SessionID, done.Ahead = uid.N(1), uid.N(2), 1000
	for _, s := range []Stream{idle, done} {
		must(t, m.Acquire(s.SessionID))
		_, err := m.Segment(ctx, s, 0)
		must(t, err)
	}
	// One encoder stops paused past Idle, the other at the end of its file.
	for end := time.Now().Add(30 * time.Second); m.Running() > 0 || len(m.Transcoding()) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("running %d, slots %v", m.Running(), m.Transcoding())
		}
	}
	m.SetLimits(Limits{CacheCap: 1, MaxVideo: 2})
	for _, s := range []Stream{idle, done} {
		if exists(m.dir(s)) {
			t.Fatalf("session %s kept its segments over the cap", s.SessionID)
		}
	}
	must(t, m.Acquire(uid.N(3)))
	must(t, m.Acquire(uid.N(4)))
	if _, err := m.Init(ctx, idle, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("init with no slot free: %v", err)
	}
	m.Release(uid.N(3))
	if p, err := m.Init(ctx, idle, 0); err != nil || !exists(p) {
		t.Fatalf("init: %v", err)
	}
	m.Release(uid.N(4))
	if p, err := m.Segment(ctx, done, 3); err != nil || !exists(p) {
		t.Fatalf("segment: %v", err)
	}
}

// Clearing the cache takes the segments of a session whose encoder ran out
// and keeps a running encoder's.
func TestClearTakesSessionsWhoseEncoderExited(t *testing.T) {
	m := ranOutBesideRunning(t)
	must(t, m.Clear(nil))
	if exists(filepath.Join(m.root(), uid.N(1).String())) {
		t.Fatal("the segments of a session whose encoder ran out survived a clear")
	}
	if !exists(filepath.Join(m.root(), uid.N(2).String())) {
		t.Fatal("clearing took a running encoder's segments")
	}
}

// tinyStream converts a clip a few dozen segments long, small enough to
// encode in moments.
func tinyStream(t *testing.T) Stream {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	clip := filepath.Join(t.TempDir(), "tiny.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=12", "-f", "lavfi", "-i", "sine", //nolint:gosec // temp path
		"-t", strconv.Itoa((keepBehind+12)*SegmentSeconds), "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", clip).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	j := convertJob(clip)
	j.Rung = playback.Rung{Width: 160, Height: 90, Bitrate: 100e3}
	return Stream{SessionID: uid.N(1), Key: "0-0", Job: j}
}

// A playing session holds only the segments near its playhead, so a long
// remux never fills the cache; a seek back to a deleted one encodes it again.
func TestPlayingSessionKeepsRecentSegments(t *testing.T) {
	st := tinyStream(t)
	m := New(Config{Root: t.TempDir(), Ahead: 2})
	defer m.Close()
	ctx := context.Background()
	var dir string
	for n := 0; n < keepBehind+10; n++ {
		p, err := m.Segment(ctx, st, n)
		must(t, err)
		dir = filepath.Dir(p)
	}
	segs, _ := filepath.Glob(filepath.Join(dir, "*.m4s"))
	if len(segs) > keepBehind+m.cfg.Ahead+5 {
		t.Fatalf("%d segments on disk", len(segs))
	}
	if exists(filepath.Join(dir, SegmentName(0))) {
		t.Fatal("segment 0 is still on disk")
	}
	if _, err := m.Segment(ctx, st, 0); err != nil {
		t.Fatalf("seeking back to segment 0: %v", err)
	}
}

// A player told to hold its stream's read-ahead has fetched that far past
// its playhead, so that much more stays behind the newest request: a seek
// back from the playhead still finds its segment on disk.
func TestSegmentsStayBehindThePlayheadOfAPlayerHoldingItsReadAhead(t *testing.T) {
	st := tinyStream(t)
	st.Ahead = 8
	m := New(Config{Root: t.TempDir()})
	defer m.Close()
	ctx := context.Background()
	var dir string
	for n := 0; n < keepBehind+10; n++ {
		p, err := m.Segment(ctx, st, n)
		must(t, err)
		dir = filepath.Dir(p)
	}
	// The newest request is 39, so the playhead may be at 31.
	if !exists(filepath.Join(dir, SegmentName(1))) {
		t.Fatal("segment 1, 30 behind a playhead at 31, is gone")
	}
	if exists(filepath.Join(dir, SegmentName(0))) {
		t.Fatal("segment 0 is still on disk")
	}
}

// After a seek back, the segments an earlier run left ahead are served as
// they are; a request past them restarts the encoder there instead of
// waiting for it to encode its way through them.
func TestSeekBackSkipsAnEarlierRunsSegments(t *testing.T) {
	st := tinyStream(t)
	m := New(Config{Root: t.TempDir(), Ahead: 2})
	defer m.Close()
	ctx := context.Background()
	dir := m.dir(st)
	must(t, os.MkdirAll(dir, 0o750))
	old := time.Now().Add(-time.Hour)
	for n := 10; n < 30; n++ {
		p := filepath.Join(dir, SegmentName(n))
		must(t, os.WriteFile(p, nil, 0o600))
		must(t, os.Chtimes(p, old, old))
	}
	for n := 2; n < 10; n++ {
		_, err := m.Segment(ctx, st, n)
		must(t, err)
	}
	if _, err := m.Segment(ctx, st, 30); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(dir, SegmentName(20))); err != nil || info.Size() != 0 {
		t.Fatalf("the earlier run's segment 20 was encoded again on the way to 30: %v", err)
	}
}

// A player paused past Idle gives up its encoder and slot, keeping its
// segments; it wins a slot back to encode again, or hears ErrBusy.
func TestIdleEncoderYieldsItsSlot(t *testing.T) {
	st := tinyStream(t)
	m := New(Config{Root: t.TempDir(), MaxVideo: 1, Ahead: 1, Idle: 300 * time.Millisecond})
	defer m.Close()
	ctx := context.Background()
	must(t, m.Acquire(st.SessionID))
	first, err := m.Segment(ctx, st, 0)
	must(t, err)
	for end := time.Now().Add(10 * time.Second); m.Running() > 0 || len(m.Transcoding()) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("running %d, slots %v", m.Running(), m.Transcoding())
		}
	}
	if !exists(first) {
		t.Fatal("the idle session's segments are gone")
	}
	// A fast host encodes the whole clip before the pause; a segment that
	// is on disk is served with no encoder.
	_ = os.Remove(filepath.Join(filepath.Dir(first), SegmentName(10)))
	must(t, m.Acquire(uid.N(2)))
	if _, err := m.Segment(ctx, st, 10); !errors.Is(err, ErrBusy) {
		t.Fatalf("resuming with no slot free: %v", err)
	}
	m.Release(uid.N(2))
	if _, err := m.Segment(ctx, st, 10); err != nil || !slices.Equal(m.Transcoding(), []uid.ID{st.SessionID}) {
		t.Fatalf("resuming: %v, slots %v", err, m.Transcoding())
	}
}

// A session about to encode or encoding is busy; one whose encoder waits
// paused ahead of its player, holding its slot, is not.
func TestBusyIsAnEncoderAtWork(t *testing.T) {
	st := tinyStream(t)
	m := New(Config{Root: t.TempDir(), Ahead: 1})
	defer m.Close()
	if m.Busy() {
		t.Fatal("no session holds a slot")
	}
	must(t, m.Acquire(st.SessionID))
	if !m.Busy() {
		t.Fatal("a slot with no encoder yet is about to encode")
	}
	_, err := m.Segment(context.Background(), st, 0)
	must(t, err)
	for end := time.Now().Add(10 * time.Second); m.Busy(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("paused ahead of its player, still busy")
		}
	}
	if m.Running() != 1 || !slices.Equal(m.Transcoding(), []uid.ID{st.SessionID}) {
		t.Fatalf("running %d, slots %v", m.Running(), m.Transcoding())
	}
	// A stand-in running encoder, gone again before Close could stop it.
	must(t, m.Acquire(uid.N(2)))
	m.mu.Lock()
	m.encoders[uid.N(2)] = &encoder{done: make(chan struct{})}
	m.mu.Unlock()
	busy := m.Busy()
	m.mu.Lock()
	delete(m.encoders, uid.N(2))
	m.mu.Unlock()
	if !busy {
		t.Fatal("an encoder running unpaused is busy")
	}
}

// An encoder that ran to the end of its file needs its slot no longer;
// another session takes it while the first one's segments play out.
func TestAnEncoderThatRanOutFreesItsSlot(t *testing.T) {
	st := tinyStream(t)
	m := New(Config{Root: t.TempDir(), Ahead: 1000})
	defer m.Close()
	ctx := context.Background()
	must(t, m.Acquire(uid.N(1)))
	if m.Rest(uid.N(1)) {
		t.Fatal("a session that has not encoded keeps its slot")
	}
	var first string
	for n := 1; n <= 3; n++ {
		id := uid.N(n)
		st.SessionID = id
		must(t, m.Acquire(id))
		var err error
		first, err = m.Segment(ctx, st, 0)
		must(t, err)
	}
	for end := time.Now().Add(30 * time.Second); len(m.Transcoding()) > 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("running %d, slots %v", m.Running(), m.Transcoding())
		}
	}
	must(t, m.Acquire(uid.N(4)))
	if !exists(first) {
		t.Fatal("the finished session's segments are gone")
	}
	_ = os.Remove(filepath.Join(filepath.Dir(first), SegmentName(10)))
	must(t, m.Acquire(uid.N(5)))
	must(t, m.Acquire(uid.N(6)))
	if _, err := m.Segment(ctx, st, 10); !errors.Is(err, ErrBusy) {
		t.Fatalf("encoding again with no slot free: %v", err)
	}
}

// A session from before a restart holds no slot; its next encoder takes one
// as a new session's does, so the limit holds and conversions see it. A run
// copying the video frees the session's.
func TestAnEncoderConvertingVideoHoldsASlot(t *testing.T) {
	st := tinyStream(t)
	m := New(Config{Root: t.TempDir(), MaxVideo: 1})
	defer m.Close()
	ctx := context.Background()
	must(t, m.Acquire(uid.N(2)))
	if _, err := m.Segment(ctx, st, 0); !errors.Is(err, ErrBusy) {
		t.Fatalf("a transcode past the limit: %v", err)
	}
	m.Release(uid.N(2))
	if _, err := m.Segment(ctx, st, 0); err != nil || !slices.Equal(m.Transcoding(), []uid.ID{st.SessionID}) {
		t.Fatalf("%v, slots %v", err, m.Transcoding())
	}
	cp := st
	cp.Key, cp.Job.Entry = "1-0", &session.PlanEntry{VideoAction: session.ActionCopy, AudioAction: session.ActionCopy}
	if _, err := m.Segment(ctx, cp, 0); err != nil || len(m.Transcoding()) != 0 {
		t.Fatalf("%v, slots %v", err, m.Transcoding())
	}
}

// A server that died without Close left its encoders, most of them stopped;
// the next one kills them, sparing another instance's.
func TestKillOrphansSparesOtherCaches(t *testing.T) {
	base := t.TempDir()
	stopped := func(root string) *exec.Cmd {
		out := filepath.Join(root, uid.N(1).String(), "0-0", "encoder.m3u8")
		must(t, os.MkdirAll(filepath.Dir(out), 0o750))
		must(t, os.WriteFile(out, nil, 0o600))
		cmd := exec.Command("tail", "-f", out) //nolint:gosec // temp path
		must(t, cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		must(t, cmd.Process.Signal(syscall.SIGSTOP))
		return cmd
	}
	ours, theirs := stopped(filepath.Join(base, "a")), stopped(filepath.Join(base, "a-demo"))
	must(t, KillOrphans(filepath.Join(base, "a")))
	gone := make(chan struct{})
	go func() { _ = ours.Wait(); close(gone) }()
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the stopped orphan survived")
	}
	if theirs.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("killed another cache's encoder")
	}
	must(t, KillOrphans(filepath.Join(base, "none")))
}

func TestSidecarCache(t *testing.T) {
	file := fixture(t, "hdr10-main10.mkv")
	m := New(Config{Root: t.TempDir()})
	p, err := m.Sidecar(context.Background(), uid.N(5), file, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); len(b) < 6 || string(b[:6]) != "WEBVTT" { //nolint:gosec // test inputs are fixtures and temp paths
		t.Fatalf("%q", b)
	}
}

// A player that gives up does not take the extraction with it, and players
// that ask while it runs share it.
func TestSidecarIsSharedAndOutlivesItsCaller(t *testing.T) {
	file := fixture(t, "sample.srt")
	count := slowTool(t, "ffmpeg", "0.5")
	m := New(Config{Root: t.TempDir()})
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	gaveUp := make(chan error, 1)
	go func() { _, err := m.Sidecar(short, uid.N(5), file, 0, nil); gaveUp <- err }()
	time.Sleep(20 * time.Millisecond)
	if _, err := m.Sidecar(context.Background(), uid.N(5), file, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := <-gaveUp; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the caller that gave up: %v", err)
	}
	if n := runs(t, count); n != 1 {
		t.Fatalf("%d extractions", n)
	}
}

// Every text track of a file comes out of one read of it: a player or a
// download asking for each in turn, or all at once, reads the file once.
func TestSidecarExtractsEveryTextTrackInOneRead(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	texts := []string{"First.", "Second."}
	args := []string{"-v", "error"}
	for i, text := range texts {
		p := filepath.Join(dir, strconv.Itoa(i)+".srt")
		must(t, os.WriteFile(p, []byte("1\n00:00:00,500 --> 00:00:02,000\n"+text+"\n"), 0o600))
		args = append(args, "-i", p)
	}
	file := filepath.Join(dir, "two.mkv")
	if out, err := exec.Command("ffmpeg", append(args, "-map", "0", "-map", "1", "-c", "copy", file)...).CombinedOutput(); err != nil { //nolint:gosec // temp paths
		t.Fatalf("%v %s", err, out)
	}
	yes := true
	subs := []*mediafile.Track{{StreamIndex: 0, Kind: mediafile.Subtitle, SubtitleIsText: &yes}, {StreamIndex: 1, Kind: mediafile.Subtitle, SubtitleIsText: &yes}}
	count := slowTool(t, "ffmpeg", "0.3")
	m := New(Config{Root: t.TempDir()})
	paths := make([]string, len(texts))
	var wg sync.WaitGroup
	for i := range texts {
		wg.Go(func() {
			p, err := m.Sidecar(context.Background(), uid.N(5), file, i, subs)
			if err != nil {
				t.Error(err)
			}
			paths[i] = p
		})
	}
	wg.Wait()
	for i, text := range texts {
		if b, _ := os.ReadFile(paths[i]); !strings.Contains(string(b), text) { //nolint:gosec // test temp path
			t.Errorf("track %d: %q, want %q", i, b, text)
		}
	}
	if n := runs(t, count); n != 1 {
		t.Fatalf("%d reads of the file", n)
	}
}

// A track ffmpeg cannot write as WebVTT, as a bitmap track marked text is,
// fails only itself: each track asked for while it took the shared read down
// is read alone, and what is written is kept.
func TestSidecarTrackThatFailsFailsOnlyItself(t *testing.T) {
	file := fixture(t, "hdr10-main10.mkv")
	count := slowTool(t, "ffmpeg", "0.3")
	yes := true
	const good, bad = 4, 3
	tracks := []*mediafile.Track{{StreamIndex: bad, Kind: mediafile.Subtitle, SubtitleIsText: &yes}, {StreamIndex: good, Kind: mediafile.Subtitle, SubtitleIsText: &yes}}
	root := t.TempDir()
	m := New(Config{Root: root})
	var wg sync.WaitGroup
	var goodPath string
	var goodErr, badErr error
	wg.Go(func() { goodPath, goodErr = m.Sidecar(context.Background(), uid.N(5), file, good, tracks) })
	wg.Go(func() { _, badErr = m.Sidecar(context.Background(), uid.N(5), file, bad, tracks) })
	wg.Wait()
	if goodErr != nil {
		t.Fatalf("the good track: %v", goodErr)
	}
	if b, _ := os.ReadFile(goodPath); !strings.HasPrefix(filepath.Base(goodPath), uid.N(5).String()+"-4-") || !strings.HasPrefix(string(b), "WEBVTT") || !strings.Contains(string(b), "-->") { //nolint:gosec // test temp path
		t.Fatalf("the good track: %s %q", goodPath, b)
	}
	if !errors.Is(badErr, ErrEncoder) {
		t.Fatalf("the bad track: %v", badErr)
	}
	n := runs(t, count)
	if p, err := m.Sidecar(context.Background(), uid.N(5), file, good, tracks); err != nil || p != goodPath || runs(t, count) != n {
		t.Fatalf("asked again: %q %v, %d reads after %d", p, err, runs(t, count), n)
	}
	if left, _ := filepath.Glob(filepath.Join(root, "sidecars", "*tmp*")); len(left) > 0 {
		t.Fatalf("left behind: %v", left)
	}
}

// A file replaced at the same path keeps its id, so its text comes from the
// new file, not the cache.
func TestSidecarFollowsAReplacedFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	file := filepath.Join(t.TempDir(), "subs.srt")
	m := New(Config{Root: t.TempDir()})
	for i, text := range []string{"Before.", "After."} {
		must(t, os.WriteFile(file, []byte("1\n00:00:00,500 --> 00:00:02,000\n"+text+"\n"), 0o600))
		at := time.Now().Add(time.Duration(i) * time.Hour)
		must(t, os.Chtimes(file, at, at))
		p, err := m.Sidecar(context.Background(), uid.N(5), file, 0, nil)
		must(t, err)
		if b, _ := os.ReadFile(p); !strings.Contains(string(b), text) { //nolint:gosec // test temp path
			t.Fatalf("got %q, want %q", b, text)
		}
	}
}

// An encoder that dies closes the segment it was writing, short of its
// length, under the segment's own name. The run drops it, so the next
// request encodes it whole.
func TestFailedEncoderLeavesNoPartialSegment(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a; do out=$a; done\ncd \"$(dirname \"$out\")\" && : > run.mp4 && : > 0.m4s && : > 1.m4s && : > 2.m4s\nexit 1\n"
	must(t, os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o700)) //nolint:gosec // a test script
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := New(Config{Root: t.TempDir()})
	defer m.Close()
	e, err := m.start(Stream{SessionID: uid.N(1), Key: "0-0", Job: convertJob("/any.mkv")}, 0)
	must(t, err)
	<-e.done
	if n := produced(e.dir); n != 1 {
		t.Fatalf("segments up to %d are on disk, want up to 1", n)
	}
}

// An init request waiting on an encoder that exits without the init on
// disk, as when the cache cap takes an exited encoder's directory, starts
// the encoder once more, as a segment request does.
func TestInitOutlastsAnEncoderThatExitedWithoutIt(t *testing.T) {
	bin := t.TempDir()
	count := filepath.Join(bin, "count")
	script := "#!/bin/sh\nfor a; do out=$a; done\nn=$(cat '" + count + "' 2>/dev/null || echo 0)\necho $((n+1)) > '" + count + "'\n" +
		"[ \"$n\" = 0 ] && exit 0\ncd \"$(dirname \"$out\")\" && : > run.mp4 && : > 0.m4s\n"
	must(t, os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte(script), 0o700)) //nolint:gosec // a test script
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m := New(Config{Root: t.TempDir()})
	defer m.Close()
	if p, err := m.Init(context.Background(), Stream{SessionID: uid.N(1), Key: "0-0", Job: convertJob("/any.mkv")}, 0); err != nil || !exists(p) {
		t.Fatalf("%q %v", p, err)
	}
}

// A hung extraction fails, so the next request for the track runs its own.
func TestSidecarExtractionHasATimeLimit(t *testing.T) {
	file := fixture(t, "sample.srt")
	bin := t.TempDir()
	must(t, os.WriteFile(filepath.Join(bin, "ffmpeg"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o700)) //nolint:gosec // a test script
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(d time.Duration) { sidecarTimeout = d }(sidecarTimeout)
	sidecarTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := New(Config{Root: t.TempDir()}).Sidecar(ctx, uid.N(5), file, 0, nil); !errors.Is(err, ErrEncoder) {
		t.Fatalf("got %v", err)
	}
}

// Writing a file's sidecar removes its sidecars from before the file last
// changed, under the name without a modification time too.
func TestSidecarRemovesItsFilesStaleOnes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	file := fixture(t, "sample.srt")
	info, err := os.Stat(file)
	must(t, err)
	root := t.TempDir()
	dir := filepath.Join(root, "sidecars")
	must(t, os.MkdirAll(dir, 0o750))
	five, other := uid.N(5).String(), uid.N(6).String()
	current := fmt.Sprintf("%s-3-%d.vtt", five, info.ModTime().UnixNano())
	stale, kept := []string{five + "-0.vtt", five + "-0-1.vtt", five + "-3-1.vtt"}, []string{other + "-0.vtt", other + "-0-1.vtt", current}
	for _, n := range append(stale, kept...) {
		must(t, os.WriteFile(filepath.Join(dir, n), []byte("WEBVTT\n"), 0o600))
	}
	p, err := New(Config{Root: root}).Sidecar(context.Background(), uid.N(5), file, 0, nil)
	must(t, err)
	for _, n := range stale {
		if exists(filepath.Join(dir, n)) {
			t.Errorf("%s is still there", n)
		}
	}
	for _, n := range append(kept, filepath.Base(p)) {
		if !exists(filepath.Join(dir, n)) {
			t.Errorf("%s is gone", n)
		}
	}
}

func TestClearKeepsActiveSessions(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{uid.N(7).String() + "/0-0/0.m4s", uid.N(8).String() + "/0-0/0.m4s", "sidecars/" + uid.N(3).String() + "-2.vtt"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o750)
		_ = os.WriteFile(filepath.Join(root, p), make([]byte, 1000), 0o600)
	}
	m := New(Config{Root: root})
	if got := m.Usage(); got != 3000 {
		t.Fatalf("usage %d", got)
	}
	if err := m.Clear(map[uid.ID]bool{uid.N(8): true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, uid.N(8).String(), "0-0", "0.m4s")); err != nil {
		t.Fatal("an active session keeps its segments")
	}
	if got := m.Usage(); got != 1000 {
		t.Fatalf("usage after clear %d", got)
	}
	if New(Config{}).Limits() != (Limits{CacheCap: 20_000_000_000, MaxVideo: 3}) {
		t.Fatal("defaults")
	}
}

// loopedFixture is the H.264 fixture three times over by stream copy: 30 s
// with keyframes only at 0, 10 and 20 s.
func loopedFixture(t *testing.T) string {
	t.Helper()
	src := fixture(t, "h264-high41.mp4")
	p := filepath.Join(t.TempDir(), "looped.mp4")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-stream_loop", "2", "-i", src, "-c", "copy", p).CombinedOutput(); err != nil { //nolint:gosec // test inputs are fixtures and temp paths
		t.Fatalf("%v %s", err, out)
	}
	return p
}

// firstVideo decodes a served segment, prefixed by its init segment when
// there is one, and returns the first video frame's time and height.
func firstVideo(t *testing.T, init, seg string) (float64, int) {
	t.Helper()
	p := seg
	if init != "" {
		a, err := os.ReadFile(init) //nolint:gosec // test temp path
		must(t, err)
		b, err := os.ReadFile(seg) //nolint:gosec // test temp path
		must(t, err)
		p = filepath.Join(t.TempDir(), "joined.mp4")
		must(t, os.WriteFile(p, append(a, b...), 0o600)) //nolint:gosec // test temp path
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "frame=pts_time,height", "-of", "csv=p=0", p).Output() //nolint:gosec // test temp path
	if err != nil {
		t.Fatalf("%s does not decode: %v", seg, err)
	}
	var at float64
	var h int
	lines := strings.Fields(string(out))
	if len(lines) == 0 {
		t.Fatalf("%s has no video frames", seg)
	}
	if _, err := fmt.Sscanf(lines[0], "%f,%d", &at, &h); err != nil {
		t.Fatalf("%q: %v", lines[0], err)
	}
	return at, h
}

// A player's ABR moves between rungs mid-stream: segments of two rungs of
// one session, fetched alternately, each decode and sit on one timeline.
func TestRungSwitchOneTimeline(t *testing.T) {
	// Evenly timed frames: where a source's are not, a run rounds them onto
	// its frame grid from its own first frame, and two runs can differ by one.
	clip := longClip(t)
	for _, ts := range []bool{false, true} {
		t.Run(fmt.Sprintf("ts=%v", ts), func(t *testing.T) {
			m := New(Config{Root: t.TempDir()})
			defer m.Close()
			ctx := context.Background()
			rungs := []playback.Rung{{Width: 640, Height: 360, Bitrate: 800e3}, {Width: 426, Height: 240, Bitrate: 400e3}}
			var streams []Stream
			for i, r := range rungs {
				j := convertJob(clip)
				j.Entry.TS, j.Rung = ts, r
				j.Video.Width, j.Video.Height = ptr(640), ptr(360)
				streams = append(streams, Stream{SessionID: uid.N(1), Key: fmt.Sprintf("0-%d", i), Job: j})
			}
			playlist := Playlist(Durations(nil, 120_000), ts)
			var zero float64
			for n := 0; n < 5; n++ {
				st := streams[n%2]
				init := ""
				if !ts {
					var err error
					init, err = m.Init(ctx, st, n)
					must(t, err)
				}
				p, err := m.Segment(ctx, st, n)
				must(t, err)
				if filepath.Base(p) != segmentName(n, ts) || !strings.Contains(playlist, segmentName(n, ts)) {
					t.Fatalf("served %s for segment %d", p, n)
				}
				at, h := firstVideo(t, init, p)
				if n == 0 {
					zero = at
				}
				// The playlist lists SegmentSeconds segments from zero; MPEG-TS
				// shifts the whole timeline by the muxer's delay.
				if math.Abs(at-zero-float64(n*SegmentSeconds)) > 0.002 || h != st.Job.Rung.Height {
					t.Errorf("segment %d of rung %d starts at %.3f at %dp, want %d s at %dp", n, n%2, at-zero, h, n*SegmentSeconds, st.Job.Rung.Height)
				}
			}
		})
	}
}

// Copied video joined from keyframe files cuts MPEG-TS segments on the same
// bounds whichever segment the encoder started at.
func TestCopiedTSSegmentsMatchAcrossRuns(t *testing.T) {
	clip := loopedFixture(t)
	m := New(Config{Root: t.TempDir(), Ahead: 50})
	defer m.Close()
	ctx := context.Background()
	b, err := m.Bounds(ctx, fileOf(clip, 30_042))
	must(t, err)
	if len(b) != 3 {
		t.Fatalf("bounds %+v", b)
	}
	job := Job{Input: clip, Entry: &session.PlanEntry{VideoAction: "copy", AudioAction: "copy", TS: true}, Bounds: b,
		Video: &mediafile.Track{StreamIndex: 0, Kind: mediafile.Video, Codec: "h264"}, Audio: &mediafile.Track{StreamIndex: 1, Kind: mediafile.Audio, Codec: "aac"}}
	starts := map[int]float64{}
	for run, from := range []int{0, 2, 1} {
		st := Stream{SessionID: uid.N(run + 1), Key: "0-0", Job: job}
		for n := from; n < len(b); n++ {
			p, err := m.Segment(ctx, st, n)
			must(t, err)
			at, h := firstVideo(t, "", p)
			if h != 360 {
				t.Errorf("segment %d is %dp", n, h)
			}
			if want, ok := starts[n]; ok && math.Abs(at-want) > 0.002 {
				t.Errorf("run from %d: segment %d starts at %.3f, an earlier run's at %.3f", from, n, at, want)
			}
			starts[n] = at
		}
	}
	for n := 1; n < len(b); n++ {
		if got, want := starts[n]-starts[0], b[n].At-b[0].At; math.Abs(got-want) > 0.002 {
			t.Errorf("segment %d starts %.3f s in, its bound is %.3f s in", n, got, want)
		}
	}
}

// Replacing a session's encoder for another rung fails nothing: a request
// still waiting on the old encoder is told it was superseded.
func TestSupersededEncoderIsNoFailure(t *testing.T) {
	clip := longClip(t)
	m := New(Config{Root: t.TempDir()})
	defer m.Close()
	a := Stream{SessionID: uid.N(1), Key: "0-0", Job: convertJob(clip)}
	b := a
	b.Key = "0-1"
	e, err := m.start(a, 0)
	must(t, err)
	done := make(chan error)
	go func() {
		_, err := m.wait(context.Background(), e, SegmentName(19))
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	_, err = m.start(b, 0)
	must(t, err)
	if err := <-done; err == nil || errors.Is(err, ErrEncoder) {
		t.Fatalf("got %v", err)
	}
}

// The pacing loop asks for the next missing segment ten times a second; it
// looks only past the last segment it already found, which stays on disk
// for the encoder's life.
func TestNextLooksPastSegmentsAlreadyFound(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		must(t, os.WriteFile(filepath.Join(dir, SegmentName(i)), nil, 0o600))
	}
	e := &encoder{dir: dir}
	if n := e.next(); n != 5 {
		t.Fatal(n)
	}
	for i := range 5 {
		must(t, os.Remove(filepath.Join(dir, SegmentName(i))))
	}
	must(t, os.WriteFile(filepath.Join(dir, SegmentName(5)), nil, 0o600))
	if n := e.next(); n != 6 {
		t.Fatalf("rescanned from the start: %d", n)
	}
}

// fragment is a keyframe file of one empty fMP4 fragment decoding at dt.
func fragment(dt uint64) []byte {
	box := func(t string, body []byte) []byte {
		return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(body))), append([]byte(t), body...)...) //nolint:gosec // a few bytes
	}
	tfdt := box("tfdt", binary.BigEndian.AppendUint64([]byte{1, 0, 0, 0}, dt))
	return append(box("moof", box("traf", tfdt)), box("mdat", nil)...)
}

// Matroska stores no decode times, so ffmpeg's guess puts the keyframe file
// that starts a bound a frame or more either side of it.
func TestKeyframeFileDecodingBeforeItsBoundStartsItsSegment(t *testing.T) {
	dir := t.TempDir()
	const scale = 16000
	for i, at := range []float64{343.515, 348.394, 358.238, 363.368} {
		must(t, os.WriteFile(filepath.Join(dir, gopName(i, false)), fragment(uint64((lead+at)*scale)), 0o600))
	}
	bounds := append(make([]Bound, 55), Bound{343.515}, Bound{348.27}, Bound{358.28}, Bound{363.243})
	e := &encoder{dir: dir, start: 55, seg: 55, scale: scale, bounds: bounds}
	e.assemble()
	for n, gops := range map[int]int{55: 1, 56: 1, 57: 1} {
		b, err := os.ReadFile(filepath.Join(dir, SegmentName(n))) //nolint:gosec // test temp path
		if err != nil {
			t.Fatalf("segment %d: %v", n, err)
		}
		if got := bytes.Count(b, []byte("moof")); got != gops {
			t.Fatalf("segment %d holds %d keyframe files, want %d", n, got, gops)
		}
	}
}

// The run pauses once it has written more than ahead segments past the
// newest request, and not one sooner.
func TestEncoderLeadsOnlyPastItsAhead(t *testing.T) {
	e := &encoder{dir: t.TempDir(), begun: time.Now().Add(-time.Minute), newest: 2, ahead: 3}
	for n := 0; n <= 5; n++ {
		must(t, os.WriteFile(filepath.Join(e.dir, SegmentName(n)), nil, 0o600))
	}
	if e.leadsLocked() {
		t.Fatal("paused with segment 5 written, 3 past a request for 2")
	}
	must(t, os.WriteFile(filepath.Join(e.dir, SegmentName(6)), nil, 0o600))
	if !e.leadsLocked() {
		t.Fatal("still running with segment 6 written, 4 past a request for 2")
	}
	e.newest = 3
	if e.leadsLocked() {
		t.Fatal("paused after the player asked for the next segment")
	}
}

// A stream's own read-ahead moves only where its encoder pauses. Left alone
// it stops that far past the newest request; a seek short of that distance
// but past Reach still restarts the encoder there instead of waiting for it.
func TestStreamAheadPausesFartherAndRestartsAsNear(t *testing.T) {
	clip := longClip(t)
	m := New(Config{Root: t.TempDir(), Ahead: 1, Reach: 1})
	defer m.Close()
	ctx := context.Background()
	st := Stream{SessionID: uid.N(1), Key: "0-0", Ahead: 8, Job: convertJob(clip)}
	p, err := m.Segment(ctx, st, 0)
	must(t, err)
	dir := filepath.Dir(p)
	e := m.encoders[uid.N(1)]
	paused := func() bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.paused
	}
	for end := time.Now().Add(20 * time.Second); !paused(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(end) || e.exited() {
			t.Fatalf("the encoder never paused; produced up to %d", produced(dir))
		}
	}
	// Pacing polls, so a fast encoder overshoots a little; it must still
	// stop short of the 20 segments in the clip.
	next := e.next()
	if next < 10 || next > 18 {
		t.Fatalf("paused with %d segments written past a playhead at 0 with ahead 8", next)
	}
	if _, err := m.Segment(ctx, st, next+3); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(dir, SegmentName(next))) {
		t.Fatalf("a request 3 past the encoder with reach 1 waited for segment %d instead of restarting past it", next)
	}
}

// written is an encoder whose run, begun a minute ago, has written segments
// 0 to last.
func written(t *testing.T, last int) *encoder {
	t.Helper()
	e := &encoder{dir: t.TempDir(), begun: time.Now().Add(-time.Minute), reach: 5}
	for n := 0; n <= last; n++ {
		must(t, os.WriteFile(filepath.Join(e.dir, SegmentName(n)), nil, 0o600))
	}
	return e
}

// A player holding its whole buffer goal has fetched ahead segments past
// its playhead; the run pauses that far past the playhead, not past the
// fetch.
func TestEncoderPausesAheadOfAReportedPlayhead(t *testing.T) {
	e := written(t, 15)
	e.newest, e.ahead, e.head, e.heard = 10, 10, 0, time.Now()
	if e.leadsLocked() {
		t.Fatal("paused with segment 15, Reach past the newest request")
	}
	must(t, os.WriteFile(filepath.Join(e.dir, SegmentName(16)), nil, 0o600))
	if !e.leadsLocked() {
		t.Fatal("still running with segment 16 written, past both the playhead's read-ahead and the newest request's reach")
	}
}

// After a skip back the reported playhead trails the newest request by more
// than the read-ahead; the run still keeps Reach past what the player
// fetched, so fetching forward again never waits on a paused encoder.
func TestEncoderKeepsReachPastTheNewestRequestAfterASkipBack(t *testing.T) {
	e := written(t, 155)
	e.newest, e.ahead, e.head, e.heard = 80, 50, 80, time.Now()
	if !e.leadsLocked() {
		t.Fatal("running with 155 written for a playhead and request at 80")
	}
	e.newest = 151
	if e.leadsLocked() {
		t.Fatal("paused with 155 written, short of Reach past a request for 151")
	}
}

// With no playhead heard, or none for a few missed reports, the run paces
// from the newest request, as for a player that does not report.
func TestEncoderPacesFromTheNewestRequestWithoutAFreshPlayhead(t *testing.T) {
	for name, heard := range map[string]time.Time{"none": {}, "stale": time.Now().Add(-headStale - time.Second)} {
		e := written(t, 20)
		e.newest, e.ahead, e.head, e.heard = 10, 10, 0, heard
		if e.leadsLocked() {
			t.Fatalf("%s: paused with 20 written, ahead past a request for 10", name)
		}
	}
}

// A reported playhead trims behind itself: a 10-second skip back, and
// keepBehind segments in all, stay on disk. A request behind it, as after a
// seek back the player has not yet reported, trims behind the request.
func TestTrimmingFollowsTheReportedPlayhead(t *testing.T) {
	e := written(t, 100)
	e.head, e.heard = 90, time.Now()
	e.request(95, 50, 50)
	if !exists(filepath.Join(e.dir, SegmentName(90-keepBehind))) || !exists(filepath.Join(e.dir, SegmentName(88))) {
		t.Fatal("a segment within keepBehind of the playhead is gone")
	}
	if exists(filepath.Join(e.dir, SegmentName(90-keepBehind-1))) {
		t.Fatal("a segment more than keepBehind behind the playhead stays")
	}
	e = written(t, 100)
	e.head, e.heard = 90, time.Now()
	e.request(40, 50, 50)
	if !exists(filepath.Join(e.dir, SegmentName(40-keepBehind))) {
		t.Fatal("a seek back's segments were trimmed behind a playhead not yet reported there")
	}
}

// A lower transcode limit takes no slot back; it only refuses new ones until
// the sessions holding slots are under it.
func TestLoweringTheTranscodeLimitKeepsRunningTranscodes(t *testing.T) {
	m := New(Config{Root: t.TempDir(), MaxVideo: 3})
	for n := 1; n <= 3; n++ {
		if err := m.Acquire(uid.N(n)); err != nil {
			t.Fatal(err)
		}
	}
	m.SetLimits(Limits{CacheCap: 20_000_000_000, MaxVideo: 1})
	if m.Limits().MaxVideo != 1 || !slices.Equal(m.Transcoding(), []uid.ID{uid.N(1), uid.N(2), uid.N(3)}) {
		t.Fatalf("%+v %v", m.Limits(), m.Transcoding())
	}
	if err := m.Acquire(uid.N(2)); err != nil {
		t.Fatal("a session keeps the slot it holds")
	}
	m.Release(uid.N(1))
	if err := m.Acquire(uid.N(4)); !errors.Is(err, ErrBusy) {
		t.Fatalf("two running at a limit of one: %v", err)
	}
	m.Release(uid.N(2))
	m.Release(uid.N(3))
	if err := m.Acquire(uid.N(4)); err != nil {
		t.Fatal(err)
	}
}

// A higher transcode limit lets the next request that was refused start.
func TestRaisingTheTranscodeLimitLetsAWaitingTranscodeStart(t *testing.T) {
	m := New(Config{Root: t.TempDir(), MaxVideo: 1})
	if err := m.Acquire(uid.N(1)); err != nil {
		t.Fatal(err)
	}
	if err := m.Acquire(uid.N(2)); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	m.SetLimits(Limits{CacheCap: 20_000_000_000, MaxVideo: 2})
	if err := m.Acquire(uid.N(2)); err != nil {
		t.Fatal(err)
	}
}

// A lower cache cap prunes idle sessions at once.
func TestLoweringTheCacheCapPrunes(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{uid.N(7).String(), uid.N(8).String()} {
		_ = os.MkdirAll(filepath.Join(root, d, "0-0"), 0o750)
		_ = os.WriteFile(filepath.Join(root, d, "0-0", "0.m4s"), make([]byte, 1000), 0o600)
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(root, uid.N(7).String()), old, old)
	m := New(Config{Root: root})
	m.SetLimits(Limits{CacheCap: 1500, MaxVideo: 3})
	if m.Limits().CacheCap != 1500 {
		t.Fatalf("%+v", m.Limits())
	}
	if _, err := os.Stat(filepath.Join(root, uid.N(7).String())); !os.IsNotExist(err) {
		t.Fatal("the oldest idle session goes")
	}
	if _, err := os.Stat(filepath.Join(root, uid.N(8).String())); err != nil {
		t.Fatal("pruning stops once under the cap")
	}
}
