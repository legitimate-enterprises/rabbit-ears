package download_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/account"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/device"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/download"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/household"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

type world struct {
	d       *sql.DB
	r       *download.Repo
	adult   int64
	kid     int64
	device  int64
	movie   int64
	file    *mediafile.File
	devices *device.Repo
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) world {
	t.Helper()
	d := dbtest.Open(t)
	a := &account.Account{Kind: account.Adult, Email: "a@example.test", Name: "A"}
	must(t, account.New(d).Create(a))
	h := &household.Household{Name: "H", AdultID: a.ID}
	must(t, household.New(d).Create(h))
	k := &account.Account{Kind: account.Kid, Name: "K", HouseholdID: &h.ID, MovieCeiling: "PG"}
	must(t, account.New(d).Create(k))
	dev := &device.Device{TokenHash: "x", Name: "Phone", Platform: "ios", AccountID: a.ID, HouseholdID: h.ID}
	must(t, device.New(d).Create(dev))
	l := &library.Library{Name: "Movies", Kind: library.Movies}
	must(t, library.New(d).Create(l))
	m := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: "Heat"}
	must(t, item.New(d).Create(m))
	f := &mediafile.File{ItemID: m.ID, Path: "/m/heat.mkv", SizeBytes: 10, ModifiedAt: time.UnixMilli(1000).UTC(), Container: "mkv"}
	must(t, mediafile.New(d).CreateFile(f))
	return world{d, download.New(d), a.ID, k.ID, dev.ID, m.ID, f, device.New(d)}
}

func (w world) conversion(height int) *download.Conversion {
	return &download.Conversion{MediaFileID: w.file.ID, SourceModifiedAt: w.file.ModifiedAt, Container: "mp4", PredictedBytes: 99,
		Entry: &session.PlanEntry{Verdict: session.VideoTranscode, VideoAction: "convert", AudioAction: "copy", VideoCodec: "h264", TopHeight: height, TopBitrate: 4e6,
			AudioTrackID: new(int64(7))}}
}

func TestRequestSharesOneConversionAndRequeuesAFailedOne(t *testing.T) {
	w := setup(t)
	a, b, other := w.conversion(720), w.conversion(720), w.conversion(480)
	for _, c := range []*download.Conversion{a, b, other} {
		must(t, w.r.Request(c))
	}
	if a.ID == 0 || b.ID != a.ID || other.ID == a.ID || a.Status != download.Queued {
		t.Fatalf("%+v %+v %+v", a, b, other)
	}
	stored, err := w.r.Conversion(a.ID)
	must(t, err)
	if stored.Entry.TopHeight != 720 || *stored.Entry.AudioTrackID != 7 || stored.Entry.VideoCodec != "h264" {
		t.Fatalf("as stored: %+v %+v", stored, stored.Entry)
	}
	now := time.Now().UTC()
	must(t, w.r.Start(a.ID, now))
	must(t, w.r.Fail(a.ID, "encoder failed", now))
	again := w.conversion(720)
	must(t, w.r.Request(again))
	if again.ID != a.ID || again.Status != download.Queued || again.Error != "" {
		t.Fatalf("asked again, a failed conversion queues again: %+v", again)
	}
	must(t, w.r.Start(a.ID, now))
	must(t, w.r.Finish(a.ID, 1234, now))
	got, err := w.r.Conversion(a.ID)
	must(t, err)
	if got.Status != download.Ready || *got.SizeBytes != 1234 || got.FinishedAt == nil {
		t.Fatalf("%+v", got)
	}
	changed := w.conversion(720)
	changed.SourceModifiedAt = changed.SourceModifiedAt.Add(time.Hour)
	must(t, w.r.Request(changed))
	if changed.ID == a.ID {
		t.Fatal("a source modified since is another conversion")
	}
}

func TestRequeueAfterRestart(t *testing.T) {
	w := setup(t)
	c := w.conversion(720)
	must(t, w.r.Request(c))
	must(t, w.r.Create(&download.Download{DeviceID: w.device, AccountID: w.adult, ItemID: w.movie, MediaFileID: w.file.ID, ConversionID: &c.ID,
		Quality: "720p"}))
	must(t, w.r.Start(c.ID, time.Now()))
	if turns, err := w.r.Turns(); err != nil || len(turns) != 0 {
		t.Fatalf("a converting one is no turn: %+v %v", turns, err)
	}
	must(t, w.r.Requeue())
	if turns, err := w.r.Turns(); err != nil || len(turns) != 1 || turns[0].ConversionID != c.ID || turns[0].DeviceID != w.device {
		t.Fatalf("%+v %v", turns, err)
	}
}

