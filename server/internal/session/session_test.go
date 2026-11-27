package session_test

import (
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/uid"
)

func plan() []*session.PlanEntry {
	return []*session.PlanEntry{
		{Verdict: session.DirectPlay, VideoAction: "copy", AudioAction: "copy", SubtitleAction: "none"},
		{Verdict: session.Remux, VideoAction: "copy", AudioAction: "copy", SubtitleAction: "sidecar"},
		{Verdict: session.VideoTranscode, VideoAction: "convert", AudioAction: "convert", SubtitleAction: "burn"},
	}
}

func setup(t *testing.T) (*session.Repo, *session.Session) {
	d := dbtest.Open(t)
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A", PasswordHash: "x"}
	_ = account.New(d).Create(a, nil)
	h := &household.Household{Name: "h", AdultID: a.ID}
	_ = household.New(d).Create(h, nil)
	dev := &device.Device{TokenHash: "t", Name: "d", Platform: "web", AccountID: a.ID, HouseholdID: h.ID}
	_ = device.New(d).Create(dev)
	l := &library.Library{Name: "m", Kind: library.Movies}
	_ = library.New(d).Create(l, nil)
	it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "m"}
	_ = item.New(d).Create(it)
	f := &mediafile.File{ItemID: &it.ID, Path: "/a.mkv"}
	if err := mediafile.New(d).CreateFile(f); err != nil {
		t.Fatal(err)
	}
	return session.New(d), &session.Session{DeviceID: dev.ID, AccountID: a.ID, ItemID: it.ID, MediaFileID: f.ID}
}

func TestCreatePlanAndPosition(t *testing.T) {
	r, s := setup(t)
	if err := r.Create(s, plan()); err != nil {
		t.Fatal(err)
	}
	p, _ := r.Plan(s.ID)
	if len(p) != 3 || p[0].Position != 0 || p[2].Verdict != session.VideoTranscode || p[1].Bad {
		t.Fatalf("%+v", p)
	}
	at := time.Now().Add(time.Minute).UTC().Truncate(time.Millisecond)
	if err := r.SetPosition(s.ID, 61000, at); err != nil {
		t.Fatal(err)
	}
	g, _ := r.Get(s.ID)
	if g.PositionMs != 61000 || !g.ModifiedAt.Equal(at) || g.EndedAt != nil {
		t.Fatalf("%+v", g)
	}
	if a, _ := r.Active(); len(a) != 1 {
		t.Fatal(len(a))
	}
	if v, err := r.ActiveByDevice(s.DeviceID); err != nil || len(v) != 1 || v[0].ID != s.ID {
		t.Fatal(err)
	}
	if err := r.End(s.ID, at); err != nil {
		t.Fatal(err)
	}
	if err := r.End(s.ID, at); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("double end: %v", err)
	}
	if err := r.SetPosition(s.ID, 1, at); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("position on ended session: %v", err)
	}
	if v, err := r.ActiveByDevice(s.DeviceID); err != nil || len(v) != 0 {
		t.Fatal(err)
	}
	if l, _ := r.List(10, 0); len(l) != 1 {
		t.Fatal(len(l))
	}
}

func TestCreateRollsBackOnBadPlan(t *testing.T) {
	r, s := setup(t)
	bad := []*session.PlanEntry{{Verdict: "warp", VideoAction: "copy", AudioAction: "copy", SubtitleAction: "none"}}
	if err := r.Create(s, bad); err == nil {
		t.Fatal("bad verdict accepted")
	}
	if l, _ := r.List(10, 0); len(l) != 0 {
		t.Fatal("session row survived a failed plan insert")
	}
	s.ItemID = uid.N(999)
	if err := r.Create(s, plan()); !errors.Is(err, db.ErrReference) {
		t.Fatal(err)
	}
}

