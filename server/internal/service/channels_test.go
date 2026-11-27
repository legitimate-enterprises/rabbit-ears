package service_test

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

// A session can leave its channel after the playback service found it
// tuned in and before the channel acts on it.
func TestAChannelSessionThatLeftIsNotTuned(t *testing.T) {
	p := newPlayed(t, 1)
	sess := p.sess()
	entry := func() *session.PlanEntry {
		return &session.PlanEntry{Verdict: session.VideoTranscode, VideoAction: session.ActionConvert, AudioAction: session.ActionCopy, SubtitleAction: session.SubtitleNone}
	}
	if err := p.s.Sessions.Repo().Create(sess, []*session.PlanEntry{entry(), entry()}); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Channels.Failed(sess); !errors.Is(err, service.ErrNotTuned) {
		t.Fatalf("failed: %v", err)
	}
	if _, err := p.s.Channels.Changed(sess, p.snap, playback.Selection{}); !errors.Is(err, service.ErrNotTuned) {
		t.Fatalf("changed: %v", err)
	}
}

// A source that converts needs a slot; with every slot held by encoders
// still running it is refused, and nothing is left behind. Sources that
// copy share without one.
func TestAFullEngineRefusesANewConvertingSource(t *testing.T) {
	p := newPlayed(t, 1)
	c, engine := p.s.Channels, p.s.Playback.Engine
	copied, err := c.Listen(uid.N(1), "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := c.Listen(uid.N(2), "copy", false); err != nil || again != copied {
		t.Fatalf("a second listener shares the source: %s %v", again, err)
	}
	must(t, engine.Acquire(uid.N(99)))
	if _, err := c.Listen(uid.N(3), "convert", true); !errors.Is(err, transcode.ErrBusy) {
		t.Fatalf("full engine: %v", err)
	}
	engine.Release(uid.N(99))
	converted, err := c.Listen(uid.N(3), "convert", true)
	if err != nil || len(engine.Transcoding()) != 1 || engine.Transcoding()[0] != converted {
		t.Fatalf("the freed slot goes to the new source: %s %v %v", converted, err, engine.Transcoding())
	}
}

// A reap leaves a channel source's encoder to its listeners, though no
// playback session has its id.
func TestReapKeepsAChannelSourcesEncoder(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil || testing.Short() {
		t.Skip("ffmpeg not installed")
	}
	p := newPlayed(t, 1)
	engine := p.s.Playback.Engine
	src, err := p.s.Channels.Listen(uid.N(1), "copy", false)
	must(t, err)
	entry := &session.PlanEntry{Verdict: session.Remux, VideoAction: session.ActionCopy, AudioAction: session.ActionCopy, SubtitleAction: session.SubtitleNone}
	_, _ = engine.Segment(context.Background(), transcode.Stream{SessionID: src, Key: "0", Job: transcode.Job{Input: "/m.mkv", Entry: entry}}, 0)
	if !slices.Contains(engine.Encoding(), src) {
		t.Fatalf("no encoder for the source: %v", engine.Encoding())
	}
	must(t, p.s.Playback.Reap(time.Now().Add(-time.Hour)))
	if !slices.Contains(engine.Encoding(), src) {
		t.Fatal("the reap stopped a source's encoder")
	}
}