func TestDownloadsOfADeviceAndProfile(t *testing.T) {
	w := setup(t)
	c := w.conversion(720)
	must(t, w.r.Request(c))
	mine := &download.Download{DeviceID: w.device, AccountID: w.adult, ItemID: w.movie, MediaFileID: w.file.ID, ConversionID: &c.ID, Quality: "720p"}
	must(t, w.r.Create(mine))
	dup := *mine
	dup.ID, dup.Quality = 0, "1080p"
	found, err := w.r.Find(&dup)
	if err != nil || found.ID != mine.ID || found.Quality != "720p" {
		t.Fatalf("the same claim is found: %+v %v", found, err)
	}
	orig := &download.Download{DeviceID: w.device, AccountID: w.adult, ItemID: w.movie, MediaFileID: w.file.ID, Quality: "original"}
	if _, err := w.r.Find(orig); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("the file itself is another claim: %v", err)
	}
	must(t, w.r.Create(orig))
	kids := &download.Download{DeviceID: w.device, AccountID: w.kid, ItemID: w.movie, MediaFileID: w.file.ID, Quality: "original"}
	must(t, w.r.Create(kids))
	got, err := w.r.List(w.device, w.adult, item.Audience{}, 10, 0)
	if err != nil || len(got) != 2 || got[0].ID != mine.ID || got[1].ID != orig.ID {
		t.Fatalf("a profile's own, oldest first: %+v %v", got, err)
	}
	kid := item.Audience{Kid: true, AccountID: w.kid, Movie: []string{"G", "PG"}}
	if got, err := w.r.List(w.device, w.kid, kid, 10, 0); err != nil || len(got) != 0 {
		t.Fatalf("an unrated movie is no kid's: %+v %v", got, err)
	}
	if _, err := w.r.Of(kids.ID, w.device, w.kid, kid); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("absent for the kid: %v", err)
	}
	if v, err := w.r.Of(orig.ID, w.device, w.adult, item.Audience{}); err != nil || v.ID != orig.ID {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := w.r.Of(orig.ID, w.device+1, w.adult, item.Audience{}); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("another device's is absent: %v", err)
	}
	if n, err := w.r.Count(c.ID); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestExpireAndUnneeded(t *testing.T) {
	w := setup(t)
	pending, done := w.conversion(720), w.conversion(480)
	must(t, w.r.Request(pending))
	must(t, w.r.Request(done))
	long := time.Now().UTC().Add(-96 * time.Hour)
	must(t, w.r.Start(done.ID, long))
	must(t, w.r.Finish(done.ID, 1, long))
	claim := func(c *int64, at time.Time) *download.Download {
		v := &download.Download{DeviceID: w.device, AccountID: w.adult, ItemID: w.movie, MediaFileID: w.file.ID, ConversionID: c, Quality: "x", CreatedAt: at}
		must(t, w.r.Create(v))
		return v
	}
	waiting := claim(&pending.ID, long)
	fetchedLong := claim(&done.ID, long)
	fresh := claim(&done.ID, time.Now().UTC())
	original := claim(nil, long)
	must(t, w.r.Expire(time.Now().UTC().Add(-72*time.Hour)))
	for _, c := range []struct {
		d    *download.Download
		kept bool
	}{{waiting, true}, {fetchedLong, false}, {fresh, true}, {original, false}} {
		if _, err := w.r.Get(c.d.ID); (err == nil) != c.kept {
			t.Errorf("download %d kept=%v: %v", c.d.ID, c.kept, err)
		}
	}
	if un, err := w.r.Unneeded(); err != nil || len(un) != 0 {
		t.Fatalf("both are still claimed: %+v %v", un, err)
	}
	must(t, w.r.Delete(fresh.ID))
	un, err := w.r.Unneeded()
	if err != nil || len(un) != 1 || un[0].ID != done.ID {
		t.Fatalf("%+v %v", un, err)
	}
	must(t, w.devices.Revoke(w.device, time.Now()))
	must(t, w.r.Expire(time.Now().UTC().Add(-72*time.Hour)))
	if _, err := w.r.Get(waiting.ID); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("a revoked device's downloads go: %v", err)
	}
}
