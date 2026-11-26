package service_test

import (
	"errors"
	"testing"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/transcode"
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
	copied, err := c.Listen(1, "copy", false)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := c.Listen(2, "copy", false); err != nil || again != copied {
		t.Fatalf("a second listener shares the source: %d %v", again, err)
	}
	must(t, engine.Acquire(99))
	if _, err := c.Listen(3, "convert", true); !errors.Is(err, transcode.ErrBusy) {
		t.Fatalf("full engine: %v", err)
	}
	engine.Release(99)
	converted, err := c.Listen(3, "convert", true)
	if err != nil || len(engine.Transcoding()) != 1 || engine.Transcoding()[0] != converted {
		t.Fatalf("the freed slot goes to the new source: %d %v %v", converted, err, engine.Transcoding())
	}
}