func TestFailEntryFallsDownThePlan(t *testing.T) {
	r, s := setup(t)
	_ = r.Create(s, plan())
	n, err := r.FailEntry(s.ID, 0, "client refused hevc")
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, err = r.FailEntry(s.ID, 1, "ffmpeg error"); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, err = r.FailEntry(s.ID, 2, "busy"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("exhausted plan: %v", err)
	}
	p, _ := r.Plan(s.ID)
	if !p[0].Bad || p[0].BadReason != "client refused hevc" || !p[2].Bad {
		t.Fatalf("%+v", p)
	}
	if err := r.ReplacePlan(s.ID, plan()[:1]); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Plan(s.ID); len(p) != 1 || p[0].Bad {
		t.Fatalf("%+v", p)
	}
	if g, _ := r.Get(s.ID); g.CurrentEntry != 0 {
		t.Fatal("replace should reset to first entry")
	}
	if _, err := r.FailEntry(uid.N(999), 0, "x"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
}

// Failures reported against an entry the session already left, as racing
// segment requests on one dead encoder all do, change nothing.
func TestFailEntryIgnoresAnEntryAlreadyLeft(t *testing.T) {
	r, s := setup(t)
	_ = r.Create(s, plan())
	if n, err := r.FailEntry(s.ID, 0, "ffmpeg exited"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	n, err := r.FailEntry(s.ID, 0, "ffmpeg exited")
	if !errors.Is(err, session.ErrMoved) || n != 1 {
		t.Fatal(n, err)
	}
	p, _ := r.Plan(s.ID)
	if g, _ := r.Get(s.ID); g.CurrentEntry != 1 || p[1].Bad {
		t.Fatalf("%d %+v", g.CurrentEntry, p[1])
	}
}

func TestDeleteDropsThePlan(t *testing.T) {
	r, s := setup(t)
	_ = r.Create(s, plan())
	if err := r.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ := r.Plan(s.ID); len(p) != 0 {
		t.Fatal("plan should cascade")
	}
}

func TestEncoderFieldsAndStreamKey(t *testing.T) {
	r, s := setup(t)
	s.StreamKey = "k1"
	e := &session.PlanEntry{Verdict: session.VideoTranscode, VideoAction: "convert", AudioAction: "convert", SubtitleAction: "burn",
		AudioChannels: 2, VideoCodec: "h264", Encoder: session.EncoderHardware, ToneMap: true, TopHeight: 720, TopBitrate: 4000000, Floor: true, TS: true}
	if err := r.Create(s, []*session.PlanEntry{e}); err != nil {
		t.Fatal(err)
	}
	p, _ := r.Plan(s.ID)
	got := *p[0]
	e.SessionID = s.ID
	if got != *e {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, *e)
	}
	g, err := r.ByStreamKey("k1")
	if err != nil || g.ID != s.ID || g.StreamKey != "k1" {
		t.Fatal(g, err)
	}
	if _, err := r.ByStreamKey("nope"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal(err)
	}
	if err := r.SetStreamKey(s.ID, "k2"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetProgram(s.ID, s.ItemID, s.MediaFileID); err != nil {
		t.Fatal(err)
	}
	if err := r.SetProgram(s.ID, s.ItemID, uid.N(999)); !errors.Is(err, db.ErrReference) {
		t.Fatalf("a file that does not exist: %v", err)
	}
	if _, err := r.ByStreamKey("k1"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("a replaced key stops working")
	}
	if _, err := r.ByStreamKey("k2"); err != nil {
		t.Fatal(err)
	}
	_ = r.End(s.ID, time.Now())
	if _, err := r.ByStreamKey("k2"); !errors.Is(err, db.ErrNotFound) {
		t.Fatal("an ended session's stream key must stop working")
	}
}

// Only the device's reports keep a session from being reaped: the server
// changing its key, program or plan entry leaves its last report as it was.
func TestServerChangesLeaveTheLastReport(t *testing.T) {
	r, s := setup(t)
	if err := r.Create(s, plan()); err != nil {
		t.Fatal(err)
	}
	reported := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	if err := r.SetPosition(s.ID, 1000, reported); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func() error{
		"stream key": func() error { return r.SetStreamKey(s.ID, "k2") },
		"program":    func() error { return r.SetProgram(s.ID, s.ItemID, s.MediaFileID) },
		"fall back":  func() error { _, err := r.FailEntry(s.ID, 0, "ffmpeg exited"); return err },
		"new plan":   func() error { return r.ReplacePlan(s.ID, plan()) },
	} {
		if err := change(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if g, _ := r.Get(s.ID); !g.ModifiedAt.Equal(reported) {
			t.Errorf("%s moved the last report to %v", name, g.ModifiedAt)
		}
	}
}

func TestThroughputSamples(t *testing.T) {
	r, s := setup(t)
	_ = r.Create(s, plan())
	at := time.Now().UTC().Truncate(time.Millisecond)
	rung := 1
	height, bitrate := 720, int64(4e6)
	in := &session.Sample{SessionID: s.ID, At: at, MeasuredBitrate: 8e6, BufferMs: 12000, StallCount: 1, CurrentRung: &rung, RungHeight: &height, RungBitrate: &bitrate}
	if err := r.AddSample(in); err != nil || in.ID.IsZero() {
		t.Fatal(err)
	}
	if err := r.AddSample(&session.Sample{SessionID: s.ID, At: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Samples(s.ID, 100)
	if err != nil || len(out) != 2 || !out[0].At.Equal(at) || *out[0].CurrentRung != 1 || out[0].MeasuredBitrate != 8e6 ||
		*out[0].RungHeight != 720 || *out[0].RungBitrate != 4e6 || out[1].RungHeight != nil || out[1].RungBitrate != nil {
		t.Fatal(out, err)
	}
	_ = r.Delete(s.ID)
	if out, _ := r.Samples(s.ID, 100); len(out) != 0 {
		t.Fatal("samples should cascade")
	}
}

// Telemetry serves only the live slow loop, so a session's samples go when
// it ends; the session and its plan stay as history.
func TestEndDropsTelemetry(t *testing.T) {
	r, s := setup(t)
	_ = r.Create(s, plan())
	_ = r.AddSample(&session.Sample{SessionID: s.ID, At: time.Now().UTC()})
	if err := r.End(s.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if out, _ := r.Samples(s.ID, 100); len(out) != 0 {
		t.Fatalf("%d samples kept", len(out))
	}
	if p, _ := r.Plan(s.ID); len(p) != 3 {
		t.Fatal("plan dropped")
	}
}

// The slow loop reads only the newest few samples, oldest of them first.
func TestLastSamples(t *testing.T) {
	r, s := setup(t)
	_ = r.Create(s, plan())
	at := time.Now().UTC().Truncate(time.Millisecond)
	for i := range 5 {
		_ = r.AddSample(&session.Sample{SessionID: s.ID, At: at.Add(time.Duration(i) * time.Second), StallCount: i})
	}
	out, err := r.Samples(s.ID, 3)
	if err != nil || len(out) != 3 || out[0].StallCount != 2 || out[2].StallCount != 4 {
		t.Fatal(out, err)
	}
}
