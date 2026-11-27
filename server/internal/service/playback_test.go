package service_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
)

// played is a VC-1 film only a video transcode plays on a device of one
// adult: sessions, snapshot and services.
type played struct {
	s    *service.Services
	snap playback.Snapshot
	sess func() *session.Session
}

func newPlayed(t *testing.T, maxVideo int) played {
	t.Helper()
	s := service.New(dbtest.Open(t))
	s.Playback.Engine = transcode.New(transcode.Config{Root: t.TempDir(), MaxVideo: maxVideo})
	a, h := adultWithHousehold(t, s, "a@example.test")
	_, dev, _ := s.Devices.Register(a.ID, h.ID, "Laptop", "web")
	l := &library.Library{Name: "m", Kind: library.Movies}
	_ = s.Libraries.Repo().Create(l, nil)
	it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "m"}
	_ = s.Items.Repo().Create(it)
	ms := int64(600000)
	f := &mediafile.File{ItemID: &it.ID, Path: "/m.mkv", Container: "mkv", DurationMs: &ms}
	_ = s.MediaFiles.Repo().CreateFile(f)
	w, hgt := 1920, 1080
	_ = s.MediaFiles.Repo().ReplaceTracks(f.ID, []*mediafile.Track{{Kind: mediafile.Video, Codec: "vc1", Width: &w, Height: &hgt}})
	snap := playback.Snapshot{Protocols: []string{"hls"}, Video: []playback.VideoDecoder{{Codec: "h264", Hardware: true, MaxWidth: 1920, MaxHeight: 1080}}}
	return played{s, snap, func() *session.Session {
		return &session.Session{DeviceID: dev.ID, AccountID: a.ID, ItemID: it.ID, MediaFileID: f.ID}
	}}
}

