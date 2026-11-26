package service_test

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/db/dbtest"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/item"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/library"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/mediafile"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playback"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/playlist"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/service"
	"github.com/legitimate-enterprises/rabbit-ears/server/internal/session"
)

// A claim racing the removal of conversions nobody claims, as the worker,
// a release and the hourly clean-up remove them, either finds its
// conversion gone and requests it again or keeps it; it never fails, nor
// goes with the conversion.
func TestAClaimSurvivesARacingRemovalOfUnclaimedConversions(t *testing.T) {
	p := newPlayed(t, 1)
	sess := p.sess()
	f, err := p.s.MediaFiles.Repo().File(sess.MediaFileID)
	if err != nil {
		t.Fatal(err)
	}
	title := &service.DownloadTitle{Row: playlist.Row{ItemID: sess.ItemID}, File: f, Plan: &playback.DownloadPlan{Container: "mp4", Quality: "720p",
		Entry: &session.PlanEntry{Verdict: session.VideoTranscode, VideoAction: session.ActionConvert, AudioAction: session.ActionCopy, VideoCodec: "h264",
			TopHeight: 720, TopBitrate: 4e6, SubtitleAction: session.SubtitleNone, SingleRung: true}}}
	repo := p.s.Downloads.Repo()
	for i := range 200 {
		created, cleaned := make(chan error, 1), make(chan error, 1)
		var wg sync.WaitGroup
		var id int64
		wg.Go(func() {
			d, err := p.s.Downloads.Create(sess.DeviceID, sess.AccountID, title)
			if err == nil {
				id = d.ID
			}
			created <- err
		})
		wg.Go(func() { cleaned <- p.s.Downloads.Clean(time.Now()) })
		wg.Wait()
		if err := <-created; err != nil {
			t.Fatalf("run %d: the claim failed: %v", i, err)
		}
		if err := <-cleaned; err != nil {
			t.Fatalf("run %d: clean-up: %v", i, err)
		}
		d, err := repo.Get(id)
		if err != nil {
			t.Fatalf("run %d: an answered claim went: %v", i, err)
		}
		if err := p.s.Downloads.Release(d); err != nil {
			t.Fatal(err)
		}
	}
}

// Devices with conversions queued take turns, the one served longest ago
// first, and each is served in the order it claimed; a conversion two
// devices claim is a turn for both.
func TestConversionsTakeTurnsByDevice(t *testing.T) {
	s := service.New(dbtest.Open(t))
	a, h := adultWithHousehold(t, s, "a@example.test")
	l := &library.Library{Name: "m", Kind: library.Movies}
	must(t, s.Libraries.Repo().Create(l))
	titles := map[string]*service.DownloadTitle{}
	for _, name := range []string{"A1", "A2", "A3", "A4", "B1", "B2", "C1"} {
		it := &item.Item{LibraryID: l.ID, Kind: item.Movie, ParsedTitle: name}
		must(t, s.Items.Repo().Create(it))
		f := &mediafile.File{ItemID: it.ID, Path: "/" + name + ".mkv", Container: "mkv"}
		must(t, s.MediaFiles.Repo().CreateFile(f))
		titles[name] = &service.DownloadTitle{Row: playlist.Row{ItemID: it.ID}, File: f, Plan: &playback.DownloadPlan{Container: "mp4", Quality: "720p",
			Entry: &session.PlanEntry{Verdict: session.VideoTranscode, VideoAction: session.ActionConvert, AudioAction: session.ActionCopy, VideoCodec: "h264", TopHeight: 720,
				SubtitleAction: session.SubtitleNone, SingleRung: true}}}
	}
	devices := map[string]int64{}
	for _, name := range []string{"A", "B", "C"} {
		_, dev, err := s.Devices.Register(a.ID, h.ID, name, "web")
		must(t, err)
		devices[name] = dev.ID
	}
	claim := func(device string, names ...string) {
		for _, name := range names {
			_, err := s.Downloads.Create(devices[device], a.ID, titles[name])
			must(t, err)
		}
	}
	run := func(n int) string {
		var got []string
		for range n {
			c, err := s.Downloads.Next()
			must(t, err)
			for name, ti := range titles {
				if ti.File.ID == c.MediaFileID {
					got = append(got, name)
				}
			}
			must(t, s.Downloads.Repo().Start(c.ID, time.Now()))
			must(t, s.Downloads.Repo().Finish(c.ID, 1, time.Now()))
		}
		if _, err := s.Downloads.Next(); !errors.Is(err, db.ErrNotFound) {
			t.Fatalf("left queued: %v", err)
		}
		return strings.Join(got, " ")
	}
	claim("A", "A1", "A2", "A3")
	claim("B", "B1")
	if got := run(4); got != "A1 B1 A2 A3" {
		t.Fatalf("ran %s", got)
	}
	// C, never served, goes first; C1 is B's turn too, so A, served before
	// B now, goes next.
	claim("A", "A4")
	claim("B", "B2", "C1")
	claim("C", "C1")
	if got := run(3); got != "C1 A4 B2" {
		t.Fatalf("ran %s", got)
	}
}