func TestReapEndsAbandonedSessionsAndFreesSlots(t *testing.T) {
	p := newPlayed(t, 1)
	s, snap := p.s, p.snap
	sess := p.sess()
	if err := s.Playback.Start(sess, snap, playback.Selection{}); err != nil {
		t.Fatal(err)
	}
	if got := s.Playback.Engine.Transcoding(); len(got) != 1 {
		t.Fatalf("a video transcode holds a slot: %v", got)
	}
	if err := s.Playback.Reap(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Sessions.Repo().Get(sess.ID); g.EndedAt != nil {
		t.Fatal("a fresh session survives")
	}
	if err := s.Playback.Reap(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Sessions.Repo().Get(sess.ID); g.EndedAt == nil || len(s.Playback.Engine.Transcoding()) != 0 {
		t.Fatal("a stale session ends and frees its slot")
	}
}

// A session whose row went with its device frees its slot at the next reap.
func TestReapFreesSessionsWhoseRowIsGone(t *testing.T) {
	p := newPlayed(t, 1)
	sess := p.sess()
	if err := p.s.Playback.Start(sess, p.snap, playback.Selection{}); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Devices.Repo().Delete(sess.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Playback.Reap(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, live := p.s.Playback.Live(sess.ID); live || len(p.s.Playback.Engine.Transcoding()) != 0 {
		t.Fatalf("slots %v, live %v", p.s.Playback.Engine.Transcoding(), live)
	}
}

// A session from before a restart has no live state; its player resuming
// starts an encoder all the same, which goes with its segments once the
// row is gone.
func TestReapFreesAnEncoderOfASessionFromBeforeARestart(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	p := newPlayed(t, 1)
	root := t.TempDir()
	p.s.Playback.Engine = transcode.New(transcode.Config{Root: root})
	sess := p.sess()
	plan := []*session.PlanEntry{{Verdict: session.Remux, VideoAction: session.ActionCopy, AudioAction: session.ActionCopy, SubtitleAction: session.SubtitleNone}}
	if err := p.s.Sessions.Repo().Create(sess, plan); err != nil {
		t.Fatal(err)
	}
	st := transcode.Stream{SessionID: sess.ID, Key: "0-0", Job: transcode.Job{Input: "/m.mkv", Entry: plan[0]}}
	_, _ = p.s.Playback.Engine.Segment(context.Background(), st, 0)
	dir := filepath.Join(root, sess.ID.String())
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Devices.Repo().Delete(sess.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Playback.Reap(time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the segments stay: %v", err)
	}
}

// A host without a hardware encoder plans its conversions in software from
// the start, so no session begins on an encoder that cannot run.
func TestPlanOnAHostWithoutHardwareConvertsInSoftware(t *testing.T) {
	p := newPlayed(t, 1)
	encoders := func() []string {
		t.Helper()
		plan, err := p.s.Playback.Plan(p.sess().MediaFileID, p.snap, playback.Selection{})
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range plan {
			out = append(out, e.Encoder)
		}
		return out
	}
	p.s.Playback.Hardware = true
	if got := encoders(); !slices.Equal(got, []string{session.EncoderHardware, session.EncoderSoftware}) {
		t.Fatalf("with hardware: %v", got)
	}
	p.s.Playback.Hardware = false
	if got := encoders(); !slices.Equal(got, []string{session.EncoderSoftware}) {
		t.Fatalf("without hardware: %v", got)
	}
}

// Every request waiting on an encoder that died reports it; only the first
// report moves the session, the rest find it already moved.
func TestRacingServerFailuresMoveOnce(t *testing.T) {
	p := newPlayed(t, 3)
	p.s.Playback.Hardware = true // for a plan of two entries
	sess := p.sess()
	if err := p.s.Playback.Start(sess, p.snap, playback.Selection{}); err != nil {
		t.Fatal(err)
	}
	plan, _ := p.s.Sessions.Repo().Plan(sess.ID)
	if len(plan) < 2 {
		t.Fatalf("need two entries: %d", len(plan))
	}
	a, b := *sess, *sess
	p.s.Playback.ServerFailed(&a, 0, errors.New("ffmpeg exited"))
	p.s.Playback.ServerFailed(&b, 0, errors.New("ffmpeg exited"))
	g, _ := p.s.Sessions.Repo().Get(sess.ID)
	plan, _ = p.s.Sessions.Repo().Plan(sess.ID)
	if g.CurrentEntry != 1 || !plan[0].Bad || plan[1].Bad {
		t.Fatalf("current %d, %+v %+v", g.CurrentEntry, plan[0], plan[1])
	}
}

// A server failure on the plan's last entry ends the session and frees its
// slot, as an ordinary end does.
func TestServerFailureOnTheLastEntryEndsTheSession(t *testing.T) {
	p := newPlayed(t, 1)
	sess := p.sess()
	if err := p.s.Playback.Start(sess, p.snap, playback.Selection{}); err != nil {
		t.Fatal(err)
	}
	plan, _ := p.s.Sessions.Repo().Plan(sess.ID)
	for i := sess.CurrentEntry; i < len(plan); i++ {
		p.s.Playback.ServerFailed(sess, i, errors.New("ffmpeg exited"))
	}
	if g, _ := p.s.Sessions.Repo().Get(sess.ID); g.EndedAt == nil {
		t.Fatal("the session is still open")
	}
	if got := p.s.Playback.Engine.Transcoding(); len(got) != 0 {
		t.Fatalf("slots %v", got)
	}
	if _, _, live := p.s.Playback.Live(sess.ID); live {
		t.Fatal("the session's live state survived")
	}
}

// A device plays one stream: starting another ends the first and hands its
// transcode slot to the new one.
func TestStartEndsTheDevicesOtherSessions(t *testing.T) {
	p := newPlayed(t, 1)
	first, second := p.sess(), p.sess()
	if err := p.s.Playback.Start(first, p.snap, playback.Selection{}); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Playback.Start(second, p.snap, playback.Selection{}); err != nil {
		t.Fatal(err)
	}
	if g, _ := p.s.Sessions.Repo().Get(first.ID); g.EndedAt == nil {
		t.Fatal("the first session is still open")
	}
	if got := p.s.Playback.Engine.Transcoding(); len(got) != 1 || got[0] != second.ID {
		t.Fatalf("slots %v", got)
	}
	if _, _, live := p.s.Playback.Live(first.ID); live {
		t.Fatal("the first session's live state survived")
	}
}

// A copy of unknown length the client cannot be handed loses to the
// edition's other copy, even one that costs more to play.
func TestBestPassesOverACopyItCouldOnlyStreamWithoutALength(t *testing.T) {
	p := newPlayed(t, 1)
	sess := p.sess()
	known, err := p.s.MediaFiles.Repo().File(sess.MediaFileID)
	if err != nil {
		t.Fatal(err)
	}
	unknown := &mediafile.File{ItemID: &sess.ItemID, Path: "/a.mkv", Container: "mkv"}
	if err := p.s.MediaFiles.Repo().CreateFile(unknown); err != nil {
		t.Fatal(err)
	}
	w, h := 1920, 1080
	if err := p.s.MediaFiles.Repo().ReplaceTracks(unknown.ID, []*mediafile.Track{{Kind: mediafile.Video, Codec: "h264", Width: &w, Height: &h}}); err != nil {
		t.Fatal(err)
	}
	if plan, err := p.s.Playback.Plan(known.ID, p.snap, playback.Selection{}); err != nil || plan[0].Verdict != session.VideoTranscode {
		t.Fatalf("the known copy: %+v %v", plan, err)
	}
	if f, err := p.s.Playback.Best([]*mediafile.File{unknown, known}, p.snap, playback.Selection{}); err != nil || f.ID != known.ID {
		t.Fatalf("got %+v %v, want file %s", f, err, known.ID)
	}
}

// A file of unknown length is only handed over: a stream of it would have
// no segments to list.
func TestAFileOfUnknownLengthIsOnlyHandedOver(t *testing.T) {
	p := newPlayed(t, 1)
	f, err := p.s.MediaFiles.Repo().File(p.sess().MediaFileID)
	if err != nil {
		t.Fatal(err)
	}
	f.DurationMs = nil
	if err := p.s.MediaFiles.Repo().UpdateFile(f); err != nil {
		t.Fatal(err)
	}
	if plan, err := p.s.Playback.Plan(f.ID, p.snap, playback.Selection{}); !errors.Is(err, service.ErrNothingPlayable) {
		t.Fatalf("a device that cannot be handed it: %+v %v", plan, err)
	}
	direct := p.snap
	direct.Protocols, direct.Containers, direct.Network.Local = []string{"http", "hls"}, []string{"mkv"}, true
	direct.Video = []playback.VideoDecoder{{Codec: "vc1", Hardware: true, MaxWidth: 1920, MaxHeight: 1080}}
	if plan, err := p.s.Playback.Plan(f.ID, direct, playback.Selection{}); err != nil || len(plan) != 1 || plan[0].Verdict != session.DirectPlay {
		t.Fatalf("a device that can: %+v %v", plan, err)
	}
}
